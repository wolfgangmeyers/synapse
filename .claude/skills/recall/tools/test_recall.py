#!/usr/bin/env python3
"""Tests for the merged recall system.

Tests core logic: config, fence stripping, prompt building, cooldown,
scheduling, cleanup, activation, and instructions. Uses temp directories.
"""

import json
import os
import shutil
import tempfile
import time
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path
from unittest.mock import patch

import recall_lib


class RecallTestBase(unittest.TestCase):
    """Base class that redirects recall_lib to a temp directory."""

    def setUp(self):
        self.tmpdir = tempfile.mkdtemp()
        self._orig_env = os.environ.get("MECHA_RECALL_DIR")
        os.environ["MECHA_RECALL_DIR"] = self.tmpdir

    def tearDown(self):
        if self._orig_env is None:
            os.environ.pop("MECHA_RECALL_DIR", None)
        else:
            os.environ["MECHA_RECALL_DIR"] = self._orig_env
        shutil.rmtree(self.tmpdir, ignore_errors=True)


class TestConfig(RecallTestBase):
    """Test configuration loading, saving, and validation."""

    def test_load_defaults_when_no_file(self):
        config = recall_lib.load_config()
        self.assertTrue(config["enabled"])
        self.assertEqual(config["delay_seconds"], 60)
        self.assertEqual(config["cooldown_messages"], 12)
        self.assertEqual(config["capture_lines"], 500)
        self.assertEqual(config["model"], "haiku")
        self.assertEqual(config["stores"], [])
        self.assertEqual(config["session_retention_days"], 7)

    def test_save_and_load(self):
        config = recall_lib.load_config()
        config["delay_seconds"] = 30
        recall_lib.save_config(config)
        loaded = recall_lib.load_config()
        self.assertEqual(loaded["delay_seconds"], 30)

    def test_merge_with_defaults(self):
        recall_lib.write_json_atomic(recall_lib.config_file(), {"enabled": False})
        config = recall_lib.load_config()
        self.assertFalse(config["enabled"])
        self.assertEqual(config["delay_seconds"], 60)

    def test_cooldown_seconds_migration(self):
        """Old cooldown_seconds config gets migrated to cooldown_messages."""
        recall_lib.write_json_atomic(recall_lib.config_file(), {
            "enabled": True,
            "cooldown_seconds": 3600,
            "delay_seconds": 60,
            "capture_lines": 500,
            "stores": [],
            "model": "haiku",
        })
        config = recall_lib.load_config()
        self.assertEqual(config["cooldown_messages"], 12)
        self.assertNotIn("cooldown_seconds", config)

    def test_validate_store_missing_fields(self):
        with self.assertRaises(ValueError):
            recall_lib.validate_store({"name": "test"})

    def test_validate_config_bad_types(self):
        config = recall_lib.default_config()
        config["enabled"] = "yes"
        with self.assertRaises(ValueError):
            recall_lib.validate_config(config)

    def test_store_with_instructions(self):
        config = recall_lib.load_config()
        config["stores"].append({
            "name": "kb",
            "index_path": "/tmp/idx",
            "base_path": "/tmp/base",
            "instructions": "Focus on action items",
        })
        recall_lib.save_config(config)
        loaded = recall_lib.load_config()
        self.assertEqual(loaded["stores"][0]["instructions"], "Focus on action items")

    def test_duplicate_store_names_rejected(self):
        config = recall_lib.load_config()
        config["stores"].append({"name": "kb", "index_path": "/a", "base_path": "/b"})
        config["stores"].append({"name": "kb", "index_path": "/c", "base_path": "/d"})
        with self.assertRaises(ValueError):
            recall_lib.save_config(config)


class TestFenceStripping(RecallTestBase):
    """Test markdown fence stripping in response parsing."""

    def test_no_fences(self):
        result = recall_lib._strip_markdown_fences('["project:test.md"]')
        self.assertEqual(result, '["project:test.md"]')

    def test_json_fences(self):
        text = '```json\n["project:test.md"]\n```'
        result = recall_lib._strip_markdown_fences(text)
        self.assertEqual(result, '["project:test.md"]')

    def test_plain_fences(self):
        text = '```\n["a", "b"]\n```'
        result = recall_lib._strip_markdown_fences(text)
        self.assertEqual(result, '["a", "b"]')

    def test_no_fences_passthrough(self):
        text = "just plain text"
        result = recall_lib._strip_markdown_fences(text)
        self.assertEqual(result, "just plain text")

    def test_fenced_response_parsing(self):
        """Full pipeline: fenced JSON should parse correctly."""
        result = recall_lib._parse_relevance_response('```json\n["project:test.md"]\n```')
        self.assertEqual(result, ["project:test.md"])

    def test_fenced_object_response(self):
        text = '```json\n{"relevant": ["kb:notes.md"]}\n```'
        result = recall_lib._parse_relevance_response(text)
        self.assertEqual(result, ["kb:notes.md"])


