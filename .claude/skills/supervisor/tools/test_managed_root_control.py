import contextlib
import hashlib
import importlib.util
import io
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

MODULE_PATH = Path(__file__).with_name("managed_root_control.py")
SPEC = importlib.util.spec_from_file_location("managed_root_control_root_tests", MODULE_PATH)
managed = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = managed
SPEC.loader.exec_module(managed)

MANIFEST_PATH = MODULE_PATH.parents[4] / "router" / "contract" / "managed-tools-v1.json"


class ManagedRootAdapterTests(unittest.TestCase):
    def request(self, request_id="request-1", operation="ops.worker", arguments=None, fence=None):
        return {
            "request_id": request_id,
            "operation_id": operation,
            "arguments": {"action": "list"} if arguments is None else arguments,
            "fence": {"agent_id": "fenced-agent"} if fence is None else fence,
        }

    def run_request(self, home, request, *, mode="execute", subprocess_run=None):
        stdout = io.StringIO()
        stderr = io.StringIO()
        patches = [
            mock.patch.object(managed.sys, "stdin", io.StringIO(json.dumps(request))),
            mock.patch.object(managed.sys, "stdout", stdout),
            mock.patch.object(managed.sys, "stderr", stderr),
            mock.patch.dict(managed.os.environ, {"MECHA_AGENT_ID": "ambient-agent"}, clear=False),
        ]
        if subprocess_run is not None:
            patches.append(mock.patch.object(managed.subprocess, "run", side_effect=subprocess_run))
        with contextlib.ExitStack() as stack:
            for patch in patches:
                stack.enter_context(patch)
            code = managed.run(mode, Path(home))
        return code, stdout.getvalue(), stderr.getvalue()

    def successful_child(self, arguments, **_kwargs):
        return subprocess.CompletedProcess(arguments, 0, stdout="child result", stderr="")

    def test_invalid_request_shapes_make_no_external_call_or_ledger(self):
        valid = self.request()
        invalid_requests = [
            {**valid, "request_id": None},
            {**valid, "request_id": ""},
            {key: value for key, value in valid.items() if key != "operation_id"},
            {**valid, "arguments": []},
            {**valid, "fence": []},
            {**valid, "fence": {"agent_id": 7}},
        ]
        for request in invalid_requests:
            with self.subTest(request=request):
                with tempfile.TemporaryDirectory() as home:
                    calls = []

                    def child(arguments, **kwargs):
                        calls.append((arguments, kwargs))
                        return self.successful_child(arguments, **kwargs)

                    code, output, _error = self.run_request(home, request, subprocess_run=child)
                    self.assertEqual(code, 1)
                    self.assertEqual(output, "")
                    self.assertEqual(calls, [])
                    self.assertFalse((Path(home) / "root-control").exists())

    def test_reconcile_unknown_request_is_truthful_and_does_not_call_child(self):
        with tempfile.TemporaryDirectory() as home:
            calls = []

            def child(arguments, **kwargs):
                calls.append((arguments, kwargs))
                return self.successful_child(arguments, **kwargs)

            code, output, _error = self.run_request(home, self.request(), mode="reconcile", subprocess_run=child)
            response = json.loads(output)
            self.assertEqual(code, 0)
            self.assertEqual(response["status"], "error")
            self.assertEqual(response["error_code"], "ROOT_OPERATION_UNKNOWN")
            self.assertNotEqual(response["status"], "ok")
            self.assertEqual(calls, [])
            state_path, _lock_path = managed.root_state(Path(home))
            self.assertEqual(managed.load_state(state_path)["requests"], {})

    def test_request_id_reuse_with_changed_fingerprint_is_rejected(self):
        with tempfile.TemporaryDirectory() as home:
            calls = []

            def child(arguments, **kwargs):
                calls.append(arguments)
                return self.successful_child(arguments, **kwargs)

            first = self.request(arguments={"action": "list"})
            code, first_output, _error = self.run_request(home, first, subprocess_run=child)
            self.assertEqual(code, 0)
            state_path, _lock_path = managed.root_state(Path(home))
            before = state_path.read_text(encoding="utf-8")

            changed = self.request(arguments={"action": "list", "unexpected": "changed"})
            code, output, error = self.run_request(home, changed, subprocess_run=child)
            self.assertEqual(code, 1)
            self.assertEqual(output, "")
            self.assertIn("another root operation", error)
            self.assertEqual(len(calls), 1)
            self.assertEqual(state_path.read_text(encoding="utf-8"), before)
            self.assertEqual(first_output, json.dumps(json.loads(first_output), separators=(",", ":")) + "\n")

    def test_successful_and_failed_external_operations_replay_exact_result(self):
        with tempfile.TemporaryDirectory() as home:
            calls = []
            success_request = self.request(request_id="success")

            def successful_child(arguments, **kwargs):
                calls.append(arguments)
                return self.successful_child(arguments, **kwargs)

            code, first_success, _error = self.run_request(home, success_request, subprocess_run=successful_child)
            self.assertEqual(code, 0)
            code, replayed_success, _error = self.run_request(home, success_request, subprocess_run=successful_child)
            self.assertEqual(code, 0)
            self.assertEqual(first_success, replayed_success)
            self.assertEqual(len(calls), 1)

            failure_request = self.request(request_id="failure")

            def failed_child(arguments, **kwargs):
                calls.append(arguments)
                return subprocess.CompletedProcess(arguments, 23, stdout="ok", stderr="child failed")

            code, first_failure, _error = self.run_request(home, failure_request, subprocess_run=failed_child)
            self.assertEqual(code, 0)
            failure = json.loads(first_failure)
            self.assertEqual(failure["status"], "error")
            self.assertNotEqual(failure["status"], "ok")
            code, replayed_failure, _error = self.run_request(home, failure_request, subprocess_run=failed_child)
            self.assertEqual(code, 0)
            self.assertEqual(first_failure, replayed_failure)
            self.assertEqual(len(calls), 2)

    def test_ledger_prunes_expired_and_oldest_records_but_keeps_newest_replayable(self):
        with tempfile.TemporaryDirectory() as home, mock.patch.object(managed.time, "time", return_value=2_000_000_000.0):
            state_path, _lock_path = managed.root_state(Path(home))
            requests = {
                "expired": {
                    "fingerprint": "expired",
                    "recorded_at": 2_000_000_000.0 - managed.RETENTION_SECONDS - 1,
                    "result": {"status": "ok", "content": "expired", "details": {}},
                }
            }
            newest_request = self.request(request_id="recent-201")
            newest_fingerprint = hashlib.sha256(
                managed.canonical({
                    "operation_id": newest_request["operation_id"],
                    "arguments": newest_request["arguments"],
                    "fence": newest_request["fence"],
                }).encode("utf-8")
            ).hexdigest()
            for index in range(202):
                request_id = f"recent-{index:03d}"
                requests[request_id] = {
                    "fingerprint": newest_fingerprint if request_id == "recent-201" else request_id,
                    "recorded_at": 2_000_000_000.0 - 202 + index,
                    "result": {"status": "ok", "content": request_id, "details": {}},
                }
            managed.save_state(state_path, {"version": 1, "requests": requests, "settings": {}})
            calls = []

            def child(arguments, **kwargs):
                calls.append(arguments)
                return self.successful_child(arguments, **kwargs)

            new_request = self.request(request_id="new")
            code, _output, _error = self.run_request(home, new_request, subprocess_run=child)
            self.assertEqual(code, 0)
            state = managed.load_state(state_path)
            self.assertEqual(len(state["requests"]), managed.MAX_RECORDS)
            self.assertNotIn("expired", state["requests"])
            self.assertNotIn("recent-000", state["requests"])
            self.assertNotIn("recent-001", state["requests"])
            self.assertNotIn("recent-002", state["requests"])
            self.assertIn("recent-201", state["requests"])
            self.assertIn("new", state["requests"])

            def should_not_run(*_args, **_kwargs):
                self.fail("retained ledger entry was not replayed")

            code, replay, _error = self.run_request(home, newest_request, subprocess_run=should_not_run)
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(replay)["content"], "recent-201")
            self.assertEqual(len(calls), 1)

    def test_nonzero_child_with_misleading_stdout_cannot_be_success(self):
        with tempfile.TemporaryDirectory() as home:
            calls = []

            def child(arguments, **kwargs):
                calls.append(arguments)
                return subprocess.CompletedProcess(arguments, 9, stdout="ok", stderr="failure")

            code, output, _error = self.run_request(home, self.request(), subprocess_run=child)
            response = json.loads(output)
            self.assertEqual(code, 0)
            self.assertEqual(response["status"], "error")
            self.assertNotEqual(response["status"], "ok")
            self.assertEqual(len(calls), 1)

    def test_mail_sender_uses_fence_identity_not_environment_identity(self):
        with tempfile.TemporaryDirectory() as home:
            calls = []

            def child(arguments, **kwargs):
                calls.append((arguments, kwargs))
                return self.successful_child(arguments, **kwargs)

            request = self.request(
                request_id="mail-request",
                operation="mail.send",
                arguments={"recipient": "worker-a", "body": "hello", "subject": "subject"},
                fence={"agent_id": "fenced-sender"},
            )
            code, output, _error = self.run_request(home, request, subprocess_run=child)
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(output)["status"], "ok")
            self.assertEqual(
                calls[0][0][1:],
                [
                    str(MODULE_PATH.with_name("mailbox_send.py")),
                    "--to", "worker-a", "--body", "hello", "--from", "fenced-sender",
                    "--managed-control-request-id", "mail-request", "--subject", "subject",
                ],
            )

    def test_unknown_operation_action_and_fields_are_rejected_without_side_effect(self):
        cases = [
            self.request(request_id="unknown-operation", operation="ops.unknown", arguments={"action": "list"}),
            self.request(request_id="unknown-action", operation="context.control", arguments={"action": "unknown"}),
            self.request(request_id="unsupported-field", operation="context.control", arguments={"action": "status", "unexpected": True}),
            self.request(request_id="child-only-operation", operation="mail.parent", arguments={"message": "no"}),
        ]
        for request in cases:
            with self.subTest(request=request), tempfile.TemporaryDirectory() as home:
                calls = []

                def child(arguments, **kwargs):
                    calls.append((arguments, kwargs))
                    return self.successful_child(arguments, **kwargs)

                code, output, _error = self.run_request(home, request, subprocess_run=child)
                response = json.loads(output)
                self.assertEqual(code, 0)
                self.assertEqual(response["status"], "error")
                self.assertEqual(response["error_code"], "ROOT_OPERATION_FAILED")
                self.assertEqual(calls, [])
                state_path, _lock_path = managed.root_state(Path(home))
                state = managed.load_state(state_path)
                self.assertEqual(state["settings"], {})

                code, replay, _error = self.run_request(home, request, subprocess_run=child)
                self.assertEqual(code, 0)
                self.assertEqual(replay, output)
                self.assertEqual(calls, [])

    def test_process_local_allowlist_covers_manifest_actions_and_preserves_shapes(self):
        manifest = json.loads(MANIFEST_PATH.read_text(encoding="utf-8"))
        manifest_tools = {tool["operation_id"]: tool for tool in manifest["tools"]}
        self.assertEqual(set(manifest_tools), set(managed.ROOT_OPERATION_CONTRACT))

        for operation, contract in managed.ROOT_OPERATION_CONTRACT.items():
            schema = manifest_tools[operation]["parameters"]
            schema_fields = set(schema["properties"])
            expected_fields = set(contract["fields"])
            if operation == "goal.control":
                expected_fields.remove("settings")
            self.assertEqual(expected_fields, schema_fields, operation)
            schema_action = schema["properties"].get("action", {})
            if "action" in schema_fields:
                self.assertIsNotNone(contract["actions"], operation)
            else:
                self.assertIsNone(contract["actions"], operation)
            if "enum" in schema_action:
                self.assertEqual(set(contract["actions"] or ()), set(schema_action["enum"]), operation)

        goal_settings = {
            "goal_lock_enabled": True,
            "snooze_enabled": True,
            "snooze_duration_ms": 60_000,
            "snooze_turn_limit": None,
            "snooze_budget_cap_ms": None,
            "snooze_refill_yield_ms": 30_000,
            "snooze_refill_elapsed_ms": 60_000,
        }
        local_cases = [
            ("goal.control", {"action": "set", "goal": "ship", "settings": goal_settings}),
            ("goal.control", {"action": "status"}),
            ("goal.control", {"action": "off"}),
            ("goal.snooze", {}),
            ("context.control", {"action": "status"}),
            ("context.control", {"action": "set_policy"}),
            ("context.control", {"action": "compact"}),
            ("recall.control", {"action": "enable", "stores": ["default"]}),
            ("recall.control", {"action": "disable"}),
            ("recall.control", {"action": "status"}),
            ("policy.inline_allowance", {"toolName": "bash", "usageCount": 1}),
            ("notifications.settled", {"action": "enable"}),
            ("notifications.settled", {"action": "disable"}),
            ("notifications.settled", {"action": "status"}),
        ]
        for index, (operation, arguments) in enumerate(local_cases):
            with self.subTest(operation=operation, arguments=arguments):
                settings = {}
                response = managed.execute_supervisor(operation, arguments, "fenced-agent", settings, f"local-{index}", {"agent_id": "fenced-agent"})
                self.assertEqual(response["status"], "ok")
                if operation == "goal.control":
                    self.assertIn("goal_state", response["details"])
                else:
                    self.assertEqual(response["details"], {"operation": operation, "action": arguments.get("action")})
                    self.assertEqual(settings["fenced-agent"][operation], arguments)


if __name__ == "__main__":
    unittest.main()
