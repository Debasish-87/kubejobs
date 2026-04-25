package worker

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand"
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
	concurrency     = 20
	blockTimeout    = 5 * time.Second
	lockTTL         = 2 * time.Minute
	retryScanPeriod = 2 * time.Second
	maxRetryBackoff = 10
	jobTTL          = 1 * time.Hour
)

// ---------------- INIT ----------------

func initConsumerGroup(ctx context.Context) {
	err := redis.RDB.XGroupCreateMkStream(ctx, streamName, groupName, "0").Err()

	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		log.Fatalf("event=consumer_group_init_failed err=%v", err)
	}

	log.Println("event=consumer_group_ready stream=jobs_stream group=workers")
}

// ---------------- LOCK ----------------

func acquireLock(ctx context.Context, jobID string) bool {
	lockKey := "job_lock:" + jobID

	ok, err := redis.RDB.SetNX(ctx, lockKey, "1", lockTTL).Result()
	if err != nil {
		log.Printf("event=lock_error job=%s err=%v", jobID, err)
		return false
	}
	return ok
}

func releaseLock(ctx context.Context, jobID string) {
	lockKey := "job_lock:" + jobID
	redis.RDB.Del(ctx, lockKey)
}

// ---------------- RETRY ----------------

func scheduleRetry(ctx context.Context, jobID string, retry int64) {

	if retry > maxRetryBackoff {
		retry = maxRetryBackoff
	}

	base := math.Pow(2, float64(retry))
	delay := time.Duration(base)*time.Second +
		time.Duration(rand.Intn(1000))*time.Millisecond

	retryAt := time.Now().Add(delay).Unix()

	err := redis.RDB.ZAdd(ctx, retryZSet, redisClient.Z{
		Score:  float64(retryAt),
		Member: jobID,
	}).Err()

	if err != nil {
		log.Printf("event=retry_schedule_failed id=%s err=%v", jobID, err)
	}
}

// ---------------- RETRY SCHEDULER ----------------

func retryScheduler(ctx context.Context) {

	log.Println("event=retry_scheduler_started")

	ticker := time.NewTicker(retryScanPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:

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

				redis.RDB.ZRem(ctx, retryZSet, jobID)

				err := redis.RDB.XAdd(ctx, &redisClient.XAddArgs{
					Stream: streamName,
					Values: map[string]interface{}{
						"job_id": jobID,
					},
				}).Err()

				if err != nil {
					log.Printf("event=retry_requeue_failed id=%s err=%v", jobID, err)

					redis.RDB.ZAdd(ctx, retryZSet, redisClient.Z{
						Score:  float64(now + 5),
						Member: jobID,
					})

					continue
				}

				log.Printf("event=retry_requeued id=%s", jobID)
			}
		}
	}
}

func heavyWork() {
	start := time.Now()

	for time.Since(start) < 5*time.Second {
		for i := 0; i < 500000; i++ {
			_ = math.Sqrt(rand.Float64())
		}
	}
}

// ---------------- PROCESS ----------------

func processJob(ctx context.Context, msgID string, values map[string]interface{}) {

	defer func() {
		if r := recover(); r != nil {
			log.Printf("event=process_job_panic msg_id=%s err=%v", msgID, r)
		}
	}()

	jobIDRaw, ok := values["job_id"]
	if !ok {
		log.Printf("event=invalid_message_missing_job_id msg_id=%s", msgID)
		return
	}

	jobID, ok := jobIDRaw.(string)
	if !ok {
		log.Printf("event=invalid_message_type msg_id=%s", msgID)
		return
	}

	jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if !acquireLock(jobCtx, jobID) {
		redis.RDB.XAck(jobCtx, streamName, groupName, msgID)
		return
	}
	defer releaseLock(jobCtx, jobID)

	jobKey := "job_data:" + jobID

	jobMap, err := redis.RDB.HGetAll(jobCtx, jobKey).Result()
	if err != nil || len(jobMap) == 0 {
		redis.RDB.XAck(jobCtx, streamName, groupName, msgID)
		return
	}

	// strict duplicate prevention
	if jobMap["status"] != "PENDING" {
		redis.RDB.XAck(jobCtx, streamName, groupName, msgID)
		return
	}

	now := time.Now().Unix()

	_ = redis.RDB.HSet(jobCtx, jobKey, map[string]interface{}{
		"status":     "RUNNING",
		"started_at": now,
		"worker_id":  config.C.WorkerID,
	}).Err()

	start := time.Now()

	log.Printf("event=job_started id=%s worker=%s", jobID, config.C.WorkerID)

	heavyWork()
	time.Sleep(5 * time.Second)

	duration := time.Since(start)
	failed := rand.Float32() < 0.2

	if failed {

		metrics.JobsFailed.WithLabelValues(config.C.WorkerID, "retry").Inc()

		retry, _ := redis.RDB.HIncrBy(jobCtx, jobKey, "retry_count", 1).Result()

		if retry > int64(config.C.MaxRetry) {

			log.Printf("event=job_dlq id=%s", jobID)

			redis.RDB.HSet(jobCtx, jobKey, "status", "DLQ")
			redis.RDB.LPush(jobCtx, "dead_letter_queue", jobID)
			redis.RDB.Expire(jobCtx, "dead_letter_queue", 24*time.Hour)

			redis.RDB.XAck(jobCtx, streamName, groupName, msgID)
			return
		}

		log.Printf("event=job_retry_scheduled id=%s retry=%d", jobID, retry)

		scheduleRetry(jobCtx, jobID, retry)

		redis.RDB.HSet(jobCtx, jobKey, map[string]interface{}{
			"status":     "FAILED",
			"started_at": 0,
			"worker_id":  "",
		})

		redis.RDB.XAck(jobCtx, streamName, groupName, msgID)
		return
	}

	log.Printf("event=job_completed id=%s duration=%s", jobID, duration)

	metrics.JobsProcessed.WithLabelValues(config.C.WorkerID).Inc()
	metrics.JobDuration.WithLabelValues(config.C.WorkerID).Observe(duration.Seconds())

	redis.RDB.HSet(jobCtx, jobKey, map[string]interface{}{
		"status":    "COMPLETED",
		"worker_id": "",
	})

	redis.RDB.Expire(jobCtx, jobKey, jobTTL)

	redis.RDB.XAck(jobCtx, streamName, groupName, msgID)
}

