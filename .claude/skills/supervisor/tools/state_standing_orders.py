#!/usr/bin/env python3
"""Manage standing orders in the supervisor state.

Standing orders are persistent directives that survive across tasks and
sessions. The watchdog reads them every cycle to evaluate whether the
supervisor is honoring them.

Usage:
  python state_standing_orders.py list
  python state_standing_orders.py add --title "..." --description "..." [--priority high|normal]
  python state_standing_orders.py remove <order-id>
  python state_standing_orders.py show <order-id>
  python state_standing_orders.py edit <order-id> --title "..." --description "..."
"""

import argparse
import sys
import os

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import (
    load_state,
    locked_state,
    add_standing_order,
    remove_standing_order,
    find_standing_order,
)


def cmd_list(args):
    state = load_state()
    orders = state.get("standing_orders", [])
    if not orders:
        print("No standing orders.")
        return
    for o in orders:
        priority = f" [{o['priority'].upper()}]" if o.get("priority", "normal") != "normal" else ""
        print(f"{o['id']}{priority}: {o['title']}")
        print(f"  {o['description']}")
        print()


def cmd_add(args):
    with locked_state() as state:
        order = add_standing_order(
            state,
            title=args.title,
            description=args.description,
            priority=args.priority or "normal",
        )
    print(f"Added standing order {order['id']}: {order['title']}")


def cmd_remove(args):
    with locked_state() as state:
        removed = remove_standing_order(state, args.order_id)
    if removed:
        print(f"Removed standing order {args.order_id}")
    else:
        print(f"Error: standing order '{args.order_id}' not found.", file=sys.stderr)
        sys.exit(1)


def cmd_show(args):
    state = load_state()
    order = find_standing_order(state, args.order_id)
    if not order:
        print(f"Error: standing order '{args.order_id}' not found.", file=sys.stderr)
        sys.exit(1)
    for k, v in order.items():
        print(f"  {k}: {v}")


def cmd_edit(args):
    with locked_state() as state:
        order = find_standing_order(state, args.order_id)
        if not order:
            print(f"Error: standing order '{args.order_id}' not found.", file=sys.stderr)
            sys.exit(1)
        if args.title:
            order["title"] = args.title
        if args.description:
            order["description"] = args.description
        if args.priority:
            order["priority"] = args.priority
    print(f"Updated standing order {args.order_id}")


def main():
    parser = argparse.ArgumentParser(description="Manage standing orders")
    sub = parser.add_subparsers(dest="command")

    sub.add_parser("list", help="List all standing orders")

    p_add = sub.add_parser("add", help="Add a standing order")
    p_add.add_argument("--title", required=True)
    p_add.add_argument("--description", required=True)
    p_add.add_argument("--priority", choices=["normal", "high"], default="normal")

    p_rm = sub.add_parser("remove", help="Remove a standing order")
    p_rm.add_argument("order_id")

    p_show = sub.add_parser("show", help="Show a standing order")
    p_show.add_argument("order_id")

    p_edit = sub.add_parser("edit", help="Edit a standing order")
    p_edit.add_argument("order_id")
    p_edit.add_argument("--title")
    p_edit.add_argument("--description")
    p_edit.add_argument("--priority", choices=["normal", "high"])

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    {"list": cmd_list, "add": cmd_add, "remove": cmd_remove, "show": cmd_show, "edit": cmd_edit}[args.command](args)


if __name__ == "__main__":
    main()
