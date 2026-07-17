---
name: install
description: "Set up or reinstall mecha-developer by symlinking personas, skills, and hooks to the home directory. Trigger: user runs /install or explicitly asks to set up or reinstall mecha-developer. Do NOT use for routine operations — this is a one-time setup utility."
---

# Install Skill

Set up mecha-developer by symlinking personas and skills to the user's home directory, and by copying the Pi mailbox extension to its stable local path.

## Usage

```
/install
```

## What It Does

1. **Cleans up** old-style skill symlinks (`.md` files) from `~/.claude/skills/`
2. **Symlinks personas** to `~/personas/` so they're accessible from any project
3. **Symlinks skill folders** to `~/.claude/skills/` so they're available globally in Claude Code
4. **Symlinks CLI scripts** (`ai`, `mecha-supervisor`) to `~/.local/bin/`
5. **Copies and registers** the canonical Pi mailbox extension for bare Pi sessions

## Process

### 1. Determine Paths

```bash
# Get the repo root (where this skill lives)
REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# Fallback: if running from Claude Code, use the working directory
REPO_ROOT="${REPO_ROOT:-$(pwd)}"
```

The repo root is the mecha-developer project directory. Detect it from the current working directory.

### 2. Clean Up Old Skill Symlinks

Remove any old-style `.md` file symlinks in `~/.claude/skills/` that point into this project. These are leftovers from before skills were restructured into directories.

```bash
for f in ~/.claude/skills/*.md; do
  if [ -L "$f" ]; then
    target=$(readlink "$f")
    if echo "$target" | grep -q "mecha-developer"; then
      unlink "$f"
      echo "cleaned up old symlink: $f -> $target"
    fi
  fi
done
```

### 2a. Retire Legacy Recall

Recall's tmux/cron/Claude-hook implementation is retired. This cleanup is
idempotent and deliberately removes only its canonical runtime producers and
state; it does not create or configure a replacement.

```bash
# Remove only crontab lines that invoke the retired recall producer.
RECALL_CRON_FRAGMENT="/.claude/skills/recall/tools/recall_cron.py"
CURRENT_CRONTAB="$(crontab -l 2>/dev/null || true)"
FILTERED_CRONTAB="$(printf '%s\n' "$CURRENT_CRONTAB" | grep -Fv "$RECALL_CRON_FRAGMENT" || true)"
if [ "$CURRENT_CRONTAB" != "$FILTERED_CRONTAB" ]; then
  printf '%s\n' "$FILTERED_CRONTAB" | crontab -
  echo "removed: retired recall cron job"
else
  echo "skipped: retired recall cron job is absent"
fi

# Remove only the exact legacy PostToolUse command; preserve every other hook
# and every other setting.
python3 - <<'PY'
import json
import os
from pathlib import Path

settings_path = Path.home() / ".claude/settings.json"
legacy_command = "~/.claude/hooks/recall-hook.py"
if not settings_path.exists():
    print("skipped: Claude settings file is absent")
    raise SystemExit

settings = json.loads(settings_path.read_text())
post_hooks = settings.get("hooks", {}).get("PostToolUse", [])
updated = []
removed = 0
for entry in post_hooks:
    hooks = entry.get("hooks", [])
    kept = [
        hook for hook in hooks
        if not (hook.get("type") == "command" and hook.get("command") == legacy_command)
    ]
    removed += len(hooks) - len(kept)
    if kept or not hooks:
        if len(kept) != len(hooks):
            entry = {**entry, "hooks": kept}
        updated.append(entry)
if removed:
    settings["hooks"]["PostToolUse"] = updated
    settings_path.write_text(json.dumps(settings, indent=2) + "\n")
    print(f"removed: {removed} retired recall PostToolUse hook registration(s)")
else:
    print("skipped: retired recall PostToolUse hook registration is absent")
PY

# Remove legacy symlinks only. Non-symlink files are deliberately preserved.
for link in \
  "$HOME/.claude/skills/recall" \
  "$HOME/.agents/skills/recall" \
  "$HOME/.claude/hooks/recall-hook.py"; do
  if [ -L "$link" ]; then
    unlink "$link"
    echo "removed legacy symlink: $link"
  elif [ -e "$link" ]; then
    echo "skipped non-symlink legacy path: $link"
  fi
done

# Remove only Recall's runtime state and log. Do not follow symlinks or remove
# an unexpected non-file/non-directory path.
RECALL_STATE="$HOME/.mecha-developer/recall"
if [ -L "$RECALL_STATE" ]; then
  unlink "$RECALL_STATE"
elif [ -d "$RECALL_STATE" ]; then
  rm -rf "$RECALL_STATE"
elif [ -e "$RECALL_STATE" ]; then
  echo "skipped unexpected non-directory recall state path: $RECALL_STATE"
fi

RECALL_LOG="$HOME/.mecha-developer/recall-cron.log"
if [ -L "$RECALL_LOG" ]; then
  unlink "$RECALL_LOG"
elif [ -f "$RECALL_LOG" ]; then
  rm -f "$RECALL_LOG"
elif [ -e "$RECALL_LOG" ]; then
  echo "skipped unexpected non-file recall log path: $RECALL_LOG"
fi
```

