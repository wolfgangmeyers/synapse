#!/usr/bin/env bash
set -euo pipefail

# Usage: tools/run_webui.sh [Vite arguments]
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WEBUI_DIR="$ROOT_DIR/web/router-ui-react"

if ! command -v npm >/dev/null 2>&1; then
  printf '%s\n' "run_webui.sh: npm is required; install Node.js and npm first." >&2
  exit 1
fi

if [[ ! -f "$WEBUI_DIR/package.json" ]]; then
  printf '%s\n' "run_webui.sh: Web UI package not found at $WEBUI_DIR." >&2
  exit 1
fi

if [[ ! -x "$WEBUI_DIR/node_modules/.bin/vite" ]]; then
  printf '%s\n' "run_webui.sh: Web UI dependencies are missing; run 'npm install' in $WEBUI_DIR." >&2
  exit 1
fi

exec npm --prefix "$WEBUI_DIR" run dev -- "$@" --host 0.0.0.0 --port 8787
