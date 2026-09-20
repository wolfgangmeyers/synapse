# Managed workflows requirements

**Status:** Initial product and system requirements; planning only. This document authorizes no production implementation.

## 1. Purpose

Managed workflows let a user define a durable state graph, launch it on a router-managed Pi agent, and let that agent advance the run through a router-owned transition tool. Definitions, active execution state, reminders, transition journals, and retained history live in the router SQLite database. The router, rather than the worker, Web UI, a skill, or a local JSON file, is the lifecycle authority.

This feature is distinct from the existing `.claude/skills/workflows` plain-English template system. That file-backed skill is deprecated for this use case and must not be extended into the managed runtime.

## 2. Goals

1. Persist mutable workflow definitions in the router database.
2. Model each definition as a validated directed state graph with exactly one entry state and named exits that lead to another state or complete the workflow.
3. Launch a workflow with validated bounded string inputs on one router-managed agent.
4. Give the bound managed worker a managed RPC transition tool with an optional journal note.
5. Make the router own state transitions, reminders, compaction sequencing, notifications, recovery, completion, history, and retention.
6. Recover nonterminal runs and pending transition work after a router restart without losing or silently duplicating state changes.
7. Notify and wake the worker on every state entry, optionally adding templated state instructions, and always state the currently available exits.
8. Support one-shot and recurring reminders scoped to a single state residency.
9. Support per-state compact-on-exit behavior, including a reliable wake after the next state is durably entered.
10. Permit intentional cycles and repeated visits to a state.
11. Provide Web UI definition authoring, validation, launch, active-run status, transition journal, and retained history.
12. Remove every terminal run and its run-owned data after seven days.

## 3. Non-goals

- A general-purpose DAG/job executor, arbitrary code execution engine, or distributed workflow service.
- Running workflows on legacy tmux/WebSocket agents, native subagents, Android clients, or non-managed Pi processes in the initial release.
- Parallel states, fork/join, child workflows, timers that transition a run automatically, conditional expressions, or computed routing.
- Letting a worker create or edit workflow definitions through the transition tool.
- Treating prompt templates or reminders as lifecycle authorities.
- Importing arbitrary scripts, environment variables, credentials, or secrets as launch inputs.
- Preserving terminal run history beyond the required seven-day window.
- Automatically converting the existing `~/.mecha-developer/workflows.json` records into state graphs.
- Replacing the existing general reminder or prompt-template features.

## 4. Existing-system constraints

The implementation plan must preserve these repository conventions and boundaries:

- The official router uses a single-connection, WAL, `synchronous=FULL`, foreign-key-enabled SQLite store with ordered forward migrations and immediate transactions for state changes.
- Managed workers receive router-defined, closed-schema tools through the versioned `mecha.tools/v1` manifest and hidden managed RPC control channel. Tool definitions are immutable within an installed manifest revision and arguments are validated before control delivery.
- Managed worker identity is fenced by agent, Pi session, session epoch, process identity, runtime generation, and manifest identity. Workflow control must use the same authority model.
- Router startup performs durable managed recovery before serving requests. Workflow recovery must join that startup boundary and must not rely only on in-memory timers.
- Native parent compaction has structured start/end events and durable router projection. Compact-on-exit must use that native managed compaction path, not synthesize a successful compact event.
- General reminders already provide durable one-shot/recurring delivery, but workflow reminders need additional run/state-entry fencing so they cannot fire outside their owning state residency.
- Existing prompt templates scan text for `${name}` tokens and render them from a flat argument map keyed by the exact text inside the braces. Managed workflows must reuse that argument and interpolation pattern for state-entry text and reminder subjects/bodies, with stricter declaration and validation rules.
- The Web UI uses closed response validation, same-origin router paths, no credential-bearing managed requests, durable router projections, and UI-live refresh. New workflow surfaces must follow those patterns.

## 5. Terms

- **Definition:** User-authored workflow metadata and lifecycle (`enabled`, `disabled`, or `archived`) whose saved revisions contain launch-input declarations, states, exits, reminder specifications, and presentation data.
- **Definition revision:** A monotonically increasing immutable snapshot produced whenever a definition is saved, including an incomplete draft. A revision has a durable validation result; an invalid draft is not runnable.
- **State:** A named node with optional entry text, state-scoped reminders, and a compact-on-exit setting.
- **Entry state:** The single state used when a run starts.
- **Exit:** A transition choice whose worker-visible name is the destination state name, or the reserved completion name `complete`. A state-targeting exit stores the target state's immutable ID so routing remains unambiguous even if the state is renamed in a later definition revision. An exit may also have a user-facing description.
- **Run:** One execution of one pinned definition revision on one managed agent with validated bounded string inputs.
- **State residency / entry occurrence:** One particular visit to a state. Cyclic re-entry creates a new occurrence with a new identity.
- **Pending transition:** Durable work between accepting an exit request and either entering the target state, completing the run, or returning the source residency to active after a failed precondition.
- **Entry notification:** The router-generated, durable worker message for a state occurrence. It includes run/state identity and available exits. Optional templated state text is included in this message.
- **Workflow reminder:** A reminder specification owned by a state and instantiated for a particular state occurrence.
- **Transition journal:** Append-only run events recording launch, state entry, requested exits, optional worker notes, compaction outcomes, completion, recovery, and failures.
- **Active run:** Any nonterminal run, including a run that is active, paused, transitioning, or blocked. An agent may have zero or one active run and can never have more than one.
- **Terminal run:** A completed, cancelled, or failed run. Completion through a named completion exit is the normal terminal outcome.

