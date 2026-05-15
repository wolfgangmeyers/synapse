#!/usr/bin/env python3
"""Notify the supervisor when a worker goes idle.

Designed to run as a Claude Code Stop hook. Posts a message to the
supervisor tmux window so the supervisor knows the worker is waiting.

Only fires when running inside a managed mecha worker session:
  - MECHA_WORKER_WINDOW must be set (set by worker_launch.py)
  - TMUX must be set (confirms we're inside a tmux session)

Uses a lockfile to prevent duplicate notifications when the hook
fires multiple times in quick succession.

Exit codes:
  0 - notification posted, or skipped gracefully (not in tmux / lock held)
"""

import logging
import os
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from _state import load_state, find_task_by_window
from _tmux import send_text_and_enter, window_exists, window_idle_seconds

SUPERVISOR_WINDOW = os.environ.get("MECHA_SUPERVISOR_WINDOW", "supervisor")
SUPERVISOR_MIN_IDLE = 30  # seconds — only notify if supervisor has been quiet this long

LOCK_FILE = os.path.expanduser("~/.mecha-developer/idle-notify.lock")
LOCK_MAX_AGE = 30  # seconds — lock older than this is assumed stale
LOG_FILE = os.path.expanduser("~/.mecha-developer/idle-notify.log")


def setup_logging():
    os.makedirs(os.path.dirname(LOG_FILE), exist_ok=True)
    logging.basicConfig(
        filename=LOG_FILE,
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(message)s",
        datefmt="%Y-%m-%d %H:%M:%S",
    )


def detect_managed_worker():
    """Return the worker window name if running in a managed mecha session, else None.

    Requires both:
      MECHA_WORKER_WINDOW — set by worker_launch.py for all managed workers
      TMUX              — set by tmux for all processes running inside a session
    """
    worker = os.environ.get("MECHA_WORKER_WINDOW")
    if not worker:
        return None
    if not os.environ.get("TMUX"):
        return None
    return worker


def acquire_lock():
    """Try to acquire the lockfile. Returns True if acquired, False if already held."""
    lock_dir = os.path.dirname(LOCK_FILE)
    os.makedirs(lock_dir, exist_ok=True)

    # Remove stale lock (process died without cleanup)
    try:
        age = time.time() - os.path.getmtime(LOCK_FILE)
        if age > LOCK_MAX_AGE:
            os.unlink(LOCK_FILE)
            logging.info("Removed stale lockfile (age %.0fs)", age)
    except FileNotFoundError:
        pass

    try:
        fd = os.open(LOCK_FILE, os.O_CREAT | os.O_EXCL | os.O_WRONLY)
        os.write(fd, str(os.getpid()).encode())
        os.close(fd)
        return True
    except FileExistsError:
        return False


def release_lock():
    try:
        os.unlink(LOCK_FILE)
    except OSError:
        pass


def is_idle_notify_enabled(worker_window):
    """Return True only if the task for this window has idle_notify enabled."""
    try:
        state = load_state()
        task = find_task_by_window(state, worker_window)
        return bool(task and task.get("idle_notify"))
    except Exception:
        return False  # fail safe: don't notify if state unreadable


def notify(worker_name, supervisor_window=SUPERVISOR_WINDOW):
    """Post an idle notification to the supervisor tmux window."""
    if not window_exists(supervisor_window):
        logging.warning(
            "Supervisor window '%s' not found, cannot notify for %s",
            supervisor_window,
            worker_name,
        )
        return

    idle = window_idle_seconds(supervisor_window)
    if idle is not None and idle < SUPERVISOR_MIN_IDLE:
        logging.info(
            "Supervisor active %.0fs ago (< %ds), skipping notification for %s",
            idle,
            SUPERVISOR_MIN_IDLE,
            worker_name,
        )
        return

    message = f"[idle-notify] {worker_name} is idle and waiting for instructions"
    send_text_and_enter(supervisor_window, message)
    logging.info(
        "Notified supervisor ('%s') that %s is idle", supervisor_window, worker_name
    )


def run():
    setup_logging()

    worker_name = detect_managed_worker()
    if not worker_name:
        sys.exit(0)

    if not is_idle_notify_enabled(worker_name):
        logging.info("idle_notify not enabled for %s, skipping", worker_name)
        sys.exit(0)

    if not acquire_lock():
        logging.info("Lock held, skipping duplicate notification for %s", worker_name)
        sys.exit(0)

    try:
        notify(worker_name)
    finally:
        release_lock()


if __name__ == "__main__":
    run()
