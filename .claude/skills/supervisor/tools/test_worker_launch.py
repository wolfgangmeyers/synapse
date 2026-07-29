#!/usr/bin/env python3
"""Focused tests for Mailbox V2 worker-launch guidance."""

import contextlib
import io
import json
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
        self.assertIn("managed worker lifecycle/worker_launch.py", output)
        self.assertNotIn("worker_send.py", output)
        self.assertNotIn("set_reminder.py", output)
        self.assertNotIn("--- Reminders ---", output)

    def test_main_defaults_to_pinned_pi_gpt_55(self):
        extension = self._pi_mailbox_extension()
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, output = self._run_main("pi-worker", agent=None)

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--provider") + 1], "openai-codex")
        self.assertEqual(command[command.index("--model") + 1], "gpt-5.5")
        self.assertIn(str(extension), command)
        self.assertNotIn("--no-extensions", command)
        session_id = command[command.index("--session-id") + 1]
        self.assertEqual(session_id, worker_launch.pi_session_id(self.tmpdir.name, "pi-worker"))
        self.assertEqual(str(uuid.UUID(session_id)), session_id)
        self.assertIn(f"--session {session_id}", output)

    def test_machine_config_can_default_to_claude_without_pi_flags(self):
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "claude"}), encoding="utf-8")
        with patch.dict(os.environ, {
            "MECHA_WORKER_RUNTIME_CONFIG": str(config),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, output = self._run_main("claude-worker", agent=None)

        self.assertIn("node", commands[0])
        self.assertIn("claude_sidecar.mjs", commands[0])
        self.assertIn("--agent-id claude-worker", commands[0])
        self.assertIn("claude --dangerously-skip-permissions", commands[0])
        self.assertNotIn("--provider", commands[0])
        self.assertNotIn(worker_launch.PI_MODEL_KEY_ENV, commands[0])
        self.assertIn("Worker (claude) launched", output)

    def test_explicit_agent_overrides_machine_default_runtime(self):
        extension = self._pi_mailbox_extension()
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "claude"}), encoding="utf-8")
        with patch.dict(os.environ, {
            "MECHA_WORKER_RUNTIME_CONFIG": str(config),
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", agent="pi")

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--provider") + 1], "openai-codex")
        self.assertEqual(command[command.index("--model") + 1], "gpt-5.5")
        self.assertNotIn("claude_sidecar.mjs", commands[0])

    def test_invalid_machine_default_runtime_fails_before_launch(self):
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "bad"}), encoding="utf-8")
        argv = ["worker_launch.py", "--window", "worker", "--dir", self.tmpdir.name]
        with patch.dict(os.environ, {"MECHA_WORKER_RUNTIME_CONFIG": str(config)}, clear=True), \
             patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists") as window_exists, \
             contextlib.redirect_stderr(stderr := io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn("default_agent", stderr.getvalue())
        window_exists.assert_not_called()

    def test_invalid_claude_router_url_fails_before_tmux_side_effects(self):
        argv = ["worker_launch.py", "--window", "claude-worker", "--dir", self.tmpdir.name, "--agent", "claude"]
        with patch.dict(os.environ, {worker_launch.PI_ROUTER_URL_ENV: "http://example.com/path"}, clear=True), \
             patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists") as window_exists, \
             patch.object(worker_launch, "ensure_session") as ensure_session, \
             patch.object(worker_launch, "new_window") as new_window, \
             contextlib.redirect_stderr(stderr := io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn(worker_launch.PI_ROUTER_URL_ENV, stderr.getvalue())
        window_exists.assert_not_called()
        ensure_session.assert_not_called()
        new_window.assert_not_called()

    def test_managed_pi_command_uses_default_extensions_with_explicit_mailbox(self):
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
        self.assertEqual(extensions, [str(extension)])
        self.assertNotIn("--no-extensions", command)

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

    def test_pi_launch_exports_router_configuration_but_not_identity_or_settled_recipient_by_default(self):
        extension = self._pi_mailbox_extension()
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", agent="pi")

        self.assertIn("export MECHA_ROUTER_URL=http://127.0.0.1:8765", commands[0])
        self.assertIn(f"export {worker_launch.PI_MODEL_KEY_ENV}=openai-codex/gpt-5.5", commands[0])
        self.assertNotIn(worker_launch.PI_NOTIFY_ON_SETTLED_ENV, commands[0])
        self.assertNotIn("MECHA_AGENT_WINDOW", commands[0])
        self.assertNotIn("MECHA_WORKER_WINDOW", commands[0])

    def test_pi_launch_exports_validated_settled_recipient(self):
        extension = self._pi_mailbox_extension()
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main(
                "pi-worker", "--notify-on-settled", "supervisor", agent="pi"
            )

        self.assertIn("export MECHA_ROUTER_URL=http://127.0.0.1:8765", commands[0])
        self.assertIn("export MECHA_PI_NOTIFY_ON_SETTLED=supervisor", commands[0])

    def test_pi_launch_exports_validated_compaction_policy(self):
        extension = self._pi_mailbox_extension()
        policy = '{"postCompactionNotification":"resume after compact","levels":[40,50,90],"models":{"openai-codex/gpt-5.6-sol":{"levels":[20,30,{"percent":45,"mandatory":true}]}}}'
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main(
                "pi-worker", "--compaction-policy", policy, agent="pi"
            )

        self.assertIn("export MECHA_ROUTER_URL=http://127.0.0.1:8765", commands[0])
        self.assertIn(f"export {worker_launch.PI_COMPACTION_POLICY_ENV}=", commands[0])
        self.assertIn(shlex.quote(policy), commands[0])

    def test_compaction_policy_rejects_invalid_values_before_launch(self):
        extension = self._pi_mailbox_extension()
        argv = [
            "worker_launch.py", "--window", "pi-worker", "--dir", self.tmpdir.name,
            "--agent", "pi", "--compaction-policy", '{"tiers":[{"maxContextTokens":1000000,"levels":[40,100]}]}',
        ]
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=True), patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists") as window_exists, \
             contextlib.redirect_stderr(stderr := io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn("percentages", stderr.getvalue())
        window_exists.assert_not_called()

    def test_compaction_policy_rejects_non_pi_agents_before_launch(self):
        argv = [
            "worker_launch.py", "--window", "worker", "--dir", self.tmpdir.name,
            "--agent", "claude", "--compaction-policy", "40,50",
        ]
        with patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists") as window_exists, \
             contextlib.redirect_stderr(stderr := io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn("only supported for Pi", stderr.getvalue())
        window_exists.assert_not_called()

    def test_settled_recipient_rejects_non_mailbox_ids_before_launch(self):
        extension = self._pi_mailbox_extension()
        for recipient in ("", "bad recipient", "bad@recipient"):
            argv = [
                "worker_launch.py", "--window", "pi-worker", "--dir", self.tmpdir.name,
                "--agent", "pi", "--notify-on-settled", recipient,
            ]
            with self.subTest(recipient=recipient), patch.dict(os.environ, {
                worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
                worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
            }, clear=True), \
                 patch.object(worker_launch.sys, "argv", argv), \
                 patch.object(worker_launch, "window_exists") as window_exists, \
                 contextlib.redirect_stderr(stderr := io.StringIO()), \
                 self.assertRaises(SystemExit) as raised:
                worker_launch.main()

            self.assertEqual(raised.exception.code, 1)
            self.assertIn("valid Mailbox agent ID", stderr.getvalue())
            window_exists.assert_not_called()

    def test_settled_recipient_rejects_non_pi_agents_before_launch(self):
        argv = [
            "worker_launch.py", "--window", "worker", "--dir", self.tmpdir.name,
            "--agent", "claude", "--notify-on-settled", "supervisor",
        ]
        with patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists") as window_exists, \
             contextlib.redirect_stderr(stderr := io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn("only supported for Pi", stderr.getvalue())
        window_exists.assert_not_called()

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