### 3. Symlink Personas

```bash
# Create ~/personas if it doesn't exist
mkdir -p ~/personas

# Symlink each persona file
for f in personas/*.md; do
  [ -e "$f" ] || continue
  name=$(basename "$f")
  target="$(pwd)/personas/$name"
  link="$HOME/personas/$name"

  if [ -L "$link" ]; then
    ln -sf "$target" "$link"
    echo "updated: $link -> $target"
  elif [ -e "$link" ]; then
    echo "skipped: $link already exists (not a symlink)"
  else
    ln -s "$target" "$link"
    echo "linked: $link -> $target"
  fi
done
```

### 4. Symlink Skill Folders

Each skill is a directory containing a `SKILL.md`. Symlink the entire directory.

```bash
# Create ~/.claude/skills if it doesn't exist
mkdir -p ~/.claude/skills

# Symlink each skill directory (except install itself)
for d in .claude/skills/*/; do
  name=$(basename "$d")
  [ "$name" = "install" ] && continue

  target="$(pwd)/.claude/skills/$name"
  link="$HOME/.claude/skills/$name"

  if [ -L "$link" ]; then
    ln -sf "$target" "$link"
    echo "updated: $link -> $target"
  elif [ -e "$link" ]; then
    echo "skipped: $link already exists (not a symlink)"
  else
    ln -s "$target" "$link"
    echo "linked: $link -> $target"
  fi
done
```

### 5. Symlink Workflows

The workflows database lives at `~/.mecha-developer/workflows.json` at runtime, but the canonical copy is in the repo. Symlink it so all machines share the same workflows and additions are committed back automatically.

```bash
mkdir -p ~/.mecha-developer

WORKFLOWS_SRC="$(pwd)/.claude/skills/workflows/workflows.json"
WORKFLOWS_LINK="$HOME/.mecha-developer/workflows.json"

if [ -L "$WORKFLOWS_LINK" ]; then
  ln -sf "$WORKFLOWS_SRC" "$WORKFLOWS_LINK"
  echo "updated: $WORKFLOWS_LINK -> $WORKFLOWS_SRC"
elif [ -e "$WORKFLOWS_LINK" ]; then
  echo "skipped: $WORKFLOWS_LINK already exists (not a symlink)"
else
  ln -s "$WORKFLOWS_SRC" "$WORKFLOWS_LINK"
  echo "linked: $WORKFLOWS_LINK -> $WORKFLOWS_SRC"
fi
```

### 6. Symlink Skills for Codex

Codex looks for skills in `~/.agents/skills/`. Create a symlink so it finds the same skills as Claude Code.

```bash
mkdir -p ~/.agents

if [ -L "$HOME/.agents/skills" ]; then
  ln -sfn "$HOME/.claude/skills" "$HOME/.agents/skills"
  echo "updated: ~/.agents/skills -> ~/.claude/skills"
elif [ -e "$HOME/.agents/skills" ]; then
  echo "skipped: ~/.agents/skills already exists (not a symlink)"
else
  ln -s "$HOME/.claude/skills" "$HOME/.agents/skills"
  echo "linked: ~/.agents/skills -> ~/.claude/skills"
fi
```

### 6. Symlink CLI Scripts

Create `~/.local/bin/` if needed and symlink CLI entry points:

