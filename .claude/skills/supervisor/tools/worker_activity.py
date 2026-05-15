#!/usr/bin/env python3
"""Get activity timestamps for all tmux windows at once.

Returns JSON mapping window names to their last activity timestamps.
This is more efficient than querying windows individually.

Usage:
  python worker_activity.py

Output:
  {
    "window-name": 1739655216,  # Unix timestamp
    "another-window": 1739655180,
    ...
  }
"""

import json
import subprocess
import sys


def main():
    # Get all windows with their activity timestamps in one call
    result = subprocess.run(
        ["tmux", "list-windows", "-a", "-F", "#{window_name}\t#{window_activity}"],
        capture_output=True,
        text=True,
        check=True,
    )

    activity_map = {}
    for line in result.stdout.splitlines():
        if not line.strip():
            continue
        parts = line.split("\t")
        if len(parts) != 2:
            continue
        window_name, timestamp = parts
        try:
            activity_map[window_name] = int(timestamp)
        except ValueError:
            # Skip windows with invalid timestamps
            continue

    print(json.dumps(activity_map, indent=2))


if __name__ == "__main__":
    main()
