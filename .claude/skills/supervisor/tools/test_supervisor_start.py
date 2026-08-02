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
        self.assertEqual(command[command.index("--model") + 1], "gpt-5.5")
        self.assertEqual(command[command.index("--name") + 1], "supervisor")
        extension_index = command.index("--extension")
        self.assertNotIn("--no-extensions", command)
        self.assertEqual(command[extension_index + 1], str(extension))
        self.assertEqual(command.count("--extension"), 1)
        self.assertIn("--approve", command)

    def test_no_env_uses_stable_extension_fallback(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            extension = pathlib.Path(tmpdir) / "mecha-messaging.js"
            extension.write_text("export default () => {};\n", encoding="utf-8")
            with patch.dict(os.environ, {}, clear=True), patch.object(
                supervisor_start, "PI_MAILBOX_EXTENSION_PATH", str(extension)
            ):
                self.assertEqual(supervisor_start.resolve_pi_mailbox_extension(), str(extension))

    def test_explicit_extension_override_wins_over_stable_path(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            override = pathlib.Path(tmpdir) / "override.js"
            override.write_text("export default () => {};\n", encoding="utf-8")
            with patch.dict(
                os.environ,
                {supervisor_start.PI_MAILBOX_EXTENSION_ENV: str(override)},
                clear=True,
            ), patch.object(supervisor_start, "PI_MAILBOX_EXTENSION_PATH", "/missing/stable.js"):
                self.assertEqual(supervisor_start.resolve_pi_mailbox_extension(), str(override))

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
                {supervisor_start.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765"},
                clear=True,
            ), patch.object(supervisor_start, "PI_MAILBOX_EXTENSION_PATH", str(extension)), patch.object(
                supervisor_start, "session_exists", return_value=False
            ), patch.object(supervisor_start, "ensure_session"), patch.object(
                supervisor_start, "send_text", side_effect=lambda _window, command: commands.append(command)
            ), patch.object(supervisor_start, "send_enter"), patch.object(supervisor_start.time, "sleep"):
                supervisor_start.main()

        self.assertIn("export MECHA_ROUTER_URL=http://127.0.0.1:8765", commands[0])
        self.assertIn(f"export {supervisor_start.PI_MODEL_KEY_ENV}=openai-codex/gpt-5.5", commands[0])
        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--extension") + 1], str(extension))
        self.assertNotIn("MECHA_AGENT_WINDOW", commands[0])
        self.assertNotIn("MECHA_WORKER_WINDOW", commands[0])

    def test_supervisor_launch_exports_validated_compaction_policy(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            extension = pathlib.Path(tmpdir) / "mecha-messaging.js"
            extension.write_text("export default () => {};\n", encoding="utf-8")
            commands = []
            policy = '{"postCompactionNotification":"resume after compact","levels":[40,50,90],"models":{"openai-codex/gpt-5.6-sol":{"levels":[20,30,{"percent":45,"mandatory":true}]}}}'
            with patch.object(
                supervisor_start.sys,
                "argv",
                ["supervisor_start.py", "--dir", tmpdir, "--compaction-policy", policy],
            ), patch.dict(
                os.environ,
                {supervisor_start.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765"},
                clear=True,
            ), patch.object(supervisor_start, "PI_MAILBOX_EXTENSION_PATH", str(extension)), patch.object(
                supervisor_start, "session_exists", return_value=False
            ), patch.object(supervisor_start, "ensure_session"), patch.object(
                supervisor_start, "send_text", side_effect=lambda _window, command: commands.append(command)
            ), patch.object(supervisor_start, "send_enter"), patch.object(supervisor_start.time, "sleep"):
                supervisor_start.main()

        self.assertIn(f"export {supervisor_start.PI_COMPACTION_POLICY_ENV}=", commands[0])
        self.assertIn(shlex.quote(policy), commands[0])

    def test_invalid_compaction_policy_exits_before_creating_tmux_session(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            extension = pathlib.Path(tmpdir) / "mecha-messaging.js"
            extension.write_text("export default () => {};\n", encoding="utf-8")
            stderr = io.StringIO()
            with patch.object(
                supervisor_start.sys,
                "argv",
                ["supervisor_start.py", "--dir", tmpdir, "--compaction-policy", '{"tiers":[{"maxContextTokens":1000000,"levels":[0,50]}]}'],
            ), patch.dict(
                os.environ,
                {supervisor_start.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765"},
                clear=True,
            ), patch.object(supervisor_start, "PI_MAILBOX_EXTENSION_PATH", str(extension)), patch.object(
                supervisor_start, "ensure_session"
            ) as ensure_session, contextlib.redirect_stderr(stderr), self.assertRaises(
                SystemExit
            ) as raised:
                supervisor_start.main()

        self.assertEqual(raised.exception.code, 1)
        ensure_session.assert_not_called()
        self.assertIn("percentages", stderr.getvalue())

    def test_invalid_stable_extension_exits_before_creating_tmux_session(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            missing = pathlib.Path(tmpdir) / "missing.js"
            stderr = io.StringIO()
            with patch.object(
                supervisor_start.sys,
                "argv",
                ["supervisor_start.py", "--dir", tmpdir],
            ), patch.dict(
                os.environ,
                {supervisor_start.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765"},
                clear=True,
            ), patch.object(supervisor_start, "PI_MAILBOX_EXTENSION_PATH", str(missing)), patch.object(
                supervisor_start, "ensure_session"
            ) as ensure_session, contextlib.redirect_stderr(stderr), self.assertRaises(
                SystemExit
            ) as raised:
                supervisor_start.main()

        self.assertEqual(raised.exception.code, 1)
        ensure_session.assert_not_called()
        self.assertIn("regular, non-symlink", stderr.getvalue())

    def test_empty_env_override_exits_before_creating_tmux_session(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            stable = pathlib.Path(tmpdir) / "mecha-messaging.js"
            stable.write_text("export default () => {};\n", encoding="utf-8")
            stderr = io.StringIO()
            with patch.object(
                supervisor_start.sys,
                "argv",
                ["supervisor_start.py", "--dir", tmpdir],
            ), patch.dict(
                os.environ,
                {
                    supervisor_start.PI_MAILBOX_EXTENSION_ENV: "",
                    supervisor_start.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
                },
                clear=True,
            ), patch.object(supervisor_start, "PI_MAILBOX_EXTENSION_PATH", str(stable)), patch.object(
                supervisor_start,
                "ensure_session",
            ) as ensure_session, contextlib.redirect_stderr(stderr), self.assertRaises(
                SystemExit
            ) as raised:
                supervisor_start.main()

        self.assertEqual(raised.exception.code, 1)
        ensure_session.assert_not_called()
        self.assertIn(supervisor_start.PI_MAILBOX_EXTENSION_ENV, stderr.getvalue())

    def test_symlinked_env_override_exits_before_creating_tmux_session(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            target = pathlib.Path(tmpdir) / "extension.js"
            target.write_text("export default () => {};\n", encoding="utf-8")
            symlink = pathlib.Path(tmpdir) / "symlink.js"
            symlink.symlink_to(target)
            stderr = io.StringIO()
            with patch.object(
                supervisor_start.sys,
                "argv",
                ["supervisor_start.py", "--dir", tmpdir],
            ), patch.dict(
                os.environ,
                {
                    supervisor_start.PI_MAILBOX_EXTENSION_ENV: str(symlink),
                    supervisor_start.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
                },
                clear=True,
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
