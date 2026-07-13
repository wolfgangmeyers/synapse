#!/usr/bin/env python3
"""Install the canonical Mailbox V2 Pi extension at its stable local path."""

import json
import os
from pathlib import Path
import shutil
import tempfile

SOURCE = Path(__file__).resolve().parents[4] / "pi-extensions" / "mecha-messaging.js"
TARGET = Path.home() / ".mecha-developer" / "extensions" / "mecha-messaging.js"
SETTINGS = Path.home() / ".pi" / "agent" / "settings.json"


def is_regular_file(path: Path) -> bool:
    return path.is_file() and not path.is_symlink()


def atomic_copy(source: Path, target: Path, prefix: str) -> None:
    fd, staged_name = tempfile.mkstemp(prefix=prefix, dir=target.parent)
    os.close(fd)
    staged = Path(staged_name)
    try:
        shutil.copy2(source, staged)
        if not is_regular_file(staged) or staged.read_bytes() != source.read_bytes():
            raise RuntimeError(f"staged copy validation failed: {source}")
        os.replace(staged, target)
    except Exception:
        staged.unlink(missing_ok=True)
        raise


def atomic_write_json(path: Path, data: dict) -> None:
    fd, staged_name = tempfile.mkstemp(prefix=".settings.", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as staged:
            json.dump(data, staged, indent=2)
            staged.write("\n")
            staged.flush()
            os.fsync(staged.fileno())
        os.replace(staged_name, path)
    except Exception:
        Path(staged_name).unlink(missing_ok=True)
        raise


def load_settings() -> tuple[dict, bool]:
    if not SETTINGS.exists() and not SETTINGS.is_symlink():
        return {}, False
    if not is_regular_file(SETTINGS):
        raise ValueError(f"Pi settings must be a regular, non-symlink file: {SETTINGS}")
    original = SETTINGS.read_text(encoding="utf-8")
    try:
        data = json.loads(original)
    except json.JSONDecodeError as error:
        raise ValueError(f"invalid Pi settings; refusing mutation: {error}") from error
    if not isinstance(data, dict):
        raise ValueError("Pi settings must contain a JSON object")
    packages = data.get("packages", [])
    if not isinstance(packages, list):
        raise ValueError("Pi settings packages must be a list")
    return data, True


def main() -> int:
    if not is_regular_file(SOURCE):
        raise SystemExit(f"Invalid canonical extension source: {SOURCE}")

    try:
        settings, had_settings = load_settings()
    except ValueError as error:
        raise SystemExit(f"Error: {error}") from error

    if (TARGET.exists() or TARGET.is_symlink()) and not is_regular_file(TARGET):
        raise SystemExit(f"Installed extension must be a regular, non-symlink file: {TARGET}")

    TARGET.parent.mkdir(parents=True, exist_ok=True)
    SETTINGS.parent.mkdir(parents=True, exist_ok=True)
    artifact_backup = TARGET.with_name(TARGET.name + ".rollback")
    settings_backup = SETTINGS.with_name(SETTINGS.name + ".mecha-messaging.rollback")
    if artifact_backup.exists() or artifact_backup.is_symlink() or settings_backup.exists() or settings_backup.is_symlink():
        raise SystemExit(
            "Existing Pi extension rollback copies need verification or recovery before updating: "
            f"{artifact_backup}, {settings_backup}"
        )

    had_target = TARGET.exists()
    try:
        if had_settings:
            atomic_copy(SETTINGS, settings_backup, ".settings-backup.")
        if had_target:
            atomic_copy(TARGET, artifact_backup, ".mecha-messaging-backup.")

        atomic_copy(SOURCE, TARGET, ".mecha-messaging.")

        packages = [entry for entry in settings.get("packages", []) if entry != str(TARGET)]
        packages.append(str(TARGET))
        settings["packages"] = packages
        atomic_write_json(SETTINGS, settings)

        verified_settings, _ = load_settings()
        if (not is_regular_file(TARGET)
                or TARGET.read_bytes() != SOURCE.read_bytes()
                or verified_settings["packages"].count(str(TARGET)) != 1):
            raise RuntimeError("post-install verification failed")
    except Exception as error:
        if had_settings and settings_backup.exists():
            os.replace(settings_backup, SETTINGS)
        elif not had_settings:
            SETTINGS.unlink(missing_ok=True)
        if had_target and artifact_backup.exists():
            os.replace(artifact_backup, TARGET)
        elif not had_target:
            TARGET.unlink(missing_ok=True)
        raise SystemExit(f"Pi extension install failed and was rolled back: {error}") from error

    print(f"Pi extension installed: {TARGET} (source: {SOURCE})")
    print(f"Bare Pi registration: {SETTINGS}")
    print("Rollback copies retained until final status verification passes.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
