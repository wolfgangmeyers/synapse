#!/usr/bin/env python3
"""Install/register the stable Mailbox V2 Pi extension bundle.

Run from the mecha-developer repo root:
    python3 .claude/skills/supervisor/tools/install_pi_extension.py

Reload or restart Pi sessions after install.
"""

import ast
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

REPOSITORY_ROOT = Path(__file__).resolve().parents[4]
GOAL_PACKAGE = REPOSITORY_ROOT / "pi-extensions" / "mecha-goal"
CHECKOUT_PACKAGE = REPOSITORY_ROOT / "pi-extensions" / "mecha-messaging"
CHECKOUT_PACKAGES = (GOAL_PACKAGE, CHECKOUT_PACKAGE)
GENERATED_CLIENT_SOURCE = CHECKOUT_PACKAGE / "mailbox-client.js"
SUPERVISOR_TOOLS_SOURCE = Path(__file__).resolve().parent
INSTALLED_SUPPORT_ROOT = Path.home() / ".mecha-developer" / "extensions"
SETTINGS = Path.home() / ".pi" / "agent" / "settings.json"
# Kept as a named legacy path so migration tests can prove that it is neither
# copied nor registered. The active entrypoint is CHECKOUT_PACKAGE.
SOURCE = REPOSITORY_ROOT / "pi-extensions" / "mecha-messaging.js"
TARGET = INSTALLED_SUPPORT_ROOT / "mecha-messaging.js"
TRANSACTION_MARKER = INSTALLED_SUPPORT_ROOT.with_name("extensions.mecha-messaging.transaction.json")
SUPPORT_BACKUP = INSTALLED_SUPPORT_ROOT.with_name("extensions.mecha-messaging.rollback")
SETTINGS_BACKUP = SETTINGS.with_name(SETTINGS.name + ".mecha-messaging.rollback")
EXTENSION_MODULES = ("goal.js", "mailbox-client.js", "memory-capture.js", "operational-tools.js", "recall-state.js", "recall-controller.js", "recall-evaluator.js", "recall-pipeline.js", "reminder-lock.js")
OPERATIONAL_TOOL_SCRIPTS = (
    "git_task_status.py",
    "mailbox_send.py",
    "pr_status.py",
    "state_add.py",
    "state_priority.py",
    "state_query.py",
    "state_update_field.py",
    "state_update_status.py",
    "state_verification_handoff.py",
    "worker_kill.py",
    "worker_launch.py",
    "worker_lifecycle.py",
    "worker_list.py",
)
OPERATIONAL_TOOL_MODULES = (
    "_cluster.py",
    "_sidecar.py",
    "_state.py",
    "_tmux.py",
    "_tmux_rest.py",
    "worker_runtime_config.py",
)
SUPERVISOR_TOOL_JAVASCRIPT = ("claude_sidecar.mjs", "activity_tail.mjs", "activity_state.mjs")
SIDECAR_HARNESS_MODULES = ("cli_entrypoint.js", "log_rotation.js", "tmux-rest-client.js")
REGISTERED_EXTENSIONS = CHECKOUT_PACKAGES
LEGACY_FLAT_REGISTRATION_NAMES = frozenset({"goal.js", "mecha-messaging.js"})
LEGACY_CHECKOUT_REGISTRATIONS = (SOURCE, REPOSITORY_ROOT / "pi-extensions" / "goal.js")
LEGACY_INSTALLED_REGISTRATIONS = (TARGET, INSTALLED_SUPPORT_ROOT / "goal.js")
LEGACY_SUPPORT_FILES = tuple(EXTENSION_MODULES) + (
    "pi-extensions/mailbox-client.js",
    "pi-extensions/mecha-messaging.js",
    "pi-extensions/goal.js",
)
PYTHON_STANDARD_MODULES = frozenset({
    "__future__",
    "argparse",
    "contextlib",
    "dataclasses",
    "datetime",
    "errno",
    "fcntl",
    "json",
    "os",
    "pathlib",
    "re",
    "secrets",
    "shlex",
    "signal",
    "sqlite3",
    "subprocess",
    "sys",
    "tempfile",
    "time",
    "typing",
    "urllib",
    "uuid",
})
NODE_BUILTIN_MODULES = frozenset({
    "node:child_process",
    "node:crypto",
    "node:fs",
    "node:path",
    "node:process",
    "node:url",
})
# Pi resolves this optional peer from its registered package extensions. It is
# intentionally not copied into Mailbox's self-contained operational bundle.
PI_EXTENSION_RUNTIME_PACKAGES = frozenset({"@gotgenes/pi-subagents"})
MAILBOX_CLIENT_GENERATOR = ("npm", "run", "generate:mailbox-client")