```bash
mkdir -p ~/.local/bin

# ai — wrapper for claude --dangerously-skip-permissions
AI_SRC="$(pwd)/bin/ai"
AI_LINK="$HOME/.local/bin/ai"
if [ -L "$AI_LINK" ]; then
  ln -sf "$AI_SRC" "$AI_LINK"
  echo "updated: $AI_LINK -> $AI_SRC"
elif [ -e "$AI_LINK" ]; then
  echo "skipped: $AI_LINK already exists (not a symlink)"
else
  ln -s "$AI_SRC" "$AI_LINK"
  echo "linked: $AI_LINK -> $AI_SRC"
fi

# mecha-supervisor — launches the supervisor tmux session
SUP_SRC="$(pwd)/.claude/skills/supervisor/tools/supervisor_start.py"
SUP_LINK="$HOME/.local/bin/mecha-supervisor"
if [ -L "$SUP_LINK" ]; then
  ln -sf "$SUP_SRC" "$SUP_LINK"
  echo "updated: $SUP_LINK -> $SUP_SRC"
elif [ -e "$SUP_LINK" ]; then
  echo "skipped: $SUP_LINK already exists (not a symlink)"
else
  ln -s "$SUP_SRC" "$SUP_LINK"
  echo "linked: $SUP_LINK -> $SUP_SRC"
fi
```

### Pi mailbox extension (bare Pi)

Do this before launching Pi. It copies—not symlinks—the canonical extension to
`~/.mecha-developer/extensions/mecha-messaging.js`, then registers that exact
path once in `~/.pi/agent/settings.json`. The installer uses the
`pi-extension-maintenance` transaction: it validates regular source/target
files, stages the copy beside the target, retains rollback copies through
verification, atomically rewrites settings, preserves unrelated settings and
packages, and removes duplicate registrations only for the exact stable path.
It never deletes a differently named or stale extension registration.

```bash
REPO_ROOT="$(git -C "$(pwd)" rev-parse --show-toplevel)"
SOURCE="$REPO_ROOT/pi-extensions/mecha-messaging.js"
TARGET="$HOME/.mecha-developer/extensions/mecha-messaging.js"
SETTINGS="$HOME/.pi/agent/settings.json"
INSTALLER="$REPO_ROOT/.claude/skills/supervisor/tools/install_pi_extension.py"

# Preflight: stop on any failure without mutating the installed artifact/settings.
test -f "$SOURCE" && test ! -L "$SOURCE"
test -f "$REPO_ROOT/tests/pi_extension_silent.test.mjs"
node --test "$REPO_ROOT/tests/pi_extension_silent.test.mjs"
node --check "$SOURCE"
(
  cd "$REPO_ROOT/.claude/skills/supervisor/tools"
  python3 -m unittest test_supervisor_start test_install_pi_extension
)
python3 "$INSTALLER"

# Final status: target must be regular, byte-identical, and registered exactly once.
python3 - "$SOURCE" "$TARGET" "$SETTINGS" <<'PY'
import json, pathlib, sys
source, target, settings = map(pathlib.Path, sys.argv[1:])
data = json.loads(settings.read_text())
packages = data.get("packages", [])
if not target.is_file() or target.is_symlink() or source.read_bytes() != target.read_bytes():
    raise SystemExit(f"invalid installed Pi extension: {target}")
count = packages.count(str(target))
if count != 1:
    raise SystemExit(f"expected one stable Pi registration, found {count}")
stale = [p for p in packages if isinstance(p, str) and p.endswith("/mecha-messaging.js") and p != str(target)]
print(f"ok: {target}; target registrations: {count}; stale registrations retained: {len(stale)}")
for path in stale:
    print(f"stale (not removed): {path}")
PY

# Only after the final status check succeeds, discard transaction rollback copies.
rm -f "$TARGET.rollback" "$SETTINGS.mecha-messaging.rollback"
```

Global registration is for bare Pi only. New supervisor and worker processes
remain explicitly launched with `--no-extensions --extension "$TARGET"`; they
do not rely on global extension discovery. If an existing bare Pi process needs
the update, reload it after the final check.

### 7. Symlink Hooks


Symlink hook scripts from the skills directory into `~/.claude/hooks/`:

