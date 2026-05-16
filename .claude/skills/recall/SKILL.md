---
name: recall
description: Proactive memory surfacing system — detects when stored knowledge is relevant and delivers it via mailbox before the agent needs to be told. Not user-invocable; runs automatically via hook + cron.
---

# Recall System

Proactive memory surfacing. A PostToolUse hook schedules deferred recall checks; a cron job fires them, uses Haiku to evaluate which stored memories are relevant to the current conversation, and delivers matches via mailbox.

## How It Works

1. **Hook** (`recall_hook.py`): PostToolUse hook fires on every tool call. Schedules a recall check N seconds in the future (debounced — if one is already scheduled, does nothing).
2. **Cron** (`recall_cron.py`): Runs every minute. For due events: captures tmux context, reads memory indexes, asks Haiku for relevance, checks cooldown, delivers via mailbox.
3. **Delivery**: Relevant memories arrive as mailbox messages with `[RECALL]` prefix from the `recall` system sender.

## Configuration

Config file: `~/.mecha-developer/recall/config.json`

| Key | Default | Description |
|-----|---------|-------------|
| `enabled` | `true` | Master on/off switch |
| `delay_seconds` | `60` | Seconds after hook fires before recall check runs |
| `cooldown_messages` | `12` | Messages between repeated recalls of same memory |
| `capture_lines` | `500` | Lines of tmux output to capture for context |
| `model` | `haiku` | Claude model for evaluation |
| `session_retention_days` | `7` | Days to keep session state |
| `stores` | `[]` | Array of memory store configs |

Each store: `{ "name": "...", "index_path": "...", "base_path": "...", "instructions": "..." }`

The optional `instructions` field provides per-store evaluation guidance passed to the Haiku prompt (e.g., "Only recall messages about deployment failures").

## Management Scripts

```bash
# Configuration
python3 tools/recall_config.py show                          # Show config
python3 tools/recall_config.py set --delay-seconds 30        # Change a value
python3 tools/recall_config.py add-store \
  --name kb \
  --index-path ~/.mecha-developer/kb/MEMORY.md \
  --base-path ~/.mecha-developer/kb \
  --instructions "Focus on action items and decisions"       # Add a store
python3 tools/recall_config.py enable                        # Enable
python3 tools/recall_config.py disable                       # Disable

# Activation (per-window runtime control)
python3 tools/recall_activate.py --window W --store S        # Activate a store
python3 tools/recall_activate.py --window W --store S --deactivate  # Deactivate
python3 tools/recall_activate.py --window W --list           # List active stores

# Status
python3 tools/recall_status.py                               # Full overview

# Cleanup
python3 tools/recall_cleanup.py prune --older-than-days 7    # Prune old sessions
python3 tools/recall_cleanup.py list                         # List sessions
python3 tools/recall_cleanup.py delete WINDOW                # Delete one
python3 tools/recall_cleanup.py reset-all                    # Delete all
```

## State Files

- `~/.mecha-developer/recall/config.json` — configuration
- `~/.mecha-developer/recall/scheduled.json` — pending scheduled events
- `~/.mecha-developer/recall/sessions/<window>.json` — per-session cooldown state
- `~/.mecha-developer/recall/active_stores.json` — per-window store activation state

## Installation

### Hook registration (settings.json)

Add to `PostToolUse` hooks:
```json
{
  "hooks": [
    {
      "type": "command",
      "command": "~/.claude/hooks/recall-hook.py"
    }
  ]
}
```

### Cron registration

```bash
if ! crontab -l 2>/dev/null | grep -q "recall_cron.py"; then
  RECALL_CRON="$REPO_ROOT/.claude/skills/recall/tools/recall_cron.py"
  (crontab -l 2>/dev/null; echo "* * * * * /usr/bin/python3 $RECALL_CRON >> ~/.mecha-developer/recall-cron.log 2>&1") | crontab -
fi
```

### Symlink for hook

```bash
ln -sf "$REPO_ROOT/.claude/skills/recall/tools/recall_hook.py" ~/.claude/hooks/recall-hook.py
```

## Design Notes

- **Cooldown keys** use `store:relative-path` format (e.g., `project:feedback_no_inline_infra.md`) to avoid basename collisions across stores.
- **Cooldown** is message-count based, not time-based. After a memory is delivered, it won't be delivered again until `cooldown_messages` more messages have been processed in that window.
- **Store activation** is a runtime concept — stores must be activated for a window before recall fires. If no stores are activated, recall is skipped entirely.
- The `recall` sender is in `SYSTEM_SENDERS` for both `mailbox-deliver.py` and `mailbox_cron.py`, so recall messages don't trigger the mandatory state-update block for the supervisor.
- Most recall checks return nothing — that's by design. Only memories that would prevent a mistake or provide missing context are surfaced.