class TestResponseParsing(RecallTestBase):
    """Test Haiku response parsing."""

    def test_empty_array(self):
        self.assertEqual(recall_lib._parse_relevance_response("[]"), [])

    def test_valid_array(self):
        result = recall_lib._parse_relevance_response('["project:test.md", "kb:notes.md"]')
        self.assertEqual(result, ["project:test.md", "kb:notes.md"])

    def test_object_with_relevant(self):
        result = recall_lib._parse_relevance_response('{"relevant": ["project:a.md"]}')
        self.assertEqual(result, ["project:a.md"])

    def test_malformed_json_raises(self):
        with self.assertRaises(ValueError):
            recall_lib._parse_relevance_response("not json")

    def test_non_array_json_raises(self):
        with self.assertRaises(ValueError):
            recall_lib._parse_relevance_response('{"key": "value"}')

    def test_non_string_items_raise(self):
        with self.assertRaises(ValueError):
            recall_lib._parse_relevance_response('[123, 456]')

    def test_empty_object_with_relevant(self):
        result = recall_lib._parse_relevance_response('{"relevant": []}')
        self.assertEqual(result, [])


class TestCooldown(RecallTestBase):
    """Test message-count based cooldown logic."""

    def test_no_prior_recall(self):
        session = {"message_count": 10, "memories_recalled": {}}
        self.assertTrue(recall_lib.should_deliver(session, "kb:test.md", 12))

    def test_within_cooldown(self):
        session = {
            "message_count": 15,
            "memories_recalled": {
                "kb:test.md": {"message_count": 10, "recalled_at": "2026-01-01T00:00:00Z"},
            },
        }
        self.assertFalse(recall_lib.should_deliver(session, "kb:test.md", 12))

    def test_outside_cooldown(self):
        session = {
            "message_count": 25,
            "memories_recalled": {
                "kb:test.md": {"message_count": 10, "recalled_at": "2026-01-01T00:00:00Z"},
            },
        }
        self.assertTrue(recall_lib.should_deliver(session, "kb:test.md", 12))

    def test_different_memory_not_affected(self):
        session = {
            "message_count": 10,
            "memories_recalled": {
                "kb:other.md": {"message_count": 10, "recalled_at": "2026-01-01T00:00:00Z"},
            },
        }
        self.assertTrue(recall_lib.should_deliver(session, "kb:test.md", 12))

    def test_legacy_string_timestamp(self):
        """Old format: timestamp string instead of dict. Should allow delivery."""
        session = {
            "message_count": 10,
            "memories_recalled": {"kb:test.md": "2026-01-01T00:00:00Z"},
        }
        self.assertTrue(recall_lib.should_deliver(session, "kb:test.md", 12))

    def test_missing_message_count_in_recalled(self):
        session = {
            "message_count": 10,
            "memories_recalled": {"kb:test.md": {"recalled_at": "2026-01-01T00:00:00Z"}},
        }
        self.assertTrue(recall_lib.should_deliver(session, "kb:test.md", 12))


class TestScheduling(RecallTestBase):
    """Test schedule management."""

    def test_schedule_new_window(self):
        now = datetime(2026, 1, 1, tzinfo=timezone.utc)
        ok, due = recall_lib.schedule_window("worker-1", 60, now=now)
        self.assertTrue(ok)
        self.assertEqual(due, now + timedelta(seconds=60))

    def test_debounce(self):
        now = datetime(2026, 1, 1, tzinfo=timezone.utc)
        recall_lib.schedule_window("worker-1", 60, now=now)
        ok, _ = recall_lib.schedule_window("worker-1", 60, now=now)
        self.assertFalse(ok)

    def test_clear_scheduled(self):
        recall_lib.schedule_window("worker-1", 60)
        cleared = recall_lib.clear_scheduled_window("worker-1")
        self.assertTrue(cleared)
        cleared2 = recall_lib.clear_scheduled_window("worker-1")
        self.assertFalse(cleared2)

    def test_due_windows(self):
        past = datetime(2020, 1, 1, tzinfo=timezone.utc)
        now = datetime(2026, 1, 1, tzinfo=timezone.utc)
        recall_lib.schedule_window("past-win", 0, now=past)
        # Schedule far enough into the future that it won't be due in 2026
        future_base = datetime(2030, 1, 1, tzinfo=timezone.utc)
        recall_lib.schedule_window("future-win", 60, now=future_base)
        due = recall_lib.due_windows(now=now)
        windows = [w for w, _ in due]
        self.assertIn("past-win", windows)
        self.assertNotIn("future-win", windows)


