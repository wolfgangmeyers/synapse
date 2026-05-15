#!/usr/bin/env python3
"""
PostToolUse hook: PR feedback reminder.

Fires after a Bash tool call that contains `git push` or `gh pr create`.
Injects a reminder telling the agent to mail the supervisor so a PR feedback
polling reminder gets set.

Workers don't set reminders themselves — the supervisor owns that. So the
hook tells the worker to send a mailbox message, and the supervisor sets
the reminder when it receives the mail.

Exit codes:
  0 - always (hook never blocks)
"""

import json
import re
import sys


def is_push_or_pr_create(command):
    """Return a description if the command is a git push or gh pr create."""
    if not isinstance(command, str):
        return None
    # git push (but not --dry-run)
    if re.search(r'\bgit\s+push\b', command) and '--dry-run' not in command:
        return "git push"
    # gh pr create
    if re.search(r'\bgh\s+pr\s+create\b', command):
        return "gh pr create"
    return None


def was_successful(hook_input):
    """Check if the tool call succeeded (exit code 0)."""
    response = hook_input.get("tool_response", {})
    if isinstance(response, dict):
        # Check for error indicators
        stdout = response.get("stdout", "")
        stderr = response.get("stderr", "")
        # gh pr create prints the URL on success
        # git push prints to stderr normally but exits 0
        return True  # PostToolUse only fires on success
    return True


REMINDER_PUSH = """\
📬 [PR Feedback Reminder] You just pushed to a remote branch. \
Before pushing, robot-review should have been run — confirm it was (unless the supervisor determined one wasn't needed). \
If this branch has an open PR (or you're about to create one), \
mail the supervisor so they can set a reminder to poll for review feedback:

python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \\
  --to supervisor \\
  --subject "PR feedback reminder needed" \\
  --body "Just pushed to <branch>. PR #<number> needs a feedback polling reminder (Greptile, CI, reviewers)."

Replace <branch> and <number> with the actual values. \
If you already mailed the supervisor about this push, ignore this reminder. \
Exception: pushes to main on mecha-developer or other non-PR branches don't need feedback reminders."""

REMINDER_PR_CREATE = """\
📬 [PR Feedback Reminder] You just created a PR. \
Mail the supervisor so they can set a reminder to poll for review feedback:

python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \\
  --to supervisor \\
  --subject "PR feedback reminder needed" \\
  --body "Just created PR #<number> (<url>). Set a feedback polling reminder (Greptile, CI, reviewers)."

Replace <number> and <url> with the values from the PR creation output. \
If you already mailed the supervisor about this PR, ignore this reminder."""


def main():
    try:
        hook_input = json.load(sys.stdin)
    except (json.JSONDecodeError, ValueError):
        sys.exit(0)

    command = hook_input.get("tool_input", {}).get("command", "")
    action = is_push_or_pr_create(command)

    if not action:
        sys.exit(0)

    reminder = REMINDER_PR_CREATE if action == "gh pr create" else REMINDER_PUSH

    output = {
        "hookSpecificOutput": {
            "hookEventName": "PostToolUse",
            "additionalContext": reminder,
        }
    }
    print(json.dumps(output))
    sys.exit(0)


if __name__ == "__main__":
    main()