def extension_artifacts() -> tuple[tuple[Path, Path], ...]:
    """Return only support files copied under the installed support root.

    Both TypeScript packages stay in the checkout as the settings registrations.
    Only the generated client crosses this installation boundary.
    """
    installed_supervisor_tools = INSTALLED_SUPPORT_ROOT / ".claude" / "skills" / "supervisor" / "tools"
    return (
        (GENERATED_CLIENT_SOURCE, INSTALLED_SUPPORT_ROOT / "pi-extensions" / "mecha-messaging" / "mailbox-client.js"),
        *((SUPERVISOR_TOOLS_SOURCE / name, installed_supervisor_tools / name)
          for name in (*OPERATIONAL_TOOL_SCRIPTS, *OPERATIONAL_TOOL_MODULES, *SUPERVISOR_TOOL_JAVASCRIPT)),
        *((REPOSITORY_ROOT / "harness" / name, INSTALLED_SUPPORT_ROOT / "harness" / name)
          for name in SIDECAR_HARNESS_MODULES),
    )


def python_imports(source: Path) -> set[str]:
    """Return every top-level module named by a Python import."""
    tree = ast.parse(source.read_text(encoding="utf-8"), filename=str(source))
    imported = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            imported.update(alias.name.split(".", 1)[0] for alias in node.names)
        elif isinstance(node, ast.ImportFrom) and node.module:
            imported.add(node.module.split(".", 1)[0])
    return imported


def missing_operational_dependencies() -> set[str]:
    """Find Python imports unavailable from the explicit clean-target bundle."""
    declared = set(OPERATIONAL_TOOL_SCRIPTS) | set(OPERATIONAL_TOOL_MODULES)
    declared_modules = {Path(name).stem for name in declared}
    return {
        f"{dependency}.py"
        for name in declared
        for dependency in python_imports(SUPERVISOR_TOOLS_SOURCE / name)
        if dependency not in declared_modules and dependency not in PYTHON_STANDARD_MODULES
    }


def _decode_javascript_string(raw: str) -> str:
    """Decode the escapes allowed in a JavaScript module specifier string."""
    quote = raw[0]
    if len(raw) < 2 or raw[-1] != quote:
        raise ValueError("unterminated JavaScript string")
    result = []
    index = 1
    escapes = {"b": "\b", "f": "\f", "n": "\n", "r": "\r", "t": "\t", "v": "\v", "0": "\0"}
    while index < len(raw) - 1:
        character = raw[index]
        if character != "\\":
            result.append(character)
            index += 1
            continue
        index += 1
        if index >= len(raw) - 1:
            raise ValueError("unterminated JavaScript escape")
        escaped = raw[index]
        if escaped in "\r\n":
            if escaped == "\r" and index + 1 < len(raw) - 1 and raw[index + 1] == "\n":
                index += 1
            index += 1
            continue
        if escaped == "x":
            digits = raw[index + 1:index + 3]
            if len(digits) != 2:
                raise ValueError("invalid JavaScript hexadecimal escape")
            result.append(chr(int(digits, 16)))
            index += 3
            continue
        if escaped == "u":
            if index + 1 < len(raw) - 1 and raw[index + 1] == "{":
                close = raw.find("}", index + 2, len(raw) - 1)
                if close < 0:
                    raise ValueError("invalid JavaScript Unicode escape")
                result.append(chr(int(raw[index + 2:close], 16)))
                index = close + 1
                continue
            digits = raw[index + 1:index + 5]
            if len(digits) != 4:
                raise ValueError("invalid JavaScript Unicode escape")
            result.append(chr(int(digits, 16)))
            index += 5
            continue
        result.append(escapes.get(escaped, escaped))
        index += 1
    return "".join(result)


