#!/usr/bin/env python3
"""Shared storage helpers for workflow scripts."""

import json
import os
import tempfile
from contextlib import contextmanager
from datetime import datetime, timezone

import fcntl


def workflows_path():
    return os.environ.get(
        "MECHA_WORKFLOWS_FILE",
        os.path.expanduser("~/.mecha-developer/workflows.json"),
    )


def now_iso():
    return datetime.now(timezone.utc).replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")


def load_workflows():
    path = workflows_path()
    if not os.path.exists(path):
        return []
    with open(path, "r", encoding="utf-8") as fh:
        data = json.load(fh)
    if not isinstance(data, list):
        raise ValueError("Workflow store is invalid: expected a JSON array")
    for i, record in enumerate(data):
        if not isinstance(record, dict):
            raise ValueError(f"Workflow store is invalid: record {i} is not an object")
    return data


def save_workflows(workflows):
    path = workflows_path()
    directory = os.path.dirname(path)
    if directory:
        os.makedirs(directory, exist_ok=True)
    tmp_path = None
    try:
        with tempfile.NamedTemporaryFile(
            "w",
            dir=directory,
            delete=False,
            encoding="utf-8",
            suffix=".tmp",
        ) as fh:
            tmp_path = fh.name
            json.dump(workflows, fh, indent=2)
            fh.write("\n")
        os.replace(tmp_path, path)
    except Exception:
        if tmp_path and os.path.exists(tmp_path):
            os.unlink(tmp_path)
        raise


@contextmanager
def store_lock():
    """Lock the workflows store for read-modify-write operations."""
    lock_path = workflows_path() + ".lock"
    directory = os.path.dirname(lock_path)
    if directory:
        os.makedirs(directory, exist_ok=True)
    with open(lock_path, "a", encoding="utf-8") as lock_fh:
        fcntl.flock(lock_fh.fileno(), fcntl.LOCK_EX)
        try:
            yield
        finally:
            fcntl.flock(lock_fh.fileno(), fcntl.LOCK_UN)
