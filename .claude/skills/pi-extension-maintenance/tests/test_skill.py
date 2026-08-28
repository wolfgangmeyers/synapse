from pathlib import Path
import json
import os
import subprocess
import tempfile
import unittest


SKILL = Path(__file__).parents[1] / "SKILL.md"
INSTALL_SKILL = SKILL.parents[1] / "install" / "SKILL.md"
CURSOR_DOC = SKILL.parents[3] / "docs" / "cursor-tool-surfaces.md"
VERIFIER_MARKER = 'python3 - "$GOAL_SOURCE" "$MESSAGING_SOURCE" "$SETTINGS" <<\'PY\'\n'
PACKAGE_SECTION = "### Pi checkout packages (bare Pi)"
MAINTENANCE_STATUS_SECTION = "## Status and registration check (read-only)"


def embedded_program():
    section = INSTALL_SKILL.read_text().split(PACKAGE_SECTION, 1)[1].split("### 8. Symlink Hooks", 1)[0]
    return section.split(VERIFIER_MARKER, 1)[1].split("\nPY\n", 1)[0]


def maintenance_program():
    section = SKILL.read_text().split(MAINTENANCE_STATUS_SECTION, 1)[1].split("\n```\n", 1)[0]
    return section.split(VERIFIER_MARKER, 1)[1].split("\nPY\n", 1)[0]


class PiExtensionMaintenanceSkillTests(unittest.TestCase):
    def test_source_in_place_contract_and_headings_are_explicit(self):
        skill = SKILL.read_text()
        for heading in (
            "## Status and registration check (read-only)",
            "## Preflight",
            "## Source updates",
            "## Final verification",
        ):
            self.assertIn(heading, skill)
        self.assertIn("REPO_ROOT/pi-extensions/mecha-goal/", skill)
        self.assertIn("REPO_ROOT/pi-extensions/mecha-messaging/", skill)
        self.assertIn("Goal-first", skill)
        self.assertNotIn("REPO_ROOT/pi-extensions/mecha-messaging.js", skill)
        self.assertIn("Do not copy, move, symlink, restore,", skill)
        self.assertIn("registration for `~/.mecha-developer/extensions/mecha-messaging.js` is stale", skill)
        self.assertIn("exactly one Goal registration followed immediately\nby exactly one Messaging registration", skill)
        self.assertIn("reject standalone or old flat Pi registration", skill)
        self.assertIn('path.name in {"goal.js", "mecha-messaging.js"}', skill)
        self.assertIn("~/.mecha-developer/extensions/pi-extensions/mecha-messaging/mailbox-client.js", skill)
        self.assertIn("npm run test:mecha-messaging", skill)
        self.assertNotIn("pre-split registration", skill)
        self.assertNotIn("pi_extension_silent.test.mjs", skill)
        self.assertNotIn("install_pi_extension.py", skill)

    def test_cursor_docs_describe_split_registrations(self):
        documentation = CURSOR_DOC.resolve().read_text()
        self.assertIn("two checkout packages", documentation)
        self.assertIn("Goal package registers Goal tools", documentation)
        self.assertIn("Messaging package registers messaging", documentation)
        self.assertNotIn("registered once", documentation)
        self.assertNotIn("composed once", documentation)

    def test_install_contains_one_registration_verifier_source(self):
        documentation = INSTALL_SKILL.read_text()
        self.assertEqual(documentation.count(VERIFIER_MARKER), 1)
        self.assertIn("expected exactly one Goal and Messaging checkout package registration in Goal-first order", documentation)
        self.assertIn("reject standalone or old flat Pi registration", documentation)
        self.assertIn('resolved.name in {"goal.js", "mecha-messaging.js"}', documentation)
        self.assertIn("registration check above", documentation)
        self.assertIn("~/.mecha-developer/extensions/pi-extensions/mecha-messaging/mailbox-client.js", documentation)
        self.assertIn("Goal-first order", documentation)
        self.assertIn("npm run test:mecha-messaging", documentation)
        self.assertNotIn("pre-split single Messaging registration", documentation)
        self.assertNotIn("pi_extension_silent.test.mjs", documentation)

    def run_verifier(self, goal_source, messaging_source, settings, program=None):
        return subprocess.run(
            ["python3", "-c", program or embedded_program(), str(goal_source), str(messaging_source), str(settings)],
            env=os.environ.copy(), capture_output=True, text=True,
        )

    def test_maintenance_verifier_rejects_flat_graph_registrations(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            goal_source = root / "pi-extensions" / "mecha-goal"
            messaging_source = root / "pi-extensions" / "mecha-messaging"
            goal_source.mkdir(parents=True)
            messaging_source.mkdir(parents=True)
            settings = root / "settings.json"
            for legacy in ("goal.js", "mecha-messaging.js"):
                with self.subTest(legacy=legacy):
                    settings.write_text(json.dumps({"packages": [str(goal_source), str(messaging_source), str(root / legacy)]}))
                    result = self.run_verifier(goal_source, messaging_source, settings, maintenance_program())
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("old flat", result.stderr)

    def test_install_verifier_rejects_flat_graph_registrations(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            goal_source = root / "pi-extensions" / "mecha-goal"
            messaging_source = root / "pi-extensions" / "mecha-messaging"
            goal_source.mkdir(parents=True)
            messaging_source.mkdir(parents=True)
            settings = root / "settings.json"
            for legacy in ("goal.js", "mecha-messaging.js"):
                with self.subTest(legacy=legacy):
                    settings.write_text(json.dumps({"packages": [str(goal_source), str(messaging_source), str(root / legacy)]}))
                    result = self.run_verifier(goal_source, messaging_source, settings)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("old flat", result.stderr)

    def test_registration_verifier_accepts_goal_then_messaging_checkout_registrations(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            goal_source = root / "pi-extensions" / "mecha-goal"
            messaging_source = root / "pi-extensions" / "mecha-messaging"
            goal_source.mkdir(parents=True)
            messaging_source.mkdir(parents=True)
            settings = root / "settings.json"
            settings.write_text(json.dumps({"packages": [str(goal_source), str(messaging_source)]}))
            result = self.run_verifier(goal_source, messaging_source, settings)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_registration_verifier_rejects_malformed_settings(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            goal_source = root / "mecha-goal"
            messaging_source = root / "mecha-messaging"
            goal_source.mkdir()
            messaging_source.mkdir()
            settings = root / "settings.json"
            settings.write_text("{malformed")
            self.assertNotEqual(self.run_verifier(goal_source, messaging_source, settings).returncode, 0)

    def test_registration_verifier_rejects_missing_registration(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            goal_source = root / "mecha-goal"
            messaging_source = root / "mecha-messaging"
            goal_source.mkdir()
            messaging_source.mkdir()
            settings = root / "settings.json"
            settings.write_text(json.dumps({"packages": []}))
            result = self.run_verifier(goal_source, messaging_source, settings)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Goal-first order", result.stderr)

    def test_registration_verifier_rejects_duplicate_registration(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            goal_source = root / "mecha-goal"
            messaging_source = root / "mecha-messaging"
            goal_source.mkdir()
            messaging_source.mkdir()
            settings = root / "settings.json"
            settings.write_text(json.dumps({"packages": [str(goal_source), str(messaging_source), str(messaging_source)]}))
            result = self.run_verifier(goal_source, messaging_source, settings)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("Goal-first order", result.stderr)


if __name__ == "__main__":
    unittest.main()
