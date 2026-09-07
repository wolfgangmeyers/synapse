#!/usr/bin/env bash
set -euo pipefail

# Print deployment pins for the exact fixed inputs used by run_router.sh. This
# is a preparation step, not part of router startup: the launcher consumes the
# resulting MECHA_MANAGED_* values and never hashes live files at runtime.
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PI_EXECUTABLE="${MECHA_ROUTER_MANAGED_PI:-$(command -v pi)}"
SUBAGENTS="${MECHA_ROUTER_MANAGED_PI_SUBAGENTS:-${HOME}/code/pi-packages/packages/pi-subagents/src/index.ts}"
CURSOR_PACKAGE="${MECHA_ROUTER_MANAGED_CURSOR_PACKAGE:-$ROOT_DIR/auth-helper/node_modules/pi-cursor-sdk}"
CURSOR="${MECHA_ROUTER_MANAGED_CURSOR:-$CURSOR_PACKAGE/dist/index.js}"
CURSOR_LOCK="${MECHA_ROUTER_MANAGED_CURSOR_LOCK:-$ROOT_DIR/auth-helper/package-lock.json}"
HELPER="${MECHA_ROUTER_MANAGED_HELPER:-$ROOT_DIR/auth-helper/index.mjs}"
HELPER_LOCK="${MECHA_ROUTER_MANAGED_HELPER_LOCK:-$ROOT_DIR/auth-helper/package-lock.json}"
UI="${MECHA_ROUTER_MANAGED_UI:-$ROOT_DIR/router-proxy/managed-ui}"

python3 - "$ROOT_DIR" "$PI_EXECUTABLE" "$SUBAGENTS" "$CURSOR" "$CURSOR_PACKAGE" "$CURSOR_LOCK" "$HELPER" "$HELPER_LOCK" "$UI" <<'PY'
import hashlib
import json
import os
import subprocess
import sys

root, pi, subagents, cursor, cursor_package, cursor_lock, helper, helper_lock, ui = sys.argv[1:]

def file_hash(path):
    with open(path, 'rb') as stream:
        return hashlib.sha256(stream.read()).hexdigest()

def tree_hash(path):
    entries = []
    for base, dirs, files in os.walk(path, followlinks=False):
        dirs[:] = sorted(d for d in dirs if d != 'node_modules')
        for name in sorted(files):
            candidate = os.path.join(base, name)
            if os.path.islink(candidate):
                raise SystemExit(f'tree contains symlink: {candidate}')
            relative = os.path.relpath(candidate, path).replace(os.sep, '/')
            entries.append((relative, open(candidate, 'rb').read()))
    digest = hashlib.sha256()
    for name, data in sorted(entries):
        digest.update(name.encode())
        digest.update(b'\0')
        digest.update(data)
        digest.update(b'\0')
    return digest.hexdigest()

with open(os.path.join(cursor_package, 'package.json'), encoding='utf-8') as stream:
    cursor_identity = json.load(stream)
print(f'export MECHA_MANAGED_PI_VERSION={subprocess.check_output([pi, "--version"], text=True).strip()}')
print(f'export MECHA_MANAGED_PI_SHA256={file_hash(pi)}')
print(f'export MECHA_MANAGED_MANIFEST_SHA256={file_hash(os.path.join(root, "router/contract/managed-tools-v1.json"))}')
print(f'export MECHA_MANAGED_MECHA_RPC_SHA256={file_hash(os.path.join(root, "pi-extensions/mecha-rpc/index.ts"))}')
print(f'export MECHA_MANAGED_PI_SUBAGENTS_ENTRY_SHA256={file_hash(subagents)}')
print(f'export MECHA_MANAGED_PI_SUBAGENTS_TREE_SHA256={tree_hash(os.path.dirname(os.path.dirname(subagents)))}')
print(f'export MECHA_MANAGED_CURSOR_EXTENSION_SHA256={file_hash(cursor)}')
print(f'export MECHA_MANAGED_CURSOR_PACKAGE_SHA256={file_hash(os.path.join(cursor_package, "package.json"))}')
print(f'export MECHA_MANAGED_CURSOR_IDENTITY={cursor_identity["name"]}@{cursor_identity["version"]}')
print(f'export MECHA_MANAGED_CURSOR_LOCK_SHA256={file_hash(cursor_lock)}')
print(f'export MECHA_MANAGED_CURSOR_TREE_SHA256={tree_hash(cursor_package)}')
print(f'export MECHA_MANAGED_HELPER_SHA256={file_hash(helper)}')
print(f'export MECHA_MANAGED_HELPER_LOCK_SHA256={file_hash(helper_lock)}')
print(f'export MECHA_MANAGED_HELPER_TREE_SHA256={tree_hash(os.path.dirname(helper))}')
print(f'export MECHA_MANAGED_UI_TREE_SHA256={tree_hash(ui)}')
PY
