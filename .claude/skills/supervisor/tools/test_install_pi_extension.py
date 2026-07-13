#!/usr/bin/env python3
"""Tests for the copied, globally registered Pi extension install transaction."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import install_pi_extension


class InstallPiExtensionTest(unittest.TestCase):
    def setUp(self):
        self.tempdir = tempfile.TemporaryDirectory()
        self.addCleanup(self.tempdir.cleanup)
        root = Path(self.tempdir.name)
        self.source = root / "source.js"
        self.target = root / "managed" / "mecha-messaging.js"
        self.settings = root / "pi" / "agent" / "settings.json"
        self.source.write_text("export default {};\n", encoding="utf-8")
        self.paths = patch.multiple(
            install_pi_extension,
            SOURCE=self.source,
            TARGET=self.target,
            SETTINGS=self.settings,
        )
        self.paths.start()
        self.addCleanup(self.paths.stop)

    def test_installs_stable_artifact_and_deduplicates_only_its_registration(self):
        stale = self.temp_path / "other" / "mecha-messaging.js"
        stale.parent.mkdir()
        stale.write_text("other", encoding="utf-8")
        self.target.parent.mkdir()
        self.target.write_text("old", encoding="utf-8")
        self.settings.parent.mkdir(parents=True)
        self.settings.write_text(json.dumps({
            "theme": "dark",
            "packages": ["npm:other", str(stale), str(self.target), str(self.target)],
        }), encoding="utf-8")

        self.assertEqual(install_pi_extension.main(), 0)

        self.assertEqual(self.target.read_text(encoding="utf-8"), self.source.read_text(encoding="utf-8"))
        settings = json.loads(self.settings.read_text(encoding="utf-8"))
        self.assertEqual(settings["theme"], "dark")
        self.assertEqual(settings["packages"], ["npm:other", str(stale), str(self.target)])
        self.assertTrue(stale.exists())
        self.assertTrue((self.target.with_name(self.target.name + ".rollback")).is_file())
        self.assertTrue((self.settings.with_name(self.settings.name + ".mecha-messaging.rollback")).is_file())

    def test_rejects_symlinked_source_without_mutation(self):
        actual = self.temp_path / "actual.js"
        actual.write_text("new", encoding="utf-8")
        source = self.temp_path / "symlink.js"
        source.symlink_to(actual)
        self.target.parent.mkdir()
        self.target.write_text("old", encoding="utf-8")
        self.settings.parent.mkdir(parents=True)
        original_settings = '{"packages": ["npm:other"]}\n'
        self.settings.write_text(original_settings, encoding="utf-8")

        with patch.object(install_pi_extension, "SOURCE", source), self.assertRaises(SystemExit):
            install_pi_extension.main()

        self.assertEqual(self.target.read_text(encoding="utf-8"), "old")
        self.assertEqual(self.settings.read_text(encoding="utf-8"), original_settings)

    def test_rejects_symlinked_target_without_mutation(self):
        actual = self.temp_path / "actual.js"
        actual.write_text("old", encoding="utf-8")
        self.target.parent.mkdir()
        self.target.symlink_to(actual)
        self.settings.parent.mkdir(parents=True)
        original_settings = '{"packages": ["npm:other"]}\n'
        self.settings.write_text(original_settings, encoding="utf-8")

        with self.assertRaises(SystemExit):
            install_pi_extension.main()

        self.assertTrue(self.target.is_symlink())
        self.assertEqual(self.settings.read_text(encoding="utf-8"), original_settings)

    def test_preserves_existing_files_when_backup_creation_fails(self):
        self.target.parent.mkdir()
        self.target.write_text("old", encoding="utf-8")
        self.settings.parent.mkdir(parents=True)
        original_settings = '{"theme": "dark", "packages": ["npm:other"]}\n'
        self.settings.write_text(original_settings, encoding="utf-8")

        with patch.object(install_pi_extension, "atomic_copy", side_effect=OSError("disk full")), \
             self.assertRaises(SystemExit):
            install_pi_extension.main()

        self.assertEqual(self.target.read_text(encoding="utf-8"), "old")
        self.assertEqual(self.settings.read_text(encoding="utf-8"), original_settings)

    def test_restores_artifact_and_settings_after_settings_write_failure(self):
        self.target.parent.mkdir()
        self.target.write_text("old", encoding="utf-8")
        self.settings.parent.mkdir(parents=True)
        original_settings = '{"theme": "dark", "packages": ["npm:other"]}\n'
        self.settings.write_text(original_settings, encoding="utf-8")

        with patch.object(install_pi_extension, "atomic_write_json", side_effect=OSError("disk full")), \
             self.assertRaises(SystemExit):
            install_pi_extension.main()

        self.assertEqual(self.target.read_text(encoding="utf-8"), "old")
        self.assertEqual(self.settings.read_text(encoding="utf-8"), original_settings)

    @property
    def temp_path(self):
        return Path(self.tempdir.name)


if __name__ == "__main__":
    unittest.main()
