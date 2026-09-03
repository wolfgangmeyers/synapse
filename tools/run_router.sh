#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DATABASE="${MECHA_ROUTER_DATABASE:-${HOME}/.mecha-developer/router.sqlite}"
LOG="${MECHA_ROUTER_LOG:-${HOME}/.mecha-developer/router.jsonl}"
HOST="${MECHA_ROUTER_HOST:-127.0.0.1}"
PORT="${MECHA_ROUTER_PORT:-8765}"
MAX_BATCH_CHARS="${MECHA_ROUTER_MAX_BATCH_CHARS:-4096}"
AUDIT_SUPERVISOR_TARGET="${MECHA_ROUTER_AUDIT_SUPERVISOR_TARGET:-supervisor}"
AUDIT_TMUX_SESSION="${MECHA_ROUTER_AUDIT_TMUX_SESSION:-mecha}"
POLICY_TIMEOUT="${MECHA_ROUTER_POLICY_TIMEOUT:-5s}"

usage() {
  printf '%s\n' "Usage: tools/run_router.sh" "" "Runs the Go Mailbox router through router-proxy/Makefile." ""
}

case "${1:-}" in
  --help|-h)
    usage
    ;;
  "")
    exec make -C "$ROOT_DIR/router-proxy" run-router \
      "ROUTER_DATABASE=$DATABASE" \
      "ROUTER_HOST=$HOST" \
      "ROUTER_PORT=$PORT" \
      "ROUTER_LOG=$LOG" \
      "ROUTER_MAX_BATCH_CHARS=$MAX_BATCH_CHARS" \
      "ROUTER_AUDIT_SUPERVISOR_TARGET=$AUDIT_SUPERVISOR_TARGET" \
      "ROUTER_AUDIT_TMUX_SESSION=$AUDIT_TMUX_SESSION" \
      "ROUTER_POLICY_TIMEOUT=$POLICY_TIMEOUT"
    ;;
  *)
    printf '%s\n' "run_router.sh: unknown argument: $1" >&2
    usage >&2
    exit 2
    ;;
esac
