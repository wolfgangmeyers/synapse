#!/usr/bin/env python3
"""Deterministic Priority Handoff Monitor.

This local five-minute monitor checks supervisor state and Mailbox router
reminders. It is separate from the LLM-based Watchdog and never creates a
router reminder. The one-time cutover command retires exact historical
Watchdog reminder IDs so terminal pre-cutover records do not mask new failures.
"""

from __future__ import annotations

import argparse
import fcntl
import json
import os
import re
import shlex
from datetime import datetime, timezone
from pathlib import Path
import subprocess
import sys
import tempfile
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

DEFAULT_ROUTER_URL = "http://127.0.0.1:8765"
LOG_MAX_BYTES = 1_048_576
SCHEDULE_MARKER = "# mecha-priority-handoff-monitor"
LEGACY_SCHEDULE_MARKER = "# mecha-priority-watchdog"
RETIREMENT_KEY = "priority_handoff_monitor_retirement"
RETIREMENT_ID_KEY = "retired_legacy_watchdog_reminder_ids"
MAX_RETIRED_LEGACY_REMINDERS = 100
# Acceptable cadence for the recurring supervisor general-sweep reminder. The
# defaults match the long-standing 5-10 minute expectation; MECHA_SWEEP_CADENCE_*
# env vars widen the window (for example, to a 30-minute sweep during the Z.AI
# backup period) without producing a false FAIL. The "exactly one" requirement
# is independent of these bounds.
DEFAULT_SWEEP_CADENCE_MIN_MS = 5 * 60 * 1_000
DEFAULT_SWEEP_CADENCE_MAX_MS = 10 * 60 * 1_000
TERMINAL_STATES = {"failed", "dead_letter"}
SUPERVISOR_WINDOW = "supervisor"
ORPHAN_TOOL_RESULT_ERROR = re.compile(r"No tool call found for function call output with call_id (call_[A-Za-z0-9_-]+)")
SESSION_DIR = Path("~/.pi/agent/sessions/--Users-wolfgang-code-mecha-developer--").expanduser()
RECOVERY_SENTINEL = Path("~/.mecha-developer/supervisor-tool-call-recovery.json").expanduser()
RECOVERY_LOG = Path("~/.mecha-developer/supervisor-tool-call-recovery.log").expanduser()
RECOVERY_DEBOUNCE_SECONDS = 10 * 60
TOOLS = Path(__file__).resolve().parent
SUPERVISOR_TOOLS = TOOLS.parents[1] / "supervisor" / "tools"


def actionable(level: str, owner: str, issue: str, action: str, resolved_when: str) -> str:
    return (
        f"{level} owner={owner}; issue={issue}; action={action}; "
        f"resolved-when={resolved_when}"
    )


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds").replace("+00:00", "Z")


def state_path(value: str | None = None) -> Path:
    return Path(value or os.environ.get("MECHA_STATE_FILE", "~/.mecha-developer/supervisor-state.json")).expanduser()


def router_base(value: str | None = None) -> str:
    return (value or os.environ.get("MECHA_ROUTER_URL", DEFAULT_ROUTER_URL)).rstrip("/")


