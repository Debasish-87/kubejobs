package worker

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	redisClient "github.com/redis/go-redis/v9"

	"kubejobs/internal/config"
	"kubejobs/internal/metrics"
	"kubejobs/internal/redis"
)

const (
	streamName      = "jobs_stream"
	groupName       = "workers"
	retryZSet       = "jobs_retry_zset"
	blockTimeout    = 5 * time.Second
	lockTTL         = 2 * time.Minute
	retryScanPeriod = 2 * time.Second
	maxRetryBackoff = 10
	jobTTL          = 1 * time.Hour

	// How long a message must be idle before it is auto-claimed by recovery.
	// Should be longer than the longest expected job runtime.
	pendingMinIdle = 30 * time.Second

	// How often the in-process recovery goroutine re-scans for stuck messages.
	recoveryInterval = 30 * time.Second
)

// ---------------- CONSUMER GROUP INIT ----------------

func initConsumerGroup(ctx context.Context) {
	err := redis.RDB.XGroupCreateMkStream(ctx, streamName, groupName, "0").Err()

	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		log.Fatalf("event=consumer_group_init_failed err=%v", err)
	}

	log.Println("event=consumer_group_ready stream=jobs_stream group=workers")
}

// ---------------- DISTRIBUTED LOCK ----------------
// Prevents two workers from processing the same job simultaneously.

func acquireLock(ctx context.Context, jobID string) bool {
	lockKey := "job_lock:" + jobID

	ok, err := redis.RDB.SetNX(ctx, lockKey, "1", lockTTL).Result()
	if err != nil {
		log.Printf("event=lock_error job_id=%s err=%v", jobID, err)
		return false
	}
	return ok
}

func releaseLock(ctx context.Context, jobID string) {
	lockKey := "job_lock:" + jobID
	redis.RDB.Del(ctx, lockKey)
}

// ---------------- RETRY SCHEDULING ----------------
// Uses exponential backoff capped at maxRetryBackoff seconds.

func scheduleRetry(ctx context.Context, jobID string, retry int64) {

	if retry > maxRetryBackoff {
		retry = maxRetryBackoff
	}

	base := math.Pow(2, float64(retry))
	delay := time.Duration(base) * time.Second

	retryAt := time.Now().Add(delay).Unix()

	err := redis.RDB.ZAdd(ctx, retryZSet, redisClient.Z{
		Score:  float64(retryAt),
		Member: jobID,
	}).Err()

	if err != nil {
		log.Printf("event=retry_schedule_failed job_id=%s err=%v", jobID, err)
	}
}

// ---------------- RETRY SCHEDULER ----------------
// Runs on a ticker. Looks at the retry ZSET for jobs whose backoff window
// has elapsed and re-enqueues them back into the Redis stream.

func retryScheduler(ctx context.Context) {

	log.Println("event=retry_scheduler_started")

	ticker := time.NewTicker(retryScanPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("event=retry_scheduler_stopped")
			return

		case <-ticker.C:

			// update queue depth metric on every tick
			if length, err := redis.RDB.XLen(ctx, streamName).Result(); err == nil {
				metrics.QueueDepth.WithLabelValues("jobs_stream").Set(float64(length))
			}

			now := time.Now().Unix()

			jobs, err := redis.RDB.ZRangeByScore(ctx, retryZSet, &redisClient.ZRangeBy{
				Min:   "-inf",
				Max:   fmt.Sprintf("%d", now),
				Count: 10,
			}).Result()

			if err != nil {
				log.Printf("event=retry_scan_error err=%v", err)
				continue
			}

			if len(jobs) == 0 {
				continue
			}

			for _, jobID := range jobs {

				jobKey := "job_data:" + jobID

				jobMap, err := redis.RDB.HGetAll(ctx, jobKey).Result()
				if err != nil || len(jobMap) == 0 {
					log.Printf("event=retry_job_missing job_id=%s", jobID)
					redis.RDB.ZRem(ctx, retryZSet, jobID)
					continue
				}

				payload := jobMap["payload"]

				err = redis.RDB.XAdd(ctx, &redisClient.XAddArgs{
					Stream: streamName,
					MaxLen: 10000,
					Values: map[string]interface{}{
						"job_id":  jobID,
						"payload": payload,
					},
				}).Err()

				if err != nil {
					log.Printf("event=retry_requeue_failed job_id=%s err=%v", jobID, err)
					// push back by 5s so we retry the requeue attempt
					redis.RDB.ZAdd(ctx, retryZSet, redisClient.Z{
						Score:  float64(now + 5),
						Member: jobID,
					})
					continue
				}

				// only remove from retry set after successful enqueue
				redis.RDB.ZRem(ctx, retryZSet, jobID)
				log.Printf("event=retry_requeued job_id=%s", jobID)
			}
		}
	}
}

// ---------------- SIMULATED WORK ----------------
// In a real system you would replace this with your actual business logic.

func heavyWork(jobID string) {
	// Use hash to get more varied distribution (0-9 seconds)
	sleep := 2 + (len(jobID)*31+len(jobID))%6
	time.Sleep(time.Duration(sleep) * time.Second)
}

