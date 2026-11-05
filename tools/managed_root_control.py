#!/usr/bin/env python3
"""Execute one router-managed root tool with a durable request correlation.

The Go result ledger owns Pi delivery. This host adapter owns the external
operation boundary: a request ID is recorded with its canonical input and
result before it returns, so recovery can replay the recorded result rather
than invoke an uncertain supervisor action a second time.
"""

from __future__ import annotations

import argparse
import base64
import hashlib
import json
import os
import re
import subprocess
import sys
import tempfile
import time
import uuid
from datetime import datetime, timezone
from html.parser import HTMLParser
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, quote, unquote, urlparse
from urllib.request import Request, urlopen

try:
    import fcntl
except ImportError as exc:  # pragma: no cover - supported router hosts are POSIX
    raise RuntimeError("managed root control requires POSIX file locking") from exc

MAX_OUTPUT_BYTES = 16 * 1024
MAX_RECORDS = 200
RETENTION_SECONDS = 30 * 24 * 60 * 60
TOOLS = Path(__file__).resolve().parent

# This runner is also a host boundary, so it must not treat an admitted
# manifest operation as permission to accept arbitrary action names or fields.
# Keep this contract static: the Go admission path owns the canonical manifest,
# while these entries make direct runner validation fail closed if either side
# receives malformed input.
ROOT_OPERATION_CONTRACT: dict[str, dict[str, frozenset[str] | None | bool]] = {
    "goal.control": {"fields": frozenset({"action", "goal", "settings"}), "actions": frozenset({"set", "status", "off"}), "available": True},
    "goal.snooze": {"fields": frozenset(), "actions": None, "available": True},
    "context.control": {"fields": frozenset({"action"}), "actions": frozenset({"status"}), "available": True},
    "ops.git_status": {"fields": frozenset({"action", "task"}), "actions": frozenset({"status"}), "available": True},
    "mail.send": {"fields": frozenset({"recipient", "body", "subject"}), "actions": None, "available": True},
    "mail.parent": {"fields": frozenset({"message"}), "actions": None, "available": False},
    "ops.pr_status": {"fields": frozenset({"action", "pullRequest"}), "actions": frozenset({"status"}), "available": True},
    "ops.priority": {"fields": frozenset({"action", "priorityId", "name", "status", "taskIds", "evidence"}), "actions": frozenset({"create", "update", "status"}), "available": True},
    "recall.control": {"fields": frozenset({"action", "stores"}), "actions": frozenset({"enable", "disable", "status"}), "available": True},
    "reminder.control": {"fields": frozenset({"action", "reminderId", "message", "subject", "delayMinutes", "scheduledAt", "intervalMinutes"}), "actions": frozenset({"list", "create", "update", "delete"}), "available": True},
    "policy.inline_allowance": {"fields": frozenset({"toolName", "usageCount"}), "actions": None, "available": True},
    "router.control": {"fields": frozenset({"action", "messageId"}), "actions": frozenset({"health", "retry", "dismiss"}), "available": True},
    "notifications.settled": {"fields": frozenset({"action"}), "actions": frozenset({"enable", "disable", "status"}), "available": True},
    "ops.state": {"fields": frozenset({"action", "task", "state", "alias", "type", "project", "description", "purpose", "window", "branch", "pr", "linear", "field", "value"}), "actions": frozenset({"query", "create", "update"}), "available": True},
    "ops.verification_handoff": {"fields": frozenset({"action", "task", "reminderId", "successor", "remoteVerified"}), "actions": frozenset({"record"}), "available": True},
    "web.search": {"fields": frozenset({"query", "freshness", "maxResults"}), "actions": None, "available": True},
}


UNAVAILABLE_OPERATIONS = frozenset({
    "ops.state", "ops.priority", "ops.verification_handoff",
    "ops.pr_status", "ops.git_status", "mail.parent",
})


def operation_unavailable(operation: str, arguments: dict[str, Any]) -> bool:
    return operation in UNAVAILABLE_OPERATIONS