```bash
mkdir -p ~/.claude/hooks

# Mailbox delivery hook (PreToolUse — all tools)
HOOK_SRC="$(pwd)/.claude/skills/supervisor/tools/hooks/mailbox-deliver.py"
HOOK_LINK="$HOME/.claude/hooks/mailbox-deliver.py"

if [ -L "$HOOK_LINK" ]; then
  ln -sf "$HOOK_SRC" "$HOOK_LINK"
  echo "updated: $HOOK_LINK -> $HOOK_SRC"
elif [ -e "$HOOK_LINK" ]; then
  echo "skipped: $HOOK_LINK already exists (not a symlink)"
else
  ln -s "$HOOK_SRC" "$HOOK_LINK"
  echo "linked: $HOOK_LINK -> $HOOK_SRC"
fi

# PR feedback reminder hook (PostToolUse — Bash)
HOOK_SRC="$(pwd)/.claude/skills/supervisor/tools/hooks/pr-feedback-reminder.py"
HOOK_LINK="$HOME/.claude/hooks/pr-feedback-reminder.py"

if [ -L "$HOOK_LINK" ]; then
  ln -sf "$HOOK_SRC" "$HOOK_LINK"
  echo "updated: $HOOK_LINK -> $HOOK_SRC"
elif [ -e "$HOOK_LINK" ]; then
  echo "skipped: $HOOK_LINK already exists (not a symlink)"
else
  ln -s "$HOOK_SRC" "$HOOK_LINK"
  echo "linked: $HOOK_LINK -> $HOOK_SRC"
fi
```

Then verify `~/.claude/settings.json` has the PostToolUse mailbox delivery hook registered and prompt suggestions disabled. Use Python to safely merge these into the existing file rather than overwriting it:

```python
import json, os

settings_path = os.path.expanduser("~/.claude/settings.json")
settings = {}
if os.path.exists(settings_path):
    with open(settings_path) as f:
        settings = json.load(f)

# Disable prompt suggestions (they confuse the watchdog LLM)
settings["promptSuggestionEnabled"] = False

# Ensure PostToolUse hook for mailbox delivery exists
post_hooks = settings.setdefault("hooks", {}).setdefault("PostToolUse", [])
mailbox_hook_entry = {
    "matcher": "",
    "hooks": [{"type": "command", "command": "~/.claude/hooks/mailbox-deliver.py"}]
}
# Check if already registered (match on command path)
already_registered = any(
    any(h.get("command", "").endswith("mailbox-deliver.py") for h in entry.get("hooks", []))
    for entry in post_hooks
)
if not already_registered:
    post_hooks.append(mailbox_hook_entry)
    print("added: PostToolUse mailbox delivery hook")
else:
    print("skipped: PostToolUse mailbox delivery hook already registered")

# Ensure PostToolUse hook for PR feedback reminder exists
pr_reminder_entry = {
    "matcher": "Bash",
    "hooks": [{"type": "command", "command": "~/.claude/hooks/pr-feedback-reminder.py"}]
}
pr_already = any(
    any(h.get("command", "").endswith("pr-feedback-reminder.py") for h in entry.get("hooks", []))
    for entry in post_hooks
)
if not pr_already:
    post_hooks.append(pr_reminder_entry)
    print("added: PostToolUse PR feedback reminder hook")
else:
    print("skipped: PostToolUse PR feedback reminder hook already registered")

with open(settings_path, "w") as f:
    json.dump(settings, f, indent=2)
    f.write("\n")
print(f"set: promptSuggestionEnabled = false in {settings_path}")
```

### 8. Set Up Dashboard Venv

The supervisor dashboard (`tools/responsibilities-server.py`) runs Flask. Create a venv with Flask installed so `dashboard_start.py` can activate it.

```bash
VENV_DIR="$(pwd)/tools/.venv"

if [ -d "$VENV_DIR" ] && "$VENV_DIR/bin/python3" -c "import flask" 2>/dev/null; then
  echo "skipped: dashboard venv already exists with Flask"
else
  python3 -m venv "$VENV_DIR"
  "$VENV_DIR/bin/pip" install --quiet flask
  echo "created: $VENV_DIR with Flask installed"
fi
```

### 9. Set Up Crontab

