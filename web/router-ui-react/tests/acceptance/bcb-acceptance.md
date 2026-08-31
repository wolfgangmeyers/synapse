# Browser Console Bridge acceptance

This checklist is opt-in. It uses a fresh task-named Chrome tab and a task-temporary Go router. It does not use the live router.

## Before the run

- Check BCB `/health` and confirm `extension_connected: true`.
- Keep existing human tabs, their URLs, and their window sizes unchanged.
- Start the isolated Go router with a temporary database, JSONL log, attachment directory, and loopback port.
- Start Vite on a separate temporary loopback port.
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
15. Record the viewport BCB reports for every visual check. Use an external device-emulation session for a narrow viewport when BCB cannot change `window.innerWidth`.

## After the run

- Recheck task process and listener ownership.
- Stop Vite and the isolated Go router. Confirm the task database, log, attachments, and temporary ports are gone.
- Confirm unrelated processes, files, runtimes, and browser tabs remain unchanged.
- If a live-state proof was authorized, capture the same read-only fingerprints and compare them with the pre-run values. Attribute changes only after checking task process paths and arguments.

BCB is not run as part of the transport test command. This document records the required acceptance procedure.
