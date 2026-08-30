# Mecha Messaging compaction orchestration extraction plan

## Status, scope, and source of truth

This plan defines the compaction-only extraction contract. The completion record at the end documents the implementation and validation that used this plan.

The implementation baseline is:

- commit: `82b794e67d449865d69779be4c2d3672bf3a2aab` (`Correct lifecycle validation roadmap`);
- tree: `c9b3b49be3922d1e865003084db6127d3632a71f`;
- package: `pi-extensions/mecha-messaging`;
- current orchestration: `pi-extensions/mecha-messaging/index.ts`.

The baseline source and direct tests are authoritative. This plan does not turn desired fixes into baseline behavior. The implementation must copy the behavior at the baseline, including the awkward cases listed under **Frozen baseline behavior**.

The checkout has unrelated dirty and untracked paths, including `tests/android_qol_parity.test.mjs`, `PLAN.md`, `REPORT.md`, `SPEC.md`, and `web/router-ui-react/`. The implementation must use an isolated clean copy of the baseline for differential work and must not stage, reset, rewrite, or include those paths.

The relevant baseline symbols are in `index.ts`:

| Baseline location | Responsibility | Extraction treatment |
| --- | --- | --- |
| `1141-1155` | logical mailbox compaction pause and resume | Move behind the orchestration object. Keep the client connected. |
| `1157-1193` | status and post-compaction handoff formatting/scheduling | Move the orchestration-owned parts. Keep policy helpers unchanged. |
| `1195-1227` | deferred self-compaction request and idle execution | Move with the overwrite and child-session behavior unchanged. |
| `1229-1268` | mandatory hard-compaction guard | Move with request identity checks and existing error containment. |
| `1271-1292` | advisory threshold notification | Move with the existing policy calls and message options. |
| `1801-1810` | `session_before_compact` capture boundary | Move the handler body into the module through an injected capture loader. |
| `1881` | `session_compact` threshold reset/resume boundary | Keep registration and delegate to the module. |
| `1883-1907` | final `agent_settled` ordering and settled suppression | Keep the root ordering; delegate self-request inspection and execution. |
| `1909-1912` | asynchronous shutdown memory capture | Keep the root's recall and deadline setup and existing catch. |
| `2055-2072` | final shutdown cleanup | Keep handler order; delegate only the compaction-owned cleanup. |

The direct behavioral tests are in `tests/mailbox_v2_delivery.test.ts`, especially the compaction tests around lines `2250-2310`, `2355-2390`, `2806-2970`, and `4630-4648`. The existing installed-Pi tests are not a deterministic proof for this extraction and are not used as the required E2E gate below.

## Explicit exclusions

Do not modify or derive implementation work from:

- `/Users/wolfgang/code/mecha-developer/PLAN.md`;
- `/Users/wolfgang/code/mecha-developer/SPEC.md`;
- `/Users/wolfgang/code/mecha-developer/REPORT.md`;
- `compaction-policy.ts` behavior, thresholds, defaults, validation, or message wording;
- `memory-capture.ts` behavior, storage, extraction, sanitization, or retrieval;
- `mailbox-client.ts`, generated `mailbox-client.js`, the delivery coordinator, delivery receipts, or session checkpoints;
- the Goal package, Goal events, or Goal policy;
- the router, router proxy, protocol, Web UI, Android code, or browser tooling;
- package installation, Pi settings, native-Pi package upgrades, or service deployment;
- the live router or any running service.

The extraction may call the existing compaction-policy and memory-capture interfaces. It must not alter either implementation or their external behavior.

## Outcome and file ledger

The implementation will create one internal module and wire it into the existing composition root:

- add `pi-extensions/mecha-messaging/compaction-orchestration.ts`;
- edit `pi-extensions/mecha-messaging/index.ts` only for imports, construction, root adapters, tool delegation, and event-handler delegation;
- add `tests/mecha_messaging_compaction_orchestration.test.ts` for direct module tests;
- add `tests/mecha_messaging_compaction_orchestration_differential.test.ts` for the runnable old-versus-new baseline harness;
- add `tests/mecha_messaging_compaction_orchestration_e2e.test.ts` for the isolated fake-Pi end-to-end path;
- add `tests/mecha_messaging_package.test.ts` for the hard-coded package file list; it must include the new `compaction-orchestration.ts` file;
- update the existing root compaction assertions in `tests/mailbox_v2_delivery.test.ts` only when a moved seam requires an assertion to be relocated or extended.

No new root export is planned. `Object.keys()` of the Messaging package must remain unchanged. No package configuration, lockfile, generated artifact, policy module, memory module, router, or unrelated test is part of this ledger.

## Design alternatives

Three boundaries were considered:

1. **Leave the code in `index.ts`.** This has no wiring risk, but it leaves policy state, pending requests, pause gating, and Pi callback ordering hidden in the composition root. It does not deliver the requested cohesive extraction.
2. **Move policy parsing, memory-capture construction, router access, and Pi event registration into a new service.** This would make the new module a second composition root. It would also change the ownership and lifetime of external resources that this plan excludes from behavior changes.
3. **Use a callback-driven internal orchestration object.** The module owns only compaction state and calls injected activity, capture, UI, message, environment, and error seams. The root retains client/coordinator/capture construction, settled notifications, event registration, and ordering. This is the selected boundary because it gives the extraction a precise API and keeps external resource lifetimes and baseline side effects at their current owners.

The selected design does not add a queue, event bus, request generation, session identity fence, retry loop, or new failure event. Those mechanisms would alter baseline behavior.

## Frozen baseline behavior

These are acceptance requirements. They describe the code at the baseline, not improvements that the extraction should introduce.

### Self-compaction