## 6. Conceptual data model

Exact table and column names belong in an implementation specification, but the durable model must represent at least:

### 6.1 Definitions and revisions

- Stable definition ID, unique user-facing name, description, lifecycle status (`enabled`, `disabled`, or `archived`), current revision, timestamps, and optimistic-concurrency version. Definition deletion is a hard delete, not an alias for archival. It is rejected while the definition has any active workflow; otherwise it transactionally cascade-deletes retained terminal runs and their run-owned data before deleting the definition and revisions, without deleting unrelated shared messages.
- Immutable revision snapshot containing the launch-input declarations, full graph, and durable validation status and errors. Incomplete or otherwise invalid snapshots are retained as drafts, clearly marked invalid, and cannot be launched.
- States keyed by a stable state ID and a name unique within the revision.
- Exactly one entry-state reference per revision.
- Exits keyed by stable exit ID, with a target kind of `state` or `complete`. A state target is stored by immutable state ID; its worker-visible exit name is the target state's name in that pinned revision. Completion uses the reserved visible name `complete`.
- State reminder specifications with one-shot/recurring schedule data and templated subject/body.
- Per-state `compact_on_exit` boolean.

### 6.2 Runs

- Stable run ID; definition ID and pinned revision; managed agent ID; validated launch-input string map; run status; current state and current entry-occurrence ID; timestamps; and a monotonically increasing run version.
- The full managed runtime fence associated with each accepted worker control request. A later runtime for the same managed agent may continue a recovered run only after normal managed readiness and router recovery establish authority.
- A database-enforced uniqueness invariant permits at most one active (nonterminal) managed workflow run per agent. An agent may have no run; every run is bound to exactly one agent.

### 6.3 Transition and delivery records

- A pending-transition record with source occurrence, named exit, target, request/control identity, phase, compaction requirement/outcome, retry metadata, and terminal outcome.
- A unique entry occurrence for every visit, including repeated visits to the same state.
- A durable entry-notification outbox record for every occurrence, with delivery and wake status.
- Workflow reminder instances fenced to run ID and entry-occurrence ID.
- An append-only journal event with sequence, type, source/target state where relevant, optional note, safe error/outcome data, actor, and timestamp.

### 6.4 Definition mutation semantics

- Saving a definition, including an incomplete draft, creates a new revision atomically, records its validation result, and advances the definition's current revision. Invalid revisions remain editable but are clearly marked invalid and are never runnable.
- Existing runs remain pinned to the exact immutable revision selected at launch. Editing, disabling, or archiving a definition must never mutate an active or historical run's graph, templates, or launch-input declarations.
- A definition lifecycle is `enabled`, `disabled`, or `archived`. Disabled and archived definitions cannot launch new runs. Definitions also have a distinct permanent hard-delete operation. Hard delete does not require archival, but it is rejected while any active workflow references the definition. If no active workflow exists, hard delete atomically removes retained terminal runs and their run-owned data, then the definition and its revisions; unrelated shared messages retain their existing ownership and retention behavior.
- Retain the latest three revisions of a definition, counting invalid and incomplete drafts. Also retain every older revision referenced by a nonterminal or retained terminal run until that reference expires. Revision pruning must never invalidate a pinned run.

## 7. Definition and graph validation

Validation is required whenever a revision is saved, through a dedicated server validation-without-saving API, and again at launch against the selected pinned revision. Save and draft validation use the same closed validation result. Invalid or incomplete revisions are persisted as drafts, must be clearly identified as invalid, and cannot be launched; validation failure at launch has no run-side effects.

1. A definition has a bounded non-empty name and at least one state.
2. State names are non-empty, bounded, unique within a revision, and cannot equal the reserved completion value `complete`.
3. Exactly one state is marked as entry. Zero or multiple entry states are invalid.
4. Every state has at least one exit.
5. Every state-targeting exit references an existing immutable state ID in the same revision, and its worker-visible name is exactly that target state's bounded, non-empty name. Completion targets use the reserved visible name `complete`. No other target or independently authored exit name is accepted.
6. A source state may have at most one exit to a given target and at most one completion exit. Because state names are unique within a revision, the submitted visible name resolves unambiguously to the stored target ID.
7. Every state is reachable from the entry state.
8. At least one completion exit is reachable from the entry state, and every reachable state has a path to completion. Cycles are valid and must not be rejected merely because a graph is cyclic.
9. Renaming a target state while authoring a later revision changes that revision's visible exit name but not its stored target identity. Existing runs continue using the names and IDs in their pinned immutable revision.
10. Templates may reference only declared launch-input names and supported router-generated fields. Missing required launch values, unknown submitted input names, and unknown or malformed template tokens are validation errors. They are never left unresolved as literal tokens.
11. Reminder delays/intervals must be positive, bounded, and representable by the router scheduler. Reminder text must fit message limits after interpolation.
12. All names, descriptions, notes, templates, and input values must satisfy explicit UTF-8 byte limits, exclude NUL and lone surrogates, and use closed request shapes.
13. Validation errors identify the definition path (for example, state and exit) and use stable machine-readable codes plus safe user-facing messages.

