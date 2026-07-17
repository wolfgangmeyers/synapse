# Sentinel dispatch

## Scope

**Observable behavior:** an authorized sender can create a one-time or recurring durable Sentinel dispatch. When due, the addressed Pi extension launches one isolated child with the resolved prompt body. The parent sees body-free Sentinel transport status; the child can send any ordinary V2 messages directly to supervisor while it runs.

**Non-goals:** Phase 1 implements nothing. Sentinel is not a general process launcher, remote-execution API, arbitrary Pi configuration surface, or a replacement for ordinary mailbox delivery. It adds no scheduler, cron watchdog, second queue, or second delivery API. A prompt cannot choose cwd, skills, tools, permissions, Pi flags, extensions, or a template path.

## Required behavior

### Preserve default delivery

Ordinary `POST /v1/messages` envelopes and reminders retain their present durable enqueue, lease, delivery, acknowledgement, retry, replay, expiry, recurrence, and visible Pi-message behavior exactly. A message without explicit Sentinel metadata follows that path without interpretation.

### Generic Sentinel creation

A caller script offers a simple interface for:

- a named, supervisor-owned template or explicit prompt input;
- normal sender, recipient, and optional subject;
- immediate, one-time delayed, or recurring dispatch; and
- one selected model from the receiver-approved allowlist.

For a named template, the script resolves the template under the supervisor skill before sending and applies its text to produce the final prompt body. The router receives only generic Sentinel metadata plus that opaque resolved body; it never receives a template name, skill path, sender convention, taxonomy, or special branch. Explicit prompt input is sent as the final body directly. Template selection is solely a caller concern. The router and recipient independently validate only the generic receiver policy and selected model at admission and launch.

A Sentinel dispatch uses the existing durable messages API immediately and the existing durable reminders API for one-time or recurring work. The router validates the normal message/reminder shape, generic Sentinel schema, allowed receiver profile, sender/profile authorization, and allowed model; applies existing body/queue limits; and records intent durably before offer. Invalid input rejects safely and never falls back to ordinary delivery.

Each receiver-approved execution profile is fixed and static. It defines the eligible receiver, allowed models, fixed child cwd/tool/permission/extension configuration, and the supervisor recipient for child output. The final prompt body has no authority to alter any profile setting. Sender authorization is a static profile allowlist. This is a policy boundary within the current loopback/same-user trust model, not a claim to authenticate hostile same-user processes.

For reminders, each due occurrence has one deterministic Sentinel occurrence/message identity. Existing reminder rules remain authoritative: connected-recipient requirement, no overlapping linked occurrence, skipped missed occurrences, and no catch-up. Identical reuse of a message/reminder ID returns its existing record; changed content fails with the existing idempotency conflict semantics.

### Parent and child behavior

On an offered Sentinel dispatch, the recipient extension durably acknowledges it but does **not** inject the original body into the parent Pi conversation. It instead persists and displays only sender, subject, message ID, Sentinel/occurrence ID, profile/model label, and transport-neutral lifecycle state such as `accepted`, `launch-claimed`, `launching`, `running`, `child-exited`, `success`, or `failure`. Parent session entries, UI notifications, logs, and status-oriented events must contain neither prompt text, a preview, a body-derived diagnostic, nor a body hash.

The extension owns active child launch. It uses the fixed execution profile and final prompt body to launch one isolated Pi child. The child has a dispatch-scoped outbound V2 identity/capability that permits normal direct V2 messages only to the profile's configured supervisor while it runs. The router does not launch Pi, resolve templates, or execute prompt-specific behavior.

A Sentinel child may send any ordinary V2 messages directly to its configured supervisor while it runs. It may include the generic dispatch and occurrence IDs in those ordinary messages for supervisor-side correlation. The router and extension treat their content as opaque: they neither inspect, parse, validate, deduplicate, generate, require, nor label child messages. A template alone decides whether a final report is required and what it means. Those ordinary child messages are delivered to supervisor and never reflected into the parent session.

### Lifecycle, observability, and recovery

The durable Sentinel record distinguishes at least accepted/queued, offered, parent-received, launch-claimed, launching, running, launch-failed, child-exited, child-process-success, child-process-failure, and terminal delivery failure. Read APIs and router JSONL events expose generic IDs, occurrence/attempt, actor IDs, profile/model, timestamps, and launch/exit diagnostic codes, never prompt body or child-message labels in status fields.

