# Managed workflows Phase 0: one-state completion slice

**Status:** Focused implementation specification; documentation only. This document does not authorize or contain production implementation.

**Authority:** `MANAGED_WORKFLOWS_REQUIREMENTS.md`. If this document is ambiguous, that requirements document wins. “Definition” is the canonical product term; a person may colloquially call a definition a workflow template.

## 1. Outcome

Phase 0 proves one complete, human-operable vertical slice:

1. A person or small script can create, list, inspect, revise, and hard-delete durable workflow definitions through closed `/v1` HTTP APIs.
2. A valid enabled definition can contain exactly one entry state with exactly one `complete` exit.
3. A person can launch its current immutable revision on one specifically selected, eligible router-managed Pi root agent.
4. The launch transaction creates the run, its initial state occurrence, journal events, and a durable entry-message outbox item. After commit, the router offers the message and wake to that root agent.
5. The installed `mecha.tools/v1` manifest exposes a root-only `mecha_workflow_transition` tool.
6. Calling `mecha_workflow_transition({entryId, exit:"complete"})` durably admits and processes the transition, completes the run, appends the journal, and creates no next-state notification.
7. The whole path is demonstrable with `curl` and `jq`; there is no UI.

The intentionally narrow product scenario is named `hello-world`. This is not a reduced-durability prototype: the runtime fence, managed-manifest identity, transactional boundaries, idempotency, outbox, and restart-recovery rules below are mandatory in Phase 0.

## 2. Scope

### 2.1 In scope

- Durable definition CRUD, immutable revision reads, validation on every save, optimistic concurrency, and hard-delete rules.
- The closed revision format in Section 5, including the future-compatible discriminated exit representation. A Phase 0 **runnable** revision is restricted to one state and one completion exit.
- Listing managed root agents that are eligible for this slice.
- Launching the current valid enabled revision with an empty input map on one explicitly named eligible agent.
- One active workflow run per managed agent, enforced in SQLite.
- Durable initial occurrence, entry notification, wake bookkeeping, journal, transition admission, completion, and recovery.
- Installation and dispatch of the root-only managed transition tool through the normal immutable manifest, generated contract, hidden managed RPC channel, control ledger, full runtime fence, and durable control-result path.
- HTTP observation of one known run and its ordered journal.
- Automated store, HTTP, manifest/RPC, recovery, and real managed-Pi slice tests.

### 2.2 Explicitly out of scope

- Web UI or other GUI work.
- Workflow reminders of any kind.
- Compaction or `compact_on_exit`; Phase 0 persists/returns `compactOnExit: false` only and rejects `true`.
- Runnable state-to-state or multi-state transitions, cycles, or self-loops. The `state` exit variant is specified now so stored JSON is unambiguous, but any Phase 0 revision containing it is saved as an invalid draft and cannot launch.
- Launch-input declarations, non-empty launch inputs, `${name}` interpolation, secrets, or typed inputs. Phase 0 requires `inputs: []` and launch `inputs: {}`.
- Cancellation, failure controls, arbitrary state setting, or user-driven transition HTTP endpoints.
- Definition validation-without-save, archive-specific routes, duplication, portable import/export, or migration of `~/.mecha-developer/workflows.json`.
- Active-run lists, retained-history lists, pagination, UI-live workflow projections, and cross-router portability. Reads by known run ID and the small Phase 0 journal are included only for walkthrough verification.
- Seven-day retention cleanup. Terminal Phase 0 runs remain durable until a later retention phase or eligible definition hard delete. Hard delete still must atomically remove retained terminal run-owned data as required.
- The eventual `managed-workflows` skill and changes to the deprecated file-backed `workflows` skill.
- Any unmanaged Pi process, native subagent, child context, legacy tmux/WebSocket agent, Android client, or remote/federated router.

## 3. Prerequisites and compatibility

- The official Go router and its single-connection SQLite store are authoritative. SQLite remains WAL, `synchronous=FULL`, foreign-key enabled, with ordered forward migrations and the existing serialized immediate-transaction path.
- Managed mode is enabled and the chosen agent is a live, ready router-managed Pi **root** with session persistence.
- The router remains on the repository’s existing local same-user trust boundary. The workflow routes add no cookies, credentials, CSRF mechanism, or second authentication model. They receive the existing non-credentialed wildcard CORS behavior; CORS is not authorization.
- Every response, including errors, sends `Cache-Control: no-store`; JSON responses use `Content-Type: application/json; charset=utf-8`. Existing `X-Correlation-ID` behavior remains.
- Every HTTP mutation requires a canonical lowercase UUID in `X-Request-ID`. Mutations with a body require `Content-Type: application/json`; hard DELETE has no body and follows the existing bodyless managed-control convention. JSON bodies are closed objects, duplicate keys/trailing values/lone surrogates are rejected, and the Phase 0 body limit is 64 KiB.
- The checked-in managed manifest must be advanced atomically from its current revision to a new revision containing the workflow tool. Generated tool contracts, manifest validators, expected tool/operation sets, prompt installation, and root handler registry must advance together. Existing roots on an older manifest are **not eligible** and must pass through the normal managed restart/readiness path to install the new immutable manifest; no tool is injected dynamically into a running manifest.

