package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	redisClient "github.com/redis/go-redis/v9"

	"kubejobs/internal/config"
	"kubejobs/internal/job"
	"kubejobs/internal/metrics"
	"kubejobs/internal/recovery"
	"kubejobs/internal/redis"
	"kubejobs/internal/worker"
)

const (
	streamName = "jobs_stream"
	groupName  = "workers"
)

// ---------------- RATE LIMITER ----------------
// Uses a mutex-guarded struct to ensure the count + window reset are one atomic operation.
// (Two separate atomics would have a race condition between checking and resetting.)

var rate struct {
	mu      sync.Mutex
	count   int64
	windowT int64
}

func allowRequest() bool {
	rate.mu.Lock()
	defer rate.mu.Unlock()

	now := time.Now().Unix()
	if now != rate.windowT {
		rate.windowT = now
		rate.count = 0
	}
	rate.count++
	return rate.count <= 200
}

// ---------------- HELPERS ----------------

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---------------- MIDDLEWARE ----------------

func recoverMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("event=panic err=%v", err)
				writeError(w, 500, "internal error")
			}
		}()
		next(w, r)
	}
}

func logMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// reqID is generated at the START of the request so it can be used
		// as a correlation ID in any mid-request logging
		reqID := fmt.Sprintf("req-%d", time.Now().UnixNano())
		start := time.Now()
		next(w, r)
		log.Printf(
			"event=http_request req_id=%s method=%s path=%s duration=%s",
			reqID,
			r.Method,
			r.URL.Path,
			time.Since(start),
		)
	}
}

// ---------------- BACKPRESSURE ----------------

func isQueueOverloaded(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	pending, err := redis.RDB.XPending(ctx, streamName, groupName).Result()
	if err != nil {
		log.Println("event=backpressure_check_failed err=", err)
		return false
	}

	return pending.Count > int64(config.C.MaxQueueSize)
}

// ---------------- CREATE JOB ----------------

func createJob(w http.ResponseWriter, r *http.Request) {

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST allowed")
		return
	}

	// Distinguish between an empty body (EOF) and genuinely malformed JSON
	var payload map[string]interface{}

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		if err == io.EOF {
			writeError(w, http.StatusBadRequest, "request body required")
			return
		}
		log.Printf("event=invalid_json err=%v", err)
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	if payload == nil {
		writeError(w, http.StatusBadRequest, "payload must not be null")
		return
	}

	if !allowRequest() {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if isQueueOverloaded(ctx) {
		log.Printf("event=queue_overloaded reject=true max=%d", config.C.MaxQueueSize)
		writeError(w, http.StatusTooManyRequests, "queue overloaded")
		return
	}

	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())

	newJob := job.Job{
		ID:        jobID,
		Status:    "PENDING",
		CreatedAt: time.Now().Unix(),
	}

	jobKey := "job_data:" + jobID
	start := time.Now()

	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		log.Printf("event=payload_marshal_failed job_id=%s err=%v", jobID, err)
		writeError(w, http.StatusInternalServerError, "invalid payload")
		return
	}

	// Store job metadata in a Redis hash
	err = redis.RDB.HSet(ctx, jobKey, map[string]interface{}{
		"id":          jobID,
		"status":      newJob.Status,
		"created_at":  newJob.CreatedAt,
		"started_at":  0,
		"retry_count": 0,
		"worker_id":   "",
		"payload":     string(payloadJSON),
	}).Err()

	if err != nil {
		log.Printf("event=job_store_failed job_id=%s err=%v", jobID, err)
		writeError(w, http.StatusInternalServerError, "failed to store job")
		return
	}

	// job metadata expires after 24h whether completed or not
	redis.RDB.Expire(ctx, jobKey, 24*time.Hour)

	// Enqueue the job into the Redis stream
	_, err = redis.RDB.XAdd(ctx, &redisClient.XAddArgs{
		Stream: streamName,
		ID:     "*",
		Values: map[string]interface{}{
			"job_id":  jobID,
			"payload": string(payloadJSON),
		},
	}).Result()

	if err != nil {
		log.Printf("event=job_enqueue_failed job_id=%s err=%v", jobID, err)
		writeError(w, http.StatusInternalServerError, "failed to enqueue job")
		return
	}

	log.Printf("event=job_created job_id=%s latency_ms=%d",
		jobID, time.Since(start).Milliseconds())

	writeJSON(w, http.StatusOK, newJob)
}

// ---------------- GET JOB ----------------

