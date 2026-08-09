---
name: pi-extension-maintenance
description: Safely inspect, install, update, uninstall, and diagnose the canonical Mailbox V2 Pi extension. Use when Pi extension registration, stale artifacts, bare Pi configuration, or managed-worker extension paths need maintenance.
---

# Pi extension maintenance

Use this skill for the real copied artifact lifecycle. Do not use a symlink. Do not switch a live global Pi installation until all preflight validation succeeds.

## Constants

```bash
REPO_ROOT="$(git -C /path/to/mecha-developer rev-parse --show-toplevel)"
SOURCE="$REPO_ROOT/pi-extensions/mecha-messaging.js"
TARGET="$HOME/.mecha-developer/extensions/mecha-messaging.js"
SETTINGS="$HOME/.pi/agent/settings.json"
```

`SOURCE` is canonical only when it is a regular, non-symlink file in the intended mecha-developer checkout. `TARGET` is the one stable installed mailbox artifact path. The router default is `http://127.0.0.1:8765`; preserve it unless an explicit `MECHA_ROUTER_URL` supplies another exact loopback HTTP(S) base URL.

## Status and drift check (read-only)

```bash
test -f "$SOURCE" && test ! -L "$SOURCE" || { echo "invalid canonical source: $SOURCE" >&2; exit 1; }
test -f "$TARGET" && test ! -L "$TARGET" || echo "extension is absent or not a real file"
if [ -f "$TARGET" ] && [ ! -L "$TARGET" ] && ! cmp -s "$SOURCE" "$TARGET"; then
  echo "DRIFT: installed artifact differs from canonical source"
fi
python3 - "$SETTINGS" "$TARGET" <<'PY'
import json, pathlib, sys
settings, target = map(pathlib.Path, sys.argv[1:])
data = json.loads(settings.read_text())
matches = [p for p in data.get("packages", []) if p == str(target)]
stale = [p for p in data.get("packages", []) if isinstance(p, str) and p.endswith("/mecha-messaging.js") and p != str(target)]
print(f"target registrations: {len(matches)}; stale mecha registrations: {len(stale)}")
for path in stale: print(f"stale: {path}")
PY
```

Healthy means: canonical source and target are regular files, byte-identical, the target occurs exactly once in `packages`, and no other `mecha-messaging.js` path is registered. An absent target is not drift; it is uninstalled.

## Preflight

Do not mutate anything until these pass. The project test is the static console-output gate. The final controlled-session check proves registration and wakeup behavior at runtime.

```bash
test -f "$SOURCE" && test ! -L "$SOURCE"
test -f "$REPO_ROOT/tests/pi_extension_silent.test.mjs"
node --test "$REPO_ROOT/tests/pi_extension_silent.test.mjs"
node --check "$SOURCE"
```

If any command fails, stop. Do not install, alter `settings.json`, reload Pi, or remove an existing working artifact.

## Install or update

Run the maintained installer from the repository root:

```bash
python3 .claude/skills/supervisor/tools/install_pi_extension.py
```

It installs/registers the stable Pi extension artifact bundle.

This transaction validates the source, copies it through a same-directory temporary file, saves artifact and settings rollback copies, then atomically replaces `settings.json` without changing unrelated keys or package entries. It removes duplicate target registrations and replaces/removes only the stable target artifact. A reported non-target stale path is not deleted until an operator confirms its ownership; this prevents an unrelated Pi package from being removed by filename coincidence. It never deletes the canonical source.