## 4. HTTP conventions

### 4.1 Versioned route inventory

These are the only new Phase 0 routes:

| Method | Path | Purpose | Success |
| --- | --- | --- | --- |
| `GET` | `/v1/workflow-definitions` | List definitions | `200` |
| `POST` | `/v1/workflow-definitions` | Create definition and revision 1 | `201` |
| `GET` | `/v1/workflow-definitions/{definitionId}` | Get definition plus current revision | `200` |
| `PUT` | `/v1/workflow-definitions/{definitionId}` | Replace editable definition and create next immutable revision | `200` |
| `DELETE` | `/v1/workflow-definitions/{definitionId}` | Hard delete definition when allowed | `200` |
| `GET` | `/v1/workflow-definitions/{definitionId}/revisions` | List retained revision summaries | `200` |
| `GET` | `/v1/workflow-definitions/{definitionId}/revisions/{revision}` | Get immutable revision | `200` |
| `GET` | `/v1/workflow-eligible-agents` | List eligible managed roots | `200` |
| `POST` | `/v1/workflow-runs` | Launch current revision | `201` |
| `GET` | `/v1/workflow-runs/{runId}` | Observe one run | `200` |
| `GET` | `/v1/workflow-runs/{runId}/journal` | Observe its complete Phase 0 journal | `200` |

No aliases, optional trailing slash, alternate verbs, or unlisted query parameters are accepted. Collection lists have no Phase 0 query parameters and sort deterministically: definitions by `(name, definitionId)`, revisions descending by revision, eligible agents by `agentId`, and journal events by ascending sequence. IDs are canonical lowercase UUIDs; `{revision}` is a canonical positive base-10 integer without leading zeroes.

### 4.2 Mutation idempotency

Use the existing managed-operation convention: idempotency is scoped by HTTP method plus `X-Request-ID`, with canonical path/body comparison. An exact replay returns the byte-equivalent stored status and JSON response without another revision, run, occurrence, journal event, or delete. Reuse with a changed path or canonical body returns `409 idempotency_conflict`. Persist the response in the same transaction as the mutation; a successful commit can never lose its replay result.

### 4.3 Error envelope and statuses

Workflow endpoints use the requirements’ stable lowercase codes while retaining the managed route envelope:

```json
{
  "error": {
    "code": "workflow_not_found",
    "message": "Workflow definition was not found.",
    "details": {
      "operation": "workflow_definition_read",
      "state": "rejected",
      "requestId": "b7ba25de-50ee-4b66-b780-4b31dc5e3725"
    }
  },
  "requestId": "b7ba25de-50ee-4b66-b780-4b31dc5e3725"
}
```

`requestId` is present only when the request supplied a valid canonical ID; safe router-generated correlation remains in the response header. Details are a closed object per operation and may contain only relevant IDs, field paths, and validation issues—never SQL, process data, definition text, entry text, inputs, or notes.

| Status | Codes / meaning |
| --- | --- |
| `400` | `invalid_request_id`, `invalid_json`, `unknown_field`, `invalid_identifier`, `invalid_request` |
| `404` | `workflow_not_found`, `revision_not_found`, `run_not_found` |
| `409` | `idempotency_conflict`, `definition_revision_conflict`, `definition_has_active_run`, `agent_has_active_run` |
| `413` | `payload_too_large` |
| `422` | `workflow_invalid`, `definition_name_conflict`, `invalid_launch_inputs`, `agent_not_eligible` |
| `503` | `managed_disabled`, `agent_not_ready`, `workflow_delivery_unavailable` (only for a pre-commit launch precondition; never rewinds committed work) |
| `500` | `internal_error`, with a bounded generic message |

A saved invalid draft is a successful `201`/`200`, not a `422`; its `validation.valid` is false. `workflow_invalid` is used when launch revalidation rejects that current revision. A validation rejection before save or launch has no partial durable effects.

## 5. Definition and revision contract

### 5.1 Bounds and common projections

Phase 0 technical limits are UTF-8 byte limits: definition/state names 256; descriptions 4,096; state entry text 32,768; complete-exit description 4,096. Values must be valid UTF-8, contain no NUL or lone surrogate, and required names must be nonempty after the same documented whitespace treatment used by the implementation. A definition name is globally unique.

A definition projection is:

```json
{
  "definitionId": "9a5aa269-a05c-46a5-afb4-26b22c853652",
  "name": "hello-world",
  "description": "One-state Phase 0 demonstration",
  "status": "enabled",
  "version": 1,
  "currentRevision": 1,
  "validation": { "valid": true, "issues": [] },
  "createdAt": "2026-03-23T12:00:00.000Z",
  "updatedAt": "2026-03-23T12:00:00.000Z"
}
```

`status` is exactly `enabled`, `disabled`, or `archived`. Only `enabled` launches. Status changes use the ordinary update operation and therefore create a revision; dedicated archive/restore routes are later work. `version` is the optimistic-concurrency version of the mutable definition row. `currentRevision` identifies its current immutable snapshot.

A validation issue is closed:

```json
{
  "path": "snapshot.states[0].exits[0].targetStateId",
  "code": "target_state_id_forbidden",
  "message": "A complete exit must not contain targetStateId."
}
```

Issue order is deterministic by path then code. Phase 0 implements all validation needed by its format: bounds and closed shapes; one entry; nonempty unique state IDs/names; reserved state name `complete`; at least one exit; discriminated exit rules; unique target per source; target existence; reachability/completion-path checks; and the Phase 0 runnable restriction. Unsupported-but-well-shaped graphs are retained with `phase0_unsupported_graph`, not silently normalized.

### 5.2 Revision snapshot and exit discriminator

The closed `mecha.workflow-definition/v1` snapshot shape is:

```json
{
  "format": "mecha.workflow-definition/v1",
  "inputs": [],
  "entryStateId": "3dd6daf9-ad5a-42d8-8541-ec2b125d16a0",
  "states": [
    {
      "stateId": "3dd6daf9-ad5a-42d8-8541-ec2b125d16a0",
      "name": "hello",
      "entryText": "Say hello, then complete this workflow.",
      "compactOnExit": false,
      "exits": [
        {
          "targetKind": "complete",
          "description": "Finish the hello-world run."
        }
      ]
    }
  ]
}
```

Exit is a minimal discriminated union:

- `{"targetKind":"state","targetStateId":"<state UUID>","description":"..."}` requires exactly `targetKind`, `targetStateId`, and optional `description`. `targetStateId` must reference a state in the same revision. Its eventual worker-visible exit name is derived from that target state’s name; there is no author-supplied exit-name field.
- `{"targetKind":"complete","description":"..."}` permits exactly `targetKind` and optional `description`; `targetStateId` is forbidden. Its visible exit name is the reserved literal `complete`.

Unknown fields are rejected. For Phase 0, a revision is runnable only when `inputs` is empty; there is exactly one state; it is `entryStateId`; its name is not `complete`; `compactOnExit` is false; and it has exactly one `complete` exit. A state-targeting exit therefore demonstrates schema validation only and makes the saved Phase 0 draft invalid.

An immutable revision response is:

```json
{
  "definitionId": "9a5aa269-a05c-46a5-afb4-26b22c853652",
  "revision": 1,
  "snapshot": { "format": "mecha.workflow-definition/v1", "inputs": [], "entryStateId": "...", "states": [] },
  "validation": { "valid": true, "issues": [] },
  "createdAt": "2026-03-23T12:00:00.000Z"
}
```

The list endpoint returns `{"definitions":[<definition projection>...]}`. Definition GET/create/update returns `{"definition":<projection>,"revision":<full current revision>}`. Revision-list returns `{"revisions":[{"definitionId":"...","revision":1,"validation":{"valid":true,"issues":[]},"createdAt":"..."}]}`; revision GET returns `{"revision":<full revision>}`.

### 5.3 Create, update, and delete requests

Create body:

```json
{
  "name": "hello-world",
  "description": "One-state Phase 0 demonstration",
  "status": "enabled",
  "snapshot": { "format": "mecha.workflow-definition/v1", "inputs": [], "entryStateId": "...", "states": [] }
}
```

`description` defaults to `""`; `status` defaults to `"disabled"`; all other fields are required.

Update is a full replacement, not an in-place graph edit:

```json
{
  "expectedVersion": 1,
  "expectedRevision": 1,
  "name": "hello-world",
  "description": "Updated human-readable description",
  "status": "enabled",
  "snapshot": { "format": "mecha.workflow-definition/v1", "inputs": [], "entryStateId": "...", "states": [] }
}
```

Both expectations are required. In one immediate transaction, the router compares them, validates the submitted replacement, inserts immutable revision 2 (valid or invalid), advances `currentRevision`, increments `version`, updates mutable metadata, prunes only revisions allowed by the authoritative retention rule, and stores the idempotent response. A stale value returns `409 definition_revision_conflict`. Revision 1 never changes, and runs already pinned to revision 1 are unaffected by this update or a later disable/archive.

DELETE has no body and still requires `X-Request-ID`. It returns:

```json
{"deleted":{"definitionId":"9a5aa269-a05c-46a5-afb4-26b22c853652"}}
```

