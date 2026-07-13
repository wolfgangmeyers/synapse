#!/usr/bin/env python3
"""Tests for the pinned Pi supervisor command."""

import contextlib
import io
import os
import pathlib
import shlex
import tempfile
import unittest
from unittest.mock import patch

import supervisor_start


class SupervisorStartTest(unittest.TestCase):
    def test_pi_uses_only_the_explicit_mailbox_extension(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            extension = pathlib.Path(tmpdir) / "mecha-messaging.js"
            extension.write_text("export default () => {};\n", encoding="utf-8")

            command = shlex.split(
                supervisor_start.agent_command(
                    "pi",
                    "bootstrap",
                    str(extension),
                )
            )

        self.assertEqual(command[0], "pi")
        self.assertEqual(command[command.index("--provider") + 1], "openai-codex")
        self.assertEqual(command[command.index("--model") + 1], "gpt-5.6-terra")
        self.assertEqual(command[command.index("--name") + 1], "supervisor")
        extension_index = command.index("--extension")
        self.assertEqual(command[extension_index - 1], "--no-extensions")
        self.assertEqual(command[extension_index + 1], str(extension))
        self.assertEqual(command.count("--extension"), 1)
        self.assertIn("--approve", command)

    def test_supervisor_launch_exports_router_configuration_but_not_identity(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            extension = pathlib.Path(tmpdir) / "mecha-messaging.js"
            extension.write_text("export default () => {};\n", encoding="utf-8")
            commands = []
            with patch.object(
                supervisor_start.sys,
                "argv",
                ["supervisor_start.py", "--dir", tmpdir],
            ), patch.dict(
                os.environ,
                {
                    supervisor_start.PI_MAILBOX_EXTENSION_ENV: str(extension),
                    supervisor_start.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
                },
                clear=False,
            ), patch.object(supervisor_start, "session_exists", return_value=False), patch.object(
                supervisor_start, "ensure_session"
            ), patch.object(
                supervisor_start, "send_text", side_effect=lambda _window, command: commands.append(command)
            ), patch.object(supervisor_start, "send_enter"), patch.object(supervisor_start.time, "sleep"):
                supervisor_start.main()

        self.assertIn("export MECHA_ROUTER_URL=http://127.0.0.1:8765", commands[0])
        self.assertNotIn("MECHA_AGENT_WINDOW", commands[0])
        self.assertNotIn("MECHA_WORKER_WINDOW", commands[0])

    def test_missing_extension_exits_before_creating_tmux_session(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            stderr = io.StringIO()
            with patch.object(
                supervisor_start.sys,
                "argv",
                ["supervisor_start.py", "--dir", tmpdir],
            ), patch.dict(
                os.environ,
                {supervisor_start.PI_MAILBOX_EXTENSION_ENV: ""},
                clear=False,
            ), patch.object(
                supervisor_start,
                "ensure_session",
            ) as ensure_session, contextlib.redirect_stderr(stderr), self.assertRaises(
                SystemExit
            ) as raised:
                supervisor_start.main()

        self.assertEqual(raised.exception.code, 1)
        ensure_session.assert_not_called()
        self.assertIn(supervisor_start.PI_MAILBOX_EXTENSION_ENV, stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
