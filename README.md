# KubeJobs — Distributed Job Processing System (Production-Oriented)

## Overview

KubeJobs is a **distributed, fault-tolerant job processing system** built using Go and Redis Streams.

It is designed for **high-throughput asynchronous workloads** and implements:

* reliable job execution
* retry orchestration with backoff
* failure recovery for stuck jobs
* idempotent processing guarantees
* queue-driven autoscaling

This system models **real-world distributed systems behavior**, not just basic background job execution.

---

## System Evolution

### v1 — Docker-Based Runtime

* Docker Compose-based multi-service setup
* Redis-backed queue with worker pool
* Custom autoscaler driven by queue depth
* Prometheus + Grafana for metrics

### v2 — Kubernetes-Based Runtime

* API and Worker as independent Deployments
* Redis deployed via StatefulSet with persistence
* Horizontal Pod Autoscaler (CPU + Memory signals)
* KEDA for queue-driven autoscaling (Redis Streams lag)
* ResourceQuota and LimitRange enforcement
* Liveness and Readiness probes for resiliency

---

## Architecture

```
Client
  ↓
API Layer (Rate Limit + Backpressure)
  ↓
Redis Streams (jobs_stream)
  ↓
Consumer Group (workers)
  ↓
Processing Engine
  ↓
 ├── Success → Completed
 ├── Failure → Retry Queue (ZSET + backoff)
 └── Max Retry → Dead Letter Queue (DLQ)

Recovery System:
Pending → XAUTOCLAIM / XCLAIM → Reprocessing
```

---

## Core Features

### Job Processing

* Redis Streams as primary queue
* Consumer group model for horizontal scaling
* Concurrent worker execution
* Distributed locking (`SETNX`) for idempotency

---

### Reliability & Fault Tolerance

* Exponential retry with jitter (prevents retry storms)
* Dead Letter Queue for poison jobs
* Recovery loop using `XPENDING` + `XCLAIM`
* Graceful shutdown with in-flight job draining

---

### Backpressure & Rate Control

* API-level rate limiting (~200 req/sec)
* Queue depth–based rejection (overload protection)
* Prevents unbounded queue growth

---

### Observability

* Prometheus metrics exposed by API and Worker
* Key metrics:

  * job throughput
  * failure rate
  * latency histogram
  * queue depth
  * worker health

---

### Autoscaling

#### Docker (v1)

* Custom autoscaler based on queue thresholds

#### Kubernetes (v2)

**HPA (Resource-based scaling)**

* CPU target: 60%
* Memory target: 70%

**KEDA (Demand-based scaling)**

* Trigger: Redis Stream lag
* Fast polling interval (2s)
* Scale range: 2 → 50 workers

---

## Tech Stack

* Go (Golang)
* Redis (Streams, ZSET, Hash)
* Docker / Docker Compose
* Kubernetes
* Prometheus
* Grafana
* KEDA

---

## Project Structure

```
cmd/server               → application entrypoint  
internal/
  ├── worker            → job execution engine
  ├── redis             → client + connection management
  ├── recovery          → stuck job recovery logic
  ├── metrics           → Prometheus instrumentation
  ├── config            → environment configuration
deployments/
  ├── docker            → local development stack
  ├── kubernetes        → production manifests
scripts/
  ├── k8sctl.sh         → cluster automation
```

---

## Running the System

### Docker (Local Development)

```bash
docker compose up --build
```

Load testing:

```bash
./scripts/devctl.sh test 500
```

---

### Kubernetes (Cluster Deployment)

```bash
./scripts/k8sctl.sh setup
```

Verify:

```bash
kubectl get pods -n kubejobs
```

---

## Scaling Behavior

### Worker Scaling Signals

* CPU utilization (HPA)
* Memory utilization (HPA)
* Queue backlog (KEDA)

### Example Scaling Logic (v1 reference)

* queue > 1000 → 20 workers
* queue > 500 → 10 workers
* queue > 100 → 5 workers
* queue > 0 → 2 workers
* idle → 1 worker

---

## Failure Handling Model

The system guarantees **at-least-once processing** through:

* Redis Streams acknowledgment (`XACK`)
* Retry scheduling via ZSET
* Job state stored in Redis Hash
* Distributed locks to prevent duplication

Failure paths:

1. **Transient failure → retry**
2. **Repeated failure → DLQ**
3. **Worker crash → recovery loop reclaims job**

---

## Observability Endpoints

* API → `/metrics`, `/health`, `/ready`
* Worker → `/metrics`

---

## Current Limitations (Critical)

This system is **not production-safe yet**. Key gaps:

### Infrastructure

* Redis is single-node (SPOF)
* No sharding of job streams

### Security

* No TLS (internal or external)
* No authentication on API
* Redis password exposed in configs
* No NetworkPolicy isolation

### Deployment

* Image versioning not enforced (`latest`)
* No CI/CD pipeline
* No rollback strategy

### Observability

* No distributed tracing (OpenTelemetry missing)
* Limited debugging visibility (job payload not stored)

---

## What This Project Actually Demonstrates

* Real distributed queue design using Redis Streams
* Worker coordination via consumer groups
* Retry orchestration and backoff strategies
* Failure recovery under worker crashes
* Autoscaling using both resource and demand signals
* Production-style Kubernetes deployment patterns

---

## What Will Break Under Real Load

Let’s be precise:

* Redis becomes bottleneck → no horizontal scaling
* Single stream → throughput ceiling
* Retry storms → ZSET amplification
* Lock contention → latency spikes
* No payload visibility → debugging pain

---

## Next Steps (v3 — Production Hardening)

### Infrastructure

* Redis Cluster / Sentinel (HA)
* Stream sharding (multiple queues)

### Security

* TLS (Ingress + Redis)
* API authentication (JWT / API keys)
* Secret management (Vault / K8s best practices)
* NetworkPolicy enforcement

### Observability

* OpenTelemetry tracing
* Structured logging (JSON)

### CI/CD

* GitHub Actions pipeline
* Image tagging (no `latest`)
* Blue/Green or Canary deployment

### System Enhancements

* Job payload support
* Priority queues
* Rate-aware retry control
* Multi-tenant isolation

---

## License

MIT License
