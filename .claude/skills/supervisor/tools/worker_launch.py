#!/usr/bin/env python3
"""Launch a new worker in a tmux window.

Usage:
  python worker_launch.py --window WINDOW --dir DIR [--resume SESSION_ID] [--skip-permissions] [--agent codex|claude|cursor]

Creates a tmux window, starts the agent in the specified directory,
and waits for it to initialize. Does NOT send instructions — use
worker_send.py for that after verifying the agent started.
"""

import argparse
import json
import os
import pathlib
import re
import shlex
import sqlite3
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import ensure_session, new_window, send_text, send_enter, window_exists


POST_LAUNCH_REMINDER_DELAY = "15s"
POST_LAUNCH_REMINDER_TIMEOUT_SECONDS = 5
DEFAULT_SUPERVISOR_WINDOW = "supervisor"


def _post_launch_reminder_tool():
    return (
        pathlib.Path(__file__).parent.parent.parent
        / "reminders"
        / "tools"
        / "set_reminder.py"
    )


def _post_launch_reminder_message(worker_window, action):
    return (
        f"Capture/check worker window '{worker_window}' and verify the worker "
        f"accepted its task instructions after it {action}. Look for active work "
        f"(tool calls, file reads, command output), not merely that the agent "
        f"process launched. If the task text is sitting at the prompt, submit it "
        f"with worker_send.py."
    )


def _quote_command(argv):
    return " ".join(shlex.quote(part) for part in argv)


def _warn_post_launch_reminder_failed(reason, manual_argv):
    print(
        f"Warning: could not set post-launch verification reminder: {reason}",
        file=sys.stderr,
    )
    print(f"  Set it manually: {_quote_command(manual_argv)}", file=sys.stderr)


