#!/bin/bash
set -euo pipefail

# ================================================================
# k8sctl.sh — KubeJobs Kubernetes Control Script
# Usage: ./k8sctl.sh [setup|build|test|validate|status|logs|down|clean]
# ================================================================

# ---------------- CONFIG ----------------

NAMESPACE="kubejobs"
K8S_DIR="deployments/kubernetes"
IMAGE_NAME="kubejobs"
IMAGE_TAG="v2.0.0"
DOCKERFILE="deployments/docker/Dockerfile"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# ---------------- LOGGING ----------------

function log_info()    { echo -e "${BLUE}[INFO]${NC}  $*"; }
function log_ok()      { echo -e "${GREEN}[OK]${NC}    $*"; }
function log_warn()    { echo -e "${YELLOW}[WARN]${NC}  $*"; }
function log_error()   { echo -e "${RED}[ERROR]${NC} $*"; }

function log_banner() {
    echo ""
    echo -e "${BLUE}=====================================================${NC}"
    echo -e "${BLUE}  $*${NC}"
    echo -e "${BLUE}=====================================================${NC}"
    echo ""
}

# ---------------- DEP CHECK ----------------

function check_deps() {
    log_info "Checking required tools..."

    local missing=0

    for cmd in kubectl curl jq minikube docker; do
        if command -v "$cmd" >/dev/null 2>&1; then
            log_ok "$cmd found ($(command -v $cmd))"
        else
            log_error "$cmd is not installed or not in PATH"
            missing=$((missing + 1))
        fi
    done

    # Check KEDA is installed in cluster (non-fatal warning)
    if kubectl get crd scaledobjects.keda.sh >/dev/null 2>&1; then
        log_ok "KEDA CRDs found"
    else
        log_warn "KEDA CRDs not found — ScaledObject will fail"
        log_warn "Install KEDA: helm install keda kedacore/keda -n keda --create-namespace"
    fi

    if [ "$missing" -gt 0 ]; then
        log_error "$missing required tool(s) missing. Aborting."
        exit 1
    fi

    log_ok "All dependencies satisfied"
}

function check_cluster() {
    log_info "Checking Kubernetes cluster connectivity..."

    if ! kubectl cluster-info >/dev/null 2>&1; then
        log_error "Kubernetes cluster not reachable"
        log_error "Run: minikube start --cpus=4 --memory=4096 --driver=docker"
        exit 1
    fi

    local context
    context=$(kubectl config current-context)
    log_ok "Connected to cluster (context: $context)"
}

# ---------------- BUILD IMAGE ----------------
# Builds the Docker image inside Minikube's Docker daemon so
# Kubernetes can pull it with imagePullPolicy: IfNotPresent.

function build_image() {
    log_banner "Building Docker Image inside Minikube"

    check_cluster

    log_info "Pointing Docker CLI at Minikube's daemon..."
    eval "$(minikube docker-env)"

    log_info "Building $IMAGE_NAME:$IMAGE_TAG ..."
    docker build \
        -f "$DOCKERFILE" \
        -t "$IMAGE_NAME:$IMAGE_TAG" \
        .

    log_ok "Image built: $IMAGE_NAME:$IMAGE_TAG"

    # Restore host Docker env
    eval "$(minikube docker-env --unset)" 2>/dev/null || true
}

# ---------------- NETWORK DISCOVERY ----------------

function get_api_url() {
    local minikube_ip node_port

    minikube_ip=$(minikube ip 2>/dev/null)
    if [ -z "$minikube_ip" ]; then
        log_error "Could not get Minikube IP. Is Minikube running?"
        exit 1
    fi

    node_port=$(kubectl get svc api \
        -n "$NAMESPACE" \
        -o jsonpath='{.spec.ports[0].nodePort}' 2>/dev/null || echo "")

    if [ -z "$node_port" ]; then
        log_error "API service NodePort not found. Is the API deployed?"
        exit 1
    fi

    API_URL="http://$minikube_ip:$node_port"
    log_info "API URL: $API_URL"
}

# ---------------- CLEAN PREVIOUS DEPLOYMENT ----------------