- `mecha_context` with `action: "compact"` rejects a context without a callable `compact` function through the existing tool catch and error result.
- A valid request is stored in one process-local `pendingSelfCompaction` slot. A later valid request overwrites the earlier request. There is no request ID, session ID, generation, or callback identity fence for self-compaction.
- The request stores `customInstructions` and a normalized per-request handoff note. An omitted note uses the effective policy's default note. The command note wins over the policy note.
- `agent_settled` is the execution boundary. A pending request is left untouched when the context is a native child or when `isIdle()` exists and returns `false`.
- A child can call `mecha_context action=compact` and arm the process-level pending slot. The child settle handler does not execute it. A later parent settle can execute that child-armed request. This child-arm quirk is intentional and must remain.
- When the parent is idle, the pending slot is cleared immediately before calling `context.compact(...)`. The module must not add a second request guard or identity check.
- `onComplete({})` is a valid completion. It resumes logical mailbox activity, resets the advisory notification watermark to zero, and schedules the optional handoff. Empty result fields remain absent from handoff details.
- `onError(error)` resumes logical mailbox activity and sends the existing `Self-compaction failed: ...` UI error. It does not create a new event or failure state.
- The baseline self-compaction call has no new synchronous `try/catch` around `context.compact(...)`. Preserve the existing propagation behavior for a synchronous throw; do not broaden error containment while extracting it.

### Hard compaction

- The context handler checks hard compaction before malformed/orphan history sanitation, exactly as it does today.
- A pending hard request causes `context.abort()` and returns `true`; it does not submit another compaction request.
- A missing usage value, null percentage, or absent mandatory threshold returns `false` without changing state.
- A missing `context.compact` aborts the current work, shows `Pi context limit reached, but compaction is unavailable.`, and returns `true`.
- A hard request stores `{ thresholdPercent }`, calls `context.abort()`, and then calls `context.compact(...)` inside the existing synchronous `try/catch`.
- The hard request object is the existing identity fence: a stale completion or error callback does nothing when the slot no longer points to that request.
- A successful completion, including `onComplete({})`, clears the matching pending hard request and resumes logical mailbox activity. It does not reset the advisory watermark and does not send a handoff.
- An asynchronous hard error clears the matching request, resumes activity, and shows `Hard context compaction failed: ...`. A synchronous `context.compact` throw clears the matching request and shows the same error. These errors remain contained at the existing hard-compaction paths.
- There is no `session_compact_failed` event, no invented failure event, and no new retry policy. `session_compact` remains the existing reset/resume boundary.

### Capture and handoff errors

- `session_before_compact` combines `messagesToSummarize` and `turnPrefixMessages` in that order and passes the array to the existing capture lifecycle.
- Capture loading or capture execution errors are caught by the existing `session_before_compact` path and do not escape that handler. The implementation must not add a new notification, event, retry, or unconditional resume. The existing abort check still resumes on an aborted signal.
- Shutdown capture keeps its existing ten-second controller/deadline and catches all capture errors. Its `finally` still aborts the controller and clears the timer.
- A successful self-completion schedules the post-compaction handoff on the injected zero-delay timer. The timer callback catches `pi.sendMessage` failures so a handoff failure cannot turn a successful compact call into a failed compact call. The handoff remains deferred; do not make it awaited or inline.
- Handoff metadata remains limited to the existing numeric/string fields: `tokens_before`, `estimated_tokens_after`, `first_kept_entry_id`, and `summary_chars`. The full summary is not sent.

### Logical mailbox pause

- A parent compaction start sets one logical pause flag and reports `mailbox: compacting`.
- While the flag is set, the mailbox client's envelope callback ignores normalized mailbox deliveries. This is gating at the callback; it is not client suspension.
- The client remains connected. The coordinator remains alive. The extraction must not call `client.pause()`, `client.resume()`, `client.close()`, or recreate the coordinator as part of compaction.
- A successful `session_compact` or an aborted compaction resumes the logical gate and reports `mailbox: connected`.
- A child context cannot change the parent logical pause. The pause-start operation is a no-op with a missing client or an already-paused gate. The surrounding `session_before_compact` handler still registers its abort listener and runs capture.

### Settled notification suppression

- The root keeps `settledNotificationPending` and the settled scheduler state. The orchestration module does not send settled replies and does not own the settled-notification scheduler.
- In the final `agent_settled` handler, the root checks for a pending self request and clears `settledNotificationPending` before executing the request. This check is intentionally not child-gated, so a child-armed request can suppress the notification at that child boundary.
- The root then invokes self-compaction execution. Only after that does it apply the child/enabled/pending guards, consume the pending notification, and call `onParentSettled(send)`.
- A pending self request therefore suppresses the ordinary settled notification even when the current settle is not yet idle and the request remains pending.
- Native child activity continues to suppress settled notifications through the existing scheduler. This extraction does not add a second settled-notification mechanism.

## Exact module boundary and API

`compaction-orchestration.ts` must not import `index.ts`, the Goal package, `mailbox-client.ts`, or any router module. It may import the existing policy functions and policy types from `./compaction-policy.js`. It uses structural types for the Pi context, activity lifecycle callbacks, mailbox client gate, and memory capture so the dependency direction remains one-way.

The module exports these internal types and one factory. `index.ts` imports the factory and types but does not re-export them.

