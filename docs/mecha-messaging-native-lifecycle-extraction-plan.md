# Mecha Messaging native lifecycle extraction plan

## Status and evidence

This is an implementation plan. It does not change production code or tests. It does not start services, commit, or push.

The extraction baseline is:

- Commit: `0cdcaf74f54dc961abd16aa3be2fec8c433647d1` (`Extract mailbox delivery coordinator`)
- Tree: `a8101f1c45b7385f7a0694cd8b25c56c2b0df39e`
- Target: `pi-extensions/mecha-messaging/index.ts`

The checkout currently contains unrelated dirty work in Android files, `tests/android_qol_parity.test.mjs`, `PLAN.md`, `SPEC.md`, and `web/router-ui-react/`. The implementation must use a clean isolated copy of the baseline and must not stage, reset, rewrite, or include those paths. The root startup/shutdown ordering cases are part of `tests/mecha_messaging_activity_lifecycle.test.ts` and are included in every focused and coverage command below.

The durable evidence for this plan is the baseline source, its tests, and the history table below. The boundary is visible in `pi-extensions/mecha-messaging/index.ts`, `mailbox-client.ts`, `delivery-coordinator.ts`, `harness/native_subagent_trace.js`, and `tests/pi_hard_compaction_e2e.test.ts`. Commits `8bb1109bc`, `94bda4451`, and `5d49b145e` record the attempted and reverted subscription design and the final polling-only contract. Commits `c342aa918`, `754fbbb8f`, and `0cdcaf74f` record owner-scoped lookup, redacted tracing, and the current extraction boundary. These files and commits are the source of truth for implementation and review.

The canonical quality loop is specification, plan review, TDD implementation, coverage, and review. The implementation owner must load the `workflows` skill and retrieve `dev-loop` before writing production code.

## Requirements

This plan implements the approved pure, internal, behavior-preserving extraction. The new module reorganizes existing activity-lifecycle code while preserving observable state, event, timer, redaction, lookup, cleanup, export, and ordering contracts. The approved specification is this plan plus the frozen contracts and observable completion criteria below. A standalone `SPEC.md` is omitted because this is not a new product behavior or public contract, and the repository's existing `SPEC.md` is unrelated dirty work that must remain untouched.

## Outcome

Create `pi-extensions/mecha-messaging/activity-lifecycle.ts` and move the activity lifecycle behavior out of `index.ts` without changing its observable behavior.

The extracted module will own:

- settled-notification scheduling;
- owner-scoped native service lookup and old-service compatibility reads;
- native child normalization, tracking, tombstones, and reconciliation;
- activity heartbeat timing and refresh suppression;
- activity and lifecycle snapshot projection;
- compaction masking and lifecycle-boundary cleanup for activity state.

The Messaging extension remains the composition root. It continues to own Pi event registration, child-session routing, Goal meaningful-activity emission, mailbox pause state, compaction orchestration, model switching, diagnostics, and actual settled-notification sending.

No wire protocol, router, client, Goal, compaction-policy, delivery, or user-interface behavior changes belong in this slice.

## Current code and historical anchors

| Area | Current source and symbols | Plan use |
| --- | --- | --- |
| Composition root | `pi-extensions/mecha-messaging/index.ts:createMechaMessagingExtension` | Keep construction and Pi event registration here. Replace calls with the extracted lifecycle object. |
| Scheduler | `index.ts:createSettledNotificationScheduler` | Move unchanged and re-export from the root so the existing public export remains available. |
| Service lookup | `index.ts:getOwnerSubagentsService`, local `nativeSubagentService` | Preserve the parent-session lookup and lookup diagnostics. The extracted module exposes an internal service accessor for the Sentinel coordinator. |
| Compatibility reads | `nativeSubagentRecords`, `lifecycleSnapshotSubagentService`, `reportNativeSubagentReadFailure` | Move unchanged. `getLifecycleSnapshots()` remains authoritative when present. |
| Tracking | `normalizeSubagentMetadata`, `nativeSubagentStatus`, `isActiveNativeSubagentRecord`, `isTerminalNativeSubagentRecord`, `mergeNativeSubagentMetadata` | Move unchanged, including old status aliases. |
| Tombstones | `rememberTerminalSubagent`, `clearTerminalSubagentTombstone`, `isTerminalTombstonedNativeRecord` | Move unchanged. Keep the bounded stale-resurrection protection and explicit newer-`startedAt` reuse rule. |
| Projection | `publicSubagentSummaries`, `lifecycleSubagentSummary`, `lifecycleSubagentSummaries`, `publishLifecycleSnapshot`, `summarizeNativeSubagents` | Move the implementation. Preserve the root export for `summarizeNativeSubagents`. |
| Reconciliation | `replaceNativeLifecycleSnapshot`, `reconcileNativeSubagentService`, `pollNativeLifecycleSnapshot` | Move unchanged. Snapshot mode replaces the local projection. Legacy mode retains event-tracked children through service-list lag. |
| Parent state | `parentActivityStateFromPiContext`, `parentActivityStateFromContext` | Move unchanged, with callbacks for compaction state and the parent context. |
| Heartbeat | `ensureActivityHeartbeatTimer`, `clearActivityHeartbeatTimer`, `startActivityHeartbeat`, `stopActivityHeartbeat`, `refreshActivityState`, `clearRunningSubagentsForBoundary` | Move unchanged behind the lifecycle object. |
| Pi activity client | `pi-extensions/mecha-messaging/mailbox-client.ts:activity` | Do not edit. Continue sending `active`, `settled`, and `compacting` with count, summaries, Goal projection, and current model. |
| Pi lifecycle client | `mailbox-client.ts:lifecycleSnapshot`, `lifecycleEvent`, `discoverCapabilities`, `receive` | Do not edit. The extracted code calls only the existing `sendLifecycleSnapshot` path. |
| Native events | `index.ts` registrations for `subagents:started` and terminal aliases | Keep registrations in the root. Delegate state changes to the module after Goal activity emission. |
| Pi events | `index.ts` registrations for `agent_start`, `agent_settled`, `session_before_compact`, `session_compact`, `session_start`, and `session_shutdown` | Keep handlers and their registration order. Replace only the activity lifecycle calls. |
| Router persistence | `harness/db.js:applySubagentLifecycleSnapshot`, `applySubagentLifecycleEvent`, `subagentLifecycleSnapshot`, `replaySubagentLifecycle`, `pruneSubagentLifecycle`; `harness/router.js:enableSubagentSync`, `receiveSubagentLifecycle`, lifecycle heartbeat | Characterize these existing boundaries. Do not modify them for this extraction. |
| Delivery boundary | `pi-extensions/mecha-messaging/delivery-coordinator.ts:createPiMailboxDeliveryCoordinator` | Leave unchanged. Activity publication is best effort and must not alter durable mailbox acknowledgment. |
| Installed-Pi precedent | `tests/pi_hard_compaction_e2e.test.ts:installedPiRuntime`, `createRuntimeSession` | Reuse the real Pi import, faux-provider, temporary session, and extension-factory patterns for the lifecycle E2E. |

