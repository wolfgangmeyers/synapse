#!/usr/bin/env python3
"""Import rendered Google Drive meeting notes through the local BCB bridge.

The importer reads the already-open Shared with me tab, navigates that same tab
one document at a time, and restores its exact Drive URL after every document.
It deliberately does not create, close, move, or group browser tabs.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

from meeting_notes_store import Note, StoreError, import_note


DEFAULT_OUTPUT = Path.home() / ".mecha-developer" / "kb" / "meeting-notes"
ORIGINAL_DRIVE_URL = "https://drive.google.com/drive/u/1/shared-with-me"
DEFAULT_MAX_FILES = 50
DEFAULT_MAX_CHARS = 500_000
DEFAULT_MAX_TOTAL_CHARS = 5_000_000
DEFAULT_TIMEOUT = 30.0
_ID_RE = re.compile(r"^[A-Za-z0-9_-]+$")
OBSERVED_TITLE_SIGNALS = ("Notes by Gemini", "Standup", "Weekly Product Meeting")
# These are the three non-note rows observed alongside the meeting-note Docs.
KNOWN_NON_NOTE_TITLES = frozenset(
    {
        "User Journey Map: Awareness to Churn",
        "🧑‍💻Security: Onboarding Guide for New Joiners",
        "Remote Onboarding: Google, Confluence & Slack",
    }
)


class ImporterError(RuntimeError):
    """A user-actionable failure; the run must not silently continue."""


class BcbError(ImporterError):
    pass


@dataclass(frozen=True)
class Candidate:
    file_id: str
    title: str
    source_url: str


@dataclass(frozen=True)
class Limits:
    max_files: int = DEFAULT_MAX_FILES
    max_chars: int = DEFAULT_MAX_CHARS
    max_total_chars: int = DEFAULT_MAX_TOTAL_CHARS


# Discovery is intentionally tied to the observed Shared with me grid. A
# broad link scan can accidentally include hidden navigation or unrelated files.
DRIVE_DISCOVERY_JS = r"""
(() => {
  const grid = document.querySelector('[role="grid"][aria-label="Item list"]');
  if (!grid) return {ready: false, on_drive: location.href === %s, error: 'Drive item-list grid not found'};
  const rows = [...grid.querySelectorAll('[role="row"][data-selectable]')];
  const candidates = [];
  const seen = new Set();
  for (const row of rows) {
    const fileId = (row.getAttribute('data-id') || '').trim();
    const target = (row.getAttribute('data-target') || '').trim();
    const titleNode = row.querySelector('.i92Sbe.a65Cwf[data-tooltip]');
    const fallbackNode = row.querySelector('.DNoYtb');
    const tooltip = titleNode?.getAttribute('data-tooltip') || '';
    const rawTitle = (tooltip || titleNode?.textContent || fallbackNode?.textContent || '').trim();
    const title = rawTitle.replace(/\s+Google (Docs|Sheets|Slides)\s*$/, '').trim();
    if (!fileId || seen.has(fileId)) continue;
    seen.add(fileId);
    candidates.push({file_id: fileId, target, title});
  }
  return {ready: document.readyState === 'complete', on_drive: location.href === %s, candidates};
})()
""" % (json.dumps(ORIGINAL_DRIVE_URL), json.dumps(ORIGINAL_DRIVE_URL))

DRIVE_READY_JS = r"""
(() => ({
  ready: document.readyState === 'complete' && location.href === %s,
  url: location.href,
}))()
""" % json.dumps(ORIGINAL_DRIVE_URL)

DOC_READY_JS = r"""
(() => ({
  ready: document.readyState === 'complete' &&
    location.hostname === 'docs.google.com' &&
    location.pathname.startsWith('/document/d/'),
  url: location.href,
  screen_reader: Boolean(document.querySelector('#docs-aria-speakable')),
  model_chunk: [...document.scripts].some(script =>
    (script.textContent || '').includes('DOCS_modelChunk')),
}))()
"""

DOC_ENABLE_SCREEN_READER_JS = r"""
(() => {
  const text = node => (node.innerText || node.textContent || '').trim();
  const all = [...document.querySelectorAll('*')];
  const support = all.find(node => /^Turn on screen reader support$/i.test(text(node)));
  if (support) {
    (support.closest('button, [role="button"], [role="menuitem"], a') || support).click();
    return {clicked: true, kind: 'support'};
  }
  const accessibility = all.find(node => /^Accessibility$/i.test(text(node)));
  if (accessibility) {
    (accessibility.closest('button, [role="button"], [role="menuitem"], a') || accessibility).click();
    return {clicked: true, kind: 'menu'};
  }
  return {clicked: false};
})()
"""

DOC_EXTRACT_JS = r"""
(() => {
  // Docs can expose a status-only #docs-aria-speakable node after accessibility
  // support is enabled. Return both rendered views so the caller can choose the
  // one that contains anchored note content instead of trusting that selector.
  const screenReader = document.querySelector('#docs-aria-speakable');
  const body = document.body;
  const bodyText = body?.innerText || body?.textContent || '';
  const screenReaderText = screenReader?.innerText || screenReader?.textContent || '';
  const titleNode = document.querySelector('[aria-label="Document title"], input[name="title"]');
  const title = (titleNode?.value || titleNode?.getAttribute('aria-label') ||
    document.title.replace(/\s*-\s*Google Docs\s*$/, '')).trim();
  return {
    ok: Boolean(bodyText || screenReaderText),
    error: bodyText || screenReaderText ? null : 'Docs body and screen-reader text were empty',
    title,
    body_text: bodyText,
    screen_reader_text: screenReaderText,
    url: location.href,
  };
})()
"""

# These are Docs controls, status messages, and accessibility text. They are
# removed before anchoring so a status-only screen-reader node cannot become a
# note, and so the body text can be used safely when it contains the document.
_DOCS_BOILERPLATE_LINES = frozenset(
    {
        'Close', 'OK', 'Share', 'Ask Gemini', 'Editing', 'Viewing', 'Suggesting',
        'Document tabs', 'Notes', 'Transcript', 'Banner hidden',
        'Screen reader support enabled.', 'Normal text', 'Google Sans Flex Normal',
        'Saved to Drive', 'Saved to this device', 'Saving...', 'Working offline',
        'Image options', 'Open link', 'Add comment', 'Comment history',
    }
)
_DOCS_BOILERPLATE_SUBSTRINGS = (
    'Navigate documents faster with tabs',
    'Select a tab to go to a specific part of this document',
    'FileEditViewInsertFormatToolsGeminiExtensionsHelpAccessibility',
    'Google Sans Flex Normal',
)
_NOTE_ANCHOR_RE = re.compile(r'^(?:summary|content|meeting notes?)\s*:?\s*$', re.IGNORECASE)


def _is_substantive_line(line: str) -> bool:
    return len(re.sub(r'[^\w]', '', line, flags=re.UNICODE)) >= 12


def _clean_docs_text(value: str) -> list[str]:
    text = value.replace('\u000b', '\n').replace('\u00a0', ' ')
    for fragment in _DOCS_BOILERPLATE_SUBSTRINGS:
        text = text.replace(fragment, '')
    lines: list[str] = []
    for raw_line in re.split(r'\n+', text):
        line = re.sub(r'\s+', ' ', raw_line).strip()
        if not line or line in _DOCS_BOILERPLATE_LINES:
            continue
        if re.fullmatch(r'\d+ visible tabs named .+', line, re.IGNORECASE):
            continue
        lines.append(line)
    return lines


def _anchored_note_text(title: str, raw_text: str) -> str:
    if not isinstance(raw_text, str):
        return ''
    lines = _clean_docs_text(raw_text)
    if not lines:
        return ''
    normalized_title = ' '.join(title.split()).casefold()
    title_index = next(
        (index for index, line in enumerate(lines) if line.casefold() == normalized_title),
        None,
    ) if normalized_title else None
    section_index = next(
        (index for index, line in enumerate(lines) if _NOTE_ANCHOR_RE.fullmatch(line)),
        None,
    )
    anchor_index = title_index if title_index is not None else section_index
    if anchor_index is None:
        # Some rendered Docs bodies omit the title and Summary/Content heading.
        # The two-line threshold is deliberately conservative: known toolbar and
        # status lines were removed above, while UI-only views still fail closed.
        if sum(_is_substantive_line(line) for line in lines) < 2:
            return ''
        return '\n'.join(lines).strip()
    anchored = lines[anchor_index:]
    content_lines = [
        line for line in anchored
        if line.casefold() != normalized_title and not _NOTE_ANCHOR_RE.fullmatch(line)
    ]
    # A title or heading by itself is not evidence that the page contains notes.
    # Require a substantive line so toolbar/status text fails closed.
    if not any(_is_substantive_line(line) for line in content_lines):
        return ''
    return '\n'.join(anchored).strip()


def extract_note_text(title: str, body_text: str, screen_reader_text: str) -> str:
    """Select anchored rendered note text, preferring body over status-only ARIA."""
    for raw_text in (body_text, screen_reader_text):
        candidate = _anchored_note_text(title, raw_text)
        if candidate:
            return candidate
    return ''


class BcbClient:
    """Small stdlib-only client matching browser-console-bridge's contract."""

    def __init__(self, host: str | None = None, port: int | None = None) -> None:
        self.host = host or os.environ.get("BCB_HOST", "localhost")
        self.port = port or int(os.environ.get("BCB_HTTP_PORT", "18080"))
        self.base_url = f"http://{self.host}:{self.port}"

    def command(self, payload: dict[str, Any], timeout: float) -> dict[str, Any]:
        payload = dict(payload)
        payload.setdefault("msg_id", str(uuid.uuid4()))
        payload.setdefault("ts", time.time())
        payload["timeout"] = timeout
        request = urllib.request.Request(
            self.base_url + "/command",
            data=json.dumps(payload).encode("utf-8"),
            headers={"Content-Type": "application/json"},
        )
        try:
            with urllib.request.urlopen(request, timeout=timeout + 5) as response:
                result = json.loads(response.read())
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise BcbError(f"BCB communication failed: {exc}") from exc
        if not isinstance(result, dict):
            raise BcbError("BCB returned a non-object response")
        if not result.get("success"):
            code = result.get("code", "BCB_ERROR")
            raise BcbError(f"BCB {code}: {result.get('error', 'unknown error')}")
        return result

    def health(self) -> dict[str, Any]:
        try:
            with urllib.request.urlopen(self.base_url + "/health", timeout=5) as response:
                result = json.loads(response.read())
        except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise BcbError(f"BCB health check failed: {exc}") from exc
        if not result.get("extension_connected"):
            raise BcbError("BCB extension is not connected")
        return result

    def list_tabs(self, timeout: float = 10) -> list[dict[str, Any]]:
        return list(self.command({"type": "list_tabs"}, timeout).get("tabs", []))

    def execute(self, code: str, tab_id: int, timeout: float = DEFAULT_TIMEOUT) -> Any:
        result = self.command(
            {"type": "execute_js", "code": code, "tab_id": tab_id, "world": "auto"},
            timeout,
        )
        return result.get("result")


