#!/usr/bin/env python3
"""Small local operator CLI for a running Mailbox v2 router."""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import quote, urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener


DEFAULT_ROUTER_URL = "http://127.0.0.1:8765"
DEFAULT_LAUNCHD_LABEL = "life.meyers.mecha-router"
LOOPBACK_HOSTS = {"127.0.0.1", "::1", "localhost"}
MAX_RESPONSE_BYTES = 512 * 1024


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, new_url):
        return None


def router_url(value: str) -> str:
    parsed = urlparse(value)
    if parsed.scheme not in {"http", "https"} or parsed.hostname not in LOOPBACK_HOSTS:
        raise argparse.ArgumentTypeError("router URL must use a loopback http(s) address")
    return value.rstrip("/")


def add_router_url(command: argparse.ArgumentParser) -> None:
    command.add_argument(
        "--router-url",
        type=router_url,
        default=os.environ.get("MECHA_ROUTER_URL", DEFAULT_ROUTER_URL),
    )


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)
    for name in ("status", "doctor"):
        add_router_url(commands.add_parser(name))
    message = commands.add_parser("message")
    message.add_argument("message_id")
    add_router_url(message)
    retry = commands.add_parser("retry")
    retry.add_argument("message_id")
    add_router_url(retry)
    dismiss = commands.add_parser("dismiss")
    dismiss.add_argument("message_id")
    add_router_url(dismiss)
    kickstart = commands.add_parser("kickstart")
    kickstart.add_argument("--label", default=os.environ.get("MECHA_ROUTER_LAUNCHD_LABEL", DEFAULT_LAUNCHD_LABEL))
    return result


def read_json_response(response: Any) -> dict[str, Any] | None:
    content = response.read(MAX_RESPONSE_BYTES + 1)
    if len(content) > MAX_RESPONSE_BYTES:
        raise ValueError("Router response exceeds local limits.")
    if not content:
        return None
    try:
        payload = json.loads(content)
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise ValueError("Router response is not valid JSON.") from error
    if not isinstance(payload, dict):
        raise ValueError("Router response is not a JSON object.")
    return payload


def request_json(url: str, method: str = "GET") -> tuple[int, dict[str, Any] | None]:
    request = Request(url, method=method)
    try:
        with build_opener(NoRedirect()).open(request, timeout=5) as response:
            return response.status, read_json_response(response)
    except HTTPError as error:
        return error.code, read_json_response(error)


def print_response(status: int, payload: dict[str, Any] | None) -> int:
    if 200 <= status < 300:
        if payload is not None:
            print(json.dumps(payload, sort_keys=True))
        return 0
    error = (payload or {}).get("error", {})
    print(f"Router request failed: {error.get('code', 'unknown_error')} — {error.get('message', 'unknown error')}", file=sys.stderr)
    return 1


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if args.command == "kickstart":
        return subprocess.run(
            ["launchctl", "kickstart", f"gui/{os.getuid()}/{args.label}"],
            check=False,
        ).returncode
    try:
        if args.command in {"status", "doctor"}:
            status, payload = request_json(f"{args.router_url}/v1/health")
        else:
            path = f"/v1/messages/{quote(args.message_id, safe='')}"
            if args.command == "retry":
                path = f"{path}/retry"
                status, payload = request_json(f"{args.router_url}{path}", "POST")
            elif args.command == "dismiss":
                status, payload = request_json(f"{args.router_url}{path}", "DELETE")
            else:
                status, payload = request_json(f"{args.router_url}{path}")
    except ValueError as error:
        print(f"Router response invalid: {error}", file=sys.stderr)
        return 1
    except URLError as error:
        print(f"Router unavailable: {error.reason}", file=sys.stderr)
        return 1
    return print_response(status, payload)


if __name__ == "__main__":
    raise SystemExit(main())