An active run causes `409 definition_has_active_run`. Otherwise one immediate transaction deletes retained terminal run-owned workflow rows, removes workflow references from shared message/delivery rows without deleting unrelated messages, deletes revisions and the definition, and stores the idempotent result. Hard delete does not require prior archival.

## 6. Agent eligibility and launch

### 6.1 Eligible-agent response

`GET /v1/workflow-eligible-agents` returns only roots that are currently eligible, not merely all managed-agent projections:

```json
{
  "agents": [
    {
      "agentId": "reviewer",
      "name": "reviewer",
      "ready": true,
      "acceptingPrompts": true,
      "hasActiveWorkflow": false,
      "manifest": { "revision": 3, "sha256": "<64 lowercase hex>" }
    }
  ]
}
```

Eligibility is recomputed from durable/current managed state: managed mode enabled; root (not child); running and authoritatively ready; accepting prompts; current process identity and full fence available; installed manifest is exactly the router’s current workflow-capable manifest; no nonterminal workflow run; and no lifecycle/reset/restart/termination boundary that forbids new work. This endpoint exposes no process ID, session path, or credential.

### 6.2 Launch request and response

`POST /v1/workflow-runs` body:

```json
{
  "definitionId": "9a5aa269-a05c-46a5-afb4-26b22c853652",
  "expectedRevision": 1,
  "agentId": "reviewer",
  "inputs": {}
}
```

All fields are required; `inputs` must be the exact empty object in Phase 0. The expected revision prevents a person from unknowingly launching a revision changed after inspection. A mismatch returns `409 definition_revision_conflict`.

Before any run side effect, recheck in the transaction that the definition exists, is enabled, its current/expected revision is valid and Phase 0-runnable, the managed binding remains eligible under its full fence, and the database partial unique invariant allows at most one nonterminal run for the agent.

Successful response:

```json
{
  "run": {
    "runId": "7e62f427-7d40-4b8d-91b3-0c906c8881d7",
    "definitionId": "9a5aa269-a05c-46a5-afb4-26b22c853652",
    "definitionName": "hello-world",
    "revision": 1,
    "agentId": "reviewer",
    "status": "active",
    "phase": "active",
    "version": 1,
    "currentState": { "stateId": "3dd6daf9-ad5a-42d8-8541-ec2b125d16a0", "name": "hello" },
    "currentEntryId": "e7ef975a-951b-4492-ae6e-71103005ce47",
    "availableExits": ["complete"],
    "notification": { "state": "queued", "wakeState": "pending" },
    "startedAt": "2026-03-23T12:01:00.000Z",
    "completedAt": null,
    "finalExit": null
  }
}
```

In one immediate transaction, launch creates exactly one pinned run, stores the empty validated input map and launch-time full managed fence, creates exactly one initial entry occurrence, appends `run_launched` then `state_entered`, creates one deterministic durable entry-notification/outbox identity, and stores the HTTP replay response. Commit is the state-entry success boundary. Only after commit may delivery be offered and the root woken. Wake acceptance updates durable delivery state but is not required for the `201`; a disconnect/rejection leaves truthful retryable state. The launch fence remains audit evidence, not permanent authority: after normal managed cold recovery establishes a later authoritative ready binding for the same agent, the router durably records that recovery/rebinding and accepts subsequent controls only under that new full fence.

`GET /v1/workflow-runs/{runId}` returns `{"run":<run projection>}`. A completed projection has `status:"completed"`, `phase:"terminal"`, `currentState:null`, `currentEntryId:null`, `availableExits:[]`, `completedAt` set, and `finalExit:"complete"`.

## 7. Mandatory entry message

The durable visible message is router-generated from pinned revision data and must be self-contained. Its fixed structure is:

```text
Managed workflow state entry

Workflow: hello-world (definition 9a5aa269-a05c-46a5-afb4-26b22c853652, revision 1)
Run: 7e62f427-7d40-4b8d-91b3-0c906c8881d7
State: hello (3dd6daf9-ad5a-42d8-8541-ec2b125d16a0)
Entry ID: e7ef975a-951b-4492-ae6e-71103005ce47

Instructions:
Say hello, then complete this workflow.

Available exits:
- complete — Finish the hello-world run.

To leave this state, use mecha_workflow_transition with this exact entry ID and one listed exit. For this state call:
mecha_workflow_transition({"entryId":"e7ef975a-951b-4492-ae6e-71103005ce47","exit":"complete"})
```

The router, not authored `entryText`, emits the identity, exact entry ID, available-exits section, and tool instruction. An empty `entryText` prints `Instructions:\n(none)`; it never suppresses the message or wake. The outbox links a stable workflow notification identity to the existing durable managed direct-message machinery. Delivery/wake retries reuse that identity and the same occurrence; they never create a new message, occurrence, or `state_entered` event. Success means the runtime accepted the wake, not that a model turn began or settled.

