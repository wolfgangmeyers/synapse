# Spec: Workflow Run Lifecycle Journal

- **ID**: `SPEC-WORKFLOWS-JOURNAL-001`
- **Status**: `Draft`; acceptance incomplete
- **Domain**: `Workflows`
- **Target E2E Suite**: `tests/e2e/workflow-run-journal.spec.ts` (12 tests exist; acceptance incomplete)

## 1. Intent & User Outcome

People can browse a managed agent's retained workflow runs, see what happened and where time was spent, ask the agent to add notes, and delete completed runs. The journal is durable but bounded. It cannot reconstruct unjournaled history. Existing workflow authorization, goals, snooze and return behavior remain intact.

## 2. Core Invariants

1. Every successful activation creates a unique run, including same-workflow reactivation and scheduled or background launches. A run is `active` until exit or an actual binding-ending/replacing administrative action, then `terminal` with outcome and server terminal time. Cross-workflow transitions continue the same run. Snapshot agent ID/name, workflow ID/name, initial state ID/name and server start time; snapshot both workflow and state ID/name on each visit. Later rename or definition deletion does not rewrite snapshots. Each visit, including a self-transition, has its own stable identity. A monotonically increasing per-run event sequence orders state entries, explicit/implicit returns, exits, administrative changes and notes, even across timestamp ties or rollback. State identity for analytics is `(workflow ID, state ID)`, never name or state ID alone.
2. In one SQLite write transaction validate binding generation and current authorization, update binding and predecessor, update run status, append lifecycle events and apply transactional retention. Journal failure rolls back binding changes. Stale, rejected and failed attempts create no lifecycle event. Competing mutations serialize; a stale contender cannot overwrite the winner. In particular, exit must re-read and validate generation, current state and that state's current exit permission in the same transaction as binding clear and terminal event; the baseline exit path is currently unfenced and is not evidence of this future guarantee. Existing explicit-edge precedence and implicit-return eligibility remain unchanged.
3. Each mutation uses a stable operation ID scoped to agent and operation kind: managed tool invocation ID or HTTP idempotency key. Persist the ID, canonical request fingerprint and result atomically. For managed transitions and exits, the result includes the exact synchronous response, including expanded guidance, constructed before the mutation transaction commits; replay checks the receipt before reading current agent or binding state. A retry with the same ID and payload returns the original result without duplicate events or repeating effects; same ID with a different payload conflicts. New intent requires a new ID, including same-workflow activation. Retain receipts for 30 minutes from commit, scoped by agent ID and operation kind, independently of manual run deletion, pruning and agent deletion. At the deadline the receipt expires; reuse is a new attempt subject to current admission and existing replay fences and may repeat an effect. Cap the separate receipt ledger at 100,000 receipts and 200 MiB. Never evict an unexpired receipt for capacity: reject new mutations atomically with a capacity error, while existing receipt replay and conflict remain available. Clean up on startup and at least once per minute using a persisted nondecreasing expiry cutoff. Preserve any longer existing replay fences. Receipt results do not restore access to deleted history. A server-generated ID returned to a keyless HTTP caller does not make an ambiguous lost response safely retryable. Notifications, reminders, monitors and guidance remain post-commit effects; missed WebSocket updates recover by REST refresh, not an exactly-once delivery promise.
4. Every successful agent transition and exit synchronously returns stable run and event IDs and prompts for a concise human-facing entry about outcome, important decisions, obstacles and next steps when relevant. Ask for plain language, no jargon, routine noise or fabricated facts. Append this prompt to the existing synchronous result: preserve transition success text and new-state guidance and exit success text verbatim. Do not promise durable delivery or crash retry, claim an entry exists before it is written, or block the transition on composition. Optional `reason` is distinct from the entry; blank means absent. Reasons, notes and entries are plain text, at most 4096 UTF-8 bytes each; reject oversize rather than truncate. Apply reason validation to transition/exit and applicable administrative/API inputs.
5. A discoverable `workflow_journal_note` tool lets the owning managed agent proactively add useful notes or respond to a human request. Ordinary notes require an active run with a current visit and target that visit. Prompted entries explicitly target the returned run/event, even after exit or a later transition. Check ownership and tool admission; reject unauthorized, restricted, blank, oversized or no-active-run requests without writes. A missing, deleted or pruned explicit target returns not-found, never attaches elsewhere. Record source (proactive, human-requested or prompted entry) and server timestamp. Ordinary conversation is not automatically a note; subagents cannot write another agent's journal.
6. Agent-scoped list and detail/timeline APIs are paginated, with agent, workflow and status filters. Workflow filter matches workflow IDs in retained visits plus preserved current-visit metadata, not evicted historical visits or the initial snapshot alone. A truncated run may stop matching an old workflow; disclose retained-only filtering. Run list order is start time descending then run ID descending; timeline is sequence ascending. Stable keyset cursors encode filter scope and ordering; reject invalid or mismatched cursors. Concurrent inserts/deletes cannot duplicate already returned rows; deleted rows can disappear. Detail exposes snapshots, events, source, reasons, entries, notes, durations, completeness and cutoff. No orphan-history route exposes runs of deleted agents.
7. The selected-run chart sums all measurable retained visit durations independently of timeline pagination, using a single server-side read snapshot per response, per workflow/state ID pair, allowing a null workflow ID for unassigned states. Each visit ends at the next visit, a state-losing administrative change, a state-identity move, or terminal event; an active current visit ends at server observation time. Clamp each elapsed duration to zero if the clock moves backward; order follows sequence. A synthetic legacy start measures only from migration time. Evicted visits with unknown boundaries contribute no invented duration; expose partial totals and retained-history cutoff. This is not a chart of completed-run durations.
8. **Settled retention defaults:** Terminal runs and their events remain available until the exact 90-day boundary after terminal time, then expire at that boundary. Expiry is inclusive: an item is expired when `terminal_time + 90 days <= cutoff`. The persisted, nondecreasing sweep cutoff is `max(previous cutoff, server now)` and governs expiry eligibility and physical pruning; the API retained-history cutoff is the boundary reported to clients for history no longer available and is distinct from the persisted sweep cutoff. Sweep on startup and at least once per minute. A forward clock jump can advance expiry early; rollback cannot lower the persisted sweep cutoff or restore expired history. Downtime or database errors can delay physical deletion, but APIs must apply expiry against the persisted cutoff. Globally cap terminal runs at 100,000, evicting oldest terminal time then run ID ascending, transactionally on closure and sweeps. Active runs are never removed by retention. Bound every active or terminal run to its newest 10,000 events, including prompted entries after exit; evict oldest transactionally on every append. Preserve bounded current-visit identity, start and snapshot separately if its entry is evicted, and preserve terminal outcome and time separately if the terminal event is evicted. Evicted events are not explicit entry targets, even when current-visit metadata survives. Evict associated content with its event; do not keep an unbounded visit or workflow index. Explicitly disclose truncation, retained-history cutoff and partial analytics; timeline pagination alone does not make the chart partial; do not invent evicted durations or block transitions/exits at the cap. Disclose possible earlier global-cap eviction and background-agent deletion. Agent deletion separately removes all its active and terminal runs and associated journal records. Pruning and manual deletion never alter execution evidence or replay fences.
9. Migrate existing active bindings once in a transaction into marked synthetic/incomplete active runs at migration time and current state when present; a partial binding without a state starts without a visit. Do not infer prior events, reasons or durations. Repeated migration and cold restart retain the same active run without a duplicate start. Disclose that unjournaled historical runs cannot be reconstructed.

