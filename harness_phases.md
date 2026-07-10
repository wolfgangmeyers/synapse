# Mailbox v2 Implementation Phases

Status: implementation plan  
Source specification: `harness_spec.md`  
Target repository: `mecha-developer`

## Overview

Mailbox v2 replaces the file mailbox, Claude hooks, mailbox cron, tmux message injection, and server-side reminder files with one Pi-only local messaging system. The work is split into five implementation phases because it crosses Pi extension behavior, a durable router, lifecycle management, reminders, email, skills, and full legacy retirement.

Development should happen in an isolated worktree. The installed system must not be changed during Phases 1–4. Those phases produce and validate the complete replacement inside the worktree. Phase 5 performs the repository-wide migration, proves the retirement gates, and prepares one deliberate cutover. Do not operate a long-lived hybrid system or add a second delivery transport.

Each phase is intended to be handed to one worker as a complete dev-loop iteration. Before starting a phase, the worker must read `harness_spec.md` and this document completely. Where this plan and the specification differ, stop and resolve the specification rather than improvising.

## Cross-phase rules

These constraints apply to every phase:

- Use documented Pi APIs only.
- Keep the core Pi-only. No tmux delivery path, Claude compatibility path, or recipient-type branch belongs in the router.
- Treat message bodies as data. The transport must never execute them as shell input, slash commands, authorization signals, markup, or terminal control.
- Bind network listeners to loopback only and create state with user-only permissions.
- Use one SQLite database and one router process.
- Use the six-field v1 message envelope exactly. Reject unknown fields.
- A durable enqueue is not delivery. `pi.sendMessage()` returning is not acknowledgement.
- Unclaimed messages expire after one hour. Successful and expired messages leave no body or tombstone in SQLite.
- Track agent connection history. Remove all resources for identities disconnected for one hour.
- Recurring reminders fire only while their target has a live WebSocket lease.
- Do not add authentication, federation, Redis, NATS, Kafka, browser UI, cron expressions, or speculative abstractions.
- Use deterministic clocks, IDs, and scheduler hooks in tests. Tests must not sleep for an hour.
- Every schema change must have a migration and restart/recovery tests.
- Every phase ends with tests, static checks, `git diff --check`, and robot review before handoff.

## Required worktree setup

Before Phase 1:

1. Create a dedicated sibling worktree and implementation branch.
2. Record the baseline test commands and their results.
3. Confirm the installed Pi version and documentation path used by `harness_spec.md`.
4. Do not symlink worktree files into the installed skills or active Pi configuration.
5. Keep test state under temporary directories. Tests must not touch the live database, live mailbox directory, active reminders file, LaunchAgent, or tmux sessions unless explicitly marked as isolated live acceptance tests.
6. Create a short implementation decision log in the worktree for evidence-based decisions from the unresolved section of `harness_spec.md`.

## Phases

### Phase 1: Prove Pi delivery and freeze the protocol

**Goal**: Resolve the Pi acknowledgement and session-history risks, select the minimum router stack, and freeze executable protocol fixtures before building persistent infrastructure.

**Depends on**: None.

**Produces**:

- A tested Pi extension spike proving or disproving correlated `message_end` acknowledgement.
- A tested session-history lookup for reconnect deduplication.
- A documented result for `--no-session` behavior.
- The selected router language and WebSocket dependency.
- Shared JSON fixtures/schema for HTTP payloads and WebSocket frames.
- A go/no-go decision for the delivery guarantee in `harness_spec.md`.

**Included**:

- Read the installed Pi 0.80.6 extension, session, TUI, and event documentation again before coding.
- Inspect the installed type declarations and implementation only to confirm documented event payload behavior.
- Build the smallest disposable extension fixture capable of:
  - receiving one synthetic message;
  - constructing the proposed `CustomMessage` with embedded router message ID;
  - calling `pi.sendMessage()` with `display: true`, `deliverAs: "steer"`, and `triggerTurn: true`;
  - observing `message_end`;
  - correlating the emitted message to the router ID;
  - reading the current persistent session branch after restart/resume and finding that ID.
