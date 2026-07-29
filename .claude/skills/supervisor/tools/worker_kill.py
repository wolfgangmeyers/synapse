#!/usr/bin/env python3
"""Kill a worker's tmux window and its mailbox sidecar.

Usage:
  python worker_kill.py --window WINDOW

Kills the named tmux window. Use after sending /exit to a worker,
or to clean up a failed launch.

Examples:
  python worker_kill.py --window worker-1
  python worker_kill.py --window review-3645
"""

import argparse
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import kill_window, window_exists


def main():
    parser = argparse.ArgumentParser(
        description="Kill a worker's tmux window."
    )
    parser.add_argument("--window", required=True, help="Tmux window name to kill")

    args = parser.parse_args()

    existed = window_exists(args.window)
    # kill_window also sweeps the mailbox sidecar — see _tmux.kill_window. It runs
    # whether or not the window existed, so already-orphaned sidecars get swept.
    sidecar_clean = kill_window(args.window)

    if existed:
        print(f"Killed window '{args.window}'.")
    else:
        print(f"Window '{args.window}' did not exist (already cleaned up).")

    # Reminders
    print()
    print("--- Reminders ---")
    print("1. Update supervisor state to reflect the window is gone:")
    print(f"   python3 state_update_field.py <alias> window \"(none — worker exited)\"")
    print("2. If the task is done and PR is merged:")
    print(f"   python3 state_update_state.py <alias> completed --result \"<result>\"")
    print("3. If abandoning:")
    print(f"   python3 state_update_state.py <alias> abandoned --reason \"<reason>\"")
    print("4. If the PR is still open, keep the task active and the worktree intact.")

    if not sidecar_clean:
        print(
            "5. FIRST: resolve the mailbox sidecar warning above. Until it is gone, "
            "mail addressed to this agent is accepted and lost, so do not mark the "
            "window free yet."
        )
        sys.exit(1)


if __name__ == "__main__":
    main()