## 8. Launch inputs and interpolation

### 8.1 Input declarations

A definition declares a closed set of launch-input names. Every input value is a bounded string. Each declaration has a unique, bounded name, a label, an optional description, a required flag, and an optional non-secret string default. The name is the exact, case-sensitive lookup key used between `${` and `}`. It must be non-empty and cannot contain `{` or `}`. Values may contain line breaks because the existing renderer substitutes strings without restricting their contents. There are no numeric, boolean, enum, secret/password, or other typed field kinds.

Launch arguments use the existing prompt-template shape: a flat map from token names to values. For example, `Review ${file}` with `{"file":"app.ts"}` renders as `Review app.ts`. The router accepts only declared input names in this map. Unknown submitted names are rejected. A required input without a submitted value or default is rejected. The router validates names and UTF-8 string bounds before creating a run and stores the resulting string map. It does not coerce or parse string contents as another type.

### 8.2 Template syntax

- State entry text and workflow reminder subject/body use the existing prompt-template token form `${name}`. The exact non-empty text between `${` and `}` is a flat string-map key, not a path or expression.
- A small closed set of namespaced router fields may also be supported, at minimum `${workflow.name}`, `${run.id}`, `${state.name}`, and `${entry.id}`. They are exact reserved keys in the same flat map. The dots have no traversal meaning, and these names cannot be declared as launch inputs.
- Every complete token must name either a declared launch input or a supported router field. Unknown and malformed tokens make the definition invalid. Missing required values and unknown launch arguments make launch fail without side effects.
- Rendering uses the existing literal replacement behavior and runs once over the original template. Replacement strings are inserted as-is, including line breaks and text that looks like another token. They are never scanned again.
- Interpolation has no expression language, dynamic or nested property traversal, shell expansion, includes, or recursive expansion.
- Substituted strings are data. They are never executable markup or tool instructions with elevated authority.
- The router renders and bounds all text before committing launch/entry/reminder work. A render that exceeds a destination limit fails safely before partial activation.
- Inputs are non-secret. The launch UI must warn that values can be persisted and appear in worker messages and history. Secret/password inputs are not supported.

## 9. Run lifecycle

### 9.1 Launch

1. The user selects an enabled valid definition, a running/ready router-managed agent, and launch input values.
2. The router validates that the selected current revision is valid and enabled, rejects missing required or unknown inputs, enforces string bounds, validates agent readiness/capacity, and enforces the database-backed rule that the agent has no other active run.
3. One immediate transaction creates the run pinned to that exact immutable revision, records the validated input string map, creates the entry occurrence and journal entries, activates that state's reminders, and durably queues its entry notification. The state entry succeeds at this durable queueing commit; wake acceptance and worker processing are not prerequisites.
4. Launch returns the durable run projection. HTTP launch uses the repository's managed-request idempotency convention so exact replay returns the same run and a changed payload conflicts.
5. The router offers the durable entry notification only after commit.

### 9.2 State entry

For every initial entry and transition target entry:

- Generate a new entry-occurrence ID even when re-entering the same state.
- Render optional entry text and state reminder text from the pinned revision and run inputs.
- Deliver a visible, durable message that identifies the workflow, run, current state, and entry occurrence.
- Always append a router-generated **Available exits** section listing each destination state name, or `complete`, with its description if present. Templates cannot suppress, rename, or forge this section.
- Include the exact entry-occurrence ID the transition tool must echo. This prevents a late model turn from advancing a newer visit to the same state.
- Wake/trigger the managed worker after the entry transaction commits. The notification must trigger a turn even when the state has no custom entry text.
- State entry succeeds once its entry message is durably queued. Wake delivery succeeds when the managed runtime accepts the wake request; it does not wait for the worker to begin or settle a turn. A wake rejection or disconnect before acceptance leaves truthful retryable delivery state and does not roll back entry.
- Record notification attempts and wake acceptance sufficiently to retry after disconnect or restart without creating another state occurrence. Processing or settlement evidence may be retained only when already naturally available; workflows must not add lifecycle states or dependencies to collect it.

### 9.3 Transition request

The managed manifest exposes a dedicated router-owned tool, conceptually:

`mecha_workflow_transition({ entryId, exit, note? })`