- Prove idle and busy behavior separately.
- Prove that the bottom editor remains unchanged.
- Prove what happens when Pi exits after emission but before a hypothetical router acknowledgement.
- Prove and document that `--no-session` cannot provide durable replay deduplication.
- Evaluate the minimum server implementation:
  - check existing repository/runtime dependencies first;
  - compare Python plus one maintained WebSocket dependency with Node using already available WebSocket support/dependencies;
  - reject handwritten WebSocket framing;
  - choose one implementation and record why.
- Define machine-readable fixtures for:
  - the exact six-field message envelope;
  - all client and server WebSocket frames;
  - structured errors;
  - version mismatch;
  - unknown-field rejection;
  - size boundaries;
  - duplicate IDs.
- Reconcile specification contradictions found during the spike. In particular, the one-hour message TTL is fixed and must not remain listed as unresolved.

**Excluded**:

- Production router persistence.
- LaunchAgent installation.
- Producer migration.
- Reminder implementation.
- Legacy adapter implementation.

**Implementation tasks**:

1. Add an isolated Pi integration fixture under the repository test structure.
2. Add a minimal test extension; keep spike-only code clearly separated from the final extension path.
3. Capture representative `message_end` payloads without logging message bodies in normal test output.
4. Identify the documented session accessor used for branch-history lookup.
5. Test session resume, new session, fork, reload, and shutdown behavior relevant to deduplication.
6. Write protocol fixtures that both router and TypeScript extension tests can consume.
7. Select the router stack and pin only the dependency actually required.
8. Update `harness_spec.md` if verified Pi behavior changes any claim.
9. Remove disposable spike code that is not useful as a regression fixture.

**Acceptance criteria**:

- A live Pi test demonstrates visible idle delivery and turn triggering.
- A live busy test demonstrates steering at the next model-call boundary and no bottom-editor mutation.
- The extension can correlate a routed ID through `message_end`, or the phase explicitly fails with that release blocker.
- With session persistence enabled, the routed ID can be found after restart/resume before reinjection.
- `--no-session` behavior is covered and reported as at-least-once only.
- Protocol fixtures reject missing fields, unknown fields, invalid UTC timestamps, invalid IDs, email addresses as agent recipients, and oversized bodies.
- One router language/dependency decision is recorded with evidence and no handwritten WebSocket protocol.
- There is no production installation or active-system mutation.

**Worker handoff**:

Report the exact Pi APIs and event fields proven, the history accessor, test commands, router-stack decision, changed specification text, and any blocked guarantee. Phase 2 must not start if correlation or durable history lookup remains unproven unless Wolfgang explicitly accepts weaker semantics and the specification is revised.

**Estimated complexity**: Complex.

---

### Phase 2: Build the durable router and lifecycle tooling

**Goal**: Deliver a standalone, fully tested local router with durable messages, WebSocket leases, expiry, stale-agent cleanup, diagnostics, and LaunchAgent management, without connecting it to production Pi sessions.

**Depends on**: Phase 1 complete with the protocol and acknowledgement contract approved.

**Produces**:

- Router service implementation.
- SQLite schema and migration framework.
- HTTP and WebSocket APIs.
- Agent lease and connection-history management.
- Fixed one-hour message expiry and one-hour stale-agent cleanup.
- Sender and router administration CLIs.
- LaunchAgent template and finite lifecycle helpers.
- Router unit, integration, restart, and lifecycle tests.

**Included**:

- Implement the proposed `harness/` modules with the fewest files justified by the selected stack.
- Create SQLite tables for:
  - `messages`;
  - `delivery_attempts`;
  - `agents`;
  - `agent_leases`;
  - `schema_migrations`.