## 8. Managed transition tool

The new immutable manifest entry is conceptually:

```json
{
  "capability_id": "mecha_workflow_transition",
  "name": "mecha_workflow_transition",
  "operation_id": "workflow.transition",
  "parameters": {
    "type": "object",
    "additionalProperties": false,
    "required": ["entryId", "exit"],
    "properties": {
      "entryId": { "type": "string", "minLength": 36, "maxLength": 36 },
      "exit": { "type": "string", "minLength": 1, "maxLength": 256 },
      "note": { "type": "string", "maxLength": 4096 }
    }
  }
}
```

The generated `definition_id` is `def_` plus the canonical definition SHA-256, as required by the existing pipeline. Prompt guidance says: use only the exact entry ID and a listed destination name or `complete` from the latest workflow entry message; a concise note is optional; never invent a target. Phase 0’s only accepted exit is `complete`.

The tool is installed on every newly started/recovered router-managed root under the current manifest, but root context is enforced at admission. It is unavailable to native subagents and child contexts. The extension validates raw arguments against the closed manifest schema and sends the normal hidden managed control request; it does not write workflow tables. The router then verifies tool/capability/operation/definition identities, manifest revision/hash, root context, agent/Pi session/session epoch/process identity/runtime generation, current authoritative binding, active run, exact current occurrence, and available exit before effects.

Stable tool errors include `no_active_workflow`, `stale_entry`, `exit_not_available`, `transition_already_admitted`, `invalid_arguments`, `stale_runtime`, and `root_context_required`. The caller cannot provide a run, definition, agent, state ID, target ID, completion boolean, or arbitrary status.

A normal Pi call first returns the existing “router control request queued” behavior. The eventual durable control result is:

```json
{
  "status": "ok",
  "content": "Workflow completed.",
  "details": {
    "outcome": "completed",
    "runId": "7e62f427-7d40-4b8d-91b3-0c906c8881d7",
    "entryId": "e7ef975a-951b-4492-ae6e-71103005ce47",
    "exit": "complete",
    "runVersion": 2
  }
}
```

“Queued” means the existing managed control ledger durably accepted the control request. The final `ok` means the completion transaction committed. Neither means merely received in process memory.

## 9. Transition, journaling, and no-next-notification rule

1. The managed control ledger durably reserves the request under its immutable identity and full runtime fence. Exact control replay returns the recorded outcome; changed arguments conflict.
2. In one immediate admission transaction, verify the authoritative binding and current active occurrence, reserve the sole pending transition keyed by `(runId, entryId)`, store the selected `complete` exit and optional bounded note, and append one `transition_requested` event. Two races have one winner; the loser produces no note/event.
3. A separate recoverable processing step uses one immediate transaction to recheck the reservation/fence, close the occurrence, mark the run `completed`, clear current state/entry, increment run version, set `completedAt` and `finalExit`, mark the pending transition terminal, and append `transition_completed` then `run_completed`.
4. That completion transaction creates **no** state occurrence, entry notification, message, wake, reminder, or compaction work.
5. The final control result is a separate durable delivery obligation. Failure to append it cannot roll back completion; recovery retries the same result identity.

Because `compactOnExit` is fixed false, the journal records no fabricated compaction event or successful compaction claim. There is no next-state notification under any completion retry or recovery path.

`GET /v1/workflow-runs/{runId}/journal` returns:

```json
{
  "runId": "7e62f427-7d40-4b8d-91b3-0c906c8881d7",
  "events": [
    {"sequence":1,"type":"run_launched","at":"...","stateId":null,"entryId":null,"exit":null,"note":null},
    {"sequence":2,"type":"state_entered","at":"...","stateId":"3dd6daf9-ad5a-42d8-8541-ec2b125d16a0","entryId":"e7ef975a-951b-4492-ae6e-71103005ce47","exit":null,"note":null},
    {"sequence":3,"type":"transition_requested","at":"...","stateId":"3dd6daf9-ad5a-42d8-8541-ec2b125d16a0","entryId":"e7ef975a-951b-4492-ae6e-71103005ce47","exit":"complete","note":null},
    {"sequence":4,"type":"transition_completed","at":"...","stateId":"3dd6daf9-ad5a-42d8-8541-ec2b125d16a0","entryId":"e7ef975a-951b-4492-ae6e-71103005ce47","exit":"complete","note":null},
    {"sequence":5,"type":"run_completed","at":"...","stateId":null,"entryId":null,"exit":"complete","note":null}
  ]
}
```

The journal response is closed and unpaginated only because Phase 0 has a fixed small maximum event count. Notes are persisted non-secret user data but do not appear in logs or generic errors.

## 10. Persistence, recovery, and security constraints

### 10.1 Durable minimum

