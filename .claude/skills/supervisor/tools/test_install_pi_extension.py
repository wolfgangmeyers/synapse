#!/usr/bin/env python3
"""Tests for the retired Pi extension installer entry point."""

import contextlib
import io
from pathlib import Path
import unittest

import install_pi_extension


class InstallPiExtensionTest(unittest.TestCase):
    def test_retirement_is_explicit_and_fails_closed(self):
        output = io.StringIO()
        with contextlib.redirect_stderr(output), self.assertRaisesRegex(
            SystemExit, "install_pi_extension.py is retired"
        ) as raised:
            install_pi_extension.main()

        self.assertEqual(raised.exception.code, install_pi_extension.RETIREMENT_MESSAGE)
        self.assertEqual(output.getvalue(), "")

    def test_retired_entry_point_has_no_artifact_copy_or_settings_mutation_paths(self):
        source = Path(install_pi_extension.__file__).read_text(encoding="utf-8")

        self.assertNotIn("shutil", source)
        self.assertNotIn("atomic_copy", source)
        self.assertNotIn("settings.json", source)
        self.assertNotIn("os.replace", source)
        self.assertIn("preserve the registered checkout ", source)
        self.assertIn("do not create ~/.mecha-developer/extensions/mecha-messaging.js", source)


if __name__ == "__main__":
    unittest.main()