Add cron jobs for mailbox idle delivery, inbound mail polling, standing-order refresh, memory monitoring, and stuck-worker detection:

```bash
TOOLS="$(pwd)/.claude/skills/supervisor/tools"

# Ensure crontab has a PATH that includes ~/.local/bin (claude), homebrew bin (tmux, etc.),
# and homebrew sbin (logrotate is installed under sbin on macOS).
if ! crontab -l 2>/dev/null | grep -q "^PATH="; then
  (echo 'PATH=/Users/'"$USER"'/.local/bin:/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin:/usr/bin:/bin'; crontab -l 2>/dev/null) | crontab -
  echo "added: PATH to crontab"
else
  echo "skipped: crontab PATH already set"
fi

# Mailbox idle delivery — runs every 10 seconds (6 staggered per-minute entries)
if ! crontab -l 2>/dev/null | grep -q "mailbox_cron.py"; then
  CRON_CMD="/usr/bin/python3 $TOOLS/mailbox_cron.py >> ~/.mecha-developer/mailbox-cron.log 2>&1"
  (crontab -l 2>/dev/null; \
    echo "* * * * * $CRON_CMD"; \
    echo "* * * * * sleep 10; $CRON_CMD"; \
    echo "* * * * * sleep 20; $CRON_CMD"; \
    echo "* * * * * sleep 30; $CRON_CMD"; \
    echo "* * * * * sleep 40; $CRON_CMD"; \
    echo "* * * * * sleep 50; $CRON_CMD" \
  ) | crontab -
  echo "added: mailbox idle delivery cron job (runs every 10 seconds)"
else
  echo "skipped: mailbox cron job already exists"
fi

# Standing-orders refresh — posts current supervisor standing orders via mailbox every 2 hours.
if ! crontab -l 2>/dev/null | grep -q "standing_orders_cron.py"; then
  (crontab -l 2>/dev/null; echo "0 */2 * * * /usr/bin/python3 $TOOLS/standing_orders_cron.py >> ~/.mecha-developer/standing-orders-cron.log 2>&1") | crontab -
  echo "added: standing-orders refresh cron job (runs every 2 hours)"
else
  echo "skipped: standing-orders refresh cron job already exists"
fi

# Memory monitor — runs every 5 minutes
if ! crontab -l 2>/dev/null | grep -q "memory_monitor.py"; then
  (crontab -l 2>/dev/null; echo "*/5 * * * * /usr/bin/python3 $TOOLS/memory_monitor.py >> ~/.mecha-developer/memory-monitor.log 2>&1") | crontab -
  echo "added: memory monitor cron job (runs every 5 minutes)"
else
  echo "skipped: memory monitor cron job already exists"
fi

# Watchdog — checks for stuck workers every 30 minutes, mails supervisor if found.
# Wrapped in `launchctl asuser $UID` so the watchdog inherits Wolfgang's GUI login
# session. Required because the claude CLI stores credentials in the macOS keychain
# ("Claude Code-credentials"), which is unreachable from cron's stripped environment.
# `bash -lc` additionally sources login profiles so PATH and env vars match an
# interactive shell. DO NOT strip this wrapper — doing so silently breaks the
# watchdog whenever credentials are keychain-backed (e.g. claude after a relogin).
EXISTING_WATCHDOG="$(crontab -l 2>/dev/null | grep "watchdog.py" || true)"
if [ -z "$EXISTING_WATCHDOG" ]; then
  USER_UID="$(id -u)"
  WATCHDOG_CMD="launchctl asuser $USER_UID /bin/bash -lc '/usr/bin/python3 $TOOLS/watchdog.py >> \$HOME/.mecha-developer/watchdog.log 2>&1'"
  (crontab -l 2>/dev/null; echo "*/30 * * * * $WATCHDOG_CMD") | crontab -
  echo "added: watchdog cron job (runs every 30 minutes, via launchctl asuser for keychain access)"
elif ! echo "$EXISTING_WATCHDOG" | grep -q "launchctl asuser"; then
  echo ""
  echo "================================================================"
  echo "WARNING: existing watchdog cron entry is missing the"
  echo "  'launchctl asuser \$UID /bin/bash -lc ...' wrapper."
  echo ""
  echo "On macOS the watchdog cannot reach the keychain without it, so"
  echo "claude auth lookups silently fail every 30 minutes."
  echo ""
  echo "Current entry:"
  echo "  $EXISTING_WATCHDOG"
  echo ""
  echo "Fix: run \`crontab -e\` and replace the watchdog line with:"
  echo "  */30 * * * * launchctl asuser \$(id -u) /bin/bash -lc '/usr/bin/python3 $TOOLS/watchdog.py >> \$HOME/.mecha-developer/watchdog.log 2>&1'"
  echo "================================================================"
  echo ""
else
  echo "skipped: watchdog cron job already exists (wrapped correctly)"
fi

# Mailbox V2 router owns reminder scheduling. Remove retired cron producers
# so they cannot create duplicate or filesystem-backed deliveries.
if crontab -l 2>/dev/null | grep -q "reminders/tools/reminder_cron.py"; then
  crontab -l 2>/dev/null | grep -v "reminders/tools/reminder_cron.py" | crontab -
  echo "removed: retired reminder cron job (Mailbox V2 router owns scheduling)"
fi
if crontab -l 2>/dev/null | grep -q "kb_reminder_cron.py"; then
  crontab -l 2>/dev/null | grep -v "kb_reminder_cron.py" | crontab -
  echo "removed: retired KB maintenance reminder cron job (router owns scheduling)"
fi

# Log rotation — runs daily, keeps 1 backup, discards logs older than 24 hours.
# Requires `logrotate` (not bundled with macOS). Install via brew if missing,
# and write a default config covering ~/.mecha-developer/*.log if absent.
if ! command -v logrotate >/dev/null 2>&1 && [ ! -x /opt/homebrew/sbin/logrotate ] && [ ! -x /usr/local/sbin/logrotate ]; then
  if command -v brew >/dev/null 2>&1; then
    brew install logrotate
    echo "installed: logrotate via brew"
  else
    echo "WARN: logrotate not installed and brew unavailable — log rotation will not run"
  fi
else
  echo "skipped: logrotate already installed"
fi

LOGROTATE_CONF="$HOME/.mecha-developer/logrotate.conf"
if [ ! -e "$LOGROTATE_CONF" ]; then
  cat > "$LOGROTATE_CONF" <<'CONF'
/Users/USERNAME/.mecha-developer/*.log {
    daily
    rotate 1
    maxage 1
    missingok
    notifempty
    copytruncate
    compress
    delaycompress
}
CONF
  # Substitute the actual username (logrotate does not expand ~ or env vars).
  sed -i.bak "s|/Users/USERNAME/|$HOME/|" "$LOGROTATE_CONF" && rm -f "$LOGROTATE_CONF.bak"
  echo "wrote: $LOGROTATE_CONF"
else
  echo "skipped: $LOGROTATE_CONF already exists"
fi

if ! crontab -l 2>/dev/null | grep -q "logrotate"; then
  (crontab -l 2>/dev/null; echo "@daily logrotate --state ~/.mecha-developer/logrotate.status ~/.mecha-developer/logrotate.conf") | crontab -
  echo "added: logrotate cron job (runs daily)"
else
  echo "skipped: logrotate cron job already exists"
fi
```

