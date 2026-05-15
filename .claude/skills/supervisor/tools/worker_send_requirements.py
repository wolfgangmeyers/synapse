#!/usr/bin/env python3
"""Send (or re-send) requirements to a worker.

Reads the requirements field from the supervisor state for the given task,
wraps it in a context message, and sends it to the worker's tmux window.

Usage:
  python worker_send_requirements.py --alias ALIAS [--update] [--dry-run]

Modes:
  Default:    Send requirements as initial context
  --update:   Wrap with "Your requirements have been updated:" prefix
  --dry-run:  Print what would be sent without sending to tmux

Examples:
  # Send initial requirements
  python worker_send_requirements.py --alias my-task

  # Re-send after requirements were updated
  python worker_send_requirements.py --alias my-task --update

  # Preview without sending
  python worker_send_requirements.py --alias my-task --dry-run
"""

import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state, find_task


def build_message(requirements, update=False):
    """Build the message to send to the worker."""
    if update:
        return (
            "Your requirements have been updated. "
            "Please review and adjust your work accordingly:\n\n"
            f"{requirements}"
        )
    return (
        "Here are the detailed requirements for your task:\n\n"
        f"{requirements}"
    )


def main():
    parser = argparse.ArgumentParser(
        description="Send requirements to a worker from supervisor state."
    )
    parser.add_argument("--alias", required=True, help="Task alias or ID")
    parser.add_argument(
        "--update",
        action="store_true",
        help="Wrap as an update notification (requirements changed)",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print what would be sent without actually sending",
    )

    args = parser.parse_args()

    # Resolve identifier
    identifier = args.alias
    try:
        identifier = int(identifier)
    except ValueError:
        pass

    state = load_state()
    task = find_task(state, identifier)

    if task is None:
        print(f"Error: task '{args.alias}' not found.", file=sys.stderr)
        sys.exit(1)

    requirements = task.get("requirements")
    if not requirements:
        print(
            f"Error: task '{args.alias}' has no requirements set.",
            file=sys.stderr,
        )
        sys.exit(1)

    window = task.get("window")
    if not window or window.startswith("(none"):
        if not args.dry_run:
            print(
                f"Error: task '{args.alias}' has no active worker window.",
                file=sys.stderr,
            )
            sys.exit(1)

    message = build_message(requirements, update=args.update)

    if args.dry_run:
        alias = task.get("alias", args.alias)
        mode = "UPDATE" if args.update else "INITIAL"
        print(f"[dry-run] Would send {mode} requirements to window '{window}' (task: {alias}):")
        print()
        print(message)
        return

    from _tmux import send_text_and_enter, window_exists

    if not window_exists(window):
        print(f"Error: window '{window}' does not exist", file=sys.stderr)
        sys.exit(1)

    send_text_and_enter(window, message)

    alias = task.get("alias", args.alias)
    mode = "updated" if args.update else "initial"
    print(f"Sent {mode} requirements to '{window}' (task: {alias}).")
    print()
    print("--- Reminders ---")
    print(f"1. Update supervisor state:")
    print(f'   python3 state_update_status.py {alias} "Sent {mode} requirements to worker"')


if __name__ == "__main__":
    main()
