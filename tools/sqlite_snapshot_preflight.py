#!/usr/bin/env python3
"""Create and verify a consistent SQLite online-backup snapshot."""

import argparse
from contextlib import closing
import os
from pathlib import Path
import shutil
import sqlite3
import tempfile
import time
from urllib.parse import quote

# How long an abandoned staging directory may sit in the snapshot directory
# before a later run removes it. The figure is not measured, and anything in a
# wide band would do: the only real constraint is that it stay far longer than
# any preflight run takes, so pruning can never reap a run still in progress.
STAGING_RETENTION_SECONDS = 24 * 60 * 60


def parse_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("--database", type=Path, required=True)
    parser.add_argument("--snapshot", type=Path, required=True)
    parser.add_argument("--restore-check", type=Path, required=True,
                        help="new temporary restore path, removed once it has verified the "
                             "snapshot and kept only when verification fails; "
                             "do not use the live router database")
    return parser.parse_args()


def require_new_regular_path(path):
    if path.exists() or path.is_symlink():
        raise SystemExit(f"refusing to overwrite existing path: {path}")
    path.parent.mkdir(parents=True, exist_ok=True)


def require_no_sidecars(snapshot):
    """Refuse to write a snapshot beside leftover -wal/-shm files.

    SQLite recovers a structurally valid -wal into whatever database file it
    opens under that name. A -wal left by an earlier snapshot of the same router
    is structurally valid, so rewrite_out_of_wal replays those stale frames into
    the fresh backup, checkpoints them in, and then deletes the evidence.
    Nothing downstream notices: integrity_check answers ok, the migration count
    matches, the tool prints success, and the snapshot holds the previous
    database's rows instead of today's.

    This is reachable from the retain-on-failure path, which leaves exactly that
    residue on purpose — an operator only has to open a retained snapshot to
    inspect it, then move the file the message named and rerun.
    """
    for suffix in ("-wal", "-shm"):
        sidecar = Path(f"{snapshot}{suffix}")
        if sidecar.exists() or sidecar.is_symlink():
            raise SystemExit(f"refusing to overwrite existing path: {sidecar}")


def prune_abandoned_staging(destination):
    """Remove staging directories left behind by runs that were killed outright.

    A signal that runs no handler leaves a near-complete copy of the router
    database in the snapshot directory, and nothing else would ever remove it.
    The cutoff is far longer than any preflight takes, so this cannot reach the
    staging directory of a run that is still going.
    """
    cutoff = time.time() - STAGING_RETENTION_SECONDS
    for leftover in destination.parent.glob(f"{destination.name}.partial-*"):
        try:
            if leftover.is_dir() and leftover.stat().st_mtime < cutoff:
                shutil.rmtree(leftover, ignore_errors=True)
        except OSError:
            # Never let tidying yesterday's debris stop today's backup.
            continue


def open_private_new(path):
    """Open path for writing as a new 0600 file, failing if it already exists.

    The 0600 is what matters: Python's open(path, 'wb') would create the restore
    copy at 0666 minus umask, and chmodding afterwards leaves a window in which a
    copy of the whole router database is readable by anyone. O_EXCL contributes
    the other half — it will not follow a symlink and will not overwrite an
    existing file.
    """
    return os.open(path, os.O_CREAT | os.O_EXCL | os.O_WRONLY, 0o600)


def copy_private(source, destination):
    """Copy source onto a new private destination, through the reserved handle.

    shutil.copyfile reopens the destination by path, which would follow a
    symlink planted between the reservation and the write, so the bytes go to
    the descriptor O_EXCL already vouched for instead.
    """
    with open(open_private_new(destination), "wb") as target, open(source, "rb") as origin:
        shutil.copyfileobj(origin, target)


def read_only_uri(path):
    """Build a read-only SQLite URI for path.

    SQLite parses the URI itself: an unescaped '?' opens the query section and
    %XX escapes are decoded. Interpolating a raw path can therefore point the
    open at a different file, or push mode=ro into the query where SQLite
    ignores it and opens the live router database read-write. Percent-encode
    everything except the path separator. A leading '//' would otherwise be read
    as a URI authority, and os.fsencode keeps a filename that is not valid UTF-8
    from failing to encode at all.
    """
    encoded = quote(os.fsencode(path), safe="/")
    return f"file:{'/' + encoded if encoded.startswith('//') else encoded}?mode=ro"