// ---------------- PROCESS JOB ----------------
// processJob handles one message end-to-end.
// It always ACKs the message before returning — callers must NOT ACK again.

func processJob(ctx context.Context, msgID string, values map[string]interface{}) {

	defer func() {
		if r := recover(); r != nil {
			log.Printf("event=process_job_panic msg_id=%s err=%v", msgID, r)
		}
	}()

	// ---------------- VALIDATE MESSAGE ----------------

	jobIDRaw, ok := values["job_id"]
	if !ok {
		log.Printf("event=invalid_message_missing_job_id msg_id=%s", msgID)
		_ = redis.RDB.XAck(ctx, streamName, groupName, msgID).Err()
		return
	}

	jobID, ok := jobIDRaw.(string)
	if !ok {
		log.Printf("event=invalid_message_type msg_id=%s", msgID)
		_ = redis.RDB.XAck(ctx, streamName, groupName, msgID).Err()
		return
	}

	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// ---------------- DISTRIBUTED LOCK ----------------

	if !acquireLock(jobCtx, jobID) {
		log.Printf("event=lock_not_acquired job_id=%s", jobID)
		_ = redis.RDB.XAck(jobCtx, streamName, groupName, msgID).Err()
		return
	}
	defer releaseLock(jobCtx, jobID)

	// ---------------- FETCH JOB STATE ----------------

	jobKey := "job_data:" + jobID

	jobMap, err := redis.RDB.HGetAll(jobCtx, jobKey).Result()
	if err != nil || len(jobMap) == 0 {
		log.Printf("event=job_not_found job_id=%s err=%v", jobID, err)
		_ = redis.RDB.XAck(jobCtx, streamName, groupName, msgID).Err()
		return
	}

	// skip jobs that are already in a terminal state
	// (guards against duplicate delivery from the stream)
	status := jobMap["status"]
	if status != "PENDING" && status != "FAILED" {
		log.Printf("event=job_skipped job_id=%s status=%s", jobID, status)
		_ = redis.RDB.XAck(jobCtx, streamName, groupName, msgID).Err()
		return
	}

	if payloadRaw, exists := values["payload"]; exists {
		log.Printf("event=job_payload job_id=%s payload=%v", jobID, payloadRaw)
	}

	// ---------------- MARK RUNNING ----------------

	now := time.Now().Unix()

	if err := redis.RDB.HSet(jobCtx, jobKey, map[string]interface{}{
		"status":     "RUNNING",
		"started_at": now,
		"worker_id":  config.C.WorkerID,
	}).Err(); err != nil {
		log.Printf("event=redis_hset_failed job_id=%s err=%v", jobID, err)
	}

	start := time.Now()
	log.Printf("event=job_started job_id=%s worker_id=%s", jobID, config.C.WorkerID)

	// ---------------- DO WORK ----------------

	heavyWork(jobID)

	duration := time.Since(start)

	// ---------------- FAILURE DETECTION ----------------
	// Simulate timeout as the failure signal.
	// In real code: check the error returned by heavyWork.

	if duration > 10*time.Second {

		failureReason := "timeout"

		log.Printf("event=job_failed job_id=%s worker_id=%s reason=%s duration=%s",
			jobID, config.C.WorkerID, failureReason, duration)

		metrics.JobsFailed.WithLabelValues(config.C.WorkerID, failureReason).Inc()

		retry, err := redis.RDB.HIncrBy(jobCtx, jobKey, "retry_count", 1).Result()
		if err != nil {
			log.Printf("event=retry_increment_failed job_id=%s err=%v", jobID, err)
		}

		if retry > int64(config.C.MaxRetry) {

			log.Printf("event=job_dlq job_id=%s retry=%d", jobID, retry)

			_ = redis.RDB.HSet(jobCtx, jobKey, "status", "DLQ").Err()
			_ = redis.RDB.LPush(jobCtx, "dead_letter_queue", jobID).Err()
			_ = redis.RDB.Expire(jobCtx, "dead_letter_queue", 24*time.Hour).Err()

			_ = redis.RDB.XAck(jobCtx, streamName, groupName, msgID).Err()
			return
		}

		log.Printf("event=job_retry_scheduled job_id=%s retry=%d", jobID, retry)

		scheduleRetry(jobCtx, jobID, retry)

		_ = redis.RDB.HSet(jobCtx, jobKey, map[string]interface{}{
			"status":     "FAILED",
			"started_at": 0,
			"worker_id":  "",
		}).Err()

		_ = redis.RDB.XAck(jobCtx, streamName, groupName, msgID).Err()
		return
	}

	// ---------------- SUCCESS ----------------

	log.Printf("event=job_completed job_id=%s duration=%s", jobID, duration)

	metrics.JobsProcessed.WithLabelValues(config.C.WorkerID).Inc()
	metrics.JobsSucceeded.WithLabelValues(config.C.WorkerID).Inc()
	metrics.JobDuration.WithLabelValues(config.C.WorkerID).Observe(duration.Seconds())

	if err := redis.RDB.HSet(jobCtx, jobKey, map[string]interface{}{
		"status":    "COMPLETED",
		"worker_id": "",
	}).Err(); err != nil {
		log.Printf("event=redis_hset_complete_failed job_id=%s err=%v", jobID, err)
	}

	_ = redis.RDB.Expire(jobCtx, jobKey, jobTTL).Err()
	_ = redis.RDB.XAck(jobCtx, streamName, groupName, msgID).Err()
}