function cleanup() {
    log_banner "Cleaning Previous Deployment"

    # Remove autoscalers first (they hold finalizers)
    kubectl delete scaledobject worker-scaler \
        -n "$NAMESPACE" --ignore-not-found=true
    kubectl delete hpa worker-hpa \
        -n "$NAMESPACE" --ignore-not-found=true

    # Remove deployments
    kubectl delete deployment api worker \
        -n "$NAMESPACE" --ignore-not-found=true

    # Remove Redis StatefulSet (but keep PVC for data safety)
    kubectl delete statefulset redis \
        -n "$NAMESPACE" --ignore-not-found=true

    log_ok "Previous deployment cleaned"
}

# ---------------- FULL SYSTEM WIPE ----------------
# Wipes the entire namespace + all PVCs. Use before a clean install.

function clean_all() {
    log_banner "Full System Clean"

    log_warn "This will DELETE the entire '$NAMESPACE' namespace and all data."
    read -r -p "Are you sure? (yes/no): " confirm

    if [ "$confirm" != "yes" ]; then
        log_info "Aborted."
        exit 0
    fi

    # Delete namespace (removes everything inside it)
    kubectl delete namespace "$NAMESPACE" --ignore-not-found=true

    log_info "Waiting for namespace to fully terminate..."
    local timeout=60
    local elapsed=0
    while kubectl get namespace "$NAMESPACE" >/dev/null 2>&1; do
        sleep 2
        elapsed=$((elapsed + 2))
        if [ "$elapsed" -ge "$timeout" ]; then
            log_warn "Namespace still terminating after ${timeout}s. Continuing anyway."
            break
        fi
        echo -n "."
    done
    echo ""

    log_ok "Namespace '$NAMESPACE' deleted"

    # Also wipe Redis PVCs if any remain
    kubectl delete pvc \
        -l app=redis \
        -n "$NAMESPACE" --ignore-not-found=true 2>/dev/null || true

    log_ok "Full clean complete"
}

# ---------------- APPLY ALL MANIFESTS ----------------

function apply_all() {
    log_banner "Applying Kubernetes Manifests"

    # 1. Namespace
    log_info "Creating namespace..."
    kubectl apply -f "$K8S_DIR/namespace.yaml"

    # 2. Limits and quotas
    log_info "Applying resource limits..."
    kubectl apply -f "$K8S_DIR/limit-range.yaml"
    kubectl apply -f "$K8S_DIR/resource-quota.yaml"

    # 3. Secrets
    log_info "Applying secrets..."
    kubectl apply -f "$K8S_DIR/redis-secret.yaml"

    # 4. Redis — service must exist before StatefulSet so DNS resolves
    log_info "Deploying Redis..."
    kubectl apply -f "$K8S_DIR/redis-service.yaml"
    kubectl apply -f "$K8S_DIR/redis-statefulset.yaml"
    kubectl apply -f "$K8S_DIR/redis-pdb.yaml"

    # 5. Wait for Redis before deploying app (app fails fast if Redis is absent)
    log_info "Waiting for Redis pod to be ready (timeout: 120s)..."
    kubectl wait \
        --for=condition=ready pod \
        -l app=redis \
        -n "$NAMESPACE" \
        --timeout=120s
    log_ok "Redis is ready"

    # 6. API
    log_info "Deploying API..."
    kubectl apply -f "$K8S_DIR/service.yaml"
    kubectl apply -f "$K8S_DIR/api-deployment.yaml"

    # 7. Worker
    log_info "Deploying Worker..."
    kubectl apply -f "$K8S_DIR/worker-deployment.yaml"

    # 8. Autoscaling
    log_info "Applying autoscaling rules..."
    kubectl apply -f "$K8S_DIR/hpa-worker.yaml"         2>/dev/null || \
        log_warn "hpa-worker.yaml not found — skipping HPA"
    kubectl apply -f "$K8S_DIR/keda-scaledobject.yaml"  2>/dev/null || \
        log_warn "keda-scaledobject.yaml not found — skipping KEDA"

    # 9. Networking
    log_info "Applying network policies and ingress..."
    kubectl apply -f "$K8S_DIR/network-policy.yaml"     2>/dev/null || \
        log_warn "network-policy.yaml not found — skipping"
    kubectl apply -f "$K8S_DIR/ingress.yaml"            2>/dev/null || \
        log_warn "ingress.yaml not found — skipping"

    log_ok "All manifests applied"
}

# ---------------- WAIT FOR READY ----------------