The relevant history is:

| Commit | Behavior established |
| --- | --- |
| `203b3e985` | Settled notifications are suppressed during child runs. |
| `057e76fa1` | Initial live activity heartbeat. |
| `ced707b39` | Running-child descriptions entered activity projection. |
| `426eb63e7` | Tombstones and service reconciliation protected stale child state. |
| `0e740ed17` | Late-published service lookup was repaired. |
| `6b5da28d5` | Polling became continuous, including after the parent settled. |
| `cf678889f` | Durable native lifecycle visibility, router persistence, and protocol were introduced. |
| `07387c2af` | Native lifecycle status handling was added. |
| `42090dace` | Lifecycle binding was repaired. |
| `7c464f0d1` | Lifecycle status was preserved through the durable path. |
| `8bb1109bc` / `94bda4451` | A lifecycle subscription attempt was made and then deliberately reverted. |
| `5d49b145e` | Polling-only snapshots became the final lifecycle publication design. |
| `a5efddb83` | Missing event-tracked children age out after one minute. |
| `c342aa918` | Native service lookup became owner-scoped. |
| `754fbbb8f` | Opt-in redacted native lifecycle tracing was added. |
| `2d21cc1c2` | Mecha Messaging moved from JavaScript to TypeScript. |
| `9d76e13a6` | Goal was split into its own Pi package. |
| `2b27be8b0` | Compaction policy was split into its own module. |
| `0cdcaf74f` | Mailbox delivery was split into `delivery-coordinator.ts`; lifecycle remains in `index.ts`. |

The roadmap to update after implementation is `docs/mecha-messaging-quality-refactor-roadmap.md`, under **Remaining slices → Activity lifecycle**.

## Proposed module boundary and API

`activity-lifecycle.ts` must have no runtime import from `index.ts`, Goal, compaction policy, or the mailbox client. Use small structural interfaces and callbacks. This keeps dependency direction one way and prevents the extraction from becoming a second composition root.

The internal API should be this shape. Names may be adjusted during implementation only when the behavior and call ordering stay the same:

```ts
type ActivityLifecycle = {
  settledNotifications: SettledNotificationScheduler;
  getNativeService(ownerSessionId?: string): NativeSubagentService | undefined;
  onAgentStart(context: PiContext): void;
  onAgentSettled(context: PiContext): void;
  onNativeStarted(event: NativeSubagent): void;
  onNativeTerminal(event: NativeSubagent): void;
  onCompactionStarted(context: PiContext): void;
  onCompactionResumed(context: PiContext): void;
  refresh(context?: PiContext): void;
  ensureHeartbeat(): void;
  onSessionBoundary(): void;
};

function createActivityLifecycle(options: ActivityLifecycleOptions): ActivityLifecycle;
function createSettledNotificationScheduler(options?: TimerOptions): SettledNotificationScheduler;
function summarizeNativeSubagents(subagents: NativeSubagent[] | undefined): NativeTraceSummary[];
```

`ActivityLifecycleOptions` must provide:

- `getClient()`: returns the current mailbox client, which is undefined before parent `session_start` and after shutdown;
- `getParentContext()`: returns the current parent Pi context;
- `isCompacting()`: reads the root-owned `mailboxPausedForCompaction` flag;
- `getGoalProjection()`: returns the already-normalized Goal projection or undefined;
- `getCurrentModel()`: returns the root's effective model projection, including the short-lived successful-switch override;
- `getNativeService(ownerSessionId)`: calls the existing owner-scoped accessor;
- `notify(message, level)`: sends the existing operational read-failure notification;
- `trace(event, fields)`: calls the existing redacted extension trace callback;
- `createSettledScheduler`: the existing injected scheduler factory;
- the existing timeout, interval, clear, and clock implementations.

The module must not own `settledNotificationPending`, `settledNotificationEnabled`, `settledNotificationRecipient`, or the reply callback. Those values are part of the root's tool and mailbox behavior. The root receives `settledNotifications` from the module and invokes its existing methods at the same points.

`onSessionBoundary()` is reusable across Pi session restarts. It stops the heartbeat, clears local child state, and performs the existing forced settled publication. It must not silently move scheduler disposal to a different point. The root will call `settledNotifications.dispose()` at the existing parent shutdown position.

## Exact integration ordering in `index.ts`

The extraction must preserve both handler registration order and the order of side effects inside each handler.

### Construction

1. Keep trace validation and `currentSubagentsServiceAccessor` initialization at extension creation.
2. Keep the root-owned `client`, `parentActivityContext`, `mailboxPausedForCompaction`, Goal projection, model projection, and child-session set.
3. Construct one `activityLifecycle` instance after those callbacks exist and before any event handler or coordinator can invoke it. Construct it once per extension instance, not per `session_start`.
4. Replace `const settledNotifications = createSettledScheduler()` with `const settledNotifications = activityLifecycle.settledNotifications`. The existing `createSettledScheduler` injection remains valid for tests.
5. Make `createMailboxCoordinator` obtain the owner-scoped service through `activityLifecycle.getNativeService(context.sessionManager.getSessionId())`. This keeps Sentinel and lifecycle polling on the same accessor and preserves `extension_service_lookup` diagnostics.

### Native child events

Keep the current `pi.events.on` registrations in the root.

- `subagents:started`: call `emitMeaningfulActivity("native-subagent-launch", event?.id, parentActivityContext)` first. Then call `activityLifecycle.onNativeStarted(event)`. In snapshot-service mode this only updates settled-notification child suppression and emits the existing trace. It must not publish a lifecycle frame. In legacy mode it retains the current local activity update and forced refresh.
- Register every existing terminal alias: `completed`, `failed`, `canceled`, `cancelled`, `aborted`, `terminated`, `stopped`, and `steered`. Delegate to `activityLifecycle.onNativeTerminal(event)`. In legacy mode preserve immediate local removal, bounded tombstone behavior, and settled-scheduler disposal. In typed `getLifecycleSnapshots()` mode retain the terminal record until the next timer-driven snapshot; that snapshot's omission removes it authoritatively. Both modes keep the absence of a direct lifecycle-frame send.
- Keep `subagents:child:session-created` and `subagents:child:disposed` in the root. They manage child reply clients and child-session tombstones. They do not become parent activity events.

