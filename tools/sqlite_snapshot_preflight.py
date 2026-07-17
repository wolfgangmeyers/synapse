#!/usr/bin/env python3
"""Create and verify a consistent SQLite online-backup snapshot."""

import argparse
import os
from pathlib import Path
import shutil
import sqlite3
import tempfile


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--database", type=Path, required=True)
    parser.add_argument("--snapshot", type=Path, required=True)
    parser.add_argument("--restore-check", type=Path, required=True,
                        help="new temporary restore path; do not use the live router database")
    return parser.parse_args()


def require_new_regular_path(path):
    if path.exists() or path.is_symlink():
        raise SystemExit(f"refusing to overwrite existing path: {path}")
    path.parent.mkdir(parents=True, exist_ok=True)


def integrity(path):
    with sqlite3.connect(f"file:{path}?mode=ro", uri=True) as database:
        result = database.execute("PRAGMA integrity_check").fetchone()
        migrations = database.execute("SELECT id FROM schema_migrations ORDER BY id").fetchall()
    if result != ("ok",):
        raise SystemExit("SQLite integrity check failed")
    return len(migrations)


def main():
    args = parse_args()
    if not args.database.is_file() or args.database.is_symlink():
        raise SystemExit("--database must name a regular SQLite file")
    require_new_regular_path(args.snapshot)
    require_new_regular_path(args.restore_check)
    try:
        with sqlite3.connect(f"file:{args.database}?mode=ro", uri=True) as source, sqlite3.connect(args.snapshot) as snapshot:
            source.backup(snapshot)
        os.chmod(args.snapshot, 0o600)
        snapshot_migrations = integrity(args.snapshot)
        shutil.copyfile(args.snapshot, args.restore_check)
        os.chmod(args.restore_check, 0o600)
        restored_migrations = integrity(args.restore_check)
        if restored_migrations != snapshot_migrations:
            raise SystemExit("restored SQLite migration count differs from snapshot")
    except Exception:
        args.snapshot.unlink(missing_ok=True)
        args.restore_check.unlink(missing_ok=True)
        raise
    print(f"snapshot_integrity=ok migrations={snapshot_migrations} restore_integrity=ok")


if __name__ == "__main__":
    main()