- Reserve reminder migration capability, but do not implement reminder scheduling in this phase.
- Enable WAL mode, foreign keys, busy timeout, and explicit transactions.
- Persist messages before attempting WebSocket delivery.
- Implement states and transitions for `queued`, `leased`, `injection_requested`, `acknowledged`, and `dead_letter`.
- Delete acknowledged messages and attempts atomically.
- Delete unclaimed messages and attempts at `created_at + 1 hour`.
- Ensure a message that has been claimed is governed by lease/retry semantics rather than expiring during Pi injection.
- Track `first_seen_at`, `last_connected_at`, and `disconnected_at`.
- Atomically remove all resources for an identity disconnected for one hour.
- Remove never-connected resources one hour after the first resource is created.
- Enforce one live identity lease and one in-flight message per recipient.
- Implement deterministic per-recipient enqueue ordering.
- Implement retry limits and retained dead letters.
- Implement queue count, byte, body-size, and request-rate limits.
- Bind only to `127.0.0.1` and `::1`; reject browser CORS.
- Implement structured body-free logs and rotation configuration.
- Implement `/v1/health`, `/v1/agents`, message inspection, and operator dead-letter retry/dismiss commands.
- Implement `mailbox_send_v2.py` for agent IDs only.
- Implement `agent_router.py install/start/stop/status/doctor/uninstall` with finite `launchctl` operations.

**Excluded**:

- Final Pi extension.
- Reminder CRUD/scheduler.
- PurelyMail and producer migration.
- Installed-system cutover.
- Non-Pi adapter.

**Implementation tasks**:

1. Define schema migration 001 and repository-level data-access methods.
2. Centralize UTC clock and ID generation so tests can inject deterministic values.
3. Implement shared envelope/frame validation from Phase 1 fixtures.
4. Implement idempotent `POST /v1/messages`:
   - same ID and same payload returns the existing state;
   - same ID and different payload returns a conflict;
   - commit occurs before offer.
5. Implement WebSocket registration, renewal, disconnect, duplicate identity rejection, and stale lease recovery.
6. Implement offer, claim/injection-requested, ack, nack, timeout, retry, and dead-letter transactions.
7. Implement the expiry and stale-agent sweepers as idempotent database operations callable directly in tests.
8. Ensure agent cleanup cascades or explicitly removes messages, attempts, leases, and the agent row in one transaction.
9. Add queue/rate limits before persistence and structured HTTP errors.
10. Add the sender CLI with explicit sender precedence and one bounded kickstart retry.
11. Reject `@` recipients and print the explicit `mailbox_email.py` guidance.
12. Build LaunchAgent plist generation with user-only state/log paths.
13. Test install/uninstall against an isolated label and temporary state root.
14. Add status/doctor output for schema, process, active agents, last connection, queue age, next expiry, retries, dead letters, and database size.

**Acceptance criteria**:

- Durable enqueue survives router termination and restart.
- Two concurrent enqueues with the same ID create one logical message.
- Same ID with changed content fails visibly.
- A connected recipient is offered the oldest queued message immediately.
- A disconnected recipient retains it only until reconnect or the fixed one-hour deadline.
- Expiry deletes the body, envelope, attempts, and row without a tombstone.
- Ack deletes message and attempts atomically.
- Lease timeout retries in order and reaches dead letter at the configured attempt limit.
- Duplicate live identity registration is rejected without fan-out.
- One hour after disconnect, no resources remain for that identity.
- Never-connected resources are removed after one hour.
- Router restart preserves original expiry timestamps and queue order.
- Queue-full, oversized, invalid, and rate-limited requests return the specified errors.
- The listener is loopback-only, state permissions are user-only, and CORS is disabled.
- LaunchAgent install/start/restart/status/uninstall works under an isolated test label.
- No test writes to live mailbox, reminder, database, LaunchAgent, or skill paths.

**Worker handoff**:

Provide the schema diagram, migration IDs, endpoint/frame contract, state-transition table, configuration defaults, lifecycle commands, test commands, and any operational caveats. Include evidence that fake-clock tests cover exact one-hour boundaries.

**Estimated complexity**: Complex.

---

### Phase 3: Implement the production Pi extension

**Goal**: Connect Pi to the router with visible, model-context delivery, correlated acknowledgement, reconnect deduplication, compact queue status, and clean lifecycle behavior.

**Depends on**: Phases 1 and 2.

**Produces**:

- Production `mecha-messaging.ts` extension.
- Extension configuration and installation documentation.
- Integration fixtures connecting a real Pi session to the router.
- Proven idle, busy, reconnect, restart, duplicate, and shutdown behavior.

**Included**:

- Resolve `agent_id` in the required precedence order:
  1. explicit extension configuration;
  2. `MECHA_AGENT_WINDOW`;
  3. `MECHA_WORKER_WINDOW`;
  4. fail closed.
- Generate one random `instance_id` per extension connection lifecycle.
- Connect during `session_start`, never in the extension factory.
- On refusal, invoke the finite router kickstart helper no more than once per backoff window.
- Reconnect with exponential backoff and jitter capped at 30 seconds.
- Register protocol/client metadata and renew the identity lease.
- Validate every frame before use.
- Process one leased message at a time.
- Check persistent session history for the routed ID before reinjecting.
- Emit a stable, model-readable `CustomMessage` with `display: true` and the routed ID.
- Always use `deliverAs: "steer"` and `triggerTurn: true`.
- Send `injection_requested` after invoking `sendMessage()`, never ack on wrapper return.
- Ack only after correlated `message_end`.
- Nack validation/injection failures with safe structured reasons.
- Maintain one compact status line with queue count and one truncated preview.
- Clear status after final correlated emission.
- Tear down sockets, timers, and status idempotently during every `session_shutdown` reason.

**Excluded**:

- Reminder scheduling.
- Producer migration.
- Terminal-control behavior.
- Non-Pi harness support.
- Active-system installation.

**Implementation tasks**:

1. Convert Phase 1's verified spike into production extension code without retaining duplicate paths.
2. Implement strict configuration parsing and actionable missing-identity errors.
3. Implement WebSocket connection states and bounded kickstart behavior.
4. Implement frame IDs, lease renewals, pong handling, and stale-connection closure.
5. Implement session-history deduplication before every replay.
6. Define the stable custom message content and renderer.
7. Escape or normalize display-only content so terminal escapes/markup cannot execute.
8. Keep model-visible body content intact within envelope limits.
9. Implement display-column truncation for the status preview, including empty subject/body and Unicode-width cases.
10. Test idle-to-busy and busy-to-idle races; correctness must not depend on an earlier `ctx.isIdle()` snapshot.
11. Test disconnect after lease, after injection request, after Pi emission, and before ack.
12. Test router restart while Pi remains active and Pi restart while the router retains a message.
13. Test all shutdown reasons for leaked timers/listeners.
14. Add an opt-in installation/configuration path scoped to the worktree test environment.

**Acceptance criteria**:

- Idle delivery appears visibly, enters model context, leaves the editor unchanged, and triggers a turn.
- Busy delivery shows one preview line immediately and enters context at the next agent-call boundary.
- Queue depth never creates multiple status lines or transcript previews.
- Ack is emitted only after the matching `message_end`.
- Crash-after-render/before-ack replay does not create a second visible message when session persistence is enabled.
- `--no-session` emits a clear diagnostic that delivery is at-least-once.
- Duplicate IDs do not render twice.
- Reconnect reclaims the same identity and replays in durable order.
- Duplicate live identities fail visibly.
- Connection refusal triggers bounded kickstart without killing an already-starting router.
- Every shutdown reason leaves no extension socket, timer, listener, or status residue.
- A live tmux-hosted Pi acceptance suite covers idle and active cases. Tmux is only the process host.

**Worker handoff**:

Provide extension installation instructions for the isolated test environment, live-test transcripts/screenshots or structured evidence, session mode requirements, race cases exercised, and any behavior that differs from `harness_spec.md`.

**Estimated complexity**: Complex.

---

### Phase 4: Add reminders v2 and cleanup semantics

**Goal**: Add durable one-shot and recurring reminders to the router, including connected-only recurrence, missed-occurrence skipping, and stale-agent resource cleanup.

**Depends on**: Phases 2 and 3.

**Produces**:

- Reminder schema migration.
- Reminder CRUD API and CLI.
- Router scheduler.
- One-shot and recurring firing transactions.
- V1 reminder importer and fail-closed migration tooling.
- Reminder concurrency, crash, cleanup, and live-delivery tests.