```ts
export type CompactionErrorLike = Error | {
  code?: string;
  message?: string;
  name?: string;
  cause?: CompactionErrorLike;
};

export type CompactionUsage = {
  percent: number | null;
  tokens: number | null;
  contextWindow: number;
  model?: { provider?: string; id?: string; name?: string; contextWindow?: number; maxTokens?: number };
};

export type CompactionResult = {
  tokensBefore?: number;
  estimatedTokensAfter?: number;
  firstKeptEntryId?: string;
  summary?: string;
};

export type CompactionContext = {
  sessionManager: { getSessionId: () => string };
  model?: { provider?: string; id?: string; name?: string; contextWindow?: number; maxTokens?: number };
  ui?: {
    notify?: (message: string, level?: string) => void;
    setStatus?: (key: string, value: string | undefined) => void;
  };
  isIdle?: () => boolean;
  getContextUsage?: () => CompactionUsage | undefined;
  compact?: (options: {
    customInstructions?: string;
    onComplete?: (result: CompactionResult) => void;
    onError?: (error: CompactionErrorLike) => void;
  }) => void;
  abort?: () => void;
};

export type CompactionEvent = {
  signal?: AbortSignal;
  preparation?: {
    messagesToSummarize?: readonly unknown[];
    turnPrefixMessages?: readonly unknown[];
  };
};

export type CaptureLifecycle = {
  compact: (messages: unknown, options?: { signal?: AbortSignal }) => Promise<unknown>;
  shutdown: (messages: unknown, options?: { deadlineAt?: number; signal?: AbortSignal }) => Promise<unknown>;
};

export type PendingSelfCompaction = {
  customInstructions?: string;
  postCompactionNotification?: string;
};

export type CompactionStatus = {
  policy: import("./compaction-policy.js").CompactionPolicy;
  effectivePolicy: import("./compaction-policy.js").CompactionPolicy;
  modelKeys: string[];
  matchedModelKey?: string;
  selectedTier?: {
    maxContextTokens: number | null;
    levels: Array<{ percent: number; guidance?: string; mandatory?: boolean }>;
    matchedModelKey?: string;
    modelKeys: string[];
  };
  usage?: CompactionUsage;
  notifiedThroughPercent: number;
  configurationError?: string;
};

export type CompactionOrchestration = {
  isMailboxPaused: () => boolean;
  hasPendingSelfCompaction: () => boolean;
  hasPendingHardCompaction: () => boolean;
  status: (context: CompactionContext) => CompactionStatus;
  setPolicy: (
    input: import("./compaction-policy.js").CompactionPolicyInput
      | readonly import("./compaction-policy.js").CompactionLevelInput[]
      | undefined,
    context: CompactionContext,
  ) => CompactionStatus;
  requestSelfCompaction: (
    input: { customInstructions?: string; postCompactionNotification?: string },
    context: CompactionContext,
  ) => PendingSelfCompaction;
  runPendingSelfCompaction: (context: CompactionContext) => void;
  enforceHardCompaction: (context: CompactionContext) => boolean;
  monitorContextThreshold: (context: CompactionContext) => void;
  onSessionBeforeCompact: (event: CompactionEvent, context: CompactionContext) => Promise<void>;
  onSessionCompact: (context: CompactionContext) => void;
  onSessionShutdown: () => void;
};

export type CompactionOrchestrationOptions = {
  getClient: () => { sendMessage?: (message: object, options?: { deliverAs?: string; triggerTurn?: boolean }) => unknown } | undefined;
  isChildSession: (sessionId: string) => boolean;
  rememberParentContext: (context: CompactionContext) => void;
  activity: {
    onCompactionStarted: (context: CompactionContext) => void;
    onCompactionResumed: (context: CompactionContext) => void;
  };
  setMailboxStatus: (context: CompactionContext, value: "compacting" | "connected") => void;
  sendMessage: (message: object, options?: { deliverAs?: string; triggerTurn?: boolean }) => unknown;
  ensureCapture: (signal: AbortSignal | undefined) => Promise<CaptureLifecycle>;
  getEnvironment: () => NodeJS.ProcessEnv;
  initialPolicy: import("./compaction-policy.js").CompactionPolicy;
  initialPolicyError?: CompactionErrorLike;
  errorMessage: (error: unknown) => string;
  setTimeoutImpl: typeof setTimeout;
};

export function createCompactionOrchestration(
  options: CompactionOrchestrationOptions,
): CompactionOrchestration;
```

The exact implementation may use named local aliases instead of inline `import(...)` types, but the method set, argument meaning, return values, and callback ownership above are fixed. In particular:

- `isMailboxPaused()` is the only root read needed for mailbox envelope gating and the activity lifecycle's `isCompacting` callback.
- `hasPendingSelfCompaction()` exists so the root can perform settled-notification suppression at the old point without exposing the pending object or moving that root state machine into the module.
- `hasPendingHardCompaction()` exists so the root `tool_call` handler can retain its pending-hard-compaction block after the child guard, without exposing the pending request object.
- `setPolicy()` resets the module's notification watermark and configuration error exactly where the tool currently does. `getEnvironment()` must return the live root environment so model-key resolution keeps its existing process-environment behavior.
- `CompactionContext.model` is copied from the root `PiContext.model`. The module must trace the baseline model through all three existing uses: `status()` passes it to `effectiveCompactionPolicy()` and `selectCompactionTier()`; `requestSelfCompaction()` passes it to `effectiveCompactionPolicy()` when choosing the default handoff note; `monitorContextThreshold()` includes it in the usage passed to `nextCompactionThreshold()`. No model use may fall back to an unrelated process-global model.
- `onSessionShutdown()` clears the pending self request and the logical pause flag only. It does not clear `pendingHardCompaction`, reset policy, reset the watermark, dispose the settled scheduler, close capture, close the client, or close the coordinator.
- The module owns the pending self and hard request slots, the logical pause flag, the current threshold policy/error, and the advisory watermark for the lifetime of one extension instance.

## State and lifetime ownership

| State or resource | Owner after extraction | Creation | Compaction boundary | Session shutdown | Session restart in same extension |
| --- | --- | --- | --- | --- | --- |
| `client` | root | existing parent `session_start` | stays connected | root closes and clears | recreated by root |
| `coordinator` | root | after client assignment and Goal-state publication | stays alive | root closes and clears | recreated by root |
| `mailboxPausedForCompaction` | orchestration module | `false` at module construction | set true/start, false on resume | set false only | remains false until next start |
| `capture` and lazy `ensureCapture` cache | root | first capture use | remains cached | shutdown calls `capture.shutdown`; root does not clear or close it | same cached object, including its existing closed state |
| local compaction threshold policy | orchestration module | root parses environment once and passes initial value | unchanged except `set_policy` | unchanged | unchanged |
| initial policy parse error | orchestration module | passed from root fallback path | unchanged | unchanged | unchanged |
| advisory `notifiedCompactionPercent` | orchestration module | zero at construction | reset by `session_compact` and successful self completion; unchanged by hard success | unchanged | unchanged |
| `pendingSelfCompaction` | orchestration module | empty | consumed before self `compact`; otherwise retained | cleared | empty after shutdown |
| `pendingHardCompaction` | orchestration module | empty | cleared only by matching callback or synchronous hard error | deliberately not cleared by this cleanup method | retains baseline behavior |
| `settledNotificationPending` and recipient/enabled flags | root | existing root initialization/session start/tool paths | root behavior unchanged | root clears pending/recipient | root behavior unchanged |
| settled scheduler | activity lifecycle module | existing injected scheduler factory | remains usable | root disposes it at the existing point | a new activity lifecycle instance is not created by session start |

