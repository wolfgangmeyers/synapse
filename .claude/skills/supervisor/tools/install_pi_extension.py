#!/usr/bin/env python3
"""Install/register stable Pi extensions: Mailbox V2 and Codex usage HUD.

Run from the mecha-developer repo root:
    python3 .claude/skills/supervisor/tools/install_pi_extension.py

Reload or restart Pi sessions after install. The Codex HUD hides gracefully when
subscription usage is unavailable.
"""

import json
import os
from pathlib import Path
import shutil
import tempfile

SOURCE = Path(__file__).resolve().parents[4] / "pi-extensions" / "mecha-messaging.js"
TARGET = Path.home() / ".mecha-developer" / "extensions" / "mecha-messaging.js"
CODEX_USAGE_HUD_SOURCE = SOURCE.with_name("codex-usage-hud.js")
CODEX_USAGE_HUD_TARGET = TARGET.with_name("codex-usage-hud.js")
SETTINGS = Path.home() / ".pi" / "agent" / "settings.json"
EXTENSION_MODULES = ("mailbox-client.js", "memory-capture.js", "recall-state.js", "recall-controller.js", "recall-evaluator.js", "recall-pipeline.js")
REGISTERED_EXTENSIONS = (TARGET, CODEX_USAGE_HUD_TARGET)


def extension_artifacts() -> tuple[tuple[Path, Path], ...]:
    return (
        (SOURCE, TARGET),
        (CODEX_USAGE_HUD_SOURCE, CODEX_USAGE_HUD_TARGET),
        *((SOURCE.with_name(name), TARGET.with_name(name)) for name in EXTENSION_MODULES),
    )


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
    try:
        data = json.loads(SETTINGS.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise ValueError(f"invalid Pi settings; refusing mutation: {error}") from error
    if not isinstance(data, dict) or not isinstance(data.get("packages", []), list):
        raise ValueError("Pi settings packages must be a list")
    return data, True


def main() -> int:
    artifacts = extension_artifacts()
    if any(not is_regular_file(source) for source, _ in artifacts):
        raise SystemExit("Invalid canonical extension bundle source")
    try:
        settings, had_settings = load_settings()
    except ValueError as error:
        raise SystemExit(f"Error: {error}") from error
    if any((target.exists() or target.is_symlink()) and not is_regular_file(target) for _, target in artifacts):
        raise SystemExit("Installed extension bundle must contain only regular, non-symlink files")

    TARGET.parent.mkdir(parents=True, exist_ok=True)
    SETTINGS.parent.mkdir(parents=True, exist_ok=True)
    artifact_backups = {target: target.with_name(target.name + ".rollback") for _, target in artifacts}
    settings_backup = SETTINGS.with_name(SETTINGS.name + ".mecha-messaging.rollback")
    if any(path.exists() or path.is_symlink() for path in (*artifact_backups.values(), settings_backup)):
        raise SystemExit("Existing Pi extension rollback copies need verification or recovery before updating")

    had_targets = {target: target.exists() for _, target in artifacts}
    try:
        if had_settings:
            atomic_copy(SETTINGS, settings_backup, ".settings-backup.")
        for _, target in artifacts:
            if had_targets[target]:
                atomic_copy(target, artifact_backups[target], ".mecha-messaging-backup.")
        for source, target in artifacts:
            atomic_copy(source, target, ".mecha-messaging.")
        registered_paths = {str(target) for target in REGISTERED_EXTENSIONS}
        packages = [entry for entry in settings.get("packages", []) if entry not in registered_paths]
        packages.extend(str(target) for target in REGISTERED_EXTENSIONS)
        settings["packages"] = packages
        atomic_write_json(SETTINGS, settings)
        verified_settings, _ = load_settings()
        if (any(not is_regular_file(target) or target.read_bytes() != source.read_bytes() for source, target in artifacts)
                or any(verified_settings["packages"].count(str(target)) != 1 for target in REGISTERED_EXTENSIONS)):
            raise RuntimeError("post-install verification failed")
    except Exception as error:
        if had_settings and settings_backup.exists():
            os.replace(settings_backup, SETTINGS)
        elif not had_settings:
            SETTINGS.unlink(missing_ok=True)
        for _, target in artifacts:
            if had_targets[target] and artifact_backups[target].exists():
                os.replace(artifact_backups[target], target)
            elif not had_targets[target]:
                target.unlink(missing_ok=True)
        raise SystemExit(f"Pi extension install failed and was rolled back: {error}") from error

    print(f"Pi extension installed: {TARGET} (source: {SOURCE})")
    print(f"Pi extension installed: {CODEX_USAGE_HUD_TARGET} (source: {CODEX_USAGE_HUD_SOURCE})")
    print(f"Bare Pi registration: {SETTINGS}")
    print("Rollback copies retained until final status verification passes.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
