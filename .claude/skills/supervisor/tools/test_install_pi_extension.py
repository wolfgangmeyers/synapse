#!/usr/bin/env python3
"""Tests for the Pi extension bundle installer."""

import unittest

import install_pi_extension


class InstallPiExtensionTest(unittest.TestCase):
    def test_bundle_declares_tmux_rest_dependencies(self):
        self.assertIn("_tmux_rest.py", install_pi_extension.OPERATIONAL_TOOL_MODULES)
        self.assertIn("tmux-rest-client.js", install_pi_extension.SIDECAR_HARNESS_MODULES)
        self.assertIn("goal.js", install_pi_extension.EXTENSION_MODULES)

    def test_bundle_dependencies_are_resolved(self):
        artifacts = install_pi_extension.extension_artifacts()
        self.assertEqual(install_pi_extension.missing_operational_dependencies(), set())
        self.assertEqual(install_pi_extension.missing_javascript_dependencies(artifacts), set())
        self.assertTrue(all(source.is_file() for source, _ in artifacts))


if __name__ == "__main__":
    unittest.main()
