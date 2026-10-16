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
MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH="${MECHA_ROUTER_MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH:-false}"
# Router-managed Pi is on by default. Only the explicit string "false" opts
# out; reject other values so a typo cannot silently change the router mode.
case "${MECHA_ROUTER_MANAGED:-true}" in
  true)
    MANAGED=true
    ;;
  false)
    MANAGED=false
    ;;
  *)
    printf '%s\n' "run_router.sh: MECHA_ROUTER_MANAGED must be true or false" >&2
    exit 2
    ;;
esac
MANAGED_PI="${MECHA_ROUTER_MANAGED_PI:-$(command -v pi || true)}"
MANAGED_HOME="${MECHA_ROUTER_MANAGED_HOME:-${HOME}/.mecha-developer/managed-pi}"
MANAGED_PI_AGENT_DIR="${MECHA_ROUTER_MANAGED_PI_AGENT_DIR:-${HOME}/.pi/agent}"
MANAGED_SESSION_ROOT="${MECHA_ROUTER_MANAGED_SESSION_ROOT:-${HOME}/.mecha-developer/managed-pi/sessions}"
MANAGED_MANIFEST="${MECHA_ROUTER_MANAGED_MANIFEST:-$ROOT_DIR/router/contract/managed-tools-v1.json}"
MANAGED_MECHA_RPC="${MECHA_ROUTER_MANAGED_MECHA_RPC:-$ROOT_DIR/pi-extensions/mecha-rpc/index.ts}"
MANAGED_PI_SUBAGENTS="${MECHA_ROUTER_MANAGED_PI_SUBAGENTS:-${HOME}/code/pi-packages/packages/pi-subagents/src/index.ts}"
if [[ "${MECHA_ROUTER_MANAGED_CURSOR_EXTENSION+x}" == "x" ]]; then
  MANAGED_CURSOR_EXTENSION="$MECHA_ROUTER_MANAGED_CURSOR_EXTENSION"
elif [[ -f "$ROOT_DIR/auth-helper/node_modules/pi-cursor-sdk/dist/index.js" ]]; then
  MANAGED_CURSOR_EXTENSION="$ROOT_DIR/auth-helper/node_modules/pi-cursor-sdk/dist/index.js"
else
  MANAGED_CURSOR_EXTENSION=""
fi
if [[ "${MECHA_ROUTER_MANAGED_ANTIGRAVITY_EXTENSION+x}" == "x" ]]; then
  MANAGED_ANTIGRAVITY_EXTENSION="$MECHA_ROUTER_MANAGED_ANTIGRAVITY_EXTENSION"
elif [[ -f "${HOME}/.pi/agent/npm/node_modules/pi-antigravity/src/index.ts" ]]; then
  MANAGED_ANTIGRAVITY_EXTENSION="${HOME}/.pi/agent/npm/node_modules/pi-antigravity/src/index.ts"
else
  MANAGED_ANTIGRAVITY_EXTENSION=""
fi
MANAGED_AUTH_HELPER="${MECHA_ROUTER_MANAGED_AUTH_HELPER:-$ROOT_DIR/auth-helper/index.mjs}"
MANAGED_RUNTIME_ROOT="${MECHA_ROUTER_MANAGED_RUNTIME_ROOT:-$ROOT_DIR}"
CODEX_CREDENTIAL_PATH="${MECHA_ROUTER_CODEX_CREDENTIAL_PATH:-${HOME}/.mecha-developer/codex-credentials.json}"
# Managed agents normally work in Wolfgang's local projects, which live under
# the home directory. An explicit configuration remains the only way to widen
# or narrow this router's launch scope.
MANAGED_CWD_ROOTS="${MECHA_ROUTER_MANAGED_CWD_ROOTS:-$HOME}"
MANAGED_QUIET_RECIPIENT="${MECHA_ROUTER_MANAGED_QUIET_RECIPIENT:-supervisor}"
# Local Go caches can be left non-traversable by interrupted tool runs. Keep
# router recovery independent from that shared cache while honoring an explicit
# caller-provided GOCACHE.
if [[ "${GOCACHE+x}" != "x" ]]; then
  export GOCACHE="$HOME/.mecha-developer/go-build-cache"
fi
mkdir -p "$GOCACHE"
# Serve a built UI when this checkout has one. A clean source checkout leaves
# the UI path unset; callers may set the path explicitly.
if [[ "${MECHA_ROUTER_MANAGED_UI_DIST+x}" == "x" ]]; then
  MANAGED_UI_DIST="$MECHA_ROUTER_MANAGED_UI_DIST"
elif [[ -f "$ROOT_DIR/web/router-ui-react/dist/index.html" ]]; then
  MANAGED_UI_DIST="$ROOT_DIR/web/router-ui-react/dist"
else
  MANAGED_UI_DIST=""
fi

