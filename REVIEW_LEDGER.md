# Review ledger

Target: native read-only tmux/router audit in `router-proxy`.

Run `./tools/hash_audit_target.sh` from the repository root. Current normalized-tar SHA-256: `1fc2c8f15fdf43d64e0372d58bee632742cfc1f541de3af5c9ae9160c2ac4eab` (`paths=607`). The script reads every tracked worktree path plus untracked `router-proxy/internal/transport/policy.go` and `router-proxy/internal/transport/policy_test.go`. It excludes `AUDIT_REQUIREMENTS.md` and `REVIEW_LEDGER.md`, then hashes a lexicographically sorted POSIX tar stream with fixed timestamp, owner, group, and mode metadata.

Disposition: blocking findings: none confirmed in the audit target. Should-add: stabilize the known lifecycle flake in `TestDifferentInstanceReplacementClearsStaleCompactingPause` separately; it is outside this target. Final authoritative coverage: `make coverage` reports 67.3%.

Final validation: package tests pass. `cd router-proxy && go test -v ./internal/transport -run '^(TestRouterDoesNotRegisterTmuxAuditWithoutConfiguration|TestTmuxRouterAudit.*|TestNativePolicy.*|TestDefaultPolicySender.*|TestRunAndStopPoliciesAcceptNilContexts|TestRouterCloseCancelsPoliciesBeforeReturning|TestAuditCoveragePredicates|TestRouterCloseBoundsNoncooperativePolicy)$' -count=1` passes. `cd router-proxy && go vet ./...`, `git diff --check`, and `gofmt -l` over all changed Go paths pass. The target hash `1fc2c8f15fdf43d64e0372d58bee632742cfc1f541de3af5c9ae9160c2ac4eab` (`paths=607`) matches `./tools/hash_audit_target.sh`.

Prior package-only coverage runs: 74.7% and 62.1%. These are historical package measurements, not the final coverage disposition.

| Focus | Reviewer/persona | Result |
| --- | --- | --- |
| requirements | Readiness audit / requirements reviewer | Pass: audit inventories tmux windows and live agents, checks goal or recurring-reminder coverage, and sends only a summary. |
| conventions | Readiness audit / Go conventions reviewer | Pass: follows existing router options, durable-store, and test patterns. |
| security | Readiness audit / security reviewer | Pass: reserves `router-audit`; summaries exclude agent IDs, goal text, reminder IDs, and reminder bodies. |
| performance | Readiness audit / performance reviewer | Pass: policy cadence is 30 minutes; policy work runs outside the sweep mutex. |
| architecture | Readiness audit / architecture reviewer | Pass: native-policy registry and narrow `PolicyContext` keep audit integration contained. |
| error-handling | Readiness audit / resilience reviewer | Pass: tmux, store, sender, ID-source, timeout, and shutdown failures are isolated and diagnostic. |
| data-integrity | Readiness audit / data-integrity reviewer | Pass: one unsettled durable audit slot prevents a growing offline queue. |
| api-design | Readiness audit / API reviewer | Pass: explicit CLI, Makefile, and environment configuration have validation and defaults. |
| observability | Readiness audit / operations reviewer | Pass: failures emit `router_error` with policy name and a stable error code. |
| comments-and-docs | Readiness audit / documentation reviewer | Pass: exported policy APIs and operational behavior are documented. |
| naming | Readiness audit / naming reviewer | Pass: policy, audit, sender, target, and session names describe their roles. |
| dead-and-unreachable-code | Readiness audit / code-hygiene reviewer | Pass: policy lifecycle paths are invoked from sweep and close paths. |
| structure-scale | Readiness audit / maintainability reviewer | Pass: audit logic is isolated in `policy.go`; existing router files contain integration points only. |
| observability-noise | Readiness audit / operations reviewer | Pass: clean audits are silent; failures use one stable diagnostic event; durable reports are deduplicated. |
| scope | Readiness audit / scope reviewer | Pass: diff is limited to native audit support, configuration, documentation, and tests. |
| plan-consistency | Readiness audit / delivery reviewer | Pass: implementation matches the stated read-only, periodic, durable-summary audit design. |
| concurrency | Readiness audit / concurrency reviewer | Pass: one policy invocation per name, bounded contexts, cancellation, and close synchronization are covered. |
| simplicity | Readiness audit / simplicity reviewer | Pass: uses the existing sweep, store, tmux adapter, and mailbox enqueue path. |
| diff-reading | Readiness audit / diff reviewer | Pass: every changed product path has a direct audit requirement or validation role. |
| architectural-seams | Readiness audit / seams reviewer | Pass: `PolicyContext` exposes only tmux inventory, live agents, reminders, clock/ID, and sender seams. |
| configuration-safety | Readiness audit / configuration reviewer | Pass: target, session, and timeout defaults are explicit; invalid empty or nonpositive values are rejected. |
| production-risks | Readiness audit / production-risk reviewer | Pass: audit does not provision tmux and cannot hold durable lifecycle recovery. |
| test-coverage | Readiness audit / test reviewer | Pass: final authoritative `make coverage` result is 67.3%; named audit tests cover success, isolation, cadence, delivery, dedupe, privacy, and shutdown. |
| documentation | Readiness audit / documentation reviewer | Pass: README documents defaults, environment overrides, cadence, delivery, and dedupe. |
| visual-consistency | Readiness audit / visual reviewer | Skipped: no UI or visual surface. |