## Binding-writer inventory and transaction mapping

This inventory describes the inspected baseline; the journal transaction is required future behavior. Definition FK effects must not bypass journaling. Preserve existing lifecycle and cleanup behavior instead of assuming that every administration action ends a binding.

| Writer / entry path | Baseline effect and required journal transaction | Scenarios |
|---|---|---|
| `store/managed.go`: `setManagedAgentActiveWorkflowWithVariablesTx`, `SetManagedAgentActiveWorkflowWithVariables`; callers `transport/managed_http.go`, `managed/embedded_workflow_admin_tools.go`, `managed/background_workflow_runner.go` including scheduled runs | Activation creates a run. Replacement, even same workflow, terminates old and starts new atomically. Nil state assignment deactivates an existing binding, even if a workflow ID is supplied. Background launch reserves the agent, binding and initial journal in one transaction before starting its worker; failure starts no worker and leaves no agent reservation. A failed launch before binding creates no run; a committed binding is not retroactively erased by a later failure except through agent deletion. | WJ-01, 05, 08, 09 |
| `store/workflow_transition_authorization.go`: `AuthorizeWorkflowTransition`, `setManagedAgentActiveWorkflowStateTx`; `managed/embedded_workflow_transition.go` | Authorized explicit, implicit, self and cross-workflow transitions change state and predecessor; append a visit in the same fenced transaction. The state-only setter does **not** update `active_workflow_id` on a cross-workflow edge: target state's workflow ID can differ from binding workflow ID. Snapshot and aggregate by the target state's actual workflow/state pair, while preserving binding workflow ID and existing return/authorization semantics. | WJ-01, 02, 09 |
| `managed/embedded_workflow_transition.go` exit via `SetManagedAgentActiveWorkflowWithVariables(nil)` | Baseline exit checks permission outside the clearing transaction and lacks a generation fence. New transaction must validate generation, current state and current exit permission, then clear binding and append terminal event atomically. Keep existing post-commit effects. | WJ-01, 02, 09 |
| `store/managed.go`: `SetManagedAgentActiveWorkflowState` / state setter | State-only administrative replacement clears predecessor via binding trigger. A non-null replacement retains the run and records an administrative visit; null state assignment clears workflow, state and variables and terminates the run, preserving baseline behavior. No production direct caller was found, but the shared boundary must remain safe. | WJ-08, 09 |
| `store/workflow_states.go`: `DeleteWorkflowState`; `store/workflows.go`: `DeleteWorkflow`; `store/workflow_import_export.go`: `ReplaceAllWorkflows`, called by `transport/git_sync_http.go` | Active state/workflow and predecessor FKs use `ON DELETE SET NULL`; state delete rejects referenced transitions, while workflow deletion and replace-all delete states first. Capture affected bindings and snapshots before FK actions in the deletion transaction. Close a run only if binding actually ends; if a partial FK change leaves it continuing, record a nonterminal administrative change, not a new activation. Predecessor-only cleanup is not a visit. Failed/in-use deletion changes nothing. | WJ-08, 09 |
| `store/managed.go` managed-agent removal/replacement; `store/managed_background.go`, `store/managed_subagents.go` bulk cleanup; `managed/background_expiry.go` and explicit deletion | Every actual agent row deletion, including launch replacement, failed-start cleanup and expiry, atomically removes that agent's journal records. Termination without actual deletion does not itself erase history. Other agents remain untouched. | WJ-05, 08, 10 |
| `store/migrations/091`, `101`, `104`, `130` and startup migration | Existing FK/trigger updates and legacy bindings need migration-aware handling; synthesize once in the migration transaction, not as normal activation. | WJ-07, 08 |