function wait_ready() {
    log_banner "Waiting for All Components"

    log_info "Waiting for API deployment (timeout: 120s)..."
    kubectl wait \
        --for=condition=available deployment/api \
        -n "$NAMESPACE" \
        --timeout=120s
    log_ok "API deployment ready"

    log_info "Waiting for Worker deployment (timeout: 120s)..."
    kubectl wait \
        --for=condition=available deployment/worker \
        -n "$NAMESPACE" \
        --timeout=120s
    log_ok "Worker deployment ready"

    # Health check via HTTP
    get_api_url
    log_info "Checking API /health endpoint..."
    local retries=10
    local wait_sec=5
    for i in $(seq 1 "$retries"); do
        local http_code
        http_code=$(curl -s -o /dev/null -w "%{http_code}" \
            --connect-timeout 3 "$API_URL/health" || echo "000")

        if [ "$http_code" = "200" ]; then
            log_ok "API /health returned 200"
            break
        fi

        log_warn "Attempt $i/$retries: /health returned $http_code — retrying in ${wait_sec}s..."
        sleep "$wait_sec"

        if [ "$i" -eq "$retries" ]; then
            log_error "API /health did not return 200 after $retries attempts"
            exit 1
        fi
    done

    # Readiness check (checks Redis connectivity from inside API)
    log_info "Checking API /ready endpoint..."
    local ready_code
    ready_code=$(curl -s -o /dev/null -w "%{http_code}" \
        --connect-timeout 3 "$API_URL/ready" || echo "000")

    if [ "$ready_code" = "200" ]; then
        log_ok "API /ready returned 200 — Redis is reachable"
    else
        log_warn "API /ready returned $ready_code — Redis may not be healthy yet"
    fi

    log_ok "All components are ready"
}

# ---------------- SEND TEST JOBS ----------------

function test_jobs() {
    local count="${1:-100}"

    log_banner "Sending $count Test Jobs"

    get_api_url

    # Validate the API is reachable before starting
    local http_code
    http_code=$(curl -s -o /dev/null -w "%{http_code}" \
        --connect-timeout 5 "$API_URL/health" || echo "000")

    if [ "$http_code" != "200" ]; then
        log_error "API not reachable at $API_URL (got HTTP $http_code)"
        exit 1
    fi

    log_info "Submitting $count jobs to $API_URL/jobs ..."

    local success=0
    local failed=0

    for i in $(seq 1 "$count"); do
        response=$(curl -s -o /dev/null -w "%{http_code}" \
            -X POST "$API_URL/jobs" \
            -H "Content-Type: application/json" \
            -d "{\"index\": $i, \"source\": \"k8sctl-test\"}" \
            --connect-timeout 5 &)

        # Throttle: pause briefly every 100 requests to avoid overwhelming the rate limiter
        if [ $(( i % 100 )) -eq 0 ]; then
            sleep 0.2
            log_info "  Submitted $i / $count jobs..."
        fi
    done

    wait

    log_ok "$count jobs submitted"
    log_info "Waiting 3s for jobs to register in stream..."
    sleep 3

    # Show current queue stats
    local stats
    stats=$(curl -s "$API_URL/stats" 2>/dev/null || echo "{}")
    echo ""
    echo "$stats" | jq . 2>/dev/null || echo "$stats"
}

# ---------------- VALIDATE ----------------

