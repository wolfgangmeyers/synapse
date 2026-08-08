#!/usr/bin/env python3
"""Tests for workflows tool scripts."""

import json
import os
import subprocess
import sys
import tempfile
import unittest


TOOLS_DIR = os.path.dirname(os.path.abspath(__file__))


class WorkflowsToolsTest(unittest.TestCase):
    def setUp(self):
        self.tmpdir = tempfile.TemporaryDirectory()
        self.workflows_file = os.path.join(self.tmpdir.name, "workflows.json")

    def tearDown(self):
        self.tmpdir.cleanup()

    def run_script(self, script_name, *args, input_text=None, env_extra=None, cwd=None):
        env = os.environ.copy()
        env["MECHA_WORKFLOWS_FILE"] = self.workflows_file
        if env_extra:
            env.update(env_extra)
        script_path = os.path.join(TOOLS_DIR, script_name)
        return subprocess.run(
            [sys.executable, script_path, *args],
            capture_output=True,
            text=True,
            env=env,
            input=input_text,
            cwd=cwd,
        )

    def read_store(self):
        if not os.path.exists(self.workflows_file):
            return []
        with open(self.workflows_file, "r", encoding="utf-8") as fh:
            return json.load(fh)

    def test_list_workflows_empty(self):
        result = self.run_script("list_workflows.py")
        self.assertEqual(result.returncode, 0)
        self.assertEqual(result.stdout.strip(), "No workflows defined yet.")

    def test_save_workflow_creates_new(self):
        result = self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
        )
        self.assertEqual(result.returncode, 0)
        self.assertIn("Saved workflow: deploy-staging", result.stdout)

        data = self.read_store()
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["name"], "deploy-staging")
        self.assertEqual(data[0]["description"], "Deploy app to staging")
        self.assertEqual(data[0]["body"], "1. Pull latest main")
        self.assertIn("created_at", data[0])
        self.assertIn("updated_at", data[0])

    def test_save_workflow_updates_existing_preserves_created_at(self):
        create_result = self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
        )
        self.assertEqual(create_result.returncode, 0)
        first = self.read_store()[0]
        created_at = first["created_at"]
        first["updated_at"] = "2000-01-01T00:00:00Z"
        with open(self.workflows_file, "w", encoding="utf-8") as fh:
            json.dump([first], fh)

        update_result = self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging quickly",
            "--body",
            "1. Pull latest main\n2. Run tests",
        )
        self.assertEqual(update_result.returncode, 0)

        second = self.read_store()[0]
        self.assertEqual(second["created_at"], created_at)
        self.assertNotEqual(second["updated_at"], "2000-01-01T00:00:00Z")
        self.assertEqual(second["description"], "Deploy app to staging quickly")
        self.assertEqual(second["body"], "1. Pull latest main\n2. Run tests")

    def test_list_workflows_with_entries(self):
        self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
        )
        result = self.run_script("list_workflows.py")
        self.assertEqual(result.returncode, 0)
        self.assertIn("deploy-staging", result.stdout)
        self.assertIn("Deploy app to staging", result.stdout)

    def test_get_workflow_by_name(self):
        save = self.run_script(
            "save_workflow.py",
            "--name",
            "onboard-dev",
            "--description",
            "Set up a new dev machine",
            "--body",
            "1. Install dependencies",
        )
        self.assertEqual(save.returncode, 0)

        result = self.run_script("get_workflow.py", "onboard-dev")
        self.assertEqual(result.returncode, 0)
        self.assertIn("Name:        onboard-dev", result.stdout)
        self.assertIn("Description: Set up a new dev machine", result.stdout)
        self.assertIn("--- Body ---", result.stdout)
        self.assertIn("1. Install dependencies", result.stdout)

    def test_get_workflow_unknown_name_exits_1(self):
        result = self.run_script("get_workflow.py", "does-not-exist")
        self.assertEqual(result.returncode, 1)
        self.assertIn("workflow not found", result.stderr.lower())

    def test_save_workflow_supports_body_file(self):
        body_file = os.path.join(self.tmpdir.name, "body.txt")
        with open(body_file, "w", encoding="utf-8") as fh:
            fh.write("1. Export $TOKEN\n2. echo \"done\"\n")

        result = self.run_script(
            "save_workflow.py",
            "--name",
            "quoted-workflow",
            "--description",
            "Handles quoted body",
            "--body-file",
            body_file,
        )
        self.assertEqual(result.returncode, 0)
        data = self.read_store()
        self.assertEqual(data[0]["body"], "1. Export $TOKEN\n2. echo \"done\"\n")

    def test_save_workflow_supports_body_file_stdin(self):
        result = self.run_script(
            "save_workflow.py",
            "--name",
            "stdin-workflow",
            "--description",
            "Uses stdin body",
            "--body-file",
            "-",
            input_text="1. Read from stdin\n2. Keep quotes: \"ok\"\n",
        )
        self.assertEqual(result.returncode, 0)
        data = self.read_store()
        self.assertEqual(data[0]["body"], "1. Read from stdin\n2. Keep quotes: \"ok\"\n")

    def test_save_workflow_missing_body_file_exits_1(self):
        result = self.run_script(
            "save_workflow.py",
            "--name",
            "missing-body",
            "--description",
            "Missing body file",
            "--body-file",
            os.path.join(self.tmpdir.name, "missing.txt"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("cannot read body file", result.stderr)

    def test_cleanup_without_confirmed_does_not_delete(self):
        self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
        )

        result = self.run_script("cleanup_workflows.py", "--name", "deploy-staging")
        self.assertEqual(result.returncode, 0)
        self.assertIn("Would delete: deploy-staging", result.stdout)
        self.assertIn("Pass --confirmed to proceed", result.stdout)

        data = self.read_store()
        self.assertEqual(len(data), 1)
        self.assertEqual(data[0]["name"], "deploy-staging")

    def test_cleanup_warns_for_missing_workflow_name(self):
        self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
        )
        result = self.run_script(
            "cleanup_workflows.py",
            "--name",
            "does-not-exist",
            "--confirmed",
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("Warning: workflow not found: does-not-exist", result.stderr)
        self.assertIn("Nothing deleted.", result.stderr)

    def test_cleanup_with_confirmed_deletes(self):
        self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
        )

        result = self.run_script(
            "cleanup_workflows.py",
            "--name",
            "deploy-staging",
            "--confirmed",
        )
        self.assertEqual(result.returncode, 0)
        self.assertIn("Deleted: deploy-staging", result.stdout)
        self.assertEqual(self.read_store(), [])

    def test_cleanup_all_with_confirmed_deletes_everything(self):
        self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
        )
        self.run_script(
            "save_workflow.py",
            "--name",
            "onboard-dev",
            "--description",
            "Set up dev machine",
            "--body",
            "1. Install dependencies",
        )

        result = self.run_script("cleanup_workflows.py", "--all", "--confirmed")
        self.assertEqual(result.returncode, 0)
        self.assertIn("Deleted: deploy-staging", result.stdout)
        self.assertIn("Deleted: onboard-dev", result.stdout)
        self.assertEqual(self.read_store(), [])

    def test_list_workflows_reports_corrupt_store(self):
        with open(self.workflows_file, "w", encoding="utf-8") as fh:
            fh.write("{")
        result = self.run_script("list_workflows.py")
        self.assertEqual(result.returncode, 1)
        self.assertIn("Error:", result.stderr)

    def test_list_workflows_reports_non_array_store(self):
        with open(self.workflows_file, "w", encoding="utf-8") as fh:
            fh.write("{}")
        result = self.run_script("list_workflows.py")
        self.assertEqual(result.returncode, 1)
        self.assertIn("expected a JSON array", result.stderr)

    def test_save_workflow_reports_non_array_store(self):
        with open(self.workflows_file, "w", encoding="utf-8") as fh:
            fh.write("{}")
        result = self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("expected a JSON array", result.stderr)

    def test_cleanup_reports_non_array_store(self):
        with open(self.workflows_file, "w", encoding="utf-8") as fh:
            fh.write("{}")
        result = self.run_script("cleanup_workflows.py", "--all", "--confirmed")
        self.assertEqual(result.returncode, 1)
        self.assertIn("expected a JSON array", result.stderr)

    def test_list_workflows_reports_non_object_record(self):
        with open(self.workflows_file, "w", encoding="utf-8") as fh:
            fh.write("[null]")
        result = self.run_script("list_workflows.py")
        self.assertEqual(result.returncode, 1)
        self.assertIn("record 0 is not an object", result.stderr)

    def test_save_workflow_works_with_bare_filename_env_path(self):
        env_file = "workflows.json"
        result = self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
            env_extra={"MECHA_WORKFLOWS_FILE": env_file},
            cwd=self.tmpdir.name,
        )
        self.assertEqual(result.returncode, 0)
        expected = os.path.join(self.tmpdir.name, env_file)
        self.assertTrue(os.path.exists(expected))
        with open(expected, "r", encoding="utf-8") as fh:
            saved = json.load(fh)
        self.assertEqual(saved[0]["name"], "deploy-staging")

    def test_save_workflow_writes_through_symlinked_store(self):
        real_dir = os.path.join(self.tmpdir.name, "real")
        link_dir = os.path.join(self.tmpdir.name, "link")
        os.makedirs(real_dir)
        os.makedirs(link_dir)
        real_file = os.path.join(real_dir, "workflows.json")
        with open(real_file, "w", encoding="utf-8") as fh:
            fh.write("[]\n")
        link_file = os.path.join(link_dir, "workflows.json")
        os.symlink(real_file, link_file)

        result = self.run_script(
            "save_workflow.py",
            "--name",
            "deploy-staging",
            "--description",
            "Deploy app to staging",
            "--body",
            "1. Pull latest main",
            env_extra={"MECHA_WORKFLOWS_FILE": link_file},
        )
        self.assertEqual(result.returncode, 0, result.stderr)

        self.assertTrue(os.path.islink(link_file))
        with open(real_file, "r", encoding="utf-8") as fh:
            saved = json.load(fh)
        self.assertEqual(len(saved), 1)
        self.assertEqual(saved[0]["name"], "deploy-staging")
        self.assertEqual(saved[0]["body"], "1. Pull latest main")

        # The lock guards the real file, so it belongs beside the real file.
        self.assertTrue(os.path.exists(os.path.join(real_dir, "workflows.json.lock")))
        self.assertFalse(os.path.exists(os.path.join(link_dir, "workflows.json.lock")))

    def test_cleanup_rejects_all_with_name(self):
        result = self.run_script(
            "cleanup_workflows.py",
            "--all",
            "--name",
            "deploy-staging",
            "--confirmed",
        )
        self.assertNotEqual(result.returncode, 0)


if __name__ == "__main__":
    unittest.main()