- `entryId` is required and names the current occurrence.
- `exit` is required and must exactly match one available destination state name, or `complete`, in the pinned source state. The router resolves that visible value to the immutable target identity in the pinned revision.
- `note` is optional bounded UTF-8 text written to the transition journal. It is not interpreted as commands or template content.
- The router derives agent/run authority from the live managed binding; the caller cannot select another agent or arbitrary run.
- The tool may be installed on all router-managed workers, but it returns a stable `no_active_workflow` error when no run is active.
- The capability must use the existing managed manifest generation/hash pipeline, raw-argument validation, root context, control ledger, runtime fence, and later control-result delivery. It must not bypass managed RPC by writing directly from the Pi extension.
- A successful call means the transition request was durably admitted, not merely received in process memory. The later control result reports the durable outcome.

### 9.4 Transition ordering

For a normal state-to-state exit:

1. In one immediate transaction, verify the run and source occurrence are current/active, verify the binding fence and exit, reserve exactly one pending transition, freeze source reminder firing, and append the requested transition/note to the journal.
2. If `compact_on_exit` is false, record compaction as not requested and proceed directly to target entry.
3. If `compact_on_exit` is true, request native parent compaction once for the exact live binding and persist its structured lifecycle outcome. Wait for success or confirmed failure/abort, but for no longer than three minutes after request acceptance. Compaction is best effort: request failure, confirmed failure/abort, or timeout records a truthful unsuccessful outcome and then proceeds with the transition.
4. One immediate transaction closes the source occurrence, cancels its remaining reminders, creates the target occurrence, updates the run version/current state, activates target reminders, appends the transition and compaction outcome journal events, and durably queues the target entry notification. Entry is committed at queueing; wake acceptance remains a later obligation.
5. Only after that transaction commits may the router offer the notification and wake the worker.
6. The control result and entry wake are independent durable obligations. Losing the control-result append must not roll back an already committed state entry, and losing the first wake offer must not lose the entry notification.

For a completion exit, the same best-effort compaction attempt applies. Success, failure, abort, request failure, or three-minute timeout is recorded before the final transaction closes the source occurrence and reminders, marks the run completed, records the exit/note and completion event, and creates no next-state notification. An unsuccessful compaction never prevents completion.

### 9.5 Best-effort compaction outcomes

- While a compaction attempt is pending, the run remains durably associated with its source occurrence in a `transitioning` phase; no next state is visible and state reminders do not fire.
- Success, confirmed failure/abort, request failure, and three-minute timeout are distinct journal and observability outcomes. Failure or timeout means compaction was skipped or unsuccessful, not that it succeeded, but the reserved transition still proceeds.
- Recovery resumes the single fenced attempt and its original deadline without issuing a duplicate compaction request. At or after the deadline it records timeout and proceeds.
- A terminal native event arriving after the timeout is recorded as a late outcome for the original transition identity only. It cannot mutate the current occurrence, reverse or repeat the committed transition, suppress or duplicate an entry notification, satisfy a later compaction attempt, or retroactively reclassify the transition's recorded timeout result.
- A subsequent transition request is allowed after the prior transition has durably entered its target or completed the run; there is no compaction-ambiguity blocked phase or manual reconciliation requirement.

### 9.6 Cycles

- State transitions may point backward or to the same state.
- Every visit is a new occurrence with fresh entry notification and reminder instances.
- One-shot reminders fire at most once per occurrence, not once per run.
- Recurrence calculations begin from each occurrence's activation time.
- Idempotency and stale-entry checks apply across all visits so a delayed request from visit N cannot advance visit N+1.

### 9.7 Cancellation and exceptional termination

The Web UI must permit an explicit run cancellation with confirmation. Explicit workflow cancellation closes the active occurrence, cancels workflow reminders and pending delivery work, records a journal event, and marks the run `cancelled`; it never interprets a completion exit. Reset and agent termination close the active occurrence and pending workflow work and mark the run `failed` with a stable lifecycle reason. Restart continues the existing nonterminal run after authoritative managed recovery. It retries only entry notifications or wakes that did not previously reach successful wake acceptance and does not create or deliver a fresh recovery notification for an already delivered entry. No lifecycle action may silently complete a workflow.

## 10. Workflow reminders

1. A reminder specification belongs to exactly one state in one definition revision.
2. It defines subject/body templates, an initial delay relative to state entry, and either one-shot behavior or a positive recurrence interval. The first fire occurs after the initial delay; for a recurring reminder, later fires follow that interval.
3. Activation creates instances fenced by run ID and entry-occurrence ID. They are eligible only while that exact occurrence is active.
4. Exit reservation freezes firing before compaction or target-entry work. State exit/completion/cancellation cancels all unfired instances for that occurrence atomically with the applicable state change.
5. The due-fire transaction rechecks the run, current occurrence, and active phase. A stale instance is cancelled/skipped without enqueueing a message.
6. A race between due firing and transition admission has one serialized outcome: either the reminder message is durably queued before the transition freezes the state, or the transition wins and no message is queued.
7. Recurring reminders skip missed intervals rather than flooding catch-up messages. They do not fire while the agent/run is unavailable or while a transition is compacting.
8. Re-entering a state creates new reminder instances. Old occurrence instances can never reactivate.
9. Workflow reminder messages identify their workflow/run/state and remain distinguishable from general reminders in history and diagnostics.
10. Editing the definition does not alter reminders for an existing run.