def stage_snapshot(destination):
    """Reserve a private sibling directory and a file inside it for the backup.

    The directory is what has to be private, not just the file. mkstemp hands
    back a descriptor, but both of the steps that follow take the staging path
    by *name* — sqlite3.connect() to write the backup, and os.link() to publish
    it — and both follow symlinks. In a snapshot directory another user can
    write to, that user can watch for the '.partial-' name, replace it with a
    symlink, and either read the whole router database as it is written or have
    the tool publish a decoy while reporting success. A 0700 directory they
    cannot traverse takes the name out of their reach.

    The backup reaches the operator's path only once it is complete. A run that
    dies part way through — including on a signal that runs no handler at all,
    such as SIGTERM from a job runner or a dropped SSH session — then leaves its
    debris under a '.partial-' name instead of at the snapshot path. What a
    killed backup leaves behind is either an empty file, if no page had been
    copied yet, or one with a hot -journal beside it. The empty one is the
    dangerous shape: PRAGMA integrity_check answers 'ok' on a zero-length file,
    so at the operator's path it reads as a finished backup.

    mkdtemp creates the directory 0700; open_private_new creates the file 0600.
    """
    staging_directory = Path(tempfile.mkdtemp(dir=destination.parent, prefix=f"{destination.name}.partial-"))
    staging = staging_directory / destination.name
    os.close(open_private_new(staging))
    return staging


def publish(staging, destination):
    """Put the finished backup at the operator's path in one atomic step.

    os.link fails rather than overwriting, so unlike the exists() check in
    require_new_regular_path this cannot lose a race with a second run of the
    tool aimed at the same snapshot path.
    """
    try:
        os.link(staging, destination)
    except FileExistsError:
        raise SystemExit(f"refusing to overwrite existing path: {destination}") from None
    except OSError as error:
        # Every rerun would fail the same way after paying for another full copy
        # of the database, so say what is actually wrong. Falling back to rename
        # would silently give up the atomic refuse-to-overwrite guarantee.
        raise SystemExit(f"could not place the snapshot at {destination}: {error}\n"
                         f"this needs a filesystem that supports hard links; choose another snapshot directory") from None


def rewrite_out_of_wal(path):
    """Take a freshly written backup file out of WAL journal mode.

    A sqlite3 online backup inherits the source database's journal mode in the
    destination file header, and the router database runs in WAL. A WAL-headed
    file with no -wal/-shm sidecars cannot be opened read-only, because a
    read-only connection is not allowed to create those sidecars, so the open
    fails with SQLITE_CANTOPEN. A static snapshot has no concurrent readers, so
    rollback journalling is the correct mode for it.
    """
    with closing(sqlite3.connect(path)) as database:
        mode = database.execute("PRAGMA journal_mode=DELETE").fetchone()
    if mode != ("delete",):
        # Bail out before the unlink below. Deleting a -wal beside a file that
        # is still in WAL mode throws away every committed transaction that has
        # not been checkpointed yet.
        raise SystemExit(f"could not take snapshot out of WAL mode, journal_mode={mode}")
    # Leaving WAL creates and then usually removes the sidecars, but a stale
    # -shm can survive. It is meaningless for a rollback-journal file and only
    # confuses an operator reading the snapshot directory, so drop both.
    for suffix in ("-wal", "-shm"):
        Path(f"{path}{suffix}").unlink(missing_ok=True)


def verify_and_count_migrations(path):
    # Both call sites below can fail with the same words, and they mean opposite
    # things: from the snapshot it says stop the release, from the restore copy
    # it says the snapshot is probably fine. Name the file in every failure.
    try:
        with closing(sqlite3.connect(read_only_uri(path), uri=True)) as database:
            rows = database.execute("PRAGMA integrity_check").fetchall()
            # Report corruption before reading any application table. A corrupt
            # file usually fails the schema_migrations read too, and that error
            # would mask the integrity detail the operator actually needs.
            if rows != [("ok",)]:
                detail = "; ".join(str(row[0]) for row in rows) or "no integrity_check output"
                raise SystemExit(f"integrity check failed: {detail}")
            migrations = database.execute("SELECT id FROM schema_migrations ORDER BY id").fetchall()
    except (Exception, SystemExit) as error:
        raise SystemExit(f"could not verify {path}: {error}") from error
    return len(migrations)