Before launch, the extension obtains an authoritative idempotent launch claim for the durable occurrence. Reconnect, replay, duplicate offers, persistence retries, and parent restarts observe that claim and cannot launch a second child. A pre-launch failure is retryable through normal delivery/replay semantics. If launch state is uncertain, recovery queries the durable claim and known child identity/exit state first; unknown state is not permission to create another child. Once a child is known launched, replay only reattaches observation. Child exit records only the observable process success/failure fact; it never invents, requires, or labels a semantic report.

The router retains the prompt body only in the existing durable message/reminder intent for its established lifecycle; it stores no second parent-delivery copy or body history. Existing cleanup removes completed queue state. The parent Pi session never persists the body.

## First Sentinel instance: supervisor watchdog template

The watchdog is the first supervisor-owned named template, not a router or extension special case. Its template lives under the supervisor skill, is resolved by the caller script before generic Sentinel submission, and may instruct its child to use `findings`, `quiet`, or `error` in the result body. Those words have no schema, migration, endpoint, extension, lifecycle, or router meaning beyond ordinary prompt content.

The watchdog skill and its supervisor prompt template must contain this requirement verbatim:

> The watchdog skill and its supervisor prompt template must tell the child exactly how to examine live worker windows through managed supervisor evidence, not an assumed opaque snapshot. It starts with durable task state and worker inventory, maps each live in-scope task to its managed window/session, obtains a fresh bounded worker_capture.py --window <alias> --tail <n> capture for each live worker, timestamps it, and distinguishes a real running command/test/build/review from a prompt, stale output, spinner, plan, status, or lease. It inspects task state, current capture, delivery/receipt, artifact/review/validation evidence together; windowless/parked/remote/scheduled tasks remain in inventory and are reconciled from state, not called complete. Missing/stale capture is unknown and must not trigger recovery. Do not use raw tmux input/capture or make mutations. State what data the dispatch child gathers itself versus what a caller supplies. This is required for acceptance and final live proof.

For this template, the caller supplies only the resolved template body, normal sender/recipient/subject, selected allowed model, and schedule. The child gathers current evidence itself through fixed read-only supervisor interfaces: durable task state and worker inventory; managed window/session mapping; fresh bounded `worker_capture.py --window <alias> --tail <n>` evidence and timestamp; managed delivery/receipt records; and artifact, validation, and review evidence. It does not accept an opaque snapshot as proof, use raw tmux capture/input, or mutate state, workers, router, schedules, or external systems. Unavailable or stale evidence is `unknown`.

## Responsibilities

| Component | Responsibility |
| --- | --- |
| Sentinel caller script | Resolve a named supervisor template or accept explicit prompt text; validate only the model against receiver policy; create immediate or scheduled dispatch via existing APIs; print durable IDs/status. |
| Router/protocol/schema | Validate only generic Sentinel transport metadata; enforce static profile/sender/model/capability policy; persist intent, occurrence, launch claim, and transport lifecycle; schedule only through the existing reminder loop; expose generic body-free status. It never launches Pi or interprets template content or child messages. |
| SQLite migrations/repository | Store generic Sentinel metadata and per-occurrence launch claim/child identity/exit state without a redundant prompt-body copy or child-message state. Preserve existing message/reminder transactions and replay behavior. |
| Recipient Pi extension | Preserve ordinary delivery; render body-free Sentinel parent state; claim/launch the isolated child under the fixed profile; track child process lifecycle; provide dispatch-scoped outbound V2 identity; prevent duplicate launch on replay. It does not inspect or label child messages. |
| Supervisor-owned templates | Define domain-specific child instructions, including any direct-supervisor update/final-report format and fixed read-only boundaries. |
| Child | Work only under its fixed profile; send any ordinary V2 messages directly to supervisor as its template directs. |

## Exact API additions

No new delivery endpoint is needed.

