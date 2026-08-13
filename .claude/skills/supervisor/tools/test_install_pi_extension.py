#!/usr/bin/env python3
"""Tests for the copied, globally registered Pi extension install transaction."""

import json
from pathlib import Path
import subprocess
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
        for name in install_pi_extension.EXTENSION_MODULES:
            self.source.with_name(name).write_text(f"export const name = {name!r};\n", encoding="utf-8")
        self.supervisor_tools = root / ".claude" / "skills" / "supervisor" / "tools"
        self.supervisor_tools.mkdir(parents=True)
        for name in (
            *install_pi_extension.OPERATIONAL_TOOL_SCRIPTS,
            *install_pi_extension.OPERATIONAL_TOOL_MODULES,
            *install_pi_extension.SUPERVISOR_TOOL_JAVASCRIPT,
        ):
            (self.supervisor_tools / name).write_text(f"# {name}\n", encoding="utf-8")
        harness = root / "harness"
        harness.mkdir()
        for name in install_pi_extension.SIDECAR_HARNESS_MODULES:
            (harness / name).write_text(f"// {name}\n", encoding="utf-8")
        pi_extensions = root / "pi-extensions"
        pi_extensions.mkdir()
        for name in install_pi_extension.SIDECAR_EXTENSION_MODULES:
            (pi_extensions / name).write_text(f"// {name}\n", encoding="utf-8")
        self.paths = patch.multiple(
            install_pi_extension,
            SOURCE=self.source,
            SUPERVISOR_TOOLS_SOURCE=self.supervisor_tools,
            TARGET=self.target,
            REGISTERED_EXTENSIONS=(self.target,),
            SETTINGS=self.settings,
        )
        self.paths.start()
        self.addCleanup(self.paths.stop)

    def test_installs_extension_artifacts_and_deduplicates_their_registrations(self):
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

        for source, target in install_pi_extension.extension_artifacts():
            self.assertEqual(target.read_bytes(), source.read_bytes())
        settings = json.loads(self.settings.read_text(encoding="utf-8"))
        self.assertEqual(settings["packages"], ["npm:other", str(stale), str(self.target)])
        self.assertTrue((self.target.with_name(self.target.name + ".rollback")).is_file())

    def test_rolls_back_every_bundle_file_after_a_partial_install_failure(self):
        self.target.parent.mkdir()
        original_files = {}
        for _, target in install_pi_extension.extension_artifacts():
            original = f"old {target.name}\n"
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_text(original, encoding="utf-8")
            original_files[target] = original
        self.settings.parent.mkdir(parents=True)
        original_settings = '{"packages": ["npm:other"]}\n'
        self.settings.write_text(original_settings, encoding="utf-8")
        copy = install_pi_extension.atomic_copy

        def fail_on_second_bundle_copy(source, target, prefix):
            if source.parent == self.source.parent and source.name == "recall-state.js":
                raise OSError("injected bundle copy failure")
            return copy(source, target, prefix)

        with patch.object(install_pi_extension, "atomic_copy", side_effect=fail_on_second_bundle_copy), self.assertRaises(SystemExit):
            install_pi_extension.main()

        for target, original in original_files.items():
            self.assertEqual(target.read_text(encoding="utf-8"), original)
        self.assertEqual(self.settings.read_text(encoding="utf-8"), original_settings)

    def test_rejects_undeclared_or_unavailable_python_dependencies_without_mutation(self):
        for source_present in (True, False):
            with self.subTest(source_present=source_present):
                (self.supervisor_tools / "state_add.py").write_text(
                    "import omitted_dependency\n", encoding="utf-8")
                omitted = self.supervisor_tools / "omitted_dependency.py"
                if source_present:
                    omitted.write_text("# omitted\n", encoding="utf-8")
                else:
                    omitted.unlink(missing_ok=True)

                with self.assertRaisesRegex(SystemExit, "omitted_dependency.py"):
                    install_pi_extension.main()

                self.assertFalse(self.target.exists())
                self.assertFalse(self.settings.exists())

    def test_proves_static_reexport_and_literal_dynamic_import_closure_from_clean_target(self):
        modules = ("static-fixture.mjs", "reexport-fixture.mjs", "dynamic-fixture.mjs")
        (self.supervisor_tools / "claude_sidecar.mjs").write_text(
            '// import "./comment-only.mjs";\n'
            'const example = `import("./template-text-only.mjs")`;\n'
            'const pattern = /import\\(".\\/regex-only.mjs"\\)/;\n'
            'import "./static-fixture.mjs";\n',
            encoding="utf-8",
        )
        (self.supervisor_tools / modules[0]).write_text(
            'export { value } from "./reexport-fixture.mjs";\n', encoding="utf-8")
        (self.supervisor_tools / modules[1]).write_text(
            'export const value = 1;\nexport const load = () => import("./dynamic-fixture.mjs");\n',
            encoding="utf-8",
        )
        (self.supervisor_tools / modules[2]).write_text("export default 2;\n", encoding="utf-8")

        declared = (*install_pi_extension.SUPERVISOR_TOOL_JAVASCRIPT, *modules)
        with patch.object(install_pi_extension, "SUPERVISOR_TOOL_JAVASCRIPT", declared):
            self.assertEqual(install_pi_extension.main(), 0)
            installed = self.target.parent / ".claude" / "skills" / "supervisor" / "tools"
            subprocess.run(
                ["node", "--input-type=module", "--eval", f'import({str(installed / "claude_sidecar.mjs")!r})'],
                check=True,
                capture_output=True,
                text=True,
            )

    def test_rejects_an_omitted_static_import_without_mutation(self):
        (self.supervisor_tools / "claude_sidecar.mjs").write_text('import "./omitted.mjs";\n', encoding="utf-8")
        (self.supervisor_tools / "omitted.mjs").write_text("// omitted\n", encoding="utf-8")

        with self.assertRaisesRegex(SystemExit, "claude_sidecar.mjs -> ./omitted.mjs"):
            install_pi_extension.main()

        self.assertFalse(self.target.exists())
        self.assertFalse(self.settings.exists())

    def test_rejects_an_omitted_reexport_without_mutation(self):
        (self.supervisor_tools / "claude_sidecar.mjs").write_text(
            'export { value } from "./omitted.mjs";\n', encoding="utf-8")
        (self.supervisor_tools / "omitted.mjs").write_text("export const value = 1;\n", encoding="utf-8")

        with self.assertRaisesRegex(SystemExit, "claude_sidecar.mjs -> ./omitted.mjs"):
            install_pi_extension.main()

        self.assertFalse(self.target.exists())
        self.assertFalse(self.settings.exists())

    def test_rejects_an_omitted_literal_dynamic_import_without_mutation(self):
        (self.supervisor_tools / "claude_sidecar.mjs").write_text(
            'export const load = () => import("./omitted.mjs");\n', encoding="utf-8")
        (self.supervisor_tools / "omitted.mjs").write_text("export default 1;\n", encoding="utf-8")

        with self.assertRaisesRegex(SystemExit, "claude_sidecar.mjs -> ./omitted.mjs"):
            install_pi_extension.main()

        self.assertFalse(self.target.exists())
        self.assertFalse(self.settings.exists())

    def test_rejects_nonliteral_dynamic_and_external_imports_without_mutation(self):
        for source, expected in [
            ('import(moduleName);\n', "unsupported dynamic import"),
            ('import dependency from "not-installed";\n', "claude_sidecar.mjs -> not-installed"),
        ]:
            with self.subTest(source=source):
                (self.supervisor_tools / "claude_sidecar.mjs").write_text(source, encoding="utf-8")
                with self.assertRaisesRegex(SystemExit, expected):
                    install_pi_extension.main()
                self.assertFalse(self.target.exists())
                self.assertFalse(self.settings.exists())

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
