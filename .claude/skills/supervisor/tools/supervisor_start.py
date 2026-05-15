#!/usr/bin/env python3
"""Start the mecha supervisor session.

Creates the 'mecha' tmux session, opens the supervisor window, sets
MECHA_AGENT_WINDOW=supervisor so the mailbox hook can deliver messages,
launches Claude Code, and sends the bootstrap prompt to activate supervisor mode.

Usage:
  python3 supervisor_start.py [--dir DIR]

Options:
  --dir DIR   Working directory for Claude (default: ~/code/mecha-wolfgang)

The script exits immediately after sending the bootstrap prompt. Attach to
the session to interact:
  tmux attach -t mecha
"""

import argparse
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import SESSION, ensure_session, send_text, send_enter, session_exists

SUPERVISOR_WINDOW = "supervisor"
DEFAULT_DIR = os.path.expanduser("~/code/mecha-wolfgang")

BOOTSTRAP_PROMPT = """/supervisor

Check the current supervisor state and resume all active work. Review any in-progress tasks, check on workers, and pick up where we left off."""


def main():
    parser = argparse.ArgumentParser(description="Start the mecha supervisor session.")
    parser.add_argument(
        "--dir",
        default=DEFAULT_DIR,
        help=f"Working directory for Claude (default: {DEFAULT_DIR})",
    )
    args = parser.parse_args()

    work_dir = os.path.expanduser(args.dir)
    if not os.path.isdir(work_dir):
        print(f"Error: directory does not exist: {work_dir}", file=sys.stderr)
        sys.exit(1)

    if session_exists():
        print(f"Error: tmux session '{SESSION}' already exists.", file=sys.stderr)
        print(f"  Attach with: tmux attach -t {SESSION}", file=sys.stderr)
        print(f"  Or kill it:  tmux kill-session -t {SESSION}", file=sys.stderr)
        sys.exit(1)

    # Create session — _tmux.ensure_session() creates it with window 0 named "supervisor"
    ensure_session()
    print(f"Created tmux session '{SESSION}' with window '{SUPERVISOR_WINDOW}'.")

    # Launch Claude: cd to mecha-wolfgang, set supervisor identity, start Claude
    cmd = (
        f"cd {work_dir}"
        f" && unset CLAUDECODE"
        f" && export MECHA_AGENT_WINDOW={SUPERVISOR_WINDOW}"
        f" && claude --dangerously-skip-permissions"
    )
    send_text(SUPERVISOR_WINDOW, cmd)
    time.sleep(0.5)
    send_enter(SUPERVISOR_WINDOW)

    print("Launched Claude Code in supervisor window.")
    print("Waiting for Claude to initialize...")
    time.sleep(15)

    # Send bootstrap prompt
    send_text(SUPERVISOR_WINDOW, BOOTSTRAP_PROMPT)
    time.sleep(5)  # large text — give Claude Code time to render paste dialog
    send_enter(SUPERVISOR_WINDOW)

    print("Bootstrap prompt sent.")
    print()
    print(f"Attach to the session to interact:")
    print(f"  tmux attach -t {SESSION}")


if __name__ == "__main__":
    main()
