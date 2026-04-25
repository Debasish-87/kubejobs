package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
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

var requestCount int64
var lastReset int64

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
		start := time.Now()
		next(w, r)
		log.Printf("event=http_request method=%s path=%s duration=%s",
			r.Method, r.URL.Path, time.Since(start))
	}
}

// ---------------- RATE LIMIT ----------------

func allowRequest() bool {
	now := time.Now().Unix()

	if now != atomic.LoadInt64(&lastReset) {
		atomic.StoreInt64(&lastReset, now)
		atomic.StoreInt64(&requestCount, 0)
	}

	count := atomic.AddInt64(&requestCount, 1)
	return count <= 200
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

	// ---------------- BASIC GUARDS ----------------

	if !allowRequest() {
		writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Only POST allowed")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if isQueueOverloaded(ctx) {
		writeError(w, http.StatusTooManyRequests, "queue overloaded")
		return
	}

	// ---------------- JOB CREATION ----------------

	jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())

	newJob := job.Job{
		ID:        jobID,
		Status:    "PENDING",
		CreatedAt: time.Now().Unix(),
	}

	jobKey := "job_data:" + jobID

	start := time.Now()

	// ---------------- STORE METADATA ----------------

	err := redis.RDB.HSet(ctx, jobKey, map[string]interface{}{
		"id":          jobID,
		"status":      newJob.Status,
		"created_at":  newJob.CreatedAt,
		"started_at":  0,
		"retry_count": 0,
		"worker_id":   "",
	}).Err()

	if err != nil {
		log.Printf("event=job_store_failed job_id=%s err=%v", jobID, err)
		writeError(w, 500, "failed to store job")
		return
	}

	// ---------------- ENQUEUE ----------------

	_, err = redis.RDB.XAdd(ctx, &redisClient.XAddArgs{
		Stream: streamName,
		ID:     "*", // MUST for uniqueness
		Values: map[string]interface{}{
			"job_id": jobID,
		},
	}).Result()

	if err != nil {
		log.Printf("event=job_enqueue_failed job_id=%s err=%v", jobID, err)
		writeError(w, 500, "failed to enqueue job")
		return
	}

	// ---------------- METRICS / LOGGING ----------------

	latency := time.Since(start).Milliseconds()

	log.Printf(
		"event=job_created job_id=%s latency_ms=%d",
		jobID,
		latency,
	)

	// ---------------- RESPONSE ----------------

	writeJSON(w, 200, newJob)
}

// ---------------- STATS ----------------

func getStats(w http.ResponseWriter, r *http.Request) {

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	streamLen, _ := redis.RDB.XLen(ctx, streamName).Result()
	dlqLen, _ := redis.RDB.LLen(ctx, "dead_letter_queue").Result()
	pending, _ := redis.RDB.XPending(ctx, streamName, groupName).Result()

	resp := map[string]interface{}{
		"stream_length": streamLen,
		"dead_letter":   dlqLen,
		"pending":       pending.Count,
	}

	writeJSON(w, 200, resp)
}

// ---------------- ROUTER ----------------

func buildRouter() *http.ServeMux {

	mux := http.NewServeMux()

	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return recoverMiddleware(logMiddleware(h))
	}

	mux.HandleFunc("/jobs", wrap(createJob))
	mux.HandleFunc("/stats", wrap(getStats))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mux.Handle("/metrics", promhttp.Handler())

	return mux
}

// ---------------- METRICS SERVER (FOR WORKER) ----------------

func startMetricsServer(port string) {
	go func() {

		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())

		server := &http.Server{
			Addr:    port,
			Handler: mux,
		}

		log.Println("event=metrics_server_started port=" + port)

		if err := server.ListenAndServe(); err != nil {
			log.Println("metrics server error:", err)
		}
	}()
}

// ---------------- API ----------------

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

	server.Shutdown(shutdownCtx)
}

// ---------------- WORKER ----------------

func startWorkerMode(ctx context.Context) {

	log.Println("event=worker_started id=", config.C.WorkerID)

	// worker metrics exposed on different port
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
