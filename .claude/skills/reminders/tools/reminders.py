#!/usr/bin/env python3
"""Manage Mailbox v2 reminders through the local router."""

from __future__ import annotations

import argparse
from datetime import datetime, timedelta, timezone
import json
import os
import re
import sys
from typing import Any
from urllib.error import URLError
from urllib.parse import quote
from uuid import uuid4

from pathlib import Path

SUPERVISOR_TOOLS = Path(__file__).resolve().parents[2] / "supervisor" / "tools"
sys.path.insert(0, str(SUPERVISOR_TOOLS))
from agent_router import request_json, router_url
from mailbox_send import post_json, router_endpoint
from _tmux import current_agent_id


SELF_ALIASES = {"self", "me", "current"}
MAX_SAFE_INTEGER = (1 << 53) - 1
PR_WATCH_REMINDER_ID = "pr-watch-wakeup-v1"
PR_WATCH_TARGET = "pr-watch"
PR_WATCH_CREATOR = "pr-watch"
PR_WATCH_SUBJECT = "PR watch wakeup"
PR_WATCH_BODY = "Run one PR watch scan now."
PR_WATCH_INTERVAL_MS = 15 * 60 * 1_000


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser(description=__doc__)
    commands = result.add_subparsers(dest="command", required=True)

    def add_router_url(command: argparse.ArgumentParser) -> None:
        command.add_argument(
            "--router-url",
            type=router_url,
            default=os.environ.get("MECHA_ROUTER_URL", "http://127.0.0.1:8765"),
        )

    create = commands.add_parser("set")
    create.add_argument("--window", required=True)
    create.add_argument("--message", required=True)
    create.add_argument("--subject")
    create.add_argument("--from", dest="creator")
    timing = create.add_mutually_exclusive_group(required=True)
    timing.add_argument("--in", dest="relative_duration", metavar="DURATION")
    timing.add_argument("--at", dest="absolute_time", metavar="ISO8601")
    timing.add_argument("--every", dest="recurrence", metavar="DURATION")
    add_router_url(create)

    listing = commands.add_parser("list")
    add_router_url(listing)
    delete = commands.add_parser("delete")
    delete.add_argument("id")
    add_router_url(delete)
    delete_all = commands.add_parser("delete-all")
    add_router_url(delete_all)
    reconcile_pr_watch = commands.add_parser(
        "reconcile-pr-watch",
        help="Safely ensure the dedicated pr-watch recurrence exists exactly once",
    )
    add_router_url(reconcile_pr_watch)
    return result


def parse_duration(value: str) -> int:
    match = re.fullmatch(r"(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?", value.strip())
    if match is None or not any(match.groups()):
        raise ValueError("duration must use positive h/m/s components such as 10m or 1h30m")
    milliseconds = (
        int(match.group(1) or 0) * 60 * 60 * 1_000
        + int(match.group(2) or 0) * 60 * 1_000
        + int(match.group(3) or 0) * 1_000
    )
    if milliseconds <= 0:
        raise ValueError("duration must be positive")
    if milliseconds > MAX_SAFE_INTEGER:
        raise ValueError("duration exceeds the router's safe millisecond range")
    return milliseconds


def due_after(now: datetime, duration_ms: int) -> datetime:
    try:
        return now + timedelta(milliseconds=duration_ms)
    except OverflowError as error:
        raise ValueError("duration is outside the supported timestamp range") from error


def parse_absolute_time(value: str) -> datetime:
    try:
        parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as error:
        raise ValueError("--at must be an ISO 8601 datetime with an offset or timezone") from error
    if parsed.tzinfo is None or parsed.utcoffset() is None:
        raise ValueError("--at must include an explicit timezone offset or Z")
    return parsed.astimezone(timezone.utc)


