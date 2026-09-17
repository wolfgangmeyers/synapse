#!/usr/bin/env python3
"""Recover the supervisor from a persisted orphan tool result."""

from __future__ import annotations

import fcntl
import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
from datetime import datetime, timezone


ORPHAN_TOOL_RESULT_ERROR = re.compile(
    r"No tool call found for function call output with call_id (call_[A-Za-z0-9_-]+)"
)
SESSION_DIR = Path("~/.pi/agent/sessions/--Users-wolfgang-code-mecha-developer--").expanduser()
RECOVERY_SENTINEL = Path("~/.mecha-developer/supervisor-tool-call-recovery.json").expanduser()
RECOVERY_LOG = Path("~/.mecha-developer/supervisor-tool-call-recovery.log").expanduser()
RECOVERY_DEBOUNCE_SECONDS = 10 * 60
SUPERVISOR_WINDOW = "supervisor"
TOOLS = Path(__file__).resolve().parent
PI_PROVIDER = "openai-codex"
PI_MODEL = "gpt-5.6-sol"


def actionable(level: str, owner: str, issue: str, action: str, resolved_when: str) -> str:
    return (
        f"{level} owner={owner}; issue={issue}; action={action}; "
        f"resolved-when={resolved_when}"
    )


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def _base_call_id(tool_call_id: str | None) -> str | None:
    if not isinstance(tool_call_id, str):
        return None
    return tool_call_id.split("|", 1)[0]


def _session_timestamp(session_path: Path) -> str | None:
    try:
        first = session_path.read_text(errors="replace").splitlines()[0]
        record = json.loads(first)
    except (OSError, IndexError, json.JSONDecodeError):
        return None
    timestamp = record.get("timestamp")
    return timestamp if isinstance(timestamp, str) else None


def detect_orphan_tool_result_crash(
    session_path: str | Path, min_repeated_errors: int = 2
) -> dict | None:
    """Return strict repeated orphan-tool-result evidence."""
    source = Path(session_path).expanduser()
    try:
        lines = source.read_text(errors="replace").splitlines()
    except OSError as error:
        return {"alert": f"could not read supervisor session {source}: {error}"}

    tool_calls: set[str] = set()
    tool_results: dict[str, list[dict]] = {}
    error_messages: dict[str, dict[str, list[int]]] = {}
    compaction_lines: list[int] = []
    latest_error_line = 0
    latest_error_timestamp = None

    for line_no, line in enumerate(lines, 1):
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if record.get("type") == "compaction":
            compaction_lines.append(line_no)
        message = record.get("message") if isinstance(record.get("message"), dict) else {}
        for content in message.get("content") or []:
            if isinstance(content, dict) and content.get("type") == "toolCall":
                call_id = _base_call_id(content.get("id"))
                if call_id:
                    tool_calls.add(call_id)
        if message.get("role") == "toolResult":
            call_id = _base_call_id(message.get("toolCallId"))
            if call_id:
                tool_results.setdefault(call_id, []).append({
                    "line": line_no,
                    "tool_call_id": message.get("toolCallId"),
                    "tool_name": message.get("toolName"),
                    "parent_id": record.get("parentId"),
                })
        error_message = message.get("errorMessage")
        if isinstance(error_message, str):
            match = ORPHAN_TOOL_RESULT_ERROR.search(error_message)
            if match:
                call_id = match.group(1)
                error_messages.setdefault(call_id, {}).setdefault(error_message, []).append(line_no)
                latest_error_line = line_no
                latest_error_timestamp = record.get("timestamp")

    for call_id, messages_for_call in error_messages.items():
        repeated = [lines for lines in messages_for_call.values() if len(lines) >= min_repeated_errors]
        if not repeated:
            continue
        lines_for_call = repeated[0]
        orphan_results = tool_results.get(call_id, []) if call_id not in tool_calls else []
        if not orphan_results:
            return {
                "alert": (
                    f"repeated missing-tool-call error for {call_id} in {source}, "
                    "but orphan proof failed; not recovering automatically"
                ),
                "call_id": call_id,
                "session_path": str(source),
                "error_lines": lines_for_call,
            }
        orphan_line = orphan_results[0]["line"]
        prior_compactions = [line for line in compaction_lines if line <= orphan_line]
        return {
            "session_path": str(source),
            "call_id": call_id,
            "error_lines": lines_for_call,
            "orphan_tool_result_line": orphan_line,
            "orphan_tool_result": orphan_results[0],
            "compaction_line": prior_compactions[-1] if prior_compactions else None,
            "latest_error_line": latest_error_line,
            "latest_error_timestamp": latest_error_timestamp,
        }
    return None