## 11. Persistence and restart recovery

- Definitions, revisions, runs, entry occurrences, pending transitions, reminder instances, notifications, and journals are durable SQLite records with foreign keys and bounded indexes.
- Every logical state mutation uses the store's single serialized immediate-transaction path. No run phase is authoritative only in a goroutine, timer, WebSocket, Pi session, or browser.
- Router startup recovers managed agents through the existing readiness/lifecycle sequence, then recovers workflow runs for each authoritative ready binding before ordinary entry/reminder delivery resumes.
- Recovery scans a bounded indexed set of nonterminal workflow work. It must not scan unbounded journal text or reconstruct state from messages.
- An active run with no live binding remains persisted and visibly blocked; it is resumed only when normal managed cold recovery establishes a new authoritative binding for that agent.
- Recovery re-offers only entry notifications or wakes that have not reached successful runtime wake acceptance, re-establishes future state reminders, resumes pending best-effort compaction/transition work against its original three-minute deadline, and resumes due retention work. It does not send a fresh recovery notification for an already delivered entry.
- Recovery must not create a second run, state occurrence, journal event, reminder occurrence, compaction request, entry notification, or wake for already committed or successfully delivered identities.
- Every external action follows durable reservation -> external action -> fenced durable completion. Ambiguous outcomes remain explicit and recoverable.
- Startup fails closed on invalid workflow database invariants; it does not serve a projection that invents a current state.

## 12. Concurrency, idempotency, and errors

### 12.1 Concurrency

- The initial release allows one nonterminal run per managed agent and one nonterminal transition per run.
- Two valid exit requests racing for the same occurrence produce one winner. The loser receives `transition_already_admitted` or `stale_entry`; it creates no duplicate note or target occurrence.
- Definition updates use an expected revision/version. A stale editor receives a conflict and must reload/merge.
- Launch, cancel, transition, reminder fire, managed-agent lifecycle changes, and retention must use explicit ordering/transactions so no orphan references or post-exit reminders are possible.

### 12.2 Idempotency

- HTTP mutations require request IDs and canonical request matching consistent with managed lifecycle endpoints.
- Managed transition deduplication uses the managed control request identity plus `(run, entry occurrence, exit)`. Exact replay returns the recorded outcome. Reuse with changed arguments is a conflict.
- Journal event, entry notification, reminder occurrence, and target occurrence identities have database uniqueness constraints.
- Delivery retries may be at-least-once offers, but state mutation and durable visible message identity are exactly-once.

### 12.3 Error contract

- APIs and tool results return closed, stable codes and bounded safe messages. Examples include `workflow_not_found`, `workflow_invalid`, `definition_revision_conflict`, `agent_not_ready`, `agent_has_active_run`, `no_active_workflow`, `stale_entry`, `exit_not_available`, `transition_already_admitted`, `run_blocked`, and `idempotency_conflict`. Best-effort compaction failures and timeouts are transition outcomes rather than transition-rejection error codes.
- Internal SQL, process, RPC, prompt text, input values, and note contents are not copied into diagnostics or generic error strings.
- A validation failure has no partial durable effects.
- A post-commit delivery failure leaves truthful pending status and is retried; it never rewinds the state graph.
- The UI shows whether an operation was rejected, admitted/pending, blocked, or completed and includes a safe correlation/request ID where available.

## 13. Managed RPC and prompt requirements

1. Add a `mecha_workflow_transition` capability with a closed schema and immutable definition identity through the generated managed-tools contract.
2. The tool is root-worker-only. Native subagents and child contexts cannot transition the parent's run.
3. The router validates tool name, capability, definition, manifest hash, runtime fence, root context, raw arguments, active run, current occurrence, and selected exit before side effects.
4. Tool prompt guidance states that the worker must use only a destination state name or `complete` listed in the latest workflow entry notification, include that notification's entry ID, and may add a concise journal note.
5. Entry messages must remain self-contained after compaction: workflow/run/state identity, optional instructions, exact entry ID, and available destination names are all present in the latest message.
6. Workflow wakes use managed direct delivery semantics and do not depend on a browser being connected. Wake delivery is successful when the runtime accepts the wake request; processing or settlement is not required.
7. The transition tool is not a generic state setter. Its `exit` value accepts only a destination name or `complete` from the latest entry notification; it accepts no arbitrary target ID, unlisted state name, completion boolean, agent ID, reminder changes, or definition changes.

## 14. Web UI requirements

### 14.1 Navigation and list

- Add a first-class **Workflows** surface, not a prompt-template or reminder submode.
- List definitions with name, description, enabled/archived status, current revision, validation status, updated time, and active-run count.
- Support create, duplicate, edit, archive/restore, portable JSON import/export, and permanent hard delete. Hard delete is blocked by an active workflow; otherwise the confirmation must explain that retained terminal workflow history for the definition is also deleted.
- Export the current immutable definition revision as closed, versioned portable JSON without runs, history, or router-assigned definition/revision identities. The file uses snapshot-local stable IDs for states and targets. Import validates the closed format, creates a new router-assigned definition and initial revision, remaps snapshot-local identities transactionally, never overwrites an existing definition, and returns a stable name-conflict error when its user-facing name is already used.

