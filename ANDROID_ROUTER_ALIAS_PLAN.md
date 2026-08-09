# Android router aliases plan

Implement `ANDROID_ROUTER_ALIAS_SPEC.md` with these scoped changes:

1. Model helpers
   - Replace the router record's ambiguous display field with an optional alias plus normalized endpoint.
   - Keep parsing compatible with existing stored records and raw endpoint records.
   - Add pure helpers for display labels and router upsert so unit tests can cover alias trimming, blank aliases, endpoint normalization, and edit replacement.

2. Router manager UI
   - Add an Alias field beside the existing hostname field.
   - Add an edit action for saved routers.
   - In add mode, save a new normalized endpoint with the trimmed alias.
   - In edit mode, replace the edited router record, preserve existing active-router switching behavior, and show endpoint metadata when an alias is present.

3. Tests and validation
   - Add Android unit tests for alias helper behavior.
   - Extend the source-level Android parity test so the JS suite guards the UI wiring.
   - Run focused Android unit tests/build and the relevant Node parity/contract tests.
   - Run coverage for the touched Android helpers where local tooling supports it.
