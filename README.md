# KubeJobs — Docker-based Distributed Job Processing System

## Overview

KubeJobs is a distributed job processing system built using Go and Redis Streams.

This version (v1) runs entirely on Docker and demonstrates a scalable and fault-tolerant background job system in a local environment.

The system supports asynchronous job execution, retries, failure handling, and dynamic worker scaling.

---

## Problem Statement

Many real-world systems require asynchronous processing for:

* background jobs
* event-driven workflows
* batch processing pipelines

Common challenges:

* worker crashes during execution
* duplicate job processing
* retry handling with backoff
* stuck or unacknowledged jobs

This system is designed to handle these scenarios reliably.

---

## Architecture

```text
Client (API)
   ↓
Redis Streams (Job Queue)
   ↓
Worker Pool (Consumer Group)
   ↓
Processing
   ↓
 ├── Success → Completed
 ├── Failure → Retry Queue (ZSET)
 └── Max Retry → Dead Letter Queue (DLQ)

Recovery:
Pending Jobs → Reclaim → Reprocess
```

---

## Core Features

* Redis Streams-based job queue
* Consumer group worker model
* Concurrent worker execution
* Retry with exponential backoff
* Dead Letter Queue (DLQ)
* Job recovery using XCLAIM
* Idempotent processing using distributed locks
* Prometheus metrics integration
* Grafana dashboards
* Dynamic worker scaling (Docker-based autoscaler)

---

## Tech Stack

* Go (Golang)
* Redis
* Docker & Docker Compose
* Prometheus
* Grafana

---

## System Components

### API Service

* Accepts job requests
* Pushes jobs into Redis Stream
* Provides stats and health endpoints

### Worker Service

* Consumes jobs using Redis consumer groups
* Processes jobs concurrently
* Handles retries and failures

### Redis

* Acts as the job queue backend
* Stores job metadata and state

### Prometheus

* Collects metrics from API and workers

### Grafana

* Visualizes system performance

---

## Running the System

### Start all services

```bash
docker compose up
```

---

### Start with autoscaler

```bash
./scripts/devctl.sh dev
```

This will:

* start all containers
* start background autoscaler
* enable dynamic worker scaling

---

### Send test jobs

```bash
./scripts/devctl.sh test 500
```

---

### Check system stats

```bash
./scripts/devctl.sh stats
```

---

### View logs

```bash
./scripts/devctl.sh logs
```

---

## Autoscaling (Docker-based)

The system includes a custom autoscaler that adjusts worker count based on queue size.

Scaling rules:

* queue > 1000 → 20 workers
* queue > 500 → 10 workers
* queue > 100 → 5 workers
* queue > 0 → 2 workers
* queue empty → 1 worker

Scaling command:

```bash
docker compose up --scale worker=N
```

---

## Observability

### Prometheus

Collects metrics for:

* jobs processed
* job failures
* queue depth
* worker activity

### Grafana

Dashboards include:

* system load
* job latency
* throughput
* worker utilization

---

## Failure Handling

The system ensures reliability using:

* retry with exponential backoff
* maximum retry threshold
* dead letter queue (DLQ)
* recovery of stuck jobs using Redis XCLAIM

---

## Limitations (v1)

* Redis is single-node (no high availability)
* No orchestration (Docker only)
* No distributed tracing
* No CI/CD pipeline
* Basic security (no network isolation)

---

## Learning Outcomes

This project demonstrates:

* distributed system design using queues
* worker concurrency and parallel processing
* failure handling and retry strategies
* container-based system architecture
* observability fundamentals

---

## Project Evolution

* v1 — Docker-based distributed system (current)
* v2 — Kubernetes-based deployment
* v3 — Production-grade infrastructure

---

## License

MIT License
