#!/usr/bin/env python3
"""Verify one Sentinel child relay from body-free router audit events."""

import argparse
import json
from pathlib import Path
import time


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--router-log", type=Path, required=True)
    parser.add_argument("--occurrence-id", required=True)
    parser.add_argument("--from", dest="sender", required=True)
    parser.add_argument("--to", required=True)
    parser.add_argument("--timeout-seconds", type=float, default=15)
    return parser.parse_args()


def main():
    args = parse_args()
    if args.timeout_seconds <= 0:
        raise SystemExit("--timeout-seconds must be positive")
    deadline = time.monotonic() + args.timeout_seconds
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
            if (event.get("event") == "message_enqueued"
                    and event.get("sentinel_child_occurrence_id") == args.occurrence_id):
                if event.get("from") != args.sender or event.get("to") != args.to or event.get("created") is not True:
                    raise SystemExit("invalid fixed-recipient Sentinel relay audit")
                relay_count += 1
                if relay_count != 1:
                    raise SystemExit("duplicate Sentinel relay audit")
                result_id = event.get("message_id")
            if result_id is not None and event.get("event") == "delivery_offered" and event.get("message_id") == result_id and event.get("agent_id") == args.to:
                offered = True
        if result_id is not None and offered:
            print(json.dumps({"occurrence_id": args.occurrence_id, "fixed_recipient": True, "relay_count": 1, "delivery_offered": True, "body_absent": True}, separators=(",", ":")))
            return
        time.sleep(0.1)
    raise SystemExit("timed out waiting for body-free Sentinel relay audit")


if __name__ == "__main__":
    main()
