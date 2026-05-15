#!/usr/bin/env python3
"""Start the supervisor dashboard in a tmux window.

Usage:
  python dashboard_start.py

Checks if the dashboard is already running. If not, creates a 'dashboard'
window in the mecha session and starts the responsibilities server.

The dashboard auto-refreshes and shows all tasks (blocked/active/completed)
at http://localhost:7777
"""

import os
import sys
import time
import urllib.request
import urllib.error

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import ensure_session, new_window, send_text, send_enter, window_exists

WINDOW = "dashboard"
URL = "http://localhost:7777"


def check_running():
    """Return True if the dashboard HTTP server is responding."""
    try:
        urllib.request.urlopen(URL, timeout=3)
        return True
    except (urllib.error.URLError, OSError):
        return False


def main():
    if window_exists(WINDOW):
        print(f"Dashboard already running at {URL}")
        sys.exit(0)

    ensure_session()
    new_window(WINDOW)

    cmd = (
        "cd ~/code/mecha-wolfgang"
        " && source tools/.venv/bin/activate"
        " && python tools/responsibilities-server.py"
    )
    send_text(WINDOW, cmd)
    time.sleep(0.5)
    send_enter(WINDOW)

    time.sleep(2)

    if check_running():
        print(f"Dashboard started at {URL}")
    else:
        print(f"Dashboard window created, but server not yet responding at {URL}")
        print("It may still be starting up. Check with:")
        print(f"  python3 ~/.claude/skills/supervisor/tools/worker_capture.py --window {WINDOW}")


if __name__ == "__main__":
    main()