def _schedule_post_launch_reminder(
    worker_window,
    action,
    reminder_tool=None,
    supervisor_window=None,
):
    tool = pathlib.Path(reminder_tool) if reminder_tool else _post_launch_reminder_tool()
    supervisor_window = (
        supervisor_window
        or os.environ.get("MECHA_SUPERVISOR_WINDOW")
        or DEFAULT_SUPERVISOR_WINDOW
    )
    message = _post_launch_reminder_message(worker_window, action)
    reminder_argv = [
        sys.executable,
        str(tool),
        "--window",
        supervisor_window,
        "--message",
        message,
        "--in",
        POST_LAUNCH_REMINDER_DELAY,
    ]

    if not tool.exists():
        _warn_post_launch_reminder_failed(
            f"reminder tool not found: {tool}",
            reminder_argv,
        )
        return None

    try:
        result = subprocess.run(
            reminder_argv,
            capture_output=True,
            text=True,
            timeout=POST_LAUNCH_REMINDER_TIMEOUT_SECONDS,
        )
    except subprocess.TimeoutExpired:
        _warn_post_launch_reminder_failed(
            f"{tool} timed out after {POST_LAUNCH_REMINDER_TIMEOUT_SECONDS}s",
            reminder_argv,
        )
        return None
    except OSError as e:
        _warn_post_launch_reminder_failed(f"could not run {tool}: {e}", reminder_argv)
        return None
    except subprocess.SubprocessError as e:
        _warn_post_launch_reminder_failed(f"{tool} failed: {e}", reminder_argv)
        return None

    if result.returncode != 0:
        details = []
        if result.stderr.strip():
            details.append(f"stderr: {result.stderr.strip()}")
        if result.stdout.strip():
            details.append(f"stdout: {result.stdout.strip()}")
        detail_text = "; ".join(details) if details else "no output"
        _warn_post_launch_reminder_failed(
            f"{tool} exited {result.returncode} ({detail_text})",
            reminder_argv,
        )
        return None

    reminder_id = None
    for line in result.stdout.splitlines():
        if line.startswith("Reminder set:"):
            reminder_id = line.split(":", 1)[1].strip()
            break

    if reminder_id:
        print(
            f"Post-launch verification reminder set: {reminder_id} "
            f"(fires in {POST_LAUNCH_REMINDER_DELAY})."
        )
    else:
        print(
            "Post-launch verification reminder set "
            f"(fires in {POST_LAUNCH_REMINDER_DELAY})."
        )
    return reminder_id


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
        description="Launch an agent in a new tmux window."
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
        default="codex",
        choices=["claude", "codex", "cursor"],
        help="Agent to launch: 'codex' (default), 'claude', or 'cursor'",
    )

    parser.add_argument(
        "--message",
        default=None,
        help="(Unused for cursor TUI — send instructions with worker_send.py --bracketed instead.) "
             "Kept for backwards compatibility.",
    )

    args = parser.parse_args()

    work_dir = os.path.abspath(os.path.expanduser(args.dir))

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
    if args.agent == "codex":
        _codex_pretrust_workspace(work_dir)
        parts = ["codex", "--no-alt-screen"]
        if args.skip_permissions:
            parts.extend(["-a", "never", "-s", "danger-full-access"])
        if args.resume:
            parts.extend(["resume", args.resume])
        agent_cmd = " ".join(parts)
    elif args.agent == "cursor":
        # Cursor agent: interactive TUI mode.
        # Text is sent via bracketed paste (worker_send.py --bracketed) which bypasses
        # cursor-agent's Z.current anti-paste guard in its ink key handler.
        # MECHA_WORKER_WINDOW is exported so cursor's shell commands can run mailbox_send.py.
        token = get_cursor_auth_token()
        if not token:
            print("Error: could not read Cursor auth token from state.vscdb", file=sys.stderr)
            sys.exit(1)

        # Pre-trust the workspace to suppress the "do you trust this workspace?" dialog.
        _cursor_pretrust_workspace(work_dir)

        cursor_bin = os.path.expanduser("~/.local/bin/cursor-agent")
        parts = [f"export CURSOR_AUTH_TOKEN={token}"]
        parts.append(f"export MECHA_WORKER_WINDOW={args.window}")

        # TUI mode: interactive session, stays alive for follow-up instructions.
        # Send tasks with: worker_send.py --window WINDOW --bracketed --text "..."
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

    # cd to dir, set worker identity, start agent
    # For cursor: agent_cmd already contains env var exports; skip CLAUDECODE/MECHA_WORKER_WINDOW wrappers
    if args.agent == "cursor":
        full_cmd = f"cd {work_dir} && {agent_cmd}"
    else:
        full_cmd = f"cd {work_dir} && unset CLAUDECODE && export MECHA_WORKER_WINDOW={args.window} && {agent_cmd}"
    send_text(args.window, full_cmd)
    time.sleep(0.5)
    send_enter(args.window)

    # Cursor TUI needs ~10s to fully initialize before accepting input
    if args.agent == "cursor":
        time.sleep(10)

    action = "resumed" if args.resume else "launched"
    print(f"Worker ({args.agent}) {action} in window '{args.window}' at {work_dir}")
    _schedule_post_launch_reminder(args.window, action)

    # Reminders
    print()
    print("--- Reminders ---")
    print(f"1. Wait 5 seconds, then verify the worker window is ready:")
    print(f"   python3 worker_capture.py --window {args.window}")
    print(f"2. Send instructions to the worker:")
    print(f"   python3 worker_send.py --window {args.window} --delay 3 \\")
    print(f"     --text 'Read ~/.agents/skills/worker/SKILL.md first, then read TASK.md for your instructions.'")
    print(f"3. Record the task in supervisor state:")
    print(f"   python3 state_add.py --alias <alias> --type worker --agent {args.agent} ...")
    if not args.resume:
        print(f"4. If the worker didn't start, kill and retry:")
        print(f"   python3 worker_kill.py --window {args.window}")
    if args.agent == "cursor":
        print()
        print(f"Note (cursor TUI mode):")
        print(f"  TUI is ready. Send tasks with --bracketed flag:")
        print(f"   python3 worker_send.py --window {args.window} --bracketed --text 'Your task: ...'")
        print(f"  Max 500 chars per message. Longer tasks: write to TASK.md, then send a pointer.")
        print(f"  Monitor: python3 worker_capture.py --window {args.window} --tail 30")
        print(f"  The cursor worker stays alive and accepts follow-up messages.")
    else:
        print()
        print(f"Note: After sending the task, capture the window to confirm the worker")
        print(f"  actually started on it — not just that it launched:")
        print(f"   python3 worker_capture.py --window {args.window} --tail 30")
        print(f"  Look for evidence of active work: tool calls, file reads, bash output.")
        print(f"  If you only see the task text sitting at the prompt, the input was not")
        print(f"  submitted. Use TASK.md next time; for now send a short follow-up:")
        print(f"   python3 worker_send.py --window {args.window} --delay 3 \\")
        print(f"     --text 'Read ~/.agents/skills/worker/SKILL.md first, then read TASK.md for your instructions.'")
        print(f"  Before shutting down, verify the work is correct (background subagent).")


if __name__ == "__main__":
    main()