### Parent Pi events

Do not merge the existing multiple `agent_settled` handlers. Their registration order is part of the behavior.

- `agent_start`: keep the child-session guard. Call `activityLifecycle.onAgentStart(context)` first. Then, if opt-in settled notifications are configured, set `settledNotificationPending = true` and call `settledNotifications.onParentActivity()` exactly as today.
- The earlier asynchronous `agent_settled` recall handler remains first.
- The threshold-monitoring `agent_settled` handler remains in its current position.
- The `session_compact` handler remains between the threshold handler and the final `agent_settled` handler.
- The final `agent_settled` handler calls `activityLifecycle.onAgentSettled(context)` first for parent sessions. It then keeps the existing pending self-compaction suppression, pending self-compaction execution, settled-notification guards, `settledNotificationPending` consumption, and `settledNotifications.onParentSettled(send)` callback in the same order.
- `session_before_compact`: the root sets `mailboxPausedForCompaction = true` before calling `activityLifecycle.onCompactionStarted(context)`. The module clears the local child projection and timer, then sends the forced `compacting` activity state with reconciliation disabled. The root keeps the mailbox status update.
- `session_compact`: preserve the root's threshold reset and parent guard. Set `mailboxPausedForCompaction = false` before calling `activityLifecycle.onCompactionResumed(context)`. The module refreshes the normal projection and ensures the heartbeat. The root keeps the connected status update.

### Parent session startup

Keep the existing parent `session_start` sequence:

1. resolve and publish the worker identity;
2. remember the parent context and reset root session-local controls;
3. create the mailbox client and install its envelope/control callbacks;
4. assign `client = mailboxClient`;
5. call `publishGoalState(mailboxClient)`;
6. create the delivery coordinator;
7. call `activityLifecycle.ensureHeartbeat()`;
8. call `mailboxClient.connect()`.

The `publishGoalState(mailboxClient)` call is an ordering side effect: it must happen after assignment, so it uses the new client, and before the delivery coordinator is created. Add an integration assertion for that order.

The child `session_start` path must return before parent mailbox or activity startup. It keeps its transient child client behavior.

### Shutdown

Keep the two `session_shutdown` handlers and their order. The initial asynchronous parent handler does not call `rememberParentActivityContext`. The final parent cleanup handler retains the existing `rememberParentActivityContext(context)` call immediately before `activityLifecycle.onSessionBoundary()`, then clears that context. Add an exact differential assertion that the initial asynchronous handler's observation list contains neither `rememberParentActivityContext` nor `onSessionBoundary`, and that the final handler's ordered list is `["rememberParentActivityContext", "onSessionBoundary"]` before context/state clearing.

In the final parent cleanup handler:

1. call `activityLifecycle.onSessionBoundary()`. It clears running children and publishes forced `settled` unless compaction is still masking it, then clears the heartbeat timer;
2. clear `parentActivityContext` and root pending activity/compaction state;
3. call `settledNotifications.dispose()` at the existing scheduler-disposal point;
4. call `coordinator?.close()` and clear `coordinator`;
5. call `client?.close()` and clear `client`;
6. clear the UI mailbox status.

A child shutdown continues to remove only its child reply-client tombstone. It must not clear the parent activity lifecycle.

## Frozen contracts and invariants

These are acceptance requirements. A code review must reject any change that alters them without a separate product decision.

### Settled notifications

- Notifications are disabled by default and require a valid `MECHA_PI_NOTIFY_ON_SETTLED` worker identity.
- Runtime enable requires a configured recipient.
- Only `agent_settled` is the parent settle boundary. `agent_end` has no role.
- Parent activity arms one pending notification and resets the scheduler generation.
- The cooldown remains exactly 120 seconds.
- Any active native child suppresses the notification.
- Disposing the last child does not retroactively send a notification. A new parent settle is required.
- A second parent run can arm independently while an earlier `sendReply()` remains unresolved.
- Disable cancels the cooldown.
- Child sessions never send the parent settled notification.
- Pending self-compaction suppresses the settled notification at that boundary.

### Service and state ownership

- Lookup is scoped to the parent session ID. A child session replacing the process-global service entry must not change the parent's projection.
- `getLifecycleSnapshots()` is the authoritative typed source when available.
- Older service shapes are selected in this order: `listAgents()`, `getRunning()`, then `running`, `runs`, or `subagents` properties.
- Snapshot mode accepts only `queued` and `running`, replaces the local running map authoritatively, and treats omission as removal.
- Legacy mode retains its compatibility status aliases and does not leak legacy status into the old activity projection.
- Event-tracked children survive normal service-list lag. Service-only children disappear when the service drops them.
- An event-tracked child missing from the legacy service ages out after exactly 60 seconds and is removed from settled-notification suppression.
- A failed service read emits the existing one-per-60-second operational notification and leaves polling alive.

### Projection and redaction

- Activity states remain `active`, `settled`, and `compacting` at the client boundary.
- Activity includes the parent state, child count, bounded child summaries, Goal projection when received, and effective current model when supported.
- Lifecycle publication uses complete snapshots through `client.sendLifecycleSnapshot`. The extension does not call `sendLifecycleEvent` for native start or terminal events.
- Snapshot and trace child objects contain only bounded `id`, optional `type`, `description`, and allowed `status`. Prompts, output, results, errors, and internal timestamps never cross the router or enter traces.
- IDs remain capped at 128 characters, types at 64, descriptions at 512, and lifecycle statuses remain in the existing seven-value set.
- The root-exported `summarizeNativeSubagents` is an independent trace/summary contract: it accepts up to 999 records, arbitrary bounded status values trimmed to the existing 64-character bound, and arbitrary bounded descriptions reported through the existing numeric `description_length` field. Its public behavior is tested separately from lifecycle projection.
- Activity and lifecycle projections remain capped at 100 children. Lifecycle projections use the existing seven allowed statuses and their existing description bound, even though the wire schema accepts up to 999; this 100/seven-status contract is tested independently of the root export.
- Visible summaries remain ordered by `startedAtMs`, then `id`.
- Children with no description still count toward `subagent_count` and are omitted from the visible summary list.

### Heartbeat and precedence

