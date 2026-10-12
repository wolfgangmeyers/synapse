#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEBUI_DIR="$ROOT_DIR/web/router-ui-react"
WEBUI_HOST="${MECHA_WEBUI_HOST:-0.0.0.0}"
WEBUI_PORT="${MECHA_WEBUI_PORT:-8787}"
ROUTER_URL="${MECHA_ROUTER_URL:-http://127.0.0.1:8765}"

VITE_ROUTER_URL="$ROUTER_URL" \
  exec npm --prefix "$WEBUI_DIR" run dev -- --host "$WEBUI_HOST" --port "$WEBUI_PORT" "$@"
