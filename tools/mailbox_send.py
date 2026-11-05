#!/usr/bin/env python3
"""Post one durable Mailbox v2 agent message to an explicit router."""

from __future__ import annotations

import argparse
import json
import os
import re
import secrets
import subprocess
import tempfile
from datetime import datetime, timezone
import sys
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener

DEFAULT_ROUTER_URL = "http://127.0.0.1:8765"
MAX_RESPONSE_BYTES = 512 * 1024
NANOID_ALPHABET = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
MESSAGE_ID_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
MESSAGE_STATES = frozenset({
    "queued",
    "leased",
    "injection_requested",
    "acknowledged",
    "dead_letter",
})


def nanoid(size: int = 21) -> str:
    return "".join(secrets.choice(NANOID_ALPHABET) for _ in range(size))


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, new_url):
        return None


def read_json_response(response: Any) -> dict[str, Any]:
    content = response.read(MAX_RESPONSE_BYTES + 1)
    if len(content) > MAX_RESPONSE_BYTES:
        raise ValueError("Router response exceeds local limits.")
    try:
        payload = json.loads(content)
    except (UnicodeDecodeError, json.JSONDecodeError):
        return {}
    return payload if isinstance(payload, dict) else {}


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    result.add_argument("--to", required=True, help="Mailbox v2 agent ID")
    result.add_argument("--body", required=True, help="Message body")
    result.add_argument("--subject", help="Optional message subject")
    result.add_argument("--from", dest="sender", help="Explicit verified sender agent ID (otherwise current tmux window)")
    result.add_argument("--managed-control-request-id", help=argparse.SUPPRESS)
    result.add_argument(
        "--router-url",
        default=os.environ.get("MECHA_ROUTER_URL", DEFAULT_ROUTER_URL),
        help="Router URL (default: %(default)s)",
    )
    return result


def fail(message: str, code: int = 2) -> int:
    print(message, file=sys.stderr)
    return code


def rejection_message(error: dict[str, Any]) -> str:
    code = error.get("code", "unknown_error")
    message = error.get("message", "unknown error")
    if code == "queue_full":
        return (
            f"Router rejected message: {code} — {message} "
            "Back off and wait for the recipient mailbox to clear before sending again."
        )
    return f"Router rejected message: {code} — {message}"


def resolve_sender(explicit: str | None) -> str:
    if explicit is not None:
        return explicit
    pane = os.environ.get("TMUX_PANE", "")
    if not os.environ.get("TMUX") or re.fullmatch(r"%\d+", pane) is None:
        raise ValueError("Missing sender identity: pass --from or run from a valid current tmux pane.")
    # Match the native extension's current-pane lookup, without legacy imports.
    # Bound both the subprocess lifetime and the bytes accepted as an identity.
    try:
        with tempfile.TemporaryFile() as output:
            subprocess.run(
                ["tmux", "display-message", "-p", "-t", pane, "#{window_name}"],
                stdin=subprocess.DEVNULL, stdout=output, stderr=subprocess.DEVNULL,
                timeout=2, check=True,
            )
            output.seek(0)
            raw = output.read(122)
        sender = raw.decode("utf-8").strip()
    except (OSError, UnicodeDecodeError, subprocess.SubprocessError) as error:
        raise ValueError("Could not resolve the current tmux sender; pass --from with a verified identity.") from error
    if len(raw) > 121 or re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,119}", sender) is None:
        raise ValueError("Current tmux window is not a valid mailbox sender.")
    return sender


def router_endpoint(router_url: str, path: str) -> str:
    parsed = urlparse(router_url)
    if (parsed.scheme not in {"http", "https"} or not parsed.hostname
            or parsed.username is not None or parsed.password is not None
            or parsed.path not in {"", "/"} or parsed.params or parsed.query or parsed.fragment):
        raise ValueError("--router-url must be an exact http(s) base URL.")
    parsed.port  # Validate malformed or out-of-range ports before I/O.
    return f"{router_url.rstrip('/')}{path}"


def post_json(url: str, payload: dict[str, Any], managed_control_request_id: str | None = None) -> tuple[int, dict[str, Any]]:
    headers = {"content-type": "application/json"}
    if managed_control_request_id is not None:
        headers["X-Mecha-Managed-Control-Request-ID"] = managed_control_request_id
    request = Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers=headers,
        method="POST",
    )
    try:
        with build_opener(NoRedirect()).open(request, timeout=5) as response:
            return response.status, read_json_response(response)
    except HTTPError as error:
        return error.code, read_json_response(error)


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if "@" in args.to:
        return fail("mailbox_send.py only sends to agent IDs.")
    try:
        sender = resolve_sender(args.sender)
        body = args.body
        endpoint = router_endpoint(args.router_url, "/v1/messages")
    except (OSError, ValueError) as error:
        return fail(str(error))

    envelope = {
        "id": nanoid(),
        "from": sender,
        "to": args.to,
        "body": body,
        "created_at": utc_now(),
    }
    if args.subject is not None:
        envelope["subject"] = args.subject

    try:
        status, response = post_json(endpoint, envelope, args.managed_control_request_id)
    except ValueError as error:
        return fail(f"Router response invalid: {error}", 1)
    except (URLError, OSError) as error:
        return fail(f"Router unavailable: {error}", 1)

    if status not in {200, 201}:
        error = response.get("error", {})
        return fail(rejection_message(error if isinstance(error, dict) else {}), 1)
    message = response.get("message")
    # A dedupe can return the existing delivery ID while conversation_message records the submitted envelope ID.
    # Validate the returned delivery record, but do not require those IDs to match.
    if (not isinstance(message, dict)
            or not isinstance(message.get("id"), str)
            or MESSAGE_ID_PATTERN.fullmatch(message["id"]) is None
            or not isinstance(message.get("state"), str)
            or message["state"] not in MESSAGE_STATES):
        return fail("Router returned an invalid acceptance response.", 1)
    print(f"Message {message['id']} accepted: {message['state']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