```bash
export SOURCE TARGET SETTINGS
python3 - <<'PY'
import json, os, pathlib, shutil, tempfile
source = pathlib.Path(os.environ["SOURCE"])
target = pathlib.Path(os.environ["TARGET"])
settings = pathlib.Path(os.environ["SETTINGS"])
if not source.is_file() or source.is_symlink(): raise SystemExit(f"invalid canonical source: {source}")
if not settings.is_file(): raise SystemExit(f"missing Pi settings: {settings}")
try:
    data = json.loads(settings.read_text())
except json.JSONDecodeError as error:
    raise SystemExit(f"invalid Pi settings; refusing mutation: {error}")
if not isinstance(data.get("packages", []), list): raise SystemExit("Pi settings packages must be a list")
data.setdefault("packages", [])
if target.is_symlink(): raise SystemExit(f"refusing symlinked installed artifact: {target}")
target.parent.mkdir(parents=True, exist_ok=True)
fd, staged_name = tempfile.mkstemp(prefix=".mecha-messaging.", dir=target.parent)
os.close(fd); staged = pathlib.Path(staged_name)
backup = target.with_name(target.name + ".rollback")
settings_backup = settings.with_name(settings.name + ".mecha-messaging.rollback")
try:
    settings_backup.write_text(settings.read_text())
    shutil.copy2(source, staged)
    if staged.is_symlink() or staged.read_bytes() != source.read_bytes(): raise RuntimeError("staged artifact validation failed")
    if target.exists() or target.is_symlink(): shutil.copy2(target, backup, follow_symlinks=False)
    os.replace(staged, target)
    packages = [p for p in data["packages"] if not isinstance(p, str) or p != str(target)]
    packages.append(str(target))
    data["packages"] = packages
    fd, name = tempfile.mkstemp(prefix=".settings.", dir=settings.parent)
    with os.fdopen(fd, "w") as out:
        json.dump(data, out, indent=2); out.write("\n"); out.flush(); os.fsync(out.fileno())
    os.replace(name, settings)
except Exception:
    staged.unlink(missing_ok=True)
    if settings_backup.exists(): os.replace(settings_backup, settings)
    if backup.exists(): os.replace(backup, target)
    else: target.unlink(missing_ok=True)
    raise
PY
```

The `packages` entry is the global package registration used by bare Pi sessions. Run the status check again, then `/reload` every existing bare Pi session after a path change. Pi only rereads global extension configuration on reload/start; never claim the new registration is active before this happens.

Managed workers use Pi's default configured extensions and add the same stable Mailbox target path explicitly:

```bash
pi --extension "$HOME/.mecha-developer/extensions/mecha-messaging.js"
```

Keep the managed launch's existing provider/model/router environment unchanged. Do not add `--no-extensions` for ordinary managed workers; reserve bare-extension launches only for explicit diagnostics.

Context-threshold/self-compaction policy is extension-owned. Managed launchers may export `MECHA_PI_COMPACTION_POLICY` through `--compaction-policy` as simple default levels plus optional per-model overrides (for example `{"levels":[40,50,60,70,80,{"percent":90,"mandatory":true}],"models":{"openai-codex/gpt-5.6-sol":{"levels":[20,30,40,{"percent":45,"mandatory":true}]}}}`) or comma-separated default percentages. Existing tiered JSON remains accepted for compatibility, but do not make tiers the recommended user-facing config. Policy JSON may include optional `postCompactionNotification` text; `mecha_context action=compact` can also provide a command-level handoff note. Include a handoff note when you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed. Pi does not expose a first-class CLI parameter for this policy; do not edit Pi settings to mimic it.

## Uninstall

First stop/reload affected Pi sessions so no process retains the file. Then atomically remove only the mecha package registrations while preserving all unrelated settings and packages; remove `TARGET` only after config mutation succeeds. A failure leaves the installed artifact in place.

```bash
export TARGET SETTINGS
python3 - <<'PY'
import json, os, pathlib, tempfile
settings, target = map(pathlib.Path, (os.environ["SETTINGS"], os.environ["TARGET"]))
data = json.loads(settings.read_text())
if not isinstance(data.get("packages", []), list): raise SystemExit("Pi settings packages must be a list")
data["packages"] = [p for p in data["packages"] if p != str(target)]
fd, name = tempfile.mkstemp(prefix=".settings.", dir=settings.parent)
with os.fdopen(fd, "w") as out:
    json.dump(data, out, indent=2); out.write("\n"); out.flush(); os.fsync(out.fileno())
os.replace(name, settings)
target.unlink(missing_ok=True)
PY
```

Run `/reload` in every affected bare Pi session. Managed workers should continue to add the explicit `--extension "$TARGET"` argument after a subsequent install while otherwise using Pi's default configured extensions.

## Final verification and recovery

1. Run the status check and the preflight test again.
2. Start/reload one controlled Pi session and confirm the mailbox extension registers and a `pr-watch` wakeup produces **no terminal console output**. UI notifications are not console output; any `console.log`, `console.warn`, or `console.error` is a failure.
3. If the settings mutation or startup check fails, restore the saved `settings.json.mecha-messaging.rollback` and `mecha-messaging.js.rollback` files before retrying. If replacement failed before the transaction completed, keep the old target; do not delete it to "clean up". After the controlled-session check passes, remove those two rollback files.
4. Report the source/target SHA-256, registration count, stale paths, test output, reload result, and any rollback performed.

Never perform a global switch, `/reload`, or live wakeup test merely to test this skill. Use an isolated fixture or obtain explicit operator authorization.
