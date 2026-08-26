# Reminder lock control and corrected WebUI reminder workflow

## Decision

Implement the corrected direction in SPEC.md. The router keeps authority over reminder records, validation, scheduling, delivery, retry, CLI, and HTTP behavior. The WebUI uses the router-owned POST/PATCH/DELETE paths and keeps no reminder store. Native reminder locks remain an in-memory target-extension guard for native update/delete only.

## Implementation

- Keep the existing protocol, router relay, target-extension lock map, incarnation fencing, and native guard unchanged except for focused tests needed by this lane.
- In `web/router-ui/app.js`, render a reminder button in every agent row. Use muted gray styling for empty rows. The button opens a dialog with a normal create form when empty and reminder cards when populated. Add labeled body/subject fields for create/edit, one-shot/recurring scheduling fields, pending/error states, and keyboard-safe controls.
- In `tools/router_web_ui.mjs`, add a narrow `reminder_crud` browser command. It forwards JSON POST/PATCH/DELETE to the selected router and relays the router status and JSON response. It does not validate, persist, schedule, or lock reminders.
- Keep lock controls as a separate relay action. CRUD controls stay enabled during lock pending/locked states.
- Add focused source/relay tests for empty indicators, keyboard behavior, POST without lock action, PATCH body/subject, pending/error handling, and lock/CRUD independence. Preserve existing router reminder tests.

## Acceptance

Every agent row has an accessible reminder button. Empty rows open creation. Create sends a normal POST, edit sends PATCH body and optional subject, and router errors remain visible. Existing lock set/clear still works and never gates WebUI CRUD. No lock state is persisted or sent in reminder CRUD payloads.
