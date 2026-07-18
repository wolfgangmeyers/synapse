#!/usr/bin/env python3
"""Focused tests for Mailbox V2 worker-launch guidance."""

import contextlib
import io
import os
import pathlib
import shlex
import tempfile
import unittest
import uuid
from unittest.mock import patch

import worker_launch


class WorkerLaunchMailboxGuidanceTest(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmpdir.cleanup)
        self.tmp_path = pathlib.Path(self.tmpdir.name)

    def test_pi_session_id_remains_a_stable_uuid5(self):
        self.assertEqual(
            worker_launch.pi_session_id("/tmp/worktree", "worker"),
            "740eb880-f670-5970-8e5f-3b7d401758c2",
        )

    def test_launch_output_uses_mailbox_v2_without_v1_reminder_or_terminal_sender(self):
        _, output = self._run_main("mailbox-worker")

        self.assertIn("mailbox_send.py --from supervisor", output)
        self.assertIn("--to mailbox-worker", output)
        self.assertIn("managed Pi lifecycle/worker_launch.py", output)
        self.assertNotIn("worker_send.py", output)
        self.assertNotIn("set_reminder.py", output)
        self.assertNotIn("--- Reminders ---", output)

    def test_main_defaults_to_pinned_pi_terra(self):
        extension = self._pi_mailbox_extension()
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, output = self._run_main("pi-worker", agent=None)

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--provider") + 1], "openai-codex")
        self.assertEqual(command[command.index("--model") + 1], "gpt-5.6-terra")
        self.assertIn(str(extension), command)
        self.assertIn("--no-extensions", command)
        session_id = command[command.index("--session-id") + 1]
        self.assertEqual(session_id, worker_launch.pi_session_id(self.tmpdir.name, "pi-worker"))
        self.assertEqual(str(uuid.UUID(session_id)), session_id)
        self.assertIn(f"--session {session_id}", output)

    def test_managed_pi_command_uses_only_explicit_subagents_and_mailbox_extensions(self):
        extension = self._pi_mailbox_extension()
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", agent="pi")

        command = shlex.split(commands[0])
        extensions = [
            command[index + 1]
            for index, part in enumerate(command)
            if part == "--extension"
        ]
        self.assertEqual(
            extensions,
            [worker_launch.PI_SUBAGENTS_EXTENSION, str(extension)],
        )
        self.assertIn("--no-extensions", command)

    def test_main_uses_absolute_subagents_extension_override_before_mailbox(self):
        mailbox_extension = self._pi_mailbox_extension()
        subagents_extension = self.tmp_path / "pi-subagents.js"
        subagents_extension.write_text("export default () => {};\n", encoding="utf-8")
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(mailbox_extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
            worker_launch.PI_SUBAGENTS_EXTENSION_ENV: str(subagents_extension),
        }, clear=True):
            commands, _ = self._run_main("pi-worker", agent="pi")

        command = shlex.split(commands[0])
        extensions = [command[index + 1] for index, value in enumerate(command) if value == "--extension"]
        self.assertEqual(extensions, [str(subagents_extension), str(mailbox_extension)])

    def test_main_rejects_invalid_subagents_extension_override_before_launch(self):
        mailbox_extension = self._pi_mailbox_extension()
        directory = self.tmp_path / "pi-subagents-directory"
        directory.mkdir()
        cases = {
            "empty": "",
            "relative": "pi-subagents.js",
            "missing": str(self.tmp_path / "missing.js"),
            "directory": str(directory),
        }
        for name, override in cases.items():
            argv = ["worker_launch.py", "--window", "pi-worker", "--dir", self.tmpdir.name]
            with self.subTest(name=name), patch.dict(os.environ, {
                worker_launch.PI_MAILBOX_EXTENSION_ENV: str(mailbox_extension),
                worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
                worker_launch.PI_SUBAGENTS_EXTENSION_ENV: override,
            }, clear=True), \
                 patch.object(worker_launch.sys, "argv", argv), \
                 patch.object(worker_launch, "window_exists") as window_exists, \
                 contextlib.redirect_stderr(stderr := io.StringIO()), \
                 self.assertRaises(SystemExit) as raised:
                worker_launch.main()

            self.assertEqual(raised.exception.code, 1)
            self.assertIn(worker_launch.PI_SUBAGENTS_EXTENSION_ENV, stderr.getvalue())
            window_exists.assert_not_called()

    def test_main_maps_resume_to_pi_session(self):
        extension = self._pi_mailbox_extension()
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", "--resume", "session-123", agent="pi")

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--session") + 1], "session-123")
        self.assertNotIn("resume", command)

    def test_pi_launch_exports_router_configuration_but_not_identity(self):
        extension = self._pi_mailbox_extension()
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", agent="pi")

        self.assertIn("export MECHA_ROUTER_URL=http://127.0.0.1:8765", commands[0])
        self.assertNotIn("MECHA_AGENT_WINDOW", commands[0])
        self.assertNotIn("MECHA_WORKER_WINDOW", commands[0])

    def test_main_refuses_pi_global_extension_discovery(self):
        argv = ["worker_launch.py", "--window", "pi-worker", "--dir", self.tmpdir.name]
        stderr = io.StringIO()
        with patch.object(worker_launch.sys, "argv", argv), \
             patch.dict(os.environ, {worker_launch.PI_MAILBOX_EXTENSION_ENV: ""}, clear=False), \
             contextlib.redirect_stderr(stderr), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn(worker_launch.PI_MAILBOX_EXTENSION_ENV, stderr.getvalue())

    def _pi_mailbox_extension(self):
        extension = self.tmp_path / "mecha-messaging.js"
        extension.write_text("export default () => {};\n", encoding="utf-8")
        return extension

    def _run_main(self, window, *extra_args, agent="claude"):
        argv = ["worker_launch.py", "--window", window, "--dir", self.tmpdir.name]
        if agent is not None:
            argv.extend(["--agent", agent])
        argv.extend(extra_args)
        commands = []
        output = io.StringIO()
        with patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists", return_value=False), \
             patch.object(worker_launch, "ensure_session", return_value=False), \
             patch.object(worker_launch, "new_window"), \
             patch.object(worker_launch, "send_text", side_effect=lambda _window, command: commands.append(command)), \
             patch.object(worker_launch, "send_enter"), \
             patch.object(worker_launch.time, "sleep"), \
             patch.object(worker_launch.os, "makedirs"), \
             patch.object(worker_launch.os.path, "exists", return_value=True), \
             contextlib.redirect_stdout(output):
            worker_launch.main()
        return commands, output.getvalue()


if __name__ == "__main__":
    unittest.main()