def session_looks_like_supervisor(session_path: str | Path) -> bool:
    try:
        sample = "\n".join(Path(session_path).read_text(errors="replace").splitlines()[:30])
    except OSError:
        return False
    return (
        "Activate Supervisor Mode" in sample
        or "Load supervisor mode" in sample
        or "operate as the supervisor" in sample
        or "Load the supervisor skill" in sample
    )


def recent_supervisor_sessions(session_dir: str | Path = SESSION_DIR, limit: int = 10) -> list[Path]:
    root = Path(session_dir).expanduser()
    try:
        sessions = [path for path in root.glob("*.jsonl") if path.is_file()]
    except OSError:
        return []
    newest = sorted(sessions, key=lambda path: path.stat().st_mtime, reverse=True)
    return [path for path in newest if session_looks_like_supervisor(path)][:limit]


def newer_healthy_supervisor_session_exists(
    corrupted_session: str | Path, session_dir: str | Path = SESSION_DIR
) -> bool:
    corrupted = Path(corrupted_session).expanduser()
    corrupted_timestamp = _session_timestamp(corrupted)
    root = Path(session_dir).expanduser()
    try:
        sessions = [path for path in root.glob("*.jsonl") if path.is_file() and path != corrupted]
    except OSError:
        return False
    for candidate in sessions:
        candidate_timestamp = _session_timestamp(candidate)
        if corrupted_timestamp and candidate_timestamp:
            is_newer = candidate_timestamp > corrupted_timestamp
        else:
            try:
                is_newer = candidate.stat().st_mtime > corrupted.stat().st_mtime
            except OSError:
                continue
        if is_newer and not detect_orphan_tool_result_crash(candidate):
            return True
    return False


def recovery_handoff_prompt(evidence: dict) -> str:
    session_path = evidence["session_path"]
    call_id = evidence["call_id"]
    return (
        "Load the supervisor skill and mailbox skill from ~/.agents/skills, then operate as the supervisor.\n\n"
        f"Do not resume corrupted supervisor session `{session_path}`. It failed with orphan toolResult `{call_id}`: "
        "Pi persisted a tool result without the matching assistant tool call, so resuming that JSONL will keep sending an invalid Responses function_call_output.\n\n"
        "Recover from the supervisor state file, current mailbox/reminder/router state, running worker windows, and the compaction handoff/tail in the corrupted session. "
        "First preserve and report the corrupted session path and call id, reconcile active workers, then continue the active supervisor queue. "
        "Do not kill workers, router, reminders, or non-supervisor windows."
    )


def recovery_tmux_command(
    evidence: dict, work_dir: str = "/Users/wolfgang/code/mecha-developer"
) -> str:
    prompt = recovery_handoff_prompt(evidence)
    router_url = os.environ.get("MECHA_ROUTER_URL", "http://127.0.0.1:8765").rstrip("/")
    model_key = f"{PI_PROVIDER}/{PI_MODEL}"
    pi_command = " ".join([
        "pi",
        "--provider", PI_PROVIDER,
        "--model", PI_MODEL,
        "--name", SUPERVISOR_WINDOW,
        "--approve", shlex.quote(prompt),
    ])
    return (
        f"cd {shlex.quote(work_dir)} && unset CLAUDECODE "
        f"&& export MECHA_ROUTER_URL={shlex.quote(router_url)} "
        f"&& export MECHA_PI_MODEL_KEY={shlex.quote(model_key)} "
        f"&& {pi_command}"
    )


def _append_recovery_log(event: dict) -> None:
    RECOVERY_LOG.parent.mkdir(parents=True, exist_ok=True)
    with RECOVERY_LOG.open("a", encoding="utf-8") as log_file:
        log_file.write(json.dumps(event, sort_keys=True) + "\n")


def recovery_already_attempted(evidence: dict, now: float | None = None) -> bool:
    now = time.time() if now is None else now
    try:
        previous = json.loads(RECOVERY_SENTINEL.read_text())
    except (OSError, UnicodeDecodeError, json.JSONDecodeError):
        return False
    if not isinstance(previous, dict):
        return False
    same_failure_succeeded = (
        previous.get("status") == "succeeded"
        and previous.get("session_path") == evidence.get("session_path")
        and previous.get("call_id") == evidence.get("call_id")
    )
    try:
        attempted_at = float(previous.get("attempted_at_epoch", 0))
    except (TypeError, ValueError):
        attempted_at = 0
    return bool(same_failure_succeeded or now - attempted_at < RECOVERY_DEBOUNCE_SECONDS)


