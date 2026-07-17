#!/usr/bin/env python3
"""Verify one Sentinel child relay from closed, body-free router audit events."""

import argparse
import json
from pathlib import Path
import time


ENQUEUE_FIELDS = {
    "timestamp", "event", "message_id", "from", "to", "agent_id", "state", "created",
    "sentinel_child_occurrence_id",
}
OFFER_FIELDS = {
    "timestamp", "event", "message_id", "agent_id", "attempt", "lease_id", "delivery_expires_at",
}


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--router-log", type=Path, required=True)
    parser.add_argument("--occurrence-id", required=True)
    parser.add_argument("--from", dest="sender", required=True)
    parser.add_argument("--to", required=True)
    parser.add_argument("--timeout-seconds", type=float, default=15)
    parser.add_argument("--post-offer-grace-seconds", type=float, default=1)
    return parser.parse_args()


def closed_enqueue(event, args):
    return (set(event) == ENQUEUE_FIELDS
            and isinstance(event["timestamp"], str)
            and event["event"] == "message_enqueued"
            and isinstance(event["message_id"], str)
            and event["from"] == args.sender
            and event["to"] == args.to
            and event["agent_id"] == args.to
            and event["state"] == "queued"
            and event["created"] is True
            and event["sentinel_child_occurrence_id"] == args.occurrence_id)


def closed_offer(event, result_id, args):
    return (set(event) == OFFER_FIELDS
            and isinstance(event["timestamp"], str)
            and event["event"] == "delivery_offered"
            and event["message_id"] == result_id
            and event["agent_id"] == args.to
            and isinstance(event["attempt"], int) and event["attempt"] >= 1
            and isinstance(event["lease_id"], str)
            and isinstance(event["delivery_expires_at"], str))


def main():
    args = parse_args()
    if args.timeout_seconds <= 0 or args.post_offer_grace_seconds < 0:
        raise SystemExit("timeout values must be non-negative and --timeout-seconds must be positive")
    deadline = time.monotonic() + args.timeout_seconds
    offer_deadline = None
    offset = 0
    result_id = None
    relay_count = 0
    offered = False
    while time.monotonic() < deadline:
        try:
            with args.router_log.open("r", encoding="utf-8") as log:
                log.seek(offset)
                lines = log.readlines()
                offset = log.tell()
        except FileNotFoundError:
            lines = []
        for line in lines:
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not isinstance(event, dict):
                continue
            if event.get("event") == "message_enqueued" and event.get("sentinel_child_occurrence_id") == args.occurrence_id:
                if not closed_enqueue(event, args):
                    raise SystemExit("invalid body-free Sentinel relay audit")
                relay_count += 1
                if relay_count != 1:
                    raise SystemExit("duplicate Sentinel relay audit")
                result_id = event["message_id"]
            if result_id is not None and event.get("event") == "delivery_offered" and event.get("message_id") == result_id:
                if not closed_offer(event, result_id, args):
                    raise SystemExit("invalid body-free Sentinel delivery audit")
                offered = True
                offer_deadline = time.monotonic() + args.post_offer_grace_seconds
            if result_id is not None and event.get("event") == "delivery_acknowledged" and event.get("message_id") == result_id:
                raise SystemExit("Sentinel relay was acknowledged before proof completed")
        if offered and offer_deadline is not None and time.monotonic() >= offer_deadline:
            print(json.dumps({"occurrence_id": args.occurrence_id, "fixed_recipient": True, "relay_count": 1, "delivery_offered": True, "body_absent": True}, separators=(",", ":")))
            return
        time.sleep(0.05)
    raise SystemExit("timed out waiting for body-free Sentinel relay audit")


if __name__ == "__main__":
    main()
