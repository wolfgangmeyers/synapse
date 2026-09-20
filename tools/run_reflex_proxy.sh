#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROXY_BINARY="${MECHA_REFLEX_PROXY_BINARY:-$ROOT_DIR/router-proxy/router-proxy}"

usage() {
  printf '%s\n' \
    'Usage: tools/run_reflex_proxy.sh <target-ip> [upstream-port] [listen-port]' \
    '' \
    'Runs the existing router proxy for a Reflex router target.'
}

case "$#" in
  1|2|3)
    ;;
  0)
    printf '%s\n' 'run_reflex_proxy.sh: target IP is required' >&2
    usage >&2
    exit 2
    ;;
  *)
    printf '%s\n' 'run_reflex_proxy.sh: too many arguments' >&2
    usage >&2
    exit 2
    ;;
esac

TARGET_IP="$1"
UPSTREAM_PORT="${2:-8765}"
LISTEN_PORT="${3:-19876}"

if [[ ! -x "$PROXY_BINARY" ]]; then
  printf 'run_reflex_proxy.sh: proxy binary is not executable: %s\n' "$PROXY_BINARY" >&2
  exit 1
fi

exec "$PROXY_BINARY" \
  --upstream-host "$TARGET_IP" \
  --upstream-port "$UPSTREAM_PORT" \
  --listen-port "$LISTEN_PORT"