- The heartbeat interval remains exactly 2,000 milliseconds and is `unref`'d.
- Polling continues after parent settlement and when no child is currently known.
- The latest typed service snapshot is read and published only from the timer-driven polling path.
- Unchanged activity refresh remains limited to one send per 10,000 milliseconds, except when state or child data changes or a forced send is requested.
- Settled with zero children is terminal and does not need a periodic refresh frame.
- Compaction wins over all other activity states. When not compacting, `PiContext.isIdle()` wins when present. The heartbeat latch is the fallback for older contexts.
- A settle event that races a still-active Pi context keeps the heartbeat alive and does not publish a stale settled frame.
- Compaction and session shutdown clear the local child map and heartbeat timer.
- A compaction start publishes `compacting`. Successful, canceled, and failed compaction resume normal activity reporting.

### Tombstones and terminal retention

- Legacy-mode terminal tombstones retain at most 2,048 IDs.
- A legacy terminal event removes the child immediately.
- A typed terminal event leaves the local record in place until the next timer-driven `getLifecycleSnapshots()` read. Omission from that snapshot removes it; this prevents an event from changing the typed projection before the authoritative poll.
- A stale legacy service row cannot resurrect a tombstoned ID.
- An explicit newer `startedAt` clears the legacy tombstone and permits ID reuse.
- Snapshot-mode omission is authoritative. The local legacy tombstone rule is not used to reconcile typed snapshots.
- Compaction clears the local activity map and heartbeat timer, while terminal tombstones survive the compaction boundary. Compaction does not dispose the settled-notification scheduler; it remains available for the resumed parent session. Shutdown clears the activity map and timer, disposes the settled scheduler at its existing root position, and leaves tombstone retention available for the process's existing boundary semantics. Tests must cover both boundaries, including scheduler survival across compaction, disposal at shutdown, and the next-session stale-row behavior.

### Boundary retention matrix

| Boundary | Local child map and activity projection | Heartbeat timer | Terminal tombstones | Settled-notification scheduler |
| --- | --- | --- | --- | --- |
| `session_before_compact` | Clear | Clear | Survive | Keep running and reusable after resume |
| `session_shutdown` | Clear | Clear | Survive for the existing process boundary | Dispose at the root's existing disposal point |

The matrix is intentional: compaction masks and rebuilds transient activity while keeping notification generations and stale-ID protection coherent; shutdown stops all timers and notification work while retaining tombstones until the existing lifecycle owner releases them. Focused tests must assert each cell and must assert that a test-only observation subscription never changes this production retention or the polling-only publication path.

### Router and client boundaries

The extraction does not change `mailbox-client.ts`, router persistence, or the protocol. Existing contracts remain frozen:

- capability discovery precedes lifecycle enablement;
- unsupported lifecycle capability still falls back to legacy activity;
- registration establishes the lease before renewal;
- sync enable includes `after_version` when a cursor exists;
- the first state response causes a complete current snapshot before older journal retries;
- lifecycle identity fields, sequences, journal bounds, collapse, expiry, oversize shutdown, correlated acknowledgments, reconnect rebinding, and duplicate-frame handling remain unchanged;
- router SQLite state commits before lifecycle acknowledgment and only applied transitions are broadcast;
- durable mailbox delivery still injects before `injection_requested`, waits for a persisted session checkpoint before `ack`, and remains independent of best-effort activity publication.

One existing client quirk must remain observable: a received state snapshot calls `lifecycleSnapshot()`, which flushes, and then calls `flushLifecycleJournal()` again. The router deduplicates the repeated lifecycle identity. Existing client/router tests must retain an observation of this duplicate send. It is outside the lifecycle-only extraction differential.

## Phased TDD implementation

Each phase starts with a failing test. After the test passes, run the smallest focused check, mutation-check the new assertions, and only then move the next behavior. Keep the old implementation available until the differential harness is green.

### Phase 0: Freeze the target, search the KB, and test the boundary

- Work from a clean copy of the baseline commit and record its tree hash.
- Search `~/.mecha-developer/kb` for native Pi, subagent lifecycle, polling, compaction, router, and Messaging extraction facts. Record the search date, queries, matching files, and the decisions carried into this plan in the implementation handoff. An empty result is evidence and must be recorded.
- Treat the active goal as the accepted specification for this pure, internal, behavior-preserving refactor. No additional product specification is required. Any discovered product behavior change remains a separately scoped decision.
- Add the module-focused test file, planned as `tests/mecha_messaging_activity_lifecycle.test.ts`.
- Add a package-discovery assertion that the new `activity-lifecycle.ts` file is inside the package graph.
- Keep `tests/mecha_messaging_package.test.ts`'s root export list unchanged. `createActivityLifecycle` is internal and is not re-exported by `index.ts`.
- Characterize the root integration with the existing fake-Pi fixture before moving code, including `publishGoalState(mailboxClient)` after client assignment and `rememberParentActivityContext(context)` before lifecycle cleanup.
- Create a persisted focus ledger in task state before implementation. Record its durable path or task-state key in the handoff. It must list each review focus area, required evidence, findings, disposition, and target tree. Update it after every material plan revision and implementation review.
- Obtain repeated plan-review signoff after material plan revisions. Run independent `pr-validate`-style validation against each proposed finding; record accepted, rejected, and unresolved findings in the focus ledger.

### Phase 1: Scheduler and bounded pure projections

Move the scheduler and bounded summary helper with tests first.

Test:

- one notification after 120 seconds;
- generation invalidation on parent activity;
- child suppression and disposal behavior;
- disposal canceling timers;
- invalid timer dependencies;
- summary ID/type/status/description limits;
- invalid records and no-description records;
- stable start-time then ID ordering;
- the 100-record projection cap.

The root continues to expose `createSettledNotificationScheduler` and `summarizeNativeSubagents` through re-exports. Test `summarizeNativeSubagents` as an independent root API with 999 records and arbitrary bounded `status`/`description_length` values. Test the lifecycle projection separately with its 100-record cap, seven-status allowlist, and lifecycle description bound. No public export is added.

### Phase 2: Service lookup, tracking, and reconciliation

Move service lookup and the local state machine without changing conditions or status names.

Test with controlled clocks and services:

