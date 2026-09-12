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
from pathlib import Path
import subprocess
import sys
import tempfile
import time
from html.parser import HTMLParser
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


def text(value: Any, name: str) -> str:
    if not isinstance(value, str) or not value.strip() or len(value) > 4000:
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
    try:
        decoded = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        decoded = {}
    if status < 200 or status >= 300:
        error = decoded.get("error") if isinstance(decoded, dict) else None
        message = error.get("message") if isinstance(error, dict) and isinstance(error.get("message"), str) else f"router request failed ({status})"
        return result("error", message, {}, "ROUTER_OPERATION_FAILED", message)
    return result("ok", canonical(decoded), decoded if isinstance(decoded, dict) else {"value": decoded})


def execute_supervisor(operation: str, arguments: dict[str, Any], agent_id: str, settings: dict[str, Any], request_id: str) -> dict[str, Any]:
    action = arguments.get("action")
    if operation == "ops.state":
        if action == "query":
            values: list[str] = []
            if arguments.get("task") is not None:
                values += ["--alias", identifier(arguments["task"], "task")]
            if arguments.get("state") is not None:
                values += ["--state", text(arguments["state"], "state")]
            if not values:
                raise ValueError("query requires task or state")
            return invoke_script("state_query.py", values)
        if action == "create":
            values = ["--alias", identifier(arguments.get("alias"), "alias"), "--type", identifier(arguments.get("type"), "type"), "--project", identifier(arguments.get("project"), "project"), "--description", text(arguments.get("description"), "description")]
            for key, flag in (("purpose", "--purpose"), ("window", "--window"), ("branch", "--branch"), ("pr", "--pr"), ("linear", "--linear")):
                if arguments.get(key) is not None:
                    values += [flag, text(arguments[key], key)]
            return invoke_script("state_add.py", values)
        if action == "update":
            task, field, value = identifier(arguments.get("task"), "task"), text(arguments.get("field"), "field"), text(arguments.get("value"), "value")
            if field == "summary":
                return invoke_script("state_update_status.py", [task, value])
            return invoke_script("state_update_field.py", [task, field, value])
    if operation == "ops.priority":
        if action == "status":
            values = ["status", identifier(arguments.get("priorityId"), "priorityId"), text(arguments.get("status"), "status")]
            if arguments.get("evidence") is not None:
                values += ["--evidence", text(arguments["evidence"], "evidence")]
            return invoke_script("state_priority.py", values)
        values = ["upsert", "--id", identifier(arguments.get("priorityId"), "priorityId"), "--name", text(arguments.get("name"), "name")]
        if arguments.get("status") is not None:
            values += ["--status", text(arguments["status"], "status")]
        task_ids = arguments.get("taskIds")
        if not isinstance(task_ids, list) or not task_ids:
            raise ValueError("taskIds is invalid")
        for task_id in task_ids:
            if not isinstance(task_id, int) or task_id < 1:
                raise ValueError("taskIds is invalid")
            values += ["--task-id", str(task_id)]
        return invoke_script("state_priority.py", values)
    if operation == "ops.verification_handoff":
        values = [identifier(arguments.get("task"), "task"), "--reminder-id", identifier(arguments.get("reminderId"), "reminderId"), "--successor", text(arguments.get("successor"), "successor")]
        if arguments.get("remoteVerified") is not None:
            values += ["--remote-verified", text(arguments["remoteVerified"], "remoteVerified")]
        return invoke_script("state_verification_handoff.py", values)
    if operation == "ops.worker":
        if action in {"launch", "cleanup"}:
            return invoke_script("worker_lifecycle.py", [action, "--alias", identifier(arguments.get("alias"), "alias")])
        if action == "list":
            return invoke_script("worker_list.py", [])
        values = ["--to", identifier(arguments.get("worker"), "worker"), "--body", text(arguments.get("message"), "message"), "--from", agent_id]
        if arguments.get("subject") is not None:
            values += ["--subject", text(arguments["subject"], "subject")]
        return invoke_script("mailbox_send.py", values)
    if operation == "ops.pr_status":
        number = arguments.get("pullRequest")
        if not isinstance(number, int) or number < 1:
            raise ValueError("pullRequest is invalid")
        return invoke_script("pr_status.py", [str(number), "--json"])
    if operation == "ops.git_status":
        return invoke_script("git_task_status.py", ["--task", identifier(arguments.get("task"), "task"), "--json"])
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
    # The remaining process-local controls have no external supervisor script.
    # They are still real root operations: retain the most recent per-agent
    # setting under the request-id idempotency boundary for a later host bridge.
    scoped = settings.setdefault(agent_id, {})
    if not isinstance(scoped, dict):
        raise RuntimeError("managed root control settings are invalid")
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
            if prior is not None:
                if not isinstance(prior, dict) or prior.get("fingerprint") != fingerprint or not isinstance(prior.get("result"), dict):
                    raise ValueError("request ID belongs to another root operation")
                print(canonical(prior["result"]))
                return 0
            if mode == "reconcile":
                print(canonical(result("error", "The root operation has no recoverable host record.", {}, "ROOT_OPERATION_UNKNOWN", "the root operation has no recoverable host record")))
                return 0
            try:
                completed = execute_supervisor(operation, arguments, fence["agent_id"], state["settings"], request_id)
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
