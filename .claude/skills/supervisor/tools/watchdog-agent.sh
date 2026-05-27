#!/usr/bin/env bash
# Cron-compatible wrapper for running the watchdog as a standard Codex agent.
#
# Example crontab:
#   */30 * * * * /Users/wolfgang.meyers/code/mecha-developer/.claude/skills/supervisor/tools/watchdog-agent.sh
#
# The legacy watchdog.py path is intentionally left available as a fallback.

set -u

DEFAULT_TIMEOUT_SECONDS=600
DEFAULT_FAILURE_ALERT_DEBOUNCE_SECONDS=21600
DEFAULT_LOCK_METADATA_GRACE_SECONDS=30
DEFAULT_RECLAIM_STALE_SECONDS=300
DEFAULT_MAX_LOG_BYTES=1048576

umask 077

if [ -z "${HOME:-}" ]; then
    HOME="$(cd ~ && pwd -P)"
fi
export HOME

export SHELL="${SHELL:-/bin/bash}"
export LANG="${LANG:-en_US.UTF-8}"
export CODEX_HOME="${CODEX_HOME:-$HOME/.codex}"

PATH_PREFIX="$HOME/.local/bin:$HOME/bin:/opt/homebrew/bin:/usr/local/bin"
PATH_SUFFIX="/usr/bin:/bin:/usr/sbin:/sbin"
if [ -n "${PATH:-}" ]; then
    export PATH="$PATH_PREFIX:$PATH:$PATH_SUFFIX"
else
    export PATH="$PATH_PREFIX:$PATH_SUFFIX"
fi

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/../../../.." && pwd -P)"
PROMPT_FILE="$REPO_ROOT/.claude/skills/watchdog/AGENT_PROMPT.md"
STATE_DIR="${MECHA_STATE_DIR:-$HOME/.mecha-developer}"
LOG_FILE="$STATE_DIR/watchdog-agent.log"
LOCK_DIR="$STATE_DIR/watchdog-agent.lock"
RECLAIM_LOCK_DIR="$LOCK_DIR.reclaim"
AGENT_WORKDIR="$STATE_DIR/watchdog-agent-work"
MAILBOX_DIR="$STATE_DIR/mailboxes"
FAILURE_ALERT_SENTINEL="$STATE_DIR/watchdog-agent-failure-alerted"
TIMEOUT_SECONDS="${MECHA_WATCHDOG_TIMEOUT_SECONDS:-$DEFAULT_TIMEOUT_SECONDS}"
FAILURE_ALERT_DEBOUNCE_SECONDS="${MECHA_WATCHDOG_FAILURE_ALERT_DEBOUNCE_SECONDS:-$DEFAULT_FAILURE_ALERT_DEBOUNCE_SECONDS}"
LOCK_METADATA_GRACE_SECONDS="${MECHA_WATCHDOG_LOCK_METADATA_GRACE_SECONDS:-$DEFAULT_LOCK_METADATA_GRACE_SECONDS}"
RECLAIM_STALE_SECONDS="${MECHA_WATCHDOG_RECLAIM_STALE_SECONDS:-$DEFAULT_RECLAIM_STALE_SECONDS}"
MAX_LOG_BYTES="${MECHA_WATCHDOG_MAX_LOG_BYTES:-$DEFAULT_MAX_LOG_BYTES}"

case "$TIMEOUT_SECONDS" in
    ''|*[!0-9]*)
        TIMEOUT_SECONDS="$DEFAULT_TIMEOUT_SECONDS"
        ;;
esac
case "$FAILURE_ALERT_DEBOUNCE_SECONDS" in
    ''|*[!0-9]*)
        FAILURE_ALERT_DEBOUNCE_SECONDS="$DEFAULT_FAILURE_ALERT_DEBOUNCE_SECONDS"
        ;;
esac
case "$LOCK_METADATA_GRACE_SECONDS" in
    ''|*[!0-9]*)
        LOCK_METADATA_GRACE_SECONDS="$DEFAULT_LOCK_METADATA_GRACE_SECONDS"
        ;;