// ---------------- MESSAGE HANDLER ----------------

func handleMessage(ctx context.Context, consumerName string, msg redisClient.XMessage, sem chan struct{}) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("event=worker_panic msg_id=%s err=%v", msg.ID, r)
		}
		<-sem // release the semaphore slot when done
	}()

	processJob(ctx, msg.ID, msg.Values)
}

// ---------------- STARTUP RECOVERY ----------------
// Called once per consumer at startup. Claims any messages left pending
// by a previously crashed consumer instance.

func recoverPending(ctx context.Context, consumerName string, sem chan struct{}) {
	for {
		msgs, _, err := redis.RDB.XAutoClaim(ctx, &redisClient.XAutoClaimArgs{
			Stream:   streamName,
			Group:    groupName,
			Consumer: consumerName,
			MinIdle:  pendingMinIdle,
			Start:    "0",
			Count:    50,
		}).Result()

		if err != nil {
			if err == redisClient.Nil {
				break
			}
			log.Printf("event=autoclaim_error consumer=%s err=%v", consumerName, err)
			time.Sleep(2 * time.Second)
			continue
		}

		if len(msgs) == 0 {
			break
		}

		for _, msg := range msgs {
			sem <- struct{}{}

			go func(m redisClient.XMessage) {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("event=recover_pending_panic msg_id=%s err=%v", m.ID, r)
					}
					<-sem
				}()

				// processJob already calls XAck — do NOT call XAck here again
				processJob(ctx, m.ID, m.Values)

			}(msg)
		}
	}
}

// ---------------- WORKER LOOP ----------------

func workerLoop(ctx context.Context, consumerName string, sem chan struct{}) {

	// periodic recovery: re-claim stuck messages every recoveryInterval
	go func() {
		ticker := time.NewTicker(recoveryInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				msgs, _, err := redis.RDB.XAutoClaim(ctx, &redisClient.XAutoClaimArgs{
					Stream:   streamName,
					Group:    groupName,
					Consumer: consumerName,
					MinIdle:  pendingMinIdle,
					Start:    "0",
					Count:    50,
				}).Result()

				if err != nil && err != redisClient.Nil {
					log.Printf("event=autoclaim_error consumer=%s err=%v", consumerName, err)
					continue
				}

				if len(msgs) == 0 {
					continue
				}

				log.Printf("event=autoclaim_messages consumer=%s count=%d", consumerName, len(msgs))

				for _, msg := range msgs {
					sem <- struct{}{}
					go handleMessage(ctx, consumerName, msg, sem)
				}
			}
		}
	}()

	// ---- MAIN READ LOOP ----
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		streams, err := redis.RDB.XReadGroup(ctx, &redisClient.XReadGroupArgs{
			Group:    groupName,
			Consumer: consumerName,
			Streams:  []string{streamName, ">"},
			Count:    10,
			Block:    5 * time.Second,
		}).Result()

		if err != nil {
			if err == redisClient.Nil || strings.Contains(err.Error(), "redis: nil") {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			log.Printf("event=xreadgroup_error consumer=%s err=%v", consumerName, err)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				sem <- struct{}{} // block if all slots are busy
				go handleMessage(ctx, consumerName, msg, sem)
			}
		}
	}
}

// ---------------- ENTRY POINT ----------------

func StartWorker(ctx context.Context) {

	concurrency := config.C.WorkerConcurrency

	log.Printf("event=worker_started id=%s concurrency=%d", config.C.WorkerID, concurrency)

	metrics.WorkerActive.WithLabelValues(config.C.WorkerID).Set(1)

	initConsumerGroup(ctx)

	sem := make(chan struct{}, concurrency)

	// retry scheduler: re-enqueues jobs whose backoff has elapsed
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("event=retry_scheduler_panic err=%v", r)
			}
		}()
		retryScheduler(ctx)
	}()

	// spawn one consumer goroutine per concurrency slot
	for i := 0; i < concurrency; i++ {
		consumerName := fmt.Sprintf("%s-%d", config.C.WorkerID, i)

		go func(name string) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("event=worker_loop_panic consumer=%s err=%v", name, r)
				}
			}()

			recoverPending(ctx, name, sem) // drain stuck messages from previous run
			workerLoop(ctx, name, sem)     // enter main read loop
		}(consumerName)
	}

	// block until shutdown signal
	<-ctx.Done()

	log.Println("event=worker_stopping draining=true")

	// drain semaphore — wait for all in-flight goroutines to finish
	for i := 0; i < concurrency; i++ {
		sem <- struct{}{}
	}

	metrics.WorkerActive.WithLabelValues(config.C.WorkerID).Set(0)

	log.Println("event=worker_stopped graceful_shutdown=true")
}
