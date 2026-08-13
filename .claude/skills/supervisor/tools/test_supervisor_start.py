#!/usr/bin/env python3
"""Tests for the supervisor start path.

These touch no tmux, no network, no process table, and no path under the real home
directory. Every collaborator is injected.

Two fake-fidelity traps are worth naming, because both have cost real time here:

- `tmux list-panes -t <missing>` exits 1; `tmux display-message -p -t <missing>` exits 0
  and answers about the active pane. `fake_tmux` below returns returncode 1 and does NOT
  raise, because a fake that raises where the real command merely exits non-zero proves
  nothing about the code under test.
- `urlopen` raises `HTTPError` for a non-2xx response; it does not return one with a
  non-200 `.status`. `fake_opener` raises, so the error branches exercise the shape
  production can actually produce.
"""

import contextlib
import io
import json
import os
import pathlib
import shlex
import subprocess
import tempfile
import unittest
from types import SimpleNamespace
from urllib.error import HTTPError
from unittest.mock import patch

import supervisor_start


LIVE_ROUTER_COMMAND = (
    "cd /Users/wolfgang/code/mecha-developer && node tools/run_router.mjs "
    "--database /Users/wolfgang/.mecha-developer/router.sqlite "
    "--log /Users/wolfgang/.mecha-developer/router.jsonl --host 127.0.0.1 --port 8765"
)
LIVE_WEB_UI_COMMAND = (
    "cd /Users/wolfgang/code/mecha-developer && node tools/router_web_ui.mjs --port 8787"
)
# Deliberately not the module's own defaults. A fixture equal to the constant under test
# cannot tell "the value was threaded through" from "the default was used".
PROBE_WINDOW = "supervisor-coldtest"
PROBE_ROUTER = "http://127.0.0.1:8799"


def fake_tmux(command_by_window):
    """A stand-in for `_tmux.run` that behaves like tmux list-panes.

    *command_by_window* maps a window name to a list of per-pane foreground commands.
    Missing windows exit 1 with a message, the way tmux really does.
    """
    def run(args, check=True):
        if args[0] == "list-panes":
            window = args[2].split(":", 1)[1]
            if window not in command_by_window:
                return SimpleNamespace(returncode=1, stdout="can't find window\n")
            panes = command_by_window[window]
            return SimpleNamespace(returncode=0, stdout="".join(f"{p}\n" for p in panes))
        return SimpleNamespace(returncode=0, stdout="")
    return run


def fake_panes(command_by_window):
    """A stand-in for `_tmux.pane_commands`, which is what `agent_is_running` calls."""
    return lambda window: list(command_by_window.get(window, []))


def fake_opener(routes):
    """A stand-in for urlopen. *routes* maps a URL to (status, payload-dict)."""
    class Response:
        def __init__(self, status, payload):
            self.status = status
            self._payload = json.dumps(payload).encode("utf-8")

        def read(self, _limit=None):
            return self._payload

        def __enter__(self):
            return self

        def __exit__(self, *_exception):
            return False

    def opener(url, timeout=None):
        if url not in routes:
            raise OSError("connection refused")
        status, payload = routes[url]
        if not 200 <= status < 300:
            raise HTTPError(url, status, "error", {}, None)
        return Response(status, payload)
    return opener


class FakeClock:
    """A monotonic clock that only advances when the code under test sleeps."""

    def __init__(self):
        self.now = 0.0

    def sleep(self, seconds):
        self.now += seconds

    def __call__(self):
        return self.now