The separate capture policy fetched from `/v1/memory/policy` is not the local compaction threshold policy. The root keeps the existing `ensureCapture` closure and passes it into the module. This prevents memory-capture implementation changes and preserves the fact that capture is lazy, cached, and not cleared by the final shutdown cleanup. The module only calls `ensureCapture(...).compact(...)` and receives the existing `CaptureLifecycle` shape.

The root still constructs `activityLifecycle` once per extension instance. It passes `isCompacting: () => compaction?.isMailboxPaused() ?? false` and then constructs the compaction object once the activity callbacks exist. The temporary `let compaction` used by that callback is initialized before any Pi handler can run. The module is not reconstructed at `session_start`.

## Root integration and event ordering

Do not merge handlers or change registration order. The following order is part of the contract.

### Construction

1. Keep root creation of `client`, `coordinator`, `parentActivityContext`, Goal projections, model projections, child-session tracking, and the lazy capture closure.
2. Keep the environment policy parse and fallback: on parse failure, retain the error and use `resolveCompactionPolicy(DEFAULT_COMPACTION_POLICY)`.
3. Construct `activityLifecycle` once with its existing callbacks.
4. Construct `compaction` once with the initial policy/error, `ensureCapture`, `getEnvironment: () => process.env`, `activityLifecycle` compaction callbacks, the root context/client/status/message callbacks, and the injected timer/error functions.
5. Use `compaction.isMailboxPaused()` in the activity lifecycle's `isCompacting` callback and in the mailbox client's `onEnvelope` callback.
6. Keep all existing `mecha_context` schema, descriptions, tool registration, and tool catch behavior. Its status, policy, and self-request calls delegate to `compaction`; the root `tool_call` guard delegates its pending-hard state read to `compaction` as described below.

### Parent startup

Keep the existing parent `session_start` sequence:

1. resolve the worker identity;
2. publish the identity message and remember the parent context;
3. create the mailbox client and its callbacks;
4. assign `client = mailboxClient`;
5. call `publishGoalState(mailboxClient)`;
6. create the delivery coordinator;
7. call `activityLifecycle.ensureHeartbeat()`;
8. start `mailboxClient.connect()`.

The envelope callback must remain a logical gate:

```text
normalize delivery -> if !compaction.isMailboxPaused() and message/delivery are valid -> coordinator.onEnvelope
```

It must not call a client pause/resume method. The `createMailboxCoordinator` closure continues to use the existing owner-scoped native service accessor.

The child `session_start` path returns before parent mailbox, capture, policy, or compaction startup. It keeps its existing transient child client behavior.

### Tool calls and parent events

- `mecha_context action=set_policy`: call `compaction.setPolicy(input.policy, toolContext)`, serialize the returned status, and preserve the existing result shape.
- `mecha_context action=compact`: call `compaction.requestSelfCompaction(input, toolContext)`, then return the same deferred text and details. The module deliberately does not reject a child context here.
- `mecha_context action=status`: call `compaction.status(toolContext)`.
- Keep the tool's outer `try/catch` and `isError` result unchanged. Every `toolContext` adapter must retain `model: toolContext.model` so all three model-dependent module calls see the event's model.
- `tool_call`: retain the child guard first, then call `compaction.hasPendingHardCompaction()`. When it is true, return the existing block/reason/terminate result; otherwise continue to `inlineToolPolicy.evaluate()`.
- `agent_start`: retain the child guard. For a parent, call `activityLifecycle.onAgentStart(context)` first, then arm the existing settled notification state and scheduler exactly as before.
- First asynchronous `agent_settled`: retain the recall handler and its order.
- Second `agent_settled`: retain the threshold-monitoring handler and call `compaction.monitorContextThreshold(context)` in its current position.
- `session_compact`: retain its position between the threshold handler and the final `agent_settled` handler. Keep the parent guard, then call `compaction.onSessionCompact(context)`. The module resets the advisory watermark, logically resumes only when the baseline pause guard permits, invokes `activityLifecycle.onCompactionResumed(context)`, and sets `mailbox: connected`. This is the success/resume boundary; there is no `session_compact_failed` path.
- Final `agent_settled`: retain this exact order:
  1. call `activityLifecycle.onAgentSettled(context)` for a parent;
  2. if `compaction.hasPendingSelfCompaction()`, set root `settledNotificationPending = false` (without a child guard);
  3. call `compaction.runPendingSelfCompaction(context)`;
  4. apply the existing child, enabled, and pending notification guards;
  5. clear the root pending notification flag;
  6. call `settledNotifications.onParentSettled(send)`.

### `session_before_compact`

Keep one root handler at the existing registration point. It delegates to `compaction.onSessionBeforeCompact(event, context)` and returns its `Promise<void>`. The existing child guard remains: child sessions return before parent compaction capture.

For a parent, the module performs these operations in this order:

1. determine whether the logical pause can start (`getClient()` is defined and the gate is not already paused);
2. only when that pause condition is true, call `rememberParentContext(context)`, set its logical pause flag, call `activity.onCompactionStarted(context)`, and set the UI status to `mailbox: compacting`;
3. regardless of client presence or the prior paused state, create the existing resume callback and register its one-shot abort listener;
4. regardless of client presence or the prior paused state, await `ensureCapture(event.signal)`, then call `capture.compact([...messagesToSummarize, ...turnPrefixMessages], { signal: event.signal })`;
5. catch capture-loader and capture-run errors exactly as the baseline does;
6. if the signal is aborted, invoke the same resume callback;
7. return `undefined`.

An absent client or an already-paused gate suppresses only the new pause-start side effects. It must not suppress capture or abort-listener registration. The resume callback still calls the existing guarded resume operation, so it is a no-op without a client and retains the baseline behavior when a prior pause exists. The root must not call `client.pause`, `client.resume`, `client.close`, or coordinator cleanup at this boundary.

### `context`

Keep the existing parent guard and call `compaction.enforceHardCompaction(context)` before history sanitation. Preserve the existing boolean return and all subsequent malformed/orphan history handling.

### Shutdown

Keep both `session_shutdown` handlers and their order.

