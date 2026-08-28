---
name: install
description: "Set up or reinstall mecha-developer by symlinking personas, skills, and hooks to the home directory. Trigger: user runs /install or explicitly asks to set up or reinstall mecha-developer. Do NOT use for routine operations — this is a one-time setup utility."
---

# Install Skill

Set up mecha-developer by symlinking personas and skills to the user's home directory, and by validating the Pi mailbox package registered from this checkout. The package must not be relocated, copied, symlinked, or restored as an installed artifact.

## Usage

```
/install
```

## What It Does

1. **Cleans up** old-style skill symlinks (`.md` files) from `~/.claude/skills/`
2. **Symlinks personas** to `~/personas/` so they're accessible from any project
3. **Symlinks skill folders** to `~/.claude/skills/` so they're available globally in Claude Code
4. **Symlinks the `ai` CLI script** to `~/.local/bin/`
5. **Validates** the canonical Pi mailbox package registration for bare Pi sessions

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

### 7. Symlink CLI Scripts

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

```

### Pi mailbox package (bare Pi)

The active package stays at `$REPO_ROOT/pi-extensions/mecha-messaging/`. Pi registers
that checkout directory and discovers its `index.ts` entrypoint. The package composes
the goal extension, so it is the sole registration for this extension graph. Do not
copy, move, symlink, or restore the package under `~/.mecha-developer` or another
installed-artifact path.

The package's `mailbox-client.ts` is the source of truth for plain-Node consumers.
Generate its checkout artifact before activation:

```bash
(cd "$REPO_ROOT/pi-extensions/mecha-messaging" && npm run generate:mailbox-client)
```

The installer copies only that generated `mailbox-client.js` into
`~/.mecha-developer/extensions/pi-extensions/mecha-messaging/mailbox-client.js` for the Claude
sidecar and harness. It verifies the copy byte-for-byte. Keep the settings
registration pointed at the checkout package, and validate it before launching Pi. The check rejects standalone `goal.js` and both old flat `goal.js`/`mecha-messaging.js` registrations, even when the package registration is also present:

```bash
REPO_ROOT="$(git -C "$(pwd)" rev-parse --show-toplevel)"
SOURCE="$REPO_ROOT/pi-extensions/mecha-messaging"
SETTINGS="$HOME/.pi/agent/settings.json"

if [ -d "$SOURCE" ] && [ ! -L "$SOURCE" ] && [ -f "$SETTINGS" ]; then
  python3 - "$SOURCE" "$SETTINGS" <<'PY'
import json, pathlib, sys
source, settings = map(pathlib.Path, sys.argv[1:])
source = source.resolve()
data = json.loads(settings.read_text())
packages = data.get("packages", [])
if not isinstance(packages, list):
    raise SystemExit("Pi settings packages must be a list")
registrations = []
legacy = []
for package in packages:
    if isinstance(package, str):
        path = pathlib.Path(package)
        if not path.is_absolute():
            path = settings.parent / path
        resolved = path.resolve()
        if resolved.name in {"goal.js", "mecha-messaging.js"}:
            legacy.append(package)
        if resolved == source:
            registrations.append(package)
if legacy:
    raise SystemExit(f"reject standalone or old flat Pi registration(s): {', '.join(legacy)}")
if len(registrations) != 1:
    raise SystemExit(f"expected exactly one Pi package registration resolving to {source}, found {len(registrations)}")
print(f"ok: one Pi package registration resolves to {source}")
PY
else
  echo "MISSING: checkout Pi package or Pi settings"
  exit 1
fi

(cd "$REPO_ROOT/pi-extensions/mecha-messaging" && npm run test:mecha-messaging)
test -f "$REPO_ROOT/pi-extensions/mecha-messaging/mailbox-client.js"
(cd "$REPO_ROOT/pi-extensions/mecha-messaging" && npm run check:mailbox-client-drift)
```

Global registration keeps the package available through Pi settings. Normal Pi
supervisor, worker, and service launches use Pi's default configured extensions and
pass neither `--extension` nor `--no-extensions`/`-ne`; those flags are reserved for
deliberate extension-test tools. If an existing bare Pi process needs the updated
package, reload it after the authorized source change.

### 8. Symlink Hooks


Symlink hook scripts from the skills directory into `~/.claude/hooks/`:

```bash
mkdir -p ~/.claude/hooks

