#!/usr/bin/env python3
"""Read and display the full supervisor state.

Usage: python state_read.py [--json]

  No args   Print the full state in human-readable markdown format.
  --json    Print the raw JSON.
"""

import json
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state, render_markdown, find_task


def main():
    if len(sys.argv) > 2:
        print(f"Usage: {sys.argv[0]} [--json]", file=sys.stderr)
        sys.exit(1)

    json_mode = False

    if len(sys.argv) == 2:
        if sys.argv[1] == "--json":
            json_mode = True
        else:
            print(f"Usage: {sys.argv[0]} [--json]", file=sys.stderr)
            sys.exit(1)

    state = load_state()

    if json_mode:
        print(json.dumps(state, indent=2))
    else:
        print(render_markdown(state))


if __name__ == "__main__":
    main()