def validate_operation_dispatch(operation: str, arguments: dict[str, Any]) -> None:
    contract = ROOT_OPERATION_CONTRACT.get(operation)
    if contract is None:
        raise ValueError("root operation is invalid")
    fields = contract["fields"]
    actions = contract["actions"]
    if not isinstance(fields, frozenset) or (actions is not None and not isinstance(actions, frozenset)):
        raise RuntimeError("root operation contract is invalid")
    unsupported = set(arguments) - fields
    if unsupported:
        raise ValueError(f"unsupported field(s) for {operation}")
    if actions is None:
        if "action" in arguments:
            raise ValueError(f"{operation} action is invalid")
    elif arguments.get("action") not in actions:
        raise ValueError(f"{operation} action is invalid")


def canonical(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)


def result(status: str, content: str, details: dict[str, Any] | None = None, code: str = "", message: str = "") -> dict[str, Any]:
    content = content.encode("utf-8")[:MAX_OUTPUT_BYTES].decode("utf-8", "ignore")
    value: dict[str, Any] = {"status": status, "content": content or "Operation completed.", "details": details or {}}
    if status == "error":
        value["error_code"] = code or "ROOT_OPERATION_FAILED"
        value["error_message"] = message or value["content"]
    return value


def root_state(home: Path) -> tuple[Path, Path]:
    directory = home / "root-control"
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    try:
        directory.chmod(0o700)
    except OSError:
        pass
    return directory / "requests.json", directory / "requests.lock"


