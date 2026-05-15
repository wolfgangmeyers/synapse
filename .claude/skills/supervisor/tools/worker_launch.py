#!/usr/bin/env python3
"""Launch a new worker in a tmux window.

Usage:
  python worker_launch.py --window WINDOW --dir DIR [--resume SESSION_ID] [--skip-permissions] [--agent claude|codex|cursor]

Creates a tmux window, starts the agent in the specified directory,
and waits for it to initialize. Does NOT send instructions — use
worker_send.py for that after verifying the agent started.
"""

import argparse
import os
import sqlite3
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import ensure_session, new_window, send_text, send_enter, window_exists


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


def main():
    parser = argparse.ArgumentParser(
        description="Launch Claude Code in a new tmux window."
    )
    parser.add_argument("--window", required=True, help="Tmux window name")
    parser.add_argument("--dir", required=True, help="Working directory for Claude")
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
        default="claude",
        choices=["claude", "codex", "cursor"],
        help="Agent to launch: 'claude' (default), 'codex', or 'cursor'",
    )

    parser.add_argument(
        "--message",
        default=None,
        help="(Unused for cursor TUI — send instructions with worker_send.py --bracketed instead.) "
             "Kept for backwards compatibility.",
    )

    args = parser.parse_args()

    work_dir = os.path.expanduser(args.dir)

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
        parts = ["codex"]
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

    # Reminders
    print()
    print("--- Reminders ---")
    print(f"1. Wait 5 seconds, then verify the worker window is ready:")
    print(f"   python3 worker_capture.py --window {args.window}")
    print(f"2. Send instructions to the worker:")
    print(f"   python3 worker_send.py --window {args.window} --text 'Your task: ...'")
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
        print(f"  submitted. Use --task-file next time; for now send a short follow-up:")
        print(f"   python3 worker_send.py --window {args.window} --text 'Read TASK.md for your instructions.'")
        print(f"  Before shutting down, verify the work is correct (background subagent).")


if __name__ == "__main__":
    main()