The cron jobs:
- **mailbox_cron.py**: Runs every 10 seconds (6 staggered entries). Checks all agent mailboxes for unread messages older than 30 seconds and delivers to tmux windows.
- **standing_orders_cron.py**: Runs every 2 hours. Reads current supervisor standing orders and posts them to the supervisor mailbox from the `standing-orders-cron` system sender.
- **memory_monitor.py**: Runs every 5 minutes. Monitors context window usage for active workers and alerts when approaching limits.
- **watchdog.py**: Runs every 30 minutes. Captures all tmux window output, asks an LLM to evaluate whether any workers are stuck (idle without being told to wait), and sends supervisor alerts through Mailbox.

  **LLM config (optional)**: `~/.mecha-developer/watchdog-llm.json` selects which CLI the watchdog uses. Schema: `{"cli": "claude" | "codex", "model": "<model>", "extra_args": []}`. If the file is missing, defaults are `claude` + `claude-sonnet-4-6` + `[]`. An invalid `cli` value is rejected with a loud ntfy notification rather than silent fallback.
- **Mailbox V2 router**: Persists and schedules durable reminders; no reminder cron job or reminder JSON file is used.
- **logrotate**: Runs daily. Rotates logs in `~/.mecha-developer/` when they exceed size limits; keeps 1 backup, discards anything older than 24 hours.

