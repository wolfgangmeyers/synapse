---
name: reminders
description: "Set one-shot reminders that fire via mailbox after a delay. Use /reminders to set, list, or delete reminders for any tmux window."
---

# Reminders Skill

Set durable Mailbox V2 reminders that deliver a message to any registered agent ID.

## Usage

```
/reminders set --window <window> --message "..." --in <duration>
/reminders set --window <window> --message "..." --at <iso-datetime>
/reminders list
/reminders delete <id>
/reminders delete-all
```

`--window me`, `--window self`, and `--window current` resolve to the current
tmux pane's window name. They fail closed outside tmux or when that name is not
a valid agent ID. Use this when setting a reminder for the current supervisor
session; it prevents hidden delivery to a literal `me` mailbox. All commands
use the local Mailbox V2 router; pass `--router-url` only for another exact
loopback HTTP(S) base URL.

## PR sweep reminder template

Use this reusable message when setting a PR sweep reminder:

> Sweep every repository in the relevant GitHub organization. Include every PR where the target reviewer previously posted a review and the current head differs from the head reviewed, regardless of current review-request assignment, other reviewers, stack or cross-PR comments, replacement PR numbers, or whether the PR is open, closed, or re-pushed. Treat review-request metadata as additional signal, not a gate. All findings are `COMMENT`-only, regardless of severity: never use `REQUEST_CHANGES` or any blocking review state.

## Recurring reminders

Use `--every <duration>` with `reminders set` to create a recurring reminder. Duration
forms include `30s`, `10m`, and `1h` (and the other forms listed below).
`--window current`, `--window self`, and `--window me` resolve to the current tmux
window. Use `reminders update <id> --every <duration>` to change an existing reminder
to recurring. If supported, `--in <duration>` changes it back to a one-shot reminder.

Recurring reminders require a live WebSocket lease for the target. Missed occurrences
are skipped. Delete a recurring reminder with `reminders delete <id>` when it is done.

## Examples

Set a reminder for 10 minutes from now:
```bash
python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window supervisor --message "Check PR #3881 for feedback" --in 10m
```

Set a reminder for 1 hour 30 minutes:
```bash
python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window supervisor --message "Follow up on standup action items" --in 1h30m
```

List all pending reminders:
```bash
python3 ~/.claude/skills/reminders/tools/reminders.py list
```

Delete a reminder by ID:
```bash
python3 ~/.claude/skills/reminders/tools/reminders.py delete <reminder-id>
```

## Duration Format

`--in` accepts: `Xs` (seconds), `Xm` (minutes), `Xh` (hours), `XhYm`, `XhYmZs`

Examples: `30s`, `10m`, `1h`, `1h30m`, `2h15m30s`

## Delivery

The Mailbox V2 router persists reminders and delivers due envelopes. It owns
scheduling; do not run the retired V1 `reminder_cron.py` or edit a local
reminder JSON file. `set_reminder.py` remains only as a compatibility entry
point and delegates to `reminders.py set`.

Reminder delivery means execute the named action and report evidence or the exact
block; acknowledgement or generic status without action is not compliant.
