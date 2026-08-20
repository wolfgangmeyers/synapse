from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

TOOLS = Path(__file__).parents[1] / "tools"
sys.path.insert(0, str(TOOLS))

import import_meeting_notes as importer  # noqa: E402
from meeting_notes_store import Note, StoreError, import_note, normalize_text  # noqa: E402


class FakeBcb:
    def __init__(self, candidates, exports=None):
        self.tabs = [{"id": 10, "url": "https://drive.google.com/drive/u/1/shared-with-me", "title": "Drive"}]
        self.candidates = candidates
        self.exports = exports or {}
        self.calls = []

    def health(self):
        return {"extension_connected": True}

    def list_tabs(self, timeout=10):
        return [dict(tab) for tab in self.tabs]

    def execute(self, code, tab_id, timeout=30):
        self.calls.append((tab_id, code))
        if code.startswith("location.href = "):
            raw = code[len("location.href = ") : -len("; true;")]
            url = json.loads(raw)
            self.tabs[0]["url"] = url
            return True
        if code == importer.DRIVE_READY_JS:
            return {"ready": True, "url": self.tabs[0]["url"]}
        if code == importer.DRIVE_DISCOVERY_JS:
            return {"ready": True, "on_drive": True, "item_list": True, "candidates": self.candidates}
        if code == importer.DOC_READY_JS:
            return {"ready": True, "url": self.tabs[0]["url"], "line_count": 1}
        if code == importer.DOC_TITLE_JS:
            file_id = self.tabs[0]["url"].split("/document/d/", 1)[1].split("/", 1)[0]
            title = next(
                (item["title"] for item in self.candidates if item.get("file_id") == file_id),
                "",
            )
            return {"title": title, "url": self.tabs[0]["url"]}
        if importer.DOC_FETCH_EXPORT_JS in code:
            file_id = self.tabs[0]["url"].split("/document/d/", 1)[1].split("/", 1)[0]
            text = self.exports.get(file_id, "")
            return {"ok": bool(text), "status": 200 if text else 404, "text": text}
        raise AssertionError(f"unexpected JS: {code[:120]}")


