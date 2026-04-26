package recovery

import (
	"context"
	"log"
	"time"

	redisClient "github.com/redis/go-redis/v9"

	"kubejobs/internal/config"
	"kubejobs/internal/redis"
)

const (
	streamName   = "jobs_stream"
	groupName    = "workers"
	retryZSet    = "jobs_retry_zset"
	minIdleTime  = 30 * time.Second
	batchSize    = 10
	scanInterval = 5 * time.Second
)

// ---------------- DELAYED RETRY ----------------

func scheduleRetry(ctx context.Context, jobID string, retry int64) {

	// exponential backoff + small jitter to avoid thundering herd
	backoff := time.Duration(1<<retry) * time.Second
	jitter := time.Duration((retry*137)%1000) * time.Millisecond

	retryAt := time.Now().Add(backoff + jitter).Unix()

	err := redis.RDB.ZAdd(ctx, retryZSet, redisClient.Z{
		Score:  float64(retryAt),
		Member: jobID,
	}).Err()

	if err != nil {
		log.Printf("event=recovery_retry_schedule_failed job_id=%s err=%v", jobID, err)
	}
}

// ---------------- MAIN RECOVERY LOOP ----------------
// StartRecovery runs on a ticker, scanning for messages that are stuck in
// the Pending Entries List (PEL) — messages that were delivered to a worker
// that crashed before it could ACK them.

func StartRecovery(ctx context.Context) {

	log.Println("event=recovery_started")

	for {

		select {
		case <-ctx.Done():
			log.Println("event=recovery_stopped")
			return
		default:
		}

		pending, err := redis.RDB.XPendingExt(ctx, &redisClient.XPendingExtArgs{
			Stream: streamName,
			Group:  groupName,
			Start:  "-",
			End:    "+",
			Count:  batchSize,
		}).Result()

		if err != nil {
			log.Printf("event=recovery_xpending_error err=%v", err)
			time.Sleep(scanInterval)
			continue
		}

		if len(pending) == 0 {
			time.Sleep(scanInterval)
			continue
		}

		for _, msg := range pending {

			// Skip messages that haven't been idle long enough
			// (the worker might still be processing them)
			if msg.Idle < minIdleTime {
				continue
			}

			log.Printf("event=recovery_claim_attempt msg_id=%s idle=%s", msg.ID, msg.Idle)

			claimed, err := redis.RDB.XClaim(ctx, &redisClient.XClaimArgs{
				Stream:   streamName,
				Group:    groupName,
				Consumer: config.C.WorkerID,
				MinIdle:  minIdleTime,
				Messages: []string{msg.ID},
			}).Result()

			if err != nil || len(claimed) == 0 {
				continue
			}

			for _, m := range claimed {

				jobIDRaw, ok := m.Values["job_id"]
				if !ok {
					// malformed message — ACK and discard
					_ = redis.RDB.XAck(ctx, streamName, groupName, m.ID).Err()
					continue
				}

				jobID, ok := jobIDRaw.(string)
				if !ok {
					_ = redis.RDB.XAck(ctx, streamName, groupName, m.ID).Err()
					continue
				}

				jobKey := "job_data:" + jobID

				jobMap, err := redis.RDB.HGetAll(ctx, jobKey).Result()
				if err != nil || len(jobMap) == 0 {
					// job metadata is gone — just ACK
					_ = redis.RDB.XAck(ctx, streamName, groupName, m.ID).Err()
					continue
				}

				status := jobMap["status"]

				// already finished — just ACK the stuck message
				if status == "COMPLETED" || status == "DLQ" {
					_ = redis.RDB.XAck(ctx, streamName, groupName, m.ID).Err()
					continue
				}

				retry, _ := redis.RDB.HIncrBy(ctx, jobKey, "retry_count", 1).Result()

				if retry > int64(config.C.MaxRetry) {

					log.Printf("event=recovery_dlq job_id=%s retry=%d", jobID, retry)

					_ = redis.RDB.HSet(ctx, jobKey, map[string]interface{}{
						"status":    "DLQ",
						"worker_id": "",
					}).Err()

					_ = redis.RDB.LPush(ctx, "dead_letter_queue", jobID).Err()

					_ = redis.RDB.XAck(ctx, streamName, groupName, m.ID).Err()
					continue
				}

				log.Printf("event=recovery_retry_scheduled job_id=%s retry=%d", jobID, retry)

				// mark FAILED before scheduling retry
				_ = redis.RDB.HSet(ctx, jobKey, map[string]interface{}{
					"status":     "FAILED",
					"started_at": 0,
					"worker_id":  "",
				}).Err()

				scheduleRetry(ctx, jobID, retry)

				// ACK the stuck message — the retry scheduler will re-enqueue it
				_ = redis.RDB.XAck(ctx, streamName, groupName, m.ID).Err()
			}
		}

		time.Sleep(scanInterval)
	}
}
