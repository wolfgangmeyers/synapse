#!/usr/bin/env python3
"""Activate or deactivate recall stores for a specific tmux window.

Usage:
    recall_activate.py --window W --store S              # Activate a store
    recall_activate.py --window W --store S --deactivate # Deactivate a store
    recall_activate.py --window W --list                 # List active stores
"""

import argparse
import sys

from recall_lib import (
    activate_store,
    deactivate_store,
    get_active_stores_for_window,
    load_config,
)


def cmd_activate(args):
    config = load_config()
    store_names = [s["name"] for s in config["stores"]]
    if args.store not in store_names:
        print(f"Unknown store: {args.store}. Configured stores: {', '.join(store_names) or 'none'}", file=sys.stderr)
        sys.exit(1)
    if args.deactivate:
        removed = deactivate_store(args.window, args.store)
        if removed:
            print(f"Deactivated store '{args.store}' for window '{args.window}'.")
        else:
            print(f"Store '{args.store}' was not active for window '{args.window}'.")
    else:
        added = activate_store(args.window, args.store)
        if added:
            print(f"Activated store '{args.store}' for window '{args.window}'.")
        else:
            print(f"Store '{args.store}' already active for window '{args.window}'.")


def cmd_list(args):
    active = get_active_stores_for_window(args.window)
    if active is None or not active:
        print(f"No stores activated for window '{args.window}'.")
        return
    print(f"Active stores for window '{args.window}':")
    for name in active:
        print(f"  - {name}")


def build_parser():
    parser = argparse.ArgumentParser(description="Activate/deactivate recall stores per window")
    parser.add_argument("--window", required=True, help="tmux window name")

    group = parser.add_mutually_exclusive_group(required=True)
    group.add_argument("--store", help="Store name to activate/deactivate")
    group.add_argument("--list", action="store_true", help="List active stores")

    parser.add_argument("--deactivate", action="store_true", help="Deactivate instead of activate")
    return parser


def main():
    args = build_parser().parse_args()
    if args.list:
        cmd_list(args)
    else:
        cmd_activate(args)


if __name__ == "__main__":
    main()
