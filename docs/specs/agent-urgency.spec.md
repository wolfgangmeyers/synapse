# Spec: Explicit Agent Urgency Tool

- **ID**: `SPEC-AGENT-URGENCY-001`
- **Status**: `Active`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/agent-urgency.spec.ts`

## 1. Intent & User Outcome

Wolfgang can ask an embedded agent to mark itself or a named agent urgent, or clear that mark, using a router-native tool. Only marked agents display a small amber exclamation triangle. Unmarked rows have no urgency button, indicator, placeholder, or reserved space.

This supersedes the row/header toggle design. Workflows already use a bolt; urgency must not use one. The minimal UI is a passive active-only marker, not another action toolbar. There is no urgency header action, including an off-state action. Activation and deactivation both happen through the tool. No new command palette, dialog, or direct human tool-runner UI is required.

Urgency is explicit visual metadata, not blocked state, execution priority, or a workflow transition. No automatic activation, scheduling, sorting, pinning, notifications, urgency levels, animations, or worker instructions result from setting it. Agents should use the tool when explicitly directed, not infer urgency from task wording or failures.

Scoped acceptance is complete: the exact 51-file overlay passed all five native-tool browser cases and the supporting checks summarized in section 4. This is not a full-repository green claim.

## 2. Core Invariants

1. The primary path is the real embedded runtime, not tmux or a Pi extension. Embedded root and worker sessions expose `mecha_agent_urgency` subject to existing tool enablement and workflow allowlists. It is not a system-escape tool. Nested subagents do not acquire it automatically.
2. The closed tool input is `{ "urgent": boolean, "agent_id"?: string }`. `urgent` is required and never coerced or inverted. Omitted `agent_id` means the caller's router-bound logical ID. A supplied ID must be a nonempty exact logical agent ID, at most 128 characters; null, empty, whitespace-only, wrong-type IDs and unknown arguments fail. No name matching, source-prefix stripping, `from`, or model-supplied caller identity. Self and existing targets on the same router are supported; the target need not have a live connection.
3. Success returns `{ "agent_id": "...", "urgent": boolean }` only after durable commit. Explicit true activates; explicit false deactivates. Repeated same-value requests remain idempotent. Separate fresh calls use last serialized write wins. Replaying an old invocation must not reapply its value over a newer call. Invalid, stale, conflicting and absent/retired-target calls cannot mutate or create identities. Conclusive pre-commit failures do not publish success. Exact correlated ErrAgentNotFound rejection can produce a checked error through the original one-use admission witness. Storage, stale-fence, inconclusive and post-COMMIT failures are not blanket checked receipts. Storage or handoff failures after COMMIT can leave a durable change despite an error/UNKNOWN caller outcome. Do not claim rollback, checked success or replay authority. Reconcile current state and retry only with a fresh explicit call, never an automatic inverse write.
4. One shared SQLite boolean belongs to router + exact logical `agent_id`. It is not keyed by display name, browser, source, lease, or session. New identities default false, including managed identities before first connection. Existing managed and legacy projections may retain compatibility: two rows for one exact ID share the value, but navigation remains source-specific. This is not a requirement to launch tmux workers or add extension tooling.
5. Ordinary turns, disconnect/reconnect, context reset, worker/router restart, recovery, and same-ID replacement preserve urgency while the identity exists. Failed/pending termination preserves it. Final managed deletion or different-ID retirement clears and invalidates the old value even if mailbox/history retains its shared row. Stale saves cannot reactivate it, even after audit expiry. Legitimate new admission or successful new lease acquisition starts a retired identity non-urgent; renewal/enqueue are not admission. Already-active same-ID admission preserves the mark.
6. Retention is one in-place urgency boolean plus the existing design's same-row identity-validity boolean, not a toggle history, archive, tombstone registry, or parallel metadata system. Routine transport/reminder cleanup must preserve extant managed identities. Legacy final shared-row pruning remains its existing one-hour stale cutoff subject to message/reminder protections; urgency does not extend it. Final removal removes urgency; recreation starts false. No new accumulating ledger is introduced; native invocation/history records use existing bounded retention.
7. The internal/common `PUT /v1/agents/{id}/urgency` may remain. It accepts exactly `{ "urgent": boolean }`; missing/null/non-boolean values, extra/duplicate/case-alias fields, malformed/trailing JSON and non-object bodies return 400. Unknown/retired targets return 404 without insertion; store failure returns 5xx. A successful response has the same committed output as the tool. Existing inline-tools routes are unchanged. The route retains permissive non-credentialed browser CORS, arbitrary Origin support, and omitted credentials. It is not the user-facing activation mechanism.
8. Native tool and internal REST writes share mutation/identity validation and ordered post-commit publication. Both REST projections and existing WebSocket observer paths expose the committed value. Connected browsers converge without reload; reload/reconnect/restart restore it. Failed reads do not fabricate false, and stale HTTP responses or old-router events cannot replace newer authority. No per-row polling or new socket is needed.
9. A confirmed true renders one small, steady amber **exclamation triangle** (12px Font Awesome Free 6.7.2 font glyph, with bundled local CSS and webfonts available without external network access), accessible as `Urgent`. It is passive, not a button, not focusable, and has no toggle or `aria-pressed`. No bolt, repeated `Urgent` text badge, amber rail, card recoloring, or animation is required. False renders nothing, including on hover/focus. The conversation header has no urgency action or duplicate urgency marker. Keep existing blocked, selected, progress, compacting, workflow, and lifecycle presentation unchanged. The triangle must not truncate the title or grow the card height.
10. Tool failures stay in the ordinary tool-result/conversation error channel. No row/header saving text, error paragraph, retry button, pending placeholder, or off-state control. The passive marker changes only with authoritative confirmation. Retrying uses a new explicit tool call with the desired value, not a UI toggle. A committed change may appear in another browser even if the caller lost its result; reconciliation recovers current state.

### Upgrade boundary

Migration 124 initializes retained pre-feature identities using available final-deletion evidence: successful completed TERMINATED lifecycle actions or final-deletion archives (including `cleanup_failed` when final cleanup removed the live managed identity). Pending/failed termination without final cleanup is not retirement evidence. Known retirement initializes the retained ID inactive. An extant managed identity or a strictly later real lease admission, evidenced by first-seen/connection time, overrides historical deletion.

If both historical evidence sources expired before upgrade, retirement cannot be reconstructed. Ambiguous retained rows keep the compatibility default active. Equal-clock pre-feature transport admission cannot be distinguished from an older connection at that timestamp: known retirement wins unless extant managed identity or strictly later admission proves otherwise. This is an upgrade-only information boundary, not a relaxation of runtime retirement guarantees.

After upgrade, the serialized `identity_active` bit is the sole identity authority. Genuine same-clock admission is correctly serialized and starts a retired identity false. Setters do not consult ongoing audit history or infer authority from timestamps. Audit expiry cannot reactivate a retirement recorded by the new runtime.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [URG-001] Explicit native activation and deactivation
- **User Goal**: Mark self or a target without cluttering the agent list.
- **Given**: Real embedded root and worker sessions, an existing target (including a never-connected managed identity), and initially unmarked rows.
- **When**: A deterministic provider invokes the exposed tool with `{"urgent":true}`, then an explicit target ID, then clears each with `urgent:false`.
- **Then**:
  - Omitted target resolves to the caller; explicit target resolves exactly, independent of selection.
  - Checked tool results contain the resolved ID and committed boolean; SQLite and live observers agree.
  - Only marked rows show the accessible 12px amber Font Awesome exclamation triangle, including when external requests are blocked. Cleared/unmarked rows show nothing, even on hover/focus.
  - No urgency button, bolt, rail, repeated text badge, header action, header marker, or retry block appears. Selection, titles, height, order, and unrelated agents remain intact.
  - Tool availability follows enabled tools/workflow policy; the implementation is not merely a REST or extension-only stub.

### Scenario: [URG-002] Synchronize and restore confirmed state
- **User Goal**: Trust the same mark across browsers and restarts.
- **Given**: Two independent browsers observe embedded workers through the real router and proxy.
- **When**: The native tool activates and clears urgency; clients reload/reconnect; the isolated router restarts with the same SQLite store.
- **Then**:
  - The other browser updates via existing WebSockets without reload.
  - Reload/reconnect and same-store restart restore both saved true and saved false.
  - Older responses/events cannot restore a cleared marker. A legacy projection for the same ID, when present, agrees without changing navigation.

### Scenario: [URG-003] Keep urgency independent of execution and workflow
- **User Goal**: Mark attention without changing work or obscuring existing state.
- **Given**: An embedded worker is blocked and selected, with workflow/progress/lifecycle presentation visible.
- **When**: The tool marks and clears it, selection changes, ordinary state updates occur, and a representative reconnect/context reset happens while marked.
- **Then**:
  - Blocked/selected/progress/compacting indicators remain intact. Workflow bolts are unchanged and distinguishable from the urgency triangle.
  - Surviving identity retains urgency through ordinary lifecycle updates.
  - No messages to the target, goal changes, interruption, rescheduling, reordering, automatic urgency, or urgency animation occur.

### Scenario: [URG-004] Reject bad calls and recover without false UI
- **User Goal**: Know whether the explicit request was saved.
- **Given**: Known saved values, an active embedded caller, and two observing browsers.
- **When**: Calls contain invalid input, unknown/retired targets, stale ownership, repeated/conflicting invocation IDs, or a controlled storage/publication/result-loss failure. A fresh call later retries an explicit value. The internal PUT is also exercised with strict validation and arbitrary Origin.
- **Then**:
  - Invalid/stale/conflicting calls cannot mutate or create identities. Repeated invocation cannot overwrite an intervening newer write; separate fresh same-value calls do not invert.
  - Pre-commit rejection publishes no success and leaves other observers unchanged. The tool returns a useful error without a row/header error block.
  - After commit with lost result, the caller does not claim an unverified outcome or invert the value. Other observers and reconnect converge on saved state; a new explicit call can safely retry.
  - The internal route retains validation/status/CORS behavior and shares publication order with native calls. Inline-tools policy is unaffected.

### Scenario: [URG-005] Bound metadata to logical identity lifetime
- **User Goal**: Preserve marks during use, not after final removal.
- **Given**: New/default-false identities and marked existing embedded agents.
- **When**: Calls repeatedly set/clear; managed cleanup/recovery runs; same-ID and different-ID replacements occur; final deletion retains unrelated mailbox data; audit history expires; stale saves arrive; legitimate new admission occurs.
- **Then**:
  - Toggles update one boolean without accumulating urgency history.
  - Managed shared rows survive routine sweep/reminder cleanup; same-ID recovery and failed/pending termination preserve urgency.
  - Different-ID retirement/final deletion clear and invalidate the old value. Stale tool/REST saves still fail after audit expiry.
  - Genuine admission/reactivation starts false; lease renewal/enqueue cannot reactivate. Frozen clock values do not change identity authority.
  - Upgrade initialization honors available final-deletion evidence and proven later/extant admission under the upgrade boundary above. Expired evidence and ambiguous pre-feature equal-clock admission follow that boundary only.
  - Legacy final pruning, if exercised for compatibility, removes urgency without extending existing retention. Unrelated mailbox/history retention is unchanged.

## 4. Automated E2E Verification Matrix

Final scoped acceptance: the exact 51-file archive at base `4cfa57338311c09158a4869ce2d6fdb87c40043e`, patch SHA256 `ff7cf8f40afdb81049ccb8aa201bdbdac302a6aaf17d2b75ddf5cf3ec1a33873`, passed all five Chromium cases (one worker, zero retries, exit 0, 191.9s). Normal production and tagged fixture Go builds, scoped urgency suites in `internal/store`, `internal/managed` and `internal/transport`, UI typecheck and production build all exited 0. Production/test sources and production dist remained unchanged. Evidence: `/tmp/urgent-native-final-overlay-validation.md` and `/home/wolfgang/code/.urgent-validation-Crv9iyKA/browser-final.log`.

Archive execution required the local pi-packages sibling, auth-helper dependencies with deterministic helper fetch preload, safe TMPDIR and `-buildvcs=false` for the archive without Git metadata. These environment corrections did not change source or assertions. Parent acceptance closes the scoped supporting/compositional disposition; detailed proof boundaries below remain. No exhaustive scenario permutation or full-repository green is claimed.

Independent screenshot review (`/tmp/urgent-native-qa-requirements.md`) passed the active selected/blocked/progress row, narrow-title row and cleared state: distinct triangle/workflow bolt, usable title, unchanged row structure and no off-state/header urgency surface. The separate narrow-width header overlap/clipping observation is outside this unchanged header layout. Screenshots prove appearance, not fault recovery. Browser assertions verify actual locally bundled Font Awesome Free 6.7.2 glyphs with external requests blocked; automated glyph checks alone are not visual approval.

Use fast Playwright tests with a real headless browser, router-proxy, router, REST, WS, SQLite, and actual embedded tool registration/execution. Replace only the external provider with deterministic local tool-call responses and use controlled boundary failures. Do not substitute mocked UI setters or a direct PUT for native-tool execution. No live workstation router, tmux requirement, external services/LLMs, or arbitrary sleeps.

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `URG-001` | Native self/target calls and active-only triangle | `tests/e2e/agent-urgency.spec.ts` | `sets and clears urgency through embedded native tools without off controls` |
| `URG-002` | Two browsers, WS and persistence | `tests/e2e/agent-urgency.spec.ts` | `synchronizes native urgency across clients reload and restart` |
| `URG-003` | Independent execution/workflow presentation | `tests/e2e/agent-urgency.spec.ts` | `keeps active urgency triangle independent of execution and workflow state` |
| `URG-004` | Tool/API errors, fencing, replay and result loss | `tests/e2e/agent-urgency.spec.ts` | `rejects invalid urgency calls and reconciles uncertain outcomes without row errors` |
| `URG-005` | Lifetime and bounded retention | `tests/e2e/agent-urgency.spec.ts` | `bounds native urgency to logical identity lifetime` |

### Supporting evidence and proof boundaries

Supporting provenance: `/tmp/urgent-native-final-backend-review.md`, `/tmp/urgent-native-gap-tests.md`, `/tmp/urgent-native-composed-loss.md` and `/tmp/urgent-native-qa-followup.md`. Final archive checks above supersede earlier execution snapshots, not their qualified proof boundaries.

- URG-001: `TestEmbeddedAgentUrgencyActualRegistry` executes actual SDK root and worker self/target true/false against SQLite. `TestEmbeddedAgentUrgencySDKAvailability` exercises actual configured-disabled and workflow-denied attempts: checked durable errors, no active urgency tool, no intent and no mutation. `TestEmbeddedAgentUrgencyPolicyAndSchema` remains supporting helper policy/schema evidence.
- Nested admission: `TestManagedSubagentAdmissionUrgencyHydration` and `TestManagedSubagentAdmissionUrgencyRollback` cover real migrated-store new/active/retired admission, exact-ID authority and transactional rollback without child lease/bootstrap. `CreateManagedSubagent` in `store/managed_subagents.go` now establishes shared identity in the admission transaction. The reproduced urgency hydration regression is fixed and independently reviewed GREEN. `TestEmbeddedAgentUrgencyNestedRuntimeAbsent` executes actual SpawnSubagent/SDK omission, checked attempted-call denial and durable completion without manually seeding a child lease; it retains checked system transcript bootstrap for the separate empty-transcript restore limitation. Evidence: `/tmp/urgent-native-nested-admission-fix.md`, `/tmp/urgent-admission-validation/` and independent review `/tmp/urgent-native-qa-followup.md`. This is not generic ordinary-runtime acceptance.
- Original values: `TestEmbeddedAgentUrgencySDKNullTarget` proves actual SDK admission rejects an original null target. The browser invalid-call case also exercises actual malformed inputs. Helper schema tests support other variants. Schema/fence variants are not exhaustively proven.
- URG-004: `TestEmbeddedAgentUrgencyFenceAndReplay`, `TestEmbeddedAgentUrgencyUnknownCannotReplayOverNewerWrite`, and `TestEmbeddedAgentUrgencyTargetsAndFailures` cover real-store stale current/process/user authority, checked/UNKNOWN replay and conflicting IDs/targets/errors. `TestEmbeddedAgentUrgencySDKProcessFence` adds an actual SDK process race. `TestEmbeddedAgentUrgencySDKUnknownTargetCheckedError` and `TestEmbeddedAgentUrgencyRejectedResultFenceAndReplay` establish correlated exact checked ErrAgentNotFound rejection, one-use error observation, rejected forgery/stale-owner/replay and no target creation. This is not authority for general error-to-observed conversion. Token takeover/epoch/runtime replacement variants are not exhaustive.
- URG-004 controlled loss: `TestEmbeddedAgentUrgencySDKLostHandoffReplay` injects coordinator failure strictly after real domain COMMIT. `TestEmbeddedAgentUrgencyPostCommitErrorReplayAndFreshRetry` exercises real SQLite COMMIT followed by state-file failure. They establish UNKNOWN/no checked success, newer false protected, old invocation rejected and fresh explicit true succeeding. These are supporting manager/store proofs, not real socket-publication failure or full browser recovery after injected loss. Observer reconnect alone is not lost-native-result proof.
- URG-002/004 composed loss and ordering: `TestAgentUrgencySDKComposedLossAndOrdering` passed both `lost-result` and `native-rest-order`, package 4.949s, exact exit 0 (`/tmp/urgent-native-composed-loss.md`). Actual embedded root Manager/SDK provider-issued calls, real HTTP prompt admission, SQLite and both direct router WebSocket paths prove checked-result/handoff loss after successful COMMIT/publication: UNKNOWN/no checked result, true observer events/projections and fresh-connection reconnect snapshot, newer REST false protected against original-ID replay, then fresh native true with checked success and true/false/true observer order. A barrier-controlled admitted native-versus-REST interleaving proves both socket paths observe native true then REST false and final store/projections/reconnect agree false. This closes the prior setter-seam limitation for that controlled interleaving, not an exhaustive race matrix. It does not inject dropped publication/frames, broken sockets, pre-publication failure or post-COMMIT storage failure. No reverse proxy, browser/reducer instance, rendering or row/header-error assertions are included. Ordinary browser rendering/convergence evidence remains separate. The older `TestAgentUrgencyPublishesBothSocketsInCommitOrder` remains supporting coordinator/public-setter socket evidence.
- URG-005: supporting real-store migration coverage exercises retained deletion with both evidence sources, action-only and archive-only evidence, later lease admission, and extant managed readmission. It checks public and admitted native writes, actual audit pruning, and false-start readmission. The focused backend review closes the retained-deletion MustFix within the bounded upgrade contract above, not the whole feature. Post-migration E2E cannot establish upgrade correctness.

These supporting proofs and final browser acceptance close the scoped feature acceptance, not full-repository validation. The unrelated old addressed-store fixture failures remain outside this suite. Catalog exposure, managed hydration, lifecycle-disabled connected snapshots, sweep/reminder guards and atomic deletion versus mutation remain contract obligations supported by the scoped Go coverage; test names alone do not establish exhaustive verification. Controlled-time coverage exercises real audit pruning, stale retirement and frozen-clock admission. Direct-socket composed loss/order evidence supplements ordinary browser convergence; it is not proxy/browser fault injection.