**Partial bindings:** A run continues while either binding workflow ID or state ID remains non-null; it ends when both are null or an explicit replacement or exit ends it. FK removal of the state closes the current visit at that administrative event. If workflow ID remains, the run is active without a current visit: ordinary notes fail with no-current-visit; a later state-only assignment starts a visit in the same run. If workflow ID alone clears, preserve the current visit using the state's actual identity and record the binding change without a new visit. Both clearing in one deletion transaction produces one terminal event. Legacy partial bindings follow these rules; a synthetic run without a state has no visit until assignment. Journal code must not repair binding columns or change existing authorization.

**State moves:** Moving an active state's actual workflow ID atomically closes its old visit and starts a distinct administrative visit at the move time with the new `(workflow ID, state ID)` snapshot, including null workflow ID for an unassigned state. Attribute duration before and after the move to their respective pairs. Do not change binding, predecessor or run, or trigger transition effects. Rename alone does not split visits or rewrite snapshots.

**Retention rationale and clock limits:** 90 days preserves recent history, 100,000 terminal runs bounds fleet-wide history, and 10,000 events per run bounds long sessions and post-exit entries. These are product defaults, not measured capacity guarantees; active-run count follows agent lifecycle rather than the terminal cap. Payloads must also be bounded. A forward clock jump can advance the persisted sweep cutoff and expire history early; rollback cannot lower that cutoff or restore expired history. A run closed with a rolled-back server timestamp can be immediately eligible when its terminal time is at or before the persisted cutoff minus 90 days. Keep actual server timestamps, order by sequence and clamp negative elapsed durations. Downtime or database errors can delay physical cleanup; retry visibly, and apply expiry against the persisted sweep cutoff before physical cleanup. The API retained-history cutoff describes the history boundary exposed to clients and is not the persisted cleanup cutoff.

