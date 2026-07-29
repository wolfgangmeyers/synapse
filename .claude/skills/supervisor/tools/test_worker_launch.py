#!/usr/bin/env python3
"""Focused tests for Mailbox V2 worker-launch guidance."""

import contextlib
import io
import json
import os
import pathlib
import re
import shlex
import subprocess
import sys
import tempfile
import textwrap
import unittest
import uuid
from unittest.mock import patch

import worker_capture
import worker_launch


class WorkerLaunchMailboxGuidanceTest(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmpdir.cleanup)
        self.tmp_path = pathlib.Path(self.tmpdir.name)
        # Pin runtime resolution away from the developer's machine-local config so
        # default-runtime expectations stay deterministic. Tests that exercise
        # machine-default resolution override this with their own temp config.
        missing_config = self.tmp_path / "absent-worker-runtime.json"
        env_patch = patch.dict(
            os.environ,
            {"MECHA_WORKER_RUNTIME_CONFIG": str(missing_config)},
            clear=False,
        )
        env_patch.start()
        self.addCleanup(env_patch.stop)

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

    def test_main_defaults_to_pinned_pi_claude_opus_5(self):
        extension = self._pi_mailbox_extension()
        with patch.dict(os.environ, {
            worker_launch.PI_MAILBOX_EXTENSION_ENV: str(extension),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, output = self._run_main("pi-worker", agent=None)

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--provider") + 1], "anthropic")
        self.assertEqual(command[command.index("--model") + 1], "claude-opus-5")
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
        self.assertEqual(command[command.index("--provider") + 1], "anthropic")
        self.assertEqual(command[command.index("--model") + 1], "claude-opus-5")
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
        self.assertIn(f"export {worker_launch.PI_MODEL_KEY_ENV}=anthropic/claude-opus-5", commands[0])
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

    def test_claude_launch_through_symlinked_tools_dir_hands_node_a_resolved_sidecar_path(self):
        """Paths handed to other processes must be canonical, not as-invoked.

        ~/.claude/skills/supervisor is a symlink into the repo, so a sidecar path built
        from an unresolved __file__ carries the symlink through to node. This worktree
        has no symlink component, so the test builds one and re-imports worker_launch
        through it; asserting on os.path.realpath alone would not reproduce the failure.
        """
        symlinked_tools = self.tmp_path / "supervisor-tools-symlink"
        symlinked_tools.symlink_to(pathlib.Path(worker_launch.__file__).resolve().parent)

        launch_script = symlinked_tools / "worker_launch.py"
        self.assertTrue(launch_script.is_file(), "symlinked tools dir must expose worker_launch.py")

        snippet = textwrap.dedent(
            """
            import contextlib, io, json, os, sys
            from unittest.mock import patch

            sys.path.insert(0, sys.argv[1])
            import worker_launch

            commands = []
            argv = ["worker_launch.py", "--window", "claude-worker",
                    "--dir", sys.argv[2], "--agent", "claude"]
            with patch.object(worker_launch.sys, "argv", argv), \\
                 patch.object(worker_launch, "window_exists", return_value=False), \\
                 patch.object(worker_launch, "ensure_session", return_value=False), \\
                 patch.object(worker_launch, "new_window"), \\
                 patch.object(worker_launch, "send_text",
                              side_effect=lambda _w, command: commands.append(command)), \\
                 patch.object(worker_launch, "send_enter"), \\
                 patch.object(worker_launch.time, "sleep"), \\
                 patch.object(worker_launch.os, "makedirs"), \\
                 patch.object(worker_launch.os.path, "exists", return_value=True), \\
                 contextlib.redirect_stdout(io.StringIO()):
                worker_launch.main()

            print(json.dumps({
                "module_file": worker_launch.__file__,
                "sidecar": worker_launch.CLAUDE_SIDECAR_SCRIPT,
                "command": commands[0],
            }))
            """
        )
        result = subprocess.run(
            [sys.executable, "-c", snippet, str(symlinked_tools), self.tmpdir.name],
            capture_output=True,
            text=True,
            timeout=60,
            env={**os.environ,
                 worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
                 "PYTHONDONTWRITEBYTECODE": "1"},
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        launched = json.loads(result.stdout.strip().splitlines()[-1])

        # The launcher really was imported through the symlink, so the defect could fire.
        self.assertTrue(launched["module_file"].startswith(str(symlinked_tools)))
        self.assertNotEqual(launched["module_file"], os.path.realpath(launched["module_file"]))

        sidecar = launched["sidecar"]
        self.assertEqual(sidecar, os.path.realpath(sidecar))
        self.assertNotIn(str(symlinked_tools), sidecar)
        self.assertTrue(os.path.isfile(sidecar))

        # And the resolved path is what actually reaches node in the launch command.
        command = shlex.split(launched["command"].replace("(", " ").replace(")", " "))
        self.assertIn(sidecar, command)
        self.assertNotIn(str(symlinked_tools / "claude_sidecar.mjs"), launched["command"])

    def test_main_refuses_pi_global_extension_discovery(self):
        argv = ["worker_launch.py", "--window", "pi-worker", "--dir", self.tmpdir.name]
        stderr = io.StringIO()
        with patch.object(worker_launch.sys, "argv", argv), \
             patch.dict(os.environ, {worker_launch.PI_MAILBOX_EXTENSION_ENV: ""}, clear=False), \
             patch.object(worker_launch, "window_exists", return_value=False), \
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


class WorkerCaptureDocumentedFlagsTest(unittest.TestCase):
    """Every worker_capture.py flag the docs put in front of an agent must parse.

    A flag the parser rejects costs the reader an argparse exit 2 and no capture output,
    so the docs are pinned to the real parser rather than to a copy of its flag list.

    Scope: flags attributable to this tool on the same line, across the three shapes the
    docs use — shell invocation, bracketed synopsis, and quoted argv list — in markdown
    and JSON workflow bodies. A flag named in prose on a line that does not name a tool
    is out of reach, because nothing on that line says which tool it belongs to.
    """

    TOOL = "worker_capture.py"

    def _flag_is_accepted(self, flag):
        """True when the real parser accepts the flag.

        Probes behavior through argparse's public API rather than reading private
        action tables: a documented flag is broken when it makes worker_capture.py
        fail, whether argparse reports it unrecognized or exits outright.
        """
        parser = worker_capture.build_parser()
        try:
            with contextlib.redirect_stderr(io.StringIO()):
                _, unrecognized = parser.parse_known_args(["--window", "probe", flag, "1"])
        except SystemExit:
            return False
        return flag not in unrecognized

    @staticmethod
    def _repo_docs(repo_root):
        """Markdown and JSON files git would track, newest edits included.

        Enumerating through git honors .gitignore, so a scratch or generated file that
        happens to mention this tool cannot fail the suite. `--others
        --exclude-standard` still includes a doc that exists but has not been staged yet.
        """
        listing = subprocess.run(
            ["git", "-C", str(repo_root), "ls-files", "--cached", "--others",
             "--exclude-standard", "-z"],
            capture_output=True,
            text=True,
            check=True,
            timeout=60,
        )
        return sorted(
            repo_root / relative
            for relative in listing.stdout.split("\0")
            if relative.endswith((".md", ".json")) and (repo_root / relative).is_file()
        )

    def _documented_flags(self):
        """Map every flag the docs attribute to this tool -> where it appears.

        Attributes flags per tool within a line, so a flag belonging to a neighbouring
        tool is not blamed on this one. Scans whole lines rather than only shell-style
        invocations, because the docs also use a bracketed synopsis
        (`--window WINDOW [--lines N]`) and a quoted argv-list form — the two shapes a
        maintainer is most likely to add a flag to. Workflow bodies live in JSON, so
        both extensions are in scope.
        """
        repo_root = pathlib.Path(worker_capture.__file__).resolve().parents[4]
        tool_token = re.compile(r"[\w./~{}$-]*\.py")
        documented = {}
        for doc in self._repo_docs(repo_root):
            # JSON stores bodies as one long escaped line; split on \n so line numbers
            # and per-line tool attribution stay meaningful.
            text = doc.read_text(encoding="utf-8").replace("\\n", "\n")
            for number, line in enumerate(text.splitlines(), 1):
                tokens = list(tool_token.finditer(line))
                for index, token in enumerate(tokens):
                    if not token.group(0).endswith(self.TOOL):
                        continue
                    end = tokens[index + 1].start() if index + 1 < len(tokens) else len(line)
                    for flag in re.findall(r"--[\w-]+", line[token.end():end]):
                        documented.setdefault(flag, f"{doc.relative_to(repo_root)}:{number}")
        return documented

    def test_probe_distinguishes_real_flags_from_rejected_ones(self):
        # Without this the drift test could pass vacuously on a broken probe.
        self.assertTrue(self._flag_is_accepted("--window"))
        self.assertTrue(self._flag_is_accepted("--tail"))
        self.assertFalse(self._flag_is_accepted("--force-tmux"))

    def test_scanner_sees_the_doc_forms_that_carry_flags(self):
        # Blind spots here are how a rejected flag slips back into the docs unnoticed.
        # Subset, not equality: documenting a newly added flag must not fail the suite.
        documented = self._documented_flags()
        self.assertLessEqual(
            {"--window", "--lines", "--tail"},
            set(documented),
            f"scanner stopped seeing known documented flags: {documented}",
        )

    def test_docs_only_use_flags_worker_capture_accepts(self):
        unsupported = {
            flag: where
            for flag, where in self._documented_flags().items()
            if not self._flag_is_accepted(flag)
        }
        self.assertEqual(
            unsupported,
            {},
            f"docs reference {self.TOOL} flags argparse rejects: {unsupported}",
        )


if __name__ == "__main__":
    unittest.main()
