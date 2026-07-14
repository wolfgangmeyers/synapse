#!/usr/bin/env python3
"""Post one durable local Mailbox v2 agent message."""

from __future__ import annotations

import argparse
import errno
import json
import os
import secrets
from datetime import datetime, timezone
from pathlib import Path
import subprocess
import sys
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener

from _tmux import current_agent_id


DEFAULT_ROUTER_URL = "http://127.0.0.1:8765"
DEFAULT_LAUNCHD_LABEL = "life.meyers.mecha-router"
LOOPBACK_HOSTS = {"127.0.0.1", "::1", "localhost"}
MAX_RESPONSE_BYTES = 512 * 1024
NANOID_ALPHABET = "_-0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"


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
    result.add_argument("--from", dest="sender", help="Explicit sender agent ID")
    result.add_argument(
        "--router-url",
        default=os.environ.get("MECHA_ROUTER_URL", DEFAULT_ROUTER_URL),
        help="Loopback router URL (default: %(default)s)",
    )
    return result


def fail(message: str, code: int = 2) -> int:
    print(message, file=sys.stderr)
    return code


def resolve_sender(explicit: str | None) -> str:
    if explicit:
        return explicit
    sender = current_agent_id()
    if sender is None:
        raise ValueError("Missing sender identity: pass --from or run from a tmux window with a valid name.")
    return sender


def router_endpoint(router_url: str, path: str) -> str:
    parsed = urlparse(router_url)
    if parsed.scheme not in {"http", "https"} or parsed.hostname not in LOOPBACK_HOSTS:
        raise ValueError("--router-url must use a loopback http(s) address.")
    return f"{router_url.rstrip('/')}{path}"


def post_json(url: str, payload: dict[str, Any]) -> tuple[int, dict[str, Any]]:
    request = Request(
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"content-type": "application/json"},
        method="POST",
    )
    try:
        with build_opener(NoRedirect()).open(request, timeout=5) as response:
            return response.status, read_json_response(response)
    except HTTPError as error:
        return error.code, read_json_response(error)


def connection_refused(error: URLError) -> bool:
    reason = error.reason
    return isinstance(reason, ConnectionRefusedError) or getattr(reason, "errno", None) == errno.ECONNREFUSED


def kickstart_once() -> str | None:
    command = [
        sys.executable,
        str(Path(__file__).with_name("agent_router.py")),
        "kickstart",
        "--label",
        os.environ.get("MECHA_ROUTER_LAUNCHD_LABEL", DEFAULT_LAUNCHD_LABEL),
    ]
    try:
        result = subprocess.run(command, check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    except OSError as error:
        return f"could not run launchctl: {error.strerror or error}"
    if result.returncode != 0:
        return f"exit status {result.returncode}"
    return None


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if "@" in args.to:
        return fail("mailbox_send.py only sends to agent IDs; use mailbox_email.py --to <address> ... for email.")
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
        status, response = post_json(endpoint, envelope)
    except ValueError as error:
        return fail(f"Router response invalid: {error}", 1)
    except URLError as error:
        if not connection_refused(error):
            return fail(f"Router unavailable: {error.reason}", 1)
        kickstart_error = kickstart_once()
        if kickstart_error is not None:
            print(f"Router kickstart failed ({kickstart_error}); retrying once.", file=sys.stderr)
        try:
            status, response = post_json(endpoint, envelope)
        except ValueError as retry_error:
            return fail(f"Router response invalid after one kickstart retry: {retry_error}", 1)
        except URLError as retry_error:
            return fail(f"Router unavailable after one kickstart retry: {retry_error.reason}", 1)

    if status not in {200, 201}:
        error = response.get("error", {})
        return fail(f"Router rejected message: {error.get('code', 'unknown_error')} — {error.get('message', 'unknown error')}", 1)
    message = response.get("message")
    if (not isinstance(message, dict)
            or message.get("id") != envelope["id"]
            or not isinstance(message.get("state"), str)
            or not message["state"]):
        return fail("Router returned an invalid acceptance response.", 1)
    print(f"Message {message['id']} accepted: {message['state']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
