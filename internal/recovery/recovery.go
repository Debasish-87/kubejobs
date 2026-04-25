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

	backoff := time.Duration(1<<retry) * time.Second
	jitter := time.Duration((retry*137)%1000) * time.Millisecond

	retryAt := time.Now().Add(backoff + jitter).Unix()

	err := redis.RDB.ZAdd(ctx, retryZSet, redisClient.Z{
		Score:  float64(retryAt),
		Member: jobID,
	}).Err()

	if err != nil {
		log.Println("recovery_retry_schedule_failed", jobID)
	}
}

// ---------------- MAIN ----------------

func StartRecovery(ctx context.Context) {

	log.Println("recovery_started")

	for {

		select {
		case <-ctx.Done():
			log.Println("recovery_stopped")
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
			log.Println("recovery_xpending_error:", err)
			time.Sleep(scanInterval)
			continue
		}

		if len(pending) == 0 {
			time.Sleep(scanInterval)
			continue
		}

		for _, msg := range pending {

			if msg.Idle < minIdleTime {
				continue
			}

			log.Printf("recovery_claim_attempt id=%s idle=%s", msg.ID, msg.Idle)

			claimed, err := redis.RDB.XClaim(ctx, &redisClient.XClaimArgs{
				Stream:   streamName,
				Group:    groupName,
				Consumer: config.C.WorkerID, // FIXED
				MinIdle:  minIdleTime,
				Messages: []string{msg.ID},
			}).Result()

			if err != nil || len(claimed) == 0 {
				continue
			}

			for _, m := range claimed {

				jobIDRaw, ok := m.Values["job_id"]
				if !ok {
					redis.RDB.XAck(ctx, streamName, groupName, m.ID)
					continue
				}

				jobID, ok := jobIDRaw.(string)
				if !ok {
					redis.RDB.XAck(ctx, streamName, groupName, m.ID)
					continue
				}

				jobKey := "job_data:" + jobID

				jobMap, err := redis.RDB.HGetAll(ctx, jobKey).Result()
				if err != nil || len(jobMap) == 0 {
					redis.RDB.XAck(ctx, streamName, groupName, m.ID)
					continue
				}

				status := jobMap["status"]

				// already completed or DLQ → just ACK
				if status == "COMPLETED" || status == "DLQ" {
					redis.RDB.XAck(ctx, streamName, groupName, m.ID)
					continue
				}

				retry, _ := redis.RDB.HIncrBy(ctx, jobKey, "retry_count", 1).Result()

				if retry > int64(config.C.MaxRetry) { // FIXED

					log.Printf("event=recovery_dlq id=%s", jobID)

					_ = redis.RDB.HSet(ctx, jobKey, map[string]interface{}{
						"status":    "DLQ",
						"worker_id": "",
					}).Err()

					_ = redis.RDB.LPush(ctx, "dead_letter_queue", jobID).Err()

					redis.RDB.XAck(ctx, streamName, groupName, m.ID)
					continue
				}

				log.Printf("event=recovery_retry_scheduled id=%s retry=%d", jobID, retry)

				// mark FAILED
				_ = redis.RDB.HSet(ctx, jobKey, map[string]interface{}{
					"status":     "FAILED",
					"started_at": 0,
					"worker_id":  "",
				}).Err()

				// ALWAYS go through retry scheduler
				scheduleRetry(ctx, jobID, retry)

				// ACK old stuck message
				redis.RDB.XAck(ctx, streamName, groupName, m.ID)
			}
		}

		time.Sleep(scanInterval)
	}
}