Ordered migrations must represent definitions, immutable revisions and validation results, runs and launch fences, state occurrences, entry-notification outbox/delivery state, pending transitions, append-only journal events, managed control/result links, and HTTP idempotency responses. Required uniqueness includes definition name, `(definitionId, revision)`, one nonterminal run per agent, one current occurrence per run, one transition per source occurrence, journal `(runId, sequence)` and semantic event keys, and one notification identity per occurrence.

Keep the latest three definition revisions, counting invalid drafts, plus every older revision referenced by a nonterminal or retained terminal run. Pruning is foreign-key safe and cannot invalidate a pinned run.

### 10.2 Startup and crash recovery

Workflow recovery joins the existing startup boundary after authoritative managed cold recovery/readiness and before ordinary workflow delivery. The router does not serve workflow endpoints until database invariants and bounded indexed recovery succeed. Recovery:

- reconstructs no authority from messages or journal text;
- leaves a nonterminal run durably blocked when no authoritative binding exists;
- retries only entry offers/wakes lacking durable wake acceptance, using the existing occurrence/notification identity;
- resumes an admitted unfinished completion exactly once;
- retries a committed control result independently;
- never creates a second run, occurrence, transition, journal semantic event, notification, wake identity, or completion;
- fails closed on impossible current-state, fence, manifest, or foreign-key invariants.

Every external action follows durable reservation → external action → fenced durable completion. Fault tests cover process death before/after each commit, message offer, wake acceptance, and control-result append.

### 10.3 Security and privacy

- Only the authoritative ready root binding assigned to the active run may transition it. Stale manifests, sessions, epochs, process identities, runtime generations, other agents, and child contexts fail without effects.
- Definition text, entry text, notes, and future inputs are untrusted persisted non-secret data. They are never executed as shell, SQL, HTML, template expressions, or RPC framing.
- Operational logs/events contain stable IDs, phases, counts, and error codes only. They exclude names, descriptions, entry text, notes, raw arguments, SQL, process details, and message bodies.
- Database/state permissions remain user-only; existing request-size, CORS, loopback/local deployment, output-sanitization, and safe-diagnostic policies remain.
- If the router is exposed beyond the existing local same-user boundary, authentication is a prerequisite for that separate expansion; Phase 0 does not imply remote security.

## 11. Copy/paste walkthrough

Prerequisites: router at `127.0.0.1:8765`, `curl`, `jq`, and at least one workflow-capable managed root already running/ready. This script deliberately generates and carries IDs so no response must be edited by hand.

```bash
set -euo pipefail
ROUTER=${ROUTER:-http://127.0.0.1:8765}

# Pick a specific eligible root. To choose manually, first run:
curl -fsS "$ROUTER/v1/workflow-eligible-agents" | jq .
AGENT_ID=${AGENT_ID:-$(curl -fsS "$ROUTER/v1/workflow-eligible-agents" | jq -er '.agents[0].agentId')}

STATE_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
CREATE_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')

CREATE_BODY=$(jq -n --arg state "$STATE_ID" '{
  name:"hello-world",
  description:"One-state Phase 0 demonstration",
  status:"enabled",
  snapshot:{
    format:"mecha.workflow-definition/v1",
    inputs:[],
    entryStateId:$state,
    states:[{
      stateId:$state,
      name:"hello",
      entryText:"Say hello, then complete this workflow.",
      compactOnExit:false,
      exits:[{targetKind:"complete",description:"Finish the hello-world run."}]
    }]
  }
}')

CREATE_RESPONSE=$(curl -fsS \
  -H 'Content-Type: application/json' \
  -H "X-Request-ID: $CREATE_ID" \
  --data "$CREATE_BODY" \
  "$ROUTER/v1/workflow-definitions")
printf '%s\n' "$CREATE_RESPONSE" | jq .
DEFINITION_ID=$(jq -r '.definition.definitionId' <<<"$CREATE_RESPONSE")
REVISION=$(jq -r '.definition.currentRevision' <<<"$CREATE_RESPONSE")
VERSION=$(jq -r '.definition.version' <<<"$CREATE_RESPONSE")

# List and inspect the mutable definition and immutable revision 1.
curl -fsS "$ROUTER/v1/workflow-definitions" | jq .
curl -fsS "$ROUTER/v1/workflow-definitions/$DEFINITION_ID" | jq .
curl -fsS "$ROUTER/v1/workflow-definitions/$DEFINITION_ID/revisions/$REVISION" | jq .

# Optional useful update: creates immutable revision 2; revision 1 remains readable.
UPDATE_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
UPDATE_BODY=$(jq -n \
  --argjson version "$VERSION" --argjson revision "$REVISION" --arg state "$STATE_ID" '{
  expectedVersion:$version,
  expectedRevision:$revision,
  name:"hello-world",
  description:"Updated description; graph unchanged",
  status:"enabled",
  snapshot:{
    format:"mecha.workflow-definition/v1",
    inputs:[],
    entryStateId:$state,
    states:[{
      stateId:$state,
      name:"hello",
      entryText:"Say hello, then complete this workflow.",
      compactOnExit:false,
      exits:[{targetKind:"complete",description:"Finish the hello-world run."}]
    }]
  }
}')
UPDATE_RESPONSE=$(curl -fsS -X PUT \
  -H 'Content-Type: application/json' \
  -H "X-Request-ID: $UPDATE_ID" \
  --data "$UPDATE_BODY" \
  "$ROUTER/v1/workflow-definitions/$DEFINITION_ID")
printf '%s\n' "$UPDATE_RESPONSE" | jq .
REVISION=$(jq -r '.definition.currentRevision' <<<"$UPDATE_RESPONSE")

# Launch exactly the revision just inspected on exactly the selected root.
LAUNCH_ID=$(uuidgen | tr '[:upper:]' '[:lower:]')
LAUNCH_BODY=$(jq -n --arg definition "$DEFINITION_ID" \
  --argjson revision "$REVISION" --arg agent "$AGENT_ID" \
  '{definitionId:$definition,expectedRevision:$revision,agentId:$agent,inputs:{}}')
LAUNCH_RESPONSE=$(curl -fsS \
  -H 'Content-Type: application/json' \
  -H "X-Request-ID: $LAUNCH_ID" \
  --data "$LAUNCH_BODY" \
  "$ROUTER/v1/workflow-runs")
printf '%s\n' "$LAUNCH_RESPONSE" | jq .
RUN_ID=$(jq -r '.run.runId' <<<"$LAUNCH_RESPONSE")
ENTRY_ID=$(jq -r '.run.currentEntryId' <<<"$LAUNCH_RESPONSE")
printf 'Agent %s should receive run %s, entry %s\n' "$AGENT_ID" "$RUN_ID" "$ENTRY_ID"

# Observe durable launch/message state.
curl -fsS "$ROUTER/v1/workflow-runs/$RUN_ID" | jq .
curl -fsS "$ROUTER/v1/workflow-runs/$RUN_ID/journal" | jq .
```