### 14.2 Authoring and editing

- Provide editors for metadata, launch-input string declarations, entry-state selection, states, entry text, destination transitions, optional transition descriptions, reminders, and compact-on-exit. Input declarations have no field-kind selector. A transition selects a target by immutable state identity; its displayed/submitted exit name is derived from the destination state name, with `complete` reserved for completion.
- Provide a graph-oriented overview that makes source, destination-named exit, target, completion targets, entry state, cycles, and unreachable states understandable. A structured form may be the primary editor, but the full graph must be reviewable before save.
- Show inline validation with stable field paths and a definition-wide validation summary.
- Allow invalid or incomplete definitions to be saved as drafts, clearly explain their validation errors, and keep launch disabled until valid. The UI should disallow invalid states where possible. Warn on unsaved changes and report optimistic-concurrency conflicts without overwriting another revision.
- Provide template token insertion/help based only on declared inputs and supported router fields, plus a preview using explicitly entered string values. The preview must use the same flat argument map and literal single-pass `${name}` replacement as launch.
- Editing an active definition clearly states that existing runs remain on their pinned revision.

### 14.3 Launch

- From a valid enabled definition, select a ready router-managed agent and complete a generated string-input form. It must accept bounded multiline values and must not offer numeric, boolean, enum, secret, or other typed controls.
- Disable or explain unavailable agents, including agents with another active run.
- Show the exact revision to be launched and a non-secret input persistence warning.
- An exact replay or page retry must not launch a second run.

### 14.4 Active status

- Show workflow/run/revision, assigned agent, current state, occurrence start time, phase (`active`, `transitioning`, `blocked`, or terminal), available exits, next reminders, compaction status, and last safe error.
- Update through router-owned live projection with HTTP hydration/reconnect fallback.
- Provide explicit cancel with confirmation. Do not provide a UI control that bypasses the listed destination-named exits to force an arbitrary state in the initial release. Agent-unavailable blocked states recover automatically when authoritative readiness returns; best-effort compaction failure/timeout does not create a special blocked state or manual reconciliation control.

### 14.5 History and journal

- List retained runs by definition, agent, status, start/end time, and final exit.
- A run detail view shows ordered state entries, selected exit names, source/target, worker notes, reminder events, compaction outcomes, recovery/blocking events, cancellation/failure, and completion.
- Notes render as untrusted sanitized Markdown under existing sanitization rules. They cannot inject HTML or controls.
- The UI explains the seven-day retention window for completed, failed, and cancelled runs and handles a run disappearing during cleanup without treating it as corruption.

### 14.6 Accessibility and failure states

- Authoring and run controls are keyboard usable, have labels and focus management, and do not rely on graph color alone.
- Loading, empty, disconnected, conflict, validation, blocked-run, and retry-pending states are explicit.
- Client guards reject malformed/oversized workflow responses rather than partially rendering them.

## 15. HTTP and live projection requirements

The implementation specification must define closed versioned routes for:

- list/get/create/update/archive/hard-delete definitions, list/get revisions, and portable JSON import/export;
- validate a draft without saving through a dedicated server endpoint;
- list eligible managed agents and launch a run;
- list/get/cancel active or blocked runs;
- list retained run history and paginated journal events.

All workflow responses use `Cache-Control: no-store`. Mutations use bounded bodies and request IDs. UI-live publishes versioned workflow projection changes only after durable commit; reconnect hydration through HTTP is authoritative. Routes follow the router's existing local-development trust boundary and do not introduce cookies, CSRF tokens, browser credentials, or a second auth model.

## 16. Security and privacy

- Only a currently authoritative, ready managed root binding can invoke a transition for its assigned active run.
- Every worker control is checked against the full runtime/manifest fence. Stale sessions and child contexts fail without effects.
- Launch inputs and notes are non-secret persisted data. UI copy and API documentation say so; logs and generic diagnostics still redact their contents.
- Template interpolation can read only exact keys in the validated flat string map. It cannot access undeclared inputs, files, environment variables, dynamic paths, network resources, or arbitrary objects.
- Definition and template output is never executed as shell, SQL, HTML, or RPC framing.
- Apply existing same-origin static Web UI boundary, closed CORS behavior, request-size limits, output sanitization, database file permissions, and safe diagnostic logging.
- Definition names, input values, prompts, notes, and reminder bodies must not appear in operational log fields. Logs use IDs, phases, counts, and stable error codes.
- Do not impose arbitrary small product limits. The implementation may and must define generous, documented technical safety limits consistent with existing router request, message, scheduler, recovery, storage, and abuse-resistance limits.

## 17. Retention and cleanup

