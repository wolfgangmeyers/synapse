#!/usr/bin/env python3
"""PostToolUse hook: schedule a delayed recall check for the current window."""

import json
import os
import sys
import traceback

from recall_lib import increment_message_count, load_config, schedule_window


def get_current_window():
    return os.environ.get("MECHA_WORKER_WINDOW") or os.environ.get("MECHA_AGENT_WINDOW")


def main():
    try:
        json.load(sys.stdin)
    except (json.JSONDecodeError, ValueError):
        pass

    window = get_current_window()
    if not window:
        sys.exit(0)

    try:
        config = load_config()
        if not config["enabled"]:
            sys.exit(0)
        if not config.get("stores"):
            sys.exit(0)
        increment_message_count(window)
        schedule_window(window, config["delay_seconds"])
    except Exception:
        traceback.print_exc(file=sys.stderr)

    sys.exit(0)


if __name__ == "__main__":
    main()
