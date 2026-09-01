#!/usr/bin/env python3
"""Regression tests: worker shutdown must not leave the mailbox sidecar running.

A sidecar that outlives its window stays registered with the router as connected,
so mail addressed to the dead worker is accepted and pasted into a window that no
longer exists — lost with no error to the sender.

Tests patch the module seams the production code actually reads (`_process_table`
and `os.kill`) rather than passing injected doubles, so `cleanup_sidecar` ->
`terminate_sidecar` -> `find_sidecar_pids` runs for real. RealProcessTableTest
skips the fakes entirely and drives a live process.
"""

import contextlib
import io
import os
import pathlib
import signal
import subprocess
import sys
import tempfile
import time
import unittest
import uuid
from unittest.mock import patch

import _sidecar
import _tmux
import worker_kill
import worker_shutdown

SIDECAR_ARGS = (
    "node /Users/x/.claude/skills/supervisor/tools/claude_sidecar.mjs "
    "--agent-id {window} --window {window} --router-url http://127.0.0.1:8765"
)


class FakeProcesses:
    """A process table whose entries disappear when signalled."""

    def __init__(self, entries):
        self.entries = list(entries)
        self.signals = []

    def table(self):
        return list(self.entries)

    def kill(self, pid, sig):
        self.signals.append((pid, sig))
        if sig in (signal.SIGTERM, signal.SIGKILL):
            self.entries = [entry for entry in self.entries if entry[0] != pid]


class UnkillableProcesses(FakeProcesses):
    """A sidecar that ignores every signal, including SIGKILL."""

    def kill(self, pid, sig):
        self.signals.append((pid, sig))


class SidecarTestCase(unittest.TestCase):
    """Patches the module seams and installs a fake process table."""

    processes_class = FakeProcesses
    entries = ()

    def setUp(self):
        self.processes = self.processes_class(self.entries)
        self.patch_processes(self.processes)

    def patch_processes(self, processes):
        patches = [
            patch.object(_sidecar, "_process_table", processes.table),
            patch.object(os, "kill", processes.kill),
            patch.object(_sidecar.time, "sleep", lambda _seconds: None),
        ]
        for active in patches:
            active.start()
            self.addCleanup(active.stop)

    def patch_tmux(self):
        """Stub tmux itself, so the real kill_window (and its sidecar sweep) runs.

        Patching kill_window instead would mock away the chokepoint under test.
        """
        self.tmux_commands = []
        self.rest_removed = []

        def run(args, check=True, capture=True):
            self.tmux_commands.append(list(args))
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        active = patch.object(_tmux, "run", run)
        active.start()
        self.addCleanup(active.stop)
        rest = patch.object(
            worker_shutdown.TmuxRestClient,
            "remove",
            lambda _client, window: self.rest_removed.append(window),
        )
        rest.start()
        self.addCleanup(rest.stop)
        rest_kill = patch.object(
            worker_kill.TmuxRestClient,
            "remove",
            lambda _client, window: self.rest_removed.append(window),
        )
        rest_kill.start()
        self.addCleanup(rest_kill.stop)

    def assert_killed_window(self, window):
        self.assertIn(window, self.rest_removed)

    def run_tool(self, module, argv):
        out = io.StringIO()
        err = io.StringIO()
        with patch.object(sys, "argv", argv):
            with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
                module.main()
        return out.getvalue() + err.getvalue()


class FindSidecarTest(SidecarTestCase):
    entries = (
        (100, SIDECAR_ARGS.format(window="worker-1")),
        (101, SIDECAR_ARGS.format(window="worker-10")),
        (102, SIDECAR_ARGS.format(window="worker-1-extra")),
    )

    def test_matches_the_sidecar_for_the_requested_window(self):
        self.assertEqual(_sidecar.find_sidecar_pids("worker-1"), [100])

    def test_window_name_matching_is_exact_not_prefix(self):
        self.assertEqual(_sidecar.find_sidecar_pids("worker-10"), [101])
        self.assertEqual(_sidecar.find_sidecar_pids("worker-1-extra"), [102])

    def test_unknown_window_matches_nothing(self):
        self.assertEqual(_sidecar.find_sidecar_pids("worker-9"), [])


