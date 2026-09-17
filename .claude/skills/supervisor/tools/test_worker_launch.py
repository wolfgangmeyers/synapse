#!/usr/bin/env python3
"""Focused tests for Mailbox V2 worker-launch guidance."""

import contextlib
import io
import json
import os
import pathlib
import re
import shlex
import shutil
import stat
import subprocess
import sys
import tempfile
import textwrap
import unittest
import uuid
from unittest.mock import Mock, call, patch

import worker_capture
import worker_launch


class WorkerLaunchMailboxGuidanceTest(unittest.TestCase):
    def test_supported_launchers_do_not_reference_retired_supervisor_start(self):
        """The router owns supervisor bootstrap and managed worker launch now."""
        tools_dir = pathlib.Path(__file__).resolve().parent
        repo_dir = tools_dir.parents[3]
        live_paths = [
            path for path in tools_dir.iterdir()
            if path.is_file() and not path.name.startswith("test_")
        ]
        live_paths.extend([
            repo_dir / "CLAUDE.md",
            repo_dir / ".claude" / "skills" / "install" / "SKILL.md",
        ])
        retired_launcher = "supervisor_" + "start.py"
        references = [
            str(path.relative_to(repo_dir))
            for path in live_paths
            if retired_launcher in path.read_text(encoding="utf-8")
        ]
        self.assertEqual([], references)

    def test_router_keys_submit_the_constructed_agent_command(self):
        client = Mock()
        with patch.object(worker_launch, "_tmux_rest", return_value=client):
            worker_launch.new_window("pi-worker")
            worker_launch.send_text("pi-worker", "cd /tmp && pi --model gpt-5.6-luna")
            worker_launch.send_enter("pi-worker")

        self.assertEqual(client.method_calls, [
            call.provision("pi-worker"),
            call.keys("pi-worker", "\x1b[200~"),
            call.keys("pi-worker", "cd /tmp && pi --model gpt-5.6-luna"),
            call.keys("pi-worker", "\x1b[201~"),
            call.keys("pi-worker", "\r"),
        ])

    def test_router_provision_receives_the_exact_pi_session_id(self):
        client = Mock()
        session_id = "pi-session-1"
        with patch.object(worker_launch, "_tmux_rest", return_value=client):
            worker_launch.new_window("pi-worker", session_id=session_id)
        client.provision.assert_called_once_with("pi-worker", session_id=session_id)

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

    @staticmethod
    def _pi_extensions(command):
        return [
            command[index + 1]
            for index, part in enumerate(command)
            if part == "--extension"
        ]

    def test_pi_extensions_are_canonical_existing_entrypoints(self):
        for extension in (
            worker_launch.PI_SUBAGENTS_EXTENSION_PATH,
            worker_launch.PI_MESSAGING_EXTENSION_PATH,
        ):
            with self.subTest(extension=extension):
                self.assertEqual(extension, os.path.realpath(extension))
                self.assertTrue(os.path.isfile(extension), extension)

    def test_pi_startup_does_not_load_settings_duplicates(self):
        """The real Pi CLI must start with settings that rediscover both packages."""
        pi = shutil.which("pi")
        if pi is None:
            self.skipTest("pi is not installed")

        home = self.tmp_path / "pi-startup-home"
        settings_dir = home / ".pi" / "agent"
        settings_dir.mkdir(parents=True)
        (settings_dir / "settings.json").write_text(json.dumps({
            "packages": [
                str(pathlib.Path(worker_launch.PI_SUBAGENTS_EXTENSION_PATH).parent.parent),
                str(pathlib.Path(worker_launch.PI_MESSAGING_EXTENSION_PATH).parent),
            ],
        }), encoding="utf-8")

        commands, _ = self._run_main("pi-startup-worker", agent="pi", home=home)
        launcher_argv = shlex.split(commands[0])
        pi_index = launcher_argv.index("pi")
        pi_argv = launcher_argv[pi_index:]
        session_flags = {"--session-id", "--session"}
        session_index = next(index for index, part in enumerate(pi_argv) if part in session_flags)
        del pi_argv[session_index:session_index + 2]
        pi_argv.extend(["--no-session", "--offline", "--print", "startup probe"])

        result = subprocess.run(
            [pi, *pi_argv[1:]],
            cwd=self.tmpdir.name,
            env={**os.environ, "HOME": str(home), "PI_OFFLINE": "1"},
            capture_output=True,
            text=True,
            timeout=30,
        )

        # No provider credential is needed: reaching Pi's normal provider error proves
        # extension startup completed, while duplicate package loading fails first.
        startup_output = result.stdout + result.stderr
        self.assertNotIn("conflicts with", startup_output)
        self.assertNotIn("Failed to load extension", startup_output)
        self.assertRegex(startup_output, r"No API key|offline|Offline")

    def test_main_defaults_to_pinned_pi_gpt_5_6_sol(self):
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, output = self._run_main("pi-worker", agent=None)

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--provider") + 1], "openai-codex")
        self.assertEqual(command[command.index("--model") + 1], "gpt-5.6-sol")
        self.assertIn(f"export {worker_launch.PI_MODEL_KEY_ENV}=openai-codex/gpt-5.6-sol", commands[0])
        self.assertIn("--no-extensions", command)
        self.assertEqual(self._pi_extensions(command), [
            worker_launch.PI_SUBAGENTS_EXTENSION_PATH,
            worker_launch.PI_MESSAGING_EXTENSION_PATH,
        ])
        session_id = command[command.index("--session-id") + 1]
        self.assertEqual(session_id, worker_launch.pi_session_id(self.tmpdir.name, "pi-worker"))
        self.assertEqual(str(uuid.UUID(session_id)), session_id)
        self.assertIn(f"--session {session_id}", output)

    def test_explicit_pi_model_updates_command_and_exported_model_key(self):
        model = "gpt-5.6 Luna"
        # This model is not meant to resolve to a real provider entry, so the store is
        # pinned to a path that cannot exist rather than left to read whatever
        # ~/.pi/agent/models-store.json happens to hold on the machine running the suite.
        missing_store = self.tmp_path / "no-such-models-store.json"
        with patch.object(worker_launch, "PI_MODELS_STORE_PATH", str(missing_store)), \
             patch.dict(os.environ, {
                 worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
             }, clear=False):
            commands, _ = self._run_main("pi-worker", "--model", model, agent="pi")

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--provider") + 1], "openai-codex")
        self.assertEqual(command[command.index("--model") + 1], model)
        self.assertIn(
            f"export {worker_launch.PI_MODEL_KEY_ENV}={shlex.quote(f'openai-codex/{model}')}",
            commands[0],
        )

    def _write_models_store(self, mapping):
        """Write a minimal models-store.json: {provider: [model_id, ...]}."""
        store = {
            provider: {"models": [{"id": model_id} for model_id in model_ids]}
            for provider, model_ids in mapping.items()
        }
        path = self.tmp_path / "models-store.json"
        path.write_text(json.dumps(store), encoding="utf-8")
        return path

    def test_resolve_pi_provider_keeps_the_pinned_default_with_no_model_override(self):
        # No --model at all must never consult the store, so even a store that maps
        # PI_MODEL to some other provider cannot move the untouched default launch.
        store = self._write_models_store({"amazon-bedrock": [worker_launch.PI_MODEL]})
        self.assertEqual(
            worker_launch.resolve_pi_provider(None, models_store_path=str(store)),
            worker_launch.PI_PROVIDER,
        )

    def test_resolve_pi_provider_looks_up_an_explicit_model_in_the_store(self):
        # The real-world case this exists for: a model hosted under a provider other than
        # the pinned default (amazon-bedrock's zai.glm-5, not openai-codex).
        store = self._write_models_store({
            "openai-codex": ["gpt-5.5"],
            "amazon-bedrock": ["zai.glm-5"],
        })
        self.assertEqual(
            worker_launch.resolve_pi_provider("zai.glm-5", models_store_path=str(store)),
            "amazon-bedrock",
        )

    def test_resolve_pi_provider_falls_back_when_the_model_is_not_listed(self):
        store = self._write_models_store({"openai-codex": ["gpt-5.5"]})
        self.assertEqual(
            worker_launch.resolve_pi_provider("some-future-model", models_store_path=str(store)),
            worker_launch.PI_PROVIDER,
        )

    def test_resolve_pi_provider_falls_back_when_the_store_is_missing(self):
        missing = self.tmp_path / "no-such-models-store.json"
        self.assertEqual(
            worker_launch.resolve_pi_provider("zai.glm-5", models_store_path=str(missing)),
            worker_launch.PI_PROVIDER,
        )

    def test_resolve_pi_provider_falls_back_when_the_store_is_unparsable(self):
        store = self.tmp_path / "broken-models-store.json"
        store.write_text("{not json", encoding="utf-8")
        self.assertEqual(
            worker_launch.resolve_pi_provider("zai.glm-5", models_store_path=str(store)),
            worker_launch.PI_PROVIDER,
        )

    def test_resolve_pi_provider_falls_back_when_the_store_is_not_valid_utf8(self):
        # A store caught mid-write (or written by something that is not Pi) can hold bytes
        # that are not valid UTF-8. json.JSONDecodeError alone would miss this: decoding
        # fails before json.load ever sees a string to parse, so the read raises
        # UnicodeDecodeError instead, and that must fall back too rather than crash the
        # launch.
        store = self.tmp_path / "non-utf8-models-store.json"
        store.write_bytes(b'{"amazon-bedrock": {"models": [{"id": "zai.glm-5\xff"}]}}')
        self.assertEqual(
            worker_launch.resolve_pi_provider("zai.glm-5", models_store_path=str(store)),
            worker_launch.PI_PROVIDER,
        )

    def test_launching_a_bedrock_hosted_model_uses_its_own_provider_not_the_pinned_default(self):
        """The actual bug: --model zai.glm-5 must launch with --provider amazon-bedrock.

        Before the fix, PI_PROVIDER was hardcoded onto the pi command line for every
        --model override, so this worker would register with Mailbox V2 and look launched,
        then fail its first real message with "Provider is not configured: openai-codex"
        because openai-codex was never the provider hosting zai.glm-5.
        """
        store = self._write_models_store({
            "openai-codex": ["gpt-5.5"],
            "amazon-bedrock": ["zai.glm-5"],
        })
        with patch.object(worker_launch, "PI_MODELS_STORE_PATH", str(store)), \
             patch.dict(os.environ, {
                 worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
             }, clear=False):
            commands, _ = self._run_main("pi-worker", "--model", "zai.glm-5", agent="pi")

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--provider") + 1], "amazon-bedrock")
        self.assertEqual(command[command.index("--model") + 1], "zai.glm-5")
        self.assertIn(
            f"export {worker_launch.PI_MODEL_KEY_ENV}=amazon-bedrock/zai.glm-5",
            commands[0],
        )

    def test_model_override_rejects_non_pi_agents_before_launch(self):
        argv = [
            "worker_launch.py", "--window", "worker", "--dir", self.tmpdir.name,
            "--agent", "claude", "--model", "gpt-5.6 Luna",
        ]
        with patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists") as window_exists, \
             contextlib.redirect_stderr(stderr := io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn("--model is only supported for Pi", stderr.getvalue())
        window_exists.assert_not_called()

    def test_empty_model_override_rejects_before_launch(self):
        argv = [
            "worker_launch.py", "--window", "pi-worker", "--dir", self.tmpdir.name,
            "--agent", "pi", "--model", " ",
        ]
        with patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists") as window_exists, \
             contextlib.redirect_stderr(stderr := io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn("--model must not be empty", stderr.getvalue())
        window_exists.assert_not_called()

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
        self.assertIn("sidecar_pid=$!", commands[0])
        self.assertIn("trap 'kill $sidecar_pid >/dev/null 2>&1 || true' EXIT INT TERM", commands[0])
        self.assertNotIn("claude_sidecar.mjs", commands[0].split("{", 1)[0])
        self.assertIn("claude --dangerously-skip-permissions", commands[0])
        self.assertTrue({"--extension", "--no-extensions", "-ne"}.isdisjoint(shlex.split(commands[0])))
        self.assertNotIn("--provider", commands[0])
        self.assertNotIn(worker_launch.PI_MODEL_KEY_ENV, commands[0])
        self.assertIn("Worker (claude) launched", output)

    def test_claude_sidecar_backgrounding_keeps_cd_in_the_foreground_shell(self):
        # bash binds `&` to the whole preceding AND-list, so an ungrouped background
        # sidecar would launch the agent in the wrong directory and capture the
        # subshell pid instead of the sidecar's.
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("claude-worker", agent="claude")

        foreground, _, backgrounded = commands[0].partition("{ ")
        self.assertIn(f"cd {self.tmpdir.name} && unset CLAUDECODE &&", foreground)
        self.assertNotIn("&", foreground.replace("&&", ""))

        group, _, after_group = backgrounded.partition(" }")
        self.assertIn("claude_sidecar.mjs", group)
        self.assertTrue(group.rstrip().endswith("&"), group)
        self.assertTrue(after_group.startswith(" && sidecar_pid=$!"), after_group)
        self.assertNotIn("&", after_group.replace("&&", "").replace("2>&1", ""))

    def test_claude_sidecar_argv_carries_the_actual_window_and_router(self):
        # sidecar_argv is shared with the supervisor bootstrap via _sidecar.py. Asserting only
        # that the call happened, without checking what it was called WITH, would not catch
        # a regression that always builds the argv for the wrong window or the wrong router
        # — exactly the defect class --agent-id/--window drift is meant to guard against.
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8799",
        }, clear=False):
            commands, _ = self._run_main("claude-worker-nondefault", agent="claude")

        self.assertIn(
            "--agent-id claude-worker-nondefault --window claude-worker-nondefault "
            "--router-url http://127.0.0.1:8799",
            commands[0],
        )

    def test_explicit_agent_overrides_machine_default_runtime(self):
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "claude"}), encoding="utf-8")
        with patch.dict(os.environ, {
            "MECHA_WORKER_RUNTIME_CONFIG": str(config),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", agent="pi")

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--provider") + 1], "openai-codex")
        self.assertEqual(command[command.index("--model") + 1], "gpt-5.6-sol")
        self.assertNotIn("claude_sidecar.mjs", commands[0])

    def test_cursor_model_flag_overrides_environment_and_config(self):
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "cursor", "cursor_model": "config-model"}), encoding="utf-8")
        with patch.object(worker_launch, "get_cursor_auth_token", return_value="token"), \
             patch.dict(os.environ, {
                 "MECHA_WORKER_RUNTIME_CONFIG": str(config),
                 worker_launch.CURSOR_MODEL_ENV: "env-model",
             }, clear=False):
            commands, _ = self._run_main(
                "cursor-worker", "--cursor-model", "flag-model", agent="cursor"
            )

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--model") + 1], "flag-model")

    def test_cursor_model_environment_overrides_config(self):
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "cursor", "cursor_model": "config-model"}), encoding="utf-8")
        with patch.object(worker_launch, "get_cursor_auth_token", return_value="token"), \
             patch.dict(os.environ, {
                 "MECHA_WORKER_RUNTIME_CONFIG": str(config),
                 worker_launch.CURSOR_MODEL_ENV: "env-model",
             }, clear=False):
            commands, _ = self._run_main("cursor-worker", agent="cursor")

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--model") + 1], "env-model")

    def test_cursor_model_config_overrides_fallback(self):
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "cursor", "cursor_model": "config-model"}), encoding="utf-8")
        with patch.object(worker_launch, "get_cursor_auth_token", return_value="token"), \
             patch.dict(os.environ, {"MECHA_WORKER_RUNTIME_CONFIG": str(config)}, clear=False):
            commands, _ = self._run_main("cursor-worker", agent="cursor")

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--model") + 1], "config-model")

    def test_cursor_model_falls_back_to_glm_5_2(self):
        with patch.object(worker_launch, "get_cursor_auth_token", return_value="token"):
            commands, output = self._run_main("cursor-worker", agent="cursor")

        command = shlex.split(commands[0])
        self.assertTrue(any(part.endswith("/cursor-agent") for part in command), command)
        self.assertEqual(command[command.index("--model") + 1], "glm-5.2")
        self.assertNotIn("--notify-on-settled", command)
        self.assertIn("Worker (cursor) launched", output)

    def test_blank_cursor_model_flag_errors_before_launch(self):
        with patch.object(worker_launch, "get_cursor_auth_token", return_value="token"), \
             self.assertRaises(SystemExit) as raised:
            self._run_main("cursor-worker", "--cursor-model", " ", agent="cursor")

        self.assertEqual(raised.exception.code, 1)

    def test_blank_cursor_model_environment_errors_before_launch(self):
        with patch.object(worker_launch, "get_cursor_auth_token", return_value="token"), \
             patch.dict(os.environ, {worker_launch.CURSOR_MODEL_ENV: " "}, clear=False), \
             self.assertRaises(SystemExit) as raised:
            self._run_main("cursor-worker", agent="cursor")

        self.assertEqual(raised.exception.code, 1)

    def test_blank_cursor_model_config_errors_before_launch(self):
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "cursor", "cursor_model": " "}), encoding="utf-8")
        with patch.object(worker_launch, "get_cursor_auth_token", return_value="token"), \
             patch.dict(os.environ, {"MECHA_WORKER_RUNTIME_CONFIG": str(config)}, clear=False), \
             self.assertRaises(SystemExit) as raised:
            self._run_main("cursor-worker", agent="cursor")

        self.assertEqual(raised.exception.code, 1)

    def test_non_string_cursor_model_config_errors_before_launch(self):
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "cursor", "cursor_model": 42}), encoding="utf-8")
        with patch.object(worker_launch, "get_cursor_auth_token", return_value="token"), \
             patch.dict(os.environ, {"MECHA_WORKER_RUNTIME_CONFIG": str(config)}, clear=False), \
             self.assertRaises(SystemExit) as raised:
            self._run_main("cursor-worker", agent="cursor")

        self.assertEqual(raised.exception.code, 1)

    def test_cursor_config_is_ignored_for_non_cursor_runtimes(self):
        config = self.tmp_path / "worker-runtime.json"
        config.write_text(json.dumps({"default_agent": "claude", "cursor_model": 42}), encoding="utf-8")
        with patch.dict(os.environ, {
            "MECHA_WORKER_RUNTIME_CONFIG": str(config),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("claude-worker", agent=None)

        self.assertIn("claude_sidecar.mjs", commands[0])

    def test_cursor_model_override_does_not_widen_pi_model_flag(self):
        argv = [
            "worker_launch.py", "--window", "cursor-worker", "--dir", self.tmpdir.name,
            "--agent", "cursor", "--model", "gpt-5.6",
        ]
        with patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists") as window_exists, \
             contextlib.redirect_stderr(stderr := io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn("--model is only supported for Pi", stderr.getvalue())
        window_exists.assert_not_called()

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

    def test_missing_canonical_pi_extension_fails_before_tmux_side_effects(self):
        argv = ["worker_launch.py", "--window", "pi-worker", "--dir", self.tmpdir.name, "--agent", "pi"]
        missing = str(self.tmp_path / "missing-mecha-messaging.ts")
        with patch.dict(os.environ, {worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765"}, clear=True), \
             patch.object(worker_launch, "PI_MESSAGING_EXTENSION_PATH", missing), \
             patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists") as window_exists, \
             contextlib.redirect_stderr(stderr := io.StringIO()), \
             self.assertRaises(SystemExit) as raised:
            worker_launch.main()

        self.assertEqual(raised.exception.code, 1)
        self.assertIn(missing, stderr.getvalue())
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

    def test_managed_pi_command_explicitly_loads_canonical_worker_extensions(self):
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", agent="pi")

        command = shlex.split(commands[0])
        self.assertIn("--no-extensions", command)
        self.assertEqual(self._pi_extensions(command), [
            worker_launch.PI_SUBAGENTS_EXTENSION_PATH,
            worker_launch.PI_MESSAGING_EXTENSION_PATH,
        ])

    def test_main_maps_resume_to_pi_session_with_explicit_worker_extensions(self):
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", "--resume", "session-123", agent="pi")

        command = shlex.split(commands[0])
        self.assertEqual(command[command.index("--session") + 1], "session-123")
        self.assertNotIn("resume", command)
        self.assertIn("--no-extensions", command)
        self.assertEqual(self._pi_extensions(command), [
            worker_launch.PI_SUBAGENTS_EXTENSION_PATH,
            worker_launch.PI_MESSAGING_EXTENSION_PATH,
        ])

    def test_pi_launch_exports_router_configuration_but_not_identity_or_settled_recipient_by_default(self):
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", agent="pi")

        self.assertIn("export MECHA_ROUTER_URL=http://127.0.0.1:8765", commands[0])
        self.assertIn(f"export {worker_launch.PI_MODEL_KEY_ENV}=openai-codex/gpt-5.6-sol", commands[0])
        self.assertNotIn(worker_launch.PI_NOTIFY_ON_SETTLED_ENV, commands[0])
        self.assertNotIn("MECHA_AGENT_WINDOW", commands[0])
        self.assertNotIn("MECHA_WORKER_WINDOW", commands[0])

    def test_pi_launch_exports_validated_settled_recipient(self):
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main(
                "pi-worker", "--notify-on-settled", "supervisor", agent="pi"
            )

        self.assertIn("export MECHA_ROUTER_URL=http://127.0.0.1:8765", commands[0])
        self.assertIn("export MECHA_PI_NOTIFY_ON_SETTLED=supervisor", commands[0])

    def test_pi_launch_exports_validated_compaction_policy(self):
        policy = '{"postCompactionNotification":"resume after compact","levels":[40,50,90],"models":{"openai-codex/custom-model":{"levels":[20,30,{"percent":45,"mandatory":true}]}}}'
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main(
                "pi-worker", "--compaction-policy", policy, agent="pi"
            )

        self.assertIn("export MECHA_ROUTER_URL=http://127.0.0.1:8765", commands[0])
        self.assertIn(f"export {worker_launch.PI_COMPACTION_POLICY_ENV}=", commands[0])
        self.assertIn(shlex.quote(policy), commands[0])

    def test_compaction_policy_rejects_invalid_values_before_launch(self):
        argv = [
            "worker_launch.py", "--window", "pi-worker", "--dir", self.tmpdir.name,
            "--agent", "pi", "--compaction-policy", '{"tiers":[{"maxContextTokens":1000000,"levels":[40,100]}]}',
        ]
        with patch.dict(os.environ, {
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
        for recipient in ("", "bad recipient", "bad@recipient"):
            argv = [
                "worker_launch.py", "--window", "pi-worker", "--dir", self.tmpdir.name,
                "--agent", "pi", "--notify-on-settled", recipient,
            ]
            with self.subTest(recipient=recipient), patch.dict(os.environ, {
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

        The subprocess inherits the real HOME, so it patches _claude_pretrust_workspace:
        without that it would record a temporary directory in the developer's own
        ~/.claude.json, and back that file up, every time the suite ran.
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
                 patch.object(worker_launch, "_claude_pretrust_workspace"), \\
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

    def _run_main(self, window, *extra_args, agent="claude", home=None):
        """Run main() without letting it write anything into the developer's home.

        Two independent guards, because either alone is a denylist. HOME points at a
        temporary directory, so anything main() writes under ~ lands there whether or not
        this method knows about it. On top of that the three pretrust helpers are patched
        by name: the Codex one creates ~/.codex/ and writes its config with no exists guard,
        and their mocks
        land on self.pretrust so the wiring tests can assert which runtime called which.

        self.calls records the order of the calls that have to happen in order.
        """
        argv = ["worker_launch.py", "--window", window, "--dir", self.tmpdir.name]
        if agent is not None:
            argv.extend(["--agent", agent])
        argv.extend(extra_args)
        commands = []
        output = io.StringIO()
        home = home or (self.tmp_path / f"home-{window}")
        home.mkdir(parents=True, exist_ok=True)
        order = Mock()
        with patch.dict(os.environ, {"HOME": str(home)}, clear=False), \
             patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists", return_value=False), \
             patch.object(worker_launch, "ensure_session", return_value=False), \
             patch.object(worker_launch, "new_window"), \
             patch.object(worker_launch, "send_text", side_effect=lambda _window, command: commands.append(command)) as send_text, \
             patch.object(worker_launch, "send_enter"), \
             patch.object(worker_launch.subprocess, "run"), \
             patch.object(worker_launch.subprocess, "Popen"), \
             patch.object(worker_launch.time, "sleep"), \
             patch.object(worker_launch, "_claude_pretrust_workspace") as claude_pretrust, \
             patch.object(worker_launch, "_cursor_pretrust_workspace") as cursor_pretrust, \
             patch.object(worker_launch, "_codex_pretrust_workspace") as codex_pretrust, \
             contextlib.redirect_stdout(output), \
             contextlib.redirect_stderr(io.StringIO()):
            order.attach_mock(claude_pretrust, "pretrust")
            order.attach_mock(send_text, "send_text")
            self.pretrust = {
                "claude": claude_pretrust,
                "cursor": cursor_pretrust,
                "codex": codex_pretrust,
            }
            self.home = home
            self.calls = order
            worker_launch.main()
        return commands, output.getvalue()

    def test_a_claude_launch_marks_its_own_work_directory_trusted_before_it_starts(self):
        # Ordering is the whole point. Recording trust after the agent has been told to
        # run puts back the race this exists to close, and assert_called_once_with alone
        # says nothing about when.
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            self._run_main("claude-worker", agent="claude")

        self.pretrust["claude"].assert_called_once_with(self.tmpdir.name)
        self.pretrust["cursor"].assert_not_called()
        self.pretrust["codex"].assert_not_called()
        self.assertEqual([name for name, _, _ in self.calls.mock_calls],
                         ["pretrust", "send_text"])

    def test_a_real_launch_leaves_its_work_directory_trusted_on_disk(self):
        """The claim end to end, with the real helper, observed in the file.

        Everything else either drives the helper directly or asserts a mock was called.
        This is the only place main()'s unresolved abspath and the helper's resolved key
        meet, and it is what proves a launch actually records trust.
        """
        home = self.tmp_path / "real-launch-home"
        home.mkdir()
        argv = [
            "worker_launch.py", "--window", "claude-worker",
            "--dir", self.tmpdir.name, "--agent", "claude",
        ]
        commands = []
        with patch.dict(os.environ, {
            "HOME": str(home),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False), \
             patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists", return_value=False), \
             patch.object(worker_launch, "ensure_session", return_value=False), \
             patch.object(worker_launch, "new_window"), \
             patch.object(worker_launch, "send_text",
                          side_effect=lambda _window, command: commands.append(command)), \
             patch.object(worker_launch, "send_enter"), \
             patch.object(worker_launch.time, "sleep"), \
             contextlib.redirect_stdout(io.StringIO()), \
             contextlib.redirect_stderr(stderr := io.StringIO()):
            worker_launch.main()

        written = json.loads((home / ".claude.json").read_text(encoding="utf-8"))
        entry = written["projects"][os.path.realpath(self.tmpdir.name)]
        self.assertIs(entry["hasTrustDialogAccepted"], True)
        self.assertEqual(len(commands), 1)
        # This home records no answer to either dialog and the launch is still silent.
        # Both are answered without asking anyone: trust in the config file above, and the
        # bypass permissions warning on the command line.
        self.assertEqual(stderr.getvalue(), "")

    def _claude_argv(self, command):
        """The claude invocation inside a launch command, after quote removal and splitting.

        shlex.split and not a substring check, because the settings payload is JSON with
        braces, quotes and a colon in it. Unquoted, that payload keeps the same characters
        and loses its double quotes, so a substring check on the JSON passes on a command
        that hands Claude Code something it cannot parse.

        This models quote removal and word splitting only. A shell also expands braces, and
        shlex does not, so this cannot see the case where a second setting makes the payload
        split for real. test_the_bypass_answer_reaches_claude_code_as_one_argument covers
        that separately by pinning the quoted form itself.

        The last `claude` token, because the sidecar arguments come first and carry the
        window name twice. A worker window named `claude` would otherwise slice from there.
        """
        tokens = shlex.split(command)
        return tokens[len(tokens) - 1 - tokens[::-1].index("claude"):]

    def test_a_claude_worker_answers_the_bypass_permissions_dialog_on_its_command_line(self):
        """The dialog that kills a worker, answered before it can appear.

        Its highlighted default is "No, exit", so the injector's blind Enter exits the
        session and the first message goes with it. Measured against Claude Code 2.1.226:
        with this flag the session starts in bypass permissions mode and shows no dialog.
        """
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("claude-worker", agent="claude")

        claude_argv = self._claude_argv(commands[0])
        self.assertIn("--dangerously-skip-permissions", claude_argv)
        # The parsed value, not the string, so reformatting the payload cannot break this
        # and a flag carrying the wrong key cannot pass it.
        settings = json.loads(claude_argv[claude_argv.index("--settings") + 1])
        self.assertIs(settings.get("skipDangerousModePermissionPrompt"), True)

    def test_the_bypass_answer_reaches_claude_code_as_one_argument(self):
        """Unquoted, the payload reaches Claude Code mangled and the dialog comes back.

        Today it has no spaces, so word splitting leaves it whole and bash strips its double
        quotes, which is enough to make the JSON unparseable. Add a second setting to the
        constant and brace expansion splits it into two words as well.

        Two assertions, because they fail on different mutations. The parsed one catches a
        payload the shell mangled. The raw one pins the quoted bytes the shell is handed, so
        it holds whatever the payload becomes, including a form shlex.split cannot model.
        """
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("claude-worker", agent="claude")

        claude_argv = self._claude_argv(commands[0])
        self.assertEqual(claude_argv.count("--settings"), 1)
        self.assertEqual(claude_argv[claude_argv.index("--settings") + 1], worker_launch.CLAUDE_BYPASS_SETTINGS_JSON)
        self.assertIn(shlex.quote(worker_launch.CLAUDE_BYPASS_SETTINGS_JSON), commands[0])

    def test_a_claude_worker_without_skip_permissions_gets_neither_flag(self):
        # The dialog only appears with --dangerously-skip-permissions, so answering it
        # without that flag would turn a setting off for a session that never asked.
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main(
                "claude-worker", "--no-skip-permissions", agent="claude"
            )

        claude_argv = self._claude_argv(commands[0])
        self.assertNotIn("--dangerously-skip-permissions", claude_argv)
        self.assertNotIn("--settings", claude_argv)

    def test_a_resumed_claude_worker_still_answers_the_bypass_dialog(self):
        """A resumed session shows the same dialog, and its id has to survive the shell.

        The id here carries a space and a semicolon on purpose. An ordinary id quotes to
        itself, so it passes under the old unquoted concatenation as well and pins nothing.
        A semicolon is the case that matters: unquoted, the shell reads it as a command
        separator and runs whatever follows.
        """
        resume_id = "9d1f session; touch pwned"
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main(
                "claude-worker", "--resume", resume_id, agent="claude"
            )

        claude_argv = self._claude_argv(commands[0])
        self.assertIn("--settings", claude_argv)
        self.assertEqual(claude_argv[claude_argv.index("--resume") + 1], resume_id)
        self.assertIn(shlex.quote(resume_id), commands[0])

    def test_every_other_runtime_leaves_the_claude_config_and_command_line_alone(self):
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            commands, _ = self._run_main("pi-worker", agent="pi")
        self.pretrust["claude"].assert_not_called()
        # None of these three takes a --settings flag, so carrying one would be an
        # unrecognized argument and the worker would not start at all.
        self.assertNotIn("--settings", commands[0])

        commands, _ = self._run_main("codex-worker", agent="codex")
        self.pretrust["claude"].assert_not_called()
        self.pretrust["codex"].assert_called_once_with(self.tmpdir.name)
        self.assertNotIn("--settings", commands[0])
        self.assertTrue({"--extension", "--no-extensions", "-ne"}.isdisjoint(shlex.split(commands[0])))

        with patch.object(worker_launch, "get_cursor_auth_token", return_value="token"):
            commands, _ = self._run_main("cursor-worker", agent="cursor")
        self.pretrust["claude"].assert_not_called()
        self.pretrust["cursor"].assert_called_once_with(self.tmpdir.name)
        self.assertNotIn("--settings", commands[0])
        self.assertTrue({"--extension", "--no-extensions", "-ne"}.isdisjoint(shlex.split(commands[0])))

    def test_a_launch_writes_no_settings_file_anywhere(self):
        """The launcher answers the bypass dialog without writing a settings file.

        Two places it must not appear. ~/.claude/settings.json is the user's configuration
        for every session on the machine. <work dir>/.claude/settings.local.json is a file
        Claude Code reads the same key from, and it would sit untracked inside whatever
        repository the worker was launched into, where nothing stops a worker committing
        it. ~/.claude/projects holds transcripts and Claude Code reads no settings there.

        HOME points at a temporary tree and the os patches are gone, so anything main()
        writes under a home lands where this can see it.
        """
        home = self.tmp_path / "settings-probe-home"
        home.mkdir()
        with patch.dict(os.environ, {
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False):
            # home= is what main() actually runs against. Patching HOME around this call
            # instead would be overwritten by _run_main's own redirect, and the assertions
            # below would then read a directory nothing ever wrote to.
            self._run_main("claude-worker", agent="claude", home=home)

        self.assertEqual(list(home.rglob("settings*.json")), [])
        self.assertFalse((home / ".claude" / "projects").exists())
        self.assertEqual(list(pathlib.Path(self.tmpdir.name).rglob("settings*.json")), [])
        self.assertFalse((pathlib.Path(self.tmpdir.name) / ".claude").exists())

    def test_a_config_the_helper_cannot_use_still_launches_the_worker(self):
        """The helper never blocks the launch.

        Runs the real helper, not a mock, because the promise being tested is that nothing
        it does can stop main() from reaching send_text.
        """
        home = self.tmp_path / "unusable-config-home"
        home.mkdir()
        config = home / ".claude.json"
        config.write_text("{not json", encoding="utf-8")

        argv = [
            "worker_launch.py", "--window", "claude-worker",
            "--dir", self.tmpdir.name, "--agent", "claude",
        ]
        commands = []
        stderr = io.StringIO()
        with patch.dict(os.environ, {
            "HOME": str(home),
            worker_launch.PI_ROUTER_URL_ENV: "http://127.0.0.1:8765",
        }, clear=False), \
             patch.object(worker_launch.sys, "argv", argv), \
             patch.object(worker_launch, "window_exists", return_value=False), \
             patch.object(worker_launch, "ensure_session", return_value=False), \
             patch.object(worker_launch, "new_window"), \
             patch.object(worker_launch, "send_text",
                          side_effect=lambda _window, command: commands.append(command)), \
             patch.object(worker_launch, "send_enter"), \
             patch.object(worker_launch.time, "sleep"), \
             contextlib.redirect_stdout(io.StringIO()), \
             contextlib.redirect_stderr(stderr):
            worker_launch.main()

        self.assertEqual(len(commands), 1)
        self.assertEqual(
            self._claude_argv(commands[0])[:2],
            ["claude", "--dangerously-skip-permissions"],
        )
        self.assertIn("workspace trust", stderr.getvalue())
        self.assertEqual(config.read_text(encoding="utf-8"), "{not json")


class ClaudeWorkspaceTrustTest(unittest.TestCase):
    """The launcher records workspace trust so Claude Code never opens its trust dialog.

    Measured against Claude Code 2.1.226: the dialog is controlled by
    projects.<resolved path>.hasTrustDialogAccepted in ~/.claude.json. An entry written
    under an unresolved spelling of a path containing a symlink does not suppress it, and
    the dialog itself prints the resolved path.

    ~/.claude.json is the developer's live configuration, so every test here points the
    helper at a file inside a temporary directory. Nothing in this class may touch the
    real home directory.
    """

    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmpdir.cleanup)
        self.tmp_path = pathlib.Path(self.tmpdir.name)
        self.config = self.tmp_path / "claude.json"
        self.backup = self.tmp_path / (
            "claude.json" + worker_launch.CLAUDE_CONFIG_BACKUP_SUFFIX
        )
        self.work_dir = self.tmp_path / "worktree"
        self.work_dir.mkdir()

    def _trust(self, work_dir=None, config_path=None):
        """Run the helper and return whatever it wrote to stderr."""
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            worker_launch._claude_pretrust_workspace(
                str(work_dir if work_dir is not None else self.work_dir),
                str(config_path if config_path is not None else self.config),
            )
        return stderr.getvalue()

    def _key(self):
        return os.path.realpath(self.work_dir)

    def _written(self):
        return json.loads(self.config.read_text(encoding="utf-8"))

    def _write_config(self, config):
        self.config.write_text(json.dumps(config, indent=2), encoding="utf-8")

    def test_an_absent_config_is_created_private_with_a_full_new_project_entry(self):
        self.assertEqual(self._trust(), "")

        written = self._written()
        self.assertEqual(list(written), ["projects"])
        # All nine fields Claude Code 2.1.226 puts on a fresh project. It backfills these
        # only when the whole entry is missing, so a short entry written here stays short:
        # an eight field entry still had no mcpContextUris after a full session ran there.
        self.assertEqual(written["projects"], {self._key(): {
            "allowedTools": [],
            "disabledMcpjsonServers": [],
            "enabledMcpjsonServers": [],
            "hasClaudeMdExternalIncludesApproved": False,
            "hasClaudeMdExternalIncludesWarningShown": False,
            "hasTrustDialogAccepted": True,
            "mcpContextUris": [],
            "mcpServers": {},
            "projectOnboardingSeenCount": 0,
        }})
        self.assertEqual(stat.S_IMODE(self.config.stat().st_mode), 0o600)

    def test_a_config_with_no_projects_key_gains_one_and_keeps_its_other_keys(self):
        # config.get("projects", {}) would hand back a detached dict here, so the file
        # would be rewritten with no trust entry at all.
        self._write_config({"numStartups": 42})

        self._trust()

        written = self._written()
        self.assertEqual(written["numStartups"], 42)
        self.assertIs(written["projects"][self._key()]["hasTrustDialogAccepted"], True)

    def test_every_other_top_level_key_and_project_survives_unchanged(self):
        original = {
            "numStartups": 42,
            "oauthAccount": {"emailAddress": "someone@example.com"},
            "projects": {
                "/somewhere/else": {"hasTrustDialogAccepted": True, "lastCost": 1.5},
            },
        }
        self._write_config(original)

        self._trust()

        written = self._written()
        written["projects"].pop(self._key())
        self.assertEqual(written, original)

    def test_an_existing_entry_keeps_its_other_fields_and_only_trust_flips(self):
        entry = {
            "hasTrustDialogAccepted": False,
            "lastCost": 2.5,
            "allowedTools": ["Bash"],
            "aFieldAFutureVersionAdded": {"kept": True},
        }
        self._write_config({"projects": {self._key(): entry}})

        self._trust()

        self.assertEqual(
            self._written()["projects"][self._key()],
            {**entry, "hasTrustDialogAccepted": True},
        )

    def test_an_already_trusted_directory_is_not_rewritten_and_leaves_no_backup(self):
        self._write_config({"projects": {self._key(): {"hasTrustDialogAccepted": True}}})
        before = self.config.read_bytes()

        # Comparing bytes alone is weak here: the helper emits the same formatting
        # _write_config does, so a rewrite would be byte-identical. Watching os.replace is
        # what says no write happened at all.
        with patch.object(worker_launch.os, "replace") as replace:
            self._trust()

        replace.assert_not_called()
        self.assertEqual(self.config.read_bytes(), before)
        self.assertFalse(self.backup.exists())

    def test_the_entry_is_keyed_by_the_resolved_path_not_the_symlink_spelling(self):
        # The fact the whole fix turns on: a trust entry under the symlink spelling does
        # not suppress the dialog, and the dialog prints the resolved path.
        link = self.tmp_path / "worktree-link"
        link.symlink_to(self.work_dir)

        self._trust(work_dir=link)

        self.assertIn(self._key(), self._written()["projects"])
        self.assertNotIn(str(link), self._written()["projects"])

    def test_an_unparseable_config_is_left_alone_and_the_warning_says_what_to_do(self):
        self.config.write_text("{not json", encoding="utf-8")
        before = self.config.read_bytes()

        warning = self._trust()

        self.assertEqual(self.config.read_bytes(), before)
        self.assertFalse(self.backup.exists())
        self.assertIn(str(self.work_dir), warning)
        self.assertIn(str(self.config), warning)
        self.assertIn("Expecting property name", warning)
        self.assertIn("before sending the worker its first message", warning)

    def test_shapes_the_helper_does_not_understand_are_left_alone(self):
        """A file can parse as JSON and still have a shape the helper cannot use.

        The warning has to name which shape was wrong. Every one of these ends in an
        exception either way, so asserting only that the file survived would pass even if
        the checks were deleted and an incidental AttributeError did the work; the
        operator would get a message about an AttributeError and no reason.
        """
        for name, content, reason in (
            ("the file is empty", "", "Expecting value"),
            ("the file is whitespace only", "   \n\t ", "Expecting value"),
            (
                # json.loads accepts a bare Infinity and json.dumps would write one
                # straight back out. Claude Code reads this file with JSON.parse, which
                # rejects it, so the config would look fine here and be unreadable there.
                "a number is not finite",
                '{"projects": {}, "weird": Infinity}',
                "Out of range float",
            ),
            ("top level is an array", "[]", "its top level is not an object"),
            (
                "projects is a string",
                json.dumps({"projects": "nope"}),
                "its projects value is not an object",
            ),
            (
                "the entry is a string",
                json.dumps({"projects": {self._key(): "nope"}}),
                f"its entry for {self._key()} is not an object",
            ),
            (
                "the entry is null",
                json.dumps({"projects": {self._key(): None}}),
                f"its entry for {self._key()} is not an object",
            ),
        ):
            with self.subTest(name):
                self.config.write_text(content, encoding="utf-8")
                before = self.config.read_bytes()

                warning = self._trust()

                self.assertEqual(self.config.read_bytes(), before)
                self.assertIn(str(self.config), warning)
                self.assertIn(reason, warning)

    def test_the_config_and_its_backup_are_private_at_every_moment(self):
        """A 0600 config stays 0600, and neither file is readable in between.

        The mode is captured at os.replace time, not just at the end. shutil.copy2 writes
        the backup at 0644 and narrows it afterwards, so a final-mode assertion alone
        cannot tell the two apart.
        """
        self._write_config({"projects": {}})
        os.chmod(self.config, 0o600)
        previous_umask = os.umask(0o000)
        self.addCleanup(os.umask, previous_umask)

        modes_at_replace = {}
        real_replace = os.replace

        def recording_replace(source, destination):
            modes_at_replace[os.path.basename(destination)] = stat.S_IMODE(
                os.stat(source).st_mode
            )
            return real_replace(source, destination)

        with patch.object(worker_launch.os, "replace", side_effect=recording_replace):
            self._trust()

        self.assertEqual(stat.S_IMODE(self.config.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(self.backup.stat().st_mode), 0o600)
        self.assertEqual(modes_at_replace, {self.config.name: 0o600, self.backup.name: 0o600})

    def test_the_config_keeps_its_mode_while_the_backup_stays_private(self):
        # Asserting 0600 alone would pass with no chmod at all, since mkstemp creates the
        # temporary file 0600 whatever the umask is. A config that is not 0600 is what
        # shows the mode is really carried across.
        #
        # The backup does not follow it. The user chose the config's mode; the backup is a
        # file they never created, holding the same account credentials, that nothing
        # reads back. A config restored by rsync or Time Machine at 0644 must not mint a
        # second group readable copy in the home directory.
        self._write_config({"projects": {}})
        os.chmod(self.config, 0o644)

        self._trust()

        self.assertEqual(stat.S_IMODE(self.config.stat().st_mode), 0o644)
        self.assertEqual(stat.S_IMODE(self.backup.stat().st_mode), 0o600)

    def test_the_backup_holds_the_bytes_that_were_on_disk_before_the_write(self):
        self._write_config({"projects": {}})
        original = self.config.read_bytes()

        self._trust()

        self.assertEqual(self.backup.read_bytes(), original)

    def test_a_second_write_reuses_the_one_backup_file(self):
        self._write_config({"projects": {}})
        self._trust()
        second_work_dir = self.tmp_path / "second-worktree"
        second_work_dir.mkdir()
        before_second_write = self.config.read_bytes()

        self._trust(work_dir=second_work_dir)

        backups = sorted(
            entry.name for entry in self.tmp_path.iterdir()
            if worker_launch.CLAUDE_CONFIG_BACKUP_SUFFIX in entry.name
        )
        self.assertEqual(backups, [self.backup.name])
        self.assertEqual(self.backup.read_bytes(), before_second_write)

    def test_the_rewrite_matches_how_claude_code_formats_the_file(self):
        self._write_config({"note": "café", "projects": {}})

        self._trust()

        written = self.config.read_text(encoding="utf-8")
        self.assertIn('\n  "projects": {', written)
        self.assertIn("café", written)
        self.assertNotIn("caf\\u00e9", written)

    def test_a_failed_backup_rename_leaves_the_config_and_the_directory_as_they_were(self):
        self._write_config({"projects": {}})
        before = self.config.read_bytes()
        listing = sorted(entry.name for entry in self.tmp_path.iterdir())

        with patch.object(worker_launch.os, "replace",
                          side_effect=OSError("no space left on device")):
            warning = self._trust()

        self.assertEqual(self.config.read_bytes(), before)
        self.assertEqual(sorted(entry.name for entry in self.tmp_path.iterdir()), listing)
        self.assertIn("no space left on device", warning)

    def test_a_failed_config_rename_leaves_the_backup_holding_the_original(self):
        """The backup is written first, so this is the state it exists for.

        Failing every os.replace only ever exercises the backup's rename, since that one
        comes first. This lets the backup through and fails the config's, which is the
        case where a copy of the original has to be on disk.
        """
        self._write_config({"projects": {}})
        before = self.config.read_bytes()
        real_replace = os.replace

        def fail_only_the_config(source, destination):
            if os.path.basename(destination) == self.config.name:
                raise OSError("no space left on device")
            return real_replace(source, destination)

        with patch.object(worker_launch.os, "replace", side_effect=fail_only_the_config):
            warning = self._trust()

        self.assertEqual(self.config.read_bytes(), before)
        self.assertEqual(self.backup.read_bytes(), before)
        self.assertEqual(
            [entry.name for entry in self.tmp_path.iterdir() if ".tmp" in entry.name], []
        )
        self.assertIn("no space left on device", warning)

    def test_the_backup_is_written_before_anything_replaces_the_config(self):
        # If the config were replaced first and the backup write then failed, there would
        # be a changed config and no copy of what it held.
        self._write_config({"projects": {}})
        destinations = []
        real_replace = os.replace

        def recording_replace(source, destination):
            destinations.append(os.path.basename(destination))
            return real_replace(source, destination)

        with patch.object(worker_launch.os, "replace", side_effect=recording_replace):
            self._trust()

        self.assertEqual(destinations, [self.backup.name, self.config.name])

    def test_the_temporary_file_is_created_in_the_destination_directory(self):
        """os.replace is only atomic within one filesystem.

        Letting mkstemp choose would put the temporary file under TMPDIR. That is the same
        volume here, so the suite would stay green while every launch on a machine with a
        separate TMPDIR failed with EXDEV and silently recorded no trust.
        """
        self._write_config({"projects": {}})
        directories = []
        real_mkstemp = tempfile.mkstemp

        def recording_mkstemp(*args, **kwargs):
            directories.append(kwargs.get("dir"))
            return real_mkstemp(*args, **kwargs)

        with patch.object(worker_launch.tempfile, "mkstemp", side_effect=recording_mkstemp):
            self._trust()

        # The backup keeps the unresolved directory and the config gets the resolved one.
        # Here they are the same place; the symlink test is what separates them.
        resolved = os.path.dirname(os.path.realpath(self.config))
        self.assertEqual(directories, [str(self.tmp_path), resolved])

    def test_the_bytes_are_flushed_to_disk_before_the_rename(self):
        self._write_config({"projects": {}})
        order = Mock()

        with patch.object(worker_launch.os, "fsync", side_effect=os.fsync) as fsync, \
             patch.object(worker_launch.os, "replace", side_effect=os.replace) as replace:
            order.attach_mock(fsync, "fsync")
            order.attach_mock(replace, "replace")
            self._trust()

        names = [name for name, _, _ in order.mock_calls]
        self.assertEqual(names, ["fsync", "replace", "fsync", "replace"])

    def test_a_rewrite_that_would_drop_something_is_refused(self):
        """Correct code never trips this, so the test calls it directly.

        The atomic rename makes a well formed but wrong write permanent, and the backup
        only holds the generation before this one, so the bytes are checked before they go
        down.
        """
        trusted = {"hasTrustDialogAccepted": True}
        original = json.dumps(
            {"numStartups": 1, "projects": {"/a": trusted, "/b": {}}}
        ).encode()

        with self.assertRaises(ValueError) as lost_key:
            worker_launch._verify_the_rewrite_is_sound(
                original, json.dumps({"projects": {"/a": trusted, "/b": {}}}), "/a"
            )
        self.assertIn("numStartups", str(lost_key.exception))

        with self.assertRaises(ValueError) as lost_project:
            worker_launch._verify_the_rewrite_is_sound(
                original, json.dumps({"numStartups": 1, "projects": {"/a": trusted}}), "/a"
            )
        self.assertIn("/b", str(lost_project.exception))

        # A rewrite that keeps everything and records no trust is the failure the deleted
        # settings.json had: written successfully, read by nothing.
        with self.assertRaises(ValueError) as no_trust:
            worker_launch._verify_the_rewrite_is_sound(
                original,
                json.dumps({"numStartups": 1, "projects": {"/a": {}, "/b": {}}}),
                "/a",
            )
        self.assertIn("would not mark /a trusted", str(no_trust.exception))

        # Adding is fine; that is the whole point of the write.
        worker_launch._verify_the_rewrite_is_sound(
            original,
            json.dumps({"numStartups": 1, "projects": {"/a": trusted, "/b": {}, "/c": {}}}),
            "/a",
        )

    def test_the_drop_check_stands_between_the_config_and_the_rename(self):
        # Correct code never drops anything, so nothing else notices if the call is
        # deleted. This makes the check refuse and asserts the write did not happen.
        self._write_config({"projects": {}})
        before = self.config.read_bytes()

        with patch.object(worker_launch, "_verify_the_rewrite_is_sound",
                          side_effect=ValueError("the rewrite would drop projects: ['/a']")):
            warning = self._trust()

        self.assertEqual(self.config.read_bytes(), before)
        self.assertFalse(self.backup.exists())
        self.assertIn("would drop projects", warning)

    def test_a_failure_creating_the_temporary_file_warns_instead_of_crashing(self):
        # This is the full-disk case. Recovering by unlinking a name mkstemp never bound
        # would raise UnboundLocalError and abort the launch before the worker started.
        self._write_config({"projects": {}})
        before = self.config.read_bytes()
        listing = sorted(entry.name for entry in self.tmp_path.iterdir())

        with patch.object(worker_launch.tempfile, "mkstemp",
                          side_effect=OSError("no space left on device")):
            warning = self._trust()

        self.assertEqual(self.config.read_bytes(), before)
        self.assertEqual(sorted(entry.name for entry in self.tmp_path.iterdir()), listing)
        self.assertIn("no space left on device", warning)

    def test_a_cleanup_that_itself_fails_does_not_hide_the_real_error(self):
        # Best-effort cleanup. If removing the stray temporary file raised, that error
        # would replace the one the operator actually needs to see.
        self._write_config({"projects": {}})
        before = self.config.read_bytes()

        with patch.object(worker_launch.os, "replace",
                          side_effect=OSError("no space left on device")), \
             patch.object(worker_launch.os, "remove",
                          side_effect=OSError("cleanup also failed")):
            warning = self._trust()

        self.assertEqual(self.config.read_bytes(), before)
        self.assertIn("no space left on device", warning)
        self.assertNotIn("cleanup also failed", warning)

    def test_a_symlinked_config_path_is_written_through_to_its_target(self):
        # Dotfile managers symlink ~/.claude.json. Without resolving it first, os.replace
        # would drop a regular file over the link and orphan the target.
        target = self.tmp_path / "real-claude.json"
        target.write_text(json.dumps({"projects": {}}, indent=2), encoding="utf-8")
        link = self.tmp_path / "linked-claude.json"
        link.symlink_to(target)

        self._trust(config_path=link)

        self.assertTrue(link.is_symlink())
        self.assertIn(self._key(), json.loads(target.read_text(encoding="utf-8"))["projects"])

    def test_a_symlinked_config_keeps_its_backup_out_of_the_target_directory(self):
        """The config follows the link. The backup does not.

        People symlink ~/.claude.json into a dotfiles repo. A backup written beside the
        resolved target would drop a full copy of a file holding account credentials
        inside that repo, where the next `git add -A` would commit it.
        """
        dotfiles = self.tmp_path / "dotfiles"
        dotfiles.mkdir()
        target = dotfiles / "claude.json"
        target.write_text(json.dumps({"projects": {}}, indent=2), encoding="utf-8")
        link = self.tmp_path / "linked-claude.json"
        link.symlink_to(target)

        self._trust(config_path=link)

        self.assertEqual(sorted(entry.name for entry in dotfiles.iterdir()), ["claude.json"])
        beside_the_link = self.tmp_path / (
            "linked-claude.json" + worker_launch.CLAUDE_CONFIG_BACKUP_SUFFIX
        )
        self.assertTrue(beside_the_link.exists())
        self.assertEqual(stat.S_IMODE(beside_the_link.stat().st_mode), 0o600)

    def test_an_unreadable_config_warns_and_is_left_as_it_was(self):
        self._write_config({"projects": {}})
        before = self.config.read_bytes()
        os.chmod(self.config, 0o000)
        self.addCleanup(os.chmod, self.config, 0o600)

        warning = self._trust()

        os.chmod(self.config, 0o600)
        self.assertEqual(self.config.read_bytes(), before)
        self.assertFalse(self.backup.exists())
        self.assertIn("Permission denied", warning)


class CursorAndCodexWorkspaceTrustTest(unittest.TestCase):
    """Direct cover for the other two pretrust helpers, against a redirected HOME.

    Neither had an assertion before. The old launch tests reached the Cursor helper with
    os.path.exists patched to True, so it took its early return and wrote nothing, and no
    test ever ran the Codex helper at all, because no test launched with agent="codex".
    _run_main now mocks both, so these are the only tests that run either for real.

    The Codex helper is worth covering directly for a second reason: it writes
    ~/.codex/config.toml with no exists guard, so any future test that launches a Codex
    worker through _run_main without that mock would edit the developer's own config.
    """

    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmpdir.cleanup)
        self.home = pathlib.Path(self.tmpdir.name) / "home"
        self.home.mkdir()
        self.work_dir = "/work/tree"
        env_patch = patch.dict(os.environ, {"HOME": str(self.home)}, clear=False)
        env_patch.start()
        self.addCleanup(env_patch.stop)

    def test_cursor_trust_marker_names_the_workspace_it_trusts(self):
        worker_launch._cursor_pretrust_workspace(self.work_dir)

        marker = self.home / ".cursor" / "projects" / "work-tree" / ".workspace-trusted"
        self.assertEqual(
            json.loads(marker.read_text(encoding="utf-8"))["workspacePath"], self.work_dir
        )

    def test_cursor_trust_marker_is_not_rewritten_once_it_exists(self):
        worker_launch._cursor_pretrust_workspace(self.work_dir)
        marker = self.home / ".cursor" / "projects" / "work-tree" / ".workspace-trusted"
        marker.write_text("kept by hand", encoding="utf-8")

        worker_launch._cursor_pretrust_workspace(self.work_dir)

        self.assertEqual(marker.read_text(encoding="utf-8"), "kept by hand")

    def test_codex_trust_is_added_without_disturbing_the_rest_of_the_config(self):
        config = self.home / ".codex" / "config.toml"
        config.parent.mkdir()
        config.write_text('model = "gpt-5.5"\n', encoding="utf-8")

        worker_launch._codex_pretrust_workspace(self.work_dir)

        written = config.read_text(encoding="utf-8")
        self.assertIn('model = "gpt-5.5"', written)
        self.assertIn(f'[projects."{self.work_dir}"]', written)
        self.assertIn('trust_level = "trusted"', written)

    def test_codex_trust_creates_the_config_when_there_is_none(self):
        worker_launch._codex_pretrust_workspace(self.work_dir)

        written = (self.home / ".codex" / "config.toml").read_text(encoding="utf-8")
        self.assertIn(f'[projects."{self.work_dir}"]', written)
        self.assertIn('trust_level = "trusted"', written)

    def test_codex_trust_leaves_an_already_trusted_config_untouched(self):
        config = self.home / ".codex" / "config.toml"
        config.parent.mkdir()
        original = f'[projects."{self.work_dir}"]\ntrust_level = "trusted"\n'
        config.write_text(original, encoding="utf-8")

        worker_launch._codex_pretrust_workspace(self.work_dir)

        self.assertEqual(config.read_text(encoding="utf-8"), original)

    def test_codex_trust_adds_the_key_to_a_table_that_has_other_settings(self):
        config = self.home / ".codex" / "config.toml"
        config.parent.mkdir()
        config.write_text(
            f'[projects."{self.work_dir}"]\nsome_other_key = 1\n', encoding="utf-8"
        )

        worker_launch._codex_pretrust_workspace(self.work_dir)

        written = config.read_text(encoding="utf-8")
        self.assertIn("some_other_key = 1", written)
        self.assertIn('trust_level = "trusted"', written)

    def test_codex_trust_upgrades_an_existing_untrusted_entry_in_place(self):
        config = self.home / ".codex" / "config.toml"
        config.parent.mkdir()
        config.write_text(
            f'[projects."{self.work_dir}"]\ntrust_level = "untrusted"\n', encoding="utf-8"
        )

        worker_launch._codex_pretrust_workspace(self.work_dir)

        written = config.read_text(encoding="utf-8")
        self.assertIn('trust_level = "trusted"', written)
        self.assertNotIn("untrusted", written)


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
