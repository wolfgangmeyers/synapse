#!/usr/bin/env python3
"""Gracefully shut down a worker: send /exit, wait, then kill the window.

Usage:
  python worker_shutdown.py --window WINDOW [--wait SECONDS] [--verified]

Sends /exit to Claude Code, waits for it to shut down, then kills the
tmux window and the worker's mailbox sidecar. This is the standard cleanup
sequence for workers and pipelines.

Requires verification that the worker's output has been checked before shutdown.
"""

import argparse
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _sidecar import cleanup_sidecar
from _tmux_rest import TmuxRestClient


def window_exists(window):
    return TmuxRestClient().exists(window)


def send_text_and_enter(window, text):
    client = TmuxRestClient()
    client.keys(window, "\x1b[200~")
    client.keys(window, text)
    client.keys(window, "\x1b[201~")
    time.sleep(5)
    # REST keys are literal terminal input; CR preserves Enter/submit semantics.
    client.keys(window, "\r")


def kill_window(window):
    try:
        TmuxRestClient().remove(window)
    except Exception:
        pass
    return cleanup_sidecar(window)


def main():
    parser = argparse.ArgumentParser(
        description="Gracefully shut down a worker."
    )
    parser.add_argument("--window", required=True, help="Tmux window name")
    parser.add_argument(
        "--wait",
        type=int,
        default=3,
        help="Seconds to wait after /exit before killing window (default: 3)",
    )
    parser.add_argument(
        "--verified",
        action="store_true",
        default=False,
        help=(
            "Confirm that a quality check has been completed before shutdown. "
            "Must only be set after: (1) the worker reported completion, "
            "(2) a background subagent verified the output is correct and complete, "
            "and (3) the supervisor is satisfied the task was done correctly."
        ),
    )

    args = parser.parse_args()

    if not args.verified:
        print(
            "ERROR: --verified flag not set.\n"
            "\n"
            "Do not shut down a worker until you have confirmed its work is correct:\n"
            "  1. Worker has reported completion (e.g. via mailbox).\n"
            "  2. A background subagent has reviewed the output and confirmed it is\n"
            "     correct and complete (check commits, read key files, run tests).\n"
            "  3. You are satisfied the task was done correctly.\n"
            "\n"
            "Once all checks are done, re-run with --verified to proceed with shutdown.",
            file=sys.stderr,
        )
        sys.exit(1)

    if not window_exists(args.window):
        print(f"Window '{args.window}' does not exist (already cleaned up).")
        # Still sweep the sidecar: an orphaned sidecar outliving its window is the
        # exact leak this handles, and it keeps accepting mail for a dead worker.
        if not cleanup_sidecar(args.window):
            sys.exit(1)
        return

    # Send /exit to Claude
    send_text_and_enter(args.window, "/exit")
    print(f"Sent /exit to '{args.window}'. Waiting {args.wait}s for shutdown...")

    time.sleep(args.wait)

    # Kill the window. This also sweeps the mailbox sidecar — see _tmux.kill_window.
    sidecar_clean = kill_window(args.window)
    print(f"Killed window '{args.window}'.")

    # Reminders
    print()
    print("--- Reminders ---")
    print("1. Update supervisor state:")
    print(f"   python3 state_update_field.py <alias> window \"(none — worker exited)\"")
    print("2. If the PR is merged, complete the task:")
    print(f"   python3 state_update_state.py <alias> completed --result \"<result>\"")
    print("3. If the PR is still open, keep the task active — relaunch if feedback arrives.")
    print("4. If abandoning, remove the worktree too:")
    print(f"   cd <project-path> && git worktree remove --force <worktree-path>")
    print(f"   python3 state_update_state.py <alias> abandoned --reason \"<reason>\"")

    if not sidecar_clean:
        print(
            "5. FIRST: resolve the mailbox sidecar warning above. Until it is gone, "
            "mail addressed to this agent is accepted and lost, so do not mark the "
            "window free yet."
        )
        sys.exit(1)


if __name__ == "__main__":
    main()
