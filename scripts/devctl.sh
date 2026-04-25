#!/bin/bash

set -e

# ---------------- PATH ----------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
COMPOSE_FILE="$PROJECT_ROOT/deployments/docker/docker-compose.yml"

# ---------------- CONFIG ----------------

COOLDOWN=5  # Down from 10 for faster response
AUTOSCALER_PID_FILE="/tmp/kubejobs_autoscaler.pid"
AUTOSCALER_LOG="/tmp/kubejobs_autoscaler.log"

# ---------------- VALIDATION ----------------

if [ ! -f "$COMPOSE_FILE" ]; then
  echo "ERROR: Compose file not found at: $COMPOSE_FILE"
  exit 1
fi

# ---------------- DEPENDENCY CHECK ----------------

function check_deps() {
  command -v docker >/dev/null || { echo "ERROR: docker not installed"; exit 1; }
  command -v curl >/dev/null || { echo "ERROR: curl not installed"; exit 1; }
  command -v jq >/dev/null || { echo "ERROR: jq not installed"; exit 1; }
}

# ---------------- HELP ----------------

function help() {
  echo "Usage:"
  echo "  ./devctl.sh dev          # Start system + autoscaler"
  echo "  ./devctl.sh test N       # Send N jobs (e.g., ./devctl.sh test 500)"
  echo "  ./devctl.sh status       # Show running containers"
  echo "  ./devctl.sh stats        # Show Redis queue stats"
  echo "  ./devctl.sh logs         # Follow container logs"
  echo "  ./devctl.sh autoscale-log # Watch autoscaler activity"
  echo "  ./devctl.sh down         # Stop everything"
  echo "  ./devctl.sh reset        # Wipe everything including volumes"
}

# ---------------- HEALTH ----------------

function wait_for_api() {
  echo "Waiting for API..."
  for i in {1..40}; do
    if curl -s http://localhost:8080/health >/dev/null; then
      echo "API ready"
      return
    fi
    sleep 1
  done
  echo "ERROR: API failed"
  exit 1
}

function wait_for_prometheus() {
  echo "Waiting for Prometheus..."
  for i in {1..30}; do
    if curl -s http://localhost:9090/-/ready >/dev/null; then
      echo "Prometheus ready"
      return
    fi
    sleep 1
  done
  echo "WARNING: Prometheus not ready"
}

# ---------------- START ----------------

function up() {
  echo "Starting system..."
  docker compose -f "$COMPOSE_FILE" up -d
  wait_for_api
  wait_for_prometheus
}

# ---------------- DEV (The Auto-Start Command) ----------------

function dev() {
  echo "Initializing DEV environment..."
  
  up                # 1. Start containers
  start_autoscaler  # 2. Start background scaling logic
  
  echo "------------------------------------------------"
  echo "DEV SYSTEM IS LIVE"
  echo "1. Run './scripts/devctl.sh test 500' to push load"
  echo "2. Run './scripts/devctl.sh autoscale-log' to watch scaling"
  echo "3. Run './scripts/devctl.sh stats' to see queue size"
  echo "------------------------------------------------"
}

# ---------------- AUTOSCALER ----------------

function autoscale_loop() {
  LAST_SCALE_TIME=0
  echo "Autoscaler loop started at $(date)"

  while true; do
    # Fetch queue length from API
    QUEUE=$(curl -s http://localhost:8080/stats | jq '.stream_length' 2>/dev/null || echo 0)
    # Count only running workers
    CURRENT=$(docker ps --filter "name=worker" --filter "status=running" --format "{{.Names}}" | wc -l)

    # Scaling Logic
    if [ "$QUEUE" -gt 1000 ]; then
      TARGET=20
    elif [ "$QUEUE" -gt 500 ]; then
      TARGET=10
    elif [ "$QUEUE" -gt 100 ]; then
      TARGET=5
    elif [ "$QUEUE" -gt 0 ]; then
      TARGET=2
    else
      TARGET=1
    fi

    NOW=$(date +%s)
    DIFF=$((NOW - LAST_SCALE_TIME))

    # Scale if target changed and cooldown passed
    if [ "$TARGET" -ne "$CURRENT" ] && [ "$DIFF" -gt "$COOLDOWN" ]; then
      echo "[$(date +%T)] Queue=$QUEUE | Scaling $CURRENT -> $TARGET"
      docker compose -f "$COMPOSE_FILE" up -d --scale worker=$TARGET --no-recreate
      LAST_SCALE_TIME=$NOW
    fi

    sleep 2
  done
}

function start_autoscaler() {
  if [ -f "$AUTOSCALER_PID_FILE" ]; then
    PID=$(cat "$AUTOSCALER_PID_FILE")
    if ps -p $PID > /dev/null; then
      echo "ℹAutoscaler already running (PID $PID)"
      return
    fi
  fi

  echo "Starting Autoscaler..."
  autoscale_loop > "$AUTOSCALER_LOG" 2>&1 &
  echo $! > "$AUTOSCALER_PID_FILE"
}

function stop_autoscaler() {
  if [ -f "$AUTOSCALER_PID_FILE" ]; then
    PID=$(cat "$AUTOSCALER_PID_FILE")
    echo "Stopping autoscaler (PID $PID)..."
    kill "$PID" || true
    rm -f "$AUTOSCALER_PID_FILE"
  fi
}

# ---------------- LOAD TEST (Scalable & Non-Blocking) ----------------

function test_jobs() {
  COUNT=${1:-100}
  echo "Sending $COUNT jobs in background..."

  # Run the loop in a subshell backgrounded
  (
    for i in $(seq 1 $COUNT); do
      curl -s -X POST http://localhost:8080/jobs >/dev/null &
      # Prevent "Too many open files" error on host
      if [ $((i % 50)) -eq 0 ]; then sleep 0.1; fi 
    done
    wait
    echo -e "\n Finished sending $COUNT jobs."
  ) & 
  
  echo "Job injection started. Use './scripts/devctl.sh stats' to monitor."
}

# ---------------- HELPERS ----------------

function status() {
  docker ps --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
}

function stats() {
  curl -s http://localhost:8080/stats | jq .
}

function logs() {
  docker compose -f "$COMPOSE_FILE" logs -f --tail=100
}

function down() {
  echo "Stopping everything..."
  stop_autoscaler
  docker compose -f "$COMPOSE_FILE" down
}

function reset() {
  echo "Wiping system and volumes..."
  stop_autoscaler
  docker compose -f "$COMPOSE_FILE" down -v
}

# ---------------- ROUTER ----------------

check_deps

case "$1" in
  dev)           dev ;;
  up)            up ;;
  down)          down ;;
  rebuild)       rebuild ;;
  scale)         scale $2 ;;
  autoscale)     start_autoscaler ;;
  autoscale-stop) stop_autoscaler ;;
  autoscale-log) tail -f "$AUTOSCALER_LOG" ;;
  logs)          logs ;;
  test)          test_jobs $2 ;;
  stats)         stats ;;
  status)        status ;;
  reset)         reset ;;
  *)             help ;;
esac
