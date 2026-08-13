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
import tempfile

SOURCE = Path(__file__).resolve().parents[4] / "pi-extensions" / "mecha-messaging.js"
SUPERVISOR_TOOLS_SOURCE = Path(__file__).resolve().parent
TARGET = Path.home() / ".mecha-developer" / "extensions" / "mecha-messaging.js"
SETTINGS = Path.home() / ".pi" / "agent" / "settings.json"
EXTENSION_MODULES = ("mailbox-client.js", "memory-capture.js", "operational-tools.js", "recall-state.js", "recall-controller.js", "recall-evaluator.js", "recall-pipeline.js")
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
    "worker_list.py",
)
OPERATIONAL_TOOL_MODULES = ("_cluster.py", "_sidecar.py", "_state.py", "_tmux.py")
SUPERVISOR_TOOL_JAVASCRIPT = ("claude_sidecar.mjs", "activity_tail.mjs", "activity_state.mjs")
SIDECAR_HARNESS_MODULES = ("cli_entrypoint.js", "log_rotation.js")
SIDECAR_EXTENSION_MODULES = ("mailbox-client.js",)
REGISTERED_EXTENSIONS = (TARGET,)
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
    "signal",
    "subprocess",
    "sys",
    "time",
    "typing",
    "urllib",
})
NODE_BUILTIN_MODULES = frozenset({
    "node:child_process",
    "node:crypto",
    "node:fs",
    "node:path",
    "node:process",
    "node:url",
})


def extension_artifacts() -> tuple[tuple[Path, Path], ...]:
    installed_root = TARGET.parent
    installed_supervisor_tools = installed_root / ".claude" / "skills" / "supervisor" / "tools"
    repository_root = SUPERVISOR_TOOLS_SOURCE.parents[3]
    return (
        (SOURCE, TARGET),
        *((SOURCE.with_name(name), TARGET.with_name(name)) for name in EXTENSION_MODULES),
        *((SUPERVISOR_TOOLS_SOURCE / name, installed_supervisor_tools / name)
          for name in (*OPERATIONAL_TOOL_SCRIPTS, *OPERATIONAL_TOOL_MODULES, *SUPERVISOR_TOOL_JAVASCRIPT)),
        *((repository_root / "harness" / name, installed_root / "harness" / name)
          for name in SIDECAR_HARNESS_MODULES),
        *((repository_root / "pi-extensions" / name, installed_root / "pi-extensions" / name)
          for name in SIDECAR_EXTENSION_MODULES),
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
    targets = {(source.resolve(), target.resolve()) for source, target in artifacts}
    missing = set()
    for source, target in artifacts:
        if source.suffix not in {".js", ".mjs"}:
            continue
        for reference in javascript_module_references(source):
            if reference in NODE_BUILTIN_MODULES:
                continue
            if not reference.startswith(".") or "?" in reference or "#" in reference:
                missing.add(f"{source.name} -> {reference}")
                continue
            dependency_source = (source.parent / reference).resolve()
            dependency_target = (target.parent / reference).resolve()
            if (dependency_source, dependency_target) not in targets:
                missing.add(f"{source.name} -> {reference}")
    return missing


def is_regular_file(path: Path) -> bool:
    return path.is_file() and not path.is_symlink()


def atomic_copy(source: Path, target: Path, prefix: str) -> None:
    fd, staged_name = tempfile.mkstemp(prefix=prefix, dir=target.parent)
    os.close(fd)
    staged = Path(staged_name)
    try:
        shutil.copy2(source, staged)
        if not is_regular_file(staged) or staged.read_bytes() != source.read_bytes():
            raise RuntimeError(f"staged copy validation failed: {source}")
        os.replace(staged, target)
    except Exception:
        staged.unlink(missing_ok=True)
        raise


def atomic_write_json(path: Path, data: dict) -> None:
    fd, staged_name = tempfile.mkstemp(prefix=".settings.", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as staged:
            json.dump(data, staged, indent=2)
            staged.write("\n")
            staged.flush()
            os.fsync(staged.fileno())
        os.replace(staged_name, path)
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


def main() -> int:
    artifacts = extension_artifacts()
    if any(not is_regular_file(source) for source, _ in artifacts):
        raise SystemExit("Invalid canonical extension bundle source")
    try:
        missing_dependencies = missing_operational_dependencies()
        missing_javascript = missing_javascript_dependencies(artifacts)
    except (OSError, SyntaxError, ValueError) as error:
        raise SystemExit(f"Invalid extension bundle source: {error}") from error
    if missing_dependencies or missing_javascript:
        missing = ", ".join(sorted(missing_dependencies | missing_javascript))
        raise SystemExit(f"Extension bundle is missing local dependencies: {missing}")
    try:
        settings, had_settings = load_settings()
    except ValueError as error:
        raise SystemExit(f"Error: {error}") from error
    if any((target.exists() or target.is_symlink()) and not is_regular_file(target) for _, target in artifacts):
        raise SystemExit("Installed extension bundle must contain only regular, non-symlink files")

    TARGET.parent.mkdir(parents=True, exist_ok=True)
    for _, target in artifacts:
        target.parent.mkdir(parents=True, exist_ok=True)
    SETTINGS.parent.mkdir(parents=True, exist_ok=True)
    artifact_backups = {target: target.with_name(target.name + ".rollback") for _, target in artifacts}
    settings_backup = SETTINGS.with_name(SETTINGS.name + ".mecha-messaging.rollback")
    if any(path.exists() or path.is_symlink() for path in (*artifact_backups.values(), settings_backup)):
        raise SystemExit("Existing Pi extension rollback copies need verification or recovery before updating")

    had_targets = {target: target.exists() for _, target in artifacts}
    try:
        if had_settings:
            atomic_copy(SETTINGS, settings_backup, ".settings-backup.")
        for _, target in artifacts:
            if had_targets[target]:
                atomic_copy(target, artifact_backups[target], ".mecha-messaging-backup.")
        for source, target in artifacts:
            atomic_copy(source, target, ".mecha-messaging.")
        registered_paths = {str(target) for target in REGISTERED_EXTENSIONS}
        packages = [entry for entry in settings.get("packages", []) if entry not in registered_paths]
        packages.extend(str(target) for target in REGISTERED_EXTENSIONS)
        settings["packages"] = packages
        atomic_write_json(SETTINGS, settings)
        verified_settings, _ = load_settings()
        if (any(not is_regular_file(target) or target.read_bytes() != source.read_bytes() for source, target in artifacts)
                or any(verified_settings["packages"].count(str(target)) != 1 for target in REGISTERED_EXTENSIONS)):
            raise RuntimeError("post-install verification failed")
    except Exception as error:
        if had_settings and settings_backup.exists():
            os.replace(settings_backup, SETTINGS)
        elif not had_settings:
            SETTINGS.unlink(missing_ok=True)
        for _, target in artifacts:
            if had_targets[target] and artifact_backups[target].exists():
                os.replace(artifact_backups[target], target)
            elif not had_targets[target]:
                target.unlink(missing_ok=True)
        raise SystemExit(f"Pi extension install failed and was rolled back: {error}") from error

    print(f"Pi extension installed: {TARGET} (source: {SOURCE})")
    print(f"Bare Pi registration: {SETTINGS}")
    print("Rollback copies retained until final status verification passes.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
