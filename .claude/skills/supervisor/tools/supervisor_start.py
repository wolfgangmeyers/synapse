#!/usr/bin/env python3
"""Start the mecha supervisor session.

Creates the 'mecha' tmux session, opens the supervisor window, and launches
Pi with the pinned OpenAI Codex GPT-5.6 Terra runtime by default. The tmux
window name supplies the mailbox identity.
sends the bootstrap prompt to activate supervisor mode.

Usage:
  python3 supervisor_start.py [--dir DIR] [--agent pi|codex|claude]

Options:
  --dir DIR       Working directory for the supervisor (default: ~/code/mecha-developer)
  --agent AGENT   Agent to launch: pi (default), codex, or claude

The script exits immediately after sending the bootstrap prompt. Attach to
the session to interact:
  tmux attach -t mecha
"""

import argparse
import os
import shlex
import sys
import time
from urllib.parse import urlparse

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import SESSION, ensure_session, send_text, send_enter, session_exists

SUPERVISOR_WINDOW = "supervisor"
DEFAULT_DIR = os.path.expanduser("~/code/mecha-developer")
PI_PROVIDER = "openai-codex"
PI_MODEL = "gpt-5.6-terra"
PI_MAILBOX_EXTENSION_ENV = "MECHA_PI_MAILBOX_EXTENSION"
PI_ROUTER_URL_ENV = "MECHA_ROUTER_URL"
LOOPBACK_ROUTER_HOSTS = {"127.0.0.1", "::1", "localhost"}

CODEX_BOOTSTRAP_PROMPT = """Load the supervisor skill and mailbox skill from ~/.agents/skills, then operate as the supervisor.

Bootstrap now: read ~/personas/wolfgang.md, read the Codex supervisor memory indexes required by the supervisor skill, read supervisor state, discover running workers, reconcile state, and report current status. This machine is Codex-only for now: launch Codex workers when workers are needed, not Claude Code workers."""

CLAUDE_BOOTSTRAP_PROMPT = """/supervisor

Check the current supervisor state and resume all active work. Review any in-progress tasks, check on workers, and pick up where we left off."""

PI_BOOTSTRAP_PROMPT = """Load the supervisor skill and mailbox skill from ~/.agents/skills, then operate as the supervisor.

Bootstrap now: read ~/personas/wolfgang.md, read the supervisor memory indexes required by the supervisor skill, read supervisor state, discover running workers, reconcile state, and report current status. Launch future workers only with Pi using provider openai-codex and model gpt-5.6-terra."""


def resolve_pi_router_url():
    """Return the configured loopback HTTP(S) Mailbox V2 router base URL."""
    configured_url = os.environ.get(PI_ROUTER_URL_ENV)
    if not configured_url:
        raise ValueError(f"{PI_ROUTER_URL_ENV} is required for Pi supervisor startup")
    parsed = urlparse(configured_url)
    if (parsed.scheme not in {"http", "https"}
            or parsed.hostname not in LOOPBACK_ROUTER_HOSTS
            or parsed.path not in {"", "/"}
            or parsed.params or parsed.query or parsed.fragment):
        raise ValueError(
            f"{PI_ROUTER_URL_ENV} must be a loopback HTTP(S) base URL without a path"
        )
    return configured_url.rstrip("/")


def resolve_pi_mailbox_extension():
    """Return the explicitly configured Mailbox V2 extension for Pi."""
    configured_path = os.environ.get(PI_MAILBOX_EXTENSION_ENV)
    if not configured_path:
        raise ValueError(
            f"{PI_MAILBOX_EXTENSION_ENV} is required for Pi supervisor startup; "
            "refusing to rely on globally discovered extensions."
        )

    extension_path = os.path.abspath(os.path.expanduser(configured_path))
    if not os.path.isfile(extension_path):
        raise ValueError(
            f"{PI_MAILBOX_EXTENSION_ENV} does not name a readable extension file: "
            f"{extension_path}"
        )
    return extension_path


def agent_command(agent, prompt, pi_mailbox_extension=None):
    if agent == "pi":
        pi_mailbox_extension = pi_mailbox_extension or resolve_pi_mailbox_extension()
        return " ".join(
            [
                "pi",
                "--provider",
                PI_PROVIDER,
                "--model",
                PI_MODEL,
                "--name",
                SUPERVISOR_WINDOW,
                "--no-extensions",
                "--extension",
                shlex.quote(pi_mailbox_extension),
                "--approve",
                shlex.quote(prompt),
            ]
        )
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
        choices=["pi", "codex", "claude"],
        default="pi",
        help="Agent to launch: pi (default), codex, or claude",
    )
    args = parser.parse_args()

    work_dir = os.path.expanduser(args.dir)
    if not os.path.isdir(work_dir):
        print(f"Error: directory does not exist: {work_dir}", file=sys.stderr)
        sys.exit(1)

    pi_mailbox_extension = None
    pi_router_url = None
    if args.agent == "pi":
        try:
            pi_mailbox_extension = resolve_pi_mailbox_extension()
            pi_router_url = resolve_pi_router_url()
        except ValueError as error:
            print(f"Error: {error}", file=sys.stderr)
            sys.exit(1)

    if session_exists():
        print(f"Error: tmux session '{SESSION}' already exists.", file=sys.stderr)
        print(f"  Attach with: tmux attach -t {SESSION}", file=sys.stderr)
        print(f"  Or kill it:  tmux kill-session -t {SESSION}", file=sys.stderr)
        sys.exit(1)

    # Create session — _tmux.ensure_session() creates it with window 0 named "supervisor"
    ensure_session()
    print(f"Created tmux session '{SESSION}' with window '{SUPERVISOR_WINDOW}'.")

    prompt = {
        "pi": PI_BOOTSTRAP_PROMPT,
        "codex": CODEX_BOOTSTRAP_PROMPT,
        "claude": CLAUDE_BOOTSTRAP_PROMPT,
    }[args.agent]
    command = agent_command(args.agent, prompt, pi_mailbox_extension)

    # Launch the supervisor agent; its tmux window name is its mailbox identity.
    router_export = (
        f" && export {PI_ROUTER_URL_ENV}={shlex.quote(pi_router_url)}"
        if args.agent == "pi" else ""
    )
    cmd = (
        f"cd {shlex.quote(work_dir)}"
        f" && unset CLAUDECODE"
        f"{router_export}"
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
