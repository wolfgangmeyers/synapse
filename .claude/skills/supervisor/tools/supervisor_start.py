#!/usr/bin/env python3
"""Start the mecha supervisor session.

Creates the 'mecha' tmux session, opens the supervisor window, sets
MECHA_AGENT_WINDOW=supervisor so the mailbox hook can deliver messages,
launches Codex by default, and sends the bootstrap prompt to activate supervisor
mode.

Usage:
  python3 supervisor_start.py [--dir DIR] [--agent codex|claude]

Options:
  --dir DIR       Working directory for the supervisor (default: ~/code/mecha-developer)
  --agent AGENT   Agent to launch: codex (default) or claude

The script exits immediately after sending the bootstrap prompt. Attach to
the session to interact:
  tmux attach -t mecha
"""

import argparse
import os
import shlex
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import SESSION, ensure_session, send_text, send_enter, session_exists

SUPERVISOR_WINDOW = "supervisor"
DEFAULT_DIR = os.path.expanduser("~/code/mecha-developer")

CODEX_BOOTSTRAP_PROMPT = """Load the supervisor skill and mailbox skill from ~/.agents/skills, then operate as the supervisor.

Bootstrap now: read ~/personas/wolfgang.md, read the Codex supervisor memory indexes required by the supervisor skill, read supervisor state, discover running workers, reconcile state, and report current status. This machine is Codex-only for now: launch Codex workers when workers are needed, not Claude Code workers."""

CLAUDE_BOOTSTRAP_PROMPT = """/supervisor

Check the current supervisor state and resume all active work. Review any in-progress tasks, check on workers, and pick up where we left off."""


def agent_command(agent, prompt):
    if agent == "codex":
        return " ".join(
            [
                "codex",
                "--no-alt-screen",
                "-a",
                "never",
                "-s",
                "danger-full-access",
                shlex.quote(prompt),
            ]
        )
    if agent == "claude":
        return "claude --dangerously-skip-permissions"
    raise ValueError(f"unsupported agent: {agent}")


def main():
    parser = argparse.ArgumentParser(description="Start the mecha supervisor session.")
    parser.add_argument(
        "--dir",
        default=DEFAULT_DIR,
        help=f"Working directory for the supervisor (default: {DEFAULT_DIR})",
    )
    parser.add_argument(
        "--agent",
        choices=["codex", "claude"],
        default="codex",
        help="Agent to launch: codex (default) or claude",
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

    prompt = CODEX_BOOTSTRAP_PROMPT if args.agent == "codex" else CLAUDE_BOOTSTRAP_PROMPT
    command = agent_command(args.agent, prompt)

    # Launch the supervisor agent with mailbox identity.
    cmd = (
        f"cd {shlex.quote(work_dir)}"
        f" && unset CLAUDECODE"
        f" && export MECHA_AGENT_WINDOW={SUPERVISOR_WINDOW}"
        f" && {command}"
    )
    send_text(SUPERVISOR_WINDOW, cmd)
    time.sleep(0.5)
    send_enter(SUPERVISOR_WINDOW)

    print(f"Launched {args.agent} in supervisor window.")
    print()
    print(f"Attach to the session to interact:")
    print(f"  tmux attach -t {SESSION}")


if __name__ == "__main__":
    main()
