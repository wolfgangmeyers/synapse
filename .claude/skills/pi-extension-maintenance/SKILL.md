---
name: pi-extension-maintenance
description: Safely inspect and validate the canonical Mailbox V2 Pi extension without relocating its source.
---

# Pi extension maintenance

The active Mailbox V2 package stays in the mecha-developer checkout at
`REPO_ROOT/pi-extensions/mecha-messaging/`. Pi registers that directory and
discovers `index.ts`. The package composes the goal extension, so it is the sole
registration for this extension graph. Do not copy, move, symlink, restore, or
replace it with an installed artifact under `~/.mecha-developer` or elsewhere.

## Constants

```bash
REPO_ROOT="$(git -C /path/to/mecha-developer rev-parse --show-toplevel)"
SOURCE="$REPO_ROOT/pi-extensions/mecha-messaging"
SETTINGS="$HOME/.pi/agent/settings.json"
```

`SOURCE` must be a directory, not a symlink, in the intended checkout. The
settings registration must resolve to `SOURCE`, with exactly one registration for
this package and no standalone `goal.js` registration. The check also rejects old flat `goal.js` and `mecha-messaging.js` registrations, including duplicates alongside the package. Preserve the router default
`http://127.0.0.1:8765` unless an explicit `MECHA_ROUTER_URL` supplies another
exact loopback HTTP(S) base URL.

## Status and registration check (read-only)

```bash
test -d "$SOURCE" && test ! -L "$SOURCE" || {
  echo "invalid canonical package: $SOURCE" >&2
  exit 1
}
python3 - "$SOURCE" "$SETTINGS" <<'PY'
import json, pathlib, sys
source, settings = map(pathlib.Path, sys.argv[1:])
data = json.loads(settings.read_text())
packages = data.get("packages", [])
resolved = []
legacy = []
for package in packages:
    if isinstance(package, str):
        path = pathlib.Path(package)
        if not path.is_absolute():
            path = settings.parent / path
        path = path.resolve()
        if path.name in {"goal.js", "mecha-messaging.js"}:
            legacy.append(package)
        if path.name == "mecha-messaging":
            resolved.append(path)
if legacy:
    raise SystemExit(f"reject standalone or old flat Pi registration(s): {', '.join(legacy)}")
matches = [path for path in resolved if path == source.resolve()]
print(f"package registrations: {len(matches)}; mecha package registrations: {len(resolved)}")
for path in resolved:
    print(f"registered: {path}")
if len(matches) != 1:
    raise SystemExit("expected exactly one registration for the checkout package")
PY
```

A healthy setup has one registration resolving to the checkout package. A
registration for `~/.mecha-developer/extensions/mecha-messaging.js` is stale
and must not be recreated or treated as the active package. The installed
support root contains the generated plain-Node client at
`~/.mecha-developer/extensions/pi-extensions/mecha-messaging/mailbox-client.js`, plus sidecar
and harness support files.

## Preflight

Run these before making an authorized source change. If a check fails, stop.
Do not relocate the extension or edit Pi settings as a workaround.

```bash
test -d "$SOURCE" && test ! -L "$SOURCE"
test -f "$REPO_ROOT/pi-extensions/mecha-messaging/mailbox-client.js"
(cd "$SOURCE" && npm run test:mecha-messaging)
(cd "$SOURCE" && npm run check:mailbox-client-drift)
```

## Source updates

Generate `mailbox-client.js` from `mailbox-client.ts` before activation. The
JavaScript file is a reproducible support artifact for plain-Node consumers; it
is not hand-edited. The installer copies it byte-for-byte to the installed
support root while keeping the TypeScript package in the checkout.

Make changes only in the checkout package. Keep its path and the single settings
registration unchanged. Do not run a copy-based installer, create a symlink, or
restore an old installed artifact. Existing bare Pi processes need a reload after
an authorized source change; do not claim the new package is active before that
reload and a controlled wakeup check.

Registration keeps the checkout package available through Pi settings. Normal
Pi supervisor, worker, service, and recovery launches use Pi's default configured
extensions and pass neither `--extension` nor `--no-extensions`/`-ne`; those flags
are reserved for deliberate extension-test tools. Keep the managed launch's
provider, model, router environment, and default extensions unchanged.

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
2. Verify the registered path still resolves to `SOURCE`.
3. If a running Pi process was intentionally reloaded, confirm the extension
   registers and a controlled wakeup produces no terminal console output.
4. Report the source path, registration count, test output, reload result, and
   any external decision that was required.

Never perform a global switch, reload, or live wakeup test merely to test this
skill. Use an isolated fixture or obtain explicit operator authorization.