def retained_paths(snapshot, restore_check):
    """Name every file the failed run left behind, and what to do with them."""
    paths = [f"retained snapshot for inspection: {snapshot}"]
    for suffix in ("-wal", "-shm"):
        sidecar = Path(f"{snapshot}{suffix}")
        if sidecar.exists():
            # rewrite_out_of_wal keeps these on purpose when it cannot confirm
            # the snapshot left WAL, and the -wal then holds committed
            # transactions the snapshot file does not. An operator who moves
            # only the snapshot destroys them by hand.
            paths.append(f"retained snapshot sidecar, move or delete it together with the snapshot: {sidecar}")
    if restore_check.exists():
        paths.append(f"retained restore copy for inspection: {restore_check}")
    # A retained file is what makes the next run fail with "refusing to
    # overwrite existing path", so say what to do about it.
    paths.append("move or delete the retained path(s) before rerunning; this tool refuses to overwrite them")
    return paths


def main():
    args = parse_args()
    if not args.database.is_file() or args.database.is_symlink():
        raise SystemExit("--database must name a regular SQLite file")
    require_new_regular_path(args.snapshot)
    require_no_sidecars(args.snapshot)
    require_new_regular_path(args.restore_check)
    prune_abandoned_staging(args.snapshot)
    staging = stage_snapshot(args.snapshot)
    try:
        # mode=ro cannot create the -wal/-shm sidecars a WAL database needs, so
        # this open fails against a WAL-headed file whose sidecars are absent —
        # a cleanly stopped router, or a bare copied .sqlite, both of which keep
        # the WAL flag in the header after the sidecars are gone. Known
        # limitation, deliberately not handled here.
        with closing(sqlite3.connect(read_only_uri(args.database), uri=True)) as source, closing(sqlite3.connect(staging)) as snapshot:
            source.backup(snapshot)
        publish(staging, args.snapshot)
    except SystemExit:
        # publish() already says exactly what happened and what to do about it;
        # the wrapper below would bury that under "rerunning is safe", which is
        # the one thing that will not help against an occupied snapshot path.
        raise
    except Exception as error:
        raise SystemExit(f"snapshot backup failed: {error}\nno snapshot was written, so rerunning is safe") from error
    finally:
        # Removes the staging directory, and with it the staging file and any
        # -journal SQLite left beside it, on Ctrl-C and on every failure above.
        # After a successful publish this drops the second link to the snapshot.
        shutil.rmtree(staging.parent, ignore_errors=True)
    # From here on the snapshot holds a complete copy of the source database.
    # Never delete it on failure: the whole point of the preflight is to have a
    # backup, and a verification problem is exactly when an operator needs the
    # file on disk to inspect.
    try:
        rewrite_out_of_wal(args.snapshot)
        snapshot_migrations = verify_and_count_migrations(args.snapshot)
        copy_private(args.snapshot, args.restore_check)
        restored_migrations = verify_and_count_migrations(args.restore_check)
        if restored_migrations != snapshot_migrations:
            raise SystemExit(f"restored SQLite migration count {restored_migrations} differs from snapshot {snapshot_migrations}")
        # The restore copy has proved the one thing it exists to prove, so it is
        # removed here rather than left for someone to clean up later: it is a
        # second full copy of the router database, one per release, and leaving
        # it would block the next run on "refusing to overwrite existing path".
        # Same rule as the snapshot sidecars — keep the artifact only when
        # something went wrong and there is something left to inspect.
        args.restore_check.unlink()
    except BaseException as error:
        # BaseException, and not just Exception, because of what is on disk by
        # now: a Ctrl-C before the snapshot was published leaves nothing worth
        # telling the operator about, but one after it leaves a complete backup
        # they would never hear about from a bare KeyboardInterrupt traceback.
        # Losing the 130 exit code is the price of always naming that file.
        raise SystemExit("\n".join([f"snapshot preflight failed after backup: {error}",
                                    *retained_paths(args.snapshot, args.restore_check)])) from error
    print(f"snapshot_integrity=ok migrations={snapshot_migrations} restore_integrity=ok")


if __name__ == "__main__":
    main()