The first asynchronous parent handler remains responsible for `mailboxSendRateLimits.clear()`, `recallController.stop()`, the ten-second abort controller/timer, branch slicing after the latest compaction boundary, and `ensureCapture(controller.signal).shutdown(...)`. It retains its broad catch and `finally`. A child or child tombstone returns before parent capture cleanup.

The final parent handler retains its existing sequence:

1. `rememberParentActivityContext(context)`;
2. call `activityLifecycle.onSessionBoundary()`;
3. call `compaction.onSessionShutdown()`;
4. clear `parentActivityContext`;
5. clear root `settledNotificationPending` and recipient;
6. keep the module's hard pending slot untouched;
7. call `settledNotifications.dispose()`;
8. close and clear the coordinator;
9. close and clear the client;
10. clear the UI mailbox status.

`activityLifecycle.onSessionBoundary()` must run before `onSessionShutdown()` clears the compaction pause and pending self slot. `compaction.onSessionShutdown()` only clears the module's pending self slot and logical pause flag. It must not call activity resume, capture close, client close, coordinator close, scheduler dispose, or policy reset. A child shutdown still removes only the child reply-client tombstone.

## Phased implementation and required evidence

The repository workflow helper reports the source-of-truth `dev-loop` workflow as: specification, plan review, TDD implementation, coverage, and review. It requires a knowledge-base search before discovery, plan review by Robot Review with independent `pr-validate`, failing tests before implementation, concrete coverage output, and a fresh full Robot Review after fixes. The implementation owner must load the `workflows` skill and run:

```text
python3 .claude/skills/workflows/tools/get_workflow.py dev-loop
```

before production-code work. The retrieved workflow, not a copied summary, is authoritative.

### Phase 0: baseline and KB evidence

- Search `~/.mecha-developer/kb/` for compaction, self-compaction, Pi abort/idle behavior, memory capture, fake providers, and extension test isolation before implementation. Record the exact queries, matching files, and decisions in the implementation handoff. Record an empty result when applicable.
- Materialize the exact baseline commit and record both commit and tree. Run the baseline tests before changing the implementation.
- Use this plan as the requirements source for this pure internal refactor. No separate `SPEC.md` edit is allowed. The implementation handoff must still state the behavior, alternatives, and acceptance evidence required by the dev-loop specification phase.
- Run plan-level Robot Review through the `robot-review` skill, not a shell command. Cover architecture, API, state ownership, ordering, error handling, testability, E2E feasibility, coverage, mutation, and scope. Run `pr-validate` with a different persona for each proposed finding. Repeat after every material plan revision and record sign-off against the plan's commit/tree.

### Phase 1: failing direct module tests

Add `tests/mecha_messaging_compaction_orchestration.test.ts` before moving the implementation. Use injected fake activity callbacks, client, capture loader, timer, message sender, context, and policy. The first version must fail against the absent module API. Then move the code in small cohesive slices.

The direct tests must cover:

- policy initialization, invalid initial policy fallback state, policy replacement, status fields, model override lookup, and watermark reset; pass a model on every context and assert all three model-dependent paths: status effective-policy/tier lookup, self-request default handoff lookup, and threshold-monitor lookup;
- self request note defaulting and command override;
- repeated self requests proving last-write-wins;
- a newer self request being armed before an older completion callback, proving the older callback still runs its baseline side effects because self-compaction has no callback identity fence;
- child request arming, child settle not executing, and later parent settle executing the child-armed request;
- non-idle settle retaining the pending request;
- missing `compact` at request time and missing `compact` at execution time;
- one self request per consumed pending slot and `onComplete({})` resume/reset behavior;
- self `onError` notification and resume behavior;
- hard guard missing usage, advisory-only usage, mandatory usage, pending hard re-entry, missing `compact`, synchronous throw, asynchronous error, and stale hard callback identity;
- `onComplete({})` clearing hard state so a subsequent tool call is not blocked;
- advisory threshold message payload, mandatory/non-mandatory delivery options, watermark, and child suppression;
- compaction-start pause/status/activity ordering;
- capture input concatenation and capture error containment;
- aborted capture resuming activity and non-aborted capture failure following the baseline path;
- deferred handoff scheduling, exact message details, and send failure containment;
- session-compaction resume and watermark reset;
- shutdown clearing only pending self and logical pause while leaving hard pending and policy state observable through later behavior.

No direct test may assert private line structure. Assertions must observe calls, arguments, ordering, state exposed by the exact API, and the root-level behavior described below.

### Phase 2: root wiring and retained tests

Wire the module after the activity lifecycle exists and before any event can run. Keep root tests for:

- tool registration and unchanged tool result text/details;
- child guards and child-arm behavior;
- logical mailbox gating with no client/coordinator pause or restart;
- hard guard before history sanitation, including the root `tool_call` block driven by `compaction.hasPendingHardCompaction()`;
- `session_before_compact`, including capture and abort-listener execution when the client is absent or the gate is already paused, plus `session_compact` and both shutdown handlers;
- shutdown ordering with `activityLifecycle.onSessionBoundary()` observed before compaction pause and pending-self cleanup;
- settled-notification suppression and re-arming;
- client assignment, Goal-state publication, coordinator construction, heartbeat start, and connect ordering;
- policy and capture lifetime across compaction and session restart;
- exact UI status/error text and handoff message options.

The root export list must remain byte-for-byte equivalent in meaning. The package discovery test must still load one Messaging extension with no duplicate tools or commands.

### Phase 3: baseline differential harness

Add `tests/mecha_messaging_compaction_orchestration_differential.test.ts`. This is required and must be runnable without a router, service, installed Pi package, network, or credentials.

The test must:

