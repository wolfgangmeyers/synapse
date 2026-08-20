---
name: google-drive-meeting-notes
description: "Import observed meeting-note Google Docs from the authenticated Shared with me Drive tab into the local KB through Browser Console Bridge, with same-tab navigation, TXT export downloads, stable-ID idempotency, and no tab lifecycle commands."
---

# Google Drive meeting notes

Import meeting notes from the authenticated Google Drive UI using the local Browser Console Bridge (BCB) only. The importer does not use the Drive or Docs APIs, clipboard, cookies, or hidden network responses. It navigates each candidate in the existing tab and fetches the authenticated plain-text export endpoint (`/export?format=txt`), which is the same payload as **File → Download → Plain text (.txt)**.

## Prerequisites

- Chrome has the authenticated tab open at exactly:
  `https://drive.google.com/drive/u/1/shared-with-me`
- The BCB extension is connected to the local server described in `/Users/wolfgang/code/browser-console-bridge/README.md`.
- The source tab ID is known:

  ```bash
  cd /Users/wolfgang/code/browser-console-bridge
  .venv/bin/python3 -m cli.bcb_tabs --json
  ```

The importer requires `--tab-id`. It uses that existing tab for discovery and for every Docs navigation. It never opens a work tab and never changes any other tab.

## Run

From the repository root:

```bash
PYTHONPATH=.claude/skills/google-drive-meeting-notes/tools \
python3 .claude/skills/google-drive-meeting-notes/tools/import_meeting_notes.py \
  --tab-id 123 --dry-run
```

After reviewing the dry-run result, omit `--dry-run` to fetch notes. By default the importer writes raw exports to a **staging directory** (not searchable KB topics):

```text
~/.mecha-developer/kb/meeting-notes/
```

## Distill into KB topics (required)

Raw Gemini exports are **source material**, not durable KB content. After import:

1. Load the **`kb`** skill.
2. Read `~/.mecha-developer/kb/INDEX.md` and search existing topics for overlap.
3. Extract decisions, facts, deadlines, ownership, and technical contracts — skip transcript noise and Gemini boilerplate.
4. Merge into existing topic files or create new topic slugs under `~/.mecha-developer/kb/`.
5. Attribute each fact with meeting title + date + Drive link (not full paste).
6. Delete the staging `meeting-notes/` directory when distill is complete.
7. Update `google-drive-meeting-notes-import.md` with run timestamps and which topics received updates.

Do not leave raw meeting-note files in the KB tree after distill.

Each completed run also updates the resume log at:

```text
~/.mecha-developer/kb/google-drive-meeting-notes-import.md
```

The original Drive URL is restored after each candidate, including failed extraction, and again before success is reported.

## Same-tab discovery and selection

1. Discovery reads the current DOM without first navigating to a search page. It requires the exact original URL and the observed grid selector:
   `[role="grid"][aria-label="Item list"] [role="row"][data-selectable]`.
2. Each row supplies `data-id` and `data-target`. Only document rows (`data-target="doc"` or `"document"`) are eligible.
3. The title is read from `.i92Sbe.a65Cwf[data-tooltip]`, falling back to `.DNoYtb`. The title is normalized and the Docs suffix is removed from tooltips.
4. The importer accepts only observed meeting-note signals: `Notes by Gemini`, `Standup`, or `Weekly Product Meeting` (case-insensitive). The known non-note titles are excluded explicitly:
   - `User Journey Map: Awareness to Churn`
   - `🧑‍💻Security: Onboarding Guide for New Joiners`
   - `Remote Onboarding: Google, Confluence & Slack`
5. Candidates are de-duplicated and sorted by Drive file ID before any candidate is opened. This snapshot is the complete bounded work list.

## Same-tab TXT export extraction

For each enumerated candidate, the importer navigates the existing source tab to:
`https://docs.google.com/document/d/<drive-file-id>/edit`.

It waits for the Docs UI, then performs a same-origin synchronous fetch of:
`/document/d/<drive-file-id>/export?format=txt`
through BCB JavaScript execution. That endpoint returns the plain-text export payload. The importer validates the text, writes the note to the KB when not in dry-run mode, and never relies on canvas/ARIA scraping.

After extraction and persistence, the same tab is navigated back to the exact original Drive URL before the next note. No `window.open`, tab-close, tab-move, or tab-group command exists in the tool.

A request to close a tab after each note is currently ambiguous with the explicit zero-new-tabs/existing-source-tab requirement. Close behavior is intentionally not implemented; the caller-owned source tab is preserved and restored instead.

## Safety and limits

- Defaults are 50 files, 500,000 characters per document, 5,000,000 characters per run, and 30 seconds per BCB command/navigation wait. Set stricter positive values with `--max-files`, `--max-chars`, `--max-total-chars`, and `--timeout`.
- Limits fail the affected run/document; candidates are never silently truncated.
- The only BCB operations are health, tab-list, and JavaScript execution against the explicit source tab.
- The source tab must exist at the exact original URL before discovery and after the run. A missing tab, changed URL, authentication redirect, BCB failure, empty export, malformed export, selector drift, or unsafe local state is a nonzero failure.
- `--dry-run` performs the same discovery and export extraction but never creates or modifies a KB file.

## Idempotent local import

Each note is stored as `<drive-file-id>.md`; filenames are never title-derived. Front matter records `drive_file_id`, title, source URL, and the SHA-256 hash of normalized rendered text. On rerun:

- same Drive ID and same content hash: `skipped`;
- same Drive ID and changed content hash: atomically `updated`;
- new Drive ID: `imported`;
- duplicate IDs, invalid metadata, or a stored hash that does not match the body: fail closed.

A title change therefore cannot create a duplicate. The tool reports `imported`, `updated`, `skipped`, `would-import`, `would-update`, and `failed` counts.

## Tool files and tests

- `tools/import_meeting_notes.py` — BCB client, same-tab DOM discovery/navigation, plain-text export fetch, filtering, limits, run-log update, and CLI.
- `tools/meeting_notes_store.py` — normalized hashes, metadata validation, atomic ID-keyed storage.
- `tools/run_log.py` — last-run resume metadata written to `~/.mecha-developer/kb/google-drive-meeting-notes-import.md`.
- `tests/test_import_meeting_notes.py` — offline regression tests using a fake BCB; no authenticated browser is required.

Run validation from the repository root:

```bash
python3 -m unittest discover -s .claude/skills/google-drive-meeting-notes/tests -p 'test_*.py' -v
python3 -m py_compile \
  .claude/skills/google-drive-meeting-notes/tools/import_meeting_notes.py \
  .claude/skills/google-drive-meeting-notes/tools/meeting_notes_store.py \
  .claude/skills/google-drive-meeting-notes/tools/run_log.py
```
