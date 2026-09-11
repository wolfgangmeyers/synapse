#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEBUI_DIR="$ROOT_DIR/web/router-ui-react"
WEBUI_HOST="${MECHA_WEBUI_HOST:-0.0.0.0}"
WEBUI_PORT="${MECHA_WEBUI_PORT:-8787}"
# This URL is consumed in the browser. The Vite proxy uses MECHA_ROUTER_URL
# server-side, so remote browsers stay on the Web UI origin for every /v1 call.
WEBUI_ORIGIN="${MECHA_WEBUI_ROUTER_URL:-http://127.0.0.1:${WEBUI_PORT}}"

VITE_ROUTER_URL="$WEBUI_ORIGIN" \
  exec npm --prefix "$WEBUI_DIR" run dev -- --host "$WEBUI_HOST" --port "$WEBUI_PORT" "$@"
