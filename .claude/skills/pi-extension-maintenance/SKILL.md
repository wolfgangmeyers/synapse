---
name: pi-extension-maintenance
description: Safely inspect and validate the canonical Mailbox V2 Pi extension without relocating its source.
---

# Pi extension maintenance

The active Mailbox V2 extension stays in the mecha-developer checkout at
`REPO_ROOT/pi-extensions/mecha-messaging.js`. Pi registers that source path.
Do not copy, move, symlink, restore, or replace it with an installed artifact
under `~/.mecha-developer` or another location.

## Constants

```bash
REPO_ROOT="$(git -C /path/to/mecha-developer rev-parse --show-toplevel)"
SOURCE="$REPO_ROOT/pi-extensions/mecha-messaging.js"
SETTINGS="$HOME/.pi/agent/settings.json"
```

`SOURCE` must be a regular, non-symlink file in the intended checkout. The
settings registration must resolve to `SOURCE`. Preserve the router default
`http://127.0.0.1:8765` unless an explicit `MECHA_ROUTER_URL` supplies another
exact loopback HTTP(S) base URL.

## Status and registration check (read-only)

```bash
test -f "$SOURCE" && test ! -L "$SOURCE" || {
  echo "invalid canonical source: $SOURCE" >&2
  exit 1
}
python3 - "$SOURCE" "$SETTINGS" <<'PY'
import json, pathlib, sys
source, settings = map(pathlib.Path, sys.argv[1:])
data = json.loads(settings.read_text())
packages = data.get("packages", [])
resolved = []
for package in packages:
    if isinstance(package, str) and package.endswith("/mecha-messaging.js"):
        path = pathlib.Path(package)
        if not path.is_absolute():
            path = settings.parent / path
        resolved.append(path.resolve())
matches = [path for path in resolved if path == source.resolve()]
print(f"source registrations: {len(matches)}; mecha registrations: {len(resolved)}")
for path in resolved:
    print(f"registered: {path}")
if len(matches) != 1:
    raise SystemExit("expected exactly one registration for the checkout source")
PY
```

A healthy setup has one registration resolving to the checkout source. A
registration for `~/.mecha-developer/extensions/mecha-messaging.js` is stale
and must not be recreated or treated as the active extension.

## Preflight

Run these before making an authorized source change. If a check fails, stop.
Do not relocate the extension or edit Pi settings as a workaround.

```bash
test -f "$SOURCE" && test ! -L "$SOURCE"
test -f "$REPO_ROOT/tests/pi_extension_silent.test.mjs"
node --test "$REPO_ROOT/tests/pi_extension_silent.test.mjs"
node --check "$SOURCE"
```

## Source updates

Make changes only in the checkout source. Keep the source path and settings
registration unchanged. Do not run a copy-based installer, create a symlink,
or restore an old installed artifact. Existing bare Pi processes need a reload
after an authorized source change; do not claim the new source is active before
that reload and a controlled wakeup check.

Registration keeps the checkout source available through Pi settings. Normal
Pi supervisor, worker, service, and recovery launches use Pi's default
configured extensions and pass neither `--extension` nor `--no-extensions`/`-ne`;
those flags are reserved for deliberate extension-test tools. Keep the managed
launch's provider, model, router environment, and default extensions unchanged.

## Uninstall

Do not uninstall or remove the moved extension as part of ordinary maintenance.
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