- typed `getLifecycleSnapshots()` takes precedence;
- a typed terminal event leaves its record visible before the next heartbeat; the following typed snapshot removes it when omitted;
- a typed snapshot replaces stale children and preserves queued/running status;
- legacy `listAgents`, `getRunning`, and property fallbacks work;
- legacy status aliases classify active and terminal records as today;
- event-only children survive service-list lag;
- service-only children are removed when omitted;
- event-only children age out at 60 seconds;
- a terminal event prevents stale service resurrection;
- a newer explicit `startedAt` permits the same ID to be reused;
- no description still contributes to count;
- parent-scoped lookup survives child service replacement;
- a read failure is rate-limited, reported, and retried.

Keep lifecycle snapshot polling as the only typed lifecycle publication path. Do not add `subscribeLifecycle`; the design in `8bb1109bc` was reverted by `94bda4451`.

### Phase 3: Heartbeat and activity/lifecycle assembly

Move parent state precedence, activity refresh suppression, timer lifecycle, compaction masking, and lifecycle snapshot publication.

Test:

- `active`, then `settled`, from parent start and settle;
- live `isIdle()` changing the state at the next heartbeat;
- the heartbeat latch as the old-context fallback;
- a settle race that sees Pi still active;
- continued polling after settlement with and without children;
- unchanged refresh suppression at 10 seconds;
- forced state refresh;
- typed snapshots changing only on the heartbeat;
- a typed terminal event remaining in the local projection before the heartbeat, then disappearing when the next typed snapshot omits it;
- native start and terminal events publishing no lifecycle frame;
- forced `compacting` with no reconciliation;
- resume after successful, canceled, and failed compaction;
- compaction clearing the activity map and heartbeat timer while tombstones and the settled scheduler survive;
- shutdown clearing the activity map and heartbeat timer, retaining tombstones, and disposing the settled scheduler at the root boundary;
- boundary cleanup and timer cancellation;
- best-effort client failures not escaping into the parent action.

The projection callbacks must prove that Goal and effective-model values are read from the root at send time. The module must not recreate either state machine.

### Phase 4: Root wiring

Replace root-local lifecycle code with the module calls in the ordering described above.

Preserve integration tests in `tests/mailbox_v2_delivery.test.ts`. Move only tests whose subject is now the module's pure scheduler/tracking behavior. Keep root tests for:

- parent and child event guards;
- Goal meaningful activity ordering;
- model-switch activity refresh;
- compaction and mailbox pause/resume;
- parent shutdown cleanup;
- startup ordering: client assignment, `publishGoalState(mailboxClient)`, then delivery-coordinator construction;
- shutdown ordering: `rememberParentActivityContext(context)` completes before lifecycle boundary cleanup;
- Sentinel's owner-scoped service accessor;
- lifecycle trace callback behavior.

Update `tests/native_subagent_trace.test.ts` only when imports or test seams require it. The trace field allowlist itself stays in `index.ts` in this slice.

### Phase 5: Direct old-vs-new lifecycle differential harness

Add `tests/mecha_messaging_activity_lifecycle_differential.test.ts`. This is a direct execution comparison for the extracted lifecycle behavior, not a second protocol or delivery test.

Use these baselines:

1. **Exact extraction baseline:** materialize `0cdcaf74f` into a temporary package and execute its pre-extraction lifecycle path.
2. **Parity history baseline:** use the durable source/history references `2d21cc1c2^`, `8bb1109bc`, `94bda4451`, and `5d49b145e` to identify behavior that must remain unchanged.

Run each lifecycle side in a separate child process. Materialize only the temporary package tree needed by relative lifecycle imports, then import it with the repository's TypeScript test runner. No temporary source is written into the checkout.

Drive the same deterministic lifecycle fixture through both implementations:

- fixed clocks and timer callbacks;
- fixed client IDs and fake clients;
- fake Pi event emitters;
- parent and child session contexts;
- typed and legacy native service shapes;
- controlled Goal and model projections;
- normalized error objects.

The differential scope is limited to ordered lifecycle observations: scheduler calls, native-child tracking and reconciliation, activity state/count/summary projection, typed heartbeat snapshots, legacy refreshes, tombstones, service lookup and failure reporting, compaction masking/resume, session-boundary cleanup, and the root ordering side effects that call the lifecycle adapter. Mailbox protocol, router persistence, lifecycle journal replay, durable delivery, Sentinel, capability fallback, and the duplicate initial client flush are covered by their existing focused tests and E2E; they are not inputs to this differential.

Record raw ordered observations after every lifecycle operation. Preserve event order, status values, snapshot-versus-event publication, sequence relationships, event identity relationships, duplicate lifecycle observations inside the scoped adapter, and deterministic frame IDs. Normalize only injected-process values such as temporary paths, timestamps, and generated UUIDs. Compare each sequence, not only final state, and fail with the first mismatch, operation, and scenario name.

The intentional-difference allowlist is explicit and closed:

- temporary paths, process-generated timestamps, leases, and UUIDs that the fixture cannot inject;
- malformed-input validation at the existing TypeScript boundary where the pre-TypeScript JavaScript accepted a shape that current TypeScript intentionally rejects, limited to scenarios named in the ledger.

Every mismatch outside this allowlist fails the test. A new allowlist entry requires a source/history citation and plan-review signoff; it cannot be added to make a failing comparison green.

The typed terminal scenario must include a pre-heartbeat observation: after a terminal event, the record remains in the local projection and no lifecycle frame is sent; only the next typed snapshot can remove it. The legacy scenario must show immediate removal.

A test assertion is not complete until mutation checking proves it can fail. Remove or invert representative assertions for typed pre-heartbeat retention, typed snapshot omission, legacy immediate removal, tombstone reuse, compacting precedence, and no-event publication, run the focused test red, and restore only those mutations.

### Phase 6: Isolated real native-Pi and temporary-router E2E

Add `tests/mecha_messaging_activity_lifecycle_native_pi_e2e.test.ts`, or extend the installed-Pi file only if the resulting ownership is clearer. Run it in its own Node process.

The process creates and owns:

- temporary `HOME`;
- temporary `PI_CODING_AGENT_DIR`;
- temporary project directory;
- temporary parent session directory;
- temporary SQLite router database and trace files;
- a fixed temporary loopback router port;
- a deterministic local/faux model provider with no credentials.

Start `createAgentRouter()` with `host: "127.0.0.1"`, `startupProvision: false`, and a long timeout-sweep interval. Open the repository with migrations 017 and 018. Use a separate temporary WebSocket reader for lifecycle state. Close and recreate the router against the same temporary database and port for restart coverage.