def load_state(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return {"version": 1, "requests": {}, "settings": {}}
    except (OSError, json.JSONDecodeError):
        raise RuntimeError("managed root control state is unreadable")
    if not isinstance(value, dict) or value.get("version") != 1 or not isinstance(value.get("requests"), dict) or not isinstance(value.get("settings"), dict):
        raise RuntimeError("managed root control state is invalid")
    return value


def save_state(path: Path, value: dict[str, Any]) -> None:
    fd, temporary = tempfile.mkstemp(prefix="requests-", suffix=".json", dir=path.parent)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as output:
            output.write(canonical(value))
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.chmod(temporary, 0o600)
        os.replace(temporary, path)
    finally:
        try:
            os.unlink(temporary)
        except FileNotFoundError:
            pass


def prune_requests(state: dict[str, Any], now: float) -> None:
    requests = state["requests"]
    for request_id, entry in list(requests.items()):
        if not isinstance(entry, dict) or not isinstance(entry.get("recorded_at"), (int, float)) or entry["recorded_at"] <= now - RETENTION_SECONDS:
            requests.pop(request_id, None)
    if len(requests) > MAX_RECORDS:
        ordered = sorted(requests.items(), key=lambda item: (float(item[1].get("recorded_at", 0)), item[0]))
        for request_id, _entry in ordered[:len(requests) - MAX_RECORDS]:
            requests.pop(request_id, None)


def command(arguments: list[str]) -> str:
    completed = subprocess.run(arguments, cwd=TOOLS, capture_output=True, text=True, timeout=30, check=False)
    output = "\n".join(part.strip() for part in (completed.stdout, completed.stderr) if part.strip()).strip()
    if completed.returncode != 0:
        raise RuntimeError(output or "supervisor operation failed")
    return output[:MAX_OUTPUT_BYTES]


def text(value: Any, name: str, maximum: int = 4000) -> str:
    if not isinstance(value, str) or not value.strip() or len(value) > maximum:
        raise ValueError(f"{name} is invalid")
    return value.strip()


def identifier(value: Any, name: str) -> str:
    raw = text(value, name)
    if not all(character.isalnum() or character in "._:-" for character in raw) or len(raw) > 120:
        raise ValueError(f"{name} is invalid")
    return raw


def invoke_script(name: str, arguments: list[str]) -> dict[str, Any]:
    output = command([sys.executable, str(TOOLS / name), *arguments])
    return result("ok", output, {"operation": name})


class BingResultParser(HTMLParser):
    """Extract the small, public result subset that can safely cross to Pi."""

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.results: list[dict[str, str]] = []
        self._current: dict[str, str] | None = None
        self._heading_depth = 0
        self._capture: str | None = None
        self._text: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        values = {name: value or "" for name, value in attrs}
        classes = set(values.get("class", "").split())
        if tag == "li" and "b_algo" in classes:
            self._current = {"title": "", "href": "", "snippet": ""}
        if self._current is None:
            return
        if tag == "h2":
            self._heading_depth += 1
        elif tag == "a" and self._heading_depth > 0 and not self._current["href"]:
            self._current["href"] = values.get("href", "")
            self._capture = "title"
            self._text = []
        elif tag in {"div", "p"} and "b_caption" in classes:
            self._capture = "snippet"
            self._text = []

    def handle_data(self, data: str) -> None:
        if self._capture is not None:
            self._text.append(data)

    def handle_endtag(self, tag: str) -> None:
        if self._current is None:
            return
        if self._capture == "title" and tag == "a":
            self._current["title"] = " ".join("".join(self._text).split())
            self._capture = None
            self._text = []
        elif self._capture == "snippet" and tag in {"div", "p"}:
            self._current["snippet"] = " ".join("".join(self._text).split())
            self._capture = None
            self._text = []
        if tag == "h2" and self._heading_depth > 0:
            self._heading_depth -= 1
        elif tag == "li":
            if self._current["title"] and self._current["href"]:
                self.results.append(self._current)
            self._current = None
            self._heading_depth = 0
            self._capture = None
            self._text = []


def managed_search_url(value: str) -> str | None:
    if not isinstance(value, str) or not value or len(value) > 2_048:
        return None
    parsed = urlparse(value)
    if parsed.scheme not in {"http", "https"} or not parsed.hostname or parsed.username or parsed.password:
        return None
    if parsed.hostname == "www.bing.com" and parsed.path == "/ck/a":
        target = unquote(parse_qs(parsed.query).get("u", [""])[0])
        if target.startswith("a1"):
            try:
                target = base64.urlsafe_b64decode(target[2:] + "=" * (-len(target[2:]) % 4)).decode("utf-8")
            except (UnicodeDecodeError, ValueError):
                return None
        return managed_search_url(target)
    return value


def managed_search_endpoint() -> str:
    configured = os.environ.get("MECHA_MANAGED_WEB_SEARCH_ENDPOINT")
    if configured is None:
        return "https://www.bing.com/search"
    parsed = urlparse(configured)
    # The configured endpoint is a host-owned test/deployment setting, not an
    # agent argument. Permit only the fixed public provider or a loopback fake.
    if parsed.scheme not in {"http", "https"} or parsed.path != "/search" or parsed.query or parsed.fragment or parsed.username or parsed.password:
        raise RuntimeError("managed web search endpoint is invalid")
    if parsed.hostname not in {"www.bing.com", "127.0.0.1", "localhost", "::1"}:
        raise RuntimeError("managed web search endpoint is invalid")
    return configured


def managed_web_search(arguments: dict[str, Any]) -> dict[str, Any]:
    query = text(arguments.get("query"), "query")
    freshness = arguments.get("freshness", "cached")
    if freshness not in {"live", "cached", "indexed"}:
        raise ValueError("freshness is invalid")
    maximum = arguments.get("maxResults", 5)
    if not isinstance(maximum, int) or isinstance(maximum, bool) or maximum < 1 or maximum > 10:
        raise ValueError("maxResults is invalid")
    endpoint = managed_search_endpoint()
    separator = "&" if "?" in endpoint else "?"
    request = Request(f"{endpoint}{separator}q={quote(query)}", headers={"User-Agent": "Mozilla/5.0 (compatible; MechaSearch/1.0)"})
    try:
        with urlopen(request, timeout=15) as response:
            raw = response.read(512 * 1024 + 1)
    except (HTTPError, URLError, TimeoutError, OSError) as exc:
        raise RuntimeError("managed web search is unavailable") from exc
    if len(raw) > 512 * 1024:
        raise RuntimeError("managed web search response is too large")
    try:
        document = raw.decode("utf-8")
    except UnicodeDecodeError as exc:
        raise RuntimeError("managed web search response is invalid") from exc
    parser = BingResultParser()
    parser.feed(document)
    parser.close()
    sources: list[dict[str, str]] = []
    for candidate in parser.results:
        url = managed_search_url(candidate["href"])
        title = candidate["title"][:512]
        snippet = candidate["snippet"][:2_000]
        if url is None or not title:
            continue
        proposed = [*sources, {"title": title, "url": url, "snippet": snippet}]
        lines = [f"Found {len(proposed)} source-backed web result{'s' if len(proposed) != 1 else ''}."]
        for index, source in enumerate(proposed, start=1):
            lines.append(f"{index}. {source['title']}\n{source['url']}" + (f"\n{source['snippet']}" if source["snippet"] else ""))
        content = "\n\n".join(lines)
        details = {"provider": "bing", "freshness": freshness, "sources": proposed}
        # The control-result envelope also contains correlation fields. Reserve
        # a little space so linked source details cannot make that envelope
        # exceed the shared 16 KiB result boundary.
        if len(content.encode("utf-8")) + len(canonical(details).encode("utf-8")) > MAX_OUTPUT_BYTES - 512:
            break
        sources = proposed
        if len(sources) >= maximum:
            break
    if not sources:
        raise RuntimeError("managed web search returned no source results")
    lines = [f"Found {len(sources)} source-backed web result{'s' if len(sources) != 1 else ''}."]
    for index, source in enumerate(sources, start=1):
        lines.append(f"{index}. {source['title']}\n{source['url']}" + (f"\n{source['snippet']}" if source["snippet"] else ""))
    return result("ok", "\n\n".join(lines), {"provider": "bing", "freshness": freshness, "sources": sources})


def request_router(path: str, method: str = "GET", payload: dict[str, Any] | None = None) -> dict[str, Any]:
    base = os.environ.get("MECHA_ROUTER_URL", "http://127.0.0.1:8765").rstrip("/")
    data = None if payload is None else canonical(payload).encode("utf-8")
    request = Request(base + path, data=data, method=method, headers={} if data is None else {"content-type": "application/json"})
    try:
        with urlopen(request, timeout=5) as response:
            raw = response.read(MAX_OUTPUT_BYTES + 1)
            status = response.status
    except HTTPError as error:
        raw, status = error.read(MAX_OUTPUT_BYTES + 1), error.code
        error.close()
    try:
        decoded = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        decoded = {}
    if status < 200 or status >= 300:
        error = decoded.get("error") if isinstance(decoded, dict) else None
        message = error.get("message") if isinstance(error, dict) and isinstance(error.get("message"), str) else f"router request failed ({status})"
        return result("error", message, {}, "ROUTER_OPERATION_FAILED", message)
    return result("ok", canonical(decoded), decoded if isinstance(decoded, dict) else {"value": decoded})


def reminder_clock_time() -> datetime:
    return datetime.fromtimestamp(time.time(), timezone.utc)


def reminder_scheduled_time(value: Any) -> str:
    raw = text(value, "scheduledAt", 64)
    if re.search(r"(?:Z|[+-]\d{2}:?\d{2})$", raw, re.IGNORECASE) is None:
        raise ValueError("scheduledAt must include an explicit timezone offset or Z")
    try:
        parsed = datetime.fromisoformat(raw[:-1] + "+00:00" if raw.endswith(("Z", "z")) else raw)
    except ValueError as exc:
        raise ValueError("scheduledAt must be a valid ISO 8601 datetime") from exc
    if parsed.tzinfo is None or parsed.utcoffset() is None:
        raise ValueError("scheduledAt must include an explicit timezone offset or Z")
    return parsed.astimezone(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def reminder_due_after(created: datetime, minutes: int) -> str:
    return datetime.fromtimestamp(created.timestamp() + minutes * 60, timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def reminder_minutes(value: Any, name: str) -> int:
    if not isinstance(value, int) or isinstance(value, bool) or value < 1 or value > 10_080:
        raise ValueError(f"{name} is invalid")
    return value


def reminder_row(payload: Any) -> dict[str, Any]:
    if not isinstance(payload, dict):
        raise RuntimeError("router returned an invalid reminder")
    required = ("id", "target", "creator", "body", "created_at", "next_due_at", "interval_ms")
    if any(key not in payload for key in required):
        raise RuntimeError("router returned an invalid reminder")
    identifier(payload["id"], "reminder id")
    for key in ("target", "creator", "body", "created_at", "next_due_at"):
        text(payload[key], key)
    interval = payload["interval_ms"]
    if interval is not None and (not isinstance(interval, int) or isinstance(interval, bool) or interval < 1):
        raise RuntimeError("router returned an invalid reminder")
    return payload


def reminder_payload(response: dict[str, Any]) -> dict[str, Any]:
    if response.get("status") != "ok":
        raise RuntimeError(response.get("error_message") or "router reminder request failed")
    payload = response.get("details")
    if not isinstance(payload, dict):
        raise RuntimeError("router returned an invalid reminder response")
    return payload


def managed_reminder(arguments: dict[str, Any], fence: dict[str, Any]) -> dict[str, Any]:
    action = arguments.get("action")
    allowed = {
        "list": {"action"},
        "create": {"action", "message", "subject", "delayMinutes", "scheduledAt", "intervalMinutes"},
        "update": {"action", "reminderId", "message", "subject", "delayMinutes", "scheduledAt", "intervalMinutes"},
        "delete": {"action", "reminderId"},
    }
    if action not in allowed:
        raise ValueError("reminder action is invalid")
    if set(arguments) - allowed[action]:
        raise ValueError("unsupported reminder field(s)")
    target = identifier(fence.get("agent_id"), "reminder target")
    timing = ("delayMinutes", "scheduledAt", "intervalMinutes")
    provided_timing = [name for name in timing if name in arguments]
    if action == "create" and len(provided_timing) != 1:
        raise ValueError("create requires exactly one of delayMinutes, scheduledAt, or intervalMinutes")
    if action == "update" and len(provided_timing) > 1:
        raise ValueError("update accepts at most one of delayMinutes, scheduledAt, or intervalMinutes")

    if action == "list":
        payload = reminder_payload(request_router("/v1/reminders"))
        reminders = payload.get("reminders")
        if not isinstance(reminders, list):
            raise RuntimeError("router returned an invalid reminder list")
        rows = [reminder_row(row) for row in reminders]
        owned = [row for row in rows if row["target"] == target]
        details = {**payload, "reminders": owned}
        return result("ok", canonical(details), details)

    if action == "create":
        message = text(arguments.get("message"), "message")
        created = reminder_clock_time()
        has_delay = "delayMinutes" in arguments
        has_interval = "intervalMinutes" in arguments
        interval_ms: int | None = None
        if has_delay or has_interval:
            name = "delayMinutes" if has_delay else "intervalMinutes"
            minutes = reminder_minutes(arguments[name], name)
            due = reminder_due_after(created, minutes)
            if has_interval:
                interval_ms = minutes * 60_000
        else:
            due = reminder_scheduled_time(arguments.get("scheduledAt"))
        payload: dict[str, Any] = {
            "id": identifier(str(uuid.uuid4()), "generated reminder ID"),
            "target": target,
            "creator": target,
            "body": message,
            "created_at": created.isoformat(timespec="milliseconds").replace("+00:00", "Z"),
            "next_due_at": due,
            "interval_ms": interval_ms,
        }
        if "subject" in arguments:
            payload["subject"] = text(arguments["subject"], "subject", 200)
        response = reminder_payload(request_router("/v1/reminders", "POST", payload))
        row = reminder_row(response.get("reminder"))
        if row["id"] != payload["id"] or row["target"] != target or row["creator"] != target:
            raise RuntimeError("router returned an invalid persisted reminder")
        return result("ok", "Reminder accepted.", {"reminder": row})

    reminder_id = identifier(arguments.get("reminderId"), "reminderId")
    owned_response = reminder_payload(request_router(f"/v1/reminders/{quote(reminder_id, safe='')}"))
    owned = reminder_row(owned_response.get("reminder"))
    if owned["id"] != reminder_id or owned["target"] != target:
        raise RuntimeError("reminder belongs to another agent")
    if action == "delete":
        response = request_router(f"/v1/reminders/{quote(reminder_id, safe='')}", "DELETE")
        reminder_payload(response)
        return result("ok", "Reminder canceled.", {"reminder_id": reminder_id})

    patch: dict[str, Any] = {}
    if "message" in arguments:
        patch["body"] = text(arguments["message"], "message")
    if "subject" in arguments:
        patch["subject"] = text(arguments["subject"], "subject", 200)
    if provided_timing:
        timing_name = provided_timing[0]
        if timing_name == "scheduledAt":
            patch["next_due_at"] = reminder_scheduled_time(arguments["scheduledAt"])
        else:
            minutes = reminder_minutes(arguments[timing_name], timing_name)
            patch["next_due_at"] = reminder_due_after(reminder_clock_time(), minutes)
            patch["interval_ms"] = None if timing_name == "delayMinutes" else minutes * 60_000
    if not patch:
        raise ValueError("update requires at least one field to change")
    response = reminder_payload(request_router(f"/v1/reminders/{quote(reminder_id, safe='')}", "PATCH", patch))
    row = reminder_row(response.get("reminder"))
    if row["id"] != reminder_id or row["target"] != target:
        raise RuntimeError("router returned an invalid persisted reminder")
    return result("ok", "Reminder updated.", {"reminder": row})


def execute_supervisor(operation: str, arguments: dict[str, Any], agent_id: str, settings: dict[str, Any], request_id: str, fence: dict[str, Any]) -> dict[str, Any]:
    validate_operation_dispatch(operation, arguments)
    if operation_unavailable(operation, arguments):
        return result("error", f"{operation} is unavailable in this checkout.", {"operation": operation}, "ROOT_OPERATION_UNAVAILABLE")
    action = arguments.get("action")
    if operation == "web.search":
        return managed_web_search(arguments)
    if operation == "mail.send":
        # The router validates this control request against its durable, fenced
        # authorization before it can enter quiet history. It is provenance,
        # not browser authentication: ordinary direct HTTP remains credentialless.
        values = ["--to", identifier(arguments.get("recipient"), "recipient"), "--body", text(arguments.get("body"), "body"), "--from", agent_id, "--managed-control-request-id", identifier(request_id, "request_id")]
        if arguments.get("subject") is not None:
            values += ["--subject", text(arguments["subject"], "subject")]
        return invoke_script("mailbox_send.py", values)
    if operation == "router.control":
        if action == "health":
            return request_router("/v1/health")
        message_id = identifier(arguments.get("messageId"), "messageId")
        return request_router("/v1/messages/" + message_id + ("/retry" if action == "retry" else ""), "POST" if action == "retry" else "DELETE")
    if operation == "reminder.control":
        return managed_reminder(arguments, fence)
    # Goal state is router-managed durable product state even though the small
    # host adapter carries it between current managed process instances. Keep a
    # stable read projection so the Web UI can reopen the shared goal editor.
    scoped = settings.setdefault(agent_id, {})
    if not isinstance(scoped, dict):
        raise RuntimeError("managed root control settings are invalid")
    if operation == "goal.control":
        default_settings = {
            "goal_lock_enabled": False,
            "snooze_enabled": True,
            "snooze_duration_ms": 60_000,
            "snooze_turn_limit": None,
            "snooze_budget_cap_ms": None,
            "snooze_refill_yield_ms": 30_000,
            "snooze_refill_elapsed_ms": 60_000,
        }
        current = scoped.get(operation)
        if not isinstance(current, dict):
            current = {"enabled": False, "goal": "", **default_settings}
        else:
            current = {**default_settings, **current}
        supplied_settings = arguments.get("settings")
        if supplied_settings is not None:
            if not isinstance(supplied_settings, dict) or set(supplied_settings) != set(default_settings):
                raise ValueError("goal settings are invalid")
            current.update(supplied_settings)
        if action == "set":
            current.update({"enabled": True, "goal": text(arguments.get("goal"), "goal")})
            scoped[operation] = current
        elif action == "off":
            current.update({"enabled": False, "goal": ""})
            scoped[operation] = current
        elif action != "status":
            raise ValueError("goal action is invalid")
        return result("ok", "Managed goal updated." if action != "status" else "Managed goal loaded.", {"goal_state": current})
    # The remaining process-local controls have no external supervisor script.
    # They are still real root operations: retain the most recent per-agent
    # setting under the request-id idempotency boundary for a later host bridge.
    scoped[operation] = arguments
    return result("ok", "Managed root control updated.", {"operation": operation, "action": action})


def run(mode: str, home: Path) -> int:
    try:
        request = json.load(sys.stdin)
        if not isinstance(request, dict):
            raise ValueError("request is invalid")
        request_id = request.get("request_id")
        operation = request.get("operation_id")
        arguments = request.get("arguments")
        fence = request.get("fence")
        if not isinstance(request_id, str) or not request_id or not isinstance(operation, str) or not operation or not isinstance(arguments, dict) or not isinstance(fence, dict) or not isinstance(fence.get("agent_id"), str):
            raise ValueError("request is invalid")
        fingerprint = hashlib.sha256(canonical({"operation_id": operation, "arguments": arguments, "fence": fence}).encode("utf-8")).hexdigest()
        state_path, lock_path = root_state(home)
        with lock_path.open("a+", encoding="utf-8") as lock:
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX)
            state = load_state(state_path)
            now = time.time()
            prune_requests(state, now)
            prior = state["requests"].get(request_id)
            # Reconciliation never guesses ownership from a request ID. A
            # mismatched fingerprint includes operation, arguments, or fence.
            if prior is not None:
                if not isinstance(prior, dict) or prior.get("fingerprint") != fingerprint or not isinstance(prior.get("result"), dict):
                    if mode == "reconcile":
                        print(canonical(result("error", "The root operation outcome is unknown.", {}, "ROOT_OPERATION_UNKNOWN", "the host record cannot be verified")))
                        return 0
                    raise ValueError("request ID belongs to another root operation")
                print(canonical(prior["result"]))
                return 0
            if operation == "ops.worker" and mode == "execute":
                print(canonical(result("error", "The retired worker operation is unavailable.", {}, "ROOT_OPERATION_UNAVAILABLE")))
                return 0
            if mode == "reconcile":
                print(canonical(result("error", "The root operation has no recoverable host record.", {}, "ROOT_OPERATION_UNKNOWN", "the root operation has no recoverable host record")))
                return 0
            try:
                completed = execute_supervisor(operation, arguments, fence["agent_id"], state["settings"], request_id, fence)
            except (OSError, RuntimeError, ValueError, subprocess.TimeoutExpired) as exc:
                completed = result("error", "The root operation could not be completed.", {}, "ROOT_OPERATION_FAILED", str(exc)[:2000])
            state["requests"][request_id] = {"fingerprint": fingerprint, "recorded_at": now, "result": completed}
            prune_requests(state, now)
            save_state(state_path, state)
            print(canonical(completed))
            return 0
    except (OSError, ValueError, RuntimeError, json.JSONDecodeError) as exc:
        print(str(exc)[:2000], file=sys.stderr)
        return 1


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("execute", "reconcile"))
    parser.add_argument("--home", required=True)
    arguments = parser.parse_args()
    return run(arguments.mode, Path(arguments.home))


if __name__ == "__main__":
    raise SystemExit(main())
