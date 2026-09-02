#!/usr/bin/env python3
"""Check visible native subagent labels against router activity state.

The checker is deliberately read-only. It asks tmux for pane scrollback and
asks the router for its window inventory and agent snapshot. It never sends
keys, restarts a process, or prints captured scrollback or raw API payloads.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from typing import Any, Callable, Iterable, Mapping, Sequence
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import capture_pane

DEFAULT_ROUTER_URL = "http://127.0.0.1:8765"
DEFAULT_SCROLLBACK_LINES = 200
MAX_SCROLLBACK_LINES = 2_000
DEFAULT_TIMEOUT = 5.0
MAX_RESPONSE_BYTES = 512 * 1024
MAX_VISIBLE_DESCRIPTIONS = 4
SPINNER_FRAMES = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"
WINDOW_NAME_RE = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$")
ANSI_RE = re.compile(
    r"(?:\x1b\][^\x07]*(?:\x07|\x1b\\)|\x1b\[[0-?]*[ -/]*[@-~]|\x1b[@-_])"
)
# The widget's running rows are rendered as:
#   ├─ ⠋ Display name  short description · ↻1 · ...
# The two spaces separate the display name from the user-provided label. The
# stats marker and running spinner keep ordinary prompt/output text out.
AGENTS_HEADING_RE = re.compile(r"^\s*[●○]\s+Agents\s*$")
WIDGET_LINE_RE = re.compile(r"^\s*(?:[├└]─|│)")
WIDGET_OVERFLOW_RE = re.compile(r"^\s*[├└]─\s+\+\d+\s+more\s+\(")
RUNNING_ROW_RE = re.compile(
    rf"^\s*(?:[├└]─\s*)?[{re.escape(SPINNER_FRAMES)}]\s+(.+?)\s+·\s+↻\d+(?:≤\d+)?(?:\s|$)"
)


class CheckerError(RuntimeError):
    """An expected, safe-to-report checker failure."""


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request: Request, fp: Any, code: int, msg: str, headers: Any, new_url: str) -> None:
        return None


@dataclass(frozen=True)
class CaptureResult:
    window_id: str
    name: str | None
    descriptions: tuple[str, ...]
    error: str | None = None
    incomplete: bool = False


@dataclass(frozen=True)
class RouterResult:
    agents: tuple[Mapping[str, Any], ...]
    windows: tuple[Mapping[str, Any], ...]
    error: str | None = None
    windows_error: str | None = None


def strip_terminal_control(text: str) -> str:
    """Remove terminal control sequences before parsing rendered rows."""
    return ANSI_RE.sub("", text).replace("\r", "")


def normalize_description(text: str) -> str:
    """Normalize only comparison whitespace/punctuation; never return it in reports."""
    text = strip_terminal_control(text).casefold()
    # Widget labels are short text. Treat punctuation and line wrapping alike,
    # so "router-state" and "router state" are a partial match.
    text = re.sub(r"[^\w]+", " ", text, flags=re.UNICODE)
    return " ".join(text.split())


def descriptions_overlap(left: str, right: str) -> bool:
    """Return true for exact or partial phrase overlap in either direction."""
    left_normalized = normalize_description(left)
    right_normalized = normalize_description(right)
    return bool(left_normalized and right_normalized and (
        left_normalized in right_normalized or right_normalized in left_normalized
    ))


def parse_latest_agents_widget(scrollback: str, limit: int = MAX_VISIBLE_DESCRIPTIONS) -> tuple[list[str], bool, bool]:
    """Return running labels from the latest Agents widget and overflow state.

    The widget is the only accepted source. Looking for a spinner in arbitrary
    scrollback would expose prompt/output text and stale historical rows.
    """
    if limit <= 0:
        return [], False, False
    lines = strip_terminal_control(scrollback).splitlines()
    heading_index = next((index for index in range(len(lines) - 1, -1, -1) if AGENTS_HEADING_RE.match(lines[index])), None)
    if heading_index is None:
        return [], False, False
    descriptions: list[str] = []
    overflow = False
    for line in lines[heading_index + 1:]:
        if not line.strip():
            continue
        if not WIDGET_LINE_RE.match(line):
            break
        if WIDGET_OVERFLOW_RE.match(line):
            overflow = True
            continue
        match = RUNNING_ROW_RE.match(line)
        if not match or len(descriptions) >= limit:
            continue
        label_and_name = match.group(1).strip()
        pieces = re.split(r"\s{2,}", label_and_name, maxsplit=1)
        if len(pieces) != 2:
            continue
        description = " ".join(pieces[1].split()).strip()
        if normalize_description(description):
            descriptions.append(description)
    return descriptions, overflow, True


def parse_active_subagent_descriptions(scrollback: str, limit: int = MAX_VISIBLE_DESCRIPTIONS) -> list[str]:
    """Extract up to *limit* running descriptions from the latest widget."""
    return parse_latest_agents_widget(scrollback, limit)[0]


def compare_descriptions(tmux_descriptions: Iterable[str], router_descriptions: Iterable[str]) -> dict[str, Any]:
    """Apply the narrow checker policy with order-independent multiset matching."""
    tmux = [text for text in tmux_descriptions if normalize_description(text)]
    router = [text for text in router_descriptions if normalize_description(text)]

    # Maximum bipartite matching prevents order from affecting partial matches
    # and preserves duplicate descriptions as separate multiset members.
    edges = [[index for index, candidate in enumerate(router) if descriptions_overlap(text, candidate)] for text in tmux]
    matched_router: dict[int, int] = {}

    def augment(tmux_index: int, seen: set[int]) -> bool:
        for router_index in edges[tmux_index]:
            if router_index in seen:
                continue
            seen.add(router_index)
            owner = matched_router.get(router_index)
            if owner is None or augment(owner, seen):
                matched_router[router_index] = tmux_index
                return True
        return False

    matched_tmux = {tmux_index for tmux_index in range(len(tmux)) if augment(tmux_index, set())}
    missing = [text for index, text in enumerate(tmux) if index not in matched_tmux]
    has_overlap = bool(matched_tmux)
    flags: list[dict[str, Any]] = []
    if tmux and router and not has_overlap:
        flags.append({"kind": "no_description_overlap"})
    for text in missing:
        flags.append({"kind": "tmux_description_absent_from_router", "description": text})
    return {
        "tmux_description_count": len(tmux),
        "router_description_count": len(router),
        "has_overlap": has_overlap,
        "missing_tmux_descriptions": missing,
        "flags": flags,
    }


def capture_window(window_name: str, lines: int = DEFAULT_SCROLLBACK_LINES, timeout: float = DEFAULT_TIMEOUT) -> CaptureResult:
    """Capture ``mecha:<window_name>`` with the existing read-only tmux helper."""
    if not WINDOW_NAME_RE.fullmatch(window_name):
        return CaptureResult(window_name, None, (), "window name must match the router tmux name format")
    try:
        # capture-pane only reads terminal state. It does not send keys or
        # change the foreground process. The timeout is enforced by _tmux.
        captured = capture_pane(window_name, lines=lines, timeout=timeout)
        if captured is None:
            raise CheckerError("tmux pane capture failed or window was not found")
        descriptions, overflow, _widget_found = parse_latest_agents_widget(captured)
        return CaptureResult(window_name, window_name, tuple(descriptions), None, overflow)
    except TypeError:
        # Keep injected/older capture helpers useful in focused tests while
        # retaining the timeout in the production helper.
        try:
            captured = capture_pane(window_name, lines=lines)
        except (OSError, subprocess.TimeoutExpired):
            return CaptureResult(window_name, window_name, (), "tmux request timed out or was unavailable")
        if captured is None:
            return CaptureResult(window_name, None, (), "tmux pane capture failed or window was not found")
        descriptions, overflow, _widget_found = parse_latest_agents_widget(captured)
        return CaptureResult(window_name, window_name, tuple(descriptions), None, overflow)
    except (OSError, subprocess.TimeoutExpired):
        return CaptureResult(window_name, window_name, (), "tmux request timed out or was unavailable")


def _router_url(value: str) -> str:
    parsed = urlparse(value)
    if parsed.scheme not in {"http", "https"} or parsed.hostname not in {"127.0.0.1", "::1", "localhost"}:
        raise argparse.ArgumentTypeError("router URL must use a loopback http(s) address")
    return value.rstrip("/")


def _read_json_response(response: Any) -> Any:
    raw = response.read(MAX_RESPONSE_BYTES + 1)
    if len(raw) > MAX_RESPONSE_BYTES:
        raise CheckerError("router response exceeded local limits")
    if not raw:
        return None
    try:
        return json.loads(raw)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise CheckerError("router response was not valid JSON") from error


def _request_json(url: str, timeout: float) -> Any:
    request = Request(url, method="GET")
    try:
        with build_opener(NoRedirect()).open(request, timeout=timeout) as response:
            if response.status < 200 or response.status >= 300:
                raise CheckerError(f"router returned HTTP {response.status}")
            return _read_json_response(response)
    except HTTPError as error:
        raise CheckerError(f"router returned HTTP {error.code}") from error
    except (URLError, TimeoutError, OSError) as error:
        raise CheckerError("router request timed out or was unavailable") from error


def _valid_records(payload: Any, key: str) -> tuple[Mapping[str, Any], ...]:
    if not isinstance(payload, dict) or not isinstance(payload.get(key), list):
        raise CheckerError(f"router response did not contain a valid {key} list")
    return tuple(item for item in payload[key] if isinstance(item, dict))


def fetch_router_state(router_url: str = DEFAULT_ROUTER_URL, timeout: float = DEFAULT_TIMEOUT) -> RouterResult:
    """Fetch sanitized agent state and tmux inventory concurrently."""
    with ThreadPoolExecutor(max_workers=2, thread_name_prefix="router-state") as pool:
        agents_future = pool.submit(_request_json, f"{router_url}/v1/agents?models=omit", timeout)
        windows_future = pool.submit(_request_json, f"{router_url}/v1/tmux/windows", timeout)
        try:
            agents = _valid_records(agents_future.result(), "agents")
        except CheckerError as error:
            return RouterResult((), (), str(error))
        try:
            windows = _valid_records(windows_future.result(), "windows")
        except CheckerError as error:
            return RouterResult(agents, (), None, str(error))
    return RouterResult(agents, windows)


def _agent_descriptions(agent: Mapping[str, Any]) -> list[str]:
    children = agent.get("subagents")
    if not isinstance(children, list):
        return []
    descriptions: list[str] = []
    for child in children:
        if isinstance(child, dict) and isinstance(child.get("description"), str):
            description = child["description"].strip()
            if normalize_description(description):
                descriptions.append(description)
    return descriptions


def _public_window(result: CaptureResult, router: RouterResult) -> dict[str, Any]:
    tmux_descriptions = list(result.descriptions)
    router_names = {item.get("name") for item in router.windows if isinstance(item.get("name"), str)}
    # The API has no numeric-ID-to-agent endpoint. Its window inventory is the
    # authoritative name set; the matching name is then the router agent_id.
    mapped_name = result.name if result.name in router_names else None
    agent_id = mapped_name
    agent = next((item for item in router.agents if item.get("agent_id") == agent_id), None)
    public: dict[str, Any] = {
        "window_id": result.window_id,
        "window_name": result.name,
        "router_agent_id": agent_id,
        "tmux_descriptions": tmux_descriptions,
        "findings": [],
    }
    if result.error:
        public["capture_error"] = result.error
        return public
    if result.incomplete:
        public["capture_status"] = "incomplete"
        public["capture_error"] = "Agents widget overflowed; visible descriptions are incomplete"
        return public
    if router.error:
        public["router_error"] = router.error
        return public
    if router.windows_error:
        public["mapping_error"] = router.windows_error
        return public
    if mapped_name is None:
        public["mapping_error"] = "window is not present in router tmux inventory"
        return public
    router_descriptions = _agent_descriptions(agent) if agent else []
    comparison = compare_descriptions(tmux_descriptions, router_descriptions)
    # Do not serialize router descriptions or raw objects. Only bounded labels
    # selected from tmux and the finding labels needed to act are emitted.
    public.update({
        "router_agent_present": agent is not None,
        "router_window_present": mapped_name is not None,
        "router_description_count": comparison["router_description_count"],
        "has_description_overlap": comparison["has_overlap"],
        "findings": comparison["flags"],
    })
    return public


def run_check(
    window_ids: Sequence[str],
    router_url: str = DEFAULT_ROUTER_URL,
    lines: int = DEFAULT_SCROLLBACK_LINES,
    timeout: float = DEFAULT_TIMEOUT,
    capture: Callable[[str, int, float], CaptureResult] = capture_window,
    router: Callable[[str, float], RouterResult] = fetch_router_state,
) -> dict[str, Any]:
    """Run all independent captures and router requests concurrently."""
    unique_ids = list(dict.fromkeys(window_ids))
    if not unique_ids:
        raise CheckerError("at least one tmux window name is required")
    with ThreadPoolExecutor(max_workers=max(2, len(unique_ids) + 2), thread_name_prefix="subagent-check") as pool:
        capture_futures = {pool.submit(capture, window_id, lines, timeout): window_id for window_id in unique_ids}
        router_future = pool.submit(router, router_url, timeout)
        capture_results: dict[str, CaptureResult] = {}
        for future in as_completed(capture_futures):
            window_id = capture_futures[future]
            try:
                capture_results[window_id] = future.result()
            except Exception as error:  # Keep one failed window from hiding other results.
                capture_results[window_id] = CaptureResult(window_id, None, (), "tmux capture failed")
        try:
            router_result = router_future.result()
        except Exception:
            router_result = RouterResult((), (), "router request failed")

    windows = [_public_window(capture_results[window_id], router_result) for window_id in unique_ids]
    findings = [
        {"window_id": item["window_id"], "kind": finding["kind"], **({"description": finding["description"]} if "description" in finding else {})}
        for item in windows
        for finding in item.get("findings", [])
    ]
    errors = [
        {"window_id": item["window_id"], "kind": "capture_error", "message": item["capture_error"]}
        for item in windows
        if item.get("capture_error")
    ]
    errors.extend(
        {"window_id": item["window_id"], "kind": "mapping_error", "message": item["mapping_error"]}
        for item in windows
        if item.get("mapping_error")
    )
    if router_result.error:
        errors.append({"kind": "router_error", "message": router_result.error})
    if router_result.windows_error:
        errors.append({"kind": "router_tmux_inventory_error", "message": router_result.windows_error})
    return {
        "version": 1,
        "mapping": {
            "method": "tmux_window_name_equals_router_agent_id",
            "inventory_endpoint": "/v1/tmux/windows",
            "agent_endpoint": "/v1/agents?models=omit",
            "direct_tmux_window_to_agent_api": False,
        },
        "privacy": "pane scrollback, prompts, message bodies, and raw router payloads are never emitted",
        "windows": windows,
        "findings": findings,
        "errors": errors,
    }


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Compare visible active native subagent labels in tmux windows with router state (read-only)."
    )
    parser.add_argument(
        "window_ids",
        nargs="*",
        metavar="WINDOW_NAME",
        help="tmux window name(s) from /v1/tmux/windows, for example worker-a",
    )
    parser.add_argument(
        "--window",
        dest="window_options",
        action="append",
        default=[],
        metavar="WINDOW_NAME",
        help="tmux window name; may be repeated",
    )
    parser.add_argument("--router-url", type=_router_url, default=os.environ.get("MECHA_ROUTER_URL", DEFAULT_ROUTER_URL))
    parser.add_argument("--lines", type=int, default=DEFAULT_SCROLLBACK_LINES, help="scrollback lines per pane (default: 200, max: 2000)")
    parser.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT, help="timeout per tmux/router request in seconds (default: 5)")
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.lines < 1 or args.lines > MAX_SCROLLBACK_LINES:
        print(f"Error: --lines must be between 1 and {MAX_SCROLLBACK_LINES}", file=sys.stderr)
        return 2
    if args.timeout <= 0:
        print("Error: --timeout must be greater than zero", file=sys.stderr)
        return 2
    window_ids = [*args.window_ids, *args.window_options]
    if not window_ids:
        print("Error: at least one tmux window name is required", file=sys.stderr)
        return 2
    try:
        report = run_check(window_ids, args.router_url, args.lines, args.timeout)
    except CheckerError as error:
        print(f"Error: {error}", file=sys.stderr)
        return 2
    print(json.dumps(report, ensure_ascii=False, sort_keys=True))
    if report["errors"]:
        return 2
    return 1 if report["findings"] else 0


if __name__ == "__main__":
    raise SystemExit(main())
