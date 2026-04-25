# KubeJobs — Kubernetes-based Distributed Job Processing System

## Overview

KubeJobs is a fault-tolerant distributed job processing system built using Go and Redis Streams.

It supports asynchronous background job execution with strong guarantees around:

* retry handling
* failure recovery
* idempotent processing
* queue-based scaling

The system is designed to simulate real-world distributed workloads and demonstrate infrastructure-level thinking.

---

## System Evolution

### v1 — Docker-based System

* Containerized services using Docker Compose
* Custom autoscaler based on queue depth
* Prometheus + Grafana for observability

### v2 — Kubernetes-based System

* API and Worker deployed as Kubernetes Deployments
* Redis deployed using StatefulSet with persistent storage
* Horizontal Pod Autoscaler (HPA)
* KEDA-based queue-driven autoscaling
* ResourceQuota and LimitRange for resource control
* Liveness and Readiness probes

---

## Architecture

```
Client (API)
   ↓
Redis Streams (Job Queue)
   ↓
Worker Pool (Consumer Group)
   ↓
Processing
   ↓
 ├── Success → Completed
 ├── Failure → Retry (ZSET with backoff)
 └── Max Retry → Dead Letter Queue (DLQ)

Recovery System:
Pending Jobs → XCLAIM → Reprocess
```

---

## Core Features

### Job Processing

* Redis Streams-based queue
* Consumer group worker model
* Concurrent job execution
* Idempotent processing using locks

### Reliability

* Retry with exponential backoff
* Dead Letter Queue (DLQ)
* Recovery of stuck jobs using XCLAIM
* Graceful worker shutdown

### Observability

* Prometheus metrics
* Job processing latency histogram
* Queue depth tracking
* Worker health metrics

### Autoscaling

#### Docker (v1)

* Custom autoscaler script based on queue size

#### Kubernetes (v2)

* HPA (CPU + Memory based scaling)
* KEDA (Redis Streams queue-based scaling)

---

## Tech Stack

* Go (Golang)
* Redis (Streams + ZSET + Hash)
* Docker & Docker Compose
* Kubernetes
* Prometheus
* Grafana
* KEDA

---

## Project Structure

```
cmd/server           → application entrypoint  
internal/            → core logic (worker, redis, recovery, metrics)  
deployments/docker   → docker-compose setup  
deployments/kubernetes → k8s manifests (deployments, HPA, KEDA)  
scripts/             → dev automation tools  
```

---

## Running the System

### Docker (v1)

Start system:

```
docker compose up
```

Start with autoscaler:

```
./scripts/devctl.sh dev
```

Send load:

```
./scripts/devctl.sh test 500
```

---

### Kubernetes (v2)

Apply resources:

```
kubectl apply -f deployments/kubernetes/
```

Check pods:

```
kubectl get pods -n kubejobs
```

---

## Scaling Behavior

### Worker Scaling

* HPA scales based on CPU and memory usage
* KEDA scales based on Redis stream backlog

### Example Scaling Logic (Docker autoscaler)

* queue > 1000 → 20 workers
* queue > 500 → 10 workers
* queue > 100 → 5 workers
* queue > 0 → 2 workers
* idle → 1 worker

---

## Failure Handling

The system ensures reliability through:

* exponential retry scheduling using Redis ZSET
* job state tracking in Redis Hash
* DLQ for failed jobs
* recovery of stuck jobs using XCLAIM
* duplicate prevention via distributed locks

---

## Limitations (Current)

* Redis is single-node (no HA)
* No distributed tracing (OpenTelemetry missing)
* No CI/CD pipeline
* No network isolation or TLS
* Image versioning not enforced (`latest` used in some configs)

---

## What This Project Demonstrates

* Distributed system design using queues
* Worker concurrency and coordination
* Failure handling and retry strategies
* Observability and metrics design
* Container orchestration (Docker + Kubernetes)
* Autoscaling strategies (HPA + KEDA)

---

## Next Steps (v3 — Production Infra)

Planned improvements:

* Redis High Availability (Sentinel / Cluster)
* Distributed tracing (OpenTelemetry)
* CI/CD pipeline (GitHub Actions)
* Chaos testing (failure simulation)
* Security hardening (Secrets, NetworkPolicy, TLS)

---

## License

MIT License
