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
            install_pi_extension, SOURCE=self.source, TARGET=self.target, SETTINGS=self.settings,
        )
        self.paths.start()
        self.addCleanup(self.paths.stop)

    def test_installs_the_mailbox_artifact_and_deduplicates_only_its_registration(self):
        stale = self.temp_path / "other" / "mecha-messaging.js"
        stale.parent.mkdir()
        stale.write_text("other", encoding="utf-8")
        self.target.parent.mkdir()
        self.target.write_text("old", encoding="utf-8")
        self.settings.parent.mkdir(parents=True)
        self.settings.write_text(json.dumps({
            "theme": "dark", "packages": ["npm:other", str(stale), str(self.target), str(self.target)],
        }), encoding="utf-8")

        self.assertEqual(install_pi_extension.main(), 0)

        self.assertEqual(self.target.read_bytes(), self.source.read_bytes())
        settings = json.loads(self.settings.read_text(encoding="utf-8"))
        self.assertEqual(settings["packages"], ["npm:other", str(stale), str(self.target)])
        self.assertTrue((self.target.with_name(self.target.name + ".rollback")).is_file())

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

    @property
    def temp_path(self):
        return Path(self.tempdir.name)


if __name__ == "__main__":
    unittest.main()
