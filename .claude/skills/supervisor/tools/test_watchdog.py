#!/usr/bin/env python3
"""Focused tests for watchdog.py momentum-nudge behavior."""

import importlib.util
import io
import os
import sys
import unittest
from unittest import mock


WATCHDOG_PATH = os.path.join(os.path.dirname(__file__), "watchdog.py")
SPEC = importlib.util.spec_from_file_location("watchdog", WATCHDOG_PATH)
watchdog = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(watchdog)


class WatchdogTests(unittest.TestCase):
    def test_prompt_includes_window_captures_and_guidance(self):
        prompt = watchdog.build_prompt(
            {
                "supervisor": "Waiting for user input.",
                "worker-a": "Task complete. Ready to be cleaned up.",
            },
            "Decide whether to send a momentum nudge.",
        )

        self.assertIn("Waiting for user input.", prompt)
        self.assertIn("Task complete. Ready to be cleaned up.", prompt)
        self.assertIn("Decide whether to send a momentum nudge.", prompt)
        self.assertIn('{"nudge": true|false', prompt)

    def test_parse_nudge_note_schema(self):
        self.assertEqual(
            watchdog.parse_response('{"nudge": true, "note": "Check worker-a."}'),
            {"nudge": True, "note": "Check worker-a."},
        )
        self.assertEqual(
            watchdog.parse_response('```json\n{"nudge": false, "note": ""}\n```'),
            {"nudge": False, "note": ""},
        )
        self.assertIsNone(watchdog.parse_response('{"verdict": "OK"}'))

    def test_mail_sent_only_when_nudge_true(self):
        with mock.patch.object(watchdog, "send_mail", return_value=True) as send_mail:
            self.assertTrue(
                watchdog.maybe_send_nudge(
                    {"nudge": True, "note": "Decide using the Wolfgang persona."},
                    "/tmp/tools",
                )
            )
            self.assertEqual(send_mail.call_count, 1)

        with mock.patch.object(watchdog, "send_mail", return_value=True) as send_mail:
            self.assertFalse(
                watchdog.maybe_send_nudge({"nudge": False, "note": ""}, "/tmp/tools")
            )
            self.assertEqual(send_mail.call_count, 0)

    def test_send_mail_uses_supervisor_mailbox(self):
        completed = mock.Mock(returncode=0, stderr="", stdout="sent")
        with mock.patch.object(watchdog.subprocess, "run", return_value=completed) as run:
            self.assertTrue(watchdog.send_mail("Keep moving.", "/tmp/tools"))

        argv = run.call_args.args[0]
        self.assertEqual(argv[0], sys.executable)
        self.assertEqual(argv[1], "/tmp/tools/mailbox_send.py")
        self.assertIn("--to", argv)
        self.assertEqual(argv[argv.index("--to") + 1], "supervisor")
        self.assertIn("--from", argv)
        self.assertEqual(argv[argv.index("--from") + 1], "watchdog")
        self.assertIn("--subject", argv)
        self.assertEqual(argv[argv.index("--subject") + 1], "Momentum nudge")
        self.assertIn("--body", argv)
        self.assertEqual(argv[argv.index("--body") + 1], "[WATCHDOG] Keep moving.")
        self.assertEqual(run.call_args.kwargs["timeout"], 30)

    def test_main_dry_run_prints_prompt_response_and_does_not_send_real_mail(self):
        stdout = io.StringIO()
        argv = ["watchdog.py", "--dry-run"]
        with (
            mock.patch.object(sys, "argv", argv),
            mock.patch.object(watchdog, "load_llm_config", return_value={"cli": "claude", "model": "m", "extra_args": []}),
            mock.patch.object(watchdog, "session_exists", return_value=True),
            mock.patch.object(watchdog, "_capture_windows", return_value={"supervisor": "idle"}),
            mock.patch.object(watchdog, "load_watchdog_guidance", return_value="nudge guidance"),
            mock.patch.object(watchdog, "call_llm", return_value=('{"nudge": true, "note": "Keep moving."}', False)),
            mock.patch.object(watchdog, "send_mail", return_value=True) as send_mail,
            mock.patch("sys.stdout", stdout),
        ):
            watchdog.main()

        output = stdout.getvalue()
        self.assertIn("=== PROMPT ===", output)
        self.assertIn("idle", output)
        self.assertIn("nudge guidance", output)
        self.assertIn("=== LLM RESPONSE ===", output)
        self.assertIn('"nudge": true', output)
        send_mail.assert_called_once_with(
            "Keep moving.",
            os.path.dirname(os.path.abspath(WATCHDOG_PATH)),
            dry_run=True,
        )


if __name__ == "__main__":
    unittest.main()