class MatcherPrecisionTest(SidecarTestCase):
    """Processes that merely mention the sidecar must never be signalled.

    The launcher pastes the whole sidecar command line through tmux send-keys, so a
    shutdown racing a relaunch of the same window would otherwise kill the paste.
    """

    entries = (
        # tmux send-keys carrying the launch command for this very window.
        (200, "tmux send-keys -l -t mecha:worker-1 { node /tools/claude_sidecar.mjs "
              "--agent-id worker-1 --window worker-1 --router-url http://127.0.0.1:8765 & }"),
        # A search for the sidecar command line.
        (201, "rg claude_sidecar.mjs --agent-id worker-1"),
        # A syntax check, which has no --agent-id at all.
        (202, "node --check /tools/claude_sidecar.mjs"),
        # An agent invoked with a prompt that quotes the command line.
        (203, "pi --print run node /tools/claude_sidecar.mjs --agent-id worker-1 for me"),
        # The router and web UI, which must never match.
        (204, "tools/run_router.sh"),
        (205, "tools/run_webui.sh"),
        # The script appears in argv but is not what node runs — it is another
        # program's option value. Matching this would SIGKILL the watcher.
        (207, "node watcher.mjs --watch /tools/claude_sidecar.mjs --agent-id worker-1"),
        # Same shape via a node option that takes the next token as its value.
        (208, "node --require /tools/claude_sidecar.mjs app.mjs --agent-id worker-1"),
        # Eval mode: there is no entrypoint script at all.
        (209, "node -e /tools/claude_sidecar.mjs --agent-id worker-1"),
        # The real one.
        (206, SIDECAR_ARGS.format(window="worker-1")),
    )

    def test_only_the_real_sidecar_process_matches(self):
        self.assertEqual(_sidecar.find_sidecar_pids("worker-1"), [206])

    def test_only_the_real_sidecar_is_signalled(self):
        _sidecar.terminate_sidecar("worker-1")
        self.assertEqual(self.processes.signals, [(206, signal.SIGTERM)])

    def test_node_options_before_the_script_do_not_prevent_a_match(self):
        # Failing to match a real sidecar silently restores the leak, so tolerate the
        # node flags a future launcher might add.
        for args in (
            "node --enable-source-maps /tools/claude_sidecar.mjs --agent-id worker-7",
            "node --max-old-space-size=256 /tools/claude_sidecar.mjs --agent-id worker-7",
            "node --require /tools/hook.js /tools/claude_sidecar.mjs --agent-id worker-7",
        ):
            with self.subTest(args=args):
                self.assertTrue(_sidecar._is_sidecar_argv(args, "worker-7"))


class TerminateSidecarTest(SidecarTestCase):
    entries = (
        (100, SIDECAR_ARGS.format(window="worker-1")),
        (101, SIDECAR_ARGS.format(window="worker-2")),
    )

    def test_terminates_with_sigterm_and_verifies_the_process_is_gone(self):
        result = _sidecar.terminate_sidecar("worker-1")
        self.assertEqual(self.processes.signals, [(100, signal.SIGTERM)])
        self.assertEqual(result, {"found": [100], "terminated": [100], "survived": []})
        self.assertEqual(_sidecar.find_sidecar_pids("worker-1"), [])

    def test_leaves_other_windows_sidecars_alone(self):
        _sidecar.terminate_sidecar("worker-1")
        self.assertEqual(self.processes.signals, [(100, signal.SIGTERM)])
        self.assertEqual(_sidecar.find_sidecar_pids("worker-2"), [101])

    def test_escalates_to_sigkill_when_sigterm_is_ignored(self):
        class StubbornProcesses(FakeProcesses):
            def kill(self, pid, sig):
                self.signals.append((pid, sig))
                if sig == signal.SIGKILL:
                    self.entries = [entry for entry in self.entries if entry[0] != pid]

        processes = StubbornProcesses(self.entries)
        self.patch_processes(processes)
        result = _sidecar.terminate_sidecar("worker-1")
        self.assertEqual(processes.signals, [(100, signal.SIGTERM), (100, signal.SIGKILL)])
        self.assertEqual(result["terminated"], [100])
        self.assertEqual(result["survived"], [])

    def test_reports_a_survivor_instead_of_claiming_success(self):
        processes = UnkillableProcesses(self.entries)
        self.patch_processes(processes)
        result = _sidecar.terminate_sidecar("worker-1")
        self.assertEqual(result["survived"], [100])
        self.assertEqual(result["terminated"], [])


