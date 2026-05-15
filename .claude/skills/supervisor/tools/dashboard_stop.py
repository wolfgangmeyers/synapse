#!/usr/bin/env python3
"""Stop the supervisor dashboard.

Usage:
  python dashboard_stop.py

Kills the 'dashboard' window in the mecha session if it exists.
"""

import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _tmux import kill_window, window_exists

WINDOW = "dashboard"


def main():
    if window_exists(WINDOW):
        kill_window(WINDOW)
        print(f"Dashboard stopped (killed window '{WINDOW}').")
    else:
        print("Dashboard is not running.")


if __name__ == "__main__":
    main()
