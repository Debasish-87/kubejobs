#!/bin/bash

set -e

# ---------------- CONFIG ----------------

NAMESPACE="kubejobs"
K8S_DIR="deployments/kubernetes"

# ---------------- DEP CHECK ----------------

function check_deps() {
for cmd in kubectl curl jq minikube; do
command -v $cmd >/dev/null || { echo "ERROR: $cmd not installed"; exit 1; }
done
}

function check_cluster() {
if ! kubectl cluster-info >/dev/null 2>&1; then
echo "ERROR: Kubernetes cluster not reachable"
echo "Run: minikube start"
exit 1
fi
}

# ---------------- NETWORK DISCOVERY ----------------

function get_api_url() {
MINIKUBE_IP=$(minikube ip)
NODE_PORT=$(kubectl get svc api -n $NAMESPACE -o jsonpath='{.spec.ports[0].nodePort}')

if [ -z "$NODE_PORT" ]; then
echo "ERROR: API service NodePort not found"
exit 1
fi

API_URL="http://$MINIKUBE_IP:$NODE_PORT"
echo "API URL: $API_URL"
}

# ---------------- CLEAN ----------------

function cleanup() {
echo "Cleaning previous deployment..."

kubectl delete scaledobject worker-scaler -n $NAMESPACE --ignore-not-found=true
kubectl delete hpa worker-hpa -n $NAMESPACE --ignore-not-found=true
}

# ---------------- APPLY ----------------

function apply_all() {
echo "Applying Kubernetes manifests..."

kubectl apply -f $K8S_DIR/namespace.yaml
kubectl apply -f $K8S_DIR/redis-secret.yaml
kubectl apply -f $K8S_DIR/limit-range.yaml
kubectl apply -f $K8S_DIR/resource-quota.yaml

kubectl apply -f $K8S_DIR/redis-service.yaml
kubectl apply -f $K8S_DIR/redis-statefulset.yaml
kubectl apply -f $K8S_DIR/redis-pdb.yaml

kubectl apply -f $K8S_DIR/api-deployment.yaml
kubectl apply -f $K8S_DIR/service.yaml

kubectl apply -f $K8S_DIR/worker-deployment.yaml

kubectl apply -f $K8S_DIR/keda-scaledobject.yaml
}

# ---------------- WAIT ----------------

function wait_ready() {
echo "Waiting for pods..."

kubectl wait --for=condition=ready pod -l app=redis -n $NAMESPACE --timeout=120s
kubectl wait --for=condition=available deployment/api -n $NAMESPACE --timeout=120s
kubectl wait --for=condition=available deployment/worker -n $NAMESPACE --timeout=120s

echo "All components ready"
}

# ---------------- TEST ----------------

function test_jobs() {
COUNT=${1:-100}
get_api_url

echo "Sending $COUNT jobs..."

for i in $(seq 1 $COUNT); do
curl -s -X POST $API_URL/jobs > /dev/null &
if [ $((i % 100)) -eq 0 ]; then sleep 0.1; fi
done

wait
echo "Jobs injected"
}

# ---------------- VALIDATE ----------------

function validate() {
get_api_url

echo "Validating system..."

RESPONSE=$(curl -s $API_URL/stats)
echo $RESPONSE | jq .

QUEUE=$(echo $RESPONSE | jq '.stream_length')

if [ "$QUEUE" -ge 0 ]; then
echo "System healthy"
else
echo "System failed"
exit 1
fi
}

# ---------------- FULL SETUP ----------------

function setup() {
echo "======================================"
echo "KubeJobs Kubernetes Setup"
echo "======================================"

check_cluster
cleanup
apply_all
wait_ready

echo "Running smoke test..."
test_jobs 50

sleep 5
validate

echo "======================================"
echo "SETUP COMPLETE"
echo "======================================"
}

# ---------------- STATUS ----------------

function status() {
kubectl get pods -n $NAMESPACE
}

function logs() {
kubectl logs -l app=worker -n $NAMESPACE --tail=50 -f
}

function down() {
kubectl delete namespace $NAMESPACE
}

# ---------------- ROUTER ----------------

check_deps

case "$1" in
setup) setup ;;
test) test_jobs $2 ;;
validate) validate ;;
status) status ;;
logs) logs ;;
down) down ;;
*) echo "Usage: ./k8sctl.sh [setup|test|validate|status|logs|down]" ;;
esac