# Retired Mailbox hook: Mailbox V2 delivery now comes from the runtime's
# verified integration (Pi package, Claude sidecar, or another bridge). Remove
# only a symlink at the legacy path; preserve non-symlink files for manual review.
HOOK_LINK="$HOME/.claude/hooks/mailbox-deliver.py"
if [ -L "$HOOK_LINK" ]; then
  unlink "$HOOK_LINK"
  echo "removed legacy mailbox hook symlink: $HOOK_LINK"
elif [ -e "$HOOK_LINK" ]; then
  echo "skipped non-symlink legacy mailbox hook: $HOOK_LINK"
fi

# Activity hook (all activity events — drives the router UI's working dot and subagent badge)
HOOK_SRC="$(pwd)/.claude/skills/supervisor/tools/hooks/claude-activity.sh"
HOOK_LINK="$HOME/.claude/hooks/claude-activity.sh"

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

Then verify `~/.claude/settings.json` has prompt suggestions disabled and current non-mailbox hooks registered. Use Python to safely merge these into the existing file rather than overwriting it:

```python
import json, os

settings_path = os.path.expanduser("~/.claude/settings.json")
settings = {}
if os.path.exists(settings_path):
    with open(settings_path) as f:
        settings = json.load(f)

# Disable prompt suggestions (they confuse the watchdog LLM)
settings["promptSuggestionEnabled"] = False

post_hooks = settings.setdefault("hooks", {}).setdefault("PostToolUse", [])
# Remove legacy mailbox hook registrations. Mailbox V2 delivery is runtime-owned,
# not a global Claude hook installed here.
removed_mailbox = 0
updated_post_hooks = []
for entry in post_hooks:
    hooks = entry.get("hooks", [])
    kept = [h for h in hooks if not h.get("command", "").endswith("mailbox-deliver.py")]
    removed_mailbox += len(hooks) - len(kept)
    if kept or not hooks:
        updated_post_hooks.append({**entry, "hooks": kept} if len(kept) != len(hooks) else entry)
settings["hooks"]["PostToolUse"] = updated_post_hooks
print(f"removed legacy mailbox hook registrations: {removed_mailbox}")
post_hooks = settings["hooks"]["PostToolUse"]

# Activity hooks: one entry per event, all running the same script with the event name.
#
# The dedupe matches on the script name appearing ANYWHERE in the command, not on the command
# ending with it. These commands end with the event name, so an endswith check never matches and
# every install run would append another eleven entries, without limit and without a symptom.
ACTIVITY_EVENTS = [
    "SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "SubagentStart",
    "SubagentStop", "Stop", "PreCompact", "PostCompact", "Notification", "SessionEnd",
]
activity_added = 0
for event_name in ACTIVITY_EVENTS:
    entries = settings.setdefault("hooks", {}).setdefault(event_name, [])
    already = any(
        any("claude-activity.sh" in h.get("command", "") for h in entry.get("hooks", []))
        for entry in entries
    )
    if already:
        continue
    entries.append({
        "matcher": "",
        "hooks": [{"type": "command", "command": f"~/.claude/hooks/claude-activity.sh {event_name}"}],
    })
    activity_added += 1
print(f"activity hooks: added {activity_added}, already present {len(ACTIVITY_EVENTS) - activity_added}")

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

### 9. Install supervisor tool-call recovery

Supervisor sessions can fail when Pi persists an orphaned tool result after
compaction. Keep the narrow recovery check on its own five-minute schedule:

```bash
REPO_ROOT="$(git rev-parse --show-toplevel)"
/usr/bin/python3 "$REPO_ROOT/.claude/skills/supervisor/tools/supervisor_tool_call_recovery.py" \
  --dry-run
```

Install exactly one marked schedule alongside the other local cron jobs:

```bash
RECOVERY="$REPO_ROOT/.claude/skills/supervisor/tools/supervisor_tool_call_recovery.py"
CURRENT_CRONTAB="$(crontab -l 2>/dev/null || true)"
FILTERED_CRONTAB="$(printf '%s\n' "$CURRENT_CRONTAB" | grep -Ev 'mecha-priority-handoff-monitor|priority_handoff_monitor.py|watchdog.py --priority-only' || true)"
if ! printf '%s\n' "$FILTERED_CRONTAB" | grep -q "mecha-supervisor-tool-call-recovery"; then
  FILTERED_CRONTAB="$FILTERED_CRONTAB