def javascript_tokens(text: str) -> list[tuple[str, str, int]]:
    """Lex enough JavaScript to identify module syntax without reading comments or literals."""
    tokens: list[tuple[str, str, int]] = []
    length = len(text)

    def regex_can_start() -> bool:
        if not tokens:
            return True
        kind, value, _ = tokens[-1]
        return ((kind == "punct" and value in "([{,:;=!?&|+-*%^~<>")
                or (kind == "identifier" and value in {"await", "case", "delete", "do", "else", "in", "instanceof", "new", "return", "throw", "typeof", "void", "yield"}))

    def scan(start: int, stop_at_template_brace: bool = False) -> int:
        index = start
        brace_depth = 0
        while index < length:
            character = text[index]
            if character.isspace():
                index += 1
                continue
            if text.startswith("//", index):
                newline = text.find("\n", index + 2)
                index = length if newline < 0 else newline + 1
                continue
            if text.startswith("/*", index):
                close = text.find("*/", index + 2)
                if close < 0:
                    raise ValueError("unterminated JavaScript block comment")
                index = close + 2
                continue
            if character in {'"', "'"}:
                quote = character
                end = index + 1
                while end < length:
                    if text[end] == "\\":
                        end += 2
                        continue
                    if text[end] == quote:
                        end += 1
                        break
                    if text[end] in "\r\n":
                        raise ValueError("unterminated JavaScript string")
                    end += 1
                else:
                    raise ValueError("unterminated JavaScript string")
                tokens.append(("string", _decode_javascript_string(text[index:end]), index))
                index = end
                continue
            if character == "`":
                index += 1
                while index < length:
                    if text[index] == "\\":
                        index += 2
                        continue
                    if text[index] == "`":
                        index += 1
                        break
                    if text.startswith("${", index):
                        index = scan(index + 2, True)
                        continue
                    index += 1
                else:
                    raise ValueError("unterminated JavaScript template literal")
                continue
            if text.startswith("/", index) and not text.startswith(("//", "/*"), index) and regex_can_start():
                end = index + 1
                in_class = False
                while end < length:
                    if text[end] == "\\":
                        end += 2
                        continue
                    if text[end] == "[":
                        in_class = True
                    elif text[end] == "]":
                        in_class = False
                    elif text[end] == "/" and not in_class:
                        end += 1
                        while end < length and (text[end].isalpha() or text[end].isdigit()):
                            end += 1
                        break
                    elif text[end] in "\r\n":
                        raise ValueError("unterminated JavaScript regular expression")
                    end += 1
                else:
                    raise ValueError("unterminated JavaScript regular expression")
                tokens.append(("regex", "", index))
                index = end
                continue
            if character.isalpha() or character in "_$":
                end = index + 1
                while end < length and (text[end].isalnum() or text[end] in "_$"):
                    end += 1
                tokens.append(("identifier", text[index:end], index))
                index = end
                continue
            if character == "{":
                brace_depth += 1
            elif character == "}":
                if stop_at_template_brace and brace_depth == 0:
                    return index + 1
                brace_depth -= 1
            tokens.append(("punct", character, index))
            index += 1
        if stop_at_template_brace:
            raise ValueError("unterminated JavaScript template expression")
        return index

    scan(0)
    return tokens


def javascript_module_references(source: Path) -> set[str]:
    """Return static imports, re-exports, and literal one-argument dynamic imports."""
    tokens = javascript_tokens(source.read_text(encoding="utf-8"))
    references = set()
    for index, (kind, value, position) in enumerate(tokens):
        if kind != "identifier" or value not in {"import", "export"}:
            continue
        following = tokens[index + 1:]
        if value == "import" and following and following[0][:2] == ("punct", "."):
            continue
        if value == "import" and following and following[0][:2] == ("punct", "("):
            if len(following) < 3 or following[1][0] != "string" or following[2][:2] != ("punct", ")"):
                raise ValueError(f"unsupported dynamic import at offset {position}")
            references.add(following[1][1])
            continue
        if value == "import" and following and following[0][0] == "string":
            references.add(following[0][1])
            continue
        if value == "export" and (not following or following[0][:2] not in {
                ("punct", "{"), ("punct", "*")}):
            continue
        for candidate_index, candidate in enumerate(following):
            if candidate[:2] == ("punct", ";"):
                break
            if candidate[:2] == ("identifier", "from"):
                if candidate_index + 1 >= len(following) or following[candidate_index + 1][0] != "string":
                    raise ValueError(f"module reference without a string at offset {position}")
                references.add(following[candidate_index + 1][1])
                break
    return references


