import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

TOOLS_DIR = Path(__file__).resolve().parent
sys.path.insert(0, str(TOOLS_DIR))

from _state import claim_task_notice, create_task, save_state  # noqa: E402


class StateNotificationDedupeTest(unittest.TestCase):
    def test_same_notice_is_claimed_once_for_the_same_worker_state(self):
        state = {"next_task_id": 1, "tasks": []}
        task = create_task(
            state,
            "worker-1",
            "worker",
            "project",
            "work",
            window="worker-1",
        )

        self.assertTrue(claim_task_notice(task, "delivery_failure"))
        self.assertFalse(claim_task_notice(task, "delivery_failure"))
        self.assertTrue(claim_task_notice(task, "blocked_worker"))

    def test_notice_can_be_claimed_again_after_block_reason_changes(self):
        task = {
            "alias": "worker-1",
            "state": "active",
            "blocked": "2026-01-01 00:00",
            "blocked_reason": "waiting for review",
        }

        self.assertTrue(claim_task_notice(task, "blocked_worker"))
        task["blocked_reason"] = "router unavailable"
        self.assertTrue(claim_task_notice(task, "blocked_worker"))
        self.assertFalse(claim_task_notice(task, "blocked_worker"))

    def test_notice_can_be_claimed_again_after_state_revision_changes(self):
        task = {"alias": "worker-1", "state": "active"}

        self.assertTrue(claim_task_notice(task, "delivery_failure"))
        task["state_revision"] = 1
        task["state"] = "completed"
        self.assertTrue(claim_task_notice(task, "delivery_failure"))
        task["state_revision"] = 2
        task["state"] = "active"
        self.assertTrue(claim_task_notice(task, "delivery_failure"))

    def test_state_block_same_reason_is_idempotent_but_changed_reason_updates_state(self):
        with tempfile.TemporaryDirectory() as tmp:
            state_path = Path(tmp) / "state.json"
            state = {"next_task_id": 2, "tasks": [{
                "id": 1,
                "alias": "worker-1",
                "state": "active",
                "type": "worker",
                "project": "project",
                "description": "work",
                "window": "worker-1",
                "blocked": None,
                "blocked_reason": None,
            }]}
            save_state_with_path(state_path, state)
            env = {**os.environ, "MECHA_STATE_FILE": str(state_path)}

            first = run_tool("state_block.py", "worker-1", "--block", "waiting for review", env=env)
            self.assertEqual(first.returncode, 0, first.stderr)
            after_first = read_state(state_path)["tasks"][0]
            first_timestamp = after_first["blocked"]
            first_revision = after_first["state_revision"]

            second = run_tool("state_block.py", "worker-1", "--block", "waiting for review", env=env)
            self.assertEqual(second.returncode, 0, second.stderr)
            self.assertEqual(second.stdout, "")
            after_second = read_state(state_path)["tasks"][0]
            self.assertEqual(after_second["blocked"], first_timestamp)
            self.assertEqual(after_second["state_revision"], first_revision)

            changed = run_tool("state_block.py", "worker-1", "--block", "router unavailable", env=env)
            self.assertEqual(changed.returncode, 0, changed.stderr)
            after_changed = read_state(state_path)["tasks"][0]
            self.assertEqual(after_changed["blocked_reason"], "router unavailable")
            self.assertEqual(after_changed["state_revision"], first_revision + 1)

    def test_unblock_clears_block_and_increments_revision(self):
        with tempfile.TemporaryDirectory() as tmp:
            state_path = Path(tmp) / "state.json"
            save_state_with_path(state_path, {
                "next_task_id": 2,
                "tasks": [{
                    "id": 1,
                    "alias": "worker-1",
                    "state": "active",
                    "type": "worker",
                    "project": "project",
                    "description": "work",
                    "window": "worker-1",
                    "blocked": "2026-01-01 00:00",
                    "blocked_reason": "waiting for review",
                    "state_revision": 1,
                }],
            })
            env = {**os.environ, "MECHA_STATE_FILE": str(state_path)}
            args = ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", "http://127.0.0.1:1"]

            blocked_failure = run_tool("mailbox_send.py", *args, env=env)
            self.assertNotEqual(blocked_failure.returncode, 0)
            self.assertNotEqual(blocked_failure.stderr, "")
            before = read_state(state_path)["tasks"][0]
            self.assertEqual(list(before["notification_dedup"]), ["blocked_worker"])

            unblocked = run_tool("state_block.py", "worker-1", "--unblock", env=env)
            self.assertEqual(unblocked.returncode, 0, unblocked.stderr)
            after_unblock = read_state(state_path)["tasks"][0]
            self.assertIsNone(after_unblock["blocked"])
            self.assertIsNone(after_unblock["blocked_reason"])
            self.assertEqual(after_unblock["state_revision"], 2)

            active_failure = run_tool("mailbox_send.py", *args, env=env)
            self.assertNotEqual(active_failure.returncode, 0)
            self.assertNotEqual(active_failure.stderr, "")
            self.assertIn("delivery_failure", read_state(state_path)["tasks"][0]["notification_dedup"])

    def test_blocked_mailbox_failure_notice_resets_when_reason_changes(self):
        with tempfile.TemporaryDirectory() as tmp:
            state_path = Path(tmp) / "state.json"
            save_state_with_path(state_path, {
                "next_task_id": 2,
                "tasks": [{
                    "id": 1,
                    "alias": "worker-1",
                    "state": "active",
                    "type": "worker",
                    "project": "project",
                    "description": "work",
                    "window": "worker-1",
                    "blocked": "2026-01-01 00:00",
                    "blocked_reason": "waiting for review",
                }],
            })
            env = {**os.environ, "MECHA_STATE_FILE": str(state_path)}
            args = ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", "http://127.0.0.1:1"]

            first = run_tool("mailbox_send.py", *args, env=env)
            second = run_tool("mailbox_send.py", *args, env=env)
            self.assertNotEqual(first.returncode, 0)
            self.assertNotEqual(second.returncode, 0)
            self.assertNotEqual(first.stderr, "")
            self.assertEqual(second.stderr, "")
            self.assertEqual(list(read_state(state_path)["tasks"][0]["notification_dedup"]), ["blocked_worker"])

            state = read_state(state_path)
            state["tasks"][0]["blocked_reason"] = "router unavailable"
            state["tasks"][0]["state_revision"] = 1
            save_state_with_path(state_path, state)
            changed = run_tool("mailbox_send.py", *args, env=env)
            self.assertNotEqual(changed.returncode, 0)
            self.assertNotEqual(changed.stderr, "")

    def test_mailbox_failure_attempts_are_not_suppressed_but_duplicate_notice_is(self):
        with tempfile.TemporaryDirectory() as tmp:
            state_path = Path(tmp) / "state.json"
            save_state_with_path(state_path, {
                "next_task_id": 2,
                "tasks": [{
                    "id": 1,
                    "alias": "worker-1",
                    "state": "active",
                    "type": "worker",
                    "project": "project",
                    "description": "work",
                    "window": "worker-1",
                }],
            })
            env = {**os.environ, "MECHA_STATE_FILE": str(state_path)}
            args = ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", "http://127.0.0.1:1"]

            first = run_tool("mailbox_send.py", *args, env=env)
            second = run_tool("mailbox_send.py", *args, env=env)

            self.assertNotEqual(first.returncode, 0)
            self.assertNotEqual(second.returncode, 0)
            self.assertNotEqual(first.stderr, "")
            self.assertEqual(second.stderr, "")
            notices = read_state(state_path)["tasks"][0]["notification_dedup"]
            self.assertEqual(list(notices), ["delivery_failure"])

            transitioned = run_tool(
                "state_update_state.py", "worker-1", "completed", "--result", "done", env=env
            )
            self.assertEqual(transitioned.returncode, 0, transitioned.stderr)
            after_transition = run_tool("mailbox_send.py", *args, env=env)
            duplicate_completed = run_tool("mailbox_send.py", *args, env=env)
            self.assertNotEqual(after_transition.returncode, 0)
            self.assertNotEqual(after_transition.stderr, "")
            self.assertNotEqual(duplicate_completed.returncode, 0)
            self.assertEqual(duplicate_completed.stderr, "")

    def test_abandoned_mailbox_failure_notice_is_deduped(self):
        with tempfile.TemporaryDirectory() as tmp:
            state_path = Path(tmp) / "state.json"
            save_state_with_path(state_path, {
                "next_task_id": 2,
                "tasks": [{
                    "id": 1,
                    "alias": "worker-1",
                    "state": "active",
                    "type": "worker",
                    "project": "project",
                    "description": "work",
                    "window": "worker-1",
                }],
            })
            env = {**os.environ, "MECHA_STATE_FILE": str(state_path)}
            args = ["--to", "worker-1", "--from", "supervisor", "--body", "Status", "--router-url", "http://127.0.0.1:1"]

            first = run_tool("mailbox_send.py", *args, env=env)
            self.assertNotEqual(first.returncode, 0)
            transitioned = run_tool(
                "state_update_state.py", "worker-1", "abandoned", "--reason", "stopped", env=env
            )
            self.assertEqual(transitioned.returncode, 0, transitioned.stderr)

            abandoned = run_tool("mailbox_send.py", *args, env=env)
            duplicate_abandoned = run_tool("mailbox_send.py", *args, env=env)
            self.assertNotEqual(abandoned.returncode, 0)
            self.assertNotEqual(abandoned.stderr, "")
            self.assertNotEqual(duplicate_abandoned.returncode, 0)
            self.assertEqual(duplicate_abandoned.stderr, "")


def save_state_with_path(path, state):
    path.write_text(json.dumps(state) + "\n", encoding="utf-8")


def read_state(path):
    return json.loads(path.read_text(encoding="utf-8"))


def run_tool(name, *args, env):
    return subprocess.run(
        [sys.executable, str(TOOLS_DIR / name), *args],
        capture_output=True,
        text=True,
        env=env,
    )


if __name__ == "__main__":
    unittest.main()