// ---------------- LOOP ----------------

func workerLoop(ctx context.Context, consumerName string, sem chan struct{}) {

	go func() {
		for {
			msgs, _, err := redis.RDB.XAutoClaim(ctx, &redisClient.XAutoClaimArgs{
				Stream:   streamName,
				Group:    groupName,
				Consumer: consumerName,
				MinIdle:  30 * time.Second,
				Start:    "0",
				Count:    50,
			}).Result()

			if err != nil && err != redisClient.Nil {
				log.Printf("event=autoclaim_error err=%v", err)
				time.Sleep(2 * time.Second)
				continue
			}

			if len(msgs) == 0 {
				break
			}

			for _, msg := range msgs {
				sem <- struct{}{}

				go handleMessage(ctx, consumerName, msg, sem)
			}
		}
	}()

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
			log.Printf("event=xreadgroup_error err=%v", err)
			continue
		}

		for _, stream := range streams {
			for _, msg := range stream.Messages {
				sem <- struct{}{}

				go handleMessage(ctx, consumerName, msg, sem)
			}
		}
	}
}

func handleMessage(ctx context.Context, consumerName string, msg redisClient.XMessage, sem chan struct{}) {

	defer func() {
		if r := recover(); r != nil {
			log.Printf("event=worker_panic msg_id=%s err=%v", msg.ID, r)
		}
		<-sem
	}()

	processJob(ctx, msg.ID, msg.Values)

	err := redis.RDB.XAck(ctx, streamName, groupName, msg.ID).Err()
	if err != nil {
		log.Printf("event=xack_failed msg_id=%s err=%v", msg.ID, err)
	}
}

func recoverPending(ctx context.Context, consumerName string, sem chan struct{}) {

	for {
		msgs, _, err := redis.RDB.XAutoClaim(ctx, &redisClient.XAutoClaimArgs{
			Stream:   streamName,
			Group:    groupName,
			Consumer: consumerName,
			MinIdle:  30 * time.Second,
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

				processJob(ctx, m.ID, m.Values)

				err := redis.RDB.XAck(ctx, streamName, groupName, m.ID).Err()
				if err != nil {
					log.Printf("event=xack_failed msg_id=%s err=%v", m.ID, err)
				}

			}(msg)
		}
	}
}

// ---------------- ENTRY ----------------

func StartWorker(ctx context.Context) {

	log.Printf("event=worker_started id=%s concurrency=%d", config.C.WorkerID, concurrency)

	metrics.WorkerActive.WithLabelValues(config.C.WorkerID).Set(1)

	initConsumerGroup(ctx)

	sem := make(chan struct{}, concurrency)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("event=retry_scheduler_panic err=%v", r)
			}
		}()
		retryScheduler(ctx)
	}()

	for i := 0; i < concurrency; i++ {

		consumerName := fmt.Sprintf("%s-%d", config.C.WorkerID, i)

		go func(name string) {

			defer func() {
				if r := recover(); r != nil {
					log.Printf("event=worker_loop_panic consumer=%s err=%v", name, r)
				}
			}()

			recoverPending(ctx, name, sem)

			workerLoop(ctx, name, sem)

		}(consumerName)
	}

	<-ctx.Done()

	log.Println("event=worker_stopping draining=true")

	// wait for in-flight jobs to finish
	for i := 0; i < cap(sem); i++ {
		sem <- struct{}{}
	}

	metrics.WorkerActive.WithLabelValues(config.C.WorkerID).Set(0)

	log.Println("event=worker_stopped graceful_shutdown=true")
}