class TestSession(RecallTestBase):
    """Test session management."""

    def test_load_new_session(self):
        session = recall_lib.load_session("worker-1")
        self.assertEqual(session["window"], "worker-1")
        self.assertEqual(session["message_count"], 0)
        self.assertEqual(session["memories_recalled"], {})

    def test_increment_message_count(self):
        count = recall_lib.increment_message_count("worker-1")
        self.assertEqual(count, 1)
        count = recall_lib.increment_message_count("worker-1")
        self.assertEqual(count, 2)

    def test_save_and_load_session(self):
        session = recall_lib.load_session("worker-1")
        session["message_count"] = 42
        recall_lib.save_session("worker-1", session)
        loaded = recall_lib.load_session("worker-1")
        self.assertEqual(loaded["message_count"], 42)
        self.assertIn("updated_at", loaded)


class TestIndexParsing(RecallTestBase):
    """Test memory index file parsing."""

    def test_parse_markdown_links(self):
        base = Path(self.tmpdir) / "store"
        base.mkdir()
        (base / "user_role.md").write_text("# User Role\n")
        (base / "feedback.md").write_text("# Feedback\n")

        index_text = (
            "# Memory\n\n"
            "- [User role](user_role.md) — user is a developer\n"
            "- [Feedback](feedback.md) — never edit infra inline\n"
        )
        candidates = recall_lib.parse_index_candidates(index_text, str(base))
        self.assertEqual(candidates, ["feedback.md", "user_role.md"])

    def test_bare_md_references(self):
        base = Path(self.tmpdir) / "store"
        base.mkdir()
        (base / "notes.md").write_text("# Notes\n")

        index_text = "Some text with notes.md reference\n"
        candidates = recall_lib.parse_index_candidates(index_text, str(base))
        self.assertEqual(candidates, ["notes.md"])

    def test_missing_files_excluded(self):
        base = Path(self.tmpdir) / "store"
        base.mkdir()
        index_text = "- [Missing](missing.md)\n"
        candidates = recall_lib.parse_index_candidates(index_text, str(base))
        self.assertEqual(candidates, [])

    def test_empty_index(self):
        base = Path(self.tmpdir) / "store"
        base.mkdir()
        candidates = recall_lib.parse_index_candidates("", str(base))
        self.assertEqual(candidates, [])


class TestPromptBuilding(RecallTestBase):
    """Test system/user prompt split and instructions."""

    def _make_stores(self, instructions=""):
        base = Path(self.tmpdir) / "store"
        base.mkdir(exist_ok=True)
        (base / "test.md").write_text("# Test\n")
        return [{
            "name": "kb",
            "index_path": str(base / "MEMORY.md"),
            "base_path": str(base),
            "index_text": "- [Test](test.md)\n",
            "candidates": ["test.md"],
            "instructions": instructions,
        }]

    def test_returns_tuple(self):
        stores = self._make_stores()
        candidate_map = recall_lib.build_candidate_map(stores)
        result = recall_lib.build_prompt("win", "context", stores, candidate_map)
        self.assertIsInstance(result, tuple)
        self.assertEqual(len(result), 2)

    def test_system_prompt_content(self):
        stores = self._make_stores()
        candidate_map = recall_lib.build_candidate_map(stores)
        system, user = recall_lib.build_prompt("win", "context", stores, candidate_map)
        self.assertIn("memory relevance evaluator", system)
        self.assertIn("JSON", system)
        self.assertIn("precision", system)

    def test_user_prompt_has_context(self):
        stores = self._make_stores()
        candidate_map = recall_lib.build_candidate_map(stores)
        system, user = recall_lib.build_prompt("win", "my test context", stores, candidate_map)
        self.assertIn("my test context", user)
        self.assertIn("kb:test.md", user)
        self.assertIn("win", user)

    def test_instructions_in_prompt(self):
        stores = self._make_stores(instructions="Focus on deployment failures")
        candidate_map = recall_lib.build_candidate_map(stores)
        system, user = recall_lib.build_prompt("win", "context", stores, candidate_map)
        self.assertIn("Focus on deployment failures", user)
        self.assertIn("Evaluation guidance", user)

    def test_no_instructions_no_guidance_line(self):
        stores = self._make_stores(instructions="")
        candidate_map = recall_lib.build_candidate_map(stores)
        system, user = recall_lib.build_prompt("win", "context", stores, candidate_map)
        self.assertNotIn("Evaluation guidance", user)