Use a matching installed Pi and `@gotgenes/pi-subagents` pair. Resolve both through an explicit identity resolver/factory wrapper owned by the E2E harness; the wrapper must verify package identity and exact version before constructing the session. Do not import the source checkout casually into an installed runtime. Use an isolated matching packaged dependency or a temporary install and record the exact Pi and native-package versions, package paths, and peer-version result in the durable focus ledger. Native-Pi absence or any version/peer mismatch is a hard failing gate. The test must fail with the missing or incompatible identity; it must not skip.

Load extensions through the real Pi resource path with explicit `DefaultResourceLoader.extensionFactories` entries for:

- the native `@gotgenes/pi-subagents` package;
- the current Goal package when required by the runtime;
- the current Messaging package.

The wrapper must assert that the intended native subagent factory and intended Messaging factory are the factories actually loaded by the session. Assert extension identity and count after loading: the native subagent extension is present, the Messaging extension is present exactly once, and no source-checkout or unintended package instance supplied either factory.

Use `SessionManager.create()` for real parent and child transcript paths, `SettingsManager.inMemory()` for settings, and explicitly call `session.bindExtensions({ mode: "json" })`. The E2E identity wrapper must set a temporary `MECHA_ROUTER_URL` for the child process, a temporary cwd inside the project directory, and a sanitized environment containing only the provider, router, Pi, and test values required by the scenario. Restore the parent process environment and cwd in teardown. Obtain the parent service by the parent session ID and subscribe before spawning; this subscription is test-only observation and must not add a production subscription API or change production's polling-only lifecycle publication. Direct service spawning is the first scenario. Add a second subagent-tool launch only after direct spawning is stable.

Required scenarios:

1. **Native start and terminal.** Spawn a child directly. Prove immediate return, queued/running lifecycle, child-session creation before child binding, a redacted heartbeat snapshot, deterministic child completion, typed terminal retention before the next heartbeat, removal on the next typed snapshot, a real child transcript, parent service availability, and child service cleanup. Assert that the loaded native subagent and Messaging extensions are the intended factories before this scenario begins.
2. **Parent settled with a child.** Let the parent settle, keep the child running, and prove heartbeat polling continues. Activity remains settled with the child count until the terminal state is observed by the typed snapshot, then converges to zero.
3. **Failure, steering, abort, and stop.** Use the public native service controls available in the matching package. Prove each terminal status stays visible until the next typed snapshot and is then removed from the active projection; legacy fixtures must prove immediate removal. The parent must remain able to settle. Do not use private manager fields.
4. **Missed event repair.** Exercise a transition where the projection changes in the service but the parent did not receive the corresponding event. Prove the two-second poll repairs it. Use fake service/event injection for any transition the real runtime cannot suppress, and keep the real service poll in the same process for the end-to-end path.
5. **Compaction boundary.** Interleave a parent compaction interval with child activity and a settle event. Prove `compacting` masks the normal state and resume publishes the current child snapshot.
6. **Router restart and reconnect.** Keep a child alive, recreate the temporary router against the same database and port, and prove durable lifecycle state, router version, producer identity, current replacement snapshot ordering, journal retry ordering, duplicate handling, and eventual terminal removal.
7. **New Pi instance.** Persist a running child, create a new isolated Pi parent with the same logical agent ID, and prove the new instance and epoch publish an empty replacement snapshot that removes stale router state.
8. **Redaction and traces.** Put private prompt/output/result/error/timestamp values in native records and prove they never reach lifecycle frames or redacted extension/router traces.
9. **Mailbox interaction.** Send an ordinary mailbox message through the real client and prove its acknowledgment still waits for the persisted parent session entry. Confirm activity or lifecycle publication failure cannot change that result.
10. **Sentinel path.** Prove successful and failed Sentinel handoff still use one native child per message ID and the same owner-scoped service accessor. This is a regression check for the coordinator integration, not a new Sentinel feature.

Run the same lifecycle semantic scenario against the exact pre-extraction implementation in a separate isolated process when practical. Keep protocol-only observations in the existing router/client and delivery tests; do not broaden the lifecycle differential to cover them. Preserve raw ordering and the known duplicate initial lifecycle flush in those existing protocol tests.

Teardown order is explicit: dispose the parent session, close the reader socket, close the router, close the repository, then remove the temporary root. No live router, shared settings file, production database, browser session, or unmanaged process is part of this proof.

## Browser Console Bridge

Browser Console Bridge is not applicable to this extraction. The changed surface is a TypeScript module behind Pi events, the native service registry, WebSocket frames, router SQLite state, and child-session JSONL. It does not change `web/router-ui/`, `web/router-ui-react/`, `tools/router_web_ui.mjs`, browser rendering, or browser interaction.

The existing React lifecycle tests (`web/router-ui-react/tests/transport/lifecycle-sync.test.ts` and the agent-row component tests) remain separate UI coverage. Use Browser Console Bridge only if a later change touches a browser surface, and then use a new isolated tab with a temporary router and temporary UI runtime. Do not use a browser check as a substitute for the Pi/router lifecycle proof here.

## Validation, coverage, and review gates

### Specification and scope gate

Before implementation, review this plan against the current tree, the durable source/history references named above, and the Phase 0 KB search record. The active goal is the accepted specification for this internal behavior-preserving refactor. Confirm the implementation file ledger contains only the lifecycle module, root wiring, focused tests, package-discovery expectation, roadmap update, and comments needed for non-obvious invariants. Confirm no protocol, router, Android, Web UI, `PLAN.md`, or `SPEC.md` change is present.

Run plan-level Robot Review with the applicable `plan-architecture`, `plan-completeness`, `plan-consistency`, `api-design`, `test-coverage`, `documentation`, and `scope` focus areas. Repeat the review after every material plan revision. Validate every proposed finding independently with a `pr-validate`-style pass, record each disposition in the persisted focus ledger, and resolve material findings before implementation. The ledger and review signoffs must identify the plan revision they cover.

### Focused behavior gate

At each implementation phase, run the smallest relevant TypeScript tests. The root's primary behavior must work before implementation Robot Review starts. The test matrix must cover every declared state, transition, failure path, identity fence, bound, and ordering rule. Keep exact outputs and ordered observations in assertions.

Run mutation checks for representative tests. A test that stays green after its key assertion is removed does not satisfy the gate.

### Type, lint, and generated-artifact gate

Run from the repository root:

```text
npm --prefix pi-extensions/mecha-messaging run typecheck
npm run lint:mecha-messaging
npm --prefix pi-extensions/mecha-messaging run check:mailbox-client-drift
git diff --check
```

`mailbox-client.js` must remain byte-identical to the generated output. No generated artifact is expected for `activity-lifecycle.ts`.

