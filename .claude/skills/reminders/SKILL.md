---
name: reminders
description: "Set one-shot reminders that fire via mailbox after a delay. Use /reminders to set, list, or delete reminders for any tmux window."
---

# Reminders Skill

Set time-based reminders that deliver a mailbox message to any tmux window.

## Usage

```
/reminders set --window <window> --message "..." --in <duration>
/reminders set --window <window> --message "..." --at <iso-datetime>
/reminders list
/reminders delete <id>
/reminders delete-all
```

`--window me`, `--window self`, and `--window current` resolve to the current
managed agent window from `MECHA_AGENT_WINDOW` / `MECHA_WORKER_WINDOW`, falling
back to `supervisor`. Use this when setting a reminder for the current
supervisor session; it prevents hidden delivery to a literal `me` mailbox.

## Examples

Set a reminder for 10 minutes from now:
```bash
python3 ~/.claude/skills/reminders/tools/set_reminder.py \
  --window supervisor --message "Check PR #3881 for feedback" --in 10m
```

Set a reminder for 1 hour 30 minutes:
```bash
python3 ~/.claude/skills/reminders/tools/set_reminder.py \
  --window supervisor --message "Follow up on standup action items" --in 1h30m
```

List all pending reminders:
```bash
python3 ~/.claude/skills/reminders/tools/manage_reminders.py list
```

Delete a reminder by ID:
```bash
python3 ~/.claude/skills/reminders/tools/manage_reminders.py delete r-1709123456789
```

## Duration Format

`--in` accepts: `Xs` (seconds), `Xm` (minutes), `Xh` (hours), `XhYm`, `XhYmZs`

Examples: `30s`, `10m`, `1h`, `1h30m`, `2h15m30s`

## Storage

Reminders are stored in `~/.mecha-developer/reminders.json`.
The cron job `reminder_cron.py` runs every minute and fires due reminders via mailbox.