**Included**:

- Add reminder fields for target, body, optional subject, creator, creation time, next due time, optional fixed interval, state, linked message ID, occurrence number, and attempt metadata.
- Implement one-shot `--in` and timezone-aware `--at` scheduling.
- Implement recurring `--every <duration>` scheduling.
- Reject cron expressions and non-positive intervals.
- Resolve `self|me|current` only from an unambiguous agent environment value.
- Fire one-shot reminders transactionally into normal mailbox messages.
- Give reminder-created messages the same one-hour pickup deadline.
- Delete completed or expired one-shot reminders with their linked message.
- Keep linked dead letters visible as failed reminders.
- Fire recurring reminders only for a currently connected target.
- Skip disconnected occurrences without queueing or catch-up.
- Advance the recurrence deterministically and prevent overlap.
- Delete recurring reminders and all target resources when the target is hour-stale.
- On first due evaluation for a never-connected target, delete all target resources without firing.
- Implement deterministic IDs from reminder ID plus occurrence number.
- Implement list/delete/delete-all and truthful cancellation conflicts.
- Implement safe import from `reminders.json` under lock.

**Excluded**:

- Enabling migration against the live reminders file.
- Cron-expression support.
- Tmux handoff reminders.
- Producer/skill migration beyond reminder-specific code.

**Implementation tasks**:

1. Add the reminder migration with indexes on state, target, and next due time.
2. Implement API validation and CLI duration/timezone parsing.
3. Implement scheduler polling/wake logic without cron and with an injectable clock.
4. Implement one-shot due transaction and deterministic linked message ID.
5. Implement ack, expiry, dead-letter, retry, and cancellation coupling.
6. Implement recurring due transaction with a live-lease check.
7. Define interval advancement from the prior scheduled due time, not wall-clock completion, so drift is deterministic.
8. If multiple intervals elapsed, advance to the first future slot without enqueueing missed occurrences.
9. Prevent a new occurrence while the prior occurrence is outstanding.
10. Reuse the Phase 2 stale-agent cleanup transaction rather than adding separate deletion logic.
11. Build importer dry-run output: source count, valid count, quarantined count, resolved targets, normalized UTC due times, and deterministic IDs.
12. Make import idempotent and require all v1 writers to be disabled before commit mode.
13. Explicitly reject legacy `"delivery": "tmux"` reminders and report them for manual redesign.
14. Add scheduler crash tests at every transaction boundary.

**Acceptance criteria**:

- Set/list/delete/delete-all works for one-shot and recurring reminders.
- Absolute times require timezone information; relative and fixed recurrence durations parse consistently.
- Ambiguous `self` fails rather than targeting `supervisor`.
- One-shot due creation and message enqueue are one transaction.
- One-shot message expiry removes the reminder and message without retained content.
- A connected recurring target receives one occurrence at each due slot.
- A recently disconnected target receives no occurrence; the slot is skipped permanently.
- A never-connected target is fully cleaned on first due evaluation.
- An hour-stale target has messages, attempts, reminders, leases, and agent row deleted atomically.
- Missed recurring slots do not replay after reconnect.
- Recurring occurrences never overlap.
- Router/scheduler crash cannot duplicate a logical occurrence.
- Cancellation racing with due firing returns the true resulting state.
- Import dry run and commit are idempotent and reconcile counts and UTC times.
- Legacy tmux handoff reminders are rejected, not silently converted.
- No reminder code writes `reminders.json`, mailbox files, or cron entries.

**Worker handoff**:

Provide schema migration details, scheduler invariants, CLI examples, fake-clock boundary tests, import dry-run evidence from a sanitized fixture, and the list of any legacy reminders requiring manual classification.

**Estimated complexity**: Complex.

---

### Phase 5: Migrate every producer and perform atomic retirement

**Goal**: Complete the repository-wide Pi-only migration, remove legacy delivery/control infrastructure, validate the full system in the worktree, and prepare a reversible one-time cutover.

**Depends on**: Phases 1–4 complete and all their acceptance criteria passing.

