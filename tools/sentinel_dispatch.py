#!/usr/bin/env python3
"""Submit fixed-profile Sentinel work through the loopback router API."""

import argparse
import json
from pathlib import Path
from urllib.error import HTTPError
from urllib.parse import urlparse
from urllib.request import HTTPRedirectHandler, Request, build_opener

TEMPLATES = Path(__file__).resolve().parents[1] / ".claude" / "skills" / "supervisor" / "templates"
LOOPBACK = {"127.0.0.1", "::1"}


class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *_args, **_kwargs):
        return None


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--router", required=True)
    parser.add_argument("--policy-file", required=True)
    parser.add_argument("--from", dest="sender", required=True)
    parser.add_argument("--to", required=True)
    parser.add_argument("--subject")
    parser.add_argument("--policy", required=True)
    parser.add_argument("--model", required=True)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--prompt")
    source.add_argument("--template")
    parser.add_argument("--id", required=True)
    parser.add_argument("--next-due-at")
    parser.add_argument("--interval-ms", type=int)
    args = parser.parse_args()
    if args.interval_ms is not None and args.next_due_at is None:
        parser.error("--interval-ms requires --next-due-at")
    if args.interval_ms is not None and args.interval_ms <= 0:
        parser.error("--interval-ms must be positive")
    return args


def validate_router(url):
    parsed = urlparse(url)
    if parsed.scheme not in {"http", "https"} or parsed.hostname not in LOOPBACK or parsed.username or parsed.password or parsed.query or parsed.fragment or parsed.path not in {"", "/"}:
        raise SystemExit("--router must be an exact loopback HTTP(S) base URL")
    return url.rstrip("/")


def policy_for(args):
    try:
        policies = json.loads(Path(args.policy_file).read_text(encoding="utf-8"))
        policy = policies[args.policy]
    except (OSError, ValueError, KeyError, TypeError):
        raise SystemExit("invalid local Sentinel policy file")
    if (policy.get("receiver") != args.to
            or args.sender not in policy.get("senders", [])
            or args.model not in policy.get("models", [])
            or policy.get("execution", {}).get("supervisor") != args.sender
            or policy.get("execution", {}).get("tools") != []):
        raise SystemExit("local Sentinel policy does not authorize this dispatch")


def prompt_for(args):
    if args.prompt is not None:
        return args.prompt
    path = (TEMPLATES / f"{args.template}.md").resolve()
    if path.parent != TEMPLATES or not path.is_file():
        raise SystemExit("unknown Sentinel template")
    return path.read_text(encoding="utf-8")


def main():
    args = parse_args()
    router = validate_router(args.router)
    policy_for(args)
    body = prompt_for(args)
    sentinel = {"receiver_policy": args.policy, "model": args.model}
    if args.next_due_at is None:
        endpoint = "/v1/messages"
        payload = {"id": args.id, "from": args.sender, "to": args.to, "body": body, "created_at": "2030-01-01T00:00:00.000Z", "sentinel": sentinel}
        if args.subject is not None:
            payload["subject"] = args.subject
    else:
        endpoint = "/v1/reminders"
        payload = {"id": args.id, "creator": args.sender, "target": args.to, "body": body, "created_at": "2030-01-01T00:00:00.000Z", "next_due_at": args.next_due_at, "interval_ms": args.interval_ms, "sentinel": sentinel}
    request = Request(f"{router}{endpoint}", data=json.dumps(payload).encode(), method="POST", headers={"content-type": "application/json"})
    try:
        with build_opener(NoRedirect).open(request) as response:
            print(response.read().decode())
    except HTTPError as error:
        raise SystemExit(f"router rejected Sentinel dispatch ({error.code})")


if __name__ == "__main__":
    main()
