from pathlib import Path
import json
import os
import subprocess
import tempfile
import unittest


SKILL = Path(__file__).parents[1] / "SKILL.md"
INSTALL_SKILL = SKILL.parents[1] / "install" / "SKILL.md"
VERIFIER_MARKER = 'python3 - "$SOURCE" "$SETTINGS" <<\'PY\'\n'


def embedded_program():
    section = INSTALL_SKILL.read_text().split("### Pi mailbox extension (bare Pi)", 1)[1].split("### 8. Symlink Hooks", 1)[0]
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
        self.assertIn("REPO_ROOT/pi-extensions/mecha-messaging.js", skill)
        self.assertIn("Do not copy, move, symlink, restore, or replace it", skill)
        self.assertIn("registration for `~/.mecha-developer/extensions/mecha-messaging.js` is stale", skill)
        self.assertNotIn("install_pi_extension.py", skill)

    def test_install_contains_one_registration_verifier_source(self):
        documentation = INSTALL_SKILL.read_text()
        self.assertEqual(documentation.count(VERIFIER_MARKER), 1)
        self.assertIn("expected exactly one Pi extension registration", documentation)
        self.assertIn("registration check above", documentation)

    def run_verifier(self, source, settings):
        return subprocess.run(
            ["python3", "-c", embedded_program(), str(source), str(settings)],
            env=os.environ.copy(), capture_output=True, text=True,
        )

    def test_registration_verifier_accepts_one_checkout_registration(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "pi-extensions" / "mecha-messaging.js"
            source.parent.mkdir()
            source.write_text("export default {};\n")
            settings = root / "settings.json"
            settings.write_text(json.dumps({"packages": [str(source)]}))
            result = self.run_verifier(source, settings)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_registration_verifier_rejects_malformed_settings(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "mecha-messaging.js"
            source.write_text("export default {};\n")
            settings = root / "settings.json"
            settings.write_text("{malformed")
            self.assertNotEqual(self.run_verifier(source, settings).returncode, 0)

    def test_registration_verifier_rejects_missing_registration(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "mecha-messaging.js"
            source.write_text("export default {};\n")
            settings = root / "settings.json"
            settings.write_text(json.dumps({"packages": []}))
            result = self.run_verifier(source, settings)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("found 0", result.stderr)

    def test_registration_verifier_rejects_duplicate_registration(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "mecha-messaging.js"
            source.write_text("export default {};\n")
            settings = root / "settings.json"
            settings.write_text(json.dumps({"packages": [str(source), str(source)]}))
            result = self.run_verifier(source, settings)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("found 2", result.stderr)


if __name__ == "__main__":
    unittest.main()