**Produces**:

- All agent-message producers using the Mailbox v2 facade.
- Explicit outbound-email CLI and migrated email callers.
- PurelyMail ingress using Mailbox v2 with the exact existing allowlist.
- Updated supervisor, worker, mailbox, reminders, watchdog, recall, PR watch, and related skills.
- Pi-native redesign or explicit removal of terminal-control-dependent workflows.
- Removal of `worker_send.py`, file mailbox, mailbox hooks, mailbox cron, reminder cron, and stale requirements.
- Repository/runtime audit tooling and cutover checklist.
- Full end-to-end validation report.

**Included**:

- Make `mailbox_send.py` the stable Mailbox v2 facade after all callers migrate.
- Implement `mailbox_email.py` as the only explicit outbound email command.
- Reject email addresses passed to the agent-message CLI with actionable guidance.
- Preserve inbound PurelyMail allowlist exactly:
  - `wolfgangmeyers@gmail.com`;
  - any sender under `agents.meyers.life`.
- Normalize accepted sender addresses and route inbound mail to `supervisor`.
- Preserve deterministic IMAP-derived IDs.
- Reject disallowed senders before Mailbox v2 enqueue.
- Audit and migrate at least:
  - mailbox skill reply instructions;
  - supervisor and worker status messaging;
  - watchdog and remote watchdog;
  - PR watch;
  - recall;
  - standing orders;
  - KB reminders;
  - memory monitor;
  - remote delegation and SSH forwarding;
  - PurelyMail poll/reply handling;
  - reminders skill and installed copies;
  - installer/package manifests and generated instructions;
  - tests, fixtures, examples, and documentation.
- Classify every `worker_send.py` use:
  - ordinary message: migrate to Mailbox v2;
  - Pi operation with a documented native API: redesign and test;
  - terminal-only operation without a safe Pi API: remove or mark unsupported;
  - do not preserve a generic terminal-injection helper.
- Determine Pi-native handling for authorization, clear/resume, cancellation, shutdown, and other transcript-sensitive controls. Security-sensitive behavior must not be triggered by ordinary message text.
- Replace reminder writers and disable v1 reminder creation before importing.
- Add machine-checkable repository and runtime retirement audits.
- Validate the whole replacement under isolated worktree configuration before touching the installed system.

**Excluded**:

- Non-Pi sidecar adapter. It remains a nice-to-have future client.
- Long-lived dual delivery.
- Tmux fallback.
- Network federation or remote internet listener.

**Implementation tasks**:

1. Inventory every reference to:
   - `mailbox_send.py`;
   - `worker_send.py`;
   - `~/.mecha-developer/mailboxes`;
   - mailbox hooks and hook symlinks;
   - `mailbox_cron.py` and cron entries;
   - `reminders.json`, reminder cron, and v1 reminder writers;
   - `--to` values containing or documenting email addresses;
   - tmux `send-keys`, paste, or Enter used for agent messaging/control.
2. Record each match in a migration matrix with owner, old behavior, new behavior, test, and disposition.
3. Implement and test explicit outbound email without changing the existing SMTP/PurelyMail security contract unnecessarily.
4. Migrate producers one logical group at a time; add an end-to-end test before marking each row complete.
5. Update source skills and every installed/package copy from the same canonical content.
6. Redesign Pi-native control workflows individually. Do not route control strings through ordinary mailbox bodies.
7. Add failing audits for newly introduced legacy paths.
8. Run the full router, extension, reminder, email, and producer suite in the worktree.
9. Run live soak scenarios covering:
   - idle and busy Pi delivery;
   - router restart;
   - Pi restart before and after emission;
   - one-hour message expiry with an accelerated test clock;
   - one-hour stale-agent cleanup;
   - connected recurring reminders;
   - skipped disconnected recurrence;
   - never-connected recurrence cleanup;
   - allowed and disallowed inbound email;
   - explicit outbound email command selection.