class MeetingNoteImporter:
    def __init__(self, bcb: BcbClient, *, limits: Limits, timeout: float,
                 sleep: Callable[[float], None] = time.sleep) -> None:
        self.bcb = bcb
        self.limits = limits
        self.timeout = timeout
        self.sleep = sleep

    @staticmethod
    def _tab(tabs: list[dict[str, Any]], tab_id: int) -> dict[str, Any]:
        for tab in tabs:
            if tab.get("id") == tab_id:
                return tab
        raise ImporterError(f"tab {tab_id} does not exist")

    def _wait(self, predicate: Callable[[], Any], label: str, deadline: float) -> Any:
        last: Any = None
        while time.monotonic() < deadline:
            last = predicate()
            if isinstance(last, dict) and last.get("ready"):
                return last
            self.sleep(0.25)
        raise ImporterError(f"timed out waiting for {label}; last state: {last!r}")

    def _navigate(self, tab_id: int, url: str) -> None:
        self.bcb.execute("location.href = " + json.dumps(url) + "; true;", tab_id, self.timeout)

    def _restore_drive(self, tab_id: int) -> None:
        self._navigate(tab_id, ORIGINAL_DRIVE_URL)
        self._wait(
            lambda: self.bcb.execute(DRIVE_READY_JS, tab_id, self.timeout),
            "original Shared with me Drive UI",
            time.monotonic() + self.timeout,
        )

    def discover(self, tab_id: int) -> list[Candidate]:
        state = self._wait(
            lambda: self.bcb.execute(DRIVE_READY_JS, tab_id, self.timeout),
            "original Shared with me Drive UI",
            time.monotonic() + self.timeout,
        )
        if not isinstance(state, dict) or state.get("url") != ORIGINAL_DRIVE_URL:
            raise ImporterError("source tab is not the exact Shared with me Drive URL")
        result = self.bcb.execute(DRIVE_DISCOVERY_JS, tab_id, self.timeout)
        if not isinstance(result, dict) or not result.get("on_drive"):
            raise ImporterError("source tab did not remain on the exact Shared with me Drive URL")
        raw = result.get("candidates")
        if not isinstance(raw, list):
            raise ImporterError("Drive discovery returned an invalid candidate list")
        candidates: list[Candidate] = []
        seen: set[str] = set()
        excluded = {" ".join(title.split()).casefold() for title in KNOWN_NON_NOTE_TITLES}
        signals = tuple(signal.casefold() for signal in OBSERVED_TITLE_SIGNALS)
        for item in raw:
            if not isinstance(item, dict):
                raise ImporterError("Drive discovery returned a malformed candidate")
            file_id, target, title = item.get("file_id"), item.get("target"), item.get("title")
            if not isinstance(file_id, str) or not _ID_RE.fullmatch(file_id):
                raise ImporterError(f"Drive discovery returned an invalid file ID: {file_id!r}")
            if file_id in seen:
                continue
            seen.add(file_id)
            if not isinstance(target, str) or target not in {"doc", "document"}:
                continue
            if not isinstance(title, str):
                continue
            title = " ".join(title.split())
            normalized_title = title.casefold()
            if normalized_title in excluded or not any(signal in normalized_title for signal in signals):
                continue
            candidates.append(
                Candidate(
                    file_id,
                    title,
                    f"https://docs.google.com/document/d/{file_id}/edit",
                )
            )
        candidates.sort(key=lambda candidate: (candidate.file_id, candidate.title))
        if len(candidates) > self.limits.max_files:
            raise ImporterError(
                f"{len(candidates)} meeting-note documents exceed max-files={self.limits.max_files}"
            )
        return candidates

    def extract_one(self, tab_id: int, candidate: Candidate) -> Note:
        self._navigate(tab_id, candidate.source_url)
        state = self._wait(
            lambda: self.bcb.execute(DOC_READY_JS, tab_id, self.timeout),
            f"Docs {candidate.file_id}",
            time.monotonic() + self.timeout,
        )
        expected_prefix = f"https://docs.google.com/document/d/{candidate.file_id}/"
        if not isinstance(state, dict) or not str(state.get("url", "")).startswith(expected_prefix):
            raise ImporterError(f"Docs navigation for {candidate.file_id} did not reach its document UI")
        # The observed Docs UI exposes note text only after this accessibility
        # link is activated. A missing link is safe to ignore: some sessions
        # already have screen-reader support enabled.
        enable_result = self.bcb.execute(DOC_ENABLE_SCREEN_READER_JS, tab_id, self.timeout)
        if isinstance(enable_result, dict) and enable_result.get("clicked"):
            self.sleep(0.75)
            # Accessibility is a menu item in some Docs layouts. The first
            # click opens that menu; the second clicks the actual support link.
            if enable_result.get("kind") == "menu":
                self.bcb.execute(DOC_ENABLE_SCREEN_READER_JS, tab_id, self.timeout)
                self.sleep(0.75)
        result = self.bcb.execute(DOC_EXTRACT_JS, tab_id, self.timeout)
        if not isinstance(result, dict):
            raise ImporterError(f"could not extract rendered text for {candidate.file_id}: {result!r}")
        title = result.get("title")
        if not isinstance(title, str) or not title.strip():
            title = candidate.title
        body_text = result.get("body_text")
        if not isinstance(body_text, str):
            # Keep accepting the old fake/bridge result shape while callers
            # transition to the two-view DOM result.
            body_text = result.get("text") if isinstance(result.get("text"), str) else ""
        screen_reader_text = result.get("screen_reader_text")
        if not isinstance(screen_reader_text, str):
            screen_reader_text = ""
        text = extract_note_text(title, body_text, screen_reader_text)
        if not text:
            raise ImporterError(f"rendered text contained no plausible note content for {candidate.file_id}")
        if len(text) > self.limits.max_chars:
            raise ImporterError(f"{candidate.file_id} exceeds max-chars={self.limits.max_chars}")
        return Note(candidate.file_id, candidate.title, candidate.source_url, text)

    def run(self, *, source_tab_id: int, output: Path, dry_run: bool) -> dict[str, int]:
        self.bcb.health()
        tabs = self.bcb.list_tabs()
        source = self._tab(tabs, source_tab_id)
        if source.get("url") != ORIGINAL_DRIVE_URL:
            raise ImporterError(
                f"source tab {source_tab_id} must be exactly {ORIGINAL_DRIVE_URL}"
            )

        # Discovery completes before the first Docs navigation so a changing
        # Drive grid cannot add work after the bounded candidate set is chosen.
        candidates = self.discover(source_tab_id)
        counts = {
            key: 0
            for key in ("imported", "updated", "skipped", "would-import", "would-update", "failed")
        }
        total_chars = 0
        for candidate in candidates:
            try:
                note = self.extract_one(source_tab_id, candidate)
                total_chars += len(note.normalized_text)
                if total_chars > self.limits.max_total_chars:
                    raise ImporterError(
                        f"run exceeds max-total-chars={self.limits.max_total_chars}"
                    )
                outcome = import_note(output, note, dry_run=dry_run)
                counts[outcome] += 1
            except (ImporterError, StoreError) as exc:
                counts["failed"] += 1
                print(f"failed {candidate.file_id}: {exc}", file=sys.stderr)
            finally:
                # The requested per-note close action is intentionally not
                # implemented: it conflicts with the zero-new-tabs contract.
                # Keep this caller-owned tab and restore its exact URL instead.
                self._restore_drive(source_tab_id)

        final_source = self._tab(self.bcb.list_tabs(), source_tab_id)
        if final_source.get("url") != ORIGINAL_DRIVE_URL:
            raise ImporterError("source tab did not return to the exact original Drive URL")
        return counts


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tab-id", type=int, required=True, help="Authenticated Shared with me Drive tab ID")
    parser.add_argument("--output", type=Path, default=DEFAULT_OUTPUT)
    parser.add_argument("--dry-run", action="store_true", help="Extract and report without writing KB files")
    parser.add_argument("--max-files", type=int, default=DEFAULT_MAX_FILES)
    parser.add_argument("--max-chars", type=int, default=DEFAULT_MAX_CHARS)
    parser.add_argument("--max-total-chars", type=int, default=DEFAULT_MAX_TOTAL_CHARS)
    parser.add_argument("--timeout", type=float, default=DEFAULT_TIMEOUT, help="Per-BCB command and navigation wait limit")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.max_files < 1 or args.max_chars < 1 or args.max_total_chars < 1 or args.timeout <= 0:
        print("limits and timeout must be positive", file=sys.stderr)
        return 2
    try:
        counts = MeetingNoteImporter(
            BcbClient(),
            limits=Limits(args.max_files, args.max_chars, args.max_total_chars),
            timeout=args.timeout,
        ).run(source_tab_id=args.tab_id, output=args.output, dry_run=args.dry_run)
    except (ImporterError, StoreError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2
    print(json.dumps(counts, sort_keys=True))
    return 1 if counts["failed"] else 0


if __name__ == "__main__":
    sys.exit(main())
