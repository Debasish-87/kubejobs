#!/bin/bash

# ── Auto-detect API URL ──────────────────────────────────────────
# Kubernetes (minikube) le lo agar available ho, warna localhost
if minikube status 2>/dev/null | grep -q "Running\|host: Running"; then
  MINIKUBE_IP=$(minikube ip 2>/dev/null)
  BASE_URL="http://$MINIKUBE_IP:30080"
else
  BASE_URL="http://localhost:8080"
fi

PASS=0
FAIL=0

GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass() { echo -e "${GREEN}[PASS]${NC} $1"; PASS=$((PASS + 1)); }
fail() { echo -e "${RED}[FAIL]${NC} $1"; FAIL=$((FAIL + 1)); }
info() { echo -e "${YELLOW}[INFO]${NC} $1"; }

echo "================================================"
echo "         KubeJobs Test Suite"
echo "================================================"
info "API URL: $BASE_URL"
echo ""

# ---- 1. Health Check ----
info "Test 1: Health check"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/health")
if [ "$STATUS" == "200" ]; then pass "Health endpoint returns 200"
else fail "Health endpoint returned $STATUS"; fi

# ---- 2. Readiness Check ----
echo ""
info "Test 2: Readiness check"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/ready")
if [ "$STATUS" == "200" ]; then pass "Ready endpoint returns 200"
else fail "Ready endpoint returned $STATUS"; fi

# ---- 3. Submit Single Job ----
echo ""
info "Test 3: Submit single job"
RESPONSE=$(curl -s -X POST "$BASE_URL/jobs" \
  -H "Content-Type: application/json" \
  -d '{"task": "test_job", "priority": "high"}')
JOB_ID=$(echo "$RESPONSE" | grep -o '"id":"[^"]*"' | cut -d'"' -f4)
if [ -n "$JOB_ID" ]; then pass "Job created with ID: $JOB_ID"
else fail "Job creation failed. Response: $RESPONSE"; fi

# ---- 4. Stats Check ----
echo ""
info "Test 4: Stats endpoint"
STATS=$(curl -s "$BASE_URL/stats")
if echo "$STATS" | grep -q "stream_length"; then pass "Stats endpoint working: $STATS"
else fail "Stats endpoint broken. Response: $STATS"; fi

# ---- 5. List Jobs ----
echo ""
info "Test 5: List jobs"
LIST=$(curl -s "$BASE_URL/jobs/list")
if echo "$LIST" | grep -q "count"; then
  COUNT=$(echo "$LIST" | grep -o '"count":[0-9]*' | cut -d':' -f2)
  pass "List jobs working. Total jobs: $COUNT"
else fail "List jobs failed. Response: $LIST"; fi

# ---- 6. Submit Batch Jobs + DLQ delta check ----
echo ""
info "Test 6: Submit 10 batch jobs"

DLQ_BEFORE=$(curl -s "$BASE_URL/stats" | grep -o '"dead_letter":[0-9]*' | cut -d':' -f2)
info "DLQ count before batch: ${DLQ_BEFORE:-0}"

BATCH_OK=0
for i in $(seq 1 10); do
  R=$(curl -s -X POST "$BASE_URL/jobs" \
    -H "Content-Type: application/json" \
    -d "{\"task\": \"batch_job_$i\", \"priority\": \"high\"}")
  echo "$R" | grep -q '"id"' && BATCH_OK=$((BATCH_OK + 1))
done

if [ "$BATCH_OK" == "10" ]; then pass "All 10 batch jobs submitted"
else fail "Only $BATCH_OK/10 jobs submitted"; fi

# ---- 7. Wait and Check Completion ----
echo ""
info "Test 7: Waiting 20s for jobs to process..."
sleep 20

STATS=$(curl -s "$BASE_URL/stats")
DLQ_AFTER=$(echo "$STATS" | grep -o '"dead_letter":[0-9]*' | cut -d':' -f2)
DLQ_NEW=$(( ${DLQ_AFTER:-0} - ${DLQ_BEFORE:-0} ))
PENDING=$(echo "$STATS" | grep -o '"pending":[0-9]*' | cut -d':' -f2)

info "Stats after processing: $STATS"
info "New DLQ jobs from this run: $DLQ_NEW (before=${DLQ_BEFORE:-0}, after=${DLQ_AFTER:-0})"

if [ "${DLQ_NEW}" -le "0" ]; then pass "No new jobs went to DLQ"
else fail "$DLQ_NEW new jobs went to DLQ — check worker timeout fix!"; fi

if [ "${PENDING:-0}" == "0" ]; then pass "No pending jobs stuck in queue"
else fail "${PENDING} jobs still pending"; fi

# ---- 8. Verify new jobs COMPLETED ----
echo ""
info "Test 8: Verify batch jobs completed"
LIST=$(curl -s "$BASE_URL/jobs/list")
COMPLETED=$(echo "$LIST" | grep -o '"status":"COMPLETED"' | wc -l)
if [ "$COMPLETED" -ge "10" ]; then pass "$COMPLETED jobs in COMPLETED state"
else fail "Only $COMPLETED jobs completed (expected >= 10)"; fi

# ---- 9. Invalid JSON ----
echo ""
info "Test 9: Invalid JSON rejected"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/jobs" \
  -H "Content-Type: application/json" \
  -d 'not_json')
if [ "$STATUS" == "400" ]; then pass "Invalid JSON returns 400"
else fail "Expected 400 but got $STATUS"; fi

# ---- 10. Empty Body Rejected ----
echo ""
info "Test 10: Empty body rejected"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE_URL/jobs" \
  -H "Content-Type: application/json")
if [ "$STATUS" == "400" ]; then pass "Empty body returns 400"
else fail "Expected 400 but got $STATUS"; fi

# ---- 11. Wrong Method Rejected ----
echo ""
info "Test 11: GET on /jobs rejected"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" -X GET "$BASE_URL/jobs")
if [ "$STATUS" == "405" ]; then pass "GET /jobs returns 405 Method Not Allowed"
else fail "Expected 405 but got $STATUS"; fi

# ---- 12. DLQ Endpoint ----
echo ""
info "Test 12: DLQ endpoint accessible"
DLQ_RESP=$(curl -s "$BASE_URL/jobs/dlq")
if echo "$DLQ_RESP" | grep -q "count"; then pass "DLQ endpoint working"
else fail "DLQ endpoint failed. Response: $DLQ_RESP"; fi

# ---- 13. Prometheus Metrics ----
echo ""
info "Test 13: Prometheus metrics exposed"
STATUS=$(curl -s -o /dev/null -w "%{http_code}" "$BASE_URL/metrics")
if [ "$STATUS" == "200" ]; then pass "Metrics endpoint returns 200"
else fail "Metrics endpoint returned $STATUS"; fi

# ---- Summary ----
echo ""
echo "================================================"
echo -e "  Results:  ${GREEN}$PASS passed${NC}  |  ${RED}$FAIL failed${NC}"
echo "================================================"

if [ "$FAIL" == "0" ]; then
  echo -e "${GREEN}  All tests passed!${NC}"
  exit 0
else
  echo -e "${RED}  Some tests failed.${NC}"
  exit 1
fi