def missing_javascript_dependencies(artifacts: tuple[tuple[Path, Path], ...]) -> set[str]:
    """Find module references unavailable from a clean installed target."""
    targets = {target.resolve() for _, target in artifacts}
    missing = set()
    for source, target in artifacts:
        if source.suffix not in {".js", ".mjs"}:
            continue
        for reference in javascript_module_references(source):
            if reference in NODE_BUILTIN_MODULES or reference in PI_EXTENSION_RUNTIME_PACKAGES:
                continue
            if not reference.startswith(".") or "?" in reference or "#" in reference:
                missing.add(f"{source.name} -> {reference}")
                continue
            dependency_target = (target.parent / reference).resolve()
            # Consumers retain their checkout-relative imports. The target path
            # is the authoritative graph after they are copied into support root.
            if dependency_target not in targets:
                missing.add(f"{source.name} -> {reference}")
    return missing


def is_regular_file(path: Path) -> bool:
    return path.is_file() and not path.is_symlink()


def validate_generated_client() -> None:
    """Reject a checked-in client that differs from the package generator output."""
    if not is_regular_file(CHECKOUT_PACKAGE / "mailbox-client.ts"):
        raise RuntimeError("mailbox-client.ts is missing")
    if not is_regular_file(GENERATED_CLIENT_SOURCE):
        raise RuntimeError("generated mailbox-client.js is missing")
    with tempfile.TemporaryDirectory(prefix=".mailbox-client-generate-") as output:
        result = subprocess.run(
            [*MAILBOX_CLIENT_GENERATOR, "--", "--outDir", output],
            cwd=CHECKOUT_PACKAGE,
            capture_output=True,
            text=True,
            check=False,
        )
        if result.returncode != 0:
            detail = (result.stderr or result.stdout).strip()
            raise RuntimeError(f"mailbox-client generator failed: {detail}")
        generated = Path(output) / "mailbox-client.js"
        if not is_regular_file(generated):
            raise RuntimeError("mailbox-client generator did not produce mailbox-client.js")
        if generated.read_bytes() != GENERATED_CLIENT_SOURCE.read_bytes():
            raise RuntimeError(
                "generated mailbox-client.js is stale; run "
                "npm run generate:mailbox-client in pi-extensions/mecha-messaging"
            )


def assert_no_symlinked_components(path: Path, base: Path | None = None) -> None:
    """Reject symlinks from a managed base through an absolute path."""
    absolute = Path(os.path.abspath(path))
    managed_base = Path(os.path.abspath(base or path))
    try:
        relative = absolute.relative_to(managed_base)
    except ValueError as error:
        raise RuntimeError(f"managed support path escapes its base: {path}") from error
    current = managed_base
    if current.is_symlink():
        raise RuntimeError(f"managed support path contains a symlinked component: {current}")
    for component in relative.parts:
        current /= component
        if current.is_symlink():
            raise RuntimeError(f"managed support path contains a symlinked component: {current}")


