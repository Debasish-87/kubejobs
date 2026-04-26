# KubeJobs

A distributed background job queue built in Go, backed by **Redis Streams**.

- **API server** accepts jobs over HTTP and enqueues them into a Redis Stream
- **Worker** reads from the stream, processes jobs concurrently, and handles retries with exponential backoff
- **Dead Letter Queue** (DLQ) catches jobs that fail more than `MAX_RETRY` times
- **Prometheus + Grafana** for metrics and dashboards

---

## Project Structure

```
kubejobs/
├── cmd/server/main.go              # Entry point (API or Worker mode)
├── internal/
│   ├── config/config.go            # Env-based config with validation
│   ├── redis/redis.go              # Redis client (pool + health check)
│   ├── job/model.go                # Job struct
│   ├── metrics/metrics.go          # Prometheus metrics
│   ├── worker/worker.go            # Worker loop, retry scheduler, DLQ
│   └── recovery/recovery.go        # Claim stuck messages from crashed workers
├── deployments/
│   ├── docker/
│   │   ├── Dockerfile
│   │   ├── docker-compose.yml
│   │   ├── prometheus.yml
│   │   └── alerts.yml
│   └── kubernetes/                 # K8s manifests
├── Makefile
└── .env                            # Local environment variables
```

---

## Quick Start (Docker Compose)

### 1. Configure environment

```bash
cp .env .env.local   # edit if needed
```

The `.env` file already has working defaults for local dev.

### 2. Start everything

```bash
make up
```

This starts Redis, the API, one Worker, Prometheus, and Grafana.

| Service    | URL                          |
|-----------|-------------------------------|
| API        | http://localhost:8080         |
| Prometheus | http://localhost:9090         |
| Grafana    | http://localhost:3000 (admin/admin) |

### 3. Stop everything

```bash
make down
```

---

## API Endpoints

### Submit a job

```bash
curl -X POST http://localhost:8080/jobs \
  -H "Content-Type: application/json" \
  -d '{"task": "send_email", "to": "user@example.com"}'
```

Response:
```json
{
  "id": "job_1714140000000000000",
  "status": "PENDING",
  "created_at": 1714140000
}
```

### Get a job by ID

```bash
curl "http://localhost:8080/job?id=job_1714140000000000000"
```

Response includes: `id`, `status`, `created_at`, `started_at`, `retry_count`, `worker_id`, `payload`.

### List all jobs

```bash
curl http://localhost:8080/jobs/list
```

### Queue stats

```bash
curl http://localhost:8080/stats
```

Response:
```json
{
  "stream_length": 42,
  "pending": 3,
  "retry_queue": 1,
  "dead_letter": 0
}
```

### Dead Letter Queue

```bash
curl http://localhost:8080/jobs/dlq
```

### Health / Readiness

```bash
curl http://localhost:8080/health   # liveness (always 200 if process is alive)
curl http://localhost:8080/ready    # readiness (200 only if Redis is reachable)
```

---

## Job Lifecycle

```
POST /jobs
    │
    ▼
Redis Hash (job_data:<id>)    ← status: PENDING
Redis Stream (jobs_stream)    ← job_id + payload
    │
    ▼
Worker picks up message
    │
    ├─ status: RUNNING
    │
    ├─ success → status: COMPLETED
    │
    └─ failure → retry_count++
                │
                ├─ retry_count <= MAX_RETRY → status: FAILED → retry ZSET (backoff)
                │                                               └─ re-enqueued after delay
                └─ retry_count  > MAX_RETRY → status: DLQ → dead_letter_queue
```

---

## Environment Variables

| Variable              | Default          | Description                          |
|----------------------|-----------------|--------------------------------------|
| `APP_MODE`           | `api`           | `api` or `worker`                    |
| `REDIS_ADDR`         | `localhost:6379`| Redis address                        |
| `REDIS_PASSWORD`     | *(empty)*       | Redis password                       |
| `REDIS_POOL_SIZE`    | `100`           | Max Redis connections                |
| `REDIS_MIN_IDLE`     | `20`            | Min idle Redis connections           |
| `WORKER_ID`          | random          | Unique worker ID (set per pod in K8s)|
| `WORKER_CONCURRENCY` | `5`             | Goroutines per worker process        |
| `MAX_RETRY`          | `3`             | Max retries before DLQ               |
| `MAX_QUEUE_SIZE`     | `1000`          | Backpressure limit (pending messages)|
| `RECOVERY_TIMEOUT`   | `40`            | Seconds before a stuck job is claimed|

---

## Running Locally (without Docker)

You need Redis running locally first:

```bash
# Option 1: Docker
docker run -d -p 6379:6379 redis:7 redis-server --requirepass supersecret123

# Option 2: Local install
redis-server --requirepass supersecret123
```

Then:

```bash
# Start the API
make run-api

# In another terminal, start a worker
make run-worker
```

---

## Kubernetes

Manifests are in `deployments/kubernetes/`. See `scripts/k8sctl.sh` for deployment helpers.

---

## Metrics (Prometheus)

| Metric                  | Type      | Description                          |
|------------------------|-----------|--------------------------------------|
| `jobs_processed_total` | Counter   | Jobs processed (by worker)           |
| `jobs_succeeded_total` | Counter   | Jobs completed successfully          |
| `jobs_failed_total`    | Counter   | Jobs failed (by worker + reason)     |
| `job_duration_seconds` | Histogram | Processing time distribution         |
| `queue_depth`          | Gauge     | Current stream length                |
| `worker_active`        | Gauge     | 1 if worker is running, 0 if stopped |

---

## Rate Limiting

The API limits to **200 requests/second** per instance. Requests exceeding this receive `429 Too Many Requests`.

The queue also applies **backpressure**: if `pending > MAX_QUEUE_SIZE`, new jobs are rejected with `429 queue overloaded` until workers catch up.