func getJob(w http.ResponseWriter, r *http.Request) {

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	jobID := r.URL.Query().Get("id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "missing id query param")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	jobKey := "job_data:" + jobID

	jobMap, err := redis.RDB.HGetAll(ctx, jobKey).Result()
	if err != nil || len(jobMap) == 0 {
		writeError(w, http.StatusNotFound, "job not found")
		return
	}

	writeJSON(w, http.StatusOK, jobMap)
}

// ---------------- LIST JOBS ----------------

func listJobs(w http.ResponseWriter, r *http.Request) {

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	// Scan for all job_data:* keys (paginated to avoid blocking Redis)
	var cursor uint64
	var keys []string

	for {
		batch, nextCursor, err := redis.RDB.Scan(ctx, cursor, "job_data:*", 100).Result()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to scan jobs")
			return
		}
		keys = append(keys, batch...)
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	jobs := make([]map[string]string, 0, len(keys))

	for _, key := range keys {
		jobMap, err := redis.RDB.HGetAll(ctx, key).Result()
		if err != nil || len(jobMap) == 0 {
			continue
		}
		jobs = append(jobs, jobMap)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count": len(jobs),
		"jobs":  jobs,
	})
}

// ---------------- DEAD LETTER QUEUE ----------------

func getDLQ(w http.ResponseWriter, r *http.Request) {

	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "only GET allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	// Return up to 100 most recent DLQ entries
	items, err := redis.RDB.LRange(ctx, "dead_letter_queue", 0, 99).Result()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read DLQ")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"count": len(items),
		"jobs":  items,
	})
}

// ---------------- STATS ----------------

func getStats(w http.ResponseWriter, r *http.Request) {

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	streamLen, _ := redis.RDB.XLen(ctx, streamName).Result()
	dlqLen, _ := redis.RDB.LLen(ctx, "dead_letter_queue").Result()
	pending, _ := redis.RDB.XPending(ctx, streamName, groupName).Result()
	retryLen, _ := redis.RDB.ZCard(ctx, "jobs_retry_zset").Result()

	resp := map[string]interface{}{
		"stream_length": streamLen,
		"dead_letter":   dlqLen,
		"pending":       pending.Count,
		"retry_queue":   retryLen,
	}

	writeJSON(w, http.StatusOK, resp)
}

// ---------------- ROUTER ----------------

func buildRouter() *http.ServeMux {

	mux := http.NewServeMux()

	// helper: chain middleware onto a handler
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return recoverMiddleware(logMiddleware(h))
	}

	mux.HandleFunc("/jobs", wrap(createJob))       // POST  — submit a job
	mux.HandleFunc("/job", wrap(getJob))            // GET   — get one job by id
	mux.HandleFunc("/jobs/list", wrap(listJobs))    // GET   — list all jobs
	mux.HandleFunc("/jobs/dlq", wrap(getDLQ))       // GET   — list DLQ entries
	mux.HandleFunc("/stats", wrap(getStats))        // GET   — queue stats

	// Liveness — process is alive (no dependency check)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	// Readiness — checks Redis connectivity.
	// Kubernetes uses /ready to decide whether to route traffic.
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
		defer cancel()

		if err := redis.RDB.Ping(ctx).Err(); err != nil {
			log.Printf("event=readiness_check_failed err=%v", err)
			writeError(w, http.StatusServiceUnavailable, "redis unavailable")
			return
		}

		w.WriteHeader(http.StatusOK)
	})

	mux.Handle("/metrics", promhttp.Handler())

	return mux
}

// ---------------- METRICS SERVER (for worker mode) ----------------

func startMetricsServer(port string) {
	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())

		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("OK"))
		})

		mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
			defer cancel()

			if err := redis.RDB.Ping(ctx).Err(); err != nil {
				log.Printf("event=worker_readiness_failed err=%v", err)
				writeError(w, http.StatusServiceUnavailable, "redis unavailable")
				return
			}
			w.WriteHeader(http.StatusOK)
		})

		server := &http.Server{
			Addr:         port,
			Handler:      mux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 10 * time.Second,
			IdleTimeout:  30 * time.Second,
		}

		log.Println("event=metrics_server_started port=" + port)

		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Println("event=metrics_server_error err=", err)
		}
	}()
}

// ---------------- API SERVER ----------------

func startAPI(ctx context.Context) {

	server := &http.Server{
		Addr:         ":8080",
		Handler:      buildRouter(),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	go func() {
		log.Println("event=api_started addr=:8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	<-ctx.Done()

	log.Println("event=api_shutdown")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("event=api_shutdown_error err=%v", err)
	}
}

// ---------------- WORKER MODE ----------------

func startWorkerMode(ctx context.Context) {

	log.Printf("event=worker_mode_started id=%s", config.C.WorkerID)

	// Worker exposes metrics + health/ready probes on :8081
	startMetricsServer(":8081")

	go worker.StartWorker(ctx)
	go recovery.StartRecovery(ctx)

	<-ctx.Done()
}

// ---------------- MAIN ----------------

func main() {

	config.Init()
	redis.InitRedis()
	metrics.Init()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Listen for SIGINT/SIGTERM and trigger graceful shutdown
	go func() {
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
		<-stop
		log.Println("event=shutdown_signal_received")
		cancel()
	}()

	if config.C.AppMode == "worker" {
		startWorkerMode(ctx)
	} else {
		startAPI(ctx)
	}

	redis.CloseRedis()

	log.Println("event=app_stopped")
}