def cap_log_file() -> None:
    path = Path(os.environ.get("MECHA_PRIORITY_HANDOFF_MONITOR_LOG", "~/.mecha-developer/priority-handoff-monitor.log")).expanduser()
    try:
        if path.stat().st_size <= LOG_MAX_BYTES:
            return
        retained = path.read_bytes()[-(LOG_MAX_BYTES // 2):]
        temporary = path.with_suffix(path.suffix + ".tmp")
        temporary.write_bytes(retained)
        temporary.replace(path)
    except FileNotFoundError:
        return
    except OSError as error:
        print(actionable("WARN", "local operator", f"cannot bound log {path}: {error}", f"make {path} writable", f"{path} is at most {LOG_MAX_BYTES} bytes"), file=sys.stderr, flush=True)


def load_priority_evidence(state_file: str | None = None, router_url: str | None = None):
    """Read the two authoritative monitor inputs without changing either."""
    source = state_path(state_file)
    try:
        state = json.loads(source.read_text())
        if not isinstance(state, dict) or not isinstance(state.get("priorities"), list) or not isinstance(state.get("tasks"), list):
            raise ValueError("missing priorities or tasks list")
        if not all(isinstance(item, dict) for item in state["priorities"] + state["tasks"]):
            raise ValueError("priorities and tasks must contain objects")
        if any(not isinstance(item.get("task_ids", []), list) for item in state["priorities"]):
            raise ValueError("priority task_ids must be lists")
    except (OSError, UnicodeDecodeError, ValueError, json.JSONDecodeError) as error:
        return None, None, f"state evidence unavailable: {error}"
    try:
        with urlopen(f"{router_base(router_url)}/v1/reminders", timeout=5) as response:
            payload = json.load(response)
        reminders = payload.get("reminders") if isinstance(payload, dict) else None
        if not isinstance(reminders, list) or not all(isinstance(item, dict) for item in reminders):
            raise ValueError("router response has no reminder object list")
    except (OSError, UnicodeDecodeError, ValueError, json.JSONDecodeError, URLError) as error:
        return state, None, f"router reminder evidence unavailable: {error}"
    return state, reminders, None


def retired_legacy_ids(state: dict) -> set[str]:
    control = state.get("priority_control")
    retirement = control.get(RETIREMENT_KEY) if isinstance(control, dict) else None
    values = retirement.get(RETIREMENT_ID_KEY) if isinstance(retirement, dict) else []
    return {value for value in values if isinstance(value, str)} if isinstance(values, list) else set()


def is_terminal(reminder: dict) -> bool:
    return reminder.get("state") in TERMINAL_STATES or reminder.get("linked_message_state") in TERMINAL_STATES


def sweep_cadence_ms(env_name: str, default: int) -> int:
    raw = os.environ.get(env_name)
    if raw is None or raw.strip() == "":
        return default
    try:
        value = int(raw)
    except ValueError:
        return default
    return value if value > 0 else default


def sweep_cadence_bounds() -> tuple[int, int]:
    """Return the acceptable general-sweep cadence window in milliseconds."""
    return (
        sweep_cadence_ms("MECHA_SWEEP_CADENCE_MIN_MS", DEFAULT_SWEEP_CADENCE_MIN_MS),
        sweep_cadence_ms("MECHA_SWEEP_CADENCE_MAX_MS", DEFAULT_SWEEP_CADENCE_MAX_MS),
    )


def describe_cadence(min_ms: int, max_ms: int) -> str:
    def minutes(ms: int) -> str:
        return f"{ms // 60_000}" if ms % 60_000 == 0 else f"{ms / 60_000:g}"
    low, high = minutes(min_ms), minutes(max_ms)
    return f"{low} minute" if low == high else f"{low}-{high} minute"


def priority_liveness_findings(state: dict, reminders: list[dict]) -> list[str]:
    """Return deterministic, actionable priority-handoff findings."""
    tasks = {task.get("id"): task for task in state["tasks"] if isinstance(task, dict)}
    reminders_by_id = {item.get("id"): item for item in reminders if isinstance(item, dict)}
    findings = []
    delivery_tasks = [
        task for task in state["tasks"]
        if task.get("state") in {"active", "blocked"}
        and task.get("type") in {"worker", "pipeline", "remote-worker"}
    ]
    active_priorities = [
        priority for priority in state["priorities"]
        if isinstance(priority, dict) and priority.get("status") in {"active", "blocked"}
    ]
    if delivery_tasks and not active_priorities:
        findings.append(actionable(
            "FAIL",
            "supervisor",
            "non-terminal delivery tasks exist but no active priority is recorded",
            "create or restore the ordered priority and link its task IDs using state_priority.py",
            "each current delivery workstream is represented by an active priority with linked tasks",
        ))

    cadence_min_ms, cadence_max_ms = sweep_cadence_bounds()
    general_sweeps = [
        reminder for reminder in reminders
        if reminder.get("target") == "supervisor"
        and reminder.get("state") == "scheduled"
        and isinstance(reminder.get("interval_ms"), int)
        and cadence_min_ms <= reminder["interval_ms"] <= cadence_max_ms
        and "sweep" in str(reminder.get("body", "")).lower()
    ]
    cadence = describe_cadence(cadence_min_ms, cadence_max_ms)
    if (delivery_tasks or active_priorities) and len(general_sweeps) != 1:
        findings.append(actionable(
            "FAIL",
            "supervisor",
            f"expected exactly one recurring {cadence} general sweep reminder, found {len(general_sweeps)}",
            "repair the recurring supervisor general-sweep reminder through Mailbox",
            f"exactly one scheduled recurring supervisor reminder at a {cadence} cadence names the general sweep",
        ))

    for priority in state["priorities"]:
        if not isinstance(priority, dict) or priority.get("status") not in {"active", "blocked"}:
            continue
        priority_id = priority.get("id")
        if not priority.get("task_ids"):
            findings.append(actionable(
                "FAIL",
                "supervisor",
                f"priority {priority_id} ({priority.get('name')}) has no linked tasks",
                "link its current delivery or CI/review successor task using state_priority.py",
                f"priority {priority_id} has at least one non-terminal linked task",
            ))
        linked = [tasks[task_id] for task_id in priority.get("task_ids", []) if task_id in tasks]
        if priority.get("task_ids") and len(linked) != len(priority["task_ids"]):
            findings.append(actionable("FAIL", "supervisor", f"priority {priority_id} references task IDs absent from state", "repair priority.task_ids to reference existing tasks", f"every task ID in priority {priority_id} resolves in supervisor state"))
        if linked and all(task.get("state") in {"completed", "abandoned"} for task in linked):
            findings.append(actionable("WARN", "supervisor", f"priority {priority_id} ({priority.get('name')}) has an exhausted queue", "record the priority completion or add its next authorized successor task", f"priority {priority_id} is completed/deprioritized or has a non-terminal successor"))
        for task in linked:
            if task.get("state") in {"completed", "abandoned"}:
                continue
            handoff = task.get("verification_handoff")
            if not isinstance(handoff, dict):
                findings.append(actionable(
                    "FAIL",
                    "supervisor",
                    f"priority {priority_id} task-{task.get('id')} has no verification handoff",
                    "create a recurring Mailbox reminder for its next asynchronous verification and record the reminder ID",
                    f"task-{task.get('id')} has a verification_handoff object linked to a live reminder",
                ))
                continue
            reminder = reminders_by_id.get(handoff.get("reminder_id"))
            if reminder is None or is_terminal(reminder):
                findings.append(actionable("FAIL", "supervisor", f"priority {priority_id} task-{task.get('id')} verification reminder is unavailable", "create or repair the task verification reminder through Mailbox", "the recorded verification reminder is scheduled or has a non-terminal linked delivery"))
            if not handoff.get("verification_started_at"):
                findings.append(actionable("WARN", "supervisor", f"priority {priority_id} task-{task.get('id')} has no verification start", "start verification and record verification_started_at", "verification_started_at contains the current verification start time"))
            elif handoff.get("remote_verified") is None:
                findings.append(actionable("WARN", "supervisor", f"priority {priority_id} task-{task.get('id')} lacks remote verification", "obtain and record the required remote verification evidence", "remote_verified records the remote proof or an explicit external block"))
            elif handoff.get("successor") is None:
                findings.append(actionable("WARN", "supervisor", f"priority {priority_id} task-{task.get('id')} lacks a successor handoff", "record the next owner or terminal disposition", "successor records the next handoff or the task is terminal"))

    retired = retired_legacy_ids(state)
    for reminder in reminders:
        if reminder.get("target") != "watchdog" or not is_terminal(reminder):
            continue
        reminder_id = reminder.get("id")
        if reminder_id in retired:
            continue
        findings.append(actionable("FAIL", "supervisor", f"current Watchdog reminder {reminder_id} is terminal ({reminder.get('state')} / {reminder.get('linked_message_state')})", "investigate the current Watchdog delivery; do not add it to the Priority Handoff Monitor retirement record", "the reminder delivers successfully or its current control is explicitly repaired"))

    control = state.get("priority_control")
    expected_id = control.get("watchdog_reminder_id") if isinstance(control, dict) else None
    if expected_id:
        findings.append(actionable("WARN", "supervisor", f"priority_control still references legacy Watchdog reminder {expected_id}", "run --reconcile-retired-controls to clear the legacy liveness reference; do not recreate a reminder", "priority_control has no watchdog_reminder_id and the local Priority Handoff Monitor cron entry is present exactly once"))
    return findings


def terminal_priority_observations(state: dict) -> list[str]:
    observations = []
    for priority in state["priorities"]:
        completion = priority.get("completion")
        evidence = completion.get("evidence") if isinstance(completion, dict) else None
        if priority.get("status") == "completed" and isinstance(evidence, str) and "terminal" in evidence.lower():
            observations.append(f"terminal priority {priority.get('id')} confirmed: {evidence}")
    return observations


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


def detect_orphan_tool_result_crash(session_path: str | Path, min_repeated_errors: int = 2) -> dict | None:
    """Return crash evidence only for the strict repeated orphan tool-result failure.

    The OpenAI Responses API rejects a session when Pi replays a
    `function_call_output` whose call id has no matching prior `function_call`.
    One provider error alone is not enough to recover; the monitor must prove the
    session JSONL contains the orphaned tool result and lacks the corresponding
    assistant tool call.
    """
    source = Path(session_path).expanduser()
    try:
        lines = source.read_text(errors="replace").splitlines()
    except OSError as error:
        return {"alert": f"could not read supervisor session {source}: {error}"}

    tool_calls: set[str] = set()
    tool_results: dict[str, list[dict]] = {}
    error_lines: dict[str, list[int]] = {}
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
                error_lines.setdefault(call_id, []).append(line_no)
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


def newer_healthy_supervisor_session_exists(corrupted_session: str | Path, session_dir: str | Path = SESSION_DIR) -> bool:
    corrupted = Path(corrupted_session).expanduser()
    corrupted_timestamp = _session_timestamp(corrupted)
    root = Path(session_dir).expanduser()
    try:
        sessions = [path for path in root.glob("*.jsonl") if path.is_file() and path != corrupted]
    except OSError:
        return False
    for candidate in sessions:
        candidate_timestamp = _session_timestamp(candidate)
        is_newer = False
        if corrupted_timestamp and candidate_timestamp:
            is_newer = candidate_timestamp > corrupted_timestamp
        else:
            try:
                is_newer = candidate.stat().st_mtime > corrupted.stat().st_mtime
            except OSError:
                continue
        # Fail closed: once any newer non-crashing Pi session exists in this
        # supervisor session directory, do not respawn the supervisor from an
        # older corrupt session just because the newer prompt shape is unfamiliar.
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


def recovery_tmux_command(evidence: dict, work_dir: str = "/Users/wolfgang/code/mecha-developer") -> str:
    prompt = recovery_handoff_prompt(evidence)
    extension = os.environ.get("MECHA_PI_MAILBOX_EXTENSION", os.path.expanduser("~/.mecha-developer/extensions/mecha-messaging.js"))
    router_url = router_base()
    pi_command = " ".join([
        "pi",
        "--provider", "anthropic",
        "--model", "claude-opus-5",
        "--name", SUPERVISOR_WINDOW,
        "--extension", shlex.quote(extension),
        "--approve", shlex.quote(prompt),
    ])
    shell_command = (
        f"cd {shlex.quote(work_dir)} && unset CLAUDECODE "
        f"&& export MECHA_ROUTER_URL={shlex.quote(router_url)} "
        "&& export MECHA_PI_MODEL_KEY=anthropic/claude-opus-5 "
        f"&& {pi_command}"
    )
    return shell_command


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
    recent = now - attempted_at < RECOVERY_DEBOUNCE_SECONDS
    return bool(same_failure_succeeded or recent)


def recover_supervisor_from_orphan_crash(evidence: dict, dry_run: bool = False) -> bool:
    if newer_healthy_supervisor_session_exists(evidence["session_path"]):
        return False
    if recovery_already_attempted(evidence):
        print(actionable("WARN", "supervisor", f"supervisor recovery already attempted for {evidence['call_id']}", "skip restart loop and inspect recovery log", "a human confirms whether another restart is safe"), flush=True)
        return False

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
            if dry_run:
                print(recovery_tmux_command(evidence), flush=True)
                return True
            command = recovery_tmux_command(evidence)
            try:
                subprocess.run(["tmux", "respawn-window", "-k", "-t", f"mecha:{SUPERVISOR_WINDOW}", command], check=True, timeout=30)
            except (OSError, subprocess.CalledProcessError, subprocess.TimeoutExpired) as error:
                event["status"] = "failed"
                event["error"] = str(error)
                _append_recovery_log(event)
                RECOVERY_SENTINEL.write_text(json.dumps(event, indent=2) + "\n")
                print(actionable("FAIL", "local operator", f"supervisor recovery command failed: {error}", "repair tmux/supervisor window state and rerun the monitor", "fresh supervisor starts from the recovery handoff"), file=sys.stderr, flush=True)
                return False
            event["status"] = "succeeded"
            _append_recovery_log(event)
            RECOVERY_SENTINEL.write_text(json.dumps(event, indent=2) + "\n")
            return True
        finally:
            fcntl.flock(lock.fileno(), fcntl.LOCK_UN)


def maybe_recover_supervisor_tool_call_crash(dry_run: bool = False) -> bool:
    for session_path in recent_supervisor_sessions():
        evidence = detect_orphan_tool_result_crash(session_path)
        if not evidence:
            continue
        if evidence.get("alert"):
            note = actionable("WARN", "supervisor", evidence["alert"], "inspect the session tail before any recovery", "orphan proof exists before a supervisor restart")
            print(note, flush=True)
            send_monitor_alert(note, dry_run=dry_run)
            return True
        if newer_healthy_supervisor_session_exists(evidence["session_path"]):
            continue
        print(actionable("FAIL", "supervisor", f"orphan toolResult crash in {evidence['session_path']} for {evidence['call_id']}", "restart only mecha:supervisor fresh/no-resume with recovery handoff", "fresh supervisor starts and preserves corrupted session path"), flush=True)
        recover_supervisor_from_orphan_crash(evidence, dry_run=dry_run)
        return True
    return False


def send_monitor_alert(note: str, dry_run: bool = False) -> bool:
    if dry_run:
        return True
    mailbox_send = SUPERVISOR_TOOLS / "mailbox_send.py"
    try:
        result = subprocess.run([sys.executable, str(mailbox_send), "--to", "supervisor", "--from", "priority-handoff-monitor", "--subject", "Priority handoff alert", "--body", f"[PRIORITY HANDOFF MONITOR] {note}"], capture_output=True, text=True, timeout=30)
        return result.returncode == 0
    except (OSError, subprocess.TimeoutExpired):
        return False


def run_monitor(dry_run: bool = False) -> int:
    if maybe_recover_supervisor_tool_call_crash(dry_run=dry_run):
        return 1
    state, reminders, error = load_priority_evidence()
    if error:
        print(actionable("FAIL", "local operator", error, "restore readable supervisor state and reachable Mailbox router", "the monitor can read valid state and GET /v1/reminders"), file=sys.stderr, flush=True)
        return 1
    findings = priority_liveness_findings(state, reminders)
    terminal = terminal_priority_observations(state)
    if not findings:
        suffix = f" {'; '.join(terminal)}" if terminal else ""
        print(f"PRIORITY HANDOFF MONITOR OK: state and router reminder evidence are current; no priority handoff breach.{suffix}", flush=True)
        return 0
    note = "\n".join(findings)
    print(f"PRIORITY HANDOFF MONITOR ALERT: {note}", flush=True)
    if terminal:
        print(f"PRIORITY HANDOFF MONITOR OBSERVED: {'; '.join(terminal)}", flush=True)
    if not send_monitor_alert(note, dry_run=dry_run):
        print(actionable("FAIL", "local operator", "could not deliver priority handoff alert", "restore Mailbox delivery for priority-handoff-monitor", "the alert is accepted by the supervisor Mailbox"), file=sys.stderr, flush=True)
        return 1
    return 1 if any(item.startswith("FAIL") for item in findings) else 0


def schedule_line() -> str:
    runtime = Path(__file__).resolve()
    log_path = Path("~/.mecha-developer/priority-handoff-monitor.log").expanduser()
    return f"*/5 * * * * /usr/bin/python3 {runtime} >> {log_path} 2>&1 {SCHEDULE_MARKER}"


def is_legacy_priority_schedule(line: str) -> bool:
    return SCHEDULE_MARKER in line or LEGACY_SCHEDULE_MARKER in line or "watchdog.py --priority-only" in line or "priority_handoff_monitor.py" in line


def reconcile_schedule(crontab: str) -> str:
    retained = [line for line in crontab.splitlines() if not is_legacy_priority_schedule(line)]
    retained.append(schedule_line())
    return "\n".join(retained) + "\n"


def install_schedule() -> None:
    current = subprocess.run(["crontab", "-l"], capture_output=True, text=True)
    if current.returncode and "no crontab" not in current.stderr.lower():
        raise RuntimeError(current.stderr.strip() or "could not read crontab")
    applied = subprocess.run(["crontab", "-"], input=reconcile_schedule(current.stdout if current.returncode == 0 else ""), capture_output=True, text=True)
    if applied.returncode:
        raise RuntimeError(applied.stderr.strip() or "could not update crontab")
    print(f"Installed one five-minute Priority Handoff Monitor schedule: {schedule_line()}")


def remove_schedule() -> None:
    current = subprocess.run(["crontab", "-l"], capture_output=True, text=True)
    if current.returncode and "no crontab" not in current.stderr.lower():
        raise RuntimeError(current.stderr.strip() or "could not read crontab")
    updated = "\n".join(line for line in current.stdout.splitlines() if SCHEDULE_MARKER not in line)
    applied = subprocess.run(["crontab", "-"], input=updated + ("\n" if updated else ""), capture_output=True, text=True)
    if applied.returncode:
        raise RuntimeError(applied.stderr.strip() or "could not update crontab")
    print("Removed the local Priority Handoff Monitor schedule.")


def legacy_control_plan(state: dict, reminders: list[dict]) -> dict:
    control = state.get("priority_control")
    if not isinstance(control, dict):
        control = {}
    existing = retired_legacy_ids(state)
    if RETIREMENT_KEY in control:
        unexpected = [item.get("id") for item in reminders if item.get("target") == "watchdog" and is_terminal(item) and item.get("id") not in existing]
        if unexpected:
            raise ValueError(f"cutover already complete; terminal Watchdog reminder(s) need investigation: {', '.join(map(str, unexpected))}")
        return {"retire_ids": [], "cancel_ids": [], "clear_reference": bool(control.get("watchdog_reminder_id")), "already_complete": True}
    legacy = [item for item in reminders if item.get("target") == "watchdog"]
    active = [item.get("id") for item in legacy if item.get("state") not in TERMINAL_STATES | {"scheduled"} and item.get("linked_message_state") not in TERMINAL_STATES | {None}]
    if active:
        raise ValueError(f"cannot retire linked Watchdog control(s): {', '.join(map(str, active))}")
    retire_ids = sorted({item.get("id") for item in legacy if isinstance(item.get("id"), str)})
    if len(existing | set(retire_ids)) > MAX_RETIRED_LEGACY_REMINDERS:
        raise ValueError(f"retirement record exceeds {MAX_RETIRED_LEGACY_REMINDERS} exact reminder IDs")
    return {"retire_ids": retire_ids, "cancel_ids": [item["id"] for item in legacy if item.get("state") == "scheduled" and item.get("linked_message_id") is None], "clear_reference": bool(control.get("watchdog_reminder_id")), "already_complete": False}


def cancel_reminder(router_url: str, reminder_id: str) -> None:
    request = Request(f"{router_base(router_url)}/v1/reminders/{reminder_id}", method="DELETE")
    try:
        with urlopen(request, timeout=5) as response:
            if response.status != 204:
                raise ValueError(f"router returned HTTP {response.status}")
    except HTTPError as error:
        raise ValueError(f"router refused to cancel {reminder_id}: HTTP {error.code}") from error
    except URLError as error:
        raise ValueError(f"router cancellation failed for {reminder_id}: {error}") from error


def update_retirement_state(source: Path, plan: dict) -> None:
    lock_path = source.with_suffix(source.suffix + ".lock")
    source.parent.mkdir(parents=True, exist_ok=True)
    with lock_path.open("a+") as lock:
        fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
        try:
            state = json.loads(source.read_text())
            control = state.setdefault("priority_control", {})
            if not isinstance(control, dict):
                raise ValueError("priority_control must be an object")
            existing = retired_legacy_ids(state)
            retired = sorted(existing | set(plan["retire_ids"]))
            if len(retired) > MAX_RETIRED_LEGACY_REMINDERS:
                raise ValueError(f"retirement record exceeds {MAX_RETIRED_LEGACY_REMINDERS} exact reminder IDs")
            control.pop("watchdog_reminder_id", None)
            control[RETIREMENT_KEY] = {
                "runtime": "local-priority-handoff-monitor",
                "reconciled_at": utc_now(),
                RETIREMENT_ID_KEY: retired,
            }
            with tempfile.NamedTemporaryFile("w", encoding="utf-8", dir=source.parent, delete=False) as temporary:
                json.dump(state, temporary, indent=2)
                temporary.write("\n")
                temporary.flush()
                os.fsync(temporary.fileno())
                temporary_path = Path(temporary.name)
            temporary_path.replace(source)
        finally:
            fcntl.flock(lock.fileno(), fcntl.LOCK_UN)


def reconcile_retired_controls(state_file: str | None = None, router_url: str | None = None, dry_run: bool = False) -> int:
    source = state_path(state_file)
    try:
        state = json.loads(source.read_text())
        if not isinstance(state, dict):
            raise ValueError("supervisor state must be an object")
        with urlopen(f"{router_base(router_url)}/v1/reminders", timeout=5) as response:
            payload = json.load(response)
        reminders = payload.get("reminders") if isinstance(payload, dict) else None
        if not isinstance(reminders, list) or not all(isinstance(item, dict) for item in reminders):
            raise ValueError("router response has no reminder object list")
        plan = legacy_control_plan(state, reminders)
        if dry_run:
            print(f"PRIORITY HANDOFF MONITOR DRY RUN: retire legacy Watchdog control IDs: {', '.join(plan['retire_ids']) or 'none'}; cancel scheduled legacy controls: {', '.join(plan['cancel_ids']) or 'none'}; clear priority_control watchdog reference: {plan['clear_reference']}")
            return 0
        for reminder_id in plan["cancel_ids"]:
            cancel_reminder(router_base(router_url), reminder_id)
        update_retirement_state(source, plan)
        print(f"PRIORITY HANDOFF MONITOR RETIRED: legacy Watchdog controls: {', '.join(plan['retire_ids']) or 'none'}. owner=supervisor; action=clear/ignore their liveness reference, not recreate a reminder; resolved-when=priority_control records the exact retired IDs and one local Priority Handoff Monitor schedule remains.")
        return 0
    except (OSError, UnicodeDecodeError, ValueError, json.JSONDecodeError, URLError) as error:
        print(actionable("FAIL", "supervisor", f"cannot reconcile legacy Watchdog controls: {error}", "resolve the reported state or router condition and rerun --reconcile-retired-controls", "legacy controls are recorded exactly once and priority_control has no watchdog_reminder_id"), file=sys.stderr, flush=True)
        return 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dry-run", action="store_true", help="Report monitor or cutover actions without Mailbox/state/router mutation")
    parser.add_argument("--install-schedule", action="store_true", help="Install exactly one local five-minute monitor schedule")
    parser.add_argument("--remove-schedule", action="store_true", help="Remove only this monitor's marked cron entry")
    parser.add_argument("--reconcile-retired-controls", action="store_true", help="Retire exact pre-cutover Watchdog reminder IDs and clear priority_control")
    args = parser.parse_args(argv)
    cap_log_file()
    if args.install_schedule:
        try:
            install_schedule()
            return 0
        except RuntimeError as error:
            print(actionable("FAIL", "local operator", str(error), "repair crontab access and rerun --install-schedule", "crontab contains exactly one Priority Handoff Monitor entry"), file=sys.stderr, flush=True)
            return 1
    if args.remove_schedule:
        try:
            remove_schedule()
            return 0
        except RuntimeError as error:
            print(actionable("FAIL", "local operator", str(error), "repair crontab access and rerun --remove-schedule", "no Priority Handoff Monitor cron entry remains"), file=sys.stderr, flush=True)
            return 1
    if args.reconcile_retired_controls:
        return reconcile_retired_controls(dry_run=args.dry_run)
    return run_monitor(dry_run=args.dry_run)


if __name__ == "__main__":
    raise SystemExit(main())
