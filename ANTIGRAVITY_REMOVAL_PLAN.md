# Antigravity removal: implementation handoff

Owner: implementing child; parent owns independent review/integration.
Base: existing dirty `main`. No commit/push, restart/deploy, live LLM calls, credential deletion/revocation, or app/Android changes. KB and concurrent workflow work are not owned by this task.

User override supersedes the old workflow ledger: implementation authorized, no dev-loop or child workflow gates.
Independent specification review approved inheritance and atomic four-provider refinements.

## Implemented

- Deleted Antigravity OAuth client secrets, refresh/read paths, Cloud Code endpoints/envelopes/retries/model aliases and grounding execution. Preserved shared Gemini conversion, tool/image/thinking/transcript/stream handling.
- Preserved Google API-key, Vertex ADC, router-owned Codex, Cursor. Child omission still inherits; only truly missing fallback now uses Vertex Flash. Corrected child stream selection so Vertex uses Vertex, not Google API-key streaming.
- Removed auth/helper/provider/UI registrations, allowlist entries, launcher discovery/flags/env forwarding, and messaging policy alias. Historical config/command fields are ignored; stored recipe/schema fields remain inert. Recovery and exec filter obsolete extension env.
- Four-provider helper/frontend/store cardinality; successful store refresh atomically removes unsupported rows, replaces models, advances revision. Pre-refresh projections hide obsolete rows. Failed/canceled/invalid refresh preserves state.
- Rebuilt shipped UI entrypoint and replaced old JS entry bundles only, preserving unrelated assets. Generated bundle reflects the current shared dirty checkout, so parent must independently assess it before integration.
- Removed obsolete OAuth-success tests; retained and adapted Gemini streaming/conversion tests to API-key wire requests (no Cloud Code envelope). Removed Cloud Code mock handlers. Existing generic supported-provider fixtures updated.

## Accepted evidence and scope (final fixture follow-up)

Authoritative broader validation: `/tmp/mecha-validation-045340/ACCEPTANCE.md`. Earlier partial helper/E2E attempts are superseded only for the exact green scopes below, not for broad suites.

- Isolated base `29dec4bf2fb05c075f9af9f56ec95fdc2cb14a1f`; broader evidence uses capture `20261102T045340119485Z`, not an automatically refreshed checkout.
- Offline Go build exit 0. Scoped managed: **53 top-level tests / 89 passing records**, exit 0. Grounding/recovery: **7 top-level tests**, including **12 cold-recovery subcases**, exit 0. These are Go HTTP/supervisor/SQLite boundaries, not browser coverage or real process recovery.
- Auth helper: **27 passed**, exit 0, 189.73s with reliable process termination. Supersedes interrupted helper runs; do not claim 37 tests.
- UI focused: **80 passed / 5 files**, exit 0. UI typecheck/build and messaging extension lint exit 0; compaction-policy **5 passed**. Validated isolated assets: 490 hashes and 135 input hashes; archived asset SHA256 `dd616361250d1e5cc33ae10c28831297eda08c6ea6589b1eab45e45dbe4081af`. No live build in this follow-up.
- KB browser suite: **10/10 passed in one complete run**, 1.6m, exit 0 with unchanged 15-second readiness limit. Previous contention/readiness failures are retained diagnostics, not a union of passing retries.
- Store fixture follow-up: `TestSubagentPrefixRootAgentSnapshotAndListing` and `TestStuckLaunchTimeoutAndRelaunchOverwrite` both pass, package 1.582s, exit 0. Only the obsolete root fixture provider changed to Vertex, assertions unchanged. Capture script still includes `router-proxy/internal/store/managed_launch_recovery_test.go`; latest capture is `20261102T051057595811Z`.
- Additional fixture-owner supporting checks on capture `20261102T045531936629Z`: `go test ./internal/managed -run 'TestAntigravityRemovalGroundingAPIKeyThenVertex|TestColdRecoveryLegacyAntigravityRecipe' -count=1 -timeout 120s -v`: exit 0, 21.298s (grounding + 12 recovery subcases); `go test ./internal/store -run 'TestAntigravityRemoval|TestManagedProviderSnapshot' -count=1 -timeout 60s -v`: exit 0, 9.334s. Logs `/tmp/agr-owned-support-{managed,store}.log`, exit files alongside. AGR-007 invalid/canceled/failed/repeated refresh evidence is SQLite transaction evidence, not browser.
- Six-case provider browser suite: `/tmp/agr-owned-rerun8.log`: **6/6 passed in one complete run**, 50.6s, exit 0. Final exact-byte rerun `/tmp/agr-owned-rerun9.log`: **6/6 passed in one complete run**, 54.6s, exit **0** (`/tmp/agr-owned-rerun9.exit`); diagnostics in `/tmp/agr-owned-rerun9-results`. No skipped tests or timeout increases. Tests cover legacy refresh/rejection, supported full tool turns/browser WS, actual inherited child execution, missing fallback/stale parent, explicit model-argument rejection, and settings/auth-method/model-search UI.