class StoreTests(unittest.TestCase):
    def test_idempotent_skip_and_same_id_update(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            first = Note("abc_123", "Meeting Notes", "https://docs.google.com/document/d/abc_123/edit", "one\r\ntwo  \n")
            self.assertEqual(import_note(root, first), "imported")
            self.assertEqual(import_note(root, Note(first.file_id, "Renamed Meeting Notes", first.source_url, "one\ntwo")), "skipped")
            self.assertEqual(len(list(root.glob("*.md"))), 1)
            self.assertEqual(import_note(root, Note(first.file_id, first.title, first.source_url, "changed")), "updated")
            self.assertIn("changed", (root / "abc_123.md").read_text())

    def test_duplicate_ids_and_hash_mismatch_fail_closed(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            note = Note("same", "Meeting Notes", "https://docs.google.com/document/d/same/edit", "body")
            rendered = import_note.__globals__["render_note"](note)
            (root / "one.md").write_text(rendered)
            (root / "two.md").write_text(rendered)
            with self.assertRaisesRegex(StoreError, "duplicate Drive file ID"):
                import_note(root, note)
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            note = Note("same", "Meeting Notes", "https://docs.google.com/document/d/same/edit", "body")
            (root / "same.md").write_text(import_note.__globals__["render_note"](note).replace("body", "tampered"))
            with self.assertRaisesRegex(StoreError, "content hash"):
                import_note(root, note)

    def test_dry_run_does_not_create_output(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "notes"
            note = Note("dry", "Meeting Notes", "https://docs.google.com/document/d/dry/edit", "body")
            self.assertEqual(import_note(root, note, dry_run=True), "would-import")
            self.assertFalse(root.exists())
            self.assertEqual(normalize_text(" a  \r\nb \n"), " a\nb")


class ImporterTests(unittest.TestCase):
    def candidates(self):
        return [
            {"file_id": "z", "target": "doc", "title": "Weekly Product Meeting"},
            {"file_id": "a", "target": "doc", "title": "Platform Standup - Notes by Gemini"},
            {"file_id": "ignored", "target": "doc", "title": "User Journey Map: Awareness to Churn"},
            {"file_id": "other", "target": "doc", "title": "Meeting Agenda"},
            {"file_id": "sheet", "target": "sheet", "title": "Weekly Product Meeting"},
            {"file_id": "z", "target": "doc", "title": "duplicate Weekly Product Meeting"},
        ]

    def test_filter_observed_signals_exclusions_and_stable_sort(self):
        fake = FakeBcb(self.candidates())
        service = importer.MeetingNoteImporter(fake, limits=importer.Limits(max_files=10), timeout=1, sleep=lambda _: None)
        found = service.discover(10)
        self.assertEqual([candidate.file_id for candidate in found], ["a", "z"])
        self.assertEqual([candidate.title for candidate in found], ["Platform Standup - Notes by Gemini", "Weekly Product Meeting"])
        self.assertTrue(all(candidate.source_url.startswith("https://docs.google.com/document/d/") for candidate in found))

    def test_extract_note_text_prefers_body_over_status_only_screen_reader(self):
        body = """Saved to Drive
Image options
Weekly Product Meeting
Summary
Discussed roadmap priorities with product and engineering.
Next steps: circulate the revised plan."""
        screen_reader = "Saved to Drive\nImage options"
        text = importer.extract_note_text("Weekly Product Meeting", body, screen_reader)
        self.assertEqual(
            text,
            "Weekly Product Meeting\nSummary\nDiscussed roadmap priorities with product and engineering.\n"
            "Next steps: circulate the revised plan.",
        )
        self.assertNotIn("Saved to Drive", text)
        self.assertNotIn("Image options", text)

    def test_validate_export_text_rejects_empty_or_tiny_exports(self):
        with self.assertRaisesRegex(importer.ImporterError, "empty"):
            importer.validate_export_text("   \n")
        with self.assertRaisesRegex(importer.ImporterError, "too short"):
            importer.validate_export_text("tiny")

    def test_run_uses_only_existing_tab_and_restores_source(self):
        with tempfile.TemporaryDirectory() as temp:
            fake = FakeBcb(
                self.candidates(),
                {
                    "a": "Platform Standup - Notes by Gemini\nSummary\nThe team reviewed this week's release risks in detail.",
                    "z": "Weekly Product Meeting\nContent\nProduct and engineering agreed on the next planning cycle.",
                },
            )
            service = importer.MeetingNoteImporter(
                fake,
                limits=importer.Limits(max_files=10),
                timeout=1,
                sleep=lambda _: None,
            )
            with patch("import_meeting_notes.write_run_log"):
                result = service.run(source_tab_id=10, output=Path(temp), dry_run=False)
            self.assertEqual(result["imported"], 2)
            self.assertEqual(result["failed"], 0)
            self.assertEqual(fake.tabs, [{"id": 10, "url": importer.ORIGINAL_DRIVE_URL, "title": "Drive"}])
            self.assertEqual({tab_id for tab_id, _ in fake.calls}, {10})
            forbidden = ("window.open", "window.close", "chrome.tabs", "browser.tabs", "tabs.create", "tabs.remove", "tabs.update")
            self.assertTrue(all(not any(token in code for token in forbidden) for _, code in fake.calls))
            navigation_urls = [
                json.loads(code[len("location.href = ") : -len("; true;")])
                for _, code in fake.calls
                if code.startswith("location.href = ")
            ]
            self.assertEqual(navigation_urls, [
                "https://docs.google.com/document/d/a/edit", importer.ORIGINAL_DRIVE_URL,
                "https://docs.google.com/document/d/z/edit", importer.ORIGINAL_DRIVE_URL,
            ])
            self.assertEqual(sorted(path.name for path in Path(temp).glob("*.md")), ["a.md", "z.md"])

    def test_extraction_failure_is_reported_and_tab_restored(self):
        with tempfile.TemporaryDirectory() as temp:
            fake = FakeBcb(self.candidates()[:1], {"z": "too short"})
            service = importer.MeetingNoteImporter(
                fake,
                limits=importer.Limits(),
                timeout=1,
                sleep=lambda _: None,
            )
            with patch("import_meeting_notes.write_run_log"):
                result = service.run(source_tab_id=10, output=Path(temp), dry_run=True)
            self.assertEqual(result["failed"], 1)
            self.assertEqual(fake.tabs[0]["url"], "https://drive.google.com/drive/u/1/shared-with-me")


class BcbClientTests(unittest.TestCase):
    def test_execute_uses_bcb_command_contract_and_explicit_tab(self):
        class Response:
            def __enter__(self): return self
            def __exit__(self, *args): return False
            def read(self): return b'{"success": true, "result": 7}'

        with patch("import_meeting_notes.urllib.request.urlopen", return_value=Response()) as opened:
            client = importer.BcbClient(host="127.0.0.1", port=18080)
            self.assertEqual(client.execute("1 + 2", 44, timeout=3), 7)
            request = opened.call_args.args[0]
            payload = json.loads(request.data)
            self.assertEqual(payload["type"], "execute_js")
            self.assertEqual(payload["tab_id"], 44)
            self.assertEqual(payload["world"], "auto")
            self.assertEqual(payload["timeout"], 3)


if __name__ == "__main__":
    unittest.main()