esac
case "$RECLAIM_STALE_SECONDS" in
    ''|*[!0-9]*)
        RECLAIM_STALE_SECONDS="$DEFAULT_RECLAIM_STALE_SECONDS"
        ;;
esac
case "$MAX_LOG_BYTES" in
    ''|*[!0-9]*)
        MAX_LOG_BYTES="$DEFAULT_MAX_LOG_BYTES"
        ;;
esac
LOCK_STALE_SECONDS="${MECHA_WATCHDOG_LOCK_STALE_SECONDS:-$((TIMEOUT_SECONDS + 300))}"
case "$LOCK_STALE_SECONDS" in
    ''|*[!0-9]*)
        LOCK_STALE_SECONDS="$((TIMEOUT_SECONDS + 300))"
        ;;
esac

log() {
    printf '%s watchdog-agent: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"
}

resolve_command() {
    command_name="$1"

    if [ "${command_name#*/}" != "$command_name" ]; then
        if [ -x "$command_name" ]; then
            printf '%s\n' "$command_name"
            return 0
        fi
        return 1
    fi

    resolved="$(command -v "$command_name" 2>/dev/null || true)"
    if [ -n "$resolved" ]; then
        printf '%s\n' "$resolved"
        return 0
    fi

    for candidate_shell in "${SHELL:-}" /bin/zsh /bin/bash; do
        if [ -z "$candidate_shell" ] || [ ! -x "$candidate_shell" ]; then
            continue
        fi
        resolved="$(
            CODEX_LOOKUP_NAME="$command_name" "$candidate_shell" -lc \
                'command -v "$CODEX_LOOKUP_NAME"' 2>/dev/null | sed -n '1p'
        )"
        if [ -n "$resolved" ] && [ -x "$resolved" ]; then
            printf '%s\n' "$resolved"
            return 0
        fi
    done

    for resolved in "$HOME"/.nvm/versions/node/*/bin/"$command_name"; do
        if [ -x "$resolved" ]; then
            printf '%s\n' "$resolved"
            return 0
        fi
    done

    return 1
}

PYTHON_BIN="${PYTHON_BIN:-python3}"
PYTHON_PATH="$(resolve_command "$PYTHON_BIN" || true)"
if [ -z "$PYTHON_PATH" ]; then
    printf 'watchdog-agent: python3 not found in PATH or login shell environment\n' >&2
    exit 0
fi
export PYTHON_PATH
PYTHON_DIR="$(CDPATH= cd -- "$(dirname -- "$PYTHON_PATH")" && pwd -P)"
export PATH="$PYTHON_DIR:$PATH"

validate_managed_paths() {
    "$PYTHON_PATH" - "$REPO_ROOT" "$STATE_DIR" "$AGENT_WORKDIR" "$LOG_FILE" \
        "$LOCK_DIR" "$RECLAIM_LOCK_DIR" "$MAILBOX_DIR" "$FAILURE_ALERT_SENTINEL" <<'PY'
from pathlib import Path
import sys


def is_relative_to(path, base):
    try:
        path.relative_to(base)
        return True
    except ValueError:
        return False


repo = Path(sys.argv[1]).resolve(strict=True)
raw_paths = [Path(p).expanduser() for p in sys.argv[2:]]
state = raw_paths[0].resolve(strict=False)
managed_paths = [p.resolve(strict=False) for p in raw_paths]

if state == repo or is_relative_to(state, repo):
    print(f"state directory must not be inside the project checkout: {state}", file=sys.stderr)
    sys.exit(1)

for raw_path, path in zip(raw_paths, managed_paths):
    if path == repo or is_relative_to(path, repo):
        print(f"managed watchdog path must not be inside the project checkout: {path}", file=sys.stderr)
        sys.exit(1)
    if raw_path.exists() and raw_path.is_symlink():
        print(f"managed watchdog path must not be a symlink: {raw_path}", file=sys.stderr)
        sys.exit(1)
    if path != state and not is_relative_to(path, state):
        print(f"managed watchdog path must stay inside the state directory: {path}", file=sys.stderr)
        sys.exit(1)
PY
}

if ! path_error="$(validate_managed_paths 2>&1)"; then
    printf 'watchdog-agent: unsafe path configuration: %s\n' "$path_error" >&2
    exit 0
fi

mkdir -p "$STATE_DIR" "$AGENT_WORKDIR" "$MAILBOX_DIR"
chmod 700 "$STATE_DIR" "$AGENT_WORKDIR" "$MAILBOX_DIR" 2>/dev/null || true
export MECHA_MAILBOX_DIR_OVERRIDE="$MAILBOX_DIR"
if ! touch "$LOG_FILE" 2>/dev/null; then
    printf 'watchdog-agent: cannot write log file: %s\n' "$LOG_FILE" >&2
    exit 0
fi

cap_log_file() {
    if [ "$MAX_LOG_BYTES" -le 0 ]; then
        return 0
    fi
    log_size="$(wc -c < "$LOG_FILE" 2>/dev/null | tr -d '[:space:]' || true)"
    case "$log_size" in
        ''|*[!0-9]*)
            log_size=0
            ;;
    esac
    if [ "$log_size" -gt "$MAX_LOG_BYTES" ]; then
        keep_bytes=$((MAX_LOG_BYTES / 2))
        if [ "$keep_bytes" -lt 1 ]; then
            keep_bytes="$MAX_LOG_BYTES"
        fi
        tmp_log="$LOG_FILE.tmp.$$"
        if ! tail -c "$keep_bytes" "$LOG_FILE" > "$tmp_log" 2>/dev/null ||
            ! mv "$tmp_log" "$LOG_FILE" 2>/dev/null; then
            rm -f "$tmp_log"
        fi
    fi
}

cap_log_file

exec >> "$LOG_FILE" 2>&1

file_age_seconds() {
    path="$1"
    "$PYTHON_PATH" - "$path" <<'PY'
import os
import sys
import time

path = sys.argv[1]
try:
    print(max(0, int(time.time() - os.path.getmtime(path))))
except OSError:
    sys.exit(1)
PY
}

send_failure_alert() {
    reason="$1"
    detail="$2"

    if [ -e "$FAILURE_ALERT_SENTINEL" ]; then
        alert_age="$(file_age_seconds "$FAILURE_ALERT_SENTINEL" || printf '%s\n' 0)"
        if [ "$alert_age" -lt "$FAILURE_ALERT_DEBOUNCE_SECONDS" ]; then
            log "failure alert suppressed by debounce: $reason"
            return 0
        fi
    fi

    mailbox_send="$REPO_ROOT/.claude/skills/supervisor/tools/mailbox_send.py"
    if [ ! -r "$mailbox_send" ]; then
        log "cannot send failure alert; mailbox_send.py not readable: $mailbox_send"
        return 0
    fi

    body="[WATCHDOG] Watchdog agent failure:

Reason: $reason
Detail: $detail

The cron-compatible Codex watchdog did not complete normally. Check $LOG_FILE on the supervisor host."

    if "$PYTHON_PATH" "$mailbox_send" \
        --to supervisor \
        --from watchdog \
        --subject "Watchdog agent failure" \
        --body "$body"; then
        log "sent failure alert: $reason"
        : > "$FAILURE_ALERT_SENTINEL"
    else
        log "failed to send failure alert: $reason"
    fi
}

clear_failure_alert() {
    rm -f "$FAILURE_ALERT_SENTINEL"
}

alert_body_preview() {
    path="$1"
    "$PYTHON_PATH" - "$path" <<'PY'
import sys

path = sys.argv[1]
try:
    with open(path, "r", encoding="utf-8", errors="replace") as f:
        data = f.read(4096)
except OSError as exc:
    print(f"could not read alert body: {exc}")
    raise SystemExit(0)

if len(data) == 4096:
    data += "\n[truncated]"
print(data)
PY
}

read_lock_pid() {
    if [ -r "$LOCK_DIR/pid" ]; then
        sed -n '1p' "$LOCK_DIR/pid" 2>/dev/null || true
    fi
}

lock_pid_is_live() {
    pid="$1"
    case "$pid" in
        ''|*[!0-9]*)
            return 1
            ;;
    esac
    kill -0 "$pid" 2>/dev/null
}

lock_pid_matches_owner() {
    pid="$1"
    if ! lock_pid_is_live "$pid"; then
        return 1
    fi

    command_line="$(ps -p "$pid" -o command= 2>/dev/null | sed -n '1p' || true)"
    case "$command_line" in
        *watchdog-agent.sh*)
            return 0
            ;;
    esac
    return 1
}

LOCK_TOKEN="pid=$$ started=$(date -u '+%Y-%m-%dT%H:%M:%SZ') random=${RANDOM:-0}-${RANDOM:-0}"

write_lock_metadata() {
    printf '%s\n' "$$" > "$LOCK_DIR/pid"
    printf '%s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" > "$LOCK_DIR/started_at"
    printf '%s\n' "$LOCK_TOKEN" > "$LOCK_DIR/owner"
    hostname > "$LOCK_DIR/host" 2>/dev/null || true
}

acquire_lock() {
    if mkdir "$LOCK_DIR" 2>/dev/null; then
        write_lock_metadata
        return 0
    fi

    old_pid="$(read_lock_pid)"
    lock_age="$(file_age_seconds "$LOCK_DIR" || printf '%s\n' 0)"
    if ! lock_pid_is_live "$old_pid" && [ "$lock_age" -lt "$LOCK_METADATA_GRACE_SECONDS" ]; then
        log "watchdog lock metadata is incomplete and only ${lock_age}s old; exiting"
        return 1
    fi

    if lock_pid_is_live "$old_pid"; then
        if lock_pid_matches_owner "$old_pid"; then
            if [ "$lock_age" -ge "$LOCK_STALE_SECONDS" ]; then
                log "watchdog lock held by live pid $old_pid for ${lock_age}s; not starting an overlapping run"
                send_failure_alert \
                    "watchdog lock held past stale threshold" \
                    "pid=$old_pid age_seconds=$lock_age stale_threshold_seconds=$LOCK_STALE_SECONDS"
                return 1
            fi
            log "another watchdog run is active with pid $old_pid; exiting"
            return 1
        fi
        if [ "$lock_age" -lt "$LOCK_METADATA_GRACE_SECONDS" ]; then
            log "watchdog lock pid $old_pid is live but not a watchdog process and only ${lock_age}s old; exiting"
            return 1
        fi
        log "watchdog lock pid $old_pid is live but not a watchdog process; reclaiming stale lock"
    fi

    if ! mkdir "$RECLAIM_LOCK_DIR" 2>/dev/null; then
        reclaim_age="$(file_age_seconds "$RECLAIM_LOCK_DIR" || printf '%s\n' 0)"
        if [ "$reclaim_age" -ge "$RECLAIM_STALE_SECONDS" ]; then
            log "removing stale watchdog reclaim lock age=${reclaim_age}s"
            rm -rf "$RECLAIM_LOCK_DIR"
            if ! mkdir "$RECLAIM_LOCK_DIR" 2>/dev/null; then
                log "could not acquire watchdog reclaim lock after stale cleanup; exiting"
                send_failure_alert \
                    "watchdog reclaim lock could not be acquired" \
                    "age_seconds=$reclaim_age reclaim_stale_seconds=$RECLAIM_STALE_SECONDS"
                return 1
            fi
        else
            log "another process is reclaiming the stale watchdog lock; exiting"
            return 1
        fi
    fi
    printf '%s\n' "$$" > "$RECLAIM_LOCK_DIR/pid" 2>/dev/null || true

    old_pid="$(read_lock_pid)"
    lock_age="$(file_age_seconds "$LOCK_DIR" || printf '%s\n' 0)"
    if ! lock_pid_is_live "$old_pid" && [ "$lock_age" -lt "$LOCK_METADATA_GRACE_SECONDS" ]; then
        rm -rf "$RECLAIM_LOCK_DIR"
        log "watchdog lock metadata became incomplete and only ${lock_age}s old during reclaim; exiting"
        return 1
    fi
    if lock_pid_is_live "$old_pid" && lock_pid_matches_owner "$old_pid"; then
        rm -rf "$RECLAIM_LOCK_DIR"
        if [ "$lock_age" -ge "$LOCK_STALE_SECONDS" ]; then
            log "watchdog lock held by live pid $old_pid for ${lock_age}s during reclaim; exiting"
            send_failure_alert \
                "watchdog lock held past stale threshold" \
                "pid=$old_pid age_seconds=$lock_age stale_threshold_seconds=$LOCK_STALE_SECONDS"
        else
            log "stale lock became active during reclaim; exiting"
        fi
        return 1
    fi

    log "removing stale watchdog lock age=${lock_age}s pid=${old_pid:-unknown}"
    rm -rf "$LOCK_DIR"
    if mkdir "$LOCK_DIR" 2>/dev/null; then
        write_lock_metadata
        rm -rf "$RECLAIM_LOCK_DIR"
        return 0
    fi

    rm -rf "$RECLAIM_LOCK_DIR"
    log "could not acquire watchdog lock; exiting"
    return 1
}

release_lock() {
    if [ -r "$LOCK_DIR/owner" ] && [ "$(sed -n '1p' "$LOCK_DIR/owner" 2>/dev/null)" = "$LOCK_TOKEN" ]; then
        rm -rf "$LOCK_DIR"
    else
        log "not removing watchdog lock because owner changed"
    fi
}

if ! acquire_lock; then
    exit 0
fi
trap release_lock EXIT INT TERM HUP

CODEX_BIN="${CODEX_BIN:-codex}"
if ! CODEX_PATH="$(resolve_command "$CODEX_BIN")"; then
    log "codex not found in PATH or login shell environment; exiting"
    send_failure_alert \
        "codex executable not found" \
        "CODEX_BIN=$CODEX_BIN PATH=$PATH"
    exit 0
fi
CODEX_DIR="$(CDPATH= cd -- "$(dirname -- "$CODEX_PATH")" && pwd -P)"
export PATH="$CODEX_DIR:$PATH"

if [ ! -r "$PROMPT_FILE" ]; then
    log "prompt file not readable: $PROMPT_FILE"
    send_failure_alert \
        "watchdog prompt file not readable" \
        "$PROMPT_FILE"
    exit 0
fi
ALERT_HELPER="$REPO_ROOT/.claude/skills/supervisor/tools/watchdog_mailbox_alert.py"
if [ ! -r "$ALERT_HELPER" ]; then
    log "alert helper not readable: $ALERT_HELPER"
    send_failure_alert \
        "watchdog alert helper not readable" \
        "$ALERT_HELPER"
    exit 0
fi

TMUX_BIN="${TMUX_BIN:-tmux}"
if ! TMUX_PATH="$(resolve_command "$TMUX_BIN")"; then
    log "tmux not found in PATH or login shell environment; exiting"
    send_failure_alert \
        "tmux executable not found" \
        "TMUX_BIN=$TMUX_BIN PATH=$PATH"
    exit 0
fi
TMUX_DIR="$(CDPATH= cd -- "$(dirname -- "$TMUX_PATH")" && pwd -P)"
export PATH="$TMUX_DIR:$PATH"

if [ ! -d "$REPO_ROOT/.git" ]; then
    log "repo root does not look like a git checkout: $REPO_ROOT"
    send_failure_alert \
        "repo root does not look like a git checkout" \
        "$REPO_ROOT"
    exit 0
fi

if ! cd "$REPO_ROOT"; then
    log "could not cd to repo root: $REPO_ROOT"
    exit 0
fi

export MECHA_DEVELOPER_ROOT="$REPO_ROOT"
export MECHA_STATE_DIR="$STATE_DIR"
export MECHA_WATCHDOG_WORKDIR="$AGENT_WORKDIR"
export TMUX_PATH

if ! PROMPT_INPUT="$(mktemp "$AGENT_WORKDIR/prompt.XXXXXX")"; then
    log "could not create prompt file in watchdog workspace: $AGENT_WORKDIR"
    send_failure_alert \
        "watchdog prompt temp file could not be created" \
        "$AGENT_WORKDIR"
    exit 0
fi
if ! ALERT_BODY_FILE="$(mktemp "$AGENT_WORKDIR/alert-body.XXXXXX")"; then
    rm -f "$PROMPT_INPUT"
    log "could not create alert body file in watchdog workspace: $AGENT_WORKDIR"
    send_failure_alert \
        "watchdog alert temp file could not be created" \
        "$AGENT_WORKDIR"
    exit 0
fi
chmod 600 "$PROMPT_INPUT" "$ALERT_BODY_FILE" 2>/dev/null || true
export MECHA_WATCHDOG_ALERT_BODY_FILE="$ALERT_BODY_FILE"
export MECHA_WATCHDOG_ALERT_HELPER="$ALERT_HELPER"
cleanup_prompt() {
    rm -f "$PROMPT_INPUT" "$ALERT_BODY_FILE"
}
trap 'cleanup_prompt; release_lock' EXIT INT TERM HUP

{
    printf 'Runtime context from watchdog-agent.sh:\n\n'
    printf -- '- Repository root: `%s`\n' "$REPO_ROOT"
    printf -- '- State directory: `%s`\n' "$STATE_DIR"
    printf -- '- Writable watchdog workspace: `%s`\n' "$AGENT_WORKDIR"
    printf -- '- Writable mailbox directory for alert delivery: `%s`\n' "$MAILBOX_DIR"
    printf -- '- The wrapper process changed to the repository root before launching Codex. Codex shell commands run from the watchdog workspace so the sandbox can deny project writes; use `MECHA_DEVELOPER_ROOT` for read-only repository paths.\n'
    printf -- '- Tmux path: `%s`\n' "$TMUX_PATH"
    printf -- '- Alert body file: `%s`\n' "$ALERT_BODY_FILE"
    printf -- '- Alert helper: `%s`\n' "$ALERT_HELPER"
    printf -- '- When stuck windows are found, write the complete alert body to the alert body file, invoke the alert helper yourself, and exit 0. The helper removes the body file after a successful send.\n\n'
    sed -n '1,$p' "$PROMPT_FILE"
} > "$PROMPT_INPUT"

CODEX_ARGS=(
    exec
    --cd "$AGENT_WORKDIR"
    --add-dir "$MAILBOX_DIR"
    --skip-git-repo-check
    --color never
    --ephemeral
    --sandbox workspace-write
    -c 'approval_policy="never"'
)

if [ -n "${MECHA_WATCHDOG_CODEX_MODEL:-}" ]; then
    CODEX_ARGS+=(--model "$MECHA_WATCHDOG_CODEX_MODEL")
fi

if [ -n "${MECHA_WATCHDOG_CODEX_PROFILE:-}" ]; then
    CODEX_ARGS+=(--profile "$MECHA_WATCHDOG_CODEX_PROFILE")
fi

log "starting codex watchdog agent in $REPO_ROOT"
"$PYTHON_PATH" - "$TIMEOUT_SECONDS" "$PROMPT_INPUT" "$CODEX_PATH" "${CODEX_ARGS[@]}" <<'PY'
import os
import signal
import subprocess
import sys

timeout = int(sys.argv[1])
prompt_path = sys.argv[2]
argv = sys.argv[3:]

with open(prompt_path, "rb") as prompt:
    proc = subprocess.Popen(
        argv,
        stdin=prompt,
        start_new_session=True,
    )
try:
    raise SystemExit(proc.wait(timeout=timeout))
except subprocess.TimeoutExpired:
    try:
        os.killpg(proc.pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        try:
            os.killpg(proc.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        proc.wait()
    raise SystemExit(124)
PY
status=$?
log "codex watchdog agent exited with status $status"
if [ "$status" -eq 0 ]; then
    if [ -s "$ALERT_BODY_FILE" ]; then
        unsent_preview="$(alert_body_preview "$ALERT_BODY_FILE")"
        send_failure_alert \
            "watchdog agent left an unsent alert body" \
            "alert_body_file=$ALERT_BODY_FILE

Unsent alert body preview:
$unsent_preview"
    else
        clear_failure_alert
    fi
elif [ "$status" -eq 124 ]; then
    send_failure_alert \
        "codex watchdog agent timed out" \
        "timeout_seconds=$TIMEOUT_SECONDS"
else
    send_failure_alert \
        "codex watchdog agent exited nonzero" \
        "exit_status=$status"
fi

cap_log_file

exit 0