### Full test gate

Run the full Mecha Messaging TypeScript suite:

```text
npm run test:mecha-messaging
```

Run the existing router/client and delivery compatibility suites through their documented commands as separate coverage for the unchanged protocol and coordinator boundaries. The lifecycle extraction is not complete if delivery, Goal, compaction, or package-discovery regressions appear in the full suite.

### Coverage gate

Run the complete changed lifecycle source and test set in one parent-process coverage command. This includes the extracted source, root wiring, focused lifecycle tests, differential test driver, package-discovery test, root startup/shutdown ordering tests, trace seam tests, delivery compatibility tests, compaction integration tests, and the installed-Pi lifecycle E2E:

```text
cd /Users/wolfgang/code/mecha-developer
npx --yes c8 --all \
  --include='pi-extensions/mecha-messaging/activity-lifecycle.ts' \
  --include='pi-extensions/mecha-messaging/index.ts' \
  --check-coverage --lines 100 --functions 100 --statements 100 --branches 100 \
  npm --prefix pi-extensions/mecha-messaging exec -- tsx --test \
    tests/mecha_messaging_activity_lifecycle.test.ts \
    tests/mecha_messaging_activity_lifecycle_differential.test.ts \
    tests/mecha_messaging_package.test.ts \
    tests/mailbox_v2_delivery.test.ts \
    tests/native_subagent_trace.test.ts \
    tests/pi_hard_compaction_e2e.test.ts \
    tests/mecha_messaging_activity_lifecycle_native_pi_e2e.test.ts
```

`c8 --check-coverage` enforces the 100% line, function, statement, and branch threshold against both changed production sources. The command must print coverage for both `activity-lifecycle.ts` and `index.ts`; an absent file, uncovered branch, or runner failure fails the gate. Generated files and unreachable defensive branches require a concrete ledger entry and an explicit focused mutation/branch test before they can be excluded. If the repository's existing coverage runner is equivalent, it may replace `c8` without weakening these includes or thresholds and without a permanent dependency change.

Child processes launched by the differential harness and native-Pi E2E do not contribute to the parent `c8` counters. They have a separate mandatory gate: run each harness test, require every child exit status to be zero, compare raw ordered observations for every scenario, and run the listed representative mutations. A passing parent coverage report cannot stand in for that child-process parity and mutation gate.

Coverage must account for:

- typed and legacy service paths, including typed terminal retention before the next heartbeat and legacy immediate removal;
- no service, read failure, and recovery;
- event and snapshot ordering;
- tombstone and ID reuse branches;
- timer start, refresh, cancellation, and boundary cleanup;
- compaction masking and restart, including map/timer clearing, tombstone survival, and scheduler disposal behavior;
- redacted projection and best-effort client failure;
- settled notification suppression and re-arming;
- root startup and shutdown ordering side effects.

Mutation checks are required for the pass bar. Mutate the focused tests or a temporary copy of the module to (1) remove typed pre-heartbeat retention, (2) remove typed omission removal, (3) delay legacy removal, (4) allow a terminal event to publish a lifecycle frame, (5) make compaction lose precedence, (6) clear tombstones at compaction, and (7) move `publishGoalState` or `rememberParentActivityContext` across its required boundary. Each mutation must make the focused suite fail. Restore the source and tests afterward; do not permanently add mutation tooling or dependencies.

### Robot Review focus ledger

This is the complete ledger for the plan review and the later committed-target review. `included` means the named reviewer must produce a clean result or a validated finding. `skipped` means the concrete reason below removes the area from this plan-only review; it must be reconsidered if the implementation diff changes.

| Focus area | Decision | Concrete reason and evidence required |
| --- | --- | --- |
| requirements | included | Check extraction behavior, frozen contracts, ordering assertions, gates, and exclusions against the baseline and observable criteria. |
| conventions | included | Check repository commands, test seams, generated-artifact rules, and existing export patterns. |
| anti-patterns | included | Check degraded and stale-state paths around the module boundary, timers, and child processes. |
| security | included | Check redaction and owner scoping for records, prompts, outputs, errors, timestamps, IDs, and traces. |
| auth-token-safety | skipped | No authentication, token, claim, credential, or token-persistence behavior is planned; the E2E uses sanitized temporary values. |
| external-integration | included | Check Pi events, native service, mailbox client, router frames, and child-process compatibility and ordering. |
| performance | included | Check polling cost, projection bounds, refresh suppression, tombstone caps, and child-process overhead. |
| test-coverage | included | Check focused, differential, E2E, mutation, root-ordering, and enforced coverage gates against runnable proof. |
| architecture | included | Check dependency direction, lifecycle ownership, and composition-root responsibilities. |
| error-handling | included | Check service-read failures, best-effort client failures, malformed records, shutdown, and child-process failures. |
| data-integrity | included | Check snapshot authority, tombstones, omission removal, retention, races, and boundary matrices. |
| api-design | included | Check the internal interface, root re-exports, structural interfaces, and no-new-public-API rule. |
| observability | included | Check trace allowlists, rate-limited failure notifications, raw observations, and lifecycle frames. |
| reported-output | included | Check activity summaries, lifecycle snapshots, status values, counts, bounds, and coverage output on degraded paths. |
| comments-and-docs | included | Check source grounding and required comments for non-obvious lifecycle invariants. |
| naming | included | Check new module, API, callback, and lifecycle names against domain vocabulary. |
| dead-and-unreachable-code | included | Check for duplicate root helpers, stale paths, and incorrect export preservation after extraction. |
| structure-scale | included | Check cohesion and ensure the extracted module does not become a second composition root. |
| observability-noise | included | Check that polling preserves useful tracing and does not add noisy terminal-path logging. |
| scope | included | Check every planned file against the explicit protocol, UI, Android, router, service, and dirty-file exclusions. |
| visual-consistency | skipped | No UI, browser, screenshot, rendered component, or visual asset changes are planned; Web UI and Browser Console Bridge are excluded. |
| plan-architecture | included | Check phased extraction topology, resource ownership, idempotency, and proportionality. |
| plan-completeness | included | Check states, transitions, failures, resources, E2E scenarios, coverage, mutation, and teardown. |
| plan-consistency | included | Check that shutdown, compaction, typed/legacy behavior, ordering, and gates agree across sections. |
| concurrency | included | Check async shutdown, timer polling, compaction races, parent/child contexts, and child-process ordering. |
| simplicity | included | Check each adapter, harness, and API against the approved behavior-preserving boundary. |
| diff-reading | included | Check exact file ledgers and hunk-level ordering, export, and assertion details. |
| architectural-seams | included | Check root callbacks, service lookup, Pi handlers, and client/router boundaries. |
| configuration-safety | included | Check temporary HOME, router URL, provider values, environment sanitization, and fallback behavior. |
| production-risks | included | Check timers, process-wide ownership, post-settlement polling, cleanup, growth bounds, and silent degradation. |
| documentation | included | Check baseline citations, requirements, evidence, exclusions, and cross-section coherence in this plan. |
| skills | skipped | No agent skill or workflow definition changes are planned; the target is a repository plan and implementation. |