1. Every terminal run—completed, failed, or cancelled—normally remains queryable with its journal for seven days from the time it becomes terminal.
2. At or after that boundary, the router's lifecycle sweep atomically removes the run-owned journal, occurrences, transition records, notification outbox rows, and workflow reminder instances, then the run row.
3. Cleanup does not delete the definition or revisions still referenced by another retained/nonterminal run.
4. Cleanup is foreign-key safe, idempotent, bounded, restartable, and reports counts without text/input/note content.
5. Delivery/message rows shared with general conversation history retain their existing owner/retention rules; workflow cleanup removes workflow references but does not violate message-store retention.
6. A terminal run cannot be reactivated during or after cleanup.
7. Hard deletion is the explicit exception to the normal seven-day terminal retention window. It is rejected if the definition has an active workflow. Otherwise one immediate transaction removes every retained terminal run and its run-owned data, removes workflow references from shared delivery/message rows without deleting unrelated shared messages, and then removes the definition and revisions.

## 18. New `managed-workflows` skill

Create a new skill at `.claude/skills/managed-workflows/SKILL.md` as part of the eventual implementation, distinct from the existing `.claude/skills/workflows` skill.

The new skill must:

- explain that managed workflows are router-owned state graphs, not plain-English templates;
- instruct a managed worker to follow the latest state-entry notification, inspect its available destination state names (or `complete`), and call `mecha_workflow_transition` with the exact entry ID and selected listed value;
- explain optional journal notes, stale-entry errors, best-effort compaction outcomes, recovery/agent-unavailable states, and when to ask the user rather than invent a destination;
- tell authors/users to use the Web UI/API for definition editing and launch rather than local JSON scripts;
- contain no duplicate workflow database, scheduler, transition implementation, or copied definition bodies;
- avoid claiming applicability to unmanaged agents or native child sessions.

The existing `workflows` skill and its JSON scripts remain separately named and are marked deprecated for managed orchestration. Its data is not read by the router. Installation/docs must make both names unambiguous and must not alias one skill to the other.

## 19. Observability

- Emit typed, content-free events for definition mutation outcome, run launch, state entry, transition admission/outcome, reminder fire/skip, compaction outcome, wake retry, recovery, completion, cancellation/failure, and retention cleanup.
- Metrics/counts distinguish transition admission from committed state entry and distinguish notification enqueue from runtime wake acceptance. Existing processing/settlement evidence may be reported when naturally available but is not required for workflow delivery success.
- Web UI history comes from the durable journal, not operational logs.
- Recovery and sweep errors include stable codes and IDs/correlation values only.

## 20. Acceptance criteria

The initial release is accepted only when automated tests and live evidence prove all of the following:

1. Definitions persist across router restart; edits create revisions; an active run remains pinned to its launch revision.
2. Validation rejects zero/multiple entry states, missing target IDs, duplicate state names, duplicate targets from one source, reserved completion-name collisions, unreachable states, states with no completion path, unknown or malformed template tokens, invalid launch-input declarations, and out-of-bound reminders; it accepts valid cyclic graphs and derives visible exit names from target state names.
3. Launch input validation accepts only bounded strings under declared names, rejects missing required and unknown inputs, preserves multiline content, and uses exact flat-map `${name}` substitution in entry/reminder text. Tests prove replacement is literal and single-pass, including values that contain `$`, backslashes, line breaks, or token-looking text.
4. Launch on a ready managed agent atomically creates one run and one entry occurrence, durably queues its entry message, and obtains runtime wake acceptance with optional entry text plus the complete destination-named exit list.
5. The installed managed RPC tool accepts a valid current `entryId`/exit/note and rejects child, stale fence, stale entry, unknown exit, no-active-run, and malformed calls without side effects.
6. Two concurrent transition requests create exactly one transition, one journal note, and one target occurrence.
7. A state-to-state transition cancels/fences source reminders, attempts configured native compaction for at most three minutes, truthfully records success/failure/timeout, commits target entry regardless of unsuccessful compaction, and wakes the worker. No wake can advertise an uncommitted state.
8. Confirmed compaction failure and three-minute timeout proceed exactly once to the selected target, and a late terminal compaction event is journaled without mutating or duplicating the committed transition, occurrence, or wake.
9. A `complete` exit records its optional note, attempts best-effort compaction under the same rule, completes the run, stops all workflow reminders, and sends no next-state entry notification.
10. Self-loops and multi-state cycles create fresh occurrence IDs, notifications, and reminder schedules; delayed old requests cannot advance the new occurrence.
11. One-shot reminders fire at most once per occurrence; recurring reminders fire only during the owning active occurrence; a due-fire/transition race has one deterministic durable winner; missed recurrence slots do not flood.
12. Crash/restart tests at every durable/external boundary recover without duplicate run, transition, compaction-success claim, occurrence, reminder occurrence, journal event, or notification identity.
13. A router restart with a temporarily unavailable agent preserves and exposes the blocked run, then retries only notifications/wakes lacking successful runtime acceptance after authoritative managed recovery/readiness; it does not duplicate an already delivered entry.
14. Web UI functional tests cover authoring/editing, validation errors, conflict handling, graph review including cycles, string-input launch including multiline values, active status, cancel, journal notes, history, disconnect/reconnect, and malformed-response rejection.
15. A real isolated Go router + SQLite + managed Pi + built Web UI exercise demonstrates author -> validate -> launch -> reminder -> compacting transition -> next-state wake -> cyclic return -> completion -> history.
16. Retention tests prove completed, failed, and cancelled runs normally remain before seven days, are graph-safely removed at/after seven days, and leave definitions plus unrelated general message history intact; hard-delete tests prove active workflows block deletion and retained terminal runs are transactionally cascade-deleted without deleting unrelated shared messages.
17. Security tests prove cross-agent, stale-runtime, child-context, arbitrary-target, template-injection, oversized-input/note under documented technical safety limits, and diagnostic-content leakage attempts fail closed.
18. Portable JSON import/export tests cover validation, versioning, identity mapping, name conflicts, and round trips in the initial release.
19. The new `managed-workflows` skill is installed and documented separately; the deprecated file-backed `workflows` skill is not used by router execution.

