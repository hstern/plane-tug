#!/usr/bin/env bash
# Orchestrator for the plane-tug e2e compose stack.
#
# Usage:
#   bash test/e2e-docker/run.sh up      # bring up + seed; emit JSON
#   bash test/e2e-docker/run.sh seed    # re-run the seed (idempotent)
#   bash test/e2e-docker/run.sh down    # tear down
#
# `up` blocks until plane-api is accepting requests, then runs the
# Django ORM seed. The last stdout line is a JSON blob with everything
# the Go e2e test needs:
#   {"workspace_slug":"…","project_id":"…","api_token":"…", …}
# Earlier lines are progress diagnostics.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="${SCRIPT_DIR}/compose.yaml"
SEED_SCRIPT="${SCRIPT_DIR}/seed.py"
SEED_INFO_FILE="${SCRIPT_DIR}/.seed-info.json"

PLANE_HOST_PORT="${PLANE_TUG_PLANE_HOST_PORT:-8765}"
PLANE_HEALTH_URL="http://localhost:${PLANE_HOST_PORT}/api/instances/"

compose() {
  if docker compose version >/dev/null 2>&1; then
    docker compose -f "$COMPOSE_FILE" "$@"
  else
    docker-compose -f "$COMPOSE_FILE" "$@"
  fi
}

log() { echo "plane-tug-e2e:" "$@" >&2; }

wait_for_api() {
  local i max=60
  for ((i = 1; i <= max; i++)); do
    if curl -fsS "$PLANE_HEALTH_URL" >/dev/null 2>&1; then
      log "plane-api healthy after ${i}s"
      return 0
    fi
    sleep 5
  done
  log "ERROR: plane-api never became healthy"
  compose logs --tail=80 plane-api >&2
  return 1
}

wait_for_bridge() {
  local i max=20
  for ((i = 1; i <= max; i++)); do
    if curl -fsS "http://localhost:8081/healthz" >/dev/null 2>&1; then
      log "plane-tug healthy after ${i}s"
      return 0
    fi
    sleep 1
  done
  log "ERROR: plane-tug never became healthy"
  compose logs --tail=80 plane-tug >&2
  return 1
}

cmd_up() {
  log "bringing up Plane CE + plane-tug (cold cache: ~90s)"
  compose up -d --build
  wait_for_api
  wait_for_bridge
  cmd_seed
}

cmd_seed() {
  log "seeding admin user + workspace + project + webhook"
  docker cp "$SEED_SCRIPT" "$(compose ps -q plane-api)":/tmp/seed.py
  local out
  out="$(compose exec -T -e PYTHONPATH=/code plane-api python /tmp/seed.py)"
  # Last line of seed.py stdout is the JSON blob; everything else is
  # diagnostic on stderr.
  local json
  json="$(echo "$out" | tail -n 1)"
  echo "$json" > "$SEED_INFO_FILE"
  echo "$json"
}

cmd_down() {
  log "tearing down Plane CE stack"
  compose down -v --remove-orphans
  rm -f "$SEED_INFO_FILE"
}

case "${1:-up}" in
up) cmd_up ;;
seed) cmd_seed ;;
down) cmd_down ;;
*)
  echo "usage: $0 {up|seed|down}" >&2
  exit 64
  ;;
esac