1. create a temporary directory;
2. use `git archive 82b794e67d449865d69779be4c2d3672bf3a2aab` to materialize the baseline `pi-extensions/mecha-messaging` and its direct `pi-extensions/mecha-goal` dependency into a temporary baseline root;
3. run a baseline child and a new-implementation child separately with the package's local `tsx` executable;
4. in each child, before importing any materialized module, create the temporary fake-`pi` executable, prepend its directory to `PATH`, and install the child-local `globalThis.fetch` stub; the stub and executable must record every call in the child's JSON observations;
5. import baseline `mecha-messaging/index.ts` in one child and the working-tree `mecha-messaging/index.ts` in the other;
6. give both children the same fake Pi runtime, fake client, fixed parent/child contexts (including the same fixed `model`), fake `setTimeout`/`clearTimeout`, fixed policy, and fixed agent identity. Capture scenarios must use the root's normal lazy capture construction with the stubbed fetch and `PATH` fake `pi`; they must not replace that path with a fake `CaptureLifecycle`;
7. make the fake fetch accept only the configured loopback origin and these exact requests: `GET /v1/memory/policy` with the bounded policy response; `POST /v1/memory/lookup` with body `{stores:["default"]}` and response `{records:[]}`; and `POST /v1/memory/apply` with the bounded success response. Record method, path, and parsed body before validating, and reject every other URL, method, or body. Make the fake executable accept only the exact memory-extractor argv shape (`--model`, `--thinking`, `off`, `--print`, `--no-tools`, `--no-extensions`, `--no-skills`, `--no-context-files`, `--no-session`, and one prompt), with `evaluator: "anthropic/claude-sonnet-5"` fixed, record the complete argument array in a bounded child call-log file that the runner reads into observations, emit only `{"decision":"noop","records":[]}`, and exit zero; the expected argv is exactly `["--model", "anthropic/claude-sonnet-5", "--thinking", "off", "--print", "--no-tools", "--no-extensions", "--no-skills", "--no-context-files", "--no-session", expectedPrompt]`;
8. record a JSON observation after each operation and side effect: operation name, call name, arguments, UI output, fetch/fake-`pi` calls, pending-slot-visible behavior, logical pause, compact callback registration, compact callback invocation, root settled suppression, and shutdown state;
9. compare ordered arrays, not only final state, and report the first differing index, operation, field path, and scenario;
10. use a closed normalization allowlist containing only values explicitly injected by the runner. The preferred fixture has no generated values, so the expected allowlist is empty. A new normalization rule requires a baseline citation and plan-review signoff.

The scenario table is:

| Scenario | Required observations |
| --- | --- |
| `self-success` | request, second-request overwrite, parent idle execution, one compact call with the second request, `onComplete({})`, reset/resume, deferred handoff scheduling |
| `self-callback-no-fence` | an older completion callback still resumes and schedules its handoff after a newer request has been armed |
| `self-non-idle` | request retained and no compact call |
| `child-arm` | child request accepted, child settle leaves request pending, parent settle executes it, no child parent-activity side effects |
| `self-error` | `onError` resume and exact UI error |
| `before-capture` | logical pause, status/activity ordering, exact combined preparation array, real lazy capture call through the exact fake fetch and fake-`pi` records, abort resume, and the same behavior when the client is absent or the gate is already paused (capture and abort listener still run while pause-start calls are absent) |
| `hard-guard` | mandatory threshold, abort before compact, repeated context blocked, root `tool_call` blocked through `hasPendingHardCompaction()`, `onComplete({})` unblocks, sync throw/error callback containment |
| `threshold` | advisory and mandatory messages, exact delivery options, watermark reset only at the existing boundaries |
| `shutdown` | `activityLifecycle.onSessionBoundary()` precedes pending-self/pause cleanup, pending self is cleared, hard pending is retained, and policy/capture lifetime is unchanged |

The harness must run with this command from the repository root:

```text
./pi-extensions/mecha-messaging/node_modules/.bin/tsx --test \
  tests/mecha_messaging_compaction_orchestration_differential.test.ts
```

The test's child runner may be embedded as a string, as the existing activity differential harness does. It must install the fetch stub and `PATH`/fake-`pi` before the dynamic import in both children, and it must close or remove those temporary resources after each child. It must exit nonzero on any mismatch and must not print a green result when a child exits nonzero. It must not modify the checkout when materializing the baseline.

### Phase 4: isolated native-extension fake-Pi E2E

Do not use the existing `installedPiRuntime()` skip-based test as the required E2E. That test depends on ambient global packages, an ambient package root, and provider/runtime timing; missing packages are currently skipped. Those conditions cannot prove this extraction and are not a valid expected-pair gate.

Add `tests/mecha_messaging_compaction_orchestration_e2e.test.ts` with a child-process harness. It must be deterministic and isolated:

- create a temporary root, `HOME`, `PI_CODING_AGENT_DIR`, project directory, and `PATH` entry;
- before importing the extension, write an executable temporary `pi` program at `<temp>/bin/pi` and prepend `<temp>/bin` to `PATH`;
- make that executable accept exactly the existing memory-extractor invocation shape: `--model`, `--thinking`, `off`, `--print`, `--no-tools`, `--no-extensions`, `--no-skills`, `--no-context-files`, `--no-session`, and one prompt argument;
- make it reject unknown arguments, read no unbounded input, record the complete argv in a bounded call-log file, emit only `{"decision":"noop","records":[]}`, and exit zero; with `evaluator: "anthropic/claude-sonnet-5"` fixed, assert the full exact argv array `["--model", "anthropic/claude-sonnet-5", "--thinking", "off", "--print", "--no-tools", "--no-extensions", "--no-skills", "--no-context-files", "--no-session", expectedPrompt]`, including the complete deterministic prompt; cap its output and execution timeout in the harness;
- install a child-local `globalThis.fetch` stub before importing the extension. It accepts only the configured loopback origin and these exact requests: `GET /v1/memory/policy` with a bounded recall/capture policy; `POST /v1/memory/lookup` with body `{stores:["default"]}` and response `{records:[]}`; and `POST /v1/memory/apply` with a bounded success response. It records every method, path, and parsed body, and rejects every other URL, method, or body;
- use a fake bounded Pi runtime object with `events.on`, `on`, `registerTool`, `registerCommand`, `sendMessage`, and `exec` recording calls; let the real root lazy capture path run for capture scenarios. Use a fake client only for the mailbox boundary where direct isolation is preferable;
- invoke the real `createMechaMessagingExtension` factory and its registered handlers/tools in the child process;
- restore the parent environment and cwd in a `finally` block and remove the temporary root after all child resources close.