class TestDelivery(RecallTestBase):
    """Test delivery error handling."""

    def test_missing_file_returns_false(self):
        memory = {"id": "kb:missing.md", "path": "/nonexistent/path.md"}
        result = recall_lib.deliver_memory("win", memory)
        self.assertFalse(result)

    def test_existing_file_with_mock_mailbox(self):
        """Verify delivery attempts to call mailbox (will fail without mailbox)."""
        mem_file = Path(self.tmpdir) / "test.md"
        mem_file.write_text("# Test content\n")
        memory = {"id": "kb:test.md", "path": str(mem_file)}
        # This will return False because mailbox_send.py isn't available,
        # but should not raise an exception
        result = recall_lib.deliver_memory("win", memory)
        self.assertIsInstance(result, bool)


class TestCleanup(RecallTestBase):
    """Test session cleanup."""

    def _create_session(self, name, age_days=0):
        recall_lib.ensure_recall_dirs()
        path = recall_lib.session_path(name)
        recall_lib.write_json_atomic(path, {"window": name, "message_count": 0, "memories_recalled": {}})
        if age_days > 0:
            old_time = time.time() - (age_days * 86400)
            os.utime(path, (old_time, old_time))

    def test_prune_old_sessions(self):
        self._create_session("old-worker", age_days=10)
        self._create_session("new-worker", age_days=1)
        removed = recall_lib.prune_sessions(7)
        self.assertEqual(removed, ["old-worker"])

    def test_prune_keeps_recent(self):
        self._create_session("recent", age_days=0)
        removed = recall_lib.prune_sessions(7)
        self.assertEqual(removed, [])

    def test_reset_sessions(self):
        self._create_session("a")
        self._create_session("b")
        removed = recall_lib.reset_state(delete_sessions=True)
        self.assertEqual(len(removed), 2)
        self.assertEqual(list(recall_lib.list_session_files()), [])

    def test_reset_schedule(self):
        recall_lib.schedule_window("win", 60)
        recall_lib.reset_state(reset_schedule=True)
        data = recall_lib.load_schedule()
        self.assertEqual(data["scheduled"], {})


class TestActivation(RecallTestBase):
    """Test store activation/deactivation."""

    def test_activate_store(self):
        result = recall_lib.activate_store("win", "kb")
        self.assertTrue(result)
        active = recall_lib.get_active_stores_for_window("win")
        self.assertEqual(active, ["kb"])

    def test_activate_idempotent(self):
        recall_lib.activate_store("win", "kb")
        result = recall_lib.activate_store("win", "kb")
        self.assertFalse(result)
        active = recall_lib.get_active_stores_for_window("win")
        self.assertEqual(active, ["kb"])

    def test_deactivate_store(self):
        recall_lib.activate_store("win", "kb")
        result = recall_lib.deactivate_store("win", "kb")
        self.assertTrue(result)
        active = recall_lib.get_active_stores_for_window("win")
        self.assertEqual(active, [])

    def test_deactivate_nonexistent(self):
        result = recall_lib.deactivate_store("win", "kb")
        self.assertFalse(result)

    def test_no_active_stores_returns_none(self):
        active = recall_lib.get_active_stores_for_window("win")
        self.assertIsNone(active)

    def test_multiple_stores(self):
        recall_lib.activate_store("win", "kb")
        recall_lib.activate_store("win", "project")
        active = recall_lib.get_active_stores_for_window("win")
        self.assertEqual(sorted(active), ["kb", "project"])

    def test_per_window_isolation(self):
        recall_lib.activate_store("win1", "kb")
        recall_lib.activate_store("win2", "project")
        self.assertEqual(recall_lib.get_active_stores_for_window("win1"), ["kb"])
        self.assertEqual(recall_lib.get_active_stores_for_window("win2"), ["project"])


