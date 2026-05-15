#!/usr/bin/env python3
"""Manage recall configuration."""

import argparse
import json
import os
import sys

from recall_lib import load_config, save_config

VALID_KEYS = {"delay_seconds", "cooldown_messages", "capture_lines", "model", "session_retention_days"}


def cmd_show(_args):
    print(json.dumps(load_config(), indent=2))


def cmd_enable(_args):
    config = load_config()
    config["enabled"] = True
    save_config(config)
    print("Recall enabled.")


def cmd_disable(_args):
    config = load_config()
    config["enabled"] = False
    save_config(config)
    print("Recall disabled.")


def cmd_set(args):
    config = load_config()
    changed = False
    for field in VALID_KEYS:
        value = getattr(args, field, None)
        if value is not None:
            config[field] = value
            changed = True
    if not changed:
        print(f"No valid keys set. Valid keys: {', '.join(sorted(VALID_KEYS))}", file=sys.stderr)
        sys.exit(1)
    save_config(config)
    print("Recall config updated.")


def cmd_add_store(args):
    config = load_config()
    config["stores"] = [s for s in config["stores"] if s["name"] != args.name]
    store = {
        "name": args.name,
        "index_path": os.path.expanduser(args.index_path),
        "base_path": os.path.expanduser(args.base_path),
    }
    if args.instructions:
        store["instructions"] = args.instructions
    config["stores"].append(store)
    save_config(config)
    print(f"Store saved: {args.name}")


def cmd_remove_store(args):
    config = load_config()
    old_count = len(config["stores"])
    config["stores"] = [s for s in config["stores"] if s["name"] != args.name]
    if len(config["stores"]) == old_count:
        print(f"Store not found: {args.name}", file=sys.stderr)
        sys.exit(1)
    save_config(config)
    print(f"Store removed: {args.name}")


def build_parser():
    parser = argparse.ArgumentParser(description="Manage recall configuration")
    sub = parser.add_subparsers(dest="command", required=True)

    show = sub.add_parser("show")
    show.set_defaults(func=cmd_show)

    enable = sub.add_parser("enable")
    enable.set_defaults(func=cmd_enable)

    disable = sub.add_parser("disable")
    disable.set_defaults(func=cmd_disable)

    set_parser = sub.add_parser("set")
    set_parser.add_argument("--delay-seconds", dest="delay_seconds", type=int)
    set_parser.add_argument("--cooldown-messages", dest="cooldown_messages", type=int)
    set_parser.add_argument("--capture-lines", dest="capture_lines", type=int)
    set_parser.add_argument("--model")
    set_parser.add_argument("--session-retention-days", dest="session_retention_days", type=int)
    set_parser.set_defaults(func=cmd_set)

    add_store = sub.add_parser("add-store")
    add_store.add_argument("--name", required=True)
    add_store.add_argument("--index-path", required=True)
    add_store.add_argument("--base-path", required=True)
    add_store.add_argument("--instructions", default="")
    add_store.set_defaults(func=cmd_add_store)

    remove_store = sub.add_parser("remove-store")
    remove_store.add_argument("name")
    remove_store.set_defaults(func=cmd_remove_store)

    return parser


def main():
    args = build_parser().parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