### 9b. Preflight: Watchdog LLM Login Check

Run a tiny ping against the configured watchdog CLI to surface auth issues at install time rather than 30 minutes later when the first cron tick happens.

```bash
WATCHDOG_CFG="$HOME/.mecha-developer/watchdog-llm.json"
WATCHDOG_CLI="claude"
if [ -f "$WATCHDOG_CFG" ]; then
  WATCHDOG_CLI="$(/usr/bin/python3 -c "import json; print(json.load(open('$WATCHDOG_CFG')).get('cli', 'claude'))" 2>/dev/null || echo claude)"
fi

if [ "$WATCHDOG_CLI" = "codex" ]; then
  PING_OUT="$(codex exec ping 2>&1 || true)"
else
  PING_OUT="$(claude -p ping 2>&1 || true)"
fi

if echo "$PING_OUT" | grep -qiE "not logged in|please run /login|codex login"; then
  echo ""
  echo "================================================================"
  echo "WARNING: $WATCHDOG_CLI is not logged in."
  echo "Run \`$WATCHDOG_CLI /login\` (claude) or \`$WATCHDOG_CLI login\` (codex)"
  echo "before the watchdog cron tick or it will silently fail every 30 min."
  echo "================================================================"
  echo ""
else
  echo "preflight: $WATCHDOG_CLI ping succeeded (or returned a non-auth error — continuing)"
fi
```

This is a non-fatal check — any non-auth error (model unavailable, network blip) is ignored. The watchdog itself will surface auth failures via ntfy at runtime.

### 10. Verify

List what was installed:

```bash
echo ""
echo "=== Personas ==="
ls -la ~/personas/*.md 2>/dev/null || echo "none"

echo ""
echo "=== Skills ==="
ls -la ~/.claude/skills/ 2>/dev/null || echo "none"

echo ""
echo "=== CLI Scripts ==="
ls -la ~/.local/bin/ai ~/.local/bin/mecha-supervisor 2>/dev/null || echo "none"

echo ""
echo "=== Pi Extension ==="
TARGET="$HOME/.mecha-developer/extensions/mecha-messaging.js"
SETTINGS="$HOME/.pi/agent/settings.json"
if [ -f "$TARGET" ] && [ ! -L "$TARGET" ] && [ -f "$SETTINGS" ]; then
  python3 - "$TARGET" "$SETTINGS" <<'PY'
import json, pathlib, sys
target, settings = map(pathlib.Path, sys.argv[1:])
print(f"target registrations: {json.loads(settings.read_text()).get('packages', []).count(str(target))}")
PY
else
  echo "MISSING: stable Pi extension or Pi settings"
fi

echo "=== Dashboard Venv ==="
if [ -d "tools/.venv" ] && tools/.venv/bin/python3 -c "import flask" 2>/dev/null; then
  echo "ok: tools/.venv exists with Flask"
else
  echo "MISSING: tools/.venv or Flask not installed"
fi

echo ""
echo "=== Crontab ==="
crontab -l 2>/dev/null | grep "mailbox_cron.py" || echo "no mailbox cron job"
crontab -l 2>/dev/null | grep "standing_orders_cron.py" || echo "no standing-orders refresh cron job"
crontab -l 2>/dev/null | grep "memory_monitor.py" || echo "no memory monitor cron job"
crontab -l 2>/dev/null | grep "watchdog.py" || echo "no watchdog cron job"
crontab -l 2>/dev/null | grep "reminders/tools/reminder_cron.py" && echo "WARN: retired reminder cron job remains" || echo "Mailbox V2 router owns reminder scheduling"
crontab -l 2>/dev/null | grep "kb_reminder_cron.py" && echo "WARN: retired KB maintenance reminder cron job remains" || echo "Router owns KB maintenance reminder scheduling"
```

### 11. Report

Print a summary of what was cleaned up, linked, updated, and skipped. If anything was skipped because a non-symlink file already existed, warn the user.