def utc_timestamp(value: datetime) -> str:
    return value.astimezone(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def current_agent_identity() -> str:
    identity = current_agent_id()
    if identity is None:
        raise ValueError("self requires a valid current tmux window name")
    return identity


def resolve_target(value: str) -> str:
    return current_agent_identity() if value in SELF_ALIASES else value


def response_error(response: dict[str, Any]) -> str:
    error = response.get("error", {})
    return f"{error.get('code', 'unknown_error')} — {error.get('message', 'unknown error')}"


def accepted_reminder(response: dict[str, Any], expected: dict[str, Any]) -> dict[str, Any] | None:
    reminder = response.get("reminder")
    if not isinstance(reminder, dict):
        return None
    if (reminder.get("id") != expected["id"]
            or reminder.get("target") != expected["target"]
            or not isinstance(reminder.get("next_due_at"), str)
            or not isinstance(reminder.get("state"), str)):
        return None
    return reminder


def set_reminder(args: argparse.Namespace) -> int:
    try:
        now = datetime.now(timezone.utc)
        target = resolve_target(args.window)
        creator = args.creator or current_agent_identity()
        if args.relative_duration is not None:
            due_at = due_after(now, parse_duration(args.relative_duration))
            interval_ms = None
        elif args.absolute_time is not None:
            due_at = parse_absolute_time(args.absolute_time)
            interval_ms = None
        else:
            interval_ms = parse_duration(args.recurrence)
            due_at = due_after(now, interval_ms)
        payload = {
            "id": str(uuid4()),
            "target": target,
            "creator": creator,
            "body": args.message,
            "created_at": utc_timestamp(now),
            "next_due_at": utc_timestamp(due_at),
            "interval_ms": interval_ms,
        }
        if args.subject is not None:
            payload["subject"] = args.subject
        status, response = post_json(router_endpoint(args.router_url, "/v1/reminders"), payload)
    except (ValueError, URLError) as error:
        print(str(error), file=sys.stderr)
        return 2
    reminder = accepted_reminder(response, payload)
    if status not in {200, 201} or reminder is None:
        print(f"Router rejected reminder: {response_error(response)}", file=sys.stderr)
        return 1
    print(
        f"Reminder {reminder['id']} is {reminder['state']} "
        f"for {reminder['target']} at {reminder['next_due_at']}",
    )
    return 0


def list_reminders(args: argparse.Namespace) -> int:
    try:
        status, response = request_json(f"{args.router_url}/v1/reminders")
    except (ValueError, URLError) as error:
        print(str(error), file=sys.stderr)
        return 1
    reminders = response.get("reminders") if isinstance(response, dict) else None
    if status != 200 or not isinstance(reminders, list):
        print(f"Router rejected reminder list: {response_error(response or {})}", file=sys.stderr)
        return 1
    print(json.dumps(reminders, sort_keys=True))
    return 0


def list_router_reminders(router: str) -> list[dict[str, Any]]:
    status, response = request_json(f"{router}/v1/reminders")
    reminders = response.get("reminders") if isinstance(response, dict) else None
    if status != 200 or not isinstance(reminders, list) or not all(isinstance(item, dict) for item in reminders):
        raise ValueError(f"Router rejected reminder list: {response_error(response or {})}")
    return reminders


def pr_watch_owned(reminder: dict[str, Any]) -> bool:
    return (
        reminder.get("target") == PR_WATCH_TARGET
        and reminder.get("creator") == PR_WATCH_CREATOR
        and reminder.get("subject") == PR_WATCH_SUBJECT
    )


def pr_watch_conforming(reminder: dict[str, Any]) -> bool:
    return (
        pr_watch_owned(reminder)
        and reminder.get("interval_ms") == PR_WATCH_INTERVAL_MS
        and reminder.get("state") == "scheduled"
    )


def delete_router_reminder(router: str, reminder_id: str) -> None:
    status, response = request_json(f"{router}/v1/reminders/{quote(reminder_id, safe='')}", "DELETE")
    if status != 204:
        raise ValueError(f"Router rejected reminder cancellation: {response_error(response or {})}")


def reconcile_pr_watch(args: argparse.Namespace) -> int:
    """Ensure one owned scheduled recurrence without racing a second service."""
    try:
        reminders = list_router_reminders(args.router_url)
        owned = [reminder for reminder in reminders if pr_watch_owned(reminder)]

        # A linked/due reminder cannot safely be canceled. Do not create another
        # recurrence while it exists; a later startup will reconcile it.
        if any(reminder.get("state") != "scheduled" for reminder in owned):
            raise ValueError("PR watch recurrence is active or linked; refusing to create another")

        conforming = [reminder for reminder in owned if pr_watch_conforming(reminder)]
        if len(conforming) == 1 and len(owned) == 1:
            print(f"PR watch recurrence is already scheduled: {conforming[0]['id']}")
            return 0

        # Retain one valid recurrence when present; otherwise remove only this
        # service's stale scheduled records before creating the stable ID below.
        keep_id = sorted((reminder["id"] for reminder in conforming), key=str)[0] if conforming else None
        for reminder in owned:
            if reminder.get("id") != keep_id:
                delete_router_reminder(args.router_url, reminder["id"])

        reminders = list_router_reminders(args.router_url)
        owned = [reminder for reminder in reminders if pr_watch_owned(reminder)]
        if any(reminder.get("state") != "scheduled" for reminder in owned):
            raise ValueError("PR watch recurrence changed while reconciling; refusing to create another")
        conforming = [reminder for reminder in owned if pr_watch_conforming(reminder)]
        if len(conforming) == 1 and len(owned) == 1:
            print(f"PR watch recurrence is scheduled: {conforming[0]['id']}")
            return 0
        if owned:
            raise ValueError("PR watch recurrence changed while reconciling; refusing to create another")

        now = datetime.now(timezone.utc)
        payload = {
            "id": PR_WATCH_REMINDER_ID,
            "target": PR_WATCH_TARGET,
            "creator": PR_WATCH_CREATOR,
            "subject": PR_WATCH_SUBJECT,
            "body": PR_WATCH_BODY,
            "created_at": utc_timestamp(now),
            "next_due_at": utc_timestamp(due_after(now, PR_WATCH_INTERVAL_MS)),
            "interval_ms": PR_WATCH_INTERVAL_MS,
        }
        status, response = post_json(router_endpoint(args.router_url, "/v1/reminders"), payload)
        if status not in {200, 201}:
            # The deterministic ID can conflict only with another concurrent
            # reconciler. Verify its result instead of inventing a second ID.
            reminders = list_router_reminders(args.router_url)
            conforming = [reminder for reminder in reminders if pr_watch_conforming(reminder)]
            owned = [reminder for reminder in reminders if pr_watch_owned(reminder)]
            if len(conforming) == 1 and len(owned) == 1:
                print(f"PR watch recurrence is scheduled: {conforming[0]['id']}")
                return 0
            raise ValueError(f"Router rejected PR watch recurrence: {response_error(response)}")
        reminder = accepted_reminder(response, payload)
        if reminder is None or not pr_watch_conforming(reminder):
            raise ValueError("Router returned an invalid PR watch recurrence")
        print(f"PR watch recurrence is scheduled: {reminder['id']}")
        return 0
    except (ValueError, URLError) as error:
        print(str(error), file=sys.stderr)
        return 1


def delete_reminder(args: argparse.Namespace, delete_all: bool) -> int:
    path = "/v1/reminders" if delete_all else f"/v1/reminders/{quote(args.id, safe='')}"
    try:
        status, response = request_json(f"{args.router_url}{path}", "DELETE")
    except (ValueError, URLError) as error:
        print(str(error), file=sys.stderr)
        return 1
    if delete_all and status == 200:
        canceled = response.get("canceled") if isinstance(response, dict) else None
        if isinstance(canceled, int) and canceled >= 0:
            print(f"Canceled {canceled} reminder(s).")
            return 0
    if not delete_all and status == 204:
        print(f"Canceled {args.id}.")
        return 0
    print(f"Router rejected reminder cancellation: {response_error(response or {})}", file=sys.stderr)
    return 1


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    if args.command == "set":
        return set_reminder(args)
    if args.command == "list":
        return list_reminders(args)
    if args.command == "reconcile-pr-watch":
        return reconcile_pr_watch(args)
    return delete_reminder(args, args.command == "delete-all")


if __name__ == "__main__":
    raise SystemExit(main())