### Fixture repairs

Signed-out Google/Vertex statuses cannot advertise available models. Authentication-method interactions use that signed-out fixture; settings catalog and launch chooser assertions use a separate authenticated fixture. Router-owned Codex has ten models, not the helper's one-model projection. Settings asserts exact API-catalog cardinality and selected model identities; launch picker searches each exact model because unfiltered results are capped at ten. Antigravity search yields no matching models.

Child `max_turns=3` avoids forced-wrapup requests exhausting a one-turn fixture queue. Child completion and persisted selections, local provider request routes/auth/payloads, and persisted completion notices are asserted. Completion-mail model turns are separated from execution requests; a queued notice does not require a follow-up model request. Codex children use the SDK default HTTP client while roots use the owned injected client: the compile-only fail-closed transport now maps the expected Codex endpoint to the same deterministic local mock. No production source changed for these fixture repairs. Diagnostic write/attach is inside `try/finally`, so failure cannot bypass teardown.

### Final source and reproducibility

Final browser validation uses `/tmp/mecha-intended-prep/capture-20261102T051057595811Z/source`, with latest two provider test files copied from live. The adjacent `pi-packages` dependency link points to the maintained fork. Isolated UI dist bytes were copied from the previously built isolated capture, not from live and not rebuilt here. Concurrent migrations 120–123 remain excluded. The general harness SIGQUIT diagnostic is **not owned or integrated**: live harness untouched; validation uses the earlier capture's harness bytes. Capture manifests predate final fixture copies; supplemental final file hashes are recorded in `/tmp/agr-owned-final-manifest.json`.

Browser command (cached Go 1.26, offline Go dependencies):

```sh
export GOROOT=/home/wolfgang/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.26.0.linux-amd64
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off
export PATH="$GOROOT/bin:$PATH"
cd /tmp/mecha-intended-prep/capture-20261102T051057595811Z/source
timeout 550 npm run test:e2e -- tests/e2e/antigravity-removal.spec.ts --output=/tmp/agr-owned-rerun9-results
```

Final cleanup: all fixture routers/helpers, proxy executable children, frontend/mock servers and runner processes exited. Process scan after terminal exit found no `agr-e2e-*`, owned rerun/support jobs or Go proxy executables. The earlier interrupted-run proxy and first-test proxy orphan were terminated by exact PID after verifying snapshot cwd; no live application services touched. Cleanup evidence: `/tmp/agr-owned-cleanup.json`.

### Broad limits and remaining browser gaps

Broad Go/App suites are **not claimed green**. Provider-progress/Vertex RPC/reset-recovery failures reproduce on HEAD. Child-snapshot omission times out on both trees. App full runs are 10 pass/12 fail on each tree; reminder isolated reruns fail on both, and no scoped regression was established. Detailed baseline comparisons remain in the acceptance record. The stale Antigravity snapshot/listing fixture blocker was subsequently repaired and verified above.