class ProcessTableUnavailableTest(unittest.TestCase):
    """An unreadable process table must not be reported as "no sidecar"."""

    def test_ps_failure_raises_rather_than_returning_an_empty_table(self):
        def failing_run(*_args, **_kwargs):
            raise OSError("fork failed")

        with patch.object(_sidecar.subprocess, "run", failing_run):
            with self.assertRaises(_sidecar.ProcessTableUnavailable):
                _sidecar._process_table()

    def test_ps_nonzero_exit_raises(self):
        class Result:
            returncode = 1
            stdout = ""

        with patch.object(_sidecar.subprocess, "run", lambda *_a, **_k: Result()):
            with self.assertRaises(_sidecar.ProcessTableUnavailable):
                _sidecar._process_table()

    def test_cleanup_reports_failure_instead_of_a_clean_all_clear(self):
        def unavailable(_window):
            raise _sidecar.ProcessTableUnavailable("ps exited with status 1")

        err = io.StringIO()
        with patch.object(_sidecar, "terminate_sidecar", unavailable):
            with contextlib.redirect_stderr(err):
                clean = _sidecar.cleanup_sidecar("worker-1")
        self.assertFalse(clean)
        self.assertIn("could not confirm", err.getvalue())
        self.assertNotIn("No mailbox sidecar process found", err.getvalue())

    def test_shutdown_exits_non_zero_when_the_process_table_is_unreadable(self):
        def unavailable(_window):
            raise _sidecar.ProcessTableUnavailable("ps exited with status 1")

        def tmux(args, check=True, capture=True):
            return subprocess.CompletedProcess(args, 0, stdout="", stderr="")

        with patch.object(_sidecar, "terminate_sidecar", unavailable), \
             patch.object(_tmux, "run", tmux), \
             patch.object(worker_kill, "window_exists", return_value=True), \
             patch.object(sys, "argv", ["worker_kill.py", "--window", "worker-1"]):
            with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as raised:
                    worker_kill.main()
        self.assertEqual(raised.exception.code, 1)


class CleanupReportingTest(SidecarTestCase):
    entries = ((100, SIDECAR_ARGS.format(window="worker-1")),)

    def test_survivor_is_reported_on_stderr_and_returns_failure(self):
        processes = UnkillableProcesses(self.entries)
        self.patch_processes(processes)
        err = io.StringIO()
        with contextlib.redirect_stderr(err):
            clean = _sidecar.cleanup_sidecar("worker-1")
        self.assertFalse(clean)
        self.assertIn("survived", err.getvalue())
        self.assertIn("lost", err.getvalue())


class WorkerShutdownSidecarTest(SidecarTestCase):
    """The end-to-end path: shutting a worker down must kill its sidecar."""

    entries = (
        (100, SIDECAR_ARGS.format(window="worker-1")),
        (101, SIDECAR_ARGS.format(window="worker-2")),
    )

    def test_worker_shutdown_kills_the_sidecar_after_killing_the_window(self):
        self.patch_tmux()
        with patch.object(worker_shutdown, "window_exists", return_value=True), \
             patch.object(worker_shutdown, "send_text_and_enter") as send, \
             patch.object(worker_shutdown.time, "sleep", lambda _seconds: None):
            output = self.run_tool(
                worker_shutdown,
                ["worker_shutdown.py", "--window", "worker-1", "--verified"],
            )
        send.assert_called_once()
        self.assert_killed_window("worker-1")
        self.assertEqual(self.processes.signals, [(100, signal.SIGTERM)])
        self.assertEqual(_sidecar.find_sidecar_pids("worker-1"), [])
        self.assertIn("Terminated mailbox sidecar for 'worker-1'", output)
        # Unrelated workers keep their sidecars.
        self.assertEqual(_sidecar.find_sidecar_pids("worker-2"), [101])

    def test_worker_shutdown_sweeps_a_sidecar_orphaned_by_a_missing_window(self):
        self.patch_tmux()
        with patch.object(worker_shutdown, "window_exists", return_value=False):
            output = self.run_tool(
                worker_shutdown,
                ["worker_shutdown.py", "--window", "worker-1", "--verified"],
            )
        self.assertEqual(self.tmux_commands, [], "no window to kill")
        self.assertEqual(self.processes.signals, [(100, signal.SIGTERM)])
        self.assertIn("Terminated mailbox sidecar for 'worker-1'", output)

    def test_worker_kill_kills_the_sidecar_too(self):
        self.patch_tmux()
        with patch.object(worker_kill, "window_exists", return_value=True):
            output = self.run_tool(worker_kill, ["worker_kill.py", "--window", "worker-1"])
        self.assert_killed_window("worker-1")
        self.assertEqual(self.processes.signals, [(100, signal.SIGTERM)])
        self.assertEqual(_sidecar.find_sidecar_pids("worker-1"), [])
        self.assertIn("Terminated mailbox sidecar for 'worker-1'", output)

    def test_worker_kill_reports_no_sidecar_without_failing(self):
        self.patch_tmux()
        with patch.object(worker_kill, "window_exists", return_value=True):
            output = self.run_tool(worker_kill, ["worker_kill.py", "--window", "worker-9"])
        self.assertEqual(self.processes.signals, [])
        self.assertIn("No mailbox sidecar process found for 'worker-9'", output)