The `workflow_binding_generation` trigger increments generation and clears predecessor on binding-column updates, including same values; a normal transition subsequently installs the old state as predecessor. Preserve this fence. `UpdateWorkflowState` may move a state definition without changing binding columns; preserve binding and split the visit as specified above if its observed identity changes. Definition renames do not rewrite past snapshots.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [WJ-01] Follow a run through visits and exit
- **User Goal**: See a faithful timeline and state-time distribution.
- **Given**: An activated agent has explicit, implicit-return, cross-workflow and self edges.
- **When**: It visits them and exits, with and without reasons in separate runs.
- **Then**: Each activation has a unique run and each entry a sequenced visit; returns and exit are distinguished. Reasons stay separate from entries. Durations are visible and revisits aggregate only by actual workflow/state pair, even when binding workflow ID differs or names match. Current active duration uses observation time and rollback clamps to zero. Blank reason is absent; oversized reason is rejected without mutation.

### Scenario: [WJ-02] Write prompted entries and notes
- **User Goal**: Capture useful context without blocking work.
- **Given**: An admitted agent receives a successful transition or exit result.
- **When**: It writes an entry using returned run/event IDs after exit or another transition, and adds proactive and human-requested notes.
- **Then**: Existing guidance is preserved and every result prompts for the specified human-facing content. Entries attach to the exact event, notes to the active visit, and source/time survive reload. An unwritten entry is not shown as written; chat alone adds none. Blank, oversize, no-active, unauthorized, restricted-tool and missing/pruned-target calls fail without writes. Delivery after crash is not promised.

### Scenario: [WJ-03] Browse retained runs
- **User Goal**: Find agent history even outside a workflow.
- **Given**: A selected managed agent has interactive, background or scheduled runs across workflows, and another agent has runs.
- **When**: User opens the distinct labeled Workflow journal header action, filters and pages, selects a run and expands a visit.
- **Then**: Only the selected agent's retained runs appear, including inactive workflows; filters and cursors follow invariant 6. The selected-run chart shows per-state time; a sci-fi vertical scrollable timeline shows every visit duration before expansion and reasons, entries and notes on expansion. The workflow badge still opens management. Loading, error and empty states are clear; text renders safely. Controls are labeled and keyboard accessible, use dark glass/blue accents and 24px `.icon-btn-round` actions, and work at narrow widths. Deterministic desktop and narrow screenshots cover visible durations and scrolling. REST refresh recovers after WebSocket disconnect.

### Scenario: [WJ-04] Delete a completed run
- **User Goal**: Remove unwanted retained history safely.
- **Given**: Active and terminal runs exist.
- **When**: User selects Delete for a terminal run and sees a DialogShell-style confirmation identifying it and warning of permanent timeline and note removal.
- **Then**: Cancel changes nothing; pending disables repeat submission; errors remain visible. DELETE atomically rechecks terminal status and removes only that run and its events; modal and chart refresh. Direct active-run deletion returns conflict; absent, double-deleted or pruned run returns not-found. A completion/delete race is decided by transactional order; a prune/delete race has one deletion winner. Other runs, agent, definitions and execution evidence remain unchanged.