AGR-005 grounding and AGR-006 legacy recovery remain supporting Go tests, not Playwright. AGR-007 browser test proves refresh/removal/revision; invalid/canceled/rollback/repeat and exact model replacement are Go SQLite tests. AGR-002 direct obsolete root admission is not established by the six browser tests; stale parent rejection is exercised after supported admission. The specification remains Draft and maps actual test titles, not invented browser tests. This follow-up completes the requested six-test fixture suite, not every possible browser scenario or broad regression suite. Parent owns integration and independent final review.

## Remaining production references

No active Cloud Code endpoint or Antigravity UI/helper registration remains in audited router/auth-helper/UI source/shipped JS. Names intentionally remain in:

- `internal/managed/models.go`: explicit deny guard.
- `internal/managed/{process.go,root_cold_recovery.go}`: obsolete env deny filters; CommandSpec historical ignored field.
- `internal/managed/config.go`: historical ignored field.
- `internal/store/{managed_root_recovery.go,managed_root_recovery_operator.go,managed.go}` and migration `086_google_auth_methods.sql`: inert serialized recipe/schema compatibility, historical hashes/column guards. Credentials untouched.
- Test preload retains synthetic token string names used by old OAuth mocking tests; no Cloud Code handlers remain. Historical/rejection/credential-preservation tests and removal spec/plan retain the name deliberately.

## Scoped paths (not the full dirty checkout)

- auth-helper/{providers.mjs,protocol.mjs,auth-helper.test.mjs,test-preload-mock-fetch.mjs}
- pi-extensions/mecha-messaging/compaction-policy.ts
- tools/run_router.sh; router-proxy/Makefile
- router-proxy/cmd/mailbox-router/{main.go,managed_adapters.go,managed_adapters_test.go,google_managed_runtime_integration_test.go,embedded_browser_controls_auth_test.go,embedded_codex_catalog_admission_red_test.go}
- router-proxy/internal/managed/{auth.go,auth_test.go,config.go,process.go,process_test.go,models.go,models_test.go,embedded_google_auth.go,embedded_google_auth_test.go,embedded_google_search.go,embedded_google_search_test.go,embedded_admission.go,embedded_session.go,embedded_subagent_runner.go,embedded_admin_tools.go,embedded_admin_tools_test.go,root_cold_recovery.go,supervisor.go,supervisor_reset_s1.go,supervisor_restart_s3.go,codex_browser_auth_test.go,embedded_codex_available_admission_test.go,embedded_codex_catalog_admission_test.go,embedded_context_threshold_lifecycle_test.go,embedded_history_startup_reconcile_test.go}
- router-proxy/internal/store/{managed.go,managed_test.go,managed_subagents.go,antigravity_removal_test.go}
- router-proxy/internal/transport/{managed_http.go,ui_live.go,managed_http_test.go,embedded_security_admission_http_test.go}
- web/router-ui-react/src/components/{ProviderAuthDialog.tsx,ManagedAgentDialog.tsx,ModelPicker.tsx}; src/protocol/{types.ts,guards.ts}; src/transport/http-client.ts
- web/router-ui-react/tests/component/{provider-auth.test.tsx,managed-settings.test.tsx,managed-agent-dialog.test.tsx,app-integration.test.tsx}; tests/unit/protocol-guards.test.ts; tests/transport/http-contract.test.ts; tests/functional/fixtures/fake-provider-auth.mjs
- router-proxy/managed-ui/index.html; removed four previous assets/index-*.js entry bundles; added assets/index-C00tVv2p.js and index-cMursH87.css
- tests/e2e/antigravity-removal.spec.ts; tests/e2e/antigravity-provider-fixture.ts; this plan. Existing removal spec preserved.

Shared dirty files contain unrelated hunks: parent must not integrate their complete diffs indiscriminately. No KB files or shared E2E harness modifications owned by this task.
