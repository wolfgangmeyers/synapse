"""Idempotent local storage for rendered Google Drive meeting notes."""
from __future__ import annotations

import hashlib
import os
import re
import tempfile
from dataclasses import dataclass
from pathlib import Path


class StoreError(RuntimeError):
    """The local KB state is unsafe to update."""


_ID_RE = re.compile(r"^[A-Za-z0-9_-]+$")


@dataclass(frozen=True)
class Note:
    file_id: str
    title: str
    source_url: str
    text: str

    @property
    def normalized_text(self) -> str:
        return normalize_text(self.text)

    @property
    def content_sha256(self) -> str:
        return hashlib.sha256(self.normalized_text.encode("utf-8")).hexdigest()


@dataclass(frozen=True)
class ExistingNote:
    file_id: str
    content_sha256: str
    path: Path


def normalize_text(text: str) -> str:
    """Hash rendered text consistently without changing meaningful indentation."""
    lines = [line.rstrip() for line in text.replace("\r\n", "\n").replace("\r", "\n").split("\n")]
    while lines and not lines[0].strip():
        lines.pop(0)
    while lines and not lines[-1].strip():
        lines.pop()
    return "\n".join(lines)


def _parse_front_matter(path: Path) -> tuple[dict[str, str], str]:
    try:
        raw = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise StoreError(f"cannot read existing note {path}: {exc}") from exc
    if not raw.startswith("---\n"):
        raise StoreError(f"existing note has no front matter: {path}")
    try:
        end = raw.index("\n---\n", 4)
    except ValueError as exc:
        raise StoreError(f"existing note has malformed front matter: {path}") from exc
    metadata: dict[str, str] = {}
    for line in raw[4:end].splitlines():
        if ":" not in line:
            raise StoreError(f"existing note has malformed metadata: {path}")
        key, value = line.split(":", 1)
        key, value = key.strip(), value.strip()
        if not key or not value:
            raise StoreError(f"existing note has empty metadata: {path}")
        metadata[key] = value
    return metadata, raw[end + 5 :]


def index_existing(root: Path) -> dict[str, ExistingNote]:
    """Index all markdown notes and reject duplicate or unsafe source identities."""
    if not root.exists():
        return {}
    if not root.is_dir():
        raise StoreError(f"output path is not a directory: {root}")
    indexed: dict[str, ExistingNote] = {}
    for path in sorted(root.glob("*.md")):
        metadata, body = _parse_front_matter(path)
        file_id = metadata.get("drive_file_id", "")
        digest = metadata.get("content_sha256", "")
        if not _ID_RE.fullmatch(file_id):
            raise StoreError(f"invalid drive_file_id in {path}")
        if not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise StoreError(f"invalid content_sha256 in {path}")
        actual = hashlib.sha256(normalize_text(body).encode("utf-8")).hexdigest()
        if actual != digest:
            raise StoreError(f"content hash does not match metadata in {path}")
        if file_id in indexed:
            raise StoreError(f"duplicate Drive file ID {file_id}: {indexed[file_id].path} and {path}")
        indexed[file_id] = ExistingNote(file_id, digest, path)
    return indexed


def note_path(root: Path, file_id: str) -> Path:
    if not _ID_RE.fullmatch(file_id):
        raise StoreError(f"invalid Drive file ID: {file_id!r}")
    return root / f"{file_id}.md"


def render_note(note: Note) -> str:
    if not _ID_RE.fullmatch(note.file_id):
        raise StoreError(f"invalid Drive file ID: {note.file_id!r}")
    title = " ".join(note.title.split()).replace("\n", " ").strip()
    if not title:
        raise StoreError(f"empty title for Drive file {note.file_id}")
    text = note.normalized_text
    if not text:
        raise StoreError(f"empty rendered text for Drive file {note.file_id}")
    return (
        "---\n"
        "source: google-drive\n"
        f"drive_file_id: {note.file_id}\n"
        f"title: {title}\n"
        f"source_url: {note.source_url}\n"
        f"content_sha256: {note.content_sha256}\n"
        "---\n\n"
        f"{text}\n"
    )


def import_note(root: Path, note: Note, *, dry_run: bool = False) -> str:
    """Return imported/updated/skipped/would-* and write atomically when allowed."""
    root = root.expanduser()
    existing = index_existing(root)
    prior = existing.get(note.file_id)
    if prior and prior.content_sha256 == note.content_sha256:
        return "skipped"
    rendered = render_note(note)
    if dry_run:
        return "would-update" if prior else "would-import"
    root.mkdir(parents=True, exist_ok=True)
    target = note_path(root, note.file_id)
    fd, temporary = tempfile.mkstemp(prefix=f".{note.file_id}.", suffix=".tmp", dir=root)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            handle.write(rendered)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, target)
    except OSError as exc:
        try:
            os.unlink(temporary)
        except OSError:
            pass
        raise StoreError(f"cannot write {target}: {exc}") from exc
    if prior and prior.path != target:
        try:
            prior.path.unlink()
        except OSError as exc:
            raise StoreError(f"updated {target} but could not remove old duplicate path {prior.path}: {exc}") from exc
    return "updated" if prior else "imported"
