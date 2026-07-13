#!/usr/bin/env python3
"""Launch a new worker in a tmux window.

Usage:
  python worker_launch.py --window WINDOW --dir DIR [--resume SESSION_ID] [--skip-permissions] [--agent pi|codex|claude|cursor] [--reasoning-effort low|medium|high|xhigh]

Creates a tmux window, starts the agent in the specified directory,
and waits for it to initialize. Deliver all instructions with mailbox_send.py
after verifying the agent registered with Mailbox V2.
"""

import argparse
import json
import os
import re
import shlex
import sqlite3
import subprocess
import sys
import time
import uuid
from urllib.parse import urlparse

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import ensure_session, new_window, send_text, send_enter, window_exists


PI_PROVIDER = "openai-codex"
PI_MODEL = "gpt-5.6-terra"
PI_MAILBOX_EXTENSION_ENV = "MECHA_PI_MAILBOX_EXTENSION"
PI_MAILBOX_EXTENSION_PATH = os.path.expanduser("~/.mecha-developer/extensions/mecha-messaging.js")
PI_ROUTER_URL_ENV = "MECHA_ROUTER_URL"
LOOPBACK_ROUTER_HOSTS = {"127.0.0.1", "::1", "localhost"}


def _quote_command(argv):
    return " ".join(shlex.quote(part) for part in argv)


def pi_session_id(work_dir, window):
    """Return the stable UUID Pi uses for a worker's initial session."""
    return str(uuid.uuid5(uuid.NAMESPACE_URL, f"mecha-worker:{work_dir}:{window}"))


def resolve_pi_router_url():
    """Return the configured loopback HTTP(S) Mailbox V2 router base URL."""
    configured_url = os.environ.get(PI_ROUTER_URL_ENV)
    if not configured_url:
        raise ValueError(f"{PI_ROUTER_URL_ENV} is required for Pi workers")
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
    """Return the explicit, validated Mailbox V2 extension path for Pi."""
    extension_path = (
        os.environ[PI_MAILBOX_EXTENSION_ENV]
        if PI_MAILBOX_EXTENSION_ENV in os.environ
        else PI_MAILBOX_EXTENSION_PATH
    )
    if not os.path.isfile(extension_path):
        raise ValueError(
            f"{PI_MAILBOX_EXTENSION_ENV} must name a validated extension file. "
            f"Install the canonical Pi extension first: "
            f"python3 {os.path.join(os.path.dirname(__file__), 'install_pi_extension.py')}"
        )
    return extension_path


def get_cursor_auth_token():
    """Read the Cursor auth token from its SQLite state DB."""
    db = os.path.expanduser("~/.config/Cursor/User/globalStorage/state.vscdb")
    conn = sqlite3.connect(db)
    row = conn.execute(
        "SELECT value FROM ItemTable WHERE key='cursorAuth/accessToken'"
    ).fetchone()
    conn.close()
    return row[0] if row else None


def _cursor_pretrust_workspace(work_dir):
    """Pre-trust a workspace directory for cursor-agent TUI mode.

    Creates ~/.cursor/projects/<slug>/.workspace-trusted to suppress the
    workspace trust dialog. Slug = path with '/' replaced by '-', leading '/' dropped.
    e.g. /home/wolfgang/code/foo → home-wolfgang-code-foo
    """
    import json as _json
    from datetime import datetime, timezone

    slug = work_dir.lstrip("/").replace("/", "-")
    trust_dir = os.path.join(os.path.expanduser("~/.cursor/projects"), slug)
    os.makedirs(trust_dir, exist_ok=True)
    trust_file = os.path.join(trust_dir, ".workspace-trusted")
    if not os.path.exists(trust_file):
        trust_data = {
            "trustedAt": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z"),
            "workspacePath": work_dir,
        }
        with open(trust_file, "w") as f:
            _json.dump(trust_data, f)