### Scenario: [WJ-05] Respect agent lifecycle
- **User Goal**: Understand background and deleted-agent history.
- **Given**: Interactive, background and scheduled agents have runs; background agents become eligible for termination 24 hours after creation, with startup/minutely sweeps and possible delays.
- **When**: Launch fails before binding, or an agent is actually deleted by explicit deletion, failed-start cleanup or background expiry.
- **Then**: Failed binding creates no phantom run; deletion removes all journal records for that agent in the deletion transaction, including active ones, with no orphan access. Other agents' journals survive. UI discloses that background history can disappear early.

### Scenario: [WJ-06] Bound retained history
- **User Goal**: Keep storage bounded without misleading analytics.
- **Given**: Controlled time and enough terminal runs or active events to cross limits.
- **When**: Startup/minutely sweep, terminal closure, clock rollback or another active event occurs.
- **Then**: Expiry is inclusive at 90 days using the nondecreasing cutoff; global cap evicts oldest terminal runs with ID tie-break; active and terminal rolling eviction retains at most 10,000 newest events and current-visit metadata. Truncation, cutoff and partial analytics are visible, only measurable retained visits contribute, and transitions and exit succeed beyond the cap. Concurrent writes/deletes and restart preserve these properties.

### Scenario: [WJ-07] Upgrade and restart
- **User Goal**: Continue active work without invented history.
- **Given**: A legacy database has an active binding and old unjournaled history.
- **When**: Migration runs repeatedly and the router cold restarts.
- **Then**: Exactly one synthetic incomplete run starts at migration time in the current state, with no inferred prior events or durations; restart uses the same run. UI discloses unavailable old history. Renamed agents and renamed/deleted definitions do not alter surviving snapshots.

### Scenario: [WJ-08] Administrative mutations and deletion
- **User Goal**: Have the journal reflect actual binding changes.
- **Given**: Active bindings and definitions, including a referenced state and a predecessor.
- **When**: An administrator deactivates, reassigns including same workflow, replaces a state, deletes a state/workflow or replaces all definitions, or deletion is rejected.
- **Then**: Each actual ending/replacement closes the old run; actual activation creates a new one; a continuing binding records its nonterminal change. FK predecessor cleanup alone creates no visit. Rejected operations create no event; retained snapshots remain readable after definition deletion. Existing goal, return and cleanup behavior remains authoritative.

### Scenario: [WJ-09] Atomicity, retry and contention
- **User Goal**: Never see a journal contradict the binding.
- **Given**: Competing transitions, exit, administrative mutations, a stale binding and injected journal failure.
- **When**: They race or the same committed operation is retried after a lost response, including exit against a changed state or permission.
- **Then**: One valid fenced mutation wins; stale/rejected/faulted transactions leave binding and journal unchanged. Exit transaction checks generation, current state and current exit authorization. Same ID and payload returns original result without duplicate event or effect; different payload conflicts. REST refresh recovers missed notification. SQLite fault-injection and concurrency tests support but do not replace real-boundary E2E.

### Scenario: [WJ-10] Delete and retention races
- **User Goal**: Avoid collateral deletion or replay.
- **Given**: Manual deletion, retention pruning, agent deletion and ongoing writes contend.
- **When**: Operations race or retry across restart.
- **Then**: SQLite transaction order determines the winner, absent targets return not-found, no active run is manually or automatically pruned, and agent deletion removes its own active and terminal records. Unrelated agents and execution evidence remain unchanged; journal deletion does not grant replay.

### Scenario: [WJ-11] Partial bindings and state moves
- **Given**: A cross-workflow binding has different binding and current-state workflow IDs, and another has a predecessor.
- **When**: Delete referenced workflows or states separately, delete only a predecessor, replace all definitions, move the current state, and migrate partial bindings.
- **Then**: Apply the partial-binding and state-move rules atomically. State removal closes its visit; workflow-only removal does not. Both-null terminates once; predecessor-only cleanup adds no visit. A move splits duration and preserves prior snapshots, binding and predecessor. Rejected deletion changes nothing.

### Scenario: [WJ-12] Rolling eviction and late entries
- **Given**: A run exceeds 10,000 mixed events, including enough notes to evict the current visit entry; another run is terminal.
- **When**: Filter by an evicted-only workflow, add an ordinary note, target an evicted event, exit, and append entries to retained terminal targets past the cap.
- **Then**: The evicted-only workflow no longer matches; current metadata still matches and accrues measurable duration. Ordinary note targets it; an explicit evicted target is not-found. Active and terminal runs remain at or below 10,000 events; terminal metadata, truncation, cutoff and partial analytics survive restart, with no unbounded auxiliary index or content.

