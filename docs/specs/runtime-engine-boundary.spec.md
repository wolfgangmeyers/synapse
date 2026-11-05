# Spec: Embedded Runtime Adapter Boundary

- **ID**: `SPEC-RUNTIME-ENGINE-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/runtime-engine-boundary.spec.ts` (partial coverage; acceptance pending)

Implementation is authorized and in progress on `claude-integration`. The active simple workflow is **Implementation**, not dev-loop. The Pi seam, shared tool authority and separate reset store/coordinator changes are implemented with partial focused validation. This specification remains **Draft** pending required E2E acceptance.

Companion: [Claude worker spec](claude-code-workers-spec.md) and [plan](claude-code-workers-plan.md). See the [root implementation evidence](../../CLAUDE_CODE_ADAPTER_PLAN.md#current-implementation-evidence) and [boundary coverage/gaps](../../tests/e2e/runtime-engine-boundary-coverage.md). The scoped 65-test browser run includes Claude and Pi cases; it does not establish the full scenario matrix. See the coverage report for exact evidence and gaps.

## 1. Intent & User Outcome

Keep the existing embedded worker experience while allowing Pi or Claude to do the work. Delivery, history, goals and resets should behave alike where useful. The UI should be near-identical, not force identical engine behavior. Claude may queue mid-turn input rather than fold it into a running response, and may report usage differently.

Recommendation: extract the smallest useful adapter for these two implementations. Isolate run/steer/interrupt/resource lifecycle and protocol translation. Reuse current router policy and persistence. Do not build a generic engine framework, capability system, event bus or universal transcript.

The first slice is Pi-only and behavior-preserving. No Claude branches, tool-name normalization, steer bugfix, SDK migration, store migration or wholesale session-construction rewrite.

## 2. Core Invariants

1. The router owns lane serialization, agent/session/epoch/generation/owner binding, admission, fencing and recovery decisions. Adapters do not select new bindings or bypass stale-owner checks.
2. Accepted input is not consumed input. Queueing a steer or writing stdin cannot ACK mail. The shared checked persistence path commits consumption and ACK using the router's reserved input and original representation.
3. Shared code owns history/publication, answer recording, goal eligibility/applied receipts/continuation, reset policy/preservation, and router-tool admission/effect certainty. Engine return values alone cannot prove a durable final, an ACK or a successful tool effect.
4. Interrupt stops current work; close releases engine resources. Neither operation independently resets context, deletes history or terminates the worker. Pi need not gain a resource manager. Claude stops and reaps its process group and revokes its grant at the end of each foreground execution, before another starts; uncertain cleanup retains its record and fences admission. No process or background authority persists between executions.
5. Run returns only after its callbacks settle, before shared execution settlement and active-handle removal. Failed checked writes/fences must stop subsequent provider/tool work under the existing rules.
6. The Pi extraction preserves current input bytes/order, cancellation, stop/error handling, nudges, context projection/fitting, provider-entry guards, tool refresh, wake completion and effect uncertainty. Known steering defects are not silently fixed.
7. Existing SDK-shaped message/checkpoint persistence remains an internal compatibility contract where practical. A public engine abstraction must not grow just to erase SDK names. Pi persistence conversion stays internal and temporary as a cross-engine bridge, not a promise that Claude must imitate every Pi object. No store-format change is implied.
8. No new accumulating data store is introduced. Active input correlation is bounded by the active turn and cleared after callbacks settle. Existing SQLite/history/checkpoint retention remains unchanged. Any later durable event log needs a separate bounded-retention design.

### 2.1 What the code actually couples

Audited at `cbd9352c` on `claude-integration`. Paths below are under `router-proxy/internal/managed/` unless stated otherwise. Function names are authoritative, not old plan line numbers.

| Code | Boundary implication |
|---|---|
| `embedded_session.go`: `runEmbeddedSessionTurnScoped` | Construction is not the only coupling. It installs provider/tool hooks and `TransformContext`, restores a session, registers it, dispatches prompts/recovery, reads `RunResult.StopReason`, `History`, `Model` and `LastAssistantText`. Keep construction mostly in place for the first slice; wrap the resulting Pi session rather than rewrite all setup. |
| Same file: `embeddedPersistence.AppendMessage`, `CommitCheckpoint`, `restore` | SDK message switches drive checked entry/ACK/history/goal/tool-result handling. Ordered restore and serialized message bytes are compatibility constraints. Reuse them first. |
| `supervisor.go`: `managedAgentLane.activeSession`, `Manager.activeEmbeddedSessions`, `InterruptAgent`, `SetEmbeddedTurnActiveForTest` | Concrete session registries and test sentinels need small adapter handles. Preserve locks and identity-checked unregistering. Merely wrapping a constructor does not isolate lifecycle. |
| `embedded_mail.go`: `trySteerActiveSession`, `buildSteerUserMessage`; `embedded_prompt.go`: `steerPrompt` | Shared code reserves and correlates input. Pi currently calls `Steer` without a receipt. Preserve rendering, image loading, timestamps and matching bytes. |
| `embedded_wake_persistence.go`: `runClaimedWake`; `embedded_restart_router.go` | Continue is not a fresh input append. Claimed-wake completion still requires the exact scope and checked final. Restart should interrupt through the active handle. |
| `embedded_context_reset.go`: `checkContextResetAfterTurn`; `embedded_automatic_fit.go` | Shared threshold policy can consume a small snapshot. Pi token estimation, context fitting and auxiliary summarization can remain Pi-specific. |
| `embedded_workflow_transition.go`: `SyncEmbeddedAgentTools` | Reaches `session.Agent.SetTools`. Keep Pi refresh through a private Pi helper/type assertion initially. Do not invent a general capability framework; Claude workflows remain outside v1. |
| `embedded_subagent_runner.go` | Separate concrete Pi sessions remain out of scope. Do not migrate native subagents in the worker adapter extraction. |
| `../store/embedded_router.go`, `embedded_reply.go`, `embedded_restart_wake_origin.go` | Checked entry, ACK, final and effect validation depends on current SDK-shaped bodies. Reuse the format; later Claude compatibility encoding must satisfy its validators, not bypass them. |

The old proposed `embeddedTurnSession` returns `coding.RunResult`, accepts `agent.AgentMessage`, and leaves callers dependent on `ai.Model`. It is a useful Pi wrapper, not yet a neutral protocol contract. That does **not** justify wholesale SDK removal. Hide only the details that force shared orchestration to understand Claude's protocol or reach into Pi sessions.

### 2.2 Small proposed contract

Semantic requirements below, not frozen Go signatures. Avoid adding methods until a real caller needs them.

**Inputs.** Give each submitted input a router identity plus source: initial instruction, mail/composer/agent/reminder provenance, explicit prompt, goal continuation, recovery notice, claimed wake or internal nudge. Keep optional message/attempt/request/wake IDs and binding in router correlation state. Equal text does not make two submissions the same input.

At the adapter entry, text and ordered images should be clear without parsing Claude JSON. An image carries validated MIME type and loaded bytes, using existing attachment validation. Original mail/envelope and serialized ACK bytes stay with the router. A Pi conversion helper may still use `agent.AgentMessage` internally; no need to migrate every existing call site or stored row to a new content schema. Preserve the distinction between ordinary mail and steer rendering.

**Run/continue.** Run ordered input, or continue checked history without adding a duplicate user. Return only what callers need: stop classification (normal final, tool continuation/turn-ending tool, aborted, error or absent final), last assistant text and error/cancellation information. Map `coding.RunResult` privately if a small outcome avoids branching on SDK results in shared code. Preserve today's special `StopError` plus error branches, including nudge/reset behavior.

**Steer.** Report accepted/not accepted if needed by the two adapters. Accepted means queued, not durable or answered. Pi keeps its extraction-time acceptance and match-then-head correlation behavior. The separately authorized reset coordinator/store fix now releases eligible definitely unsubmitted reservations atomically with the epoch transition after callback drain; uncertain Claude writes remain held on the durable message row. This is not an incidental adapter change; full reset/race verification remains pending.

**Interrupt/close.** An active-turn handle supports interruption without exposing `session.Agent`. Resource close is separate from turn completion. A no-op Pi close is sufficient if there are no resources to release. Claude process ownership belongs in its adapter/manager, not in shared turn policy.

**Snapshot.** Expose a read-only view of the existing needs: last assistant text, whether history has a user, current context tokens, model provider/ID and window. Preserve Pi's projected-token calculation and model-window fallback. Future Claude reported usage should be labelled as reported rather than silently treated as the same estimator. Unknown usage is not automatically zero. Sequence/digest, checked final and goal/effect receipts remain shared persistence facts, not trusted snapshot claims. A private Pi model/helper can remain for evaluator/context-fitting work.

### 2.3 Consumption, persistence and tool authority

Use current Pi ordered persistence callbacks as consumption evidence initially. Keep byte matching and `popSteerAttempt`'s current match-then-head fallback unchanged. Do not add new input IDs to serialized SDK messages merely to correlate them.

The existing `embeddedPersistence` is a practical bridge to checked SQLite entries, history and ACKs. Retain it as internal compatibility code. Claude can translate into the portions it needs through a small helper/sink; it must not pretend to be a full Pi session. Prefer targeted shared operations over a complete normalized event architecture.

A normalized sink is a **later option**, not a purity gate. Extract one only if implementing Claude would otherwise duplicate checked consumption, assistant/tool history or final policy. A small synchronous error-returning sink is enough: consumed input identity, assistant step, tool start/result, usage and outcome as needed. Preserve order, metadata and error propagation. Do not add a durable event store. Keep raw tool identity separate from display mapping, and defer Claude-specific mapping until Claude implementation.

Provider-entry duties remain shared policy even if their Pi hooks stay in place initially. `runEmbeddedSessionTurnScoped` currently starts notices, renews ownership, checks ordinary execution/headroom, marks non-mail entry, reads goal policy and records applied evidence. Auxiliary work still starts notices, renews ownership and reads goal policy; ordinary-only checks/markers/receipts keep their guards. For Pi an extraction preserves those pre-dispatch conditions. Claude pre-submit admission and checked echo/publication cannot imply a CLI per-request gate; use the detailed plan’s settled submission/background contract.

Router tools must keep admission, intent/completion/UNKNOWN and idempotency guarantees. Claude uses the settled canonical producer/admission/Execute/completion/checked-result path in detailed plan §6, not direct enqueue/browser-goal calls. Idle effects need a real admitted notification execution or fail; no fabricated turn or old-token reuse.

### 2.4 Sequence and decisions left open

1. **A: Pi-backed seam.** Wrap the existing constructed session; update active registries and required lifecycle/run callers. Add only small input/outcome/snapshot conversions where they reduce actual coupling. Keep persistence, tool setup and context hooks intact. No Claude or opportunistic bugfixes.
2. **B: targeted shared sink/helper, if needed.** Identify the minimum checked operations Claude needs, validate their ordering/error handling with Pi, and reuse current persistence. Full event normalization and broad SDK-type cleanup are optional, not prerequisites. If the existing bridge suffices, document that decision and proceed without another architecture layer.
3. **C: Claude adapter.** Add process/protocol translation, availability, MCP and near-parity UI after A is verified and the B tradeoff is settled. Re-review the Claude draft against the actual seam, not the obsolete interface sketch.

Settled integration choices: retain private SDK compatibility types and extract only the few admission/result operations required by Claude, with exact signatures following callers. Detailed plan §5 specifies pre-submit vs checked-echo authority, atomic reset release and bounds; §6 specifies the shared MCP path; §9 historically used only the usable configured Pi router default evaluator; the planned [Claude default contract](claude-router-default.spec.md) supersedes that restriction. No fallback search or new provider settings/polling. These choices do not change A's behavior-preserving scope. Claude scenarios CC-005/CC-018 cover the separately authorized reset/admission behavior. That atomic-release fix is now implemented; RE-008 follows the current reset contract. The pre-fix stranded reservation is historical evidence only, not a permanent compatibility requirement. No full scenario acceptance is claimed.

## 3. User Scenarios (Behavioral Contract)

These are Pi compatibility scenarios for A, repeated for any B extraction. Claude-specific differences remain in its draft spec.

### Scenario: [RE-001] Text and images complete normally
- **User Goal**: Keep normal conversation behavior.
- **Given**: An idle Pi worker and a deterministic local provider that records received content.
- **When**: The browser sends text and an image through the proxy.
- **Then**: The provider receives unchanged content/order; checked input and ACK commit together; the timeline shows one answer, activity returns idle and projected context usage updates. Nothing is mailed automatically.

### Scenario: [RE-002] Accepted steering is not receipt
- **User Goal**: See receipt only when input is consumed.
- **Given**: A Pi turn held at a deterministic provider barrier.
- **When**: A second mail is accepted as a steer, then allowed to reach the user persistence callback.
- **Then**: Acceptance alone creates no ACK. Checked consumption commits input/ACK. Equal-text inputs keep distinct router correlation and current Pi ordering/matching behavior.

### Scenario: [RE-003] Interrupt keeps the conversation
- **User Goal**: Stop work without losing history.
- **Given**: A turn blocked after partial output.
- **When**: The browser interrupts, sends another message, then terminates the worker.
- **Then**: Current partial-output/cancellation behavior remains; callbacks settle before idle; the next turn uses preserved history. Termination uses shared cleanup. Engine resource close alone cannot ACK input, change epochs or delete history.
- **Concurrent termination**: If mail dispatch is entering admission while termination starts, both operations make progress without deadlock. Termination prevents new turns from being admitted, durably revokes the worker, and completes shared cleanup. Concurrent reset or termination must not reopen admission prematurely.
- **Committed termination with a permission error**: When direct DELETE durably reserves termination but the subsequent SQLite state-file permission check fails, the caller sees the error, while the original reservation still interrupts and hands off cleanup exactly once. An identical retry replays the durable operation without a second interrupt. A reservation not conclusively committed must not authorize cleanup. Embedded tool cleanup retains its existing producer handoff rules.

### Scenario: [RE-004] Tools keep checked effects and history
- **User Goal**: Avoid repeated uncertain effects.
- **Given**: Scripted router mail and deterministic failing local tool calls.
- **When**: Tools run and the router restarts at a controlled post-intent/pre-completion boundary.
- **Then**: Tool cards, companion text and nudges follow current ordering. Shared records determine certainty; an UNKNOWN effect is not repeated or accepted as successful from the adapter outcome.

### Scenario: [RE-005] Goals and tool refresh still work
- **User Goal**: Keep continuation and permissions unchanged.
- **Given**: An applied goal, deterministic NOT_DONE/DONE evaluator responses and workflow restrictions.
- **When**: Turns/continuations run and workflow state changes while a session is active.
- **Then**: Applied-receipt eligibility, snooze/caps and DONE behavior remain unchanged; active Pi tools refresh. Auxiliary requests do not create ordinary applied receipts or entry markers.

### Scenario: [RE-006] Reset and recovery preserve context
- **User Goal**: Keep scratchpad and recent history.
- **Given**: A worker with scratchpad entries and recent conversation.
- **When**: Tool, threshold and browser resets run as separate cases, and router restart resumes a claimed interrupted wake.
- **Then**: Preservation order/window, epochs, thresholds and notices remain unchanged. Continue appends no duplicate user. Wake completion still requires the scoped checked final.

### Scenario: [RE-007] Stale or failed writes stop work
- **User Goal**: Prevent old work from changing the current conversation.
- **Given**: A turn held at a checked persistence/provider/tool boundary.
- **When**: Its binding becomes stale or a deterministic store fault rejects its next write.
- **Then**: Existing error handling stops later provider/tool entry; no false successful final/ACK is published. A stale snapshot cannot overwrite new-epoch usage. Settlement precedes deregistration.

### Scenario: [RE-008] Preserve error behavior and apply the approved reset release
- **User Goal**: Keep Pi error behavior without stranding unconsumed mail after reset.
- **Given**: Scripts producing StopError, thinking-only/missing finals, and accepted-but-unconsumed steering.
- **When**: Turns finish/fail, including reset while an unconsumed steer is reserved.
- **Then**: Existing nudge/error behavior and Pi match-then-head correlation remain unchanged. The shared reset coordinator blocks new admissions, interrupts and drains callbacks before the epoch transition. In the same SQLite transaction, reset releases only exact-old-binding attempts with neither ACK nor checked consumed-user entry and their append keys. ACKed/consumed attempts stay protected; rollback changes neither epoch nor reservations. Live queued mail retains its accepted identity/body. Only definitely unsubmitted eligible mail gets a fresh new-epoch attempt, checked consumption and ACK; a write-started Claude message without a checked echo remains held until existing expiry. Its durable message-row `claude_stdin_started` bit survives deletion of the old attempt and restart. Late old callbacks cannot consume or alter a replacement attempt. The stranded-reservation defect belongs to the historical extraction baseline, not this current contract.
- **Verification status**: The scoped green browser run passed the Pi reset/redelivery case and Claude CC-005 queued-mail reset. These do not prove every rollback, late-callback or uncertain-write race in this scenario; see the coverage report.

### Scenario: [RE-009] A shared helper rejects invalid consumption
- **User Goal**: Keep protocol mistakes from corrupting delivery.
- **Given**: A later B helper/sink correlates consumption to router inputs.
- **When**: Deterministic boundary fault injection supplies an unknown input or stale binding, or persistence rejects an append.
- **Then**: The operation fails without ACK/final or later engine work. Valid translations preserve A's entries/history/live observations. This scenario applies if such a helper is introduced, not as a demand to build it.

## 4. Automated E2E Verification Matrix

The suite now has partial coverage, with gaps recorded in the linked coverage report. The changed shared paths and their existing regressions remain acceptance requirements. The broader Pi compatibility matrix below is retained as behavioral specification and explicitly deferred where not exercised by scoped evidence; it is not claimed green. Cover changed paths and applicable regressions with headless Playwright, real UI, REST router, transparent Go router-proxy, WebSocket feed and isolated SQLite store. Replace only external LLM/provider/evaluator boundaries with deterministic local mocks. No external services or real Claude/LLM calls in automated tests.

Assert browser state, WS observations and checked SQLite ACK/entry/effect/wake records. Restart the real router over the same database for recovery. Use explicit local barriers and bounded waits, not sleep races. Exercise direct cross-origin browser controls as well as proxy transport where applicable. Internal JS mocks and isolated unit tests are not substitutes.

| Scenario | Acceptance criterion | Planned suite | Planned test title |
|---|---|---|---|
| RE-001 | AC1: input/conversation parity | `tests/e2e/runtime-engine-boundary.spec.ts` | `preserves text image ACK answer and usage` |
| RE-002 | AC2: accepted vs consumed | same suite | `ACKs steering only on checked consumption` |
| RE-003 | AC3: interrupt vs close | same suite | `interrupts without losing history and terminates through shared lifecycle` |
| RE-004 | AC4: shared effects/history | same suite | `preserves tool cards and uncertain effects across restart` |
| RE-005 | AC5: shared goals/tool refresh | same suite | `preserves receipts continuations and workflow refresh` |
| RE-006 | AC6: shared reset/wake policy | same suite | `preserves reset context and claimed wake checks` |
| RE-007 | AC7: fencing/error propagation | same suite | `stops stale or failed writes before further work` |
| RE-008 | AC8: error compatibility plus approved atomic reset release | same suite | `RE-008 approved reset releases and redelivers an unconsumed steer` (error/nudge gaps remain) |
| RE-009 | AC9: helper validation, if B adds one | same suite | `rejects invalid consumption and preserves valid translations` |

Supporting review: inspect concrete active registries and mail/prompt/wake/restart/reset/workflow consumers, not just the constructor. Existing Go suites must stay green; allow mechanical seam adaptation without weakening assertions. No broad SDK-import ban is required. Review whether protocol/lifecycle dependencies are actually isolated and every changed production file serves A's compatibility outcome.

### Scenario: [RE-010] Header controls remain reachable at constrained widths
- **Given**: A Pi or Claude worker with a long generated name is selected at 1000px or 1280px viewport width.
- **When**: The user clicks More agent controls and Reset for Pi, or Interrupt worker for an active Claude worker.
- **Then**: The model picker does not cover the controls; ordinary clicks activate the intended action. Reset preserves the recent context and scratchpad, and interrupt preserves the goal.
- **Verification**: `tests/e2e/runtime-engine-boundary.spec.ts` RE-006 browser reset at both widths; `tests/e2e/claude-code-recovery.spec.ts` CC-009 interrupt at both widths.


### Scenario: Mail arrives during a provisional termination gate

- **Given** an embedded worker has a termination admission in progress,
- **When** a mail offer encounters its gate and termination fails before commit or replays an existing operation,
- **Then** the queued mail is offered again once all overlapping gates permit admission, without another external message.
- **And** committed termination never admits another turn; uncertain Claude input remains held. Gate release never waits on the serial lane while holding admission.