usage() {
  printf '%s\n' "Usage: tools/run_router.sh" "" "Runs the Go Mailbox router through router-proxy/Makefile." "Running tools/run_router.sh is mandatory for automatic restarts." "Router-managed Pi is enabled by default; set MECHA_ROUTER_MANAGED=false for legacy-only routing." ""
}

case "${1:-}" in
  --help|-h)
    usage
    ;;
  "")
    if [[ "$MANAGED" == "true" ]]; then
      mkdir -p "$MANAGED_HOME" "$MANAGED_SESSION_ROOT"
      chmod 700 "$MANAGED_HOME" "$MANAGED_SESSION_ROOT"
      if [[ ! -f "$ROOT_DIR/auth-helper/node_modules/@earendil-works/pi-coding-agent/package.json" ]]; then
        printf '%s\n' "run_router.sh: installing auth-helper dependencies..." >&2
        npm --prefix "$ROOT_DIR/auth-helper" ci
      fi
      if [[ -z "$MANAGED_CURSOR_EXTENSION" && -f "$ROOT_DIR/auth-helper/node_modules/pi-cursor-sdk/dist/index.js" ]]; then
        MANAGED_CURSOR_EXTENSION="$ROOT_DIR/auth-helper/node_modules/pi-cursor-sdk/dist/index.js"
      fi
    fi
    EXIT_FILE="$(mktemp -t router_exit.XXXXXX)"
    export ROUTER_EXIT_FILE="$EXIT_FILE"
    INTERRUPTED=0
    trap 'rm -f "$EXIT_FILE"' EXIT
    trap 'INTERRUPTED=1; exit 0' INT TERM
    while true; do
      set +e
      make -C "$ROOT_DIR/router-proxy" run-router \
        "ROUTER_DATABASE=$DATABASE" \
        "ROUTER_HOST=$HOST" \
        "ROUTER_PORT=$PORT" \
        "ROUTER_LOG=$LOG" \
        "ROUTER_MAX_BATCH_CHARS=$MAX_BATCH_CHARS" \
        "ROUTER_AUDIT_SUPERVISOR_TARGET=$AUDIT_SUPERVISOR_TARGET" \
        "ROUTER_AUDIT_TMUX_SESSION=$AUDIT_TMUX_SESSION" \
        "ROUTER_POLICY_TIMEOUT=$POLICY_TIMEOUT" \
        "ROUTER_MANAGED=$MANAGED" \
        "ROUTER_MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH=$MANAGED_ALLOW_LAUNCH_RECIPE_MISMATCH" \
        "ROUTER_MANAGED_PI=$MANAGED_PI" \
        "ROUTER_MANAGED_HOME=$MANAGED_HOME" \
        "ROUTER_MANAGED_PI_AGENT_DIR=$MANAGED_PI_AGENT_DIR" \
        "ROUTER_MANAGED_SESSION_ROOT=$MANAGED_SESSION_ROOT" \
        "ROUTER_MANAGED_MANIFEST=$MANAGED_MANIFEST" \
        "ROUTER_MANAGED_MECHA_RPC=$MANAGED_MECHA_RPC" \
        "ROUTER_MANAGED_PI_SUBAGENTS=$MANAGED_PI_SUBAGENTS" \
        "ROUTER_MANAGED_CURSOR_EXTENSION=$MANAGED_CURSOR_EXTENSION" \
        "ROUTER_MANAGED_ANTIGRAVITY_EXTENSION=$MANAGED_ANTIGRAVITY_EXTENSION" \
        "ROUTER_MANAGED_AUTH_HELPER=$MANAGED_AUTH_HELPER" \
        "ROUTER_MANAGED_RUNTIME_ROOT=$MANAGED_RUNTIME_ROOT" \
        "ROUTER_MANAGED_CWD_ROOTS=$MANAGED_CWD_ROOTS" \
        "ROUTER_MANAGED_QUIET_RECIPIENT=$MANAGED_QUIET_RECIPIENT" \
        "ROUTER_MANAGED_UI_DIST=$MANAGED_UI_DIST" \
        "ROUTER_CODEX_CREDENTIAL_PATH=$CODEX_CREDENTIAL_PATH"
      exit_code=$?
      if [[ -s "$EXIT_FILE" ]]; then
        exit_code="$(<"$EXIT_FILE")"
        rm -f "$EXIT_FILE"
      fi
      set -e
      if [[ "$INTERRUPTED" -eq 1 || "$exit_code" -eq 0 || "$exit_code" -eq 130 || "$exit_code" -eq 143 ]]; then
        exit 0
      fi
      printf '%s\n' "run_router.sh: router exited with code $exit_code; restarting in 1s..." >&2
      sleep 1
      continue
    done
    ;;
  *)
    printf '%s\n' "run_router.sh: unknown argument: $1" >&2
    usage >&2
    exit 2
    ;;
esac