def fsync_directory(path: Path) -> None:
    fd = os.open(path, os.O_RDONLY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def fsync_tree(path: Path) -> None:
    if path.is_symlink():
        return
    if path.is_dir():
        for child in path.iterdir():
            fsync_tree(child)
        fsync_directory(path)
    elif path.is_file():
        fd = os.open(path, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)


def remove_path(path: Path) -> None:
    if path.is_symlink() or path.is_file():
        path.unlink()
    elif path.is_dir():
        shutil.rmtree(path)


def atomic_copy(source: Path, target: Path, prefix: str) -> None:
    target.parent.mkdir(parents=True, exist_ok=True)
    fd, staged_name = tempfile.mkstemp(prefix=prefix, dir=target.parent)
    os.close(fd)
    staged = Path(staged_name)
    try:
        shutil.copy2(source, staged)
        if not is_regular_file(staged) or staged.read_bytes() != source.read_bytes():
            raise RuntimeError(f"staged copy validation failed: {source}")
        fd = os.open(staged, os.O_RDONLY)
        try:
            os.fsync(fd)
        finally:
            os.close(fd)
        os.replace(staged, target)
        fsync_directory(target.parent)
    except Exception:
        staged.unlink(missing_ok=True)
        raise


def atomic_write_json(path: Path, data: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, staged_name = tempfile.mkstemp(prefix=".settings.", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as staged:
            json.dump(data, staged, indent=2)
            staged.write("\n")
            staged.flush()
            os.fsync(staged.fileno())
        os.replace(staged_name, path)
        fsync_directory(path.parent)
    except Exception:
        Path(staged_name).unlink(missing_ok=True)
        raise


def load_settings() -> tuple[dict, bool]:
    if not SETTINGS.exists() and not SETTINGS.is_symlink():
        return {}, False
    if not is_regular_file(SETTINGS):
        raise ValueError(f"Pi settings must be a regular, non-symlink file: {SETTINGS}")
    try:
        data = json.loads(SETTINGS.read_text(encoding="utf-8"))
    except json.JSONDecodeError as error:
        raise ValueError(f"invalid Pi settings; refusing mutation: {error}") from error
    if not isinstance(data, dict) or not isinstance(data.get("packages", []), list):
        raise ValueError("Pi settings packages must be a list")
    return data, True


def _resolved_registration(value: object) -> Path | None:
    if not isinstance(value, str):
        return None
    path = Path(value)
    if not path.is_absolute():
        path = SETTINGS.parent / path
    return path.resolve()


def _is_legacy_flat_registration(value: object) -> bool:
    """Recognize old flat Goal/Messaging entrypoints wherever they were installed."""
    if not isinstance(value, str):
        return False
    path = Path(value)
    if not path.is_absolute():
        path = SETTINGS.parent / path
    return path.name in LEGACY_FLAT_REGISTRATION_NAMES or path.resolve().name in LEGACY_FLAT_REGISTRATION_NAMES


def build_settings(settings: dict) -> dict:
    """Replace old and duplicate registrations while retaining other packages."""
    retired = {
        path.resolve()
        for path in (*LEGACY_CHECKOUT_REGISTRATIONS, *LEGACY_INSTALLED_REGISTRATIONS)
    }
    registered = {path.resolve() for path in CHECKOUT_PACKAGES}
    packages = [
        entry for entry in settings.get("packages", [])
        if not _is_legacy_flat_registration(entry)
        and _resolved_registration(entry) not in retired | registered
    ]
    result = dict(settings)
    result["packages"] = [*packages, *(str(path) for path in CHECKOUT_PACKAGES)]
    return result


def verify_manifest(artifacts: tuple[tuple[Path, Path], ...], root: Path) -> None:
    """Check staged support files before they can become visible."""
    for source, target in artifacts:
        relative = target.relative_to(INSTALLED_SUPPORT_ROOT)
        staged = root / relative
        if not is_regular_file(source) or not is_regular_file(staged):
            raise RuntimeError(f"support manifest contains a non-file: {relative}")
        if staged.read_bytes() != source.read_bytes():
            raise RuntimeError(f"support manifest mismatch: {relative}")


def stage_support_root(artifacts: tuple[tuple[Path, Path], ...]) -> Path:
    parent = INSTALLED_SUPPORT_ROOT.parent
    assert_no_symlinked_components(parent)
    for _, target in artifacts:
        assert_no_symlinked_components(target.parent, base=parent)
    parent.mkdir(parents=True, exist_ok=True)
    staged = Path(tempfile.mkdtemp(prefix=".mecha-messaging-stage-", dir=parent))
    try:
        if INSTALLED_SUPPORT_ROOT.exists() or INSTALLED_SUPPORT_ROOT.is_symlink():
            if not INSTALLED_SUPPORT_ROOT.is_dir() or INSTALLED_SUPPORT_ROOT.is_symlink():
                raise RuntimeError(f"installed support root must be a directory: {INSTALLED_SUPPORT_ROOT}")
            shutil.copytree(INSTALLED_SUPPORT_ROOT, staged, dirs_exist_ok=True, symlinks=True)
        for relative in LEGACY_SUPPORT_FILES:
            remove_path(staged / relative)
        for source, target in artifacts:
            relative = target.relative_to(INSTALLED_SUPPORT_ROOT)
            assert_no_symlinked_components((staged / relative).parent, base=staged)
            atomic_copy(source, staged / relative, ".mecha-messaging-stage-file.")
        verify_manifest(artifacts, staged)
        fsync_tree(staged)
        fsync_directory(parent)
        return staged
    except Exception:
        remove_path(staged)
        raise


def copy_support_backup(had_support: bool) -> None:
    if not had_support:
        return
    shutil.copytree(INSTALLED_SUPPORT_ROOT, SUPPORT_BACKUP, symlinks=True)
    fsync_tree(SUPPORT_BACKUP)
    fsync_directory(SUPPORT_BACKUP.parent)


def failure_injection(point: str) -> None:
    if os.environ.get("MECHA_MESSAGING_INSTALL_FAIL_PHASE") == point:
        raise RuntimeError(f"injected installer failure: {point}")


def write_transaction(marker: dict) -> None:
    atomic_write_json(TRANSACTION_MARKER, marker)


def _marker_data() -> dict:
    if not is_regular_file(TRANSACTION_MARKER):
        raise RuntimeError(f"invalid installer transaction marker: {TRANSACTION_MARKER}")
    try:
        marker = json.loads(TRANSACTION_MARKER.read_text(encoding="utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise RuntimeError(f"invalid installer transaction marker: {error}") from error
    base_fields = {
        "version", "phase", "support_backup", "settings_backup", "staged_support_root",
        "had_support_root", "had_settings", "intended_settings", "manifest",
    }
    version = marker.get("version") if isinstance(marker, dict) else None
    required = base_fields | ({"registered_packages"} if version == 2 else set())
    if not isinstance(marker, dict) or set(marker) != required:
        raise RuntimeError("invalid installer transaction marker schema")
    if type(version) is not int or version not in {1, 2}:
        raise RuntimeError("unsupported installer transaction marker version")
    if not isinstance(marker["phase"], str) or marker["phase"] not in {"recorded", "support_replaced", "settings_replaced", "completed", "restored"}:
        raise RuntimeError("invalid installer transaction marker phase")
    if any(not isinstance(marker[field], str) for field in ("support_backup", "settings_backup", "staged_support_root")):
        raise RuntimeError("invalid installer transaction marker paths")
    if marker["support_backup"] != str(SUPPORT_BACKUP) or marker["settings_backup"] != str(SETTINGS_BACKUP):
        raise RuntimeError("installer transaction marker backup path is unsafe")
    if type(marker["had_support_root"]) is not bool or type(marker["had_settings"]) is not bool:
        raise RuntimeError("invalid installer transaction marker state")
    if not isinstance(marker["intended_settings"], dict) or not isinstance(marker["manifest"], list):
        raise RuntimeError("invalid installer transaction marker payload")
    if version == 2:
        expected_packages = [str(path) for path in CHECKOUT_PACKAGES]
        if marker["registered_packages"] != expected_packages or any(not isinstance(item, str) for item in marker["registered_packages"]):
            raise RuntimeError("invalid installer transaction package registrations")
        intended_packages = marker["intended_settings"].get("packages")
        if (
            not isinstance(intended_packages, list)
            or intended_packages[-len(expected_packages):] != expected_packages
            or any(intended_packages.count(package) != 1 for package in expected_packages)
        ):
            raise RuntimeError("invalid installer transaction settings registrations")
    expected_manifest = [str(target.relative_to(INSTALLED_SUPPORT_ROOT)) for _, target in extension_artifacts()]
    if marker["manifest"] != expected_manifest or any(not isinstance(item, str) for item in marker["manifest"]):
        raise RuntimeError("invalid installer transaction marker manifest")

    staged = Path(marker["staged_support_root"])
    parent = INSTALLED_SUPPORT_ROOT.parent.resolve(strict=False)
    staged_resolved = staged.resolve(strict=False)
    if not staged.is_absolute():
        raise RuntimeError("installer transaction marker staged path is unsafe")
    try:
        relative = staged_resolved.relative_to(parent)
    except ValueError as error:
        raise RuntimeError("installer transaction marker staged path escapes its directory") from error
    if len(relative.parts) != 1 or not staged.name.startswith(".mecha-messaging-stage-"):
        raise RuntimeError("installer transaction marker staged path is unsafe")
    assert_no_symlinked_components(staged_resolved, base=parent)
    if staged.exists() and (not staged.is_dir() or staged.is_symlink()):
        raise RuntimeError("installer transaction marker staged path is not a directory")
    return marker


def clear_transaction(marker: dict) -> None:
    staged = Path(marker.get("staged_support_root", ""))
    if staged and staged != Path(".") and (staged.exists() or staged.is_symlink()):
        remove_path(staged)
    if SUPPORT_BACKUP.exists() or SUPPORT_BACKUP.is_symlink():
        remove_path(SUPPORT_BACKUP)
    SETTINGS_BACKUP.unlink(missing_ok=True)
    # Keep the marker until both rollback copies are gone so cleanup can resume
    # safely if the process stops partway through it.
    TRANSACTION_MARKER.unlink(missing_ok=True)
    fsync_directory(TRANSACTION_MARKER.parent)
    fsync_directory(SETTINGS.parent)


def restore_support_backup() -> None:
    """Restore a copy while retaining the rollback source for the next phase."""
    restored = Path(tempfile.mkdtemp(prefix=".mecha-messaging-recover-", dir=INSTALLED_SUPPORT_ROOT.parent))
    remove_path(restored)
    try:
        shutil.copytree(SUPPORT_BACKUP, restored, symlinks=True)
        fsync_tree(restored)
        fsync_directory(restored.parent)
        replace_support_root(restored)
    except Exception:
        if restored.exists() or restored.is_symlink():
            remove_path(restored)
        raise


def restore_transaction(marker: dict) -> None:
    """Restore both complete old roots before a retry does any new work."""
    had_support = bool(marker.get("had_support_root"))
    had_settings = bool(marker.get("had_settings"))
    if had_support and (SUPPORT_BACKUP.is_symlink() or not SUPPORT_BACKUP.is_dir()):
        raise RuntimeError("support-root backup is missing")
    if had_settings and not is_regular_file(SETTINGS_BACKUP):
        raise RuntimeError("settings backup is missing")
    assert_no_symlinked_components(INSTALLED_SUPPORT_ROOT.parent)
    if had_support:
        restore_support_backup()
    elif INSTALLED_SUPPORT_ROOT.exists() or INSTALLED_SUPPORT_ROOT.is_symlink():
        remove_path(INSTALLED_SUPPORT_ROOT)
    # The support backup remains in place until the settings restore succeeds.
    failure_injection("during_recovery_after_support")
    if had_settings:
        atomic_copy(SETTINGS_BACKUP, SETTINGS, ".settings-recover.")
    else:
        SETTINGS.unlink(missing_ok=True)
    fsync_directory(INSTALLED_SUPPORT_ROOT.parent)
    fsync_directory(SETTINGS.parent)
    marker["phase"] = "restored"
    write_transaction(marker)
    clear_transaction(marker)


def recover_transaction() -> None:
    # Keep the recovery entrypoint safe when called directly as well as through
    # main(), before it reads or restores settings.
    assert_no_symlinked_components(SETTINGS.parent, base=Path.home())
    if not TRANSACTION_MARKER.exists() and not TRANSACTION_MARKER.is_symlink():
        if SUPPORT_BACKUP.exists() or SETTINGS_BACKUP.exists():
            raise RuntimeError("installer rollback copies exist without a transaction marker")
        return
    marker = _marker_data()
    if marker.get("phase") in {"completed", "restored"}:
        clear_transaction(marker)
    else:
        restore_transaction(marker)


def replace_support_root(staged: Path) -> None:
    assert_no_symlinked_components(INSTALLED_SUPPORT_ROOT.parent)
    displaced = Path(tempfile.mkdtemp(prefix=".mecha-messaging-old-", dir=INSTALLED_SUPPORT_ROOT.parent))
    remove_path(displaced)
    had_root = INSTALLED_SUPPORT_ROOT.exists() or INSTALLED_SUPPORT_ROOT.is_symlink()
    try:
        if had_root:
            os.replace(INSTALLED_SUPPORT_ROOT, displaced)
        os.replace(staged, INSTALLED_SUPPORT_ROOT)
        fsync_directory(INSTALLED_SUPPORT_ROOT.parent)
    except Exception:
        if INSTALLED_SUPPORT_ROOT.exists() or INSTALLED_SUPPORT_ROOT.is_symlink():
            remove_path(INSTALLED_SUPPORT_ROOT)
        if had_root and displaced.exists():
            os.replace(displaced, INSTALLED_SUPPORT_ROOT)
        raise
    finally:
        if displaced.exists() or displaced.is_symlink():
            remove_path(displaced)


def validate_checkout_packages() -> None:
    """Require both registered package sources to be real checkout directories."""
    for package in CHECKOUT_PACKAGES:
        if not package.is_dir() or package.is_symlink():
            raise RuntimeError(f"checkout package must be a directory, not a symlink: {package}")


def main() -> int:
    try:
        # Settings writes must stay below the real home directory. Check this
        # before recovery or loading settings so a symlinked parent cannot
        # redirect either operation outside the managed path.
        assert_no_symlinked_components(SETTINGS.parent, base=Path.home())
        recover_transaction()
    except Exception as error:
        raise SystemExit(f"Installer recovery failed; refusing new work: {error}") from error
    try:
        validate_checkout_packages()
    except OSError as error:
        raise SystemExit(f"Invalid canonical extension package: {error}") from error
    except RuntimeError as error:
        raise SystemExit(f"Invalid canonical extension package: {error}") from error
    artifacts = extension_artifacts()
    if any(not is_regular_file(source) for source, _ in artifacts):
        raise SystemExit("Invalid canonical extension bundle source")
    try:
        validate_generated_client()
        missing_dependencies = missing_operational_dependencies()
        missing_javascript = missing_javascript_dependencies(artifacts)
    except (OSError, RuntimeError, SyntaxError, ValueError) as error:
        raise SystemExit(f"Invalid extension bundle source: {error}") from error
    if missing_dependencies or missing_javascript:
        missing = ", ".join(sorted(missing_dependencies | missing_javascript))
        raise SystemExit(f"Extension bundle is missing local dependencies: {missing}")
    try:
        settings, had_settings = load_settings()
    except ValueError as error:
        raise SystemExit(f"Error: {error}") from error
    intended_settings = build_settings(settings)
    # Recovery must be able to fsync this directory even when the first install
    # creates settings from scratch.
    SETTINGS.parent.mkdir(parents=True, exist_ok=True)
    had_support = INSTALLED_SUPPORT_ROOT.exists() or INSTALLED_SUPPORT_ROOT.is_symlink()
    if had_support and (not INSTALLED_SUPPORT_ROOT.is_dir() or INSTALLED_SUPPORT_ROOT.is_symlink()):
        raise SystemExit("Installed support root must be a regular directory")
    try:
        assert_no_symlinked_components(INSTALLED_SUPPORT_ROOT.parent)
    except RuntimeError as error:
        raise SystemExit(str(error)) from error
    try:
        staged = stage_support_root(artifacts)
        copy_support_backup(had_support)
        if had_settings:
            atomic_copy(SETTINGS, SETTINGS_BACKUP, ".settings-backup.")
        marker = {
            "version": 2,
            "phase": "recorded",
            "support_backup": str(SUPPORT_BACKUP),
            "settings_backup": str(SETTINGS_BACKUP),
            "staged_support_root": str(staged),
            "had_support_root": had_support,
            "had_settings": had_settings,
            "intended_settings": intended_settings,
            "manifest": [str(target.relative_to(INSTALLED_SUPPORT_ROOT)) for _, target in artifacts],
            "registered_packages": [str(path) for path in CHECKOUT_PACKAGES],
        }
        write_transaction(marker)
    except Exception as error:
        if 'staged' in locals() and staged.exists():
            remove_path(staged)
        if SUPPORT_BACKUP.exists() or SUPPORT_BACKUP.is_symlink():
            remove_path(SUPPORT_BACKUP)
        SETTINGS_BACKUP.unlink(missing_ok=True)
        raise SystemExit(f"Pi extension staging failed: {error}") from error
    try:
        failure_injection("after_recording")
        replace_support_root(staged)
        marker["phase"] = "support_replaced"
        write_transaction(marker)
        failure_injection("after_support_replacement")
        atomic_write_json(SETTINGS, intended_settings)
        marker["phase"] = "settings_replaced"
        write_transaction(marker)
        failure_injection("after_settings_replacement")
        verify_manifest(artifacts, INSTALLED_SUPPORT_ROOT)
        verified_settings, _ = load_settings()
        if verified_settings != intended_settings:
            raise RuntimeError("post-install settings verification failed")
        marker["phase"] = "completed"
        write_transaction(marker)
        clear_transaction(marker)
    except Exception as error:
        # Leave the marker and complete backups in place. The next invocation
        # restores them before validating or staging any new bundle.
        raise SystemExit(f"Pi extension install interrupted; retry to recover: {error}") from error

    print(f"Pi extension support installed under {INSTALLED_SUPPORT_ROOT} (source: {CHECKOUT_PACKAGE})")
    print(f"Pi registration: {SETTINGS}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