The fake executable and fetch stub make both the compaction capture path and the memory-capture shutdown path runnable without a live router or an installed Pi. The fake Pi runtime must keep callback execution bounded and explicit. In the capture scenario, preparation must contain a successful capturable tool result, such as `{id:"tool-result-1", type:"message", message:{role:"toolResult", content:[{type:"text", text:"The verified result is available."}], isError:false}}`. Assert that this result causes exactly one fake-`pi` invocation whose complete argv equals the expected array, including the full prompt string, and that the fetch record is exactly `POST /v1/memory/lookup` with `{stores:["default"]}` returning `{records:[]}`; an exited child or swallowed capture error is insufficient. No test in this file may call `t.skip`. There is no installed-Pi version precondition for this extraction. The required E2E has no installed-Pi skip branch. If a separate future native-Pi test is retained, a missing package, wrong package identity, or version/peer mismatch must throw a failing error; it must never skip an expected matching-Pi gate.

The E2E scenarios are:

1. parent startup, `mecha_context` self request, parent settle, exactly one compact call, `onComplete({})`, deferred handoff, and no settled notification;
2. non-idle parent settle retaining the request and a later idle settle executing it;
3. child self request accepted, child settle not executing it, then parent settle executing it;
4. hard threshold abort, duplicate-trigger suppression, `onComplete({})` unblock, and synchronous/asynchronous hard failure containment;
5. `session_before_compact` with a successful capturable `toolResult`, asserting the exact fake fetch records and exactly one fake-`pi` invocation with the complete expected argv (including prompt), followed by `session_compact` resume;
6. mailbox delivery while logically paused is ignored while the client and coordinator remain alive;
7. parent shutdown runs bounded capture cleanup, clears only pending self through the final handler, and leaves the process usable for the asserted baseline behavior.

The E2E must compare its recorded ordered observations with the corresponding differential scenario where practical. It must not claim native Pi behavior, router durability, or provider behavior that the fake runtime cannot exercise.

### Phase 5: implementation review

The implementation owner must:

- run focused tests after each TDD slice;
- run the Node coverage command below and use the `test-coverage` skill with `lang=node`, package `pi-extensions/mecha-messaging/compaction-orchestration.ts`, and a focus on the extracted module;
- confirm every behavior in this plan has a passing direct or root test;
- run Robot Review on the complete diff with one reviewer per applicable focus area;
- run `pr-validate` with a different persona for each reviewer finding;
- address valid findings and run a fresh full Robot Review after each material fix until all reviewers sign off;
- compare the final diff with the baseline and re-read the file ledger and explicit exclusions;
- record exact commit/tree identity and all test, differential, E2E, coverage, mutation, and review results in the implementation handoff.

## Validation commands

Run these from `/Users/wolfgang/code/mecha-developer` against the completed implementation.

### Type, lint, generated artifact, and diff checks

```text
npm --prefix pi-extensions/mecha-messaging run typecheck
npm run lint:mecha-messaging
npm --prefix pi-extensions/mecha-messaging run check:mailbox-client-drift
git diff --check
```

`mailbox-client.js` must remain unchanged. No generated artifact is expected for the new module.

### Focused tests

```text
./pi-extensions/mecha-messaging/node_modules/.bin/tsx --test \
  tests/mecha_messaging_compaction_orchestration.test.ts \
  tests/mecha_messaging_compaction_orchestration_differential.test.ts \
  tests/mecha_messaging_compaction_orchestration_e2e.test.ts \
  tests/mailbox_v2_delivery.test.ts
```

### Full package tests

```text
npm run test:mecha-messaging
```

Existing installed-Pi tests may remain separately available, but their skip behavior does not satisfy this extraction's E2E evidence and their result must be reported separately.

## Exact coverage gate

The threshold applies to the extracted source only. Root `index.ts` wiring is covered by the focused root tests and the differential/E2E ordered observations; it is not misleadingly reported as 100% coverage for the entire 2,000-line composition root.

Run the `test-coverage` skill for the Node package and then run this exact c8 command:

```text
cd /Users/wolfgang/code/mecha-developer
rm -rf /tmp/mecha-compaction-coverage
npx --yes c8@10.1.3 \
  --all \
  --extension=.ts \
  --include='pi-extensions/mecha-messaging/compaction-orchestration.ts' \
  --exclude='**/*.d.ts' \
  --reporter=text \
  --reporter=json-summary \
  --report-dir=/tmp/mecha-compaction-coverage \
  --check-coverage \
  --lines=100 \
  --functions=100 \
  --statements=100 \
  --branches=100 \
  ./pi-extensions/mecha-messaging/node_modules/.bin/tsx --test \
    tests/mecha_messaging_compaction_orchestration.test.ts
```

The pass bar is 100% lines, functions, statements, and branches for `compaction-orchestration.ts`, with that file present in both the text report and `/tmp/mecha-compaction-coverage/coverage-summary.json`. The direct test command must pass. No branch may be excluded because it is inconvenient. A branch that cannot be reached must have a source invariant and a focused test at the boundary that proves the invariant; otherwise it is a coverage failure.

The root wiring must also be exercised by the focused tests for all of these paths: startup, `mecha_context` policy/status/compact, parent and child settle, `session_before_compact`, `context`, `session_compact`, both shutdown handlers, logical envelope gating, and settled-notification suppression. This is a behavior gate, not part of the extracted-module percentage scope.

## Mutation proof

Mutation proof is required in addition to coverage. Do not add permanent mutation dependencies or leave mutated source in the checkout.

After the implementation is green:

1. copy the final `compaction-orchestration.ts` and any root wiring fixture needed by the test to a new temporary directory;
2. for each row below, apply exactly one textual mutation to the temporary copy;
3. run the same focused direct/root test command against that temporary copy through the test harness's module-path override;
4. require a nonzero test result and name the assertion that failed;
5. restore the original temporary copy before applying the next mutation;
6. remove the temporary directory and verify the real checkout is byte-identical to the pre-mutation target with `git diff --check` and a hash of each changed file.

The test harness must support an explicit module path argument for the temporary copy. If a direct import cannot be overridden, the mutation runner may create a temporary test copy with only the import path rewritten. It must not modify the repository's committed tests or source.