class SurvivingSidecarExitCodeTest(SidecarTestCase):
    """A sidecar that will not die must make every shutdown path exit non-zero."""

    processes_class = UnkillableProcesses
    entries = ((100, SIDECAR_ARGS.format(window="worker-1")),)

    def assert_exits_one(self, module, argv, **patches):
        self.patch_tmux()
        with contextlib.ExitStack() as stack:
            for target, value in patches.items():
                stack.enter_context(patch.object(module, target, value))
            with self.assertRaises(SystemExit) as raised:
                self.run_tool(module, argv)
        self.assertEqual(raised.exception.code, 1)

    def test_worker_kill_exits_one(self):
        self.assert_exits_one(
            worker_kill,
            ["worker_kill.py", "--window", "worker-1"],
            window_exists=lambda _window: True,
        )

    def test_worker_shutdown_exits_one_on_the_normal_path(self):
        self.assert_exits_one(
            worker_shutdown,
            ["worker_shutdown.py", "--window", "worker-1", "--verified"],
            window_exists=lambda _window: True,
            send_text_and_enter=lambda _window, _text: None,
        )

    def test_worker_shutdown_exits_one_on_the_missing_window_path(self):
        self.assert_exits_one(
            worker_shutdown,
            ["worker_shutdown.py", "--window", "worker-1", "--verified"],
            window_exists=lambda _window: False,
        )


class NoUnsweptWindowKillTest(unittest.TestCase):
    """Guard the defect class, not the instances.

    "A path ends a worker's window without ending its mailbox sidecar" is a class of
    bug: fixing the instances someone happened to notice leaves the next one live. Every
    kill goes through `_tmux.kill_window`, which sweeps. This test fails if a tool
    reaches for tmux's kill-window directly again.
    """

    def setUp(self):
        self.tools = pathlib.Path(__file__).parent

    def test_kill_window_sweeps_the_sidecar(self):
        source = (self.tools / "_tmux.py").read_text()
        self.assertIn("cleanup_sidecar(window)", source)

    def test_no_tool_kills_a_tmux_window_directly(self):
        offenders = []
        for path in sorted(self.tools.glob("*.py")):
            if path.name.startswith("test_") or path.name == "_tmux.py":
                continue
            for number, line in enumerate(path.read_text().splitlines(), start=1):
                if "kill-window" in line and not line.lstrip().startswith("#"):
                    offenders.append(f"{path.name}:{number}")
        self.assertEqual(
            offenders,
            [],
            "these lines kill a tmux window without _tmux.kill_window, so they leak the "
            "window's mailbox sidecar and hand its mail to a stranger: "
            + ", ".join(offenders),
        )


class RealProcessTableTest(unittest.TestCase):
    """No fakes: discovery must work against real `ps` output and a real process.

    `_process_table` is the one function that touches reality. If its parsing breaks,
    every other test still passes while shutdown cheerfully reports "no sidecar found"
    and the leak comes back.
    """

    def setUp(self):
        if subprocess.run(["which", "node"], capture_output=True).returncode != 0:
            self.skipTest("node is not installed")
        self.window = f"probe-{uuid.uuid4().hex[:12]}"
        self.tmpdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmpdir.cleanup)
        # A stand-in with the real script name, launched with the real argv shape.
        script = pathlib.Path(self.tmpdir.name) / _sidecar.SIDECAR_SCRIPT
        script.write_text("setInterval(() => {}, 1000);\n")
        self.process = subprocess.Popen(
            ["node", str(script), "--agent-id", self.window,
             "--window", self.window, "--router-url", "http://127.0.0.1:8765"],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        self.addCleanup(self.stop_process)
        deadline = time.monotonic() + 10
        while not _sidecar.find_sidecar_pids(self.window):
            if time.monotonic() > deadline:
                self.fail(f"the probe process never appeared in ps as '{self.window}'")
            time.sleep(0.1)

    def stop_process(self):
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait(timeout=10)

    def test_finds_a_live_sidecar_shaped_process_by_window(self):
        self.assertEqual(_sidecar.find_sidecar_pids(self.window), [self.process.pid])
        self.assertEqual(_sidecar.find_sidecar_pids(f"{self.window}-x"), [])

    def test_terminate_kills_the_real_process_and_confirms_it(self):
        result = _sidecar.terminate_sidecar(self.window)
        self.assertEqual(result["found"], [self.process.pid])
        self.assertEqual(result["terminated"], [self.process.pid])
        self.assertEqual(result["survived"], [])
        self.assertIsNotNone(self.process.poll(), "the process should have exited")
        self.assertEqual(_sidecar.find_sidecar_pids(self.window), [])


if __name__ == "__main__":
    unittest.main()
