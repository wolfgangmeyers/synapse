#!/usr/bin/env python3
"""Monitor system RAM and alert supervisor based on usage thresholds.

At >= 85%: sends a warning mailbox message to the supervisor.
At >= 90%: kills Google Chrome processes and notifies the supervisor.

Each level has its own cooldown to avoid flooding the mailbox.
"""

import json
import os
import signal
import subprocess
import sys
import time
from datetime import datetime, timezone
from pathlib import Path

WARN_THRESHOLD_PCT = 85.0
KILL_THRESHOLD_PCT = 90.0
COOLDOWN_MINUTES = 30
COOLDOWN_FILE = Path(os.path.expanduser("~/.mecha-developer/memory_monitor_last_alert.json"))

MAILBOX_SEND = Path(__file__).parent / "mailbox_send.py"


def read_meminfo():
    values = {}
    with open("/proc/meminfo") as f:
        for line in f:
            parts = line.split()
            if len(parts) >= 2:
                key = parts[0].rstrip(":")
                values[key] = int(parts[1])  # kB
    return values


def get_ram_pct(meminfo):
    total = meminfo["MemTotal"]
    available = meminfo["MemAvailable"]
    used = total - available
    return (used / total) * 100, used, total


def kb_to_gb(kb):
    return kb / 1024 / 1024


def cooldown_elapsed(key):
    if not COOLDOWN_FILE.exists():
        return True
    try:
        data = json.loads(COOLDOWN_FILE.read_text())
        last = data.get(key, 0)
        return (time.time() - last) >= (COOLDOWN_MINUTES * 60)
    except Exception:
        return True


def record_alert(key):
    COOLDOWN_FILE.parent.mkdir(parents=True, exist_ok=True)
    try:
        data = json.loads(COOLDOWN_FILE.read_text()) if COOLDOWN_FILE.exists() else {}
    except Exception:
        data = {}
    data[key] = time.time()
    COOLDOWN_FILE.write_text(json.dumps(data))


def send_mailbox(body, subject="High RAM usage"):
    subprocess.run(
        [sys.executable, str(MAILBOX_SEND), "--to", "supervisor", "--from", "memory-monitor",
         "--subject", subject, "--body", body],
        check=True,
    )


def get_top_procs():
    procs = []
    for pid_dir in Path("/proc").iterdir():
        if not pid_dir.name.isdigit():
            continue
        try:
            status = (pid_dir / "status").read_text()
            comm = (pid_dir / "comm").read_text().strip()
            rss_kb = 0
            for line in status.splitlines():
                if line.startswith("VmRSS:"):
                    rss_kb = int(line.split()[1])
                    break
            if rss_kb > 0:
                procs.append((rss_kb, comm, pid_dir.name))
        except Exception:
            continue
    procs.sort(reverse=True)
    return procs


def find_chrome_pids():
    """Find all Google Chrome / Chromium process PIDs by exe path."""
    pids = []
    for pid_dir in Path("/proc").iterdir():
        if not pid_dir.name.isdigit():
            continue
        try:
            exe = os.readlink(str(pid_dir / "exe"))
            if "chrome" in exe.lower() or "chromium" in exe.lower():
                pids.append(int(pid_dir.name))
        except Exception:
            continue
    return pids


def kill_chrome():
    """Send SIGTERM to all Chrome/Chromium processes. Returns list of killed PIDs."""
    pids = find_chrome_pids()
    killed = []
    for pid in pids:
        try:
            os.kill(pid, signal.SIGTERM)
            killed.append(pid)
        except Exception:
            continue
    return killed


def main():
    meminfo = read_meminfo()
    pct, used_kb, total_kb = get_ram_pct(meminfo)

    used_gb = kb_to_gb(used_kb)
    total_gb = kb_to_gb(total_kb)

    if pct < WARN_THRESHOLD_PCT:
        return  # all good

    top_procs = get_top_procs()

    # >= 90%: kill Chrome and notify
    if pct >= KILL_THRESHOLD_PCT and cooldown_elapsed("last_kill_ts"):
        killed = kill_chrome()
        if killed:
            body_lines = [
                f"RAM usage hit {pct:.1f}% ({used_gb:.1f} GB / {total_gb:.1f} GB used).",
                "",
                f"Automatically killed Google Chrome ({len(killed)} process(es), PIDs: {', '.join(str(p) for p in killed)}).",
                "",
                "Top memory consumers before kill:",
            ]
        else:
            body_lines = [
                f"RAM usage hit {pct:.1f}% ({used_gb:.1f} GB / {total_gb:.1f} GB used).",
                "",
                "Attempted to kill Google Chrome but no Chrome processes were found.",
                "",
                "Top memory consumers:",
            ]
        for rss_kb, comm, pid in top_procs[:8]:
            body_lines.append(f"  {comm:<20} {kb_to_gb(rss_kb):.2f} GB  (pid {pid})")
        send_mailbox("\n".join(body_lines), subject="High RAM — Chrome killed")
        record_alert("last_kill_ts")
        print(f"[memory_monitor] Chrome killed ({len(killed)} pids), alert sent: {pct:.1f}% RAM used at {datetime.now(timezone.utc).isoformat()}")
        return

    # >= 85%: warn supervisor
    if cooldown_elapsed("last_warn_ts"):
        body_lines = [
            f"RAM usage is at {pct:.1f}% ({used_gb:.1f} GB / {total_gb:.1f} GB used).",
            "",
            "Consider killing idle worker windows or pausing low-priority tasks to free memory.",
            "",
            "Top memory consumers (from /proc):",
        ]
        for rss_kb, comm, pid in top_procs[:8]:
            body_lines.append(f"  {comm:<20} {kb_to_gb(rss_kb):.2f} GB  (pid {pid})")
        send_mailbox("\n".join(body_lines))
        record_alert("last_warn_ts")
        print(f"[memory_monitor] Alert sent: {pct:.1f}% RAM used at {datetime.now(timezone.utc).isoformat()}")


if __name__ == "__main__":
    main()
