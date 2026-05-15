#!/usr/bin/env python3
"""Show current recall configuration and state."""

from datetime import datetime, timezone

from recall_lib import load_config, load_schedule, list_session_files, read_json


def _fmt_ts(value):
    try:
        dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
        return dt.astimezone(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
    except Exception:
        return value


def main():
    config = load_config()
    schedule = load_schedule()["scheduled"]

    print("Recall configuration")
    print(f"  enabled: {config['enabled']}")
    print(f"  delay_seconds: {config['delay_seconds']}")
    print(f"  cooldown_messages: {config['cooldown_messages']}")
    print(f"  capture_lines: {config['capture_lines']}")
    print(f"  model: {config['model']}")
    print(f"  session_retention_days: {config.get('session_retention_days', 7)}")
    print()

    print("Stores")
    if not config["stores"]:
        print("  none")
    else:
        for store in config["stores"]:
            print(f"  - {store['name']}: index={store['index_path']} base={store['base_path']}")
    print()

    print("Scheduled windows")
    if not schedule:
        print("  none")
    else:
        for window, due_at in sorted(schedule.items()):
            print(f"  - {window}: {_fmt_ts(due_at)}")
    print()

    print("Session cooldowns")
    session_files = list_session_files()
    if not session_files:
        print("  none")
    else:
        for path in session_files:
            data = read_json(path, {"window": path.stem, "memories_recalled": {}})
            count = len(data.get("memories_recalled", {}))
            print(f"  - {data.get('window', path.stem)}: message_count={data.get('message_count', 0)}, recalled={count}")


if __name__ == "__main__":
    main()