class TestStoreFiltering(RecallTestBase):
    """Test cron's store filtering by activation state."""

    def test_filter_stores_no_activation(self):
        from recall_cron import filter_stores_by_activation
        config = recall_lib.default_config()
        config["stores"] = [
            {"name": "kb", "index_path": "/a", "base_path": "/b"},
        ]
        result = filter_stores_by_activation(config, "win")
        self.assertIsNone(result)

    def test_filter_stores_activated(self):
        from recall_cron import filter_stores_by_activation
        recall_lib.activate_store("win", "kb")
        config = recall_lib.default_config()
        config["stores"] = [
            {"name": "kb", "index_path": "/a", "base_path": "/b"},
            {"name": "project", "index_path": "/c", "base_path": "/d"},
        ]
        result = filter_stores_by_activation(config, "win")
        self.assertIsNotNone(result)
        self.assertEqual(len(result["stores"]), 1)
        self.assertEqual(result["stores"][0]["name"], "kb")

    def test_filter_stores_empty_activation(self):
        from recall_cron import filter_stores_by_activation
        recall_lib.activate_store("win", "kb")
        recall_lib.deactivate_store("win", "kb")
        config = recall_lib.default_config()
        config["stores"] = [
            {"name": "kb", "index_path": "/a", "base_path": "/b"},
        ]
        result = filter_stores_by_activation(config, "win")
        self.assertIsNone(result)


class TestTimestamps(RecallTestBase):
    """Test timestamp parsing edge cases."""

    def test_isoformat_utc(self):
        dt = datetime(2026, 1, 1, 12, 0, 0, tzinfo=timezone.utc)
        result = recall_lib.isoformat_utc(dt)
        self.assertEqual(result, "2026-01-01T12:00:00Z")

    def test_parse_iso8601_with_z(self):
        dt = recall_lib.parse_iso8601("2026-01-01T12:00:00Z")
        self.assertEqual(dt.year, 2026)
        self.assertEqual(dt.tzinfo, timezone.utc)

    def test_parse_iso8601_with_offset(self):
        dt = recall_lib.parse_iso8601("2026-01-01T12:00:00+00:00")
        self.assertEqual(dt.year, 2026)

    def test_invalid_iso8601_raises(self):
        with self.assertRaises(ValueError):
            recall_lib.parse_iso8601("not-a-date")


class TestHookEarlyExit(RecallTestBase):
    """Test hook early-exit conditions."""

    def _run_hook(self):
        import io
        import recall_hook
        with patch("sys.stdin", io.StringIO("{}")):
            try:
                recall_hook.main()
            except SystemExit:
                pass

    def test_no_stores_exits_early(self):
        """Hook should not schedule if no stores configured."""
        config = recall_lib.default_config()
        config["stores"] = []
        recall_lib.save_config(config)
        with patch("recall_hook.current_agent_id", return_value="test-win"):
            self._run_hook()
        data = recall_lib.load_schedule()
        self.assertEqual(data["scheduled"], {})

    def test_disabled_exits_early(self):
        config = recall_lib.default_config()
        config["enabled"] = False
        config["stores"] = [{"name": "kb", "index_path": "/a", "base_path": "/b"}]
        recall_lib.save_config(config)
        with patch("recall_hook.current_agent_id", return_value="test-win"):
            self._run_hook()
        data = recall_lib.load_schedule()
        self.assertEqual(data["scheduled"], {})

    def test_with_stores_schedules(self):
        config = recall_lib.default_config()
        config["stores"] = [{"name": "kb", "index_path": "/a", "base_path": "/b"}]
        recall_lib.save_config(config)
        with patch("recall_hook.current_agent_id", return_value="test-win"):
            self._run_hook()
        data = recall_lib.load_schedule()
        self.assertIn("test-win", data["scheduled"])


class TestLoadStoreSnapshots(RecallTestBase):
    """Test store snapshot loading with instructions."""

    def test_includes_instructions(self):
        base = Path(self.tmpdir) / "store"
        base.mkdir()
        idx = base / "MEMORY.md"
        idx.write_text("- [Test](test.md)\n")
        (base / "test.md").write_text("# Test\n")

        config = recall_lib.default_config()
        config["stores"] = [{
            "name": "kb",
            "index_path": str(idx),
            "base_path": str(base),
            "instructions": "Focus on errors",
        }]
        stores = recall_lib.load_store_snapshots(config)
        self.assertEqual(len(stores), 1)
        self.assertEqual(stores[0]["instructions"], "Focus on errors")

    def test_missing_instructions_default_empty(self):
        base = Path(self.tmpdir) / "store"
        base.mkdir()
        idx = base / "MEMORY.md"
        idx.write_text("- [Test](test.md)\n")
        (base / "test.md").write_text("# Test\n")

        config = recall_lib.default_config()
        config["stores"] = [{
            "name": "kb",
            "index_path": str(idx),
            "base_path": str(base),
        }]
        stores = recall_lib.load_store_snapshots(config)
        self.assertEqual(stores[0]["instructions"], "")


if __name__ == "__main__":
    unittest.main()