Historical signal-race disposition: an earlier required combined run hit the known lifecycle flake after the signal fix. The final package-test validation passes, so this is not the authoritative package result or a confirmed audit-target regression. The earlier failed command was `cd router-proxy && go test ./internal/transport ./cmd/mailbox-router -count=1`: `internal/transport` failed after 27.737s at `TestDifferentInstanceReplacementClearsStaleCompactingPause` line 929 with `failed to get reader: use of closed network connection`; `cmd/mailbox-router` passed in 3.090s. Exact isolated reruns both passed: `go test -v ./internal/transport -run '^TestDifferentInstanceReplacementClearsStaleCompactingPause$' -count=1` passed in 0.065s (test 0.06s), then passed in 0.301s (test 0.08s). `git blame` attributes lines 900–940 to `327d5c61`; the current target has no diff hunk in that test. `TestCommandStartsWithDistinctDatabaseLogAttachmentsAndPort` also uses `-buildvcs=false` to control its pre-existing VCS-metadata build flake without changing product runtime behavior.

Pre-existing race disposition: `cd router-proxy && go test -race ./internal/transport -run '^TestExpiredActivityIdleFrameRetainsGoalAndCurrentModel$' -count=3` failed all three runs on this worktree. Each failed at `transport_test.go:749` with `Condition never satisfied` after 2.27s, 1.92s, and 2.69s. The same command on a detached clean `HEAD` worktree (`d73fe5c`) failed two of three runs at the unchanged assertion (`transport_test.go:727`) after 1.57s and 1.54s. `git blame` assigns this test to `bb7b9c35`; the audit target does not change its implementation or the activity-expiry path. The failure is pre-existing and outside this audit scope.

## Per-file evidence mapping

This ledger is evidence-only. It is excluded from the immutable code-target hash above.

| Path | Requirement or validated blocker served |
| --- | --- |
| `router-proxy/Makefile` | Configurable audit target, tmux session, and timeout launcher defaults. |
| `router-proxy/README.md` | Documents read-only audit behavior, cadence, overrides, and single-message retention. |
| `router-proxy/cmd/mailbox-router/main.go` | CLI configuration, validation, and native audit registration. |
| `router-proxy/cmd/mailbox-router/main_test.go` | Validates audit CLI defaults; controls the pre-existing VCS-metadata build flake. |
| `router-proxy/internal/store/delivery.go` | Validates the one-unsettled-message durable dedupe requirement. |
| `router-proxy/internal/transport/http.go` | Protects the reserved native audit sender from HTTP spoofing. |
| `router-proxy/internal/transport/lifecycle_test.go` | Adapts lifecycle snapshot coverage to context-aware live-agent lookup. |
| `router-proxy/internal/transport/live.go` | Runs observational policies outside the sweep mutex with bounded store reads. |
| `router-proxy/internal/transport/ownership_test.go` | Adapts ownership snapshot coverage to context-aware live-agent lookup. |
| `router-proxy/internal/transport/router.go` | Registers, configures, bounds, and shuts down native policies. |
| `router-proxy/internal/transport/transport_test.go` | Validates rejection of reserved-sender spoofing. |
| `tools/run_router.sh` | Passes audit environment configuration to the supported launcher. |
| `router-proxy/internal/transport/policy.go` | Implements the read-only periodic tmux/router audit, privacy-safe summary, isolation, cadence, and durable dedupe. |
| `router-proxy/internal/transport/policy_test.go` | Validates audit success, privacy, isolation, cadence, delivery, dedupe, and shutdown. |
| `AUDIT_REQUIREMENTS.md` | Authoritative audit contract, acceptance criteria, and non-goals; excluded from the code-target hash. |
| `tools/hash_audit_target.sh` | Reproducibly identifies the code target with normalized tar metadata. |
| `REVIEW_LEDGER.md` | Evidence-only review record, including scope mapping and validated pre-existing test failures; excluded from the code-target hash. |
