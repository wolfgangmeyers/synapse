#!/usr/bin/env python3
"""Submit target-worker native Sentinel work through the loopback router API."""

import argparse
from datetime import datetime, timezone
import json
import os
from urllib.error import HTTPError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener
from uuid import uuid4

LOOPBACK = {"127.0.0.1", "::1"}
DEFAULT_ROUTER = os.environ.get("MECHA_ROUTER_URL", "http://127.0.0.1:8765")


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        return None


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--router", default=DEFAULT_ROUTER)
    parser.add_argument("--from", dest="sender", default="sentinel")
    parser.add_argument("--to", required=True)
    parser.add_argument("--prompt", required=True)
    parser.add_argument("--model", required=True)
    parser.add_argument("--subject")
    parser.add_argument("--id", default=None)
    parser.add_argument("--next-due-at")
    parser.add_argument("--interval-ms", type=int)
    args = parser.parse_args()
    if args.interval_ms is not None and args.next_due_at is None:
        parser.error("--interval-ms requires --next-due-at")
    if args.interval_ms is not None and args.interval_ms <= 0:
        parser.error("--interval-ms must be positive")
    if args.id is None:
        args.id = f"sentinel-{uuid4()}"
    return args


def validate_router(url):
    parsed = urlparse(url)
    if (parsed.scheme not in {"http", "https"} or parsed.hostname not in LOOPBACK
            or parsed.username or parsed.password or parsed.query or parsed.fragment
            or parsed.path not in {"", "/"}):
        raise SystemExit("--router must be an exact loopback HTTP(S) base URL")
    return url.rstrip("/")


def now():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def main():
    args = parse_args()
    router = validate_router(args.router)
    sentinel = {"model": args.model}
    if args.next_due_at is None:
        endpoint = "/v1/messages"
        payload = {"id": args.id, "from": args.sender, "to": args.to, "body": args.prompt,
                   "created_at": now(), "sentinel": sentinel}
        if args.subject is not None:
            payload["subject"] = args.subject
    else:
        endpoint = "/v1/reminders"
        payload = {"id": args.id, "creator": args.sender, "target": args.to, "body": args.prompt,
                   "created_at": now(), "next_due_at": args.next_due_at,
                   "interval_ms": args.interval_ms, "sentinel": sentinel}
    request = Request(f"{router}{endpoint}", data=json.dumps(payload).encode(), method="POST",
                      headers={"content-type": "application/json"})
    try:
        with build_opener(NoRedirect).open(request) as response:
            print(response.read().decode())
    except HTTPError as error:
        raise SystemExit(f"router rejected Sentinel dispatch ({error.code})")


if __name__ == "__main__":
    main()
