#!/usr/bin/env python3
"""Launch or clean up one managed worker resolved from supervisor state.

Only a supervisor task alias is accepted. The working directory, runtime, session,
and cleanup window all come from the matching state record.

Usage:
  python worker_lifecycle.py launch --alias ALIAS
  python worker_lifecycle.py cleanup --alias ALIAS
"""

import argparse
import os
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import _state as state_store
from _sidecar import ProcessTableUnavailable, find_sidecar_pids
from _tmux import AGENT_ID, window_exists

TOOLS_DIR = os.path.dirname(os.path.realpath(__file__))
WORKER_LAUNCH = os.path.join(TOOLS_DIR, "worker_launch.py")
WORKER_KILL = os.path.join(TOOLS_DIR, "worker_kill.py")
SUPPORTED_RUNTIMES = {"pi", "claude", "codex", "cursor"}
CLEANED_WINDOW = "(none — worker exited)"
MAX_ERROR_DETAIL = 4_000
SCRIPT_TIMEOUT_SECONDS = 25


class LifecycleError(RuntimeError):
    """A lifecycle request that cannot be completed safely."""


def resolve_worker(state, alias):
    matches = [task for task in state.get("tasks", []) if task.get("alias") == alias]
    if not matches:
        raise LifecycleError(f"No supervisor task has alias '{alias}'.")
    if len(matches) != 1:
        raise LifecycleError(f"Supervisor task alias '{alias}' is ambiguous.")
    task = matches[0]
    if task.get("state") != "active" or task.get("type") != "worker":
        raise LifecycleError(f"Supervisor task alias '{alias}' is not an active managed worker.")
    return task


def valid_agent_id(value):
    return (
        isinstance(value, str)
        and len(value) <= 120
        and AGENT_ID.fullmatch(value) is not None
    )


def script_failure(result):
    detail = "\n".join(
        value.strip()
        for value in (result.stderr, result.stdout)
        if isinstance(value, str) and value.strip()
    )
    if not detail:
        detail = f"lifecycle script exited with status {result.returncode}"
    return detail[:MAX_ERROR_DETAIL]


def run_script(script, arguments):
    try:
        result = subprocess.run(
            [sys.executable, script, *arguments],
            capture_output=True,
            text=True,
            timeout=SCRIPT_TIMEOUT_SECONDS,
            check=False,
        )
    except subprocess.TimeoutExpired as error:
        raise LifecycleError("Managed worker lifecycle script timed out.") from error
    except OSError as error:
        raise LifecycleError("Managed worker lifecycle script could not start.") from error
    if result.returncode != 0:
        raise LifecycleError(script_failure(result))
    return result


def active_identity(window):
    try:
        return window_exists(window) or bool(find_sidecar_pids(window))
    except (OSError, ProcessTableUnavailable) as error:
        raise LifecycleError(f"Could not verify whether worker identity '{window}' is active.") from error


def validate_session(value):
    if value is None:
        return None
    if not isinstance(value, str) or not value.strip() or len(value) > 1_024 or "\0" in value:
        raise LifecycleError("The recorded worker session is invalid.")
    return value.strip()


def launch(alias):
    with state_store.locked_state() as state:
        task = resolve_worker(state, alias)
        if not valid_agent_id(alias):
            raise LifecycleError("The worker alias cannot be used as a managed worker identity.")

        recorded_window = task.get("window")
        identities = [alias]
        if valid_agent_id(recorded_window) and recorded_window != alias:
            identities.append(recorded_window)
        if any(active_identity(identity) for identity in identities):
            raise LifecycleError(f"Managed worker alias '{alias}' is already active.")

        worktree = task.get("worktree")
        if not isinstance(worktree, str) or not worktree.strip():
            raise LifecycleError("The worker state has no recorded worktree.")
        worktree = os.path.abspath(os.path.expanduser(worktree.strip()))
        if not os.path.isdir(worktree):
            raise LifecycleError("The recorded worker worktree does not exist.")

        runtime = task.get("agent")
        if runtime not in SUPPORTED_RUNTIMES:
            raise LifecycleError("The worker state has no valid recorded runtime.")
        session = validate_session(task.get("session"))

        arguments = ["--window", alias, "--dir", worktree, "--agent", runtime]
        if session is not None:
            arguments.extend(["--resume", session])
        if runtime == "pi":
            arguments.extend(["--notify-on-settled", "supervisor"])
        run_script(WORKER_LAUNCH, arguments)

        task["window"] = alias
        task["last_checked"] = state_store.now_timestamp()

    print(f"Launched managed worker '{alias}'.")


def cleanup(alias):
    with state_store.locked_state() as state:
        task = resolve_worker(state, alias)
        window = task.get("window")
        if not valid_agent_id(window):
            raise LifecycleError("The worker state has no valid recorded window to clean up.")

        run_script(WORKER_KILL, ["--window", window])

        task["window"] = CLEANED_WINDOW
        task["last_checked"] = state_store.now_timestamp()

    print(f"Cleaned up managed worker '{alias}' window '{window}'.")


def main():
    parser = argparse.ArgumentParser(description="Run a state-resolved managed worker lifecycle action.")
    parser.add_argument("action", choices=["launch", "cleanup"])
    parser.add_argument("--alias", required=True, help="Exact supervisor task alias")
    args = parser.parse_args()

    if not valid_agent_id(args.alias):
        parser.error("--alias must be a valid managed worker identity")

    try:
        if args.action == "launch":
            launch(args.alias)
        else:
            cleanup(args.alias)
    except LifecycleError as error:
        print(f"Error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