function validate() {
    log_banner "Validating System Health"

    get_api_url

    # 1. Health endpoint
    local health_code
    health_code=$(curl -s -o /dev/null -w "%{http_code}" \
        --connect-timeout 5 "$API_URL/health" || echo "000")

    if [ "$health_code" = "200" ]; then
        log_ok "/health → $health_code"
    else
        log_error "/health → $health_code (expected 200)"
        exit 1
    fi

    # 2. Readiness endpoint
    local ready_code
    ready_code=$(curl -s -o /dev/null -w "%{http_code}" \
        --connect-timeout 5 "$API_URL/ready" || echo "000")

    if [ "$ready_code" = "200" ]; then
        log_ok "/ready → $ready_code (Redis reachable)"
    else
        log_warn "/ready → $ready_code (Redis may be degraded)"
    fi

    # 3. Stats endpoint
    local stats
    stats=$(curl -s --connect-timeout 5 "$API_URL/stats" 2>/dev/null || echo "")

    if [ -z "$stats" ]; then
        log_error "/stats returned empty response"
        exit 1
    fi

    echo ""
    log_info "Queue stats:"
    echo "$stats" | jq . 2>/dev/null || echo "$stats"
    echo ""

    local stream_length
    stream_length=$(echo "$stats" | jq -r '.stream_length // -1' 2>/dev/null || echo "-1")

    if [ "$stream_length" -ge 0 ] 2>/dev/null; then
        log_ok "stream_length=$stream_length — system is healthy"
    else
        log_error "Could not parse stream_length from stats"
        exit 1
    fi

    # 4. Pod status
    log_info "Pod status:"
    kubectl get pods -n "$NAMESPACE" \
        -o custom-columns='NAME:.metadata.name,STATUS:.status.phase,READY:.status.containerStatuses[0].ready'

    echo ""

    # 5. Check for any CrashLoopBackOff or Error pods
    local bad_pods
    bad_pods=$(kubectl get pods -n "$NAMESPACE" \
        --field-selector='status.phase!=Running,status.phase!=Succeeded' \
        -o name 2>/dev/null || echo "")

    if [ -n "$bad_pods" ]; then
        log_warn "Pods not in Running/Succeeded state:"
        echo "$bad_pods"
    else
        log_ok "All pods in healthy state"
    fi

    # 6. HPA status
    if kubectl get hpa -n "$NAMESPACE" >/dev/null 2>&1; then
        log_info "HPA status:"
        kubectl get hpa -n "$NAMESPACE"
    fi

    # 7. KEDA ScaledObject status
    if kubectl get scaledobject -n "$NAMESPACE" >/dev/null 2>&1; then
        log_info "KEDA ScaledObject status:"
        kubectl get scaledobject -n "$NAMESPACE"
    fi

    echo ""
    log_ok "Validation complete"
}

# ---------------- STATUS ----------------

function status() {
    log_banner "KubeJobs Cluster Status"

    echo -e "${YELLOW}--- Pods ---${NC}"
    kubectl get pods -n "$NAMESPACE" -o wide 2>/dev/null || \
        log_warn "Namespace $NAMESPACE not found"

    echo ""
    echo -e "${YELLOW}--- Deployments ---${NC}"
    kubectl get deployments -n "$NAMESPACE" 2>/dev/null || true

    echo ""
    echo -e "${YELLOW}--- Services ---${NC}"
    kubectl get services -n "$NAMESPACE" 2>/dev/null || true

    echo ""
    echo -e "${YELLOW}--- HPA ---${NC}"
    kubectl get hpa -n "$NAMESPACE" 2>/dev/null || \
        log_warn "No HPA found"

    echo ""
    echo -e "${YELLOW}--- KEDA ScaledObjects ---${NC}"
    kubectl get scaledobject -n "$NAMESPACE" 2>/dev/null || \
        log_warn "No ScaledObjects found (KEDA may not be installed)"

    echo ""
    echo -e "${YELLOW}--- PVCs ---${NC}"
    kubectl get pvc -n "$NAMESPACE" 2>/dev/null || true

    echo ""
    echo -e "${YELLOW}--- Events (last 10) ---${NC}"
    kubectl get events -n "$NAMESPACE" \
        --sort-by='.lastTimestamp' 2>/dev/null | tail -10 || true
}

# ---------------- LOGS ----------------

function show_logs() {
    local component="${1:-worker}"

    case "$component" in
        worker)
            log_info "Tailing worker logs (Ctrl+C to stop)..."
            kubectl logs -l app=worker \
                -n "$NAMESPACE" --tail=80 -f
            ;;
        api)
            log_info "Tailing API logs (Ctrl+C to stop)..."
            kubectl logs -l app=api \
                -n "$NAMESPACE" --tail=80 -f
            ;;
        redis)
            log_info "Tailing Redis logs (Ctrl+C to stop)..."
            kubectl logs redis-0 \
                -n "$NAMESPACE" --tail=80 -f
            ;;
        all)
            log_info "Tailing all pod logs (Ctrl+C to stop)..."
            kubectl logs \
                -n "$NAMESPACE" \
                --selector='app in (api,worker)' \
                --tail=50 -f
            ;;
        *)
            log_error "Unknown component: $component"
            log_error "Usage: ./k8sctl.sh logs [worker|api|redis|all]"
            exit 1
            ;;
    esac
}

