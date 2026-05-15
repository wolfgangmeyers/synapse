"""Shared reminder storage helpers."""

import contextlib
import fcntl
import json
import os
import pathlib
import uuid


DEFAULT_REMINDERS_FILE = pathlib.Path.home() / ".mecha-wolfgang" / "reminders.json"


def reminders_file():
    override = os.environ.get("MECHA_REMINDERS_FILE")
    if override:
        return pathlib.Path(override).expanduser()
    return DEFAULT_REMINDERS_FILE


def load_reminders():
    path = reminders_file()
    if not path.exists():
        return []
    with open(path, encoding="utf-8") as f:
        return json.load(f)


def save_reminders(reminders):
    path = reminders_file()
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_name(f".{path.name}.{os.getpid()}.{uuid.uuid4().hex}.tmp")
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(reminders, f, indent=2)
        f.write("\n")
        f.flush()
        os.fsync(f.fileno())
    os.replace(tmp, path)


@contextlib.contextmanager
def locked_reminders(save=True):
    """Yield reminders while holding an interprocess file lock.

    If loading fails, the caller gets the original exception and the reminders
    file is left untouched. If the caller raises, no save is attempted.
    """
    path = reminders_file()
    path.parent.mkdir(parents=True, exist_ok=True)
    lock_path = path.with_name(f"{path.name}.lock")
    with open(lock_path, "a+", encoding="utf-8") as lock_fh:
        fcntl.flock(lock_fh.fileno(), fcntl.LOCK_EX)
        try:
            reminders = load_reminders()
            yield reminders
            if save:
                save_reminders(reminders)
        finally:
            fcntl.flock(lock_fh.fileno(), fcntl.LOCK_UN)
