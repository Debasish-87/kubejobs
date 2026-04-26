# ============================================================
# KubeJobs — Makefile
# ============================================================

ifneq (,$(wildcard .env))
  include .env
  export
endif

BINARY     = bin/kubejobs
DOCKER_DIR = deployments/docker
K8S_DIR    = deployments/kubernetes
K8S_SCRIPT = ./scripts/k8sctl.sh
GO         = go

NAMESPACE  = kubejobs
IMAGE      = kubejobs
TAG        = v2.0.0

.PHONY: help build run-api run-worker fmt vet \
        up down logs clean redis-cli \
        k8s-setup k8s-down k8s-status k8s-logs \
        k8s-test k8s-validate k8s-build k8s-clean \
        redis-flush

# Default target
help:
	@echo ""
	@echo "  KubeJobs — available targets"
	@echo "  ───────────────────────────────────────────"
	@echo ""
	@echo "  LOCAL DEV (Docker Compose)"
	@echo "  make up             Start all services via Docker Compose"
	@echo "  make down           Stop all Docker Compose services"
	@echo "  make logs           Tail Docker Compose logs"
	@echo "  make redis-cli      Open Redis CLI (Docker)"
	@echo "  make redis-flush    Flush all Redis data (Docker)"
	@echo ""
	@echo "  KUBERNETES (Minikube)"
	@echo "  make k8s-setup      Full deploy: build + apply manifests + smoke test"
	@echo "  make k8s-build      Build image inside Minikube only"
	@echo "  make k8s-down       Delete kubejobs namespace"
	@echo "  make k8s-clean      Full wipe: namespace + PVCs"
	@echo "  make k8s-status     Show pods, deployments, services, HPA"
	@echo "  make k8s-logs       Tail worker logs"
	@echo "  make k8s-test       Send 100 test jobs to Kubernetes API"
	@echo "  make k8s-validate   Health check all Kubernetes components"
	@echo ""
	@echo "  BUILD & QUALITY"
	@echo "  make build          Build binary to bin/kubejobs"
	@echo "  make run-api        Run API server locally"
	@echo "  make run-worker     Run worker locally"
	@echo "  make fmt            Format all Go files"
	@echo "  make vet            Run go vet"
	@echo "  make clean          Remove build artifacts"
	@echo ""


# ---- Build ----

build:
	@mkdir -p bin
	$(GO) build -ldflags="-s -w" -o $(BINARY) ./cmd/server
	@echo "Built: $(BINARY)"


# ---- Run locally ----

run-api: build
	APP_MODE=api ./$(BINARY)

run-worker: build
	APP_MODE=worker WORKER_ID=worker-local WORKER_CONCURRENCY=5 ./$(BINARY)


# ---- Quality ----

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...


# ============================================================
# DOCKER COMPOSE — local dev
# ============================================================

up:
	docker compose --env-file .env -f $(DOCKER_DIR)/docker-compose.yml up --build -d
	@echo ""
	@echo "  Services started:"
	@echo "    API        http://localhost:8080"
	@echo "    Prometheus http://localhost:9090"
	@echo "    Grafana    http://localhost:3000  (admin/admin)"
	@echo ""

down:
	docker compose -f $(DOCKER_DIR)/docker-compose.yml down

logs:
	docker compose -f $(DOCKER_DIR)/docker-compose.yml logs -f

redis-cli:
	docker compose -f $(DOCKER_DIR)/docker-compose.yml \
	  exec redis redis-cli -a $(REDIS_PASSWORD)

redis-flush:
	docker compose -f $(DOCKER_DIR)/docker-compose.yml \
	  exec redis redis-cli -a $(REDIS_PASSWORD) FLUSHALL
	@echo "Redis flushed."


# ============================================================
# KUBERNETES — minikube
# ============================================================

# Full deploy: build image inside minikube + apply all manifests + smoke test
k8s-setup:
	@minikube status | grep -q "Running\|host: Running" || (echo "Minikube not running. Run: minikube start" && exit 1)
	$(K8S_SCRIPT) setup

# Build Docker image inside Minikube only
k8s-build:
	$(K8S_SCRIPT) build

# Show cluster status
k8s-status:
	$(K8S_SCRIPT) status

# Tail worker logs (use k8s-logs component=api for API logs)
k8s-logs:
	$(K8S_SCRIPT) logs $(or $(component),worker)

# Send N test jobs (default 100). Usage: make k8s-test N=500
k8s-test:
	$(K8S_SCRIPT) test $(or $(N),100)

# Health check all components
k8s-validate:
	$(K8S_SCRIPT) validate

# Delete kubejobs namespace (keeps Minikube running)
k8s-down:
	$(K8S_SCRIPT) down

# Full wipe: namespace + PVCs
k8s-clean:
	$(K8S_SCRIPT) clean

# Apply only manifests (no rebuild)
k8s-apply:
	kubectl apply -f $(K8S_DIR)/namespace.yaml
	kubectl apply -f $(K8S_DIR)/limit-range.yaml
	kubectl apply -f $(K8S_DIR)/resource-quota.yaml
	kubectl apply -f $(K8S_DIR)/redis-secret.yaml
	kubectl apply -f $(K8S_DIR)/redis-service.yaml
	kubectl apply -f $(K8S_DIR)/redis-statefulset.yaml
	kubectl apply -f $(K8S_DIR)/redis-pdb.yaml
	kubectl apply -f $(K8S_DIR)/service.yaml
	kubectl apply -f $(K8S_DIR)/api-deployment.yaml
	kubectl apply -f $(K8S_DIR)/worker-deployment.yaml
	kubectl apply -f $(K8S_DIR)/hpa-worker.yaml
	kubectl apply -f $(K8S_DIR)/network-policy.yaml
	@echo "All manifests applied."

# Quick stats from Kubernetes API
k8s-stats:
	@MINIKUBE_IP=$$(minikube ip 2>/dev/null); \
	curl -s http://$$MINIKUBE_IP:30080/stats | python3 -m json.tool 2>/dev/null || \
	curl -s http://$$MINIKUBE_IP:30080/stats


# ---- Clean ----

clean:
	rm -rf bin/