Update this ledger after each material plan revision and implementation review with the target tree, reviewer, result (`clean` or validated finding), independent validation disposition, and changed decision. A missing row is incomplete review evidence.

### Independent review gate

After focused behavior and coverage pass, run independent Robot Review on the complete committed target. Include architecture, architectural seams, concurrency, data integrity, external integration, production risks, error handling, test coverage, comments-and-docs, API design, requirements, and scope as applicable. Use `pr-validate` with a different skeptical persona for every proposed finding. Re-review the complete target after each material behavior or contract change. Persist the focus ledger, plan-review signoffs, exact target identity, and each finding's independent validation. The final applicable pass must have zero Blocking and zero Should Address findings.

Do not review an uncommitted working tree. Commit boundaries belong to the later implementation workflow. The review target must be an immutable commit or tree, and the final result must identify that target.

## Documentation and roadmap updates after implementation

The implementation must:

- add concise comments beside polling-only publication, snapshot replacement, tombstones, compaction masking, service read failure containment, and redaction branches. Comments explain the external behavior protected by each branch.
- update `docs/mecha-messaging-quality-refactor-roadmap.md` after the extracted behavior and tests are complete. Mark **Activity lifecycle** complete only with the exact implementation commit/tree, module API, focused tests, differential result, native-Pi E2E result, and full validation result.
- keep the root export list and wire contracts documented by existing tests. Do not add a new public package or a new feature flag.
- record any newly verified native Pi runtime fact in the persistent Pi subagent knowledge entry, with a source path and date. Do not copy private prompts, output, credentials, or temporary data into documentation.

No standalone `PLAN.md` or `SPEC.md` update is part of this slice. Those files are currently unrelated dirty work.

## Scope exclusions

The following work is explicitly out of scope:

- production code or test changes during this planning task;
- protocol schema edits, router or router-proxy edits, Go/Node parity changes, migrations, lifecycle storage changes, or shared-router changes;
- changes to `mailbox-client.ts`, generated `mailbox-client.js`, delivery receipts, durable session checkpoints, Sentinel protocol, or router acknowledgment behavior;
- Goal package changes, Goal event changes, meaningful-activity policy changes, or a second Goal implementation;
- compaction policy or compaction orchestration extraction;
- diagnostics, history sanitation, memory capture, model projection, model controls, reminder controls, or operational tools;
- Android source/tests, Android parity documents, legacy Web UI, React Web UI, `tools/router_web_ui.mjs`, or Browser Console Bridge work;
- worker launcher, Pi settings, package installation, native package version changes, or service deployment;
- changing polling intervals, refresh limits, status classification, redaction, export lists, event names, error text, or shutdown ordering;
- fixing pre-existing TypeScript-versus-JavaScript malformed-input differences discovered during archaeology;
- cleanup or inclusion of the current Android/router-Web-UI/`PLAN.md`/`SPEC.md` dirty files;
- any new feature, subscription API, lifecycle event publication path, protocol field, or UI behavior.

If implementation review discovers a real behavior defect, keep the extraction behavior-preserving and record the defect for a separately scoped change. Do not repair it inside this refactor.

## Observable completion criteria

The extraction is complete only when all of these are directly observable:

- `activity-lifecycle.ts` owns the lifecycle responsibilities listed above, and `index.ts` retains only the documented root adapters and event ordering.
- `Object.keys()` of the Messaging root export remains exactly the existing list. Package discovery loads one Messaging extension and does not duplicate Goal tools or commands.
- The exact old-vs-new lifecycle differential harness reports no unlisted mismatch across all declared scenarios. It compares ordered lifecycle observations, not only final state, and applies only the documented closed intentional-difference allowlist.
- Activity output, lifecycle snapshot output, redaction, status values, child ordering, bounds, timer cadence, refresh suppression, tombstone behavior, compaction masking, typed pre-heartbeat terminal retention, legacy immediate removal, and shutdown cleanup match the pre-extraction TypeScript baseline.
- The root-exported `summarizeNativeSubagents` independently passes its 999-record and arbitrary bounded `status`/`description_length` cases. Lifecycle projections independently pass their 100-record and seven-status cases.
- Native start and terminal events do not publish lifecycle frames. Typed lifecycle frames come from the heartbeat snapshot path only.
- Startup observations show `publishGoalState(mailboxClient)` after client assignment and before delivery-coordinator construction. Shutdown observations show `rememberParentActivityContext(context)` before lifecycle cleanup.
- Compaction and shutdown tests show the activity map and heartbeat timer clear, tombstones survive, compaction keeps the settled scheduler alive, and shutdown disposes it at the existing root position.
- The native-Pi E2E runs with an explicitly resolved, exact-version-compatible Pi/native package pair. Missing packages, identity mismatch, or version mismatch fails the gate; no skip is accepted. The process uses the temporary router URL, cwd, sanitized environment, and asserts the intended native subagent and Messaging factories load.
- Phase 0 records the KB search, accepted active-goal specification, persisted focus ledger, repeated plan-review signoffs, and independent finding validation.
- The real native-Pi E2E runs with a matching Pi/native-package pair, a temporary router/database, and a deterministic provider. It proves the complete child lifecycle, parent-settled polling, compaction boundary, restart/reconnect, new-instance replacement, redaction, mailbox durability, and Sentinel regression paths.
- Typecheck, lint, generated-client drift, focused tests, full tests, focused coverage, mutation checks, and `git diff --check` pass on the complete implementation target.
- Independent review has no unresolved Blocking or Should Address findings, and the final target identity is recorded.
- The implementation diff contains no unrelated Android, router, Web UI, `PLAN.md`, or `SPEC.md` work and leaves shared services and settings untouched.
- The roadmap records the exact final tree and evidence. Wolfgang can inspect the module, run the same tests, and observe the whole lifecycle end to end with baseline behavior and no regressions.