1. Extend the shared V2 envelope accepted by `POST /v1/messages` with an optional, closed generic `sentinel` descriptor containing only receiver-profile and allowed-model selection. It is absent for normal delivery.
2. Extend the existing reminder request accepted by `POST /v1/reminders` with the same optional closed descriptor, so a due reminder creates the same Sentinel occurrence rather than a separate delivery kind.
3. Extend existing message/reminder reads and router JSONL events with generic, body-free Sentinel transport status/occurrence fields. Add a read-only status lookup beneath the existing message/reminder resource hierarchy only if those representations cannot unambiguously expose an occurrence and process lifecycle; it must never accept delivery or launch commands.

Normal child-to-supervisor messages remain ordinary V2 envelopes. Sentinel adds no child-message API, schema, table, endpoint, validation, or deduplication.

The implementation plan will choose exact field names and any read path from current schema/repository conventions. It must not introduce a Sentinel-specific scheduler, delivery endpoint, queue, watchdog endpoint, or watchdog-specific router/extension field.

## Failure behavior

- Invalid generic Sentinel schema, sender/profile/recipient mismatch, model, or child capability: reject before enqueue; do not downgrade to ordinary delivery.
- Ordinary envelope/reminder: no Sentinel behavior change.
- Disconnected target, lease expiry, queue limit, dead letter, and recurrence: retain existing behavior and correlate a terminal transport failure where applicable.
- Parent persistence unavailable: retain/replay rather than acknowledge a parent-visible state that cannot be checkpointed.
- Child configuration or launch failure: record body-free transport failure; do not launch.
- Launch uncertainty: query durable claim plus child identity/exit evidence before retry; never duplicate a child from stale status, lease, or acknowledgement.
- Child crash, cancellation, or exit: record only observable process exit/success/failure facts. Sentinel does not require, generate, infer, or inspect a report.
- Duplicate replay/reconnect: observe or return the existing launch claim/lifecycle state; no duplicate child.
- Supervisor unavailable: ordinary child V2 messages remain durable and replay normally.
- Missing/stale managed watchdog capture: report unknown; do not infer healthy, stalled, complete, or recovery eligibility.

## Alternatives considered

### 1. Ordinary parent message asks the parent model to launch a child

This is simple but persists the original body in the parent session, depends on parent turn scheduling, and allows prompt text to influence the launcher. Reject.

### 2. A watchdog-specific dispatcher and scheduler

This duplicates durable delivery, recurrence, idempotency, and lifecycle logic while baking one prompt domain into generic infrastructure. It creates split-control-plane and duplicate-producer risk. Reject.

### 3. Router-spawned Pi children

This centralizes execution in the wrong component: the router would own Pi runtime, cwd/tool policy, and session lifecycle rather than remaining a durable transport. Reject.

### 4. Chosen: generic Sentinel metadata on existing messages/reminders, extension-owned child launch, supervisor-resolved templates

This preserves one queue and scheduler, keeps router and extension generic, leaves templates under supervisor ownership, prevents prompt text from becoming execution configuration, and reuses existing durable idempotency/replay machinery. The durable launch claim provides no-duplicate recovery without a side channel.

## Next-phase delivery and acceptance

The next phase must update router, Pi extension, shared schema/migrations, Sentinel caller script, supervisor template/instructions, docs/artifact names, and behavioral tests. Router, schema, migration, API, delivery, idempotency, replay, lifecycle, and extension tests must contain only generic Sentinel concepts; watchdog tests belong to the watchdog template/instruction behavior, not generic branches.

Tests must cover default-mailbox compatibility; named-template and explicit-prompt callers; immediate/one-shot/recurring Sentinel dispatch; model-policy rejection; parent body exclusion including persisted parent session; durable intent/replay; launch idempotency; child process exit observation; recurrence correlation; ordinary child V2 delivery; absence of Sentinel child-message inspection/parsing/schema/state; and the watchdog template's managed-evidence/no-raw-tmux/no-mutation instructions.

Only after updated router and extension are available may a **new managed Pi worker** perform isolated live proof against a dedicated test router/database/port. It must use a short one-time delay then a short recurrence interval, observe ordinary direct watchdog-template messages reaching supervisor, prove original prompt body is absent from the parent session, replay without a duplicate child, and observe multiple recurrence process lifecycles. It must inspect real lifecycle evidence and not touch the shared router or revive retired cron watchdog.

No commit, push, PR, install, shared-router restart, or other external mutation is authorized.
