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
from datetime import datetime, timezone
from pathlib import Path
import subprocess
import sys
import tempfile
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen

DEFAULT_ROUTER_URL = "http://127.0.0.1:8765"
LOG_MAX_BYTES = 1_048_576
SCHEDULE_MARKER = "# mecha-priority-handoff-monitor"
LEGACY_SCHEDULE_MARKER = "# mecha-priority-watchdog"
RETIREMENT_KEY = "priority_handoff_monitor_retirement"
RETIREMENT_ID_KEY = "retired_legacy_watchdog_reminder_ids"
MAX_RETIRED_LEGACY_REMINDERS = 100
TERMINAL_STATES = {"failed", "dead_letter"}
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

    general_sweeps = [
        reminder for reminder in reminders
        if reminder.get("target") == "supervisor"
        and reminder.get("state") == "scheduled"
        and isinstance(reminder.get("interval_ms"), int)
        and 300_000 <= reminder["interval_ms"] <= 600_000
        and "sweep" in str(reminder.get("body", "")).lower()
    ]
    if (delivery_tasks or active_priorities) and len(general_sweeps) != 1:
        findings.append(actionable(
            "FAIL",
            "supervisor",
            f"expected exactly one recurring 5-10 minute general sweep reminder, found {len(general_sweeps)}",
            "repair the recurring supervisor general-sweep reminder through Mailbox",
            "exactly one scheduled recurring supervisor reminder at a 5-10 minute cadence names the general sweep",
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