def recover_supervisor_from_orphan_crash(evidence: dict, dry_run: bool = False) -> bool:
    if newer_healthy_supervisor_session_exists(evidence["session_path"]):
        return False
    if recovery_already_attempted(evidence):
        print(actionable(
            "WARN", "supervisor",
            f"supervisor recovery already attempted for {evidence['call_id']}",
            "skip restart loop and inspect recovery log",
            "a human confirms whether another restart is safe",
        ), flush=True)
        return False

    if dry_run:
        print(recovery_tmux_command(evidence), flush=True)
        return True

    event = {
        "event": "supervisor_orphan_tool_result_recovery",
        "attempted_at": utc_now(),
        "attempted_at_epoch": time.time(),
        "session_path": evidence["session_path"],
        "call_id": evidence["call_id"],
        "error_lines": evidence.get("error_lines"),
        "orphan_tool_result_line": evidence.get("orphan_tool_result_line"),
        "compaction_line": evidence.get("compaction_line"),
        "dry_run": dry_run,
        "status": "dry_run" if dry_run else "started",
    }
    RECOVERY_SENTINEL.parent.mkdir(parents=True, exist_ok=True)
    lock_path = RECOVERY_SENTINEL.with_suffix(RECOVERY_SENTINEL.suffix + ".lock")
    with lock_path.open("a+") as lock:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
        try:
            if recovery_already_attempted(evidence):
                return False
            command = recovery_tmux_command(evidence)
            try:
                subprocess.run(
                    ["tmux", "respawn-window", "-k", "-t", f"mecha:{SUPERVISOR_WINDOW}", command],
                    check=True,
                    timeout=30,
                )
            except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
                event["status"] = "failed"
                event["error"] = str(error)
                _append_recovery_log(event)
                RECOVERY_SENTINEL.write_text(json.dumps(event, indent=2) + "\n")
                print(actionable(
                    "FAIL", "local operator",
                    f"supervisor recovery command failed: {error}",
                    "repair tmux/supervisor window state and rerun recovery",
                    "fresh supervisor starts from the recovery handoff",
                ), file=sys.stderr, flush=True)
                return False
            event["status"] = "succeeded"
            _append_recovery_log(event)
            RECOVERY_SENTINEL.write_text(json.dumps(event, indent=2) + "\n")
            return True
        finally:
            fcntl.flock(lock.fileno(), fcntl.LOCK_UN)


def send_recovery_alert(note: str, dry_run: bool = False) -> bool:
    if dry_run:
        return True
    mailbox_send = TOOLS / "mailbox_send.py"
    try:
        result = subprocess.run(
            [
                sys.executable,
                str(mailbox_send),
                "--to", "supervisor",
                "--from", "supervisor-tool-call-recovery",
                "--subject", "Supervisor tool-call recovery",
                "--body", note,
            ],
            capture_output=True,
            text=True,
            timeout=30,
        )
        return result.returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


def maybe_recover_supervisor_tool_call_crash(dry_run: bool = False) -> bool:
    for session_path in recent_supervisor_sessions():
        evidence = detect_orphan_tool_result_crash(session_path)
        if not evidence:
            continue
        if evidence.get("alert"):
            note = actionable(
                "WARN", "supervisor", evidence["alert"],
                "inspect the session tail before any recovery",
                "orphan proof exists before a supervisor restart",
            )
            print(note, flush=True)
            send_recovery_alert(note, dry_run=dry_run)
            return True
        if newer_healthy_supervisor_session_exists(evidence["session_path"]):
            continue
        note = actionable(
            "FAIL", "supervisor",
            f"orphan toolResult crash in {evidence['session_path']} for {evidence['call_id']}",
            "restart only mecha:supervisor fresh/no-resume with recovery handoff",
            "fresh supervisor starts and preserves corrupted session path",
        )
        print(note, flush=True)
        recover_supervisor_from_orphan_crash(evidence, dry_run=dry_run)
        return True
    return False


def run_recovery(dry_run: bool = False) -> int:
    return 1 if maybe_recover_supervisor_tool_call_crash(dry_run=dry_run) else 0


def main(argv: list[str] | None = None) -> int:
    import argparse

    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dry-run", action="store_true")
    args = parser.parse_args(argv)
    return run_recovery(dry_run=args.dry_run)


if __name__ == "__main__":
    raise SystemExit(main())