*/5 * * * * /usr/bin/python3 $RECOVERY >> ~/.mecha-developer/supervisor-tool-call-recovery.log 2>&1 # mecha-supervisor-tool-call-recovery"
  echo "added: supervisor tool-call recovery cron job"
else
  echo "skipped: supervisor tool-call recovery cron job already exists"
fi
printf '%s\n' "$FILTERED_CRONTAB" | crontab -
```

The recovery check only examines recent supervisor session files and restarts
the `mecha:supervisor` window when strict orphan-tool-result evidence exists.
Its log is bounded by the existing daily log-retention job.

### 10. Set Up Other Crontab Jobs

Add the memory-monitor cron job and remove retired mailbox/standing-orders/reminder producers when present:

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

# Remove legacy mailbox idle delivery cron entries. Mailbox V2 delivery is owned
# by each verified runtime integration, not a global tmux cron producer.
if crontab -l 2>/dev/null | grep -q "mailbox_cron.py"; then
  crontab -l 2>/dev/null | grep -v "mailbox_cron.py" | crontab -
  echo "removed legacy mailbox idle delivery cron job"
fi

# Standing-orders cron refresh is retired. Mailbox V2 router-based delivery
# owns supervisor inbox messages now, so the standing_orders_cron.py producer
# is obsolete. Remove any leftover entry instead of installing one.
if crontab -l 2>/dev/null | grep -q "standing_orders_cron.py"; then
  crontab -l 2>/dev/null | grep -v "standing_orders_cron.py" | crontab -
  echo "removed: retired standing-orders refresh cron job (Mailbox V2 router owns delivery)"
fi

# Memory monitor — runs every 5 minutes
if ! crontab -l 2>/dev/null | grep -q "memory_monitor.py"; then
  (crontab -l 2>/dev/null; echo "*/5 * * * * /usr/bin/python3 $TOOLS/memory_monitor.py >> ~/.mecha-developer/memory-monitor.log 2>&1") | crontab -
  echo "added: memory monitor cron job (runs every 5 minutes)"
else
  echo "skipped: memory monitor cron job already exists"
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
- **memory_monitor.py**: Runs every 5 minutes. Monitors context window usage for active workers and alerts when approaching limits.
- **Supervisor tool-call recovery**: Runs every five minutes. It detects strict orphan-tool-result crashes and restarts only the supervisor window with a recovery handoff.
- **Mailbox V2 router**: Persists and schedules durable reminders; no reminder cron job or reminder JSON file is used.
- **logrotate**: Runs daily. Rotates logs in `~/.mecha-developer/` when they exceed size limits; keeps 1 backup, discards anything older than 24 hours.

### 11. Verify

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
ls -la ~/.local/bin/ai 2>/dev/null || echo "none"

echo ""
echo "=== Pi Package ==="
REPO_ROOT="$(git -C "$(pwd)" rev-parse --show-toplevel)"
SOURCE="$REPO_ROOT/pi-extensions/mecha-messaging"
SETTINGS="$HOME/.pi/agent/settings.json"
if [ -d "$SOURCE" ] && [ ! -L "$SOURCE" ] && [ -f "$SETTINGS" ]; then
  echo "Pi package and settings are present; run the read-only registration check above."
else
  echo "MISSING: checkout Pi package or Pi settings"
fi

echo ""
echo "=== Crontab ==="
crontab -l 2>/dev/null | grep "mailbox_cron.py" && echo "WARN: retired mailbox cron job remains" || echo "no retired mailbox cron job"
crontab -l 2>/dev/null | grep "standing_orders_cron.py" && echo "WARN: retired standing-orders cron job remains" || echo "no retired standing-orders cron job (router owns delivery)"
crontab -l 2>/dev/null | grep "memory_monitor.py" || echo "no memory monitor cron job"
crontab -l 2>/dev/null | grep "mecha-supervisor-tool-call-recovery" || echo "no supervisor tool-call recovery cron job"
crontab -l 2>/dev/null | grep "reminders/tools/reminder_cron.py" && echo "WARN: retired reminder cron job remains" || echo "Mailbox V2 router owns reminder scheduling"
crontab -l 2>/dev/null | grep "kb_reminder_cron.py" && echo "WARN: retired KB maintenance reminder cron job remains" || echo "Router owns KB maintenance reminder scheduling"
```

### 12. Report

Print a summary of what was cleaned up, linked, updated, and skipped. If anything was skipped because a non-symlink file already existed, warn the user.