## 21. Assumptions

These are initial decisions unless changed by a later product decision:

- Only router-managed Pi root agents participate.
- One nonterminal run per managed agent keeps tool routing and user status unambiguous.
- Definition revisions are immutable snapshots; definitions themselves are mutable pointers to the current revision.
- State reminders are relative to each state-entry time.
- Every newly committed state entry communicates destination-named exits; recovery retries only entries whose wakes were not accepted and does not redeliver an accepted entry. Only a newly committed occurrence rerenders entry text and creates reminders.
- A state-targeting exit is displayed and submitted as its destination state name while storing the target's immutable state ID. A completion target uses the reserved `complete` sentinel.
- Compact-on-exit is a best-effort attempt before both state-to-state and completion exits. Failure, abort, request failure, or three-minute timeout is recorded and the transition proceeds.
- Reset and agent termination mark an active workflow failed; explicit workflow cancellation marks it cancelled; restart continues it without duplicating successfully delivered entries.
- Completed, failed, and cancelled runs are normally retained for seven days after becoming terminal; eligible definition hard delete removes retained terminal runs early.
- The transition journal stores optional notes rendered as sanitized Markdown, but operational logs do not.
- All launch inputs are non-secret.

## 22. Recorded decisions and nonblocking follow-up

### 22.1 Recorded decisions

1. State entry succeeds when its entry message is durable. Wake delivery succeeds when the managed runtime accepts the wake request; workflow correctness does not wait for turn processing or settlement.
2. Compact-on-exit is best effort. The router waits at most three minutes, records success, failure, abort, request failure, or timeout truthfully, and proceeds with the reserved transition. Late terminal events are observational only and are fenced from current or later transitions.
3. Reset and agent termination mark an active workflow failed. Explicit workflow cancellation marks it cancelled. Restart continues the run and retries only notification/wake work that never reached runtime acceptance.
4. The latest three revisions are retained, including invalid and incomplete drafts. Older revisions remain while referenced by nonterminal or retained terminal runs.
5. Invalid and incomplete drafts may be saved with clear validation errors and cannot launch. A dedicated server endpoint validates drafts without saving.
6. Definition delete is a permanent hard delete and does not require prior archival. Active workflows block it; otherwise retained terminal runs and run-owned data are transactionally cascade-deleted without deleting unrelated shared messages.
7. Completed, failed, and cancelled runs are normally retained for seven days after becoming terminal and are then deleted with their run-owned data.
8. Journal notes render as sanitized Markdown.
9. The implementation uses generous documented technical safety limits consistent with existing router limits rather than arbitrary small product limits.
10. Reminder first fire occurs after its initial delay; recurring reminders subsequently use their positive interval.
11. Portable JSON import/export is required in the initial release and uses closed versioned snapshots with local identity remapping rather than portable router database identities.
12. State-targeting exits are displayed and submitted as the destination state name, while the graph stores the immutable target state ID. Completion uses the reserved value `complete`.
13. Best-effort compaction creates no special blocked/manual-reconciliation state. Agent-unavailable runs remain visibly blocked and resume automatically after authoritative readiness; explicit cancellation remains available.
14. Launch inputs are bounded strings supplied as the existing prompt-template flat argument map. State-entry text and reminder subjects/bodies use exact `${name}` keys with literal, single-pass replacement. Managed workflow validation rejects undeclared tokens, unknown arguments, and missing required values.

### 22.2 Implementation-specification details

No unresolved product question currently blocks writing the implementation specification. The specification must still select and document concrete generous safety-limit values consistent with existing router limits and fully version the portable import/export JSON schema, but those are implementation design tasks under the decisions above rather than additional product choices.

### 22.3 Nonblocking future product choices

- Whether definitions later support tags, folders, search, or duplication across routers.
- Whether a run later supports a user-authored display name in addition to its generated ID.
- Whether to add a future migration assistant for selected deprecated plain-English workflows, provided it creates drafts and never claims automatic semantic conversion.
