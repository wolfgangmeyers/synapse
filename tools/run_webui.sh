#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEBUI_DIR="$ROOT_DIR/web/router-ui-react"

VITE_ROUTER_URL="${MECHA_ROUTER_URL:-http://127.0.0.1:8765}" \
  exec npm --prefix "$WEBUI_DIR" run dev -- --host "${MECHA_WEBUI_HOST:-0.0.0.0}" --port "${MECHA_WEBUI_PORT:-8787}" "$@"