In the selected root agent, verify that the visible message names `hello-world`, the run, `hello`, the exact `$ENTRY_ID`, lists `complete`, and explicitly says to use `mecha_workflow_transition`. The root then calls:

```text
mecha_workflow_transition({entryId: "<the exact Entry ID from the message>", exit: "complete"})
```

Finally verify completion from the shell:

```bash
curl -fsS "$ROUTER/v1/workflow-runs/$RUN_ID" \
  | jq -e '.run.status == "completed" and .run.finalExit == "complete" and .run.currentEntryId == null'

curl -fsS "$ROUTER/v1/workflow-runs/$RUN_ID/journal" \
  | jq -e '[.events[].type] == ["run_launched","state_entered","transition_requested","transition_completed","run_completed"]'
```

The implementation test harness must additionally assert that no next-state notification/outbox row or wake exists; that fact is intentionally not inferred merely from the HTTP projection.

## 12. Tests

### 12.1 Definition/store and HTTP contract

- Migration/restart preserves definitions and every retained immutable revision under WAL/FULL/foreign keys.
- Closed JSON, method/path, UUID, UTF-8/NUL/surrogate, byte-limit, unknown-field, no-store, content type, and error-envelope tests.
- Create/list/get/update/revision-list/revision-get/delete happy paths with deterministic ordering.
- Update creates a new immutable revision even for metadata/status changes; stale expected version/revision conflicts; a run remains pinned to its old snapshot.
- Exact HTTP mutation replay returns the stored result; changed path/body conflicts; crash after mutation commit cannot repeat it.
- Valid one-state completion graph passes. Zero/multiple entry, reserved `complete` state name, duplicate names/targets, missing/forbidden `targetStateId`, unknown target, no exit/completion path, nonempty inputs, `compactOnExit:true`, and runnable multi-state/state-target graphs produce deterministic issues. Invalid drafts persist and cannot launch.
- Hard delete blocks with an active run and otherwise atomically removes terminal run-owned data plus revisions without deleting unrelated shared messages.

### 12.2 Launch and entry delivery

- Eligibility excludes child/unmanaged/not-ready/not-accepting/stale-manifest/lifecycle-busy/already-active agents.
- Launch rejects disabled/archived/invalid/stale-revision definitions and nonempty/unknown inputs without run-side effects.
- Two concurrent launches for one agent produce one run; exact replay produces the same run.
- Launch transaction creates exactly one run, pinned revision, occurrence, two ordered journal events, and notification identity before any offer.
- Rendered visible message contains workflow/run/state identity, exact entry ID, optional instructions, only `complete`, and explicit `mecha_workflow_transition` guidance.
- Disconnect/restart around offer and wake acceptance retries only unfinished delivery under the same identity. No wake can advertise uncommitted state.

