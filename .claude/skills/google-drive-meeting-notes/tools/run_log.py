"""Persist last-run metadata for the Google Drive meeting-notes importer."""
from __future__ import annotations

from datetime import datetime, timezone
from pathlib import Path
from typing import Any


RUN_LOG_PATH = Path.home() / ".mecha-developer" / "kb" / "google-drive-meeting-notes-import.md"


def _utc_now() -> datetime:
    return datetime.now(timezone.utc)


def _format_timestamp(value: datetime) -> str:
    return value.astimezone(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")


def render_run_log(
    *,
    started_at: datetime,
    finished_at: datetime,
    counts: dict[str, int],
    candidate_count: int,
    output: Path,
    export_method: str,
    source_tab_id: int,
    dry_run: bool,
    error: str | None = None,
) -> str:
    status = "failed" if error else ("dry-run" if dry_run else "completed")
    lines = [
        "# Google Drive Meeting Notes Import",
        "",
        "**Tags**: google-drive, meeting-notes, kb-import, automation",
        "**Sources**: google-drive-meeting-notes skill / BCB same-tab export importer",
        f"**Created**: {started_at.date().isoformat()}",
        f"**Updated**: {finished_at.date().isoformat()}",
        "",
        "---",
        "",
        "## Summary",
        "",
        "Tracks the most recent Google Drive meeting-note scrape/import run so future sessions can resume from the last successful sync.",
        "",
        "## Last Run",
        "",
        f"- **Status**: {status}",
        f"- **Started**: {_format_timestamp(started_at)}",
        f"- **Finished**: {_format_timestamp(finished_at)}",
        f"- **Source tab ID**: {source_tab_id}",
        f"- **Export method**: {export_method}",
        f"- **Output directory**: `{output}`",
        f"- **Candidates discovered**: {candidate_count}",
        f"- **Imported**: {counts.get('imported', 0)}",
        f"- **Updated**: {counts.get('updated', 0)}",
        f"- **Skipped**: {counts.get('skipped', 0)}",
        f"- **Would import**: {counts.get('would-import', 0)}",
        f"- **Would update**: {counts.get('would-update', 0)}",
        f"- **Failed**: {counts.get('failed', 0)}",
    ]
    if error:
        lines.extend(["", f"- **Error**: {error}"])
    lines.extend(
        [
            "",
            "## Resume Guidance",
            "",
            "1. Open Chrome to `https://drive.google.com/drive/u/1/shared-with-me` with BCB connected.",
            "2. Run the importer from the mecha-developer repo using the tab ID from `bcb_tabs --json`.",
            "3. Re-run safely anytime; notes are idempotent by Drive file ID and content hash.",
            "",
            "## See Also",
            "",
            "- [Local Browser Tooling](local-browser-tooling.md) -- Browser Console Bridge setup",
        ]
    )
    return "\n".join(lines) + "\n"


def write_run_log(**kwargs: Any) -> Path:
    content = render_run_log(**kwargs)
    path = RUN_LOG_PATH.expanduser()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")
    return path
