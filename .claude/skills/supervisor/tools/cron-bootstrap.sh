#!/bin/bash
# Bootstrap wrapper for cron jobs that need the user's shell environment.
# macOS cron runs in a minimal env without login shell init, so tools like
# `claude` may fail (missing auth, PATH, etc).
#
# This script:
# 1. Sources the user's shell profile for PATH/env vars
# 2. Loads the Claude OAuth token for headless API access
#
# Usage in crontab:
#   * * * * * /path/to/cron-bootstrap.sh /usr/bin/python3 /path/to/script.py

# Source shell profile to pick up PATH, brew env, etc.
[ -f "$HOME/.zprofile" ] && source "$HOME/.zprofile"
[ -f "$HOME/.zshrc" ] && source "$HOME/.zshrc"

# Load Claude OAuth token for headless use (created via `claude setup-token`)
TOKEN_FILE="$HOME/.config/claude-code/oauth-token"
if [ -f "$TOKEN_FILE" ]; then
    export CLAUDE_CODE_OAUTH_TOKEN="$(cat "$TOKEN_FILE")"
fi

exec "$@"
