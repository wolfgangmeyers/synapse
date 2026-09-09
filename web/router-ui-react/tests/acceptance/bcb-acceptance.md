# Browser Console Bridge acceptance

This checklist is opt-in. It uses a fresh task-named Chrome tab and a task-temporary Go router. It does not use the live router.

## Before the run

- Check BCB `/health` and confirm `extension_connected: true`.
- Keep existing human tabs, their URLs, and their window sizes unchanged.
- Start the isolated Go router with a temporary database, JSONL log, attachment directory, and loopback port.
- Standard UI acceptance may start Vite on a separate temporary loopback port. The managed-RPC release proof below instead serves a production build directly from its isolated Go router.
- Record the task process IDs, working directories, arguments, ports, database path, log path, and attachment path.
- Capture read-only live-state fingerprints only when a live-router proof is explicitly authorized. The fingerprint inputs are router database files, the JSONL log, attachment-directory contents, and the connected-agent snapshot. Record an unavailable live router as an environment condition. This Phase 7 test slice does not read or connect to a live router.

## Browser steps

1. Open a fresh Chrome tab through BCB and navigate to the Vite URL.
2. Configure the isolated router URL and the deterministic UI agent ID.
3. Confirm the page reports connected after registration. Confirm the isolated agent appears and is excluded from the peer list.
4. Use fixture agents to show active, working, compacting, and expanded subagent states.
5. Exercise goal indicators and the goal editor: set, turn off, lock, snooze, turn limit, budget, refill values, and mapped errors.
6. Exercise model catalogs and model selection, including search, truncation, optimistic state, rejection, and stale snapshots.
7. Create, edit, delete, and lock a reminder. Check the countdown and the separate lock-after-create failure state.
8. Select a peer and send text. Receive a message. Check pending-to-canonical settlement, the reply footer, reconnect history, and peer-scoped persistence.
9. Select and drag an image. Check conversion limits, preview/removal, attachment references, rendered thumbnails, and relative attachment URLs.
10. Paste the isolated `clipboard.png` fixture into the composer textarea using its `data-testid="clipboard-paste-target"` hook as its own step. Confirm the image preview appears.
11. Create, use, import, overwrite, and delete a prompt template. Check token order, selection insertion, focus preservation, and that insertion does not send a message.
12. Click the prompt manager's `data-testid="prompt-export"` hook as its own step. Confirm the browser download is triggered and record the downloaded JSON.
13. Exercise a message arrival. Check source filtering, replacement, dismissal, expiry, safe body rendering, and separation from conversation history.
14. Inspect the console and network panel for uncaught errors, failed imports, invalid requests, stale-state updates, and unexpected router URLs.
15. Record the viewport BCB reports for every visual check. The managed-RPC release proof opens its own BCB-controlled desktop popup, adjusts that popup with `window.resizeTo`, and requires `innerWidth=1480` with no horizontal overflow. Mobile and 390 px browser work are outside this delivery. Device emulation is not release evidence. Before every release screenshot, the runner gives that popup a unique title, checks the exact BCB tab ID, URL, title, selected parent, and nested child, then makes the matching Chrome window the macOS accessibility main window. If that focus check is unavailable or fails, the screenshot is not release evidence.

## Managed-RPC release proof

Run the real managed-Pi path with:

```sh
npm --prefix web/router-ui-react run test:acceptance:bcb -- --managed-rpc --release
```

It requires `MECHA_PI_SOURCE_LOCK`, `MECHA_PI_PACKAGE_ROOT`, `MECHA_PI_BIN`, and a working BCB checkout with its extension connected. The runner builds `web/router-ui-react`, compiles `cmd/managed-acceptance-router`, and passes that disposable built directory to the isolated router's static-UI boundary. It creates its own database, log, attachment directory, managed HOME/session root, and deterministic local provider, then launches installed Pi through that router. No Vite server or UI gateway participates. The runner opens one local BCB popup directly on the isolated router, clears its storage, and closes only that popup during cleanup. It writes `REPORT.json`, endpoint snapshots, recovery artifacts, and desktop BCB screenshots to `MECHA_BCB_ARTIFACT_DIR` (or a new directory under `/private/tmp`).

The acceptance-only router command is the only path that installs `pi-extensions/mecha-rpc/tests/fixtures/managed-rpc-release-probe.mjs`; production `mailbox-router` continues to omit test extensions and test environment values. The automated run proves the real nested child from installed Pi, the existing `mecha.child.delta.v1` lifecycle path durably merged through the current managed fence, selected/expanded desktop DOM visibility while compacting, reconnect while compacting and after completion, parent-session reset/recovery, and an isolated-router restart that reports bounded `reconstructing` state rather than inventing a terminal child result. It captures the compacting state, compacting reconnect, completed reconnect, and the restart's reconstructing state with retained child records marked `interrupted_unknown`. Because `bcb_screenshot` captures Chrome's foreground tab, the runner uses macOS System Events to make its uniquely titled popup the exact main Chrome window immediately before each capture. Each image artifact records its SHA-256 plus the BCB tab ID, URL, title, selected parent, and visible child state. Missing prerequisites, lifecycle evidence, a passed focus/identity record, or a supported desktop popup route block acceptance.

## After the run

- Recheck task process and listener ownership.
- Stop the isolated Go router (and Vite only when running the standard UI acceptance). Confirm the task database, log, attachments, and temporary ports are gone.
- Confirm unrelated processes, files, runtimes, and browser tabs remain unchanged.
- If a live-state proof was authorized, capture the same read-only fingerprints and compare them with the pre-run values. Attribute changes only after checking task process paths and arguments.

BCB is not run as part of the transport test command. This document records the required acceptance procedure.