def _codex_pretrust_workspace(work_dir):
    """Pre-trust a workspace directory for Codex TUI mode.

    Codex stores workspace trust in ~/.codex/config.toml:
      [projects."/abs/path"]
      trust_level = "trusted"
    """
    config_file = os.path.join(os.path.expanduser("~/.codex"), "config.toml")
    os.makedirs(os.path.dirname(config_file), exist_ok=True)

    path_literal = json.dumps(work_dir)
    header = f"[projects.{path_literal}]"

    try:
        with open(config_file, "r") as f:
            config = f.read()
    except FileNotFoundError:
        config = ""

    table_pattern = re.compile(
        rf"(?ms)^(?P<header>\[projects\.{re.escape(path_literal)}\]\s*\n)"
        rf"(?P<body>.*?)(?=^\[|\Z)"
    )
    match = table_pattern.search(config)
    if match:
        body = match.group("body")
        if re.search(r'(?m)^\s*trust_level\s*=\s*"trusted"\s*$', body):
            return

        if re.search(r"(?m)^\s*trust_level\s*=", body):
            body = re.sub(
                r'(?m)^(\s*trust_level\s*=\s*).*$',
                r'\1"trusted"',
                body,
                count=1,
            )
        else:
            body = body.rstrip() + '\ntrust_level = "trusted"\n'

        config = config[: match.start("body")] + body + config[match.end("body") :]
    else:
        prefix = "\n\n" if config and not config.endswith("\n\n") else ""
        config = config + prefix + f'{header}\ntrust_level = "trusted"\n'

    with open(config_file, "w") as f:
        f.write(config)


