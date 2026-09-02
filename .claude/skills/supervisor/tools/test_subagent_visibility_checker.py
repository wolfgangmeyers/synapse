#!/usr/bin/env python3
"""Focused tests for the read-only native-subagent visibility checker."""

from __future__ import annotations

import json
import sys
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import patch

TOOLS = Path(__file__).resolve().parent
sys.path.insert(0, str(TOOLS))

import subagent_visibility_checker as checker

FIXTURES = TOOLS / "fixtures" / "subagent_visibility"


class ParsingTests(unittest.TestCase):
    def test_latest_widget_extracts_only_running_rows_and_keeps_duplicate_members(self):
        scrollback = (FIXTURES / "active-widget.txt").read_text()
        self.assertEqual(
            checker.parse_active_subagent_descriptions(scrollback),
            ["inspect router state", "check API mapping"],
        )

    def test_latest_widget_wins_over_an_older_widget(self):
        scrollback = "● Agents\n└─ ⠋ Old  old label · ↻1 · 0.1s\n● Agents\n└─ ⠋ New  new label · ↻1 · 0.1s\n"
        self.assertEqual(checker.parse_active_subagent_descriptions(scrollback), ["new label"])

    def test_overflow_is_incomplete_and_does_not_expose_prompt_text(self):
        descriptions, overflow, found = checker.parse_latest_agents_widget(
            (FIXTURES / "overflow-widget.txt").read_text()
        )
        self.assertEqual(descriptions, ["first visible child"])
        self.assertTrue(overflow)
        self.assertTrue(found)
        self.assertNotIn("private", json.dumps(descriptions))

    def test_non_widget_spinner_and_completed_rows_are_ignored(self):
        scrollback = "⠋ not a widget row · ↻1 · private\n● Agents\n└─ ✓ General-purpose  finished · ↻1 · 1.0s\n"
        self.assertEqual(checker.parse_active_subagent_descriptions(scrollback), [])


class ComparisonTests(unittest.TestCase):
    def test_exact_and_partial_matches_are_not_findings(self):
        result = checker.compare_descriptions(
            ["inspect router state", "check API"],
            ["check API mapping", "inspect router state"],
        )
        self.assertEqual(result["flags"], [])
        self.assertTrue(result["has_overlap"])

    def test_matching_is_multiset_and_order_independent(self):
        result = checker.compare_descriptions(["same", "same"], ["same"])
        self.assertEqual(
            result["flags"],
            [{"kind": "tmux_description_absent_from_router", "description": "same"}],
        )

    def test_no_overlap_is_the_only_global_finding(self):
        result = checker.compare_descriptions(["read logs"], ["write docs"])
        self.assertEqual(result["flags"], [{"kind": "no_description_overlap"}, {"kind": "tmux_description_absent_from_router", "description": "read logs"}])

    def test_empty_and_router_only_descriptions_do_not_flag(self):
        self.assertEqual(checker.compare_descriptions([], ["router only"])["flags"], [])
        self.assertEqual(checker.compare_descriptions(["   "], ["router only"])["flags"], [])

    def test_one_missing_child_does_not_flag_global_no_overlap(self):
        result = checker.compare_descriptions(["present", "missing"], ["present"])
        self.assertEqual(result["flags"], [{"kind": "tmux_description_absent_from_router", "description": "missing"}])


class RouterTests(unittest.TestCase):
    def test_router_fetch_uses_inventory_and_agents_state_without_model_payload(self):
        calls = []

        def request(url, timeout):
            calls.append(url)
            if url.endswith("/v1/tmux/windows"):
                return {"session": "mecha", "windows": [{"name": "worker-a", "target": "mecha:worker-a"}]}
            self.assertTrue(url.endswith("/v1/agents?models=omit"))
            return {"agents": [{"agent_id": "worker-a", "subagents": [{"description": "private child prompt"}]}]}

        with patch.object(checker, "_request_json", side_effect=request):
            result = checker.fetch_router_state("http://127.0.0.1:8765", 1)
        self.assertEqual({item["name"] for item in result.windows}, {"worker-a"})
        self.assertEqual(result.agents[0]["agent_id"], "worker-a")
        self.assertEqual(set(calls), {"http://127.0.0.1:8765/v1/tmux/windows", "http://127.0.0.1:8765/v1/agents?models=omit"})


class IntegrationShapeTests(unittest.TestCase):
    def test_capture_uses_router_window_name_and_existing_read_only_helper(self):
        with patch.object(checker, "capture_pane", return_value="● Agents\n└─ ⠋ General-purpose  read logs · ↻1 · 0.1s\n") as capture:
            result = checker.capture_window("worker-a", lines=80, timeout=1.25)
        capture.assert_called_once_with("worker-a", lines=80, timeout=1.25)
        self.assertEqual(result.name, "worker-a")
        self.assertEqual(result.descriptions, ("read logs",))

    def test_run_check_captures_and_router_request_concurrently(self):
        started = []
        lock = threading.Lock()

        def capture(window, lines, timeout):
            with lock:
                started.append("capture:" + window)
            time.sleep(0.05)
            return checker.CaptureResult(window, window, ("same",))

        def router(url, timeout):
            with lock:
                started.append("router")
            time.sleep(0.05)
            return checker.RouterResult(
                (
                    {"agent_id": "worker-a", "subagents": [{"description": "same"}]},
                    {"agent_id": "worker-b", "subagents": []},
                ),
                (
                    {"name": "worker-a", "target": "mecha:worker-a"},
                    {"name": "worker-b", "target": "mecha:worker-b"},
                ),
            )

        report = checker.run_check(["worker-a", "worker-b"], capture=capture, router=router)
        self.assertEqual(set(started), {"capture:worker-a", "capture:worker-b", "router"})
        self.assertEqual(report["findings"], [{"window_id": "worker-b", "kind": "tmux_description_absent_from_router", "description": "same"}])

    def test_report_does_not_emit_scrollback_or_router_descriptions(self):
        def capture(window, lines, timeout):
            return checker.CaptureResult(window, window, ("visible label",))

        def router(url, timeout):
            return checker.RouterResult(
                ({"agent_id": "worker-a", "subagents": [{"description": "router private prompt"}]},),
                ({"name": "worker-a", "target": "mecha:worker-a"},),
            )

        report = checker.run_check(["worker-a"], capture=capture, router=router)
        encoded = json.dumps(report)
        self.assertNotIn("router private prompt", encoded)
        self.assertNotIn("ordinary output", encoded)
        self.assertEqual([item["kind"] for item in report["findings"]], ["no_description_overlap", "tmux_description_absent_from_router"])

    def test_overflow_is_operational_error_and_suppresses_comparison(self):
        def capture(window, lines, timeout):
            return checker.CaptureResult(window, window, ("visible",), incomplete=True)

        def router(url, timeout):
            return checker.RouterResult(
                ({"agent_id": "worker-a", "subagents": [{"description": "visible"}]},),
                ({"name": "worker-a", "target": "mecha:worker-a"},),
            )

        report = checker.run_check(["worker-a"], capture=capture, router=router)
        self.assertEqual(report["findings"], [])
        self.assertEqual(report["errors"][0]["kind"], "capture_error")
        self.assertIn("overflowed", report["errors"][0]["message"])


if __name__ == "__main__":
    unittest.main()
