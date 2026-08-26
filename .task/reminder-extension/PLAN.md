# Reminder lock control and corrected WebUI reminder workflow

## Decision

Implement the corrected direction in SPEC.md. The router keeps authority over reminder records, validation, scheduling, delivery, retry, CLI, and HTTP behavior. The WebUI uses the router-owned POST/PATCH/DELETE paths and keeps no reminder store. Native reminder locks remain an in-memory target-extension guard for native update/delete only.

## Implementation

- Keep the existing protocol, router relay, target-extension lock map, incarnation fencing, and native guard unchanged except for focused tests needed by this lane.
- In `web/router-ui/index.html` and `web/router-ui/app.js`, place one reminder button in the selected-agent conversation header beside the goal flag. Disable it with no selection, use muted gray styling with no active reminders, and preserve create/list behavior and keyboard access. Keep agent rows free of reminder controls.
- In `tools/router_web_ui.mjs`, add a narrow `reminder_crud` browser command. It forwards JSON POST/PATCH/DELETE to the selected router and relays the router status and JSON response. It does not validate, persist, schedule, or lock reminders.
- Keep lock controls as a separate relay action. CRUD controls stay enabled during lock pending/locked states.
- Add focused source/relay tests for empty indicators, keyboard behavior, POST without lock action, PATCH body/subject, pending/error handling, and lock/CRUD independence. Preserve existing router reminder tests.

## Acceptance

The selected-agent conversation header has an accessible reminder button beside the goal flag. It is disabled with no selection, opens creation when empty, and opens the list when reminders exist. Create sends a normal POST, edit sends PATCH body and optional subject, and router errors remain visible. Existing lock set/clear still works and never gates WebUI CRUD. No lock state is persisted or sent in reminder CRUD payloads.
