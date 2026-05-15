#!/usr/bin/env python3
"""List, prune, or clear recall state."""

import argparse
import sys
from datetime import datetime, timezone

from recall_lib import list_session_files, load_schedule, prune_sessions, read_json, reset_state, session_path


def _fmt_mtime(path):
    mtime = datetime.fromtimestamp(path.stat().st_mtime, tz=timezone.utc)
    return mtime.strftime("%Y-%m-%d %H:%M:%S UTC")


def cmd_list(_args):
    print("Scheduled windows")
    scheduled = load_schedule()["scheduled"]
    if not scheduled:
        print("  none")
    else:
        for window, due_at in sorted(scheduled.items()):
            print(f"  - {window}: {due_at}")
    print()

    print("Sessions")
    sessions = list_session_files()
    if not sessions:
        print("  none")
        return
    for path in sessions:
        data = read_json(path, {"window": path.stem, "memories_recalled": {}})
        print(
            f"  - {data.get('window', path.stem)}: {_fmt_mtime(path)}, "
            f"message_count={data.get('message_count', 0)}, "
            f"{len(data.get('memories_recalled', {}))} memory(s)"
        )


def cmd_delete(args):
    path = session_path(args.window)
    if not path.exists():
        print(f"Session not found: {args.window}", file=sys.stderr)
        sys.exit(1)
    path.unlink()
    print(f"Deleted session: {args.window}")


def cmd_delete_all(_args):
    removed = reset_state(delete_sessions=True, reset_schedule=False)
    print(f"Deleted {len(removed)} session(s).")


def cmd_prune(args):
    removed = prune_sessions(args.older_than_days)
    print(f"Pruned {len(removed)} session(s).")


def cmd_reset_schedule(_args):
    reset_state(delete_sessions=False, reset_schedule=True)
    print("Schedule cleared.")


def cmd_reset_all(_args):
    removed = reset_state(delete_sessions=True, reset_schedule=True)
    print(f"Reset recall state. Deleted {len(removed)} session(s) and cleared schedule.")


def build_parser():
    parser = argparse.ArgumentParser(description="Manage recall state")
    sub = parser.add_subparsers(dest="command", required=True)

    list_cmd = sub.add_parser("list")
    list_cmd.set_defaults(func=cmd_list)

    delete_cmd = sub.add_parser("delete")
    delete_cmd.add_argument("window")
    delete_cmd.set_defaults(func=cmd_delete)

    delete_all_cmd = sub.add_parser("delete-all")
    delete_all_cmd.set_defaults(func=cmd_delete_all)

    prune_cmd = sub.add_parser("prune")
    prune_cmd.add_argument("--older-than-days", type=int, required=True)
    prune_cmd.set_defaults(func=cmd_prune)

    reset_schedule_cmd = sub.add_parser("reset-schedule")
    reset_schedule_cmd.set_defaults(func=cmd_reset_schedule)

    reset_all_cmd = sub.add_parser("reset-all")
    reset_all_cmd.set_defaults(func=cmd_reset_all)

    return parser


def main():
    args = build_parser().parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