def main():
    parser = argparse.ArgumentParser(
        description="Launch a Pi worker by default in a new tmux window."
    )
    parser.add_argument("--window", required=True, help="Tmux window name")
    parser.add_argument("--dir", required=True, help="Working directory for the agent")
    parser.add_argument("--resume", default=None, help="Session ID to resume")
    parser.add_argument(
        "--skip-permissions",
        action="store_true",
        default=True,
        help="Use --dangerously-skip-permissions flag (default: True)",
    )
    parser.add_argument(
        "--no-skip-permissions",
        action="store_false",
        dest="skip_permissions",
        help="Do NOT use --dangerously-skip-permissions flag",
    )

    parser.add_argument(
        "--init",
        action="store_true",
        default=False,
        help="Run 'mise trust' and 'winit' in the working directory before launching (for new worktrees)",
    )

    parser.add_argument(
        "--agent",
        default="pi",
        choices=["pi", "claude", "codex", "cursor"],
        help="Agent to launch: 'pi' (default, pinned to OpenAI Codex GPT-5.6 Terra), or a legacy runtime",
    )

    parser.add_argument(
        "--message",
        default=None,
        help="Unused; deliver instructions through Mailbox V2. Kept for backwards compatibility.",
    )
    parser.add_argument(
        "--reasoning-effort",
        choices=["low", "medium", "high", "xhigh"],
        default=None,
        help="Codex-only reasoning effort override for this worker launch.",
    )

    args = parser.parse_args()

    work_dir = os.path.abspath(os.path.expanduser(args.dir))
    pi_mailbox_extension = None
    pi_router_url = None
    if args.agent == "pi":
        try:
            pi_mailbox_extension = resolve_pi_mailbox_extension()
            pi_router_url = resolve_pi_router_url()
        except ValueError as error:
            print(f"Error: {error}", file=sys.stderr)
            sys.exit(1)

    if not os.path.isdir(work_dir):
        print(f"Error: directory does not exist: {work_dir}", file=sys.stderr)
        sys.exit(1)

    if window_exists(args.window):
        print(f"Error: window '{args.window}' already exists", file=sys.stderr)
        sys.exit(1)

    # Initialize worktree if requested (mise trust + winit)
    if args.init:
        print(f"Initializing worktree at {work_dir}...")
        subprocess.run(["mise", "trust"], cwd=work_dir, capture_output=True)
        winit_result = subprocess.run(["winit"], cwd=work_dir, capture_output=True, text=True)
        if winit_result.returncode != 0 and winit_result.stderr:
            print(f"  winit warning: {winit_result.stderr.strip()}", file=sys.stderr)
        else:
            print("  Worktree initialized (mise trust + winit).")

    # Build the agent command
    if args.agent == "pi":
        parts = [
            "pi",
            "--provider",
            PI_PROVIDER,
            "--model",
            PI_MODEL,
            "--name",
            args.window,
        ]
        # Load only the validated Mailbox V2 extension. Global discovery may point
        # to a stale staging checkout and duplicate control-plane client behavior.
        parts.extend(["--no-extensions", "--extension", pi_mailbox_extension])
        # Pi has no permission mode. --approve makes the trusted project resources
        # available without a startup prompt, matching the existing launcher default.
        if args.skip_permissions:
            parts.append("--approve")
        # Pi's --resume opens an interactive picker; --session accepts the supplied
        # session path or ID and is the supported non-interactive resume mapping.
        if args.resume:
            parts.extend(["--session", args.resume])
        else:
            parts.extend(["--session-id", pi_session_id(work_dir, args.window)])
        agent_cmd = _quote_command(parts)
    elif args.agent == "codex":
        _codex_pretrust_workspace(work_dir)
        parts = ["codex", "--no-alt-screen"]
        if args.reasoning_effort:
            parts.extend(["-c", f'model_reasoning_effort="{args.reasoning_effort}"'])
        if args.skip_permissions:
            parts.extend(["-a", "never", "-s", "danger-full-access"])
        if args.resume:
            parts.extend(["resume", args.resume])
        agent_cmd = " ".join(parts)
    elif args.agent == "cursor":
        # Cursor agent: interactive TUI mode.
        token = get_cursor_auth_token()
        if not token:
            print("Error: could not read Cursor auth token from state.vscdb", file=sys.stderr)
            sys.exit(1)

        # Pre-trust the workspace to suppress the "do you trust this workspace?" dialog.
        _cursor_pretrust_workspace(work_dir)

        cursor_bin = os.path.expanduser("~/.local/bin/cursor-agent")
        parts = [f"export CURSOR_AUTH_TOKEN={token}"]

        # TUI mode: interactive session, stays alive for Mailbox V2 messages.
        agent_parts = [cursor_bin, "--yolo", "--model", "auto"]
        if args.resume:
            agent_parts.extend(["--resume", args.resume])

        parts.append(" ".join(agent_parts))
        agent_cmd = " && ".join(parts)
    else:
        # Default: Claude Code
        agent_cmd = "claude"
        if args.skip_permissions:
            agent_cmd += " --dangerously-skip-permissions"
        if args.resume:
            agent_cmd += f" --resume {args.resume}"

    # Pre-create the Claude project directory for this worktree with
    # skipDangerousModePermissionPrompt=true so the workspace trust dialog
    # is suppressed before the agent even starts.
    encoded = work_dir.lstrip("/").replace("/", "-")
    project_dir = os.path.join(os.path.expanduser("~/.claude/projects"), f"-{encoded}")
    os.makedirs(project_dir, exist_ok=True)
    project_settings = os.path.join(project_dir, "settings.json")
    if not os.path.exists(project_settings):
        import json as _json
        with open(project_settings, "w") as f:
            _json.dump({"skipDangerousModePermissionPrompt": True}, f)

    created = ensure_session()
    if created:
        print("Created mecha tmux session.")

    new_window(args.window)

    # Change to the worktree and start the agent. Its tmux window name is its identity.
    # For cursor: agent_cmd already contains its authentication export.
    if args.agent == "cursor":
        full_cmd = f"cd {work_dir} && {agent_cmd}"
    else:
        router_export = (
            f"export {PI_ROUTER_URL_ENV}={shlex.quote(pi_router_url)} && "
            if args.agent == "pi" else ""
        )
        full_cmd = (
            f"cd {work_dir} && unset CLAUDECODE && "
            f"{router_export}{agent_cmd}"
        )
    send_text(args.window, full_cmd)
    time.sleep(0.5)
    send_enter(args.window)

    # Cursor TUI needs ~10s to fully initialize before accepting input
    if args.agent == "cursor":
        time.sleep(10)

    action = "resumed" if args.resume else "launched"
    print(f"Worker ({args.agent}) {action} in window '{args.window}' at {work_dir}")
    print()
    print("--- Next steps ---")
    print("1. Verify the Pi worker registered with Mailbox V2, then inspect its status if needed:")
    print(f"   python3 worker_capture.py --window {args.window}")
    print("2. Deliver instructions through Mailbox V2:")
    print("   python3 mailbox_send.py --from supervisor \\")
    print(f"     --to {args.window} --body 'Read ~/.agents/skills/worker/SKILL.md first, then read TASK.md for your instructions.'")
    print("3. Record the task in supervisor state:")
    if args.agent == "pi":
        print(
            f"   python3 state_add.py --alias <alias> --type worker --agent pi "
            f"--session {pi_session_id(work_dir, args.window)} ..."
        )
    else:
        print(f"   python3 state_add.py --alias <alias> --type worker --agent {args.agent} ...")
    print()
    print("If the recipient is not registered or is not running, restore it through")
    print("the managed Pi lifecycle/worker_launch.py. Do not type into its terminal or tmux pane.")
    print("Before shutting down, verify the work is correct (background subagent).")


if __name__ == "__main__":
    main()