10. Disable v1 reminder writers, lock and import pending reminders, and reconcile dry-run/commit counts in a cutover rehearsal using copied state.
11. Rehearse legacy mailbox draining and verify no producer recreates files.
12. Produce exact cutover and rollback commands. Rollback restores the old system as a whole; it must not dual-deliver.
13. At approved cutover:
   - stop old cron jobs and hooks;
   - disable all v1 writers;
   - import reminders under lock;
   - switch canonical sender/install paths;
   - enable router LaunchAgent and Pi extension;
   - verify health and live delivery;
   - drain/classify remaining mailbox files;
   - remove legacy scripts, hooks, cron entries, and stale documentation only after gates pass.

**Acceptance criteria**:

- Repository-wide search finds no direct mailbox-file writer.
- Runtime audit finds no process, hook, or cron job capable of creating/delivering mailbox files or `reminders.json`.
- No active skill or tool instructs an agent to use `worker_send.py`.
- `worker_send.py` and generic terminal message injection are removed.
- No agent-message caller infers email from `@`.
- All outbound email callers use `mailbox_email.py` explicitly.
- Allowed PurelyMail senders enter Mailbox v2 with deterministic IDs and normalized `from`; disallowed senders never enter it.
- Every producer in the migration matrix has an end-to-end test or validated documentation check.
- Every enabled workflow has a Pi-native implementation. Unsupported terminal-only workflows are explicitly removed or disabled.
- No ordinary message body can invoke authorization, slash commands, shell execution, markup execution, or terminal control in the transport.
- Pending reminders import with matching counts, targets, and normalized due times; ambiguous and tmux handoff records are quarantined.
- Active, busy, offline, expiry, restart, ordering, deduplication, stale cleanup, recurring reminder, and email acceptance tests all pass.
- The installed-system cutover checklist is complete, reversible, and does not require a hybrid delivery window.
- All objective retirement criteria in `harness_spec.md` section 12.5 and overall criteria in section 18 pass.

**Worker handoff**:

Provide the completed migration matrix, deleted legacy inventory, full test/soak evidence, reminder reconciliation report, repository/runtime audit output, cutover commands, rollback commands, and any workflow intentionally left unsupported. Do not perform the real cutover without explicit approval.

**Estimated complexity**: Complex.

## Phase dependency graph

```text
Phase 1: Pi proof + protocol
    |
    v
Phase 2: Durable router + lifecycle
    |
    v
Phase 3: Production Pi extension
    |
    v
Phase 4: Reminders v2
    |
    v
Phase 5: Producer migration + retirement + cutover
```

The sequence is intentionally linear. The protocol and acknowledgement proof constrain persistence; persistence constrains extension behavior; connection state constrains recurring reminders; all replacement capabilities must exist before legacy retirement.

## Review and completion protocol for every worker

Each phase worker must:

1. Read `harness_spec.md` and this phase document fully.
2. Confirm the phase dependencies exist and pass before changing code.
3. Write a phase-specific implementation plan before coding.
4. Keep changes inside the worktree and avoid active installed state.
5. Add tests with the implementation, including failure and restart boundaries.
6. Run targeted tests, then the complete relevant suite.
7. Run coverage and identify material untested branches.
8. Run robot review and resolve all validated blocking and should-address findings.
9. Run `git diff --check`.
10. Update documentation and the migration matrix where applicable.
11. Report exact commands, results, changed files, known limitations, and the contract handed to the next phase.
12. Stop at the phase boundary. Do not begin the next phase without a new assignment.

## Nice-to-have follow-up: non-Pi sidecar

Do not implement this during Mailbox v2. If a concrete legacy harness is needed later, create a separate project/phase for a background sidecar that registers as a normal WebSocket client and injects messages through that harness's supported interface. A terminal-only adapter may use a configured tmux pane and bracketed paste/Enter, but it can acknowledge only successful terminal submission unless the harness exposes a stronger event. It must not modify the router protocol, core envelope, Pi extension, or delivery state definitions.

## Final cutover principle

The phases are implementation checkpoints, not production coexistence stages. Build and test the complete replacement in the worktree. Keep the current installed system intact until all retirement gates pass. Then cut over once, verify immediately, and retain a whole-system rollback for the observation period.