| Mutation | Required failing assertion |
| --- | --- |
| remove the pending-self guard in `runPendingSelfCompaction` | duplicate/non-idle or child settle test |
| remove self last-write-wins assignment and retain the first request | overwrite test |
| add a self request identity/session fence | child-arm test |
| remove the child guard from self execution | child settle test |
| leave self pending after consuming it | one-request/duplicate compact test |
| remove `onComplete({})` self resume or watermark reset | self completion test |
| make self handoff send synchronous or rethrow its error | deferred handoff containment test |
| remove logical pause gating from the envelope callback | paused-delivery test |
| call client pause/resume or close/recreate the coordinator | no-client/coordinator-pause test |
| remove the hard pending guard | repeated hard-trigger request-count test |
| remove hard request identity checking | stale hard callback test |
| remove hard cleanup on `onComplete({})` | hard unblock test |
| remove hard synchronous/asynchronous catches | hard failure containment test |
| make empty hard `onComplete({})` invalid | hard empty-result test |
| move capture errors out of the existing catch | capture containment test |
| add unconditional resume after a non-aborted capture failure | baseline differential test for capture failure |
| reset/clear hard pending state during shutdown | shutdown-only-self-clear test |
| reset policy or watermark at session start | policy lifetime/differential test |
| add a `session_compact_failed` handler or alternate error event | event registration and differential observation test |
| remove `hasPendingHardCompaction()` from the root `tool_call` path | repeated hard-trigger tool-call block test |
| move settled suppression after self execution or add a child guard | settled-notification suppression and child-arm tests |
| return early from `session_before_compact` when the client is absent or the gate is already paused | baseline capture/abort-listener differential test |
| clear compaction pause or pending self before `activityLifecycle.onSessionBoundary()` | shutdown ordering test |
| omit `CompactionContext.model` from any of the three model-dependent calls | model override status, self-note, or threshold test |

Every mutation must make at least one test fail. A mutation that leaves the suite green means the corresponding assertion is missing and must be added before acceptance.

## Completion record

The compaction orchestration extraction and its focused tests are complete in commit `6b752c4e5b8aeb90572755e0d8db3738207d5d26`, tree `042c04a378430c6ba6f36504ee6c9e8351678ea6`. Validation stayed local: no service was started, no live router was contacted, and no production runtime was changed.

Validation evidence for the completed target:

- The focused compaction command passed 24 tests: 15 direct orchestration tests, 4 differential-harness tests, 1 isolated fake-Pi E2E test, and 4 installed-Pi tests.
- The isolated fake-Pi E2E ran all seven scenarios without a skip. It used temporary module-resolution, HOME, Pi settings, project, session, SQLite, memory, and fake-`pi` paths. It verified the exact capture request and complete fake-`pi` argument list.
- Plain `npm test` exited successfully. The TypeScript package suite reported 432 passed, 0 failed, and 3 expected installed-Pi skips. The JavaScript suite reported 391 passed, 0 failed, and 2 unrelated environment skips.
- With the installed package root explicitly configured, all four installed-Pi tests passed. With no installed package available, the same tests reported the expected three skips. A configured missing root and a configured package with the wrong manifest identity failed all three installed-Pi tests; those cases do not silently skip.
- Typecheck, Mecha Messaging lint, and `git diff --check` passed.

A 29-mutation proof was completed against temporary copies of the extracted module and root test fixture. Each mutation was applied once, its focused assertion failed, the temporary copy was restored before the next mutation, and the temporary directory was removed afterward. No mutation runner, permanent mutation dependency, or mutated source was added to the repository. All 29 mutations were killed across self-compaction request and completion state, hard-compaction guards and callbacks, logical mailbox pause and capture/handoff behavior, policy and model forwarding, root tool/event ordering, and shutdown ownership and ordering.

The proof covered the named categories in this plan: self overwrite, idle and child gating, pending-slot consumption, completion and handoff behavior; hard re-entry, identity, empty completion, and error containment; capture failure and resume boundaries; mailbox gating without client or coordinator pause; policy and watermark lifetime; root tool-call and settled-notification ordering; session-before-capture and shutdown ordering; and model propagation. The proof did not claim a live router, provider, or native Pi behavior beyond the separate installed-Pi run.

## Review focus and completion criteria

The plan-level and implementation-level review must explicitly cover:

- API shape and dependency direction, including `hasPendingHardCompaction()` and `CompactionContext.model`;
- ownership and lifetime of pause state, policy, watermark, capture cache, and both pending request slots;
- exact root event registration and side-effect order, including shutdown session-boundary ordering and the root `tool_call` use;
- child-arm, overwrite, and no-identity-fence behavior;
- hard identity fence versus self's lack of one;
- capture, handoff, and hard error containment without invented behavior;
- logical gating without client/coordinator pause or restart;
- `session_before_compact` capture and abort-listener behavior when the client is absent or the gate is already paused;
- settled-notification suppression;
- differential harness source materialization, pre-import fetch/PATH stubbing in both children, exact fake capture calls, closed normalization, ordered comparison, and child exit handling;
- native-extension fake-Pi E2E isolation, bounded fake executable/fetch, exact capturable-`toolResult` invocation arguments, absence of skips, and limits of native-Pi claims;
- exact c8 scope and thresholds;
- mutation proof and restoration;
- TypeScript type safety without `any`, `unknown as`, or unsafe assertion casts;
- explicit exclusion compliance and absence of unrelated changes.

The extraction is complete only when:

- `compaction-orchestration.ts` implements the exact internal API and `index.ts` remains the composition root;
- root exports, tool schema, messages, status text, policy behavior, capture behavior, and generated client remain unchanged;
- the root uses `hasPendingHardCompaction()` for the existing pending-hard `tool_call` block, and all three model-dependent paths receive `CompactionContext.model`;
- the baseline differential harness reports no unallowlisted ordered mismatch, including the exact fake fetch and fake-`pi` capture records;
- the fake-Pi E2E passes without a router, installed Pi, service, network, or skip;
- direct module coverage meets 100% lines/functions/statements/branches within the stated scope;
- every listed mutation fails its named test and the final target is restored;
- typecheck, lint, drift, focused tests, full tests, and `git diff --check` pass;
- Robot Review and independent validation have signed off on the final immutable target;
- no service was started and no live router was contacted during this implementation validation.
