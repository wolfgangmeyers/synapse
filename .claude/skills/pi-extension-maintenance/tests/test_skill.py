from pathlib import Path
import os
import subprocess
import tempfile
import unittest


SKILL = Path(__file__).parents[1] / "SKILL.md"


def embedded_program(start, end):
    section = SKILL.read_text().split(start, 1)[1].split(end, 1)[0]
    return section.split("python3 - <<'PY'\n", 1)[1].split("\nPY\n", 1)[0]


def install_program():
    return embedded_program("## Install or update", "## Uninstall")


def uninstall_program():
    return embedded_program("## Uninstall", "## Final verification")


def run_with_settings_replace_failure(program, environment):
    prelude = '''\nimport os\n_original_replace = os.replace\n_failed = False\ndef _fail_settings_replace(source, destination):\n    global _failed\n    if not _failed and os.fspath(destination) == os.environ["SETTINGS"]:\n        _failed = True\n        raise OSError("simulated settings replace failure")\n    return _original_replace(source, destination)\nos.replace = _fail_settings_replace\n'''
    return subprocess.run(["python3", "-c", prelude + program], env={**os.environ, **environment}, capture_output=True, text=True)


class PiExtensionMaintenanceSkillTests(unittest.TestCase):
    def setUp(self):
        self.skill = SKILL.read_text()

    def test_lifecycle_contract_is_explicit(self):
        for text in (
            "`TARGET` is the one stable installed artifact path",
            "test ! -L \"$SOURCE\"",
            "cmp -s \"$SOURCE\" \"$TARGET\"",
            "os.replace",
            "settings.name + \".mecha-messaging.rollback\"",
            "MECHA_ROUTER_URL",
            "pi --extension",
            "`/reload`",
            "console.log",
            "Do not mutate anything until these pass",
        ):
            with self.subTest(text=text):
                self.assertIn(text, self.skill)

    def test_install_preserves_unrelated_settings_and_packages(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "source.js"; source.write_text("export default {};\n")
            target = root / "managed" / "mecha-messaging.js"
            unrelated = root / "other" / "mecha-messaging.js"; unrelated.parent.mkdir(); unrelated.write_text("other")
            settings = root / "settings.json"
            settings.write_text('{"theme":"dark","packages":["npm:other","%s","%s","%s"]}\n' % (unrelated, target, target))
            result = subprocess.run(["python3", "-c", install_program()], env={**os.environ, "SOURCE": str(source), "TARGET": str(target), "SETTINGS": str(settings)}, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            import json
            data = json.loads(settings.read_text())
            self.assertEqual(data["theme"], "dark")
            self.assertEqual(data["packages"], ["npm:other", str(unrelated), str(target)])
            self.assertEqual(data["packages"].count(str(target)), 1)
            self.assertEqual(target.read_text(), source.read_text())
            self.assertTrue(unrelated.exists())

    def test_install_rejects_a_symlinked_canonical_source_without_mutation(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); canonical = root / "canonical.js"; actual = root / "actual.js"; actual.write_text("new"); canonical.symlink_to(actual)
            target = root / "managed" / "mecha-messaging.js"; target.parent.mkdir(); target.write_text("old")
            settings = root / "settings.json"; original_settings = '{"packages":["npm:other"]}\n'; settings.write_text(original_settings)
            result = subprocess.run(["python3", "-c", install_program()], env={**os.environ, "SOURCE": str(canonical), "TARGET": str(target), "SETTINGS": str(settings)}, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(target.read_text(), "old")
            self.assertEqual(settings.read_text(), original_settings)

    def test_install_rolls_back_new_artifact_when_settings_replace_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source = root / "source.js"; source.write_text("new")
            target = root / "managed" / "mecha-messaging.js"; settings = root / "settings.json"
            original_settings = '{"theme":"dark","packages":["npm:other"]}\n'; settings.write_text(original_settings)
            result = run_with_settings_replace_failure(install_program(), {"SOURCE": str(source), "TARGET": str(target), "SETTINGS": str(settings)})
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(target.exists())
            self.assertEqual(settings.read_text(), original_settings)

    def test_uninstall_retains_artifact_and_settings_when_settings_replace_fails(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); target = root / "managed" / "mecha-messaging.js"; target.parent.mkdir(); target.write_text("installed")
            settings = root / "settings.json"; original_settings = '{"theme":"dark","packages":["npm:other","%s"]}\n' % target; settings.write_text(original_settings)
            result = run_with_settings_replace_failure(uninstall_program(), {"TARGET": str(target), "SETTINGS": str(settings)})
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(target.read_text(), "installed")
            self.assertEqual(settings.read_text(), original_settings)

    def test_install_rejects_a_symlinked_target(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source = root / "source.js"; source.write_text("new")
            original = root / "original.js"; original.write_text("old")
            target = root / "managed" / "mecha-messaging.js"; target.parent.mkdir(); target.symlink_to(original)
            settings = root / "settings.json"; settings.write_text('{"packages": []}\n')
            result = subprocess.run(["python3", "-c", install_program()], env={**os.environ, "SOURCE": str(source), "TARGET": str(target), "SETTINGS": str(settings)}, capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertTrue(target.is_symlink())
            self.assertEqual(settings.read_text(), '{"packages": []}\n')

    def test_install_initializes_missing_packages(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); source = root / "source.js"; source.write_text("export default {};\n")
            target = root / "managed" / "mecha-messaging.js"; settings = root / "settings.json"; settings.write_text('{"theme":"dark"}\n')
            result = subprocess.run(["python3", "-c", install_program()], env={**os.environ, "SOURCE": str(source), "TARGET": str(target), "SETTINGS": str(settings)}, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            import json
            self.assertEqual(json.loads(settings.read_text())["packages"], [str(target)])


if __name__ == "__main__":
    unittest.main()
