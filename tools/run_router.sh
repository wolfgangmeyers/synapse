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
MANAGED="${MECHA_ROUTER_MANAGED:-false}"
MANAGED_PI="${MECHA_ROUTER_MANAGED_PI:-$(command -v pi || true)}"
MANAGED_HOME="${MECHA_ROUTER_MANAGED_HOME:-${HOME}/.mecha-developer/managed-pi}"
MANAGED_SESSION_ROOT="${MECHA_ROUTER_MANAGED_SESSION_ROOT:-${HOME}/.mecha-developer/managed-pi/sessions}"
MANAGED_MANIFEST="${MECHA_ROUTER_MANAGED_MANIFEST:-$ROOT_DIR/router/contract/managed-tools-v1.json}"
MANAGED_MECHA_RPC="${MECHA_ROUTER_MANAGED_MECHA_RPC:-$ROOT_DIR/pi-extensions/mecha-rpc/index.ts}"
MANAGED_PI_SUBAGENTS="${MECHA_ROUTER_MANAGED_PI_SUBAGENTS:-${HOME}/code/pi-packages/packages/pi-subagents/src/index.ts}"
MANAGED_CURSOR_EXTENSION="${MECHA_ROUTER_MANAGED_CURSOR_EXTENSION:-$ROOT_DIR/auth-helper/node_modules/pi-cursor-sdk/dist/index.js}"
MANAGED_AUTH_HELPER="${MECHA_ROUTER_MANAGED_AUTH_HELPER:-$ROOT_DIR/auth-helper/index.mjs}"
MANAGED_RUNTIME_ROOT="${MECHA_ROUTER_MANAGED_RUNTIME_ROOT:-$ROOT_DIR}"
MANAGED_CWD_ROOTS="${MECHA_ROUTER_MANAGED_CWD_ROOTS:-$ROOT_DIR}"
MANAGED_QUIET_RECIPIENT="${MECHA_ROUTER_MANAGED_QUIET_RECIPIENT:-supervisor}"
# Serve a built UI when this checkout has one. A clean source checkout still
# starts the legacy router normally; callers may set the path explicitly.
if [[ "${MECHA_ROUTER_MANAGED_UI_DIST+x}" == "x" ]]; then
  MANAGED_UI_DIST="$MECHA_ROUTER_MANAGED_UI_DIST"
elif [[ -f "$ROOT_DIR/web/router-ui-react/dist/index.html" ]]; then
  MANAGED_UI_DIST="$ROOT_DIR/web/router-ui-react/dist"
else
  MANAGED_UI_DIST=""
fi

usage() {
  printf '%s\n' "Usage: tools/run_router.sh" "" "Runs the Go Mailbox router through router-proxy/Makefile." ""
}

case "${1:-}" in
  --help|-h)
    usage
    ;;
  "")
    if [[ "$MANAGED" == "true" ]]; then
      mkdir -p "$MANAGED_HOME" "$MANAGED_SESSION_ROOT"
      chmod 700 "$MANAGED_HOME" "$MANAGED_SESSION_ROOT"
    fi
    exec make -C "$ROOT_DIR/router-proxy" run-router \
      "ROUTER_DATABASE=$DATABASE" \
      "ROUTER_HOST=$HOST" \
      "ROUTER_PORT=$PORT" \
      "ROUTER_LOG=$LOG" \
      "ROUTER_MAX_BATCH_CHARS=$MAX_BATCH_CHARS" \
      "ROUTER_AUDIT_SUPERVISOR_TARGET=$AUDIT_SUPERVISOR_TARGET" \
      "ROUTER_AUDIT_TMUX_SESSION=$AUDIT_TMUX_SESSION" \
      "ROUTER_POLICY_TIMEOUT=$POLICY_TIMEOUT" \
      "ROUTER_MANAGED=$MANAGED" \
      "ROUTER_MANAGED_PI=$MANAGED_PI" \
      "ROUTER_MANAGED_HOME=$MANAGED_HOME" \
      "ROUTER_MANAGED_SESSION_ROOT=$MANAGED_SESSION_ROOT" \
      "ROUTER_MANAGED_MANIFEST=$MANAGED_MANIFEST" \
      "ROUTER_MANAGED_MECHA_RPC=$MANAGED_MECHA_RPC" \
      "ROUTER_MANAGED_PI_SUBAGENTS=$MANAGED_PI_SUBAGENTS" \
      "ROUTER_MANAGED_CURSOR_EXTENSION=$MANAGED_CURSOR_EXTENSION" \
      "ROUTER_MANAGED_AUTH_HELPER=$MANAGED_AUTH_HELPER" \
      "ROUTER_MANAGED_RUNTIME_ROOT=$MANAGED_RUNTIME_ROOT" \
      "ROUTER_MANAGED_CWD_ROOTS=$MANAGED_CWD_ROOTS" \
      "ROUTER_MANAGED_QUIET_RECIPIENT=$MANAGED_QUIET_RECIPIENT" \
      "ROUTER_MANAGED_UI_DIST=$MANAGED_UI_DIST"
    ;;
  *)
    printf '%s\n' "run_router.sh: unknown argument: $1" >&2
    usage >&2
    exit 2
    ;;
esac