### 12.3 Manifest, tool, transition, and recovery

- Manifest/generator/hash fixtures prove the new revision is closed and immutable and that all expected operation registries include exactly `workflow.transition` once.
- A real managed root receives the tool; native child and stale/other root calls fail before side effects.
- Raw-argument tests reject extra/missing/wrong-type/oversized fields and malformed UUID; business validation rejects no active run, stale entry, and unknown exit.
- Valid current call durably reserves one transition, records one optional note, completes once, creates the exact journal sequence, delivers a correlated final result, and creates zero next occurrences/notifications/wakes.
- Concurrent and replayed controls produce one winning transition and one set of semantic journal events; changed-argument reuse conflicts.
- Crash matrix covers before/after control-ledger reserve, transition admission, completion commit, and result append. Recovery neither loses completion nor duplicates run, occurrence, journal event, note, notification, wake identity, or result.
- Startup with unavailable agent preserves the active run as blocked; authoritative recovered readiness resumes only obligations that lack durable success.
- Logs and errors are scanned to prove definition names/text, entry text, notes, raw arguments, SQL, and process/session data do not leak.

### 12.4 Live acceptance exercise

Against an isolated Go router + SQLite database + real managed Pi root using the built manifest:

1. Run the walkthrough to create, list, inspect, revise, and launch `hello-world`.
2. Capture the exact durable entry message and prove runtime wake acceptance.
3. Have the root invoke the exact transition tool call from that message.
4. Prove HTTP status is completed and journal order is exact.
5. Query test/store evidence proving one run, one occurrence, one entry notification, one transition, no next-state notification, and correct full-fence/control-ledger identities.
6. Restart once before delivery and once after transition admission in separate runs and prove convergence without duplicates.

## 13. Acceptance criteria

Phase 0 is complete only when all of the following are true:

1. The documented route inventory and closed JSON contracts are represented in router contract fixtures and all responses are `no-store`.
2. A person can run the copy/paste flow without database access, manually inventing router IDs, or using a UI.
3. Saving an update creates an immutable revision, and launch pins the exact expected current revision.
4. Launch on a specifically selected eligible managed root commits one run, occurrence, journal prefix, and durable entry notification atomically before delivery.
5. The received message identifies workflow/run/state, includes the exact entry ID, lists `complete`, and explicitly instructs use of `mecha_workflow_transition`.
6. The tool is installed through the versioned immutable managed manifest and is accepted only from the assigned authoritative root under the complete runtime/manifest fence.
7. `mecha_workflow_transition({entryId, exit:"complete"})` is durably admitted, completes the run exactly once, journals the request/outcome/completion, returns a durable correlated result, and creates no next-state notification.
8. Exact HTTP/control replays are idempotent, changed reuses conflict, and races have one winner.
9. Router or agent loss at every durable/external boundary recovers truthfully without duplicate state or false delivery claims.
10. No Phase 0 code path implements UI, reminders, compaction, runnable multi-state transitions, retention cleanup, import/export, or the other exclusions in Section 2.2.

## 14. Recommended implementation order

1. Freeze this contract in route/error/JSON fixtures and add failing contract tests.
2. Add ordered SQLite migrations, invariants, typed records, and immediate-transaction store methods for definition CRUD/revisions/idempotency.
3. Add Phase 0 validation and definition/revision HTTP handlers.
4. Add run/occurrence/journal/outbox persistence, eligibility read, launch transaction, run reads, and launch recovery tests.
5. Advance the managed manifest/generator/extension contract and closed root handler registry with `workflow.transition`; make old-manifest roots ineligible.
6. Add entry rendering plus post-commit direct delivery/wake and its retry/recovery state.
7. Add fenced transition admission, completion processor, journal, durable result, and crash recovery.
8. Run focused unit/contract/race/fault tests, then the isolated live acceptance exercise and retain content-safe evidence.

## 15. Requirements cross-check and decisions

This slice preserves the authoritative ownership and durability model: router/SQLite authority, immutable pinned revisions, one active run per agent, exact occurrence fencing, durable notification before wake, full managed runtime/manifest fencing, root-only tool use, append-only journal, immediate transactions, managed request/control idempotency, and startup recovery. It intentionally defers—not weakens—reminders, compaction, state-to-state execution, UI, retention sweep, imports/exports, and broader initial-release surfaces.

Phase 0 design decisions made here are: the route prefix is `/v1/workflow-*`; synchronous durable create/launch returns `201`; update is full replacement with both expected definition version and revision; invalid drafts save successfully; state-target exits are representable but not Phase 0-runnable; launch inputs are exactly empty; and old-manifest roots are ineligible until normal managed restart/recovery installs the workflow-capable manifest.

No unresolved product decision blocks this phase. User confirmation is needed only if a different public route naming convention or a different choice than full-replacement `PUT` is preferred before those names are frozen in implementation and contract fixtures.
