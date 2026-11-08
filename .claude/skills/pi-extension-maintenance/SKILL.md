---
name: pi-extension-maintenance
description: Safely inspect and validate the canonical Mailbox V2 Pi extension without relocating its source.
---

# Pi extension maintenance

This skill maintains one Pi source package, the Messaging checkout package. The
Goal package has been removed, so a Pi registration for it is stale and must be
deleted from settings.

1. `REPO_ROOT/pi-extensions/mecha-messaging/` (discovers `index.ts` and retains
   the generated `mailbox-client.js` for plain-Node consumers)

The package has exactly one registration. Do not copy, move, symlink, restore,
or replace the package with an installed artifact under `~/.mecha-developer` or
elsewhere.

## Constants

```bash
REPO_ROOT="$(git -C /path/to/mecha-developer rev-parse --show-toplevel)"
MESSAGING_SOURCE="$REPO_ROOT/pi-extensions/mecha-messaging"
SETTINGS="$HOME/.pi/agent/settings.json"
```

The Messaging checkout package path must be a directory, not a symlink. The
settings `packages` list must contain exactly one Messaging registration and no
Goal registration; reject any `mecha-goal` entry as a stale registration of the
removed package. Reject standalone `goal.js`, `mecha-messaging.js`, and any other
flat legacy entry, including entries kept alongside the checkout package.
Preserve the router default
`http://127.0.0.1:8765` unless an explicit `MECHA_ROUTER_URL` supplies another
exact loopback HTTP(S) base URL.

## Status and registration check (read-only)

```bash
test -d "$MESSAGING_SOURCE" && test ! -L "$MESSAGING_SOURCE" || {
  echo "invalid canonical package: $MESSAGING_SOURCE" >&2
  exit 1
}
python3 - "$MESSAGING_SOURCE" "$SETTINGS" <<'PY'
import json, pathlib, sys
messaging_source, settings = map(pathlib.Path, sys.argv[1:])
data = json.loads(settings.read_text())
packages = data.get("packages", [])
resolved = []
legacy = []
removed = []
for package in packages:
    if isinstance(package, str):
        path = pathlib.Path(package)
        if not path.is_absolute():
            path = settings.parent / path
        path = path.resolve()
        if path.suffix == ".js" or path.name in {"mecha-goal", "mecha-messaging"}:
            if path.name in {"goal.js", "mecha-messaging.js"}:
                legacy.append(package)
            if path.name == "mecha-goal":
                removed.append(package)
            if path.name == "mecha-messaging":
                resolved.append(path)
if legacy:
    raise SystemExit(f"reject standalone or old flat Pi registration(s): {', '.join(legacy)}")
if removed:
    raise SystemExit(f"reject stale Goal registration(s) for the removed package; delete from settings: {', '.join(removed)}")
expected = [messaging_source.resolve()]
if resolved != expected:
    raise SystemExit(f"expected exactly one Messaging registration; found {resolved}")
print("ok: one Messaging checkout package registration")
PY
```

A healthy setup has one registration, for the Messaging checkout package. A
registration for `~/.mecha-developer/extensions/mecha-messaging.js` is stale
and must not be recreated or treated as the active package. Messaging retains
the generated plain-Node client at
`~/.mecha-developer/extensions/pi-extensions/mecha-messaging/mailbox-client.js`, plus sidecar
and harness support files.

## Preflight

Run these before making an authorized source change. If a check fails, stop.
Do not relocate the extension or edit Pi settings as a workaround.

```bash
test -d "$MESSAGING_SOURCE" && test ! -L "$MESSAGING_SOURCE"
test -f "$MESSAGING_SOURCE/mailbox-client.js"
(cd "$MESSAGING_SOURCE" && npm run test:mecha-messaging)
(cd "$MESSAGING_SOURCE" && npm run check:mailbox-client-drift)
```

## Source updates

Generate `mailbox-client.js` from `mailbox-client.ts` before activation. The
JavaScript file is a reproducible support artifact for plain-Node consumers; it
is not hand-edited. The installer copies it byte-for-byte to the installed
support root while keeping the TypeScript package in the checkout.

Make changes only in the appropriate checkout package. Keep the checkout path
and its settings registration unchanged. Do not run a copy-based
installer, create a symlink, or restore an old installed artifact. Existing bare
Pi processes need a reload after an authorized source change; do not claim the
new packages are active before that reload and a controlled wakeup check.

Registration keeps the checkout package available through Pi settings. Managed
Pi supervisor, worker, service, and recovery launches preserve Pi's normal
extension configuration; they must not disable, replace, or selectively reconstruct
extensions. Other runtimes keep their normal extension policy. Keep the managed
launch's provider, model, and router environment unchanged.

## Installer recovery

The installer stages a complete preserved replacement for
`~/.mecha-developer/extensions`, including unrelated support files. Before
replacing it or `~/.pi/agent/settings.json`, it records durable backups and a
transaction marker. If installation stops after recording, after support-root
replacement, or after settings replacement, the next run restores both prior
roots before doing new work. A completed transaction removes its marker and
rollback copies. Missing or malformed rollback state stops installation.

## Uninstall

Do not uninstall or remove the moved package as part of ordinary maintenance.
If the extension should be retired, that is a separate explicitly authorized
registration and source-lifecycle decision.

## Final verification

1. Re-run the status and preflight checks.
2. Verify the registered path resolves to the Messaging checkout package.
3. If a running Pi process was intentionally reloaded, confirm the extension
   registers and a controlled wakeup produces no terminal console output.
4. Report the source path, registration count, test output, reload result, and
   any external decision that was required.

Never perform a global switch, reload, or live wakeup test merely to test this
skill. Use an isolated fixture or obtain explicit operator authorization.
