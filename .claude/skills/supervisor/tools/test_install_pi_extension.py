#!/usr/bin/env python3
"""Tests for the Pi extension support-bundle installer."""

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock


INSTALLER = Path(__file__).with_name("install_pi_extension.py")


class InstallPiExtensionTest(unittest.TestCase):
    def setUp(self):
        self.home = tempfile.TemporaryDirectory()
        self.previous_home = os.environ.get("HOME")
        os.environ["HOME"] = self.home.name
        self.installer = self._load_installer()

    def tearDown(self):
        if self.previous_home is None:
            os.environ.pop("HOME", None)
        else:
            os.environ["HOME"] = self.previous_home
        self.home.cleanup()

    @staticmethod
    def _load_installer():
        spec = importlib.util.spec_from_file_location("install_pi_extension_test_target", INSTALLER)
        module = importlib.util.module_from_spec(spec)
        assert spec.loader is not None
        spec.loader.exec_module(module)
        return module

    def reset_home(self):
        self.home.cleanup()
        self.home = tempfile.TemporaryDirectory()
        os.environ["HOME"] = self.home.name
        self.installer = self._load_installer()

    def seed_old_install(self):
        installer = self.installer
        installer.INSTALLED_SUPPORT_ROOT.mkdir(parents=True, exist_ok=True)
        (installer.INSTALLED_SUPPORT_ROOT / "unrelated-support.txt").write_bytes(b"keep this file\n")
        (installer.INSTALLED_SUPPORT_ROOT / "unrelated" / "nested.bin").parent.mkdir()
        (installer.INSTALLED_SUPPORT_ROOT / "unrelated" / "nested.bin").write_bytes(b"also keep\0\xff")
        # These represent the old copied flat graph. The migration must remove
        # them while retaining files outside the installer manifest.
        for relative in installer.LEGACY_SUPPORT_FILES:
            path = installer.INSTALLED_SUPPORT_ROOT / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(f"legacy:{relative}\n", encoding="utf-8")
        legacy_client = installer.INSTALLED_SUPPORT_ROOT / "pi-extensions" / "mecha-messaging" / "mailbox-client.js"
        legacy_client.parent.mkdir(parents=True, exist_ok=True)
        legacy_client.write_text("legacy generated client\n", encoding="utf-8")

        installer.SETTINGS.parent.mkdir(parents=True, exist_ok=True)
        installer.SETTINGS.write_text(json.dumps({
            "theme": "preserve",
            "packages": [
                str(installer.SOURCE),
                str(installer.LEGACY_CHECKOUT_REGISTRATIONS[1]),
                str(installer.TARGET),
                str(installer.INSTALLED_SUPPORT_ROOT / "goal.js"),
                str(installer.GOAL_PACKAGE),
                str(installer.GOAL_PACKAGE.parent / "mecha-goal" / ".." / "mecha-goal"),
                str(installer.CHECKOUT_PACKAGE),
                str(installer.CHECKOUT_PACKAGE.parent / "mecha-messaging" / ".." / "mecha-messaging"),
                "/some/other/package",
            ],
        }), encoding="utf-8")

    @staticmethod
    def snapshot_tree(root):
        result = {}
        if not root.exists():
            return result
        for path in sorted(root.rglob("*")):
            relative = path.relative_to(root)
            if path.is_symlink():
                result[str(relative)] = ("symlink", os.readlink(path))
            elif path.is_file():
                result[str(relative)] = ("file", path.read_bytes())
            elif path.is_dir():
                result[str(relative)] = ("directory", None)
        return result

    def test_manifest_uses_checkout_package_and_generated_client_support_copy(self):
        installer = self.installer
        artifacts = installer.extension_artifacts()
        self.assertEqual(installer.REGISTERED_EXTENSIONS, (installer.GOAL_PACKAGE, installer.CHECKOUT_PACKAGE))
        self.assertEqual(artifacts[0][0], installer.CHECKOUT_PACKAGE / "mailbox-client.js")
        self.assertEqual(
            artifacts[0][1],
            installer.INSTALLED_SUPPORT_ROOT / "pi-extensions" / "mecha-messaging" / "mailbox-client.js",
        )
        self.assertNotIn(installer.TARGET, installer.REGISTERED_EXTENSIONS)
        installed_sidecar = (
            installer.INSTALLED_SUPPORT_ROOT / ".claude" / "skills" / "supervisor" / "tools" / "claude_sidecar.mjs"
        )
        sidecar_client = installed_sidecar.parent / "../../../../pi-extensions/mecha-messaging/mailbox-client.js"
        self.assertEqual(artifacts[0][1].resolve(), sidecar_client.resolve())
        self.assertEqual(installer.missing_operational_dependencies(), set())
        self.assertEqual(installer.missing_javascript_dependencies(artifacts), set())
        self.assertTrue(all(source.is_file() for source, _ in artifacts))

    def test_build_settings_registers_ordered_packages_and_deduplicates_both_paths(self):
        installer = self.installer
        settings = {
            "theme": "preserve",
            "packages": [
                "/some/other/package",
                "/unrelated/extension.js",
                "/arbitrary/old-copy/goal.js",
                "/arbitrary/old-copy/mecha-messaging.js",
                str(installer.CHECKOUT_PACKAGE),
                str(installer.GOAL_PACKAGE),
                str(installer.GOAL_PACKAGE.parent / "mecha-goal" / ".." / "mecha-goal"),
                str(installer.CHECKOUT_PACKAGE.parent / "mecha-messaging" / ".." / "mecha-messaging"),
            ],
        }

        built = installer.build_settings(settings)

        self.assertEqual(built["packages"], [
            "/some/other/package",
            "/unrelated/extension.js",
            str(installer.GOAL_PACKAGE),
            str(installer.CHECKOUT_PACKAGE),
        ])
        self.assertEqual(settings["packages"][0], "/some/other/package")

    def test_rejects_missing_or_symlinked_checkout_package(self):
        installer = self.installer
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            goal = root / "mecha-goal"
            messaging = root / "mecha-messaging"
            goal.mkdir()
            messaging.mkdir()

            with mock.patch.object(installer, "CHECKOUT_PACKAGES", (goal, root / "missing")):
                with self.assertRaises(SystemExit) as raised:
                    installer.main()
                self.assertIn("checkout package", str(raised.exception))

            link_target = root / "real-messaging"
            link_target.mkdir()
            symlink = root / "mecha-messaging-link"
            symlink.symlink_to(link_target, target_is_directory=True)
            with mock.patch.object(installer, "CHECKOUT_PACKAGES", (goal, symlink)):
                with self.assertRaises(SystemExit) as raised:
                    installer.main()
                self.assertIn("checkout package", str(raised.exception))

    def test_success_preserves_unrelated_support_and_registers_only_checkout_packages(self):
        self.seed_old_install()
        installer = self.installer
        unrelated_before = self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT / "unrelated")

        self.assertEqual(installer.main(), 0)

        self.assertEqual(
            self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT / "unrelated"),
            unrelated_before,
        )
        self.assertFalse((installer.INSTALLED_SUPPORT_ROOT / "mecha-messaging.js").exists())
        self.assertFalse((installer.INSTALLED_SUPPORT_ROOT / "goal.js").exists())
        self.assertFalse((installer.INSTALLED_SUPPORT_ROOT / "mailbox-client.js").exists())
        self.assertFalse((installer.INSTALLED_SUPPORT_ROOT / "pi-extensions" / "mailbox-client.js").exists())
        installer.verify_manifest(installer.extension_artifacts(), installer.INSTALLED_SUPPORT_ROOT)
        generated_source = (installer.CHECKOUT_PACKAGE / "mailbox-client.js").read_bytes()
        generated_support_copy = (installer.INSTALLED_SUPPORT_ROOT / "pi-extensions" / "mecha-messaging" / "mailbox-client.js").read_bytes()
        self.assertEqual(generated_support_copy, generated_source)
        settings = json.loads(installer.SETTINGS.read_text(encoding="utf-8"))
        self.assertEqual(settings["theme"], "preserve")
        self.assertEqual(settings["packages"], [
            "/some/other/package",
            str(installer.GOAL_PACKAGE),
            str(installer.CHECKOUT_PACKAGE),
        ])
        self.assertNotIn(str(installer.TARGET), settings["packages"])
        self.assertNotIn(str(installer.SOURCE), settings["packages"])
        self.assertNotIn(str(installer.LEGACY_CHECKOUT_REGISTRATIONS[1]), settings["packages"])
        self.assertNotIn(str(installer.INSTALLED_SUPPORT_ROOT / "goal.js"), settings["packages"])
        self.assertFalse(installer.TRANSACTION_MARKER.exists())
        self.assertFalse(installer.SUPPORT_BACKUP.exists())
        self.assertFalse(installer.SETTINGS_BACKUP.exists())

    def test_recovery_restores_complete_prior_state_after_each_injected_phase(self):
        for phase in ("after_recording", "after_support_replacement", "after_settings_replacement"):
            with self.subTest(phase=phase):
                self.reset_home()
                self.seed_old_install()
                installer = self.installer
                old_support = self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT)
                old_settings = installer.SETTINGS.read_bytes()
                os.environ["MECHA_MESSAGING_INSTALL_FAIL_PHASE"] = phase
                with self.assertRaises(SystemExit) as raised:
                    installer.main()
                self.assertIn("retry to recover", str(raised.exception))
                self.assertTrue(installer.TRANSACTION_MARKER.is_file())
                marker = json.loads(installer.TRANSACTION_MARKER.read_text(encoding="utf-8"))
                self.assertEqual(marker["version"], 2)
                self.assertEqual(marker["registered_packages"], [
                    str(installer.GOAL_PACKAGE),
                    str(installer.CHECKOUT_PACKAGE),
                ])
                self.assertEqual(marker["intended_settings"]["packages"][-2:], marker["registered_packages"])

                # Import after HOME is set, as the real retry process does.
                recovered = self._load_installer()
                recovered.recover_transaction()
                self.assertEqual(self.snapshot_tree(recovered.INSTALLED_SUPPORT_ROOT), old_support)
                self.assertEqual(recovered.SETTINGS.read_bytes(), old_settings)
                self.assertFalse(recovered.TRANSACTION_MARKER.exists())
                self.assertFalse(recovered.SUPPORT_BACKUP.exists())
                self.assertFalse(recovered.SETTINGS_BACKUP.exists())
                os.environ.pop("MECHA_MESSAGING_INSTALL_FAIL_PHASE", None)
                self.assertEqual(recovered.main(), 0)

    def test_recovery_accepts_version_one_marker_after_schema_upgrade(self):
        self.seed_old_install()
        installer = self.installer
        old_support = self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT)
        old_settings = installer.SETTINGS.read_bytes()

        os.environ["MECHA_MESSAGING_INSTALL_FAIL_PHASE"] = "after_support_replacement"
        try:
            with self.assertRaises(SystemExit):
                installer.main()
        finally:
            os.environ.pop("MECHA_MESSAGING_INSTALL_FAIL_PHASE", None)

        marker = json.loads(installer.TRANSACTION_MARKER.read_text(encoding="utf-8"))
        marker["version"] = 1
        marker.pop("registered_packages")
        marker["intended_settings"] = {"packages": [str(installer.CHECKOUT_PACKAGE)]}
        installer.TRANSACTION_MARKER.write_text(json.dumps(marker), encoding="utf-8")

        recovered = self._load_installer()
        recovered.recover_transaction()
        self.assertEqual(self.snapshot_tree(recovered.INSTALLED_SUPPORT_ROOT), old_support)
        self.assertEqual(recovered.SETTINGS.read_bytes(), old_settings)
        self.assertFalse(recovered.TRANSACTION_MARKER.exists())
        self.assertFalse(recovered.SUPPORT_BACKUP.exists())
        self.assertFalse(recovered.SETTINGS_BACKUP.exists())

    def test_recovery_removes_only_unmarked_settings_backup_with_valid_current_settings(self):
        installer = self.installer
        installer.INSTALLED_SUPPORT_ROOT.mkdir(parents=True, exist_ok=True)
        (installer.INSTALLED_SUPPORT_ROOT / "current-support.txt").write_bytes(b"current support\n")
        support_before = self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT)
        installer.SETTINGS.parent.mkdir(parents=True, exist_ok=True)
        installer.SETTINGS.write_text(
            json.dumps({"theme": "current", "packages": ["/current/package"]}),
            encoding="utf-8",
        )
        settings_before = installer.SETTINGS.read_bytes()
        installer.SETTINGS_BACKUP.write_text(
            json.dumps({"theme": "legacy", "packages": ["/old/package"]}),
            encoding="utf-8",
        )

        installer.recover_transaction()

        self.assertEqual(installer.SETTINGS.read_bytes(), settings_before)
        self.assertEqual(self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT), support_before)
        self.assertFalse(installer.SETTINGS_BACKUP.exists())
        self.assertFalse(installer.TRANSACTION_MARKER.exists())

    def test_main_continues_after_recovering_unmarked_settings_backup(self):
        self.seed_old_install()
        installer = self.installer
        installer.SETTINGS_BACKUP.write_text(
            json.dumps({"theme": "legacy", "packages": ["/old/package"]}),
            encoding="utf-8",
        )

        self.assertEqual(installer.main(), 0)

        settings = json.loads(installer.SETTINGS.read_text(encoding="utf-8"))
        self.assertEqual(settings["theme"], "preserve")
        self.assertEqual(settings["packages"][-2:], [
            str(installer.GOAL_PACKAGE),
            str(installer.CHECKOUT_PACKAGE),
        ])
        self.assertFalse(installer.SETTINGS_BACKUP.exists())
        self.assertFalse(installer.TRANSACTION_MARKER.exists())

    def test_main_stops_before_staging_when_unmarked_backup_cleanup_fsync_fails(self):
        installer = self.installer
        installer.SETTINGS.parent.mkdir(parents=True, exist_ok=True)
        installer.SETTINGS.write_text(json.dumps({"packages": []}), encoding="utf-8")
        settings_before = installer.SETTINGS.read_bytes()
        installer.SETTINGS_BACKUP.write_text(json.dumps({"packages": []}), encoding="utf-8")

        with (
            mock.patch.object(installer, "fsync_directory", side_effect=OSError("fsync failed")),
            mock.patch.object(installer, "stage_support_root") as stage_support_root,
            self.assertRaises(SystemExit) as raised,
        ):
            installer.main()

        self.assertIn("Installer recovery failed", str(raised.exception))
        stage_support_root.assert_not_called()
        self.assertEqual(installer.SETTINGS.read_bytes(), settings_before)
        self.assertFalse(installer.TRANSACTION_MARKER.exists())

    def test_marked_recovery_does_not_run_unmarked_cleanup(self):
        self.seed_old_install()
        installer = self.installer
        old_support = self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT)
        old_settings = installer.SETTINGS.read_bytes()
        os.environ["MECHA_MESSAGING_INSTALL_FAIL_PHASE"] = "after_recording"
        try:
            with self.assertRaises(SystemExit):
                installer.main()
        finally:
            os.environ.pop("MECHA_MESSAGING_INSTALL_FAIL_PHASE", None)

        recovered = self._load_installer()
        with mock.patch.object(recovered, "clear_unmarked_settings_backup") as unmarked_cleanup:
            recovered.recover_transaction()

        unmarked_cleanup.assert_not_called()
        self.assertEqual(self.snapshot_tree(recovered.INSTALLED_SUPPORT_ROOT), old_support)
        self.assertEqual(recovered.SETTINGS.read_bytes(), old_settings)
        self.assertFalse(recovered.TRANSACTION_MARKER.exists())
        self.assertFalse(recovered.SUPPORT_BACKUP.exists())
        self.assertFalse(recovered.SETTINGS_BACKUP.exists())

    def test_recovery_preserves_unmarked_settings_backup_without_valid_current_settings(self):
        for current in (None, b"not json", json.dumps({"packages": "not a list"}).encode("utf-8")):
            with self.subTest(current=current):
                self.reset_home()
                installer = self.installer
                installer.SETTINGS.parent.mkdir(parents=True, exist_ok=True)
                if current is not None:
                    installer.SETTINGS.write_bytes(current)
                installer.SETTINGS_BACKUP.write_text(
                    json.dumps({"theme": "legacy", "packages": ["/old/package"]}),
                    encoding="utf-8",
                )
                backup_before = installer.SETTINGS_BACKUP.read_bytes()

                with self.assertRaises(RuntimeError):
                    installer.recover_transaction()

                self.assertEqual(installer.SETTINGS_BACKUP.read_bytes(), backup_before)
                if current is None:
                    self.assertFalse(installer.SETTINGS.exists())
                else:
                    self.assertEqual(installer.SETTINGS.read_bytes(), current)

    def test_recovery_preserves_ambiguous_unmarked_rollback_state(self):
        installer = self.installer
        installer.SETTINGS.parent.mkdir(parents=True, exist_ok=True)
        installer.SETTINGS.write_text(json.dumps({"packages": []}), encoding="utf-8")
        installer.SETTINGS_BACKUP.write_text(json.dumps({"packages": []}), encoding="utf-8")
        installer.SUPPORT_BACKUP.mkdir(parents=True)
        (installer.SUPPORT_BACKUP / "old-support.txt").write_bytes(b"old support\n")
        settings_backup_before = installer.SETTINGS_BACKUP.read_bytes()
        support_backup_before = self.snapshot_tree(installer.SUPPORT_BACKUP)

        with self.assertRaises(RuntimeError):
            installer.recover_transaction()

        self.assertEqual(installer.SETTINGS_BACKUP.read_bytes(), settings_backup_before)
        self.assertEqual(self.snapshot_tree(installer.SUPPORT_BACKUP), support_backup_before)

    def test_recovery_explicitly_preserves_symlinked_unmarked_settings_backup(self):
        installer = self.installer
        installer.SETTINGS.parent.mkdir(parents=True, exist_ok=True)
        installer.SETTINGS.write_text(json.dumps({"packages": []}), encoding="utf-8")
        outside = Path(self.home.name) / "outside-settings-backup"
        outside.write_bytes(b"do not touch\n")
        installer.SETTINGS_BACKUP.symlink_to(outside)

        with self.assertRaises(RuntimeError):
            installer.recover_transaction()

        self.assertTrue(installer.SETTINGS_BACKUP.is_symlink())
        self.assertEqual(outside.read_bytes(), b"do not touch\n")

    def test_recovery_preserves_directory_unmarked_settings_backup(self):
        installer = self.installer
        installer.SETTINGS.parent.mkdir(parents=True, exist_ok=True)
        installer.SETTINGS.write_text(json.dumps({"packages": []}), encoding="utf-8")
        installer.SETTINGS_BACKUP.mkdir()
        (installer.SETTINGS_BACKUP / "unexpected.txt").write_bytes(b"do not touch\n")
        backup_before = self.snapshot_tree(installer.SETTINGS_BACKUP)

        with self.assertRaises(RuntimeError):
            installer.recover_transaction()

        self.assertEqual(self.snapshot_tree(installer.SETTINGS_BACKUP), backup_before)

    def test_recovery_retries_after_interruption_between_restore_phases(self):
        self.seed_old_install()
        installer = self.installer
        old_support = self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT)
        old_settings = installer.SETTINGS.read_bytes()

        os.environ["MECHA_MESSAGING_INSTALL_FAIL_PHASE"] = "after_support_replacement"
        try:
            with self.assertRaises(SystemExit):
                installer.main()
        finally:
            os.environ.pop("MECHA_MESSAGING_INSTALL_FAIL_PHASE", None)

        recovered = self._load_installer()
        os.environ["MECHA_MESSAGING_INSTALL_FAIL_PHASE"] = "during_recovery_after_support"
        try:
            with self.assertRaises(SystemExit) as raised:
                recovered.main()
            self.assertIn("Installer recovery failed", str(raised.exception))
            # The support restore completed, but both backups must remain so a
            # retry can safely repeat it and still restore settings.
            self.assertEqual(self.snapshot_tree(recovered.INSTALLED_SUPPORT_ROOT), old_support)
            self.assertEqual(recovered.SETTINGS.read_bytes(), old_settings)
            self.assertTrue(recovered.TRANSACTION_MARKER.is_file())
            self.assertTrue(recovered.SUPPORT_BACKUP.is_dir())
            self.assertTrue(recovered.SETTINGS_BACKUP.is_file())
        finally:
            os.environ.pop("MECHA_MESSAGING_INSTALL_FAIL_PHASE", None)

        self.assertEqual(recovered.main(), 0)
        self.assertFalse(recovered.TRANSACTION_MARKER.exists())
        self.assertFalse(recovered.SUPPORT_BACKUP.exists())
        self.assertFalse(recovered.SETTINGS_BACKUP.exists())

    def test_rejects_symlinked_parent_for_managed_support_write(self):
        installer = self.installer
        installer.INSTALLED_SUPPORT_ROOT.mkdir(parents=True, exist_ok=True)
        outside = Path(self.home.name) / "outside-support"
        outside.mkdir()
        (installer.INSTALLED_SUPPORT_ROOT / "pi-extensions").symlink_to(outside, target_is_directory=True)
        installer.SETTINGS.parent.mkdir(parents=True, exist_ok=True)
        installer.SETTINGS.write_text(json.dumps({"packages": []}), encoding="utf-8")

        with self.assertRaises(SystemExit) as raised:
            installer.main()

        self.assertIn("symlinked component", str(raised.exception))
        self.assertEqual(self.snapshot_tree(outside), {})
        self.assertFalse(installer.TRANSACTION_MARKER.exists())
        self.assertEqual(self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT / "pi-extensions"), {})

    def test_rejects_symlinked_settings_parent_before_read_or_write(self):
        installer = self.installer
        outside = Path(self.home.name) / "outside-settings"
        outside.mkdir()
        installer.SETTINGS.parent.parent.mkdir(parents=True, exist_ok=True)
        installer.SETTINGS.parent.symlink_to(outside, target_is_directory=True)
        (outside / "settings.json").write_text(json.dumps({"packages": []}), encoding="utf-8")

        with self.assertRaises(SystemExit) as raised:
            installer.main()

        self.assertIn("symlinked component", str(raised.exception))
        self.assertEqual(self.snapshot_tree(outside), {
            "settings.json": ("file", json.dumps({"packages": []}).encode("utf-8")),
        })
        self.assertFalse(installer.TRANSACTION_MARKER.exists())

    def test_rejects_stale_generated_client(self):
        installer = self.installer
        with tempfile.TemporaryDirectory() as tmp:
            stale = Path(tmp) / "mailbox-client.js"
            stale.write_bytes(installer.GENERATED_CLIENT_SOURCE.read_bytes() + b"\n// stale\n")
            with mock.patch.object(installer, "GENERATED_CLIENT_SOURCE", stale):
                with self.assertRaises(RuntimeError) as raised:
                    installer.validate_generated_client()
            self.assertIn("generated mailbox-client.js is stale", str(raised.exception))

    def test_rejects_malformed_or_unsafe_transaction_marker_without_mutation(self):
        self.seed_old_install()
        installer = self.installer
        old_support = self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT)
        old_settings = installer.SETTINGS.read_bytes()

        installer.TRANSACTION_MARKER.parent.mkdir(parents=True, exist_ok=True)
        installer.TRANSACTION_MARKER.write_text('{"version": 1}', encoding="utf-8")
        marker_before = installer.TRANSACTION_MARKER.read_bytes()
        with self.assertRaises(RuntimeError):
            installer.recover_transaction()
        self.assertEqual(self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT), old_support)
        self.assertEqual(installer.SETTINGS.read_bytes(), old_settings)
        self.assertEqual(installer.TRANSACTION_MARKER.read_bytes(), marker_before)

        unsafe_marker = {
            "version": 2,
            "phase": "recorded",
            "support_backup": str(installer.SUPPORT_BACKUP),
            "settings_backup": str(installer.SETTINGS_BACKUP),
            "staged_support_root": str(installer.INSTALLED_SUPPORT_ROOT.parent.parent / "unsafe-stage"),
            "had_support_root": True,
            "had_settings": True,
            "intended_settings": {"packages": [str(path) for path in installer.CHECKOUT_PACKAGES]},
            "manifest": [str(target.relative_to(installer.INSTALLED_SUPPORT_ROOT)) for _, target in installer.extension_artifacts()],
            "registered_packages": [str(path) for path in installer.CHECKOUT_PACKAGES],
        }
        installer.TRANSACTION_MARKER.write_text(json.dumps(unsafe_marker), encoding="utf-8")
        marker_before = installer.TRANSACTION_MARKER.read_bytes()
        with self.assertRaises(RuntimeError):
            installer.recover_transaction()
        self.assertEqual(self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT), old_support)
        self.assertEqual(installer.SETTINGS.read_bytes(), old_settings)
        self.assertEqual(installer.TRANSACTION_MARKER.read_bytes(), marker_before)

    def test_recovery_runs_before_source_validation(self):
        self.seed_old_install()
        installer = self.installer
        old_support = self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT)
        old_settings = installer.SETTINGS.read_bytes()
        os.environ["MECHA_MESSAGING_INSTALL_FAIL_PHASE"] = "after_recording"
        with self.assertRaises(SystemExit):
            installer.main()
        os.environ.pop("MECHA_MESSAGING_INSTALL_FAIL_PHASE")

        # A broken checkout manifest must not prevent recovery of an interrupted
        # transaction. Recovery happens before manifest validation in main().
        recovered = self._load_installer()
        missing_source = recovered.CHECKOUT_PACKAGE / "missing-generated-client.js"
        original_artifacts = recovered.extension_artifacts
        recovered.extension_artifacts = lambda: (
            (missing_source, recovered.INSTALLED_SUPPORT_ROOT / "pi-extensions" / "mecha-messaging" / "mailbox-client.js"),
        )
        with self.assertRaises(SystemExit):
            recovered.main()
        recovered.extension_artifacts = original_artifacts
        self.assertEqual(self.snapshot_tree(installer.INSTALLED_SUPPORT_ROOT), old_support)
        self.assertEqual(installer.SETTINGS.read_bytes(), old_settings)


if __name__ == "__main__":
    unittest.main()