# ---------------- DOWN ----------------

function down() {
    log_banner "Tearing Down KubeJobs"

    log_warn "This will delete the '$NAMESPACE' namespace."
    read -r -p "Continue? (yes/no): " confirm

    if [ "$confirm" != "yes" ]; then
        log_info "Aborted."
        exit 0
    fi

    kubectl delete namespace "$NAMESPACE" --ignore-not-found=true

    log_ok "Namespace '$NAMESPACE' deleted"
    log_info "Note: Minikube cluster is still running. Use 'minikube delete' to fully remove it."
}

# ---------------- FULL SETUP ----------------

function setup() {
    log_banner "KubeJobs Full Kubernetes Setup"

    check_deps
    check_cluster

    # Build image inside Minikube so k8s can pull it
    build_image

    # Remove only autoscalers and deployments — keep namespace/secrets if they exist
    cleanup

    # Apply all manifests in correct order
    apply_all

    # Wait for everything to be ready
    wait_ready

    # Smoke test
    log_info "Running smoke test (50 jobs)..."
    test_jobs 50

    sleep 3

    # Full validation
    validate

    log_banner "SETUP COMPLETE"

    get_api_url
    echo ""
    echo -e "  ${GREEN}API:${NC}        $API_URL"
    echo -e "  ${GREEN}Health:${NC}     curl $API_URL/health"
    echo -e "  ${GREEN}Stats:${NC}      curl $API_URL/stats | jq ."
    echo -e "  ${GREEN}Submit job:${NC} curl -X POST $API_URL/jobs -H 'Content-Type: application/json' -d '{\"type\":\"test\"}'"
    echo -e "  ${GREEN}Logs:${NC}       ./k8sctl.sh logs worker"
    echo -e "  ${GREEN}Status:${NC}     ./k8sctl.sh status"
    echo -e "  ${GREEN}Teardown:${NC}   ./k8sctl.sh down"
    echo ""
}

# ---------------- WATCH ----------------
# Live-refresh status every N seconds

function watch_status() {
    local interval="${1:-5}"
    log_info "Watching status every ${interval}s — Ctrl+C to stop"
    watch -n "$interval" "
        echo '=== Pods ===' && \
        kubectl get pods -n $NAMESPACE -o wide 2>/dev/null && \
        echo '' && echo '=== HPA ===' && \
        kubectl get hpa -n $NAMESPACE 2>/dev/null && \
        echo '' && echo '=== ScaledObject ===' && \
        kubectl get scaledobject -n $NAMESPACE 2>/dev/null
    "
}

# ---------------- ENTRY POINT ----------------

# Always check deps before running any command
check_deps

case "${1:-}" in
    setup)
        setup
        ;;
    build)
        build_image
        ;;
    clean)
        clean_all
        ;;
    test)
        test_jobs "${2:-100}"
        ;;
    validate)
        validate
        ;;
    status)
        status
        ;;
    logs)
        show_logs "${2:-worker}"
        ;;
    watch)
        watch_status "${2:-5}"
        ;;
    down)
        down
        ;;
    help|--help|-h|"")
        echo ""
        echo "Usage: ./k8sctl.sh <command> [args]"
        echo ""
        echo "Commands:"
        echo "  setup              Full deploy: build image + apply manifests + smoke test"
        echo "  build              Build Docker image inside Minikube"
        echo "  clean              Delete entire namespace + PVCs (fresh start)"
        echo "  test [N]           Send N jobs to the API (default: 100)"
        echo "  validate           Health check: endpoints + pods + HPA + KEDA"
        echo "  status             Show pods, deployments, services, HPA, events"
        echo "  logs [component]   Tail logs: worker (default) | api | redis | all"
        echo "  watch [seconds]    Live-refresh status every N seconds (default: 5)"
        echo "  down               Delete the kubejobs namespace"
        echo "  help               Show this help message"
        echo ""
        echo "Examples:"
        echo "  ./k8sctl.sh setup"
        echo "  ./k8sctl.sh test 500"
        echo "  ./k8sctl.sh logs api"
        echo "  ./k8sctl.sh watch 3"
        echo "  ./k8sctl.sh down"
        echo ""
        ;;
    *)
        log_error "Unknown command: $1"
        echo "Run './k8sctl.sh help' to see available commands."
        exit 1
        ;;
esac