#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATABASE="${MECHA_ROUTER_DATABASE:-${HOME}/.mecha-developer/router.sqlite}"
LOG="${MECHA_ROUTER_LOG:-${HOME}/.mecha-developer/router.jsonl}"
HOST="${MECHA_ROUTER_HOST:-127.0.0.1}"
PORT="${MECHA_ROUTER_PORT:-8765}"
MAX_BATCH_CHARS="${MECHA_ROUTER_MAX_BATCH_CHARS:-4096}"

usage() {
  printf '%s\n' "Usage: tools/run_router.sh [--node]" "" "Runs the Go Mailbox router through router-proxy/Makefile." "Use --node to run the Node router as an explicit rollback path."
}

case "${1:-}" in
  --help|-h)
    usage
    ;;
  --node)
    if (($# != 1)); then
      printf '%s\n' "run_router.sh: --node does not accept additional arguments" >&2
      usage >&2
      exit 2
    fi
    exec node "$ROOT_DIR/tools/run_router.mjs" \
      --database "$DATABASE" \
      --host "$HOST" \
      --port "$PORT" \
      --log "$LOG" \
      --max-batch-chars "$MAX_BATCH_CHARS"
    ;;
  "")
    exec make -C "$ROOT_DIR/router-proxy" run-router \
      "ROUTER_DATABASE=$DATABASE" \
      "ROUTER_HOST=$HOST" \
      "ROUTER_PORT=$PORT" \
      "ROUTER_LOG=$LOG" \
      "ROUTER_MAX_BATCH_CHARS=$MAX_BATCH_CHARS"
    ;;
  *)
    printf '%s\n' "run_router.sh: unknown argument: $1" >&2
    usage >&2
    exit 2
    ;;
esac