### Scenario: [WJ-13] Receipt lifetime and deletion
- **Given**: A committed operation has a receipt and may have a longer execution replay fence.
- **When**: Delete or prune its run, delete its agent, restart, retry identical and changed payloads within 30 minutes, exhaust capacity, and retry at and after expiry.
- **Then**: In-window identical retries replay and changed payload conflicts without effects even if the journal target is gone; returned IDs do not restore history. Existing receipts replay or conflict at capacity; new operations fail atomically. Expiry frees capacity without changing execution fences. Late reuse is a new attempt subject to admission and longer fences, not a guarantee against duplicate effects.

## 4. Automated E2E Verification Matrix

All cases are **planned**, not acceptance evidence. Each uses the real browser, WebSocket, REST, router-proxy, router and SQLite with deterministic external LLM mocks and controlled time as needed. Store concurrency/fault-injection tests supplement, not replace, E2E.

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
|---|---|---|---|
| WJ-01 | Visits, identity, exit and durations | `tests/e2e/workflow-run-journal.spec.ts` | `records transitions returns and per-state durations` |
| WJ-02 | Prompts, entries and note admission | `tests/e2e/workflow-run-journal.spec.ts` | `associates prompted entries and authorized notes` |
| WJ-03 | Agent-scoped accessible browsing | `tests/e2e/workflow-run-journal.spec.ts` | `browses paginated journal and responsive timeline` |
| WJ-04 | Confirmed terminal deletion | `tests/e2e/workflow-run-journal.spec.ts` | `confirms and deletes terminal runs only` |
| WJ-05 | Background and deleted agents | `tests/e2e/workflow-run-journal.spec.ts` | `cleans journals with deleted agents` |
| WJ-06 | Retention and partial analytics | `tests/e2e/workflow-run-journal.spec.ts` | `bounds retention across clock rollback and restart` |
| WJ-07 | Legacy migration and restart | `tests/e2e/workflow-run-journal.spec.ts` | `migrates one incomplete active run` |
| WJ-08 | Administrative mutation inventory | `tests/e2e/workflow-run-journal.spec.ts` | `journals actual administrative binding changes` |
| WJ-SCHEDULED | Scheduled launch activation journal | `tests/e2e/workflow-run-journal.spec.ts` | `scheduled launch journal survives router restart` |
| WJ-09 | Atomicity and retry | `tests/e2e/workflow-run-journal.spec.ts` | `fences concurrent operations and committed retries` |
| WJ-10 | Cleanup races | `tests/e2e/workflow-run-journal.spec.ts` | `serializes deletion pruning and agent cleanup` |

| WJ-11 | Partial FK bindings and state moves | `tests/e2e/workflow-run-journal.spec.ts` | `preserves partial bindings and splits moved state durations` |
| WJ-12 | Active and terminal rolling eviction | `tests/e2e/workflow-run-journal.spec.ts` | `bounds late entries and filters retained visits` |
| WJ-13 | Receipt expiry, capacity and deletion | `tests/e2e/workflow-run-journal.spec.ts` | `replays independently of journal deletion until receipt expiry` |

Additional E2E assertions: WJ-06 covers the exact 90-day boundary, timestamp ties and ID tie-break, forward jump then rollback, rolled-back closure and restart before cleanup. WJ-03 covers invalid filter-scoped cursors, inserts/deletes between pages and retained-only filter changes. WJ-09 covers lost responses for tool and keyed HTTP IDs, payload conflicts and keyless HTTP ambiguity. WJ-11 covers workflow-only, state-only and both-null FK results and moves across equal-named workflows. WJ-12 verifies an evicted current event is not an explicit target despite preserved current metadata, and retained terminal targets accept entries after exit. Assert persisted SQLite state and browser/API outcomes through the proxy, with controlled time and deterministic external LLM mocks.
