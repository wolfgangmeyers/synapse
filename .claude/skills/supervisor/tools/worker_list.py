#!/usr/bin/env python3
"""List all tmux windows in the mecha session.

Usage:
  python worker_list.py

Shows all windows with their index and name. Used during startup/resume
to reconcile running windows with supervisor state.

Examples:
  python worker_list.py
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import session_exists, list_windows


def main():
    if not session_exists():
        print("No mecha tmux session running.")
        print()
        print("--- Reminders ---")
        print("1. A session will be created automatically when you launch a worker.")
        return

    windows = list_windows()
    if not windows:
        print("Mecha session exists but has no windows.")
        return

    print(f"Mecha session: {len(windows)} window(s)")
    print()
    for idx, name in windows:
        print(f"  {idx}: {name}")

    # Reminders
    print()
    print("--- Reminders ---")
    print("1. Reconcile these windows with supervisor state:")
    print("   python3 state_read.py")
    print("2. Flag tasks whose windows no longer exist (crashed workers).")
    print("3. Flag windows that don't match any tracked task (orphans).")


if __name__ == "__main__":
    main()