class CommandShapeTest(unittest.TestCase):
    """The generated command lines must match the processes running on this machine."""

    def test_router_command_matches_the_live_pane(self):
        self.assertEqual(
            supervisor_start.router_command(
                "/Users/wolfgang/.mecha-developer/router.sqlite",
                "/Users/wolfgang/.mecha-developer/router.jsonl",
                "127.0.0.1",
                8765,
                repo_dir="/Users/wolfgang/code/mecha-developer",
            ),
            LIVE_ROUTER_COMMAND,
        )

    def test_web_ui_command_on_the_default_router_matches_the_live_pane(self):
        command = supervisor_start.web_ui_command(
            8787, "http://127.0.0.1:8765", repo_dir="/Users/wolfgang/code/mecha-developer"
        )
        self.assertEqual(command, LIVE_WEB_UI_COMMAND)
        self.assertNotIn("--router", command)
        self.assertNotIn("--host", command)

    def test_web_ui_command_points_at_a_non_default_router(self):
        command = supervisor_start.web_ui_command(8800, PROBE_ROUTER)
        self.assertIn(f"--router {PROBE_ROUTER}", command)

    def test_web_ui_command_ignores_a_trailing_slash_on_the_default_router(self):
        self.assertNotIn("--router", supervisor_start.web_ui_command(8787, "http://127.0.0.1:8765/"))

    def test_sidecar_argv_matches_the_live_supervisor_sidecar(self):
        self.assertEqual(
            supervisor_start.sidecar_argv("supervisor", "http://127.0.0.1:8765"),
            [
                "node",
                supervisor_start.SIDECAR_SCRIPT_PATH,
                "--agent-id", "supervisor",
                "--window", "supervisor",
                "--router-url", "http://127.0.0.1:8765",
            ],
        )

    def test_sidecar_agent_id_and_window_are_the_same_value(self):
        argv = supervisor_start.sidecar_argv(PROBE_WINDOW, PROBE_ROUTER)
        self.assertEqual(argv[argv.index("--agent-id") + 1], argv[argv.index("--window") + 1])
        self.assertEqual(argv[argv.index("--window") + 1], PROBE_WINDOW)
        self.assertEqual(argv[argv.index("--router-url") + 1], PROBE_ROUTER)

    def test_the_sibling_scripts_this_module_shells_out_to_actually_exist(self):
        """abspath does not resolve symlinks, and `~/.local/bin/mecha-supervisor` is one.

        Built with abspath, both of these point into ~/.local/bin, where neither file
        exists, and steps 5 and 6 fail on the only path an operator ever runs.
        """
        self.assertTrue(os.path.isfile(supervisor_start.SIDECAR_SCRIPT_PATH),
                        supervisor_start.SIDECAR_SCRIPT_PATH)
        self.assertTrue(os.path.isfile(supervisor_start.MAILBOX_SEND_SCRIPT),
                        supervisor_start.MAILBOX_SEND_SCRIPT)

    def test_claude_command_carries_no_prompt_argument(self):
        # Claude takes its first prompt as a paste, not an argument. This is why the script
        # has to send the bootstrap prompt separately.
        self.assertEqual(supervisor_start.agent_command("claude", "ignored"), "claude --dangerously-skip-permissions")

    def test_pi_uses_only_the_explicit_mailbox_extension(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            extension = pathlib.Path(tmpdir) / "mecha-messaging.js"
            extension.write_text("export default () => {};\n", encoding="utf-8")
            command = shlex.split(supervisor_start.agent_command("pi", "bootstrap", str(extension)))
        self.assertEqual(command[0], "pi")
        self.assertEqual(command.count("--extension"), 1)
        self.assertEqual(command[command.index("--extension") + 1], str(extension))
        self.assertNotIn("--no-extensions", command)
        self.assertEqual(command[command.index("--provider") + 1], "openai-codex")
        self.assertEqual(command[command.index("--model") + 1], "gpt-5.5")

    def test_codex_takes_its_prompt_as_an_argument(self):
        command = shlex.split(supervisor_start.agent_command("codex", "the-prompt"))
        self.assertEqual(command[0], "codex")
        self.assertIn("the-prompt", command)


class EnvironmentOverrideTest(unittest.TestCase):
    def test_router_paths_come_from_the_environment(self):
        database, log = supervisor_start.resolve_router_paths({
            "MECHA_ROUTER_DATABASE": "/tmp/probe.sqlite",
            "MECHA_ROUTER_LOG": "/tmp/probe.jsonl",
        })
        self.assertEqual(database, "/tmp/probe.sqlite")
        self.assertEqual(log, "/tmp/probe.jsonl")

    def test_a_blank_router_path_override_fails_fast(self):
        """Every sibling override in this module fails fast with ConfigurationError. A
        blank database or log path used to pass through silently and fail ~15s later when
        the router process died in its pane with nothing but an empty argument to explain
        why."""
        for env_name in (supervisor_start.ROUTER_DATABASE_ENV, supervisor_start.ROUTER_LOG_ENV):
            with self.subTest(env_name=env_name):
                with self.assertRaises(supervisor_start.ConfigurationError):
                    supervisor_start.resolve_router_paths({env_name: "   "})

    def test_router_path_overrides_reach_the_router_command(self):
        database, log = supervisor_start.resolve_router_paths({
            "MECHA_ROUTER_DATABASE": "/tmp/probe.sqlite",
            "MECHA_ROUTER_LOG": "/tmp/probe.jsonl",
        })
        command = supervisor_start.router_command(database, log, "127.0.0.1", 8799)
        self.assertIn("--database /tmp/probe.sqlite", command)
        self.assertIn("--log /tmp/probe.jsonl", command)

    def test_web_ui_port_comes_from_the_environment(self):
        self.assertEqual(supervisor_start.resolve_web_ui_port({"MECHA_ROUTER_WEB_UI_PORT": "8800"}), 8800)
        self.assertEqual(supervisor_start.resolve_web_ui_port({}), 8787)

    def test_web_ui_port_must_be_a_valid_port(self):
        for value in ("0", "70000", "not-a-port"):
            with self.assertRaises(supervisor_start.ConfigurationError):
                supervisor_start.resolve_web_ui_port({"MECHA_ROUTER_WEB_UI_PORT": value})

    def test_router_url_must_be_loopback_without_a_path(self):
        for value in ("http://192.168.1.5:8765", "http://127.0.0.1:8765/v1", "ftp://127.0.0.1",
                      "http://127.0.0.1:8765?x=1", "http://0.0.0.0:8765"):
            with self.assertRaises(supervisor_start.ConfigurationError):
                supervisor_start.resolve_router_url({"MECHA_ROUTER_URL": value})

    def test_router_url_must_not_carry_credentials(self):
        """The web UI refuses a router URL with credentials, and this value is printed,
        put in a sidecar argv where ps shows it, and exported into a shell line for Pi."""
        with self.assertRaises(supervisor_start.ConfigurationError):
            supervisor_start.resolve_router_url({"MECHA_ROUTER_URL": "http://user:pw@127.0.0.1:8765"})

    def test_router_url_userinfo_cannot_smuggle_a_remote_host(self):
        with self.assertRaises(supervisor_start.ConfigurationError):
            supervisor_start.resolve_router_url({"MECHA_ROUTER_URL": "http://127.0.0.1@evil.example:8765"})

    def test_router_url_needs_an_explicit_valid_port(self):
        for value in ("http://127.0.0.1", "http://127.0.0.1:abc", "http://127.0.0.1:99999"):
            with self.assertRaises(supervisor_start.ConfigurationError):
                supervisor_start.resolve_router_url({"MECHA_ROUTER_URL": value})

    def test_localhost_is_normalized_because_the_router_will_not_bind_it(self):
        """`run_router.mjs` binds 127.0.0.1 and ::1 only, so passing `--host localhost`
        makes it exit at startup and the failure surfaces 15 seconds later in a dead pane."""
        self.assertEqual(
            supervisor_start.resolve_router_url({"MECHA_ROUTER_URL": "http://localhost:8765"}),
            "http://127.0.0.1:8765",
        )

    def test_router_url_is_normalized(self):
        self.assertEqual(
            supervisor_start.resolve_router_url({"MECHA_ROUTER_URL": " http://127.0.0.1:8799/ "}),
            PROBE_ROUTER,
        )

    def test_window_suffix_is_empty_in_production(self):
        self.assertEqual(supervisor_start.resolve_window_suffix({}), "")
        self.assertEqual(supervisor_start.window_name("supervisor", ""), "supervisor")

    def test_window_suffix_applies_to_every_window_together(self):
        suffix = supervisor_start.resolve_window_suffix({"MECHA_SUPERVISOR_START_SUFFIX": "coldtest"})
        self.assertEqual(supervisor_start.window_name("supervisor", suffix), "supervisor-coldtest")
        self.assertEqual(supervisor_start.window_name("mailbox-router", suffix), "mailbox-router-coldtest")

    def test_window_suffix_must_keep_the_name_a_valid_mailbox_address(self):
        for value in ("has space", "dotted.name", "semi;colon"):
            with self.assertRaises(supervisor_start.ConfigurationError):
                supervisor_start.resolve_window_suffix({"MECHA_SUPERVISOR_START_SUFFIX": value})

    def test_a_window_name_that_is_not_a_mailbox_address_is_rejected(self):
        with self.assertRaises(supervisor_start.ConfigurationError):
            supervisor_start.window_name("_leading-underscore", "")


class AgentResolutionTest(unittest.TestCase):
    def _config(self, tmpdir, payload):
        path = pathlib.Path(tmpdir) / "worker-runtime.json"
        path.write_text(payload, encoding="utf-8")
        return {"MECHA_WORKER_RUNTIME_CONFIG": str(path)}

    def test_machine_config_supplies_the_default(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._config(tmpdir, '{"default_agent":"claude"}')
            self.assertEqual(supervisor_start.resolve_agent(None, environment=environment), "claude")

    def test_explicit_agent_beats_the_config(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._config(tmpdir, '{"default_agent":"claude"}')
            self.assertEqual(supervisor_start.resolve_agent("pi", environment=environment), "pi")

    def test_a_missing_config_is_an_error_rather_than_a_silent_pi_default(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = {"MECHA_WORKER_RUNTIME_CONFIG": os.path.join(tmpdir, "absent.json")}
            with self.assertRaises(supervisor_start.ConfigurationError) as caught:
                supervisor_start.resolve_agent(None, environment=environment)
            self.assertIn("--agent", str(caught.exception))

    def test_a_runtime_this_script_cannot_launch_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._config(tmpdir, '{"default_agent":"cursor"}')
            with self.assertRaises(supervisor_start.ConfigurationError):
                supervisor_start.resolve_agent(None, environment=environment)
        with self.assertRaises(supervisor_start.ConfigurationError):
            supervisor_start.resolve_agent("cursor", environment={})
        with self.assertRaises(supervisor_start.ConfigurationError):
            supervisor_start.agent_command("cursor", "prompt")

    def test_a_malformed_config_is_a_configuration_error(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._config(tmpdir, "{not json")
            with self.assertRaises(supervisor_start.ConfigurationError):
                supervisor_start.resolve_agent(None, environment=environment)


class ProbeTest(unittest.TestCase):
    def test_router_health_needs_status_ok(self):
        healthy = fake_opener({"http://127.0.0.1:8765/v1/health": (200, {"status": "ok"})})
        self.assertTrue(supervisor_start.router_is_healthy("http://127.0.0.1:8765", opener=healthy))
        degraded = fake_opener({"http://127.0.0.1:8765/v1/health": (200, {"status": "starting"})})
        self.assertFalse(supervisor_start.router_is_healthy("http://127.0.0.1:8765", opener=degraded))
        self.assertFalse(supervisor_start.router_is_healthy("http://127.0.0.1:8765", opener=fake_opener({})))

    def test_router_health_is_probed_at_the_configured_url(self):
        opener = fake_opener({f"{PROBE_ROUTER}/v1/health": (200, {"status": "ok"})})
        self.assertTrue(supervisor_start.router_is_healthy(PROBE_ROUTER, opener=opener))
        self.assertFalse(supervisor_start.router_is_healthy("http://127.0.0.1:8765", opener=opener))

    def test_web_ui_health_needs_a_200_on_the_configured_port(self):
        opener = fake_opener({"http://127.0.0.1:8800/": (200, {})})
        self.assertTrue(supervisor_start.web_ui_is_healthy(8800, opener=opener))
        self.assertFalse(supervisor_start.web_ui_is_healthy(8787, opener=opener))
        self.assertFalse(supervisor_start.web_ui_is_healthy(
            8800, opener=fake_opener({"http://127.0.0.1:8800/": (503, {})})))

    def test_router_lists_agent_matches_the_whole_id(self):
        opener = fake_opener({
            f"{PROBE_ROUTER}/v1/agents": (200, {"agents": [{"agent_id": PROBE_WINDOW}]}),
        })
        self.assertTrue(supervisor_start.router_lists_agent(PROBE_ROUTER, PROBE_WINDOW, opener=opener))
        self.assertFalse(supervisor_start.router_lists_agent(PROBE_ROUTER, "supervisor", opener=opener))

    def test_a_malformed_agent_list_is_not_a_registration(self):
        opener = fake_opener({f"{PROBE_ROUTER}/v1/agents": (200, {"agents": "not a list"})})
        self.assertFalse(supervisor_start.router_lists_agent(PROBE_ROUTER, "supervisor", opener=opener))

    def test_a_top_level_payload_that_is_not_an_object_is_not_a_registration(self):
        """Valid JSON, wrong shape (a bare list here). Calling `.get` on it without a shape
        check raises AttributeError, which escapes this predicate mid-poll and replaces the
        script's worded failure message with a raw traceback."""
        opener = fake_opener({f"{PROBE_ROUTER}/v1/agents": (200, ["not", "an", "object"])})
        self.assertFalse(supervisor_start.router_lists_agent(PROBE_ROUTER, "supervisor", opener=opener))

    def test_a_missing_window_reads_as_no_agent_running_rather_than_raising(self):
        import _tmux
        with patch.object(_tmux, "run", fake_tmux({})):
            self.assertEqual(_tmux.pane_commands("supervisor"), [])
        self.assertFalse(supervisor_start.agent_is_running("supervisor", panes=fake_panes({})))

    def test_pane_commands_returns_one_entry_per_pane(self):
        import _tmux
        with patch.object(_tmux, "run", fake_tmux({"supervisor": ["zsh", "2.1.226"]})):
            self.assertEqual(_tmux.pane_commands("supervisor"), ["zsh", "2.1.226"])

    def test_every_listed_shell_reads_as_no_agent(self):
        for shell in sorted(supervisor_start.SHELL_COMMANDS):
            with self.subTest(shell=shell):
                self.assertFalse(
                    supervisor_start.agent_is_running("supervisor", panes=fake_panes({"supervisor": [shell]}))
                )

    def test_a_running_claude_is_an_agent(self):
        self.assertTrue(
            supervisor_start.agent_is_running("supervisor", panes=fake_panes({"supervisor": ["2.1.226"]}))
        )

    def test_an_agent_in_any_pane_counts_as_occupied(self):
        """send-keys without a pane suffix targets the ACTIVE pane. Deciding on pane 0
        while writing to the active pane types a launch command into a live agent whenever
        someone has split the window to work beside it."""
        self.assertTrue(
            supervisor_start.agent_is_running("supervisor", panes=fake_panes({"supervisor": ["zsh", "2.1.226"]}))
        )

    def test_tui_readiness_needs_a_marker_not_just_a_pane(self):
        self.assertFalse(supervisor_start.claude_tui_ready("supervisor", capture=lambda _w, lines: "starting\n"))
        self.assertFalse(supervisor_start.claude_tui_ready("supervisor", capture=lambda _w, lines: None))
        for marker in supervisor_start.CLAUDE_READY_MARKERS:
            with self.subTest(marker=marker):
                self.assertTrue(
                    supervisor_start.claude_tui_ready("supervisor", capture=lambda _w, lines: f"  {marker}")
                )

    def test_tui_readiness_reads_the_visible_screen_not_scrollback(self):
        """A crashed Claude leaves its status line in scrollback. Reading history makes the
        check true on the first poll for a pane sitting at a shell, and the bootstrap
        prompt is then pasted into that shell and lost."""
        captured = {}

        def capture(window, lines):
            captured["lines"] = lines
            return ""

        supervisor_start.claude_tui_ready("supervisor", capture=capture)
        self.assertEqual(captured["lines"], 0)

    def test_a_stale_marker_that_never_clears_is_not_ready(self):
        clock = FakeClock()
        self.assertFalse(supervisor_start.wait_for_fresh_marker(
            "supervisor", 1.0, ready=lambda _w: True, sleep=clock.sleep, now=clock,
        ))

    def test_the_two_phases_share_one_deadline_rather_than_each_getting_the_full_budget(self):
        """A caller quotes `ready_timeout` in its own error message, so the total cost of
        this call must stay at `timeout`, not `2 * timeout`. To see the difference at all,
        phase one must succeed (the marker actually clears) so phase two runs and can burn
        its own separate budget if the two are not sharing one deadline: a marker that never
        clears at all makes phase one fail and return early in BOTH the fixed and the buggy
        shape, which proves nothing. Here the marker is present until t=3, then absent
        forever, so phase one succeeds quickly and phase two times out completely."""
        clock = FakeClock()
        cleared_at = 3.0
        supervisor_start.wait_for_fresh_marker(
            "supervisor", 10.0, ready=lambda _w: clock.now < cleared_at,
            sleep=clock.sleep, now=clock,
        )
        self.assertLessEqual(clock.now, 10.0 + supervisor_start.POLL_INTERVAL_SECONDS)

    def test_a_marker_that_appears_after_the_pane_clears_is_ready(self):
        clock = FakeClock()
        states = iter([True, False, False, True])
        self.assertTrue(supervisor_start.wait_for_fresh_marker(
            "supervisor", 5.0, ready=lambda _w: next(states, True), sleep=clock.sleep, now=clock,
        ))

    def test_process_argv_reads_the_command_line(self):
        def run(command, capture_output, text, check):
            return SimpleNamespace(returncode=0, stdout="node claude_sidecar.mjs --agent-id x\n")

        self.assertEqual(supervisor_start.read_process_argv(42, run=run), "node claude_sidecar.mjs --agent-id x")

    def test_process_argv_on_a_dead_pid_is_empty_rather_than_an_error(self):
        def run(command, capture_output, text, check):
            return SimpleNamespace(returncode=1, stdout="")

        self.assertEqual(supervisor_start.read_process_argv(42, run=run), "")


class ClockThreadingTest(unittest.TestCase):
    """Every internal `wait_until` call must pass its own `now`.

    `wait_until` defaults `now` to `time.monotonic`. A caller that forgets `now=now` while
    still passing a fake `sleep` mixes a sleep that does not block with a deadline measured
    against the real clock: the predicate loop then spins for the real `timeout` instead of
    the instant the FakeClock is built to produce, which is exactly the defect that made this
    suite take 7+ seconds before every waiting function threaded its clock through. A runtime
    test for this would have to actually wait out a real timeout to notice, which defeats the
    point; reading the call sites is what actually catches a dropped `now=`.
    """

    FUNCTIONS_WITH_INTERNAL_WAITS = (
        "ensure_service_window", "ensure_agent", "ensure_sidecar",
        "verify_delivery", "wait_for_fresh_marker",
    )

    def test_every_internal_wait_until_call_passes_now(self):
        import ast
        import inspect

        offenders = []
        for name in self.FUNCTIONS_WITH_INTERNAL_WAITS:
            source = inspect.getsource(getattr(supervisor_start, name))
            tree = ast.parse(source)
            for node in ast.walk(tree):
                if (isinstance(node, ast.Call) and isinstance(node.func, ast.Name)
                        and node.func.id == "wait_until"):
                    if not any(keyword.arg == "now" for keyword in node.keywords):
                        offenders.append(f"{name}:{node.lineno}")
        self.assertEqual(offenders, [], f"wait_until() call(s) missing now=: {offenders}")


class WaitUntilTest(unittest.TestCase):
    def test_a_condition_that_already_holds_never_sleeps(self):
        clock = FakeClock()
        self.assertTrue(supervisor_start.wait_until(lambda: True, 10, sleep=clock.sleep, now=clock))
        self.assertEqual(clock.now, 0.0)

    def test_a_condition_that_never_holds_gives_up_on_elapsed_time(self):
        clock = FakeClock()
        self.assertFalse(supervisor_start.wait_until(
            lambda: False, 1.0, sleep=clock.sleep, interval=0.25, now=clock))
        self.assertGreaterEqual(clock.now, 1.0)

    def test_the_budget_counts_slow_predicates_not_only_sleeps(self):
        """Every predicate here is an HTTP probe with its own timeout. Counting sleeps
        alone let a router that accepts a connection and never answers stretch a 15 second
        step past two minutes."""
        clock = FakeClock()

        def slow_predicate():
            clock.now += 2.0  # what a socket timeout costs
            return False

        self.assertFalse(supervisor_start.wait_until(
            slow_predicate, 4.0, sleep=clock.sleep, interval=0.25, now=clock))
        self.assertLess(clock.now, 10.0)


class ServiceWindowTest(unittest.TestCase):
    def _kwargs(self, **overrides):
        self.sent = []
        self.created = []
        self.removed = []
        clock = FakeClock()
        kwargs = dict(
            output=lambda _m: None,
            sleep=clock.sleep,
            now=clock,
            send_command=lambda window, command: self.sent.append((window, command)),
            exists=lambda _w: False,
            make_session=lambda: None,
            make_window=self.created.append,
            remove_window=lambda window, sweep_sidecar: self.removed.append(window),
        )
        kwargs.update(overrides)
        return kwargs

    def test_a_healthy_service_starts_nothing(self):
        sessions = []
        started = supervisor_start.ensure_service_window(
            "mailbox-router", "cmd", lambda: True, "router",
            **self._kwargs(make_session=lambda: sessions.append("session")),
        )
        self.assertFalse(started)
        self.assertEqual(self.sent, [])
        self.assertEqual(self.created, [])
        self.assertEqual(sessions, [])

    def test_an_existing_window_is_never_typed_into(self):
        with self.assertRaises(supervisor_start.StartupError) as caught:
            supervisor_start.ensure_service_window(
                "mailbox-router", "cmd", lambda: False, "router",
                **self._kwargs(exists=lambda _w: True),
            )
        self.assertEqual(self.sent, [])
        self.assertEqual(self.created, [])
        self.assertEqual(self.removed, [])
        self.assertIn("already exists", str(caught.exception))

    def test_an_unhealthy_service_is_started_and_waited_for(self):
        health = iter([False, False, True])
        started = supervisor_start.ensure_service_window(
            "mailbox-router-coldtest", "the-command", lambda: next(health), "router",
            **self._kwargs(),
        )
        self.assertTrue(started)
        self.assertEqual(self.created, ["mailbox-router-coldtest"])
        self.assertEqual(self.sent, [("mailbox-router-coldtest", "the-command")])

    def test_a_deferred_command_is_only_built_when_something_is_started(self):
        builds = []

        def build():
            builds.append("built")
            return "the-command"

        supervisor_start.ensure_service_window(
            "mailbox-router", build, lambda: True, "router", **self._kwargs())
        self.assertEqual(builds, [])
        supervisor_start.ensure_service_window(
            "mailbox-router", build, lambda: len(builds) == 1, "router", **self._kwargs())
        self.assertEqual(builds, ["built"])

    def test_a_service_that_never_answers_removes_the_window_it_created(self):
        """Left behind, that window trips the refusal above on every later run, so one
        transient failure would turn the start path into one that refuses forever."""
        with patch.object(supervisor_start, "capture_pane", return_value="boom: EADDRINUSE"):
            with self.assertRaises(supervisor_start.StartupError) as caught:
                supervisor_start.ensure_service_window(
                    "mailbox-router", "cmd", lambda: False, "router",
                    timeout=1.0, **self._kwargs(),
                )
        self.assertEqual(self.removed, ["mailbox-router"])
        self.assertIn("re-running this is safe", str(caught.exception))
        self.assertIn("EADDRINUSE", str(caught.exception))


class SupervisorWindowTest(unittest.TestCase):
    def test_the_session_is_created_before_the_window_is_counted(self):
        """The bug this guards: `ensure_session` creates the session with a window already
        named `supervisor`, and `new_window` calls `ensure_session` itself. Checking for the
        window before creating the session made two windows with the same name."""
        windows = set()
        supervisor_start.ensure_supervisor_window(
            "supervisor",
            output=lambda _m: None,
            exists=lambda name: name in windows,
            make_session=lambda: windows.add("supervisor"),
            make_window=lambda name: windows.add(f"duplicate-{name}"),
        )
        self.assertEqual(windows, {"supervisor"})

    def test_a_missing_window_is_created(self):
        windows = set()
        created = supervisor_start.ensure_supervisor_window(
            PROBE_WINDOW,
            output=lambda _m: None,
            exists=lambda name: name in windows,
            make_session=lambda: None,
            make_window=windows.add,
        )
        self.assertTrue(created)
        self.assertEqual(windows, {PROBE_WINDOW})

    def test_a_window_that_cannot_be_created_fails(self):
        with self.assertRaises(supervisor_start.StartupError):
            supervisor_start.ensure_supervisor_window(
                "supervisor", output=lambda _m: None, exists=lambda _n: False,
                make_session=lambda: None, make_window=lambda _n: None,
            )


class AgentLaunchTest(unittest.TestCase):
    def _launch(self, **overrides):
        """Collaborators wired so the agent only 'starts' once Enter has been pressed.

        A fake that reports the agent running regardless lets the launch line be typed and
        never submitted while the test still passes.
        """
        self.calls = {"cleared": [], "sent": [], "entered": [], "prompts": []}
        clock = FakeClock()
        kwargs = dict(
            output=lambda _m: None,
            sleep=clock.sleep,
            now=clock,
            clear_input=self.calls["cleared"].append,
            send=lambda window, text: self.calls["sent"].append((window, text)),
            enter=self.calls["entered"].append,
            send_prompt=lambda window, text: self.calls["prompts"].append((window, text)),
            is_running=lambda _w: bool(self.calls["entered"]),
            wait_ready=lambda _w, _t, sleep=None, now=None: True,
        )
        kwargs.update(overrides)
        return kwargs

    def test_an_agent_already_running_is_left_alone(self):
        kwargs = self._launch(is_running=lambda _w: True)
        state = supervisor_start.ensure_agent(PROBE_WINDOW, "claude", "/tmp", "claude", "prompt", **kwargs)
        self.assertEqual(state, supervisor_start.AGENT_ALREADY_RUNNING)
        self.assertEqual(self.calls["sent"], [])
        self.assertEqual(self.calls["prompts"], [])
        self.assertEqual(self.calls["cleared"], [])

    def test_claude_is_launched_and_gets_its_bootstrap_prompt(self):
        kwargs = self._launch()
        state = supervisor_start.ensure_agent(
            PROBE_WINDOW, "claude", "/tmp/wd", "claude --x", "the-prompt", **kwargs)
        self.assertEqual(state, supervisor_start.AGENT_BOOTSTRAPPED)
        self.assertEqual(self.calls["cleared"], [PROBE_WINDOW])
        self.assertEqual(self.calls["sent"],
                         [(PROBE_WINDOW, "cd /tmp/wd && unset CLAUDECODE && claude --x")])
        self.assertEqual(self.calls["entered"], [PROBE_WINDOW])
        self.assertEqual(self.calls["prompts"], [(PROBE_WINDOW, "the-prompt")])

    def test_the_pane_input_line_is_cleared_before_the_launch_line_is_typed(self):
        """This window is reused rather than created fresh, so anything a human left
        half-typed would otherwise become a prefix of the launch command."""
        order = []
        kwargs = self._launch(
            clear_input=lambda w: order.append("clear"),
            send=lambda w, t: order.append("send"),
        )
        kwargs["is_running"] = lambda _w: "enter" in order
        kwargs["enter"] = lambda w: order.append("enter")
        supervisor_start.ensure_agent(PROBE_WINDOW, "claude", "/tmp", "claude", "p", **kwargs)
        self.assertEqual(order[:3], ["clear", "send", "enter"])

    def test_a_runtime_that_takes_its_prompt_as_an_argument_is_not_pasted_twice(self):
        kwargs = self._launch()
        state = supervisor_start.ensure_agent(PROBE_WINDOW, "pi", "/tmp/wd", "pi --approve p", None, **kwargs)
        self.assertEqual(state, supervisor_start.AGENT_BOOTSTRAPPED)
        self.assertEqual(self.calls["prompts"], [])

    def test_pi_exports_reach_the_launch_line(self):
        kwargs = self._launch()
        supervisor_start.ensure_agent(
            PROBE_WINDOW, "pi", "/tmp/wd", "pi", None, pi_exports="export A=b && ", **kwargs)
        self.assertIn("export A=b && pi", self.calls["sent"][0][1])

    def test_an_agent_that_never_starts_fails_without_sending_a_prompt(self):
        kwargs = self._launch(is_running=lambda _w: False)
        with self.assertRaises(supervisor_start.StartupError):
            supervisor_start.ensure_agent(
                PROBE_WINDOW, "claude", "/tmp", "claude", "prompt", start_timeout=1.0, **kwargs)
        self.assertEqual(self.calls["prompts"], [])

    def test_a_tui_that_never_becomes_ready_reports_not_bootstrapped_and_prints_the_prompt(self):
        printed = []
        kwargs = self._launch(wait_ready=lambda _w, _t, sleep=None, now=None: False, output=printed.append)
        state = supervisor_start.ensure_agent(
            PROBE_WINDOW, "claude", "/tmp", "claude", "the-prompt", ready_timeout=1.0, **kwargs)
        self.assertEqual(state, supervisor_start.AGENT_NOT_BOOTSTRAPPED)
        self.assertEqual(self.calls["prompts"], [])
        joined = "\n".join(printed)
        self.assertIn("NOT sent", joined)
        self.assertIn("the-prompt", joined)


class SidecarTest(unittest.TestCase):
    def _kwargs(self, **overrides):
        self.listed = []
        clock = FakeClock()
        kwargs = dict(
            output=lambda _m: None,
            sleep=clock.sleep,
            now=clock,
            lists_agent=lambda url, agent: self.listed.append((url, agent)) or True,
            argv_of=lambda _pid: "node claude_sidecar.mjs --agent-id supervisor",
        )
        kwargs.update(overrides)
        return kwargs

    def test_one_existing_sidecar_is_reused_and_verified_against_the_router(self):
        with patch.object(supervisor_start.subprocess, "Popen") as popen:
            pid = supervisor_start.ensure_sidecar(
                PROBE_WINDOW, PROBE_ROUTER, **self._kwargs(find_pids=lambda _w: [4242]),
            )
        self.assertEqual(pid, 4242)
        popen.assert_not_called()
        self.assertEqual(self.listed, [(PROBE_ROUTER, PROBE_WINDOW)])

    def test_no_sidecar_starts_exactly_one_with_the_live_argv(self):
        started = []
        pids = iter([[], [7], [7]])
        supervisor_start.ensure_sidecar(
            PROBE_WINDOW, PROBE_ROUTER,
            **self._kwargs(
                find_pids=lambda _w: next(pids),
                popen=lambda argv, **kwargs: started.append((argv, kwargs)),
            ),
        )
        self.assertEqual(len(started), 1)
        argv, kwargs = started[0]
        self.assertEqual(argv, supervisor_start.sidecar_argv(PROBE_WINDOW, PROBE_ROUTER))
        self.assertEqual(kwargs["stdout"], subprocess.DEVNULL)
        self.assertEqual(kwargs["stderr"], subprocess.DEVNULL)
        self.assertTrue(kwargs["start_new_session"])
        self.assertEqual(self.listed, [(PROBE_ROUTER, PROBE_WINDOW)])

    def test_duplicate_sidecars_are_refused_without_starting_or_killing_anything(self):
        with patch.object(supervisor_start.subprocess, "Popen") as popen:
            with self.assertRaises(supervisor_start.StartupError) as caught:
                supervisor_start.ensure_sidecar(
                    PROBE_WINDOW, PROBE_ROUTER, **self._kwargs(find_pids=lambda _w: [11, 12]),
                )
        popen.assert_not_called()
        self.assertIn("11, 12", str(caught.exception))

    def test_an_unreadable_process_table_is_not_treated_as_no_sidecar(self):
        def find_pids(_window):
            raise supervisor_start.ProcessTableUnavailable("ps exited with status 1")

        with patch.object(supervisor_start.subprocess, "Popen") as popen:
            with self.assertRaises(supervisor_start.StartupError):
                supervisor_start.ensure_sidecar(
                    PROBE_WINDOW, PROBE_ROUTER, **self._kwargs(find_pids=find_pids))
        popen.assert_not_called()

    def test_an_unreadable_process_table_during_the_start_poll_is_not_success(self):
        calls = {"n": 0}

        def find_pids(_window):
            calls["n"] += 1
            if calls["n"] == 1:
                return []
            raise supervisor_start.ProcessTableUnavailable("ps failed")

        with self.assertRaises(supervisor_start.StartupError):
            supervisor_start.ensure_sidecar(
                PROBE_WINDOW, PROBE_ROUTER, timeout=1.0,
                **self._kwargs(find_pids=find_pids, popen=lambda *a, **k: None),
            )

    def test_a_process_the_router_does_not_list_is_a_failure(self):
        """A live sidecar pointed at a different router looks identical to a good one:
        `find_sidecar_pids` matches on --agent-id and never on --router-url."""
        with self.assertRaises(supervisor_start.StartupError) as caught:
            supervisor_start.ensure_sidecar(
                PROBE_WINDOW, PROBE_ROUTER, timeout=1.0,
                **self._kwargs(
                    find_pids=lambda _w: [4242],
                    lists_agent=lambda _u, _a: False,
                    argv_of=lambda _pid: "node claude_sidecar.mjs --agent-id x --router-url http://127.0.0.1:8765",
                ),
            )
        message = str(caught.exception)
        self.assertIn("does not list it", message)
        self.assertIn("http://127.0.0.1:8765", message)
        self.assertIn(f"{PROBE_WINDOW}.log", message)


class DeliveryTest(unittest.TestCase):
    def test_the_message_id_is_read_out_of_the_acceptance_line(self):
        self.assertEqual(
            supervisor_start.parse_accepted_message_id("Message GoojWZNyDBPynoYfiIBEl accepted: queued\n"),
            "GoojWZNyDBPynoYfiIBEl",
        )

    def test_unreadable_send_output_fails_rather_than_guessing(self):
        for output in ("", "something else entirely", "Message accepted"):
            with self.assertRaises(supervisor_start.StartupError):
                supervisor_start.parse_accepted_message_id(output)

    def _log(self, tmpdir, name, text):
        (pathlib.Path(tmpdir) / name).write_text(text, encoding="utf-8")
        return str(tmpdir)

    def test_an_ack_in_the_live_log_counts(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            self._log(tmpdir, "supervisor.log", 'x event=delivery_acked message_id="abc" attempt="1"\n')
            self.assertTrue(supervisor_start.delivery_acked("supervisor", "abc", tmpdir))

    def test_the_two_fields_are_matched_in_either_order(self):
        """They are adjacent today only because of key order at the sidecar's log() call.
        Depending on that adjacency turns a harmless reordering there into this script
        reporting broken mail on a healthy machine."""
        with tempfile.TemporaryDirectory() as tmpdir:
            self._log(tmpdir, "supervisor.log", 'x attempt="1" message_id="abc" event=delivery_acked\n')
            self.assertTrue(supervisor_start.delivery_acked("supervisor", "abc", tmpdir))

    def test_the_two_fields_must_be_on_the_same_line(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            self._log(tmpdir, "supervisor.log",
                      'x event=delivery_acked message_id="other"\ny event=delivery_received message_id="abc"\n')
            self.assertFalse(supervisor_start.delivery_acked("supervisor", "abc", tmpdir))

    def test_an_ack_only_in_the_rotated_log_counts(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            self._log(tmpdir, "supervisor.log", 'x event=mailbox_client state="event"\n')
            self._log(tmpdir, "supervisor.log.1", 'x event=delivery_acked message_id="abc" attempt="1"\n')
            self.assertTrue(supervisor_start.delivery_acked("supervisor", "abc", tmpdir))

    def test_another_messages_ack_does_not_count(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            self._log(tmpdir, "supervisor.log", 'x event=delivery_acked message_id="abcd" attempt="1"\n')
            self.assertFalse(supervisor_start.delivery_acked("supervisor", "abc", tmpdir))

    def test_another_agents_log_does_not_count(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            self._log(tmpdir, "someone-else.log", 'x event=delivery_acked message_id="abc"\n')
            self.assertFalse(supervisor_start.delivery_acked("supervisor", "abc", tmpdir))

    def test_received_without_acked_does_not_count(self):
        """The sidecar logs `delivery_received` before it pastes and `delivery_acked` only
        after the paste returns. Accepting the first would report success on the exact
        failure this check exists to catch."""
        with tempfile.TemporaryDirectory() as tmpdir:
            self._log(tmpdir, "supervisor.log",
                      'x event=delivery_received message_id="abc" attempt="1" from="w" to="supervisor"\n')
            self.assertFalse(supervisor_start.delivery_acked("supervisor", "abc", tmpdir))

    def test_a_missing_log_does_not_count(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            self.assertFalse(supervisor_start.delivery_acked("supervisor", "abc", tmpdir))

    def test_a_confirmed_delivery_runs_against_the_real_log_reader(self):
        """Deliberately not injecting `acked`. A fake there accepts any argument order, so
        swapping window and message id at the call site survives — and in production that
        looks for the window name inside a file named after the message id, and the check
        can never pass."""
        clock = FakeClock()
        with tempfile.TemporaryDirectory() as tmpdir:
            self._log(tmpdir, f"{PROBE_WINDOW}.log",
                      'x event=delivery_acked message_id="mid-1" attempt="1"\n')
            message_id = supervisor_start.verify_delivery(
                PROBE_WINDOW, PROBE_ROUTER,
                output=lambda _m: None, sleep=clock.sleep, now=clock, log_dir=tmpdir,
                send=lambda _w, _u: "mid-1",
            )
        self.assertEqual(message_id, "mid-1")

    def test_a_log_holding_only_another_id_is_not_a_delivery(self):
        clock = FakeClock()
        with tempfile.TemporaryDirectory() as tmpdir:
            self._log(tmpdir, f"{PROBE_WINDOW}.log",
                      'x event=delivery_acked message_id="different" attempt="1"\n')
            with self.assertRaises(supervisor_start.StartupError):
                supervisor_start.verify_delivery(
                    PROBE_WINDOW, PROBE_ROUTER,
                    output=lambda _m: None, sleep=clock.sleep, now=clock, log_dir=tmpdir, timeout=1.0,
                    send=lambda _w, _u: "mid-1", diagnose=lambda _u, _m: "no idea",
                )

    def test_an_unacked_delivery_fails_and_reports_the_router_state(self):
        clock = FakeClock()
        with self.assertRaises(supervisor_start.StartupError) as caught:
            supervisor_start.verify_delivery(
                PROBE_WINDOW, PROBE_ROUTER,
                output=lambda _m: None, sleep=clock.sleep, now=clock, timeout=1.0,
                send=lambda _w, _u: "mid-1", acked=lambda _w, _m, _d: False,
                diagnose=lambda _u, _m: "the router still holds it in state 'queued'",
            )
        message = str(caught.exception)
        self.assertIn("never acked", message)
        self.assertIn("state 'queued'", message)

    def test_the_send_targets_the_configured_router_and_the_window(self):
        recorded = {}

        def run(command, capture_output, text, check):
            recorded["command"] = command
            return SimpleNamespace(returncode=0, stdout="Message mid-9 accepted: queued\n", stderr="")

        message_id = supervisor_start.send_startup_message(PROBE_WINDOW, PROBE_ROUTER, run=run)
        self.assertEqual(message_id, "mid-9")
        command = recorded["command"]
        self.assertEqual(command[1], supervisor_start.MAILBOX_SEND_SCRIPT)
        self.assertEqual(command[command.index("--to") + 1], PROBE_WINDOW)
        self.assertEqual(command[command.index("--router-url") + 1], PROBE_ROUTER)
        self.assertEqual(command[command.index("--from") + 1], supervisor_start.STARTUP_SENDER)
        self.assertEqual(command[command.index("--subject") + 1], supervisor_start.STARTUP_SUBJECT)
        self.assertEqual(command[command.index("--body") + 1], supervisor_start.STARTUP_BODY)

    def test_a_failed_send_names_the_exit_code_and_the_command(self):
        def run(command, capture_output, text, check):
            return SimpleNamespace(returncode=2, stdout="", stderr="Router rejected message: queue_full")

        with self.assertRaises(supervisor_start.StartupError) as caught:
            supervisor_start.send_startup_message(PROBE_WINDOW, PROBE_ROUTER, run=run)
        message = str(caught.exception)
        self.assertIn("queue_full", message)
        self.assertIn("exited 2", message)
        self.assertIn("mailbox_send.py", message)
        self.assertTrue(caught.exception.report)

    def test_a_duplicate_suppressed_send_stays_failed_without_another_notice(self):
        responses = iter([
            SimpleNamespace(returncode=1, stdout="", stderr="Router unavailable: refused"),
            SimpleNamespace(returncode=1, stdout="", stderr=""),
        ])

        def run(command, capture_output, text, check):
            return next(responses)

        with self.assertRaises(supervisor_start.StartupError) as first:
            supervisor_start.send_startup_message(PROBE_WINDOW, PROBE_ROUTER, run=run)
        with self.assertRaises(supervisor_start.StartupError) as duplicate:
            supervisor_start.send_startup_message(PROBE_WINDOW, PROBE_ROUTER, run=run)

        self.assertTrue(first.exception.report)
        self.assertFalse(duplicate.exception.report)
        self.assertNotIn("Router unavailable: refused", str(duplicate.exception))


class DiagnosticTest(unittest.TestCase):
    def test_a_cleared_message_does_not_claim_it_was_delivered(self):
        """404 also covers TTL expiry, dead-letter dismissal, and a batch merge into
        another queued message for the same recipient. Printed under a failure, "it was
        acked" would contradict its own sentence."""
        def opener(_url, timeout=None):
            raise HTTPError("url", 404, "Not Found", {}, None)

        text = supervisor_start.message_diagnostic(PROBE_ROUTER, "mid", opener=opener)
        self.assertIn("no longer holds it", text)
        self.assertIn("expired", text)
        self.assertNotIn("which means it was acked", text)

    def test_a_held_message_reports_its_state(self):
        opener = fake_opener({f"{PROBE_ROUTER}/v1/messages/mid": (200, {"message": {"state": "queued"}})})
        self.assertIn("state 'queued'",
                      supervisor_start.message_diagnostic(PROBE_ROUTER, "mid", opener=opener))

    def test_an_unreachable_router_says_so_rather_than_guessing(self):
        self.assertIn("could not be asked",
                      supervisor_start.message_diagnostic(PROBE_ROUTER, "mid", opener=fake_opener({})))

    def test_a_top_level_payload_that_is_not_an_object_does_not_raise(self):
        opener = fake_opener({f"{PROBE_ROUTER}/v1/messages/mid": (200, ["not", "an", "object"])})
        text = supervisor_start.message_diagnostic(PROBE_ROUTER, "mid", opener=opener)
        self.assertIn("state None", text)


class RunStartupTest(unittest.TestCase):
    """End-to-end sequencing with every step recorded, arguments included.

    Recording only the call order leaves everything `run_startup` computes unpinned, and a
    step wired to a module default instead of the resolved value still passes.
    """

    @contextlib.contextmanager
    def _patched(self, calls):
        def record(name, result=None):
            def recorder(*args, **kwargs):
                calls.append((name, args, kwargs))
                return result
            return recorder

        with patch.object(supervisor_start, "ensure_service_window", side_effect=record("service")), \
                patch.object(supervisor_start, "ensure_supervisor_window", side_effect=record("window")), \
                patch.object(supervisor_start, "ensure_agent",
                             side_effect=record("agent", supervisor_start.AGENT_BOOTSTRAPPED)), \
                patch.object(supervisor_start, "ensure_sidecar", side_effect=record("sidecar")), \
                patch.object(supervisor_start, "verify_delivery", side_effect=record("delivery")):
            yield

    def _environment(self, tmpdir, **extra):
        config = pathlib.Path(tmpdir) / "worker-runtime.json"
        config.write_text('{"default_agent":"claude"}', encoding="utf-8")
        environment = {
            "MECHA_WORKER_RUNTIME_CONFIG": str(config),
            # Pinned so the suite never writes into the real ~/.mecha-developer.
            "MECHA_ROUTER_DATABASE": os.path.join(tmpdir, "router.sqlite"),
            "MECHA_ROUTER_LOG": os.path.join(tmpdir, "router.jsonl"),
        }
        environment.update(extra)
        return environment

    def _run(self, tmpdir, environment, agent=None, compaction_policy=None):
        calls = []
        args = SimpleNamespace(dir=tmpdir, agent=agent, compaction_policy=compaction_policy)
        with self._patched(calls):
            code = supervisor_start.run_startup(args, environment=environment, output=lambda _m: None)
        return code, calls

    def test_the_six_steps_run_in_order_and_carry_the_resolved_configuration(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._environment(
                tmpdir,
                MECHA_ROUTER_URL=PROBE_ROUTER,
                MECHA_ROUTER_WEB_UI_PORT="8800",
                MECHA_SUPERVISOR_START_SUFFIX="coldtest",
            )
            code, calls = self._run(tmpdir, environment)

        self.assertEqual(code, 0)
        self.assertEqual([name for name, _a, _k in calls],
                         ["service", "service", "window", "agent", "sidecar", "delivery"])

        router_call, web_ui_call, window_call, agent_call, sidecar_call, delivery_call = calls

        # Step 1 targets the suffixed window and builds the command from the overrides.
        self.assertEqual(router_call[1][0], "mailbox-router-coldtest")
        router_command = router_call[1][1]
        router_command = router_command() if callable(router_command) else router_command
        self.assertIn("--port 8799", router_command)
        self.assertIn(os.path.join(tmpdir, "router.sqlite"), router_command)
        self.assertIn(os.path.join(tmpdir, "router.jsonl"), router_command)

        # Step 2 uses the overridden port and points at the non-default router.
        self.assertEqual(web_ui_call[1][0], "router-web-ui-coldtest")
        self.assertIn("--port 8800", web_ui_call[1][1])
        self.assertIn(f"--router {PROBE_ROUTER}", web_ui_call[1][1])

        # Steps 3 to 6 all address the same suffixed window and the same router.
        self.assertEqual(window_call[1][0], PROBE_WINDOW)
        self.assertEqual(agent_call[1][0], PROBE_WINDOW)
        self.assertEqual(sidecar_call[1], (PROBE_WINDOW, PROBE_ROUTER))
        self.assertEqual(delivery_call[1], (PROBE_WINDOW, PROBE_ROUTER))

        # Claude gets a real bootstrap prompt, not None. Passing None here is the defect
        # that left a Claude supervisor sitting at an empty session.
        self.assertEqual(agent_call[1][4], supervisor_start.CLAUDE_BOOTSTRAP_PROMPT)

        # The health predicates are closures. Recording only the call is not enough: one
        # wired to a module default instead of the resolved value looks identical until it
        # is actually invoked, and then it probes the live router during a rehearsal.
        probed = []
        with patch.object(supervisor_start, "router_is_healthy",
                          side_effect=lambda url: probed.append(url) or True):
            router_call[1][2]()
        self.assertEqual(probed, [PROBE_ROUTER])

        probed_ports = []
        with patch.object(supervisor_start, "web_ui_is_healthy",
                          side_effect=lambda port: probed_ports.append(port) or True):
            web_ui_call[1][2]()
        self.assertEqual(probed_ports, [8800])

    def test_a_suffix_against_the_live_router_is_refused(self):
        """Namespacing the windows while pointing at the live router is the opposite of
        isolation: it starts a second real supervisor and registers it with the live
        router."""
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._environment(tmpdir, MECHA_SUPERVISOR_START_SUFFIX="coldtest")
            with self.assertRaises(supervisor_start.ConfigurationError) as caught:
                self._run(tmpdir, environment)
        self.assertIn("MECHA_ROUTER_URL", str(caught.exception))

    def test_pi_exits_three_and_skips_the_checks_it_cannot_make(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            extension = pathlib.Path(tmpdir) / "mecha-messaging.js"
            extension.write_text("export default () => {};\n", encoding="utf-8")
            environment = self._environment(tmpdir, MECHA_PI_MAILBOX_EXTENSION=str(extension))
            code, calls = self._run(tmpdir, environment, agent="pi")
        self.assertEqual(code, 3)
        names = [name for name, _a, _k in calls]
        self.assertNotIn("sidecar", names)
        self.assertNotIn("delivery", names)
        # Pi takes its prompt as a launch argument, so nothing is pasted.
        agent_call = next(call for call in calls if call[0] == "agent")
        self.assertIsNone(agent_call[1][4])

    def test_codex_is_told_it_has_no_mailbox_delivery_at_all(self):
        """worker_launch.py attaches the Mailbox extension on the Pi branch only and the
        sidecar on the Claude branch only, so Codex has neither."""
        printed = []
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._environment(tmpdir)
            args = SimpleNamespace(dir=tmpdir, agent="codex", compaction_policy=None)
            with self._patched([]):
                code = supervisor_start.run_startup(args, environment=environment, output=printed.append)
        self.assertEqual(code, 3)
        joined = "\n".join(printed)
        self.assertIn("NO mailbox delivery", joined)
        self.assertNotIn("its own extension", joined)

    def test_an_already_running_agent_is_not_reported_as_bootstrapped(self):
        printed = []
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._environment(tmpdir)
            args = SimpleNamespace(dir=tmpdir, agent=None, compaction_policy=None)
            with patch.object(supervisor_start, "ensure_service_window"), \
                    patch.object(supervisor_start, "ensure_supervisor_window"), \
                    patch.object(supervisor_start, "ensure_agent",
                                 return_value=supervisor_start.AGENT_ALREADY_RUNNING), \
                    patch.object(supervisor_start, "ensure_sidecar"), \
                    patch.object(supervisor_start, "verify_delivery"):
                code = supervisor_start.run_startup(args, environment=environment, output=printed.append)
        self.assertEqual(code, 0)
        joined = "\n".join(printed)
        self.assertIn("did not send the bootstrap prompt", joined)

    def test_a_supervisor_that_was_never_bootstrapped_does_not_exit_zero(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._environment(tmpdir)
            args = SimpleNamespace(dir=tmpdir, agent=None, compaction_policy=None)
            with patch.object(supervisor_start, "ensure_service_window"), \
                    patch.object(supervisor_start, "ensure_supervisor_window"), \
                    patch.object(supervisor_start, "ensure_agent",
                                 return_value=supervisor_start.AGENT_NOT_BOOTSTRAPPED), \
                    patch.object(supervisor_start, "ensure_sidecar"), \
                    patch.object(supervisor_start, "verify_delivery"):
                code = supervisor_start.run_startup(args, environment=environment, output=lambda _m: None)
        self.assertEqual(code, 3)

    def test_the_router_database_directory_is_not_created_when_nothing_is_started(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            database = os.path.join(tmpdir, "unused", "router.sqlite")
            environment = self._environment(tmpdir, MECHA_ROUTER_DATABASE=database)
            self._run(tmpdir, environment)
            self.assertFalse(os.path.exists(os.path.dirname(database)))

    def test_a_missing_working_directory_is_a_configuration_error(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            environment = self._environment(tmpdir)
            args = SimpleNamespace(dir=os.path.join(tmpdir, "absent"), agent=None, compaction_policy=None)
            with self.assertRaises(supervisor_start.ConfigurationError):
                supervisor_start.run_startup(args, environment=environment, output=lambda _m: None)

    def test_a_compaction_policy_outside_pi_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            args = SimpleNamespace(dir=tmpdir, agent="claude", compaction_policy="50,80")
            with self.assertRaises(supervisor_start.ConfigurationError):
                supervisor_start.run_startup(args, environment={}, output=lambda _m: None)

    def test_a_pi_policy_reaches_the_launch_line(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            extension = pathlib.Path(tmpdir) / "mecha-messaging.js"
            extension.write_text("export default () => {};\n", encoding="utf-8")
            environment = self._environment(tmpdir, MECHA_PI_MAILBOX_EXTENSION=str(extension))
            _code, calls = self._run(tmpdir, environment, agent="pi", compaction_policy="50,80")
        agent_call = next(call for call in calls if call[0] == "agent")
        exports = agent_call[2]["pi_exports"]
        self.assertIn("MECHA_PI_COMPACTION_POLICY=50,80", exports)
        self.assertIn("MECHA_PI_MODEL_KEY=openai-codex/gpt-5.5", exports)


class MainExitCodeTest(unittest.TestCase):
    def test_main_returns_one_without_repeating_a_suppressed_duplicate_notice(self):
        responses = iter([
            SimpleNamespace(returncode=1, stdout="", stderr="Router unavailable: refused"),
            SimpleNamespace(returncode=1, stdout="", stderr=""),
        ])

        def fake_run(*args, **kwargs):
            return next(responses)

        def run_startup(_args):
            return supervisor_start.send_startup_message(PROBE_WINDOW, PROBE_ROUTER, run=fake_run)

        stderr = io.StringIO()
        with tempfile.TemporaryDirectory() as tmpdir:
            with patch.object(supervisor_start, "RUN_LOCK_PATH", os.path.join(tmpdir, "run.lock")), \
                    patch.object(supervisor_start, "run_startup", side_effect=run_startup), \
                    contextlib.redirect_stderr(stderr):
                self.assertEqual(supervisor_start.main(["--dir", tmpdir]), 1)
                first_notice = stderr.getvalue()
                self.assertEqual(supervisor_start.main(["--dir", tmpdir]), 1)
        self.assertIn("Router unavailable: refused", first_notice)
        self.assertEqual(stderr.getvalue(), first_notice)

    def test_a_configuration_error_is_exit_two(self):
        stderr = io.StringIO()
        with tempfile.TemporaryDirectory() as tmpdir:
            with patch.object(supervisor_start, "run_startup",
                              side_effect=supervisor_start.ConfigurationError("bad")), \
                    contextlib.redirect_stderr(stderr):
                self.assertEqual(supervisor_start.main(["--dir", tmpdir]), 2)
        self.assertIn("bad", stderr.getvalue())

    def test_a_startup_error_is_exit_one(self):
        stderr = io.StringIO()
        with tempfile.TemporaryDirectory() as tmpdir:
            with patch.object(supervisor_start, "run_startup",
                              side_effect=supervisor_start.StartupError("nope")), \
                    contextlib.redirect_stderr(stderr):
                self.assertEqual(supervisor_start.main(["--dir", tmpdir]), 1)
        self.assertIn("nope", stderr.getvalue())

    def test_an_unexpected_os_error_is_exit_one(self):
        stderr = io.StringIO()
        with tempfile.TemporaryDirectory() as tmpdir:
            with patch.object(supervisor_start, "run_startup", side_effect=OSError("disk gone")), \
                    contextlib.redirect_stderr(stderr):
                self.assertEqual(supervisor_start.main(["--dir", tmpdir]), 1)
        self.assertIn("disk gone", stderr.getvalue())

    def test_the_exit_code_contract_is_in_the_help_text(self):
        self.assertIn("exit codes", supervisor_start.EXIT_CODE_HELP)
        for code in ("0", "1", "2", "3"):
            self.assertIn(f"  {code}  ", supervisor_start.EXIT_CODE_HELP)


class RunLockTest(unittest.TestCase):
    def test_main_holds_the_lock_for_the_whole_run(self):
        """Without this, two runs race: both see no window, both create one, and the pair
        can leave two sidecars for one agent."""
        held = {}

        def run_startup(_args):
            try:
                with supervisor_start.exclusive_run_lock():
                    held["reentered"] = True
            except supervisor_start.ConfigurationError:
                held["reentered"] = False
            return 0

        with tempfile.TemporaryDirectory() as tmpdir:
            with patch.object(supervisor_start, "RUN_LOCK_PATH", os.path.join(tmpdir, "run.lock")), \
                    patch.object(supervisor_start, "run_startup", side_effect=run_startup):
                self.assertEqual(supervisor_start.main(["--dir", tmpdir]), 0)
        self.assertIs(held.get("reentered"), False, "main() did not hold the run lock")

    def test_a_second_concurrent_run_is_refused(self):
        """Two runs racing both see no window and both create one, which leaves a dead
        duplicate and can leave two sidecars for one agent."""
        with tempfile.TemporaryDirectory() as tmpdir:
            lock = os.path.join(tmpdir, "supervisor-start.lock")
            with supervisor_start.exclusive_run_lock(lock):
                with self.assertRaises(supervisor_start.ConfigurationError) as caught:
                    with supervisor_start.exclusive_run_lock(lock):
                        pass
            self.assertIn("already running", str(caught.exception))

    def test_the_lock_is_released_when_the_run_ends(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            lock = os.path.join(tmpdir, "supervisor-start.lock")
            with supervisor_start.exclusive_run_lock(lock):
                pass
            with supervisor_start.exclusive_run_lock(lock):
                pass

    def test_the_lock_is_released_even_when_the_run_raises(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            lock = os.path.join(tmpdir, "supervisor-start.lock")
            with self.assertRaises(RuntimeError):
                with supervisor_start.exclusive_run_lock(lock):
                    raise RuntimeError("boom")
            with supervisor_start.exclusive_run_lock(lock):
                pass


class SymlinkedInvocationTest(unittest.TestCase):
    """`~/.local/bin/mecha-supervisor` is a symlink to this module.

    `os.path.abspath` does not resolve symlinks, so a sibling-script path built from the
    as-invoked location points into ~/.local/bin, where neither claude_sidecar.mjs nor
    mailbox_send.py exists. Loaded from the real directory the defect is invisible, which
    is why this loads the module through a symlink the way an operator does.
    """

    def test_sibling_script_paths_survive_being_reached_through_a_symlink(self):
        tools = os.path.dirname(os.path.abspath(supervisor_start.__file__))
        with tempfile.TemporaryDirectory() as tmpdir:
            linked = os.path.join(tmpdir, "linked-tools")
            os.symlink(tools, linked)
            snippet = (
                "import json, sys\n"
                f"sys.path.insert(0, {linked!r})\n"
                "import supervisor_start as s\n"
                "print(json.dumps({'module': s.__file__, 'sidecar': s.SIDECAR_SCRIPT_PATH,"
                " 'send': s.MAILBOX_SEND_SCRIPT}))\n"
            )
            result = subprocess.run(
                [__import__("sys").executable, "-c", snippet],
                capture_output=True, text=True, timeout=60,
                env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"},
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            loaded = json.loads(result.stdout.strip().splitlines()[-1])

        # The module really was reached through the symlink, so the defect could fire.
        self.assertTrue(loaded["module"].startswith(linked))
        self.assertNotEqual(loaded["module"], os.path.realpath(loaded["module"]))
        # And both sibling paths still name files that exist.
        self.assertTrue(os.path.isfile(loaded["sidecar"]), loaded["sidecar"])
        self.assertTrue(os.path.isfile(loaded["send"]), loaded["send"])

    def test_the_sidecar_module_resolves_its_script_when_imported_through_a_symlink(self):
        """`_sidecar` owns the path node is handed, and it is importable on its own.

        Reached through `supervisor_start` the defect hides, because that module puts the
        real directory on sys.path before importing anything. Any other importer gets the
        as-invoked directory, and then abspath names a script that is not there.
        """
        tools = os.path.dirname(os.path.abspath(supervisor_start.__file__))
        with tempfile.TemporaryDirectory() as tmpdir:
            linked = os.path.join(tmpdir, "linked-tools")
            os.symlink(tools, linked)
            snippet = (
                "import json, sys\n"
                f"sys.path.insert(0, {linked!r})\n"
                "import _sidecar\n"
                "print(json.dumps({'module': _sidecar.__file__,"
                " 'sidecar': _sidecar.SIDECAR_SCRIPT_PATH}))\n"
            )
            result = subprocess.run(
                [__import__("sys").executable, "-c", snippet],
                capture_output=True, text=True, timeout=60,
                env={**os.environ, "PYTHONDONTWRITEBYTECODE": "1"},
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            loaded = json.loads(result.stdout.strip().splitlines()[-1])

        self.assertTrue(loaded["module"].startswith(linked))
        self.assertNotEqual(loaded["module"], os.path.realpath(loaded["module"]))
        self.assertTrue(os.path.isfile(loaded["sidecar"]), loaded["sidecar"])


class PiExtensionTest(unittest.TestCase):
    def test_the_stable_extension_is_used_when_there_is_no_override(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            extension = pathlib.Path(tmpdir) / "mecha-messaging.js"
            extension.write_text("export default () => {};\n", encoding="utf-8")
            with patch.object(supervisor_start, "PI_MAILBOX_EXTENSION_PATH", str(extension)):
                self.assertEqual(supervisor_start.resolve_pi_mailbox_extension({}), str(extension))

    def test_an_override_wins_over_the_stable_extension(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            override = pathlib.Path(tmpdir) / "override.js"
            override.write_text("export default () => {};\n", encoding="utf-8")
            with patch.object(supervisor_start, "PI_MAILBOX_EXTENSION_PATH", "/missing/stable.js"):
                resolved = supervisor_start.resolve_pi_mailbox_extension(
                    {"MECHA_PI_MAILBOX_EXTENSION": str(override)}
                )
        self.assertEqual(resolved, str(override))

    def test_a_symlinked_extension_is_refused(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            real = pathlib.Path(tmpdir) / "real.js"
            real.write_text("export default () => {};\n", encoding="utf-8")
            link = pathlib.Path(tmpdir) / "link.js"
            link.symlink_to(real)
            with self.assertRaises(supervisor_start.ConfigurationError):
                supervisor_start.resolve_pi_mailbox_extension({"MECHA_PI_MAILBOX_EXTENSION": str(link)})

    def test_a_missing_extension_is_refused(self):
        with self.assertRaises(supervisor_start.ConfigurationError):
            supervisor_start.resolve_pi_mailbox_extension({"MECHA_PI_MAILBOX_EXTENSION": "/missing/nope.js"})

    def test_an_empty_override_is_refused(self):
        with self.assertRaises(supervisor_start.ConfigurationError):
            supervisor_start.resolve_pi_mailbox_extension({"MECHA_PI_MAILBOX_EXTENSION": ""})

    def test_pi_requires_a_resolved_extension_rather_than_reaching_for_os_environ(self):
        with self.assertRaises(supervisor_start.ConfigurationError):
            supervisor_start.agent_command("pi", "prompt", None)


class CompactionPolicyTest(unittest.TestCase):
    def test_valid_policies_pass_through(self):
        self.assertEqual(supervisor_start.validate_compaction_policy("50,80"), "50,80")
        self.assertIsNone(supervisor_start.validate_compaction_policy(None))
        tiered = '{"tiers":[{"maxContextTokens":1000000,"levels":[50]}]}'
        self.assertEqual(supervisor_start.validate_compaction_policy(tiered), tiered)
        levels = '{"levels":[50],"postCompactionNotification":"hi","models":{"a":1}}'
        self.assertEqual(supervisor_start.validate_compaction_policy(levels), levels)

    def test_invalid_policies_are_rejected(self):
        for policy in (
            "", "0,50", "50,100", '{"tiers":[]}', '{"levels":[]}', '"a string"',
            '{"postCompactionNotification":1,"levels":[50]}',
            '{"models":"nope","levels":[50]}',
            '{"tiers":["not an object"]}',
            '{"tiers":[{"maxContextTokens":0,"levels":[50]}]}',
        ):
            with self.subTest(policy=policy):
                with self.assertRaises(ValueError):
                    supervisor_start.validate_compaction_policy(policy)


if __name__ == "__main__":
    unittest.main()
