# Android router aliases

## Behavior

Android should match the Web UI router alias behavior for saved routers:

- A saved router can have an optional human-friendly alias in addition to its normalized router endpoint.
- Blank aliases are allowed and mean the endpoint is used as the visible name.
- The router list shows the alias as the primary label when present and keeps the endpoint visible as secondary context.
- Users can add a new router with an alias, edit an existing router's endpoint or alias, select a router, or remove a router.
- Changing the active router preserves the existing connection/runtime gates: Android still connects only after a router is selected, keeps the install-owned Android agent identity, sends the existing Android registration metadata, and uses the same per-router history/draft scoping.
- Removing a router keeps the current cleanup behavior for that router's scoped local state.

## Design

The Android app already stores routers as normalized endpoint records and displays a router manager screen. Extend that same model with an optional alias field and add an edit path to the existing manager. Keep aliases local-only, just like the Web UI; the router protocol and installed extension/runtime registration gates do not change.

Alternatives considered:

1. Reuse the existing display field as a freeform alias only. This is the smallest storage change, but it keeps endpoint and alias semantics blurred and makes blank aliases hard to represent cleanly.
2. Add a separate alias field in Android preferences. This mirrors the Web UI most closely, but it would require coordinating two stores during add/edit/remove.
3. Store router records as alias plus endpoint in the existing saved-router set. This keeps the feature local and scoped to the current Android patterns while giving the code clear alias semantics. Choose this option.
