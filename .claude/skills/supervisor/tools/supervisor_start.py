#!/usr/bin/env python3
"""Start the mecha supervisor session.

Creates the 'mecha' tmux session, opens the supervisor window, and launches
Pi with the pinned OpenAI Codex GPT-5.5 runtime by default. The tmux
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
PI_MODEL = "gpt-5.5"
PI_MAILBOX_EXTENSION_ENV = "MECHA_PI_MAILBOX_EXTENSION"
PI_MAILBOX_EXTENSION_PATH = os.path.expanduser("~/.mecha-developer/extensions/mecha-messaging.js")
PI_ROUTER_URL_ENV = "MECHA_ROUTER_URL"
PI_COMPACTION_POLICY_ENV = "MECHA_PI_COMPACTION_POLICY"
PI_MODEL_KEY_ENV = "MECHA_PI_MODEL_KEY"
LOOPBACK_ROUTER_HOSTS = {"127.0.0.1", "::1", "localhost"}

CODEX_BOOTSTRAP_PROMPT = """Load the supervisor skill and mailbox skill from ~/.agents/skills, then operate as the supervisor.

Bootstrap now: read ~/personas/wolfgang.md, read the Codex supervisor memory indexes required by the supervisor skill, read supervisor state, discover running workers, reconcile state, and report current status. This machine is Codex-only for now: launch Codex workers when workers are needed, not Claude Code workers."""

CLAUDE_BOOTSTRAP_PROMPT = """/supervisor

Check the current supervisor state and resume all active work. Review any in-progress tasks, check on workers, and pick up where we left off."""

PI_BOOTSTRAP_PROMPT = """Load the supervisor skill and mailbox skill from ~/.agents/skills, then operate as the supervisor.

Bootstrap now: read ~/personas/wolfgang.md, read the supervisor memory indexes required by the supervisor skill, read supervisor state, discover running workers, reconcile state, and report current status. Launch future workers only with Pi using provider openai-codex and model gpt-5.5."""


def _validate_compaction_levels(levels):
    if not isinstance(levels, list) or not levels:
        raise ValueError("compaction policy must define at least one percentage level")
    for level in levels:
        percent = level if isinstance(level, (int, float)) else level.get("percent") if isinstance(level, dict) else None
        if not isinstance(percent, (int, float)) or percent <= 0 or percent >= 100:
            raise ValueError("compaction policy percentages must be greater than 0 and less than 100")


def validate_compaction_policy(policy):
    """Validate a JSON tiered policy, JSON levels policy, or comma-list percentages for Pi."""
    if policy is None:
        return None
    import json
    policy = policy.strip()
    if not policy:
        raise ValueError("compaction policy must not be empty")
    try:
        parsed = json.loads(policy)
    except json.JSONDecodeError:
        parsed = [float(part.strip()) for part in policy.split(",")]
    if isinstance(parsed, list):
        _validate_compaction_levels(parsed)
        return policy
    if not isinstance(parsed, dict):
        raise ValueError("compaction policy must be a levels list or tiered object")
    handoff = parsed.get("postCompactionNotification")
    if handoff is not None and not isinstance(handoff, str):
        raise ValueError("compaction policy postCompactionNotification must be a string")
    models = parsed.get("models")
    if models is not None and not isinstance(models, (dict, list)):
        raise ValueError("compaction policy models must be an object or list")
    if "levels" in parsed:
        _validate_compaction_levels(parsed.get("levels"))
        return policy
    tiers = parsed.get("tiers")
    if not isinstance(tiers, list) or not tiers:
        raise ValueError("compaction policy must define default levels or at least one context-size tier")
    for tier in tiers:
        if not isinstance(tier, dict):
            raise ValueError("compaction policy tiers must be objects")
        max_context = tier.get("maxContextTokens")
        if max_context is not None and (not isinstance(max_context, int) or max_context <= 0):
            raise ValueError("compaction policy tier maxContextTokens must be a positive integer or null")
        _validate_compaction_levels(tier.get("levels"))
    return policy


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
    """Return the explicit override or stable Mailbox V2 extension for Pi."""
    has_override = PI_MAILBOX_EXTENSION_ENV in os.environ
    configured_path = os.environ[PI_MAILBOX_EXTENSION_ENV] if has_override else PI_MAILBOX_EXTENSION_PATH
    extension_path = os.path.abspath(os.path.expanduser(configured_path))
    if not os.path.isfile(extension_path) or os.path.islink(extension_path):
        source = PI_MAILBOX_EXTENSION_ENV if has_override else "stable Pi extension"
        raise ValueError(
            f"{source} must name a regular, non-symlink extension file: {extension_path}. "
            f"Install the canonical Pi extension first: python3 "
            f"{os.path.join(os.path.dirname(__file__), 'install_pi_extension.py')}"
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
    parser.add_argument(
        "--compaction-policy",
        default=None,
        metavar="POLICY",
        help="Pi-only: context notification policy as tiered JSON, levels JSON, or comma-separated percentages.",
    )
    args = parser.parse_args()

    if args.compaction_policy is not None and args.agent != "pi":
        print("Error: --compaction-policy is only supported for Pi", file=sys.stderr)
        sys.exit(1)
    try:
        compaction_policy = validate_compaction_policy(args.compaction_policy)
    except ValueError as error:
        print(f"Error: {error}", file=sys.stderr)
        sys.exit(1)

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
    model_key_export = (
        f" && export {PI_MODEL_KEY_ENV}={shlex.quote(f'{PI_PROVIDER}/{PI_MODEL}')}"
        if args.agent == "pi" else ""
    )
    compaction_export = (
        f" && export {PI_COMPACTION_POLICY_ENV}={shlex.quote(compaction_policy)}"
        if args.agent == "pi" and compaction_policy is not None else ""
    )
    cmd = (
        f"cd {shlex.quote(work_dir)}"
        f" && unset CLAUDECODE"
        f"{router_export}"
        f"{model_key_export}"
        f"{compaction_export}"
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
