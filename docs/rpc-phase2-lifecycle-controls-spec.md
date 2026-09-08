# Phase 2 lifecycle-control spine — implementation specification

**Status:** planning only. This authorizes no product implementation.

**Scope:** the later, locally unauthenticated lifecycle-control slice for router-managed Pi workers. It adds reset, terminate, Restart All, their durable recovery and retry behavior, and the root-control result ledger. It follows the accepted same-session replacement slice and direct managed-mail delivery.

## 1. Authority and hard boundaries

The authority order is:

1. `RPC_IMPLEMENTATION_PLAN.html#phase2-rebaseline`.
2. This document for the later lifecycle-control slice.
3. The control-spine and five-gate rules in `RPC_SPEC.html` where they do not conflict with the rebaseline.
4. Current source for integration seams.

This slice keeps the following rules:

- Managed HTTP is locally unauthenticated. New routes read no cookie, CSRF value, ticket, caller identity, `Origin`, or browser state. They add no CORS or preflight behavior. Existing router-wide legacy CORS behavior is unchanged and is not authority.
- Idempotency remains worker-wide: `(method, X-Request-ID)` under migration 022's fixed internal scope `router-managed-worker-v1`. The canonical path and canonical JSON body must match. A mismatch returns `409 IDEMPOTENCY_CONFLICT`.
- The only new direct routes are `POST /v1/managed-agents/{id}/reset`, `DELETE /v1/managed-agents/{id}`, and `POST /v1/managed-agents/restart-all`. Reset and Restart All accept exactly `{}`. Terminate accepts no body. Every result uses `Cache-Control: no-store`.
- Restart All considers only managed rows where `desired_state='running'`. It never reads, signals, alters, or calls a tmux adapter.
- P3/P4, browser/session authentication, provider work, UI work, generic desired-running recovery, tmux lifecycle, Phase 1 application vocabulary/payload changes, and direct-mail redesign stay out of scope. The private P2 exact-entry and explicit-ID append seams in section 7.3 are the only extension/Pi exception.

## 2. Entry gates

No 027 implementation starts until all four gates are recorded on a clean commit:

1. Same-session restart replacement and its `Router.Listen` recovery are accepted.
2. The five-gate `readinessGate(ctx, binding, fence)` is accepted.
3. Migration 026 direct managed-mail delivery is accepted on a clean commit. The current working-tree 026 changes are not this gate.
4. The installed Pi used by native tests has both private P2 seams described in section 7.3: typed exact `get_entry(entry_id)` and a validated explicit-ID hidden custom-message append. The current installed Pi 0.85.0 has `abort`, `get_state`, and `--session-id` support. It does not have either P2 seam.

The durable record is `docs/rpc-phase2-lifecycle-controls-gate-record.md`. Its required fields are part of this gate: clean source/tree identities, source and evidence hashes, focused proof, acceptance decision, and for Pi the tested source commit, package version, installed executable path/hash, and exact bounded response shapes. The S4 native proof independently validates the accepted G4 source lock and installed executable before starting Pi. An artifact that only cites G4 is not runtime proof. A pending row is not acceptance.

The 026 acceptance record must name the commit, migration checksum, and these stable post-026 boundaries:

- `Store.RetireManagedRecipientSourceForTermination(ctx, agentID, recipientName)` in `router-proxy/internal/store/delivery.go`.
- Its idempotent invalid-target result for snapshots that still select the retired managed source.
- Its transaction contract: it owns recipient-source retirement, direct-attempt settlement/removal, and direct-delivery diagnostic cleanup. The lifecycle owner does not read, delete, retarget, or infer ordinary mail rows.

`RetireManagedRecipientSourceForTermination` has one direct-mail-owned transaction. For an active managed primary owned by the terminating agent, it changes the row to `state='retired'` and replaces `owner_identity` with the deterministic non-agent sentinel `retired:<sha256(agent_id)>`. For a conflicted managed primary, it promotes the recorded tmux secondary and clears the conflict. For a conflicted tmux primary with the terminating agent as managed secondary, it clears the conflict and keeps the tmux primary. A missing row or that agent's deterministic retired row is idempotent success; any other ownership is `DIRECT_MAIL_SOURCE_MISMATCH`.

Before the hook returns success, it makes every snapshot whose immutable `(source_kind, owner_identity)` is `(managed, agent_id)` resolve as `invalid_target`, deletes only that agent's direct-delivery attempts and diagnostics, and makes stale direct acknowledgements no-ops. It leaves the ordinary message and immutable snapshot rows intact. A later recipient claim may reuse the retired source row, but an old snapshot can never retarget because its owner identity no longer equals the active source. The lifecycle action calls only this hook and consumes its bounded result.

If any gate is missing, this slice remains documentation only.

## 3. Public admission, retry, and immutable outcomes

### 3.1 Admission responses

The original `202` response is the immutable body in `managed_http_operations`. It never changes when the lifecycle work reaches a terminal outcome. Exact replay returns that body and performs no process, signal, RPC abort, artifact deletion, batch admission, or projection work.

A lifecycle action has a separate bounded outcome:

```json
{
  "operation_id": "canonical UUID",
  "kind": "restart|reset|terminate|restart_all",
  "state": "succeeded|failed|cancelled|superseded",
  "code": "UPPER_SNAKE_CASE",
  "message": "safe fixed text",
  "retryable": false,
  "completed_at": "RFC3339"
}
```

The outcome is internal. The public status surface remains `GET /v1/managed-agents/{id}`. Successful termination deletes the managed row only after all cleanup commits. It then returns the ordinary `404 AGENT_NOT_FOUND`.

### 3.2 Retry admission

Retry uses a new request ID. It has no new route and no body field. The router resolves exact replay and idempotency conflict before selecting a retry predecessor.

| Request | Retry predecessor | New action | Rejection |
| --- | --- | --- | --- |
| `POST .../{id}/reset` | The only newest retained `reset` action for the same agent with `state='failed'`, `retryable=1`, no existing retry successor, and a retained target fence/artifact. | A new `reset` action with `retry_of=<predecessor action_id>`. It uses the retained target session/artifact and source fence. It does not increment `session_epoch` or create another target session. | `409 CONFLICT` if the failed row is not uniquely retryable, a higher action owns the lane, or its retained fence/artifact is unsafe. |
| `DELETE .../{id}` | The only newest retained `terminate` action for the same terminated tombstone with `state='failed'`, `retryable=1`, no existing retry successor, and a safe remaining cleanup stage. | A new `terminate` action with `retry_of=<predecessor action_id>`. It probes or reaps only the recorded owned group and resumes owned cleanup. It never launches an agent. | `409 CONFLICT` if the tombstone has no safe retry predecessor. A mismatch or unknown process identity is never retryable into a signal. |
| `POST .../restart-all` | None. | A new current snapshot and a new batch. | It never retries an old batch. An exact replay still returns the old immutable list. |

`retry_of` is one predecessor only. `UNIQUE(retry_of)` prevents a retry fork. A failed retry can have its own successor. The predecessor must have the same kind and agent, except that Restart All has no retry lineage.

Retryable reset failures use these outcomes:

| Failure | Code and message | Resume stage |
| --- | --- | --- |
| Target process failed before readiness after verified cleanup | `RESET_TARGET_START_FAILED` / `reset replacement did not become ready` | `target_start` |
| Target reached readiness but old artifact cleanup failed | `RESET_OLD_ARTIFACT_CLEANUP_FAILED` / `reset session cleanup did not complete` | `old_artifact_cleanup` |
| Target identity was rejected and its group was verified absent | `RESET_TARGET_IDENTITY_UNSAFE` / `reset replacement identity could not be verified` | `target_start` |

All other reset failures are non-retryable until a separate fenced operator action can establish safe ownership.

### 3.3 Cancellation and supersession

`cancelled` and `superseded` have one fixed distinction:

- **Cancelled:** a lower lifecycle action had no external side effect. Its state, linked operation, and any root controls are terminalized in the same immediate transaction.
- **Superseded:** a lower lifecycle action may already have stopped a process, created a target, or entered cleanup. The higher action adopts only the persisted owned cleanup work. The lower action and linked operation terminalize in the same immediate transaction before the higher action makes an external call.

A cancelled lifecycle action has `{code:"CANCELLED_BY_HIGHER_ACTION", message:"lifecycle action cancelled before external work", retryable:false}`. A superseded lifecycle action has `{code:"SUPERSEDED_BY_HIGHER_ACTION", message:"lifecycle action superseded by a higher priority action", retryable:false}`. Both linked HTTP operations become terminal with the normal 24-hour expiry.

Root controls always use `cancelled`, never `superseded`: reset writes `CANCELLED_BY_RESET`; terminate writes `CANCELLED_BY_TERMINATE`.

| Higher action | Lower action | Action result | Lower operation | Projection effect |
| --- | --- | --- | --- | --- |
| terminate | reset in `admitted` before an external call | cancelled | terminal, 24-hour expiry | one termination reservation bump |
| terminate | reset after external work, or any restart transition/action | superseded; terminate adopts retained owned cleanup | terminal, 24-hour expiry | one termination reservation bump |
| reset | manual or Restart All child restart before external work | cancelled | terminal, 24-hour expiry | one reset reservation bump |
| reset | restart after external work | superseded; reset first completes only safe retained old-group cleanup | terminal, 24-hour expiry | one reset reservation bump |
| Restart All child | an equal-priority pending manual restart | no replacement action; batch item is `coalesced` | unchanged | none |
| lower request during a higher action | any | `409 CONFLICT`, no operation or action row | none | none |

The reserved priority order is `terminate > reset > auth-change restart > manual restart / Restart All > crash recovery`. Future auth-change and crash actions use the same cancelled-before-side-effect or superseded-after-side-effect rule. They add no route or recovery scope in this slice.

Each terminalization uses one immediate transaction with the lane's current action ID and complete fence predicate. A stale completion has no durable change and no projection bump.

## 4. Restart All reservation and fan-out

Restart All has one bounded parent transaction. This closes concurrent replay without storing a placeholder response. Its parent is a direct `restart_all` action and has no source agent or process, so its source fence is intentionally all-null. Each accepted child is a `restart` action with `source='restart_all_child'`, its parent action ID, and a full source fence.

1. Start `BEGIN IMMEDIATE`.
2. Resolve `(POST, X-Request-ID)` under `router-managed-worker-v1`. An exact matching row returns its immutable response. A path/body mismatch returns `IDEMPOTENCY_CONFLICT`. Replay returns before a snapshot read or child-ID allocation.
3. Read eligible managed IDs once, ordered by `agent_id ASC`. The ordered list is the snapshot. Its zero-based list position is `snapshot_position`.
4. Compute one item for every snapshot ID from the durable rows while the writer lock is held. No row is written yet. An item is `accepted`, `coalesced` with the existing equal-priority direct operation, or `rejected` with a fixed safe code and message. For each accepted item only, the store calls the manager-provided bounded ID allocator for a canonical child action/operation/request ID and planned target generation. The allocator has no durable or process side effect.
5. The store marshals the parent body from the classified ordered items. It inserts the complete parent operation as `pending`, then its `restart_all` action as `admitted/admission`. Each accepted child operation has `operation_id == request_id == action_id`, method `POST`, canonical body hash for `{}`, and the non-routable canonical path `/internal/managed-lifecycle/restart-all/<parent-action-id>/children/<agent-id>`. It stores a bounded canonical restarting, admission-closed projection as that child operation's private `202` response. It then inserts the matching child action and every batch item. This all stays inside the same transaction.
6. After every accepted-agent fenced update and batch-item insert succeeds, terminalize the parent operation as `BATCH_ADMITTED` with 24-hour expiry, then terminalize the matching parent action as `succeeded/complete`. A terminal parent action is never inserted or committed with a pending operation. Commit.
7. After commit, enqueue accepted child execution through each agent's existing lane. The parent transaction never waits for a lane, process, settlement, or readiness check. A dispatch failure is recorded internally but still returns the committed stored `202`; it never rewrites the parent response or maps the first delivery to a public error.

A crash before commit leaves no parent, child operation, action, or item. A crash after commit leaves the full immutable batch and recoverable child records. A concurrent caller waits on SQLite's writer lock, then observes the immutable parent. This creates at most one child admission per `(parent_action_id, agent_id)` and one item per snapshot position. A direct restart can be coalesced by multiple parents under the cardinality rule in section 5.2.1.

The maximum snapshot size is the existing managed-agent cap of 32. The parent response describes admission only. Later child replacement success is visible through each agent projection.

## 5. Migration 027: exact durable contract

Migration `027_managed_control_requests.sql` is forward-only. It does not alter migrations 022–026. The lifecycle-control owner is the only owner of 027. It creates the independent tables in this order: `managed_session_artifacts`, `managed_lifecycle_actions`, `managed_lifecycle_batch_items`, and `managed_control_requests`. It creates every index except `managed_lifecycle_one_active_lane`, rebuilds the populated parent, backfills nonterminal 025 restart transitions, then creates that active-lane index. A root-only control slice consumes this combined migration; it never creates a competing root-only 027 or defers an original lifecycle/control field to one. The later narrow Restart All batch-reference correction in section 5.2.1 is a forward migration owned by the Restart All slice. It does not alter 027.

### 5.0 Populated SQLite rebuild procedure

`managed_agents` has populated foreign-key children after 026. They are `managed_readiness_sources` from 024, `managed_restart_transitions` from 025, and `managed_direct_delivery_attempts` plus `managed_direct_delivery_diagnostics` from 026. A normal parent `DROP TABLE` fails with foreign keys enabled. `PRAGMA defer_foreign_keys` does not fix that drop. `PRAGMA foreign_keys` cannot change inside `BEGIN IMMEDIATE`.

The store has one SQLite connection (`SetMaxOpenConns(1)`) and holds its store mutex during migrations. Migration 027 gets a dedicated runner on that same connection. It is the only migration allowed to suspend foreign-key enforcement. The runner must use this sequence:

1. Verify `PRAGMA foreign_keys=1` and `PRAGMA legacy_alter_table=0`. Verify that 024, 025, and 026 are recorded in `schema_migrations`, and that the four child tables above exist. Capture each child's `sqlite_master` table and index SQL, row count, and `PRAGMA foreign_key_list` result.
2. Outside any transaction, execute `PRAGMA foreign_keys=OFF`, then read it back as `0`. Do not use a separate connection.
3. Before `BEGIN IMMEDIATE`, reject a database containing a 025 `managed_agents` row with `lifecycle_state='starting'`. It is an unmatched pre-027 launch and has no durable operation link. The migration leaves that database untouched until its existing launch owner reaches a terminal state. Start `BEGIN IMMEDIATE`. Create the four new 027 tables in the stated order and every required index except `managed_lifecycle_one_active_lane`. Add nullable private `terminal_code` and `terminal_message` columns to `managed_http_operations`; 022–026 rows retain null values. Create `managed_agents_027_new` with the complete 025 `managed_agents` column list and constraints, widen `desired_state` to `('running','terminated')`, and add only nullable `launch_operation_id TEXT REFERENCES managed_http_operations(operation_id) ON DELETE RESTRICT`. Do not create, detach, copy, drop, or recreate any child table.
4. Copy the parent with an explicit ordered 025 column list plus null `launch_operation_id`. Save a temporary full-row snapshot before the copy. The copied row count must equal the snapshot count, two directional `EXCEPT` queries over every 025 column must be empty, and every copied launch-operation link must be null.
5. Drop `managed_agents`; rename `managed_agents_027_new` to `managed_agents`; recreate only `managed_agents_slot_idx` with its 025 definition. With `legacy_alter_table=OFF`, SQLite rewrites the existing child foreign-key metadata to the replacement parent name while leaving all child rows and DDL intact.
6. Backfill every nonterminal 025 restart transition into its one lifecycle `restart` action before creating `managed_lifecycle_one_active_lane`. The mapping, pending-operation preflight, full source/optional target fences, timestamps, null `retry_of`, and `execution_owner='legacy025'` are defined in section 5.1. Create the active-lane index only after every backfill row has been inserted.
7. Before committing, require zero rows from `PRAGMA foreign_key_check` and `PRAGMA integrity_check` equal to `ok`. Verify every nonnull `launch_operation_id` names an operation. Compare every captured child table/index SQL, row count, and foreign-key list with its pre-rebuild value. The child foreign key must still name `managed_agents(agent_id)`.
8. Insert the 027 migration record, then commit. Outside the transaction, execute `PRAGMA foreign_keys=ON` and read it back as `1`. Run `PRAGMA foreign_key_check` again before returning the store.

The runner must roll back the open transaction for any SQL error, parity failure, foreign-key failure, integrity failure, or migration-record failure. It must then restore and verify `PRAGMA foreign_keys=ON`. Rollback leaves the old parent, all 024/025/026 children, their rows, indexes, and migration ledger untouched, with no 027 record. If the post-commit re-enable or verification fails, the runner closes the database and returns an error; it must not return a store using a connection with foreign keys disabled.

The required migration tests use a real accepted-026 fixture with populated rows in `managed_agents`, all four child tables, `managed_http_operations`, and nonterminal 025 restart transitions covering every backfill state. They prove: successful 026-to-027 upgrade; parent row equality; child row/count/DDL/index/FK-list equality; both foreign-key checks and integrity check clean; widened `desired_state` accepts `terminated` and still rejects any other value; expected 027 tables and indexes exist; and reopen leaves foreign keys enabled. Each 025 transition must produce its one mapped `restart` action with `retry_of IS NULL` and the required operation, fence, target-plan, and timestamp values.

The same successful-upgrade test inserts valid initial `reset`, `terminate`, and lifecycle-owned direct `restart` actions, plus a direct `restart_all` parent with an all-null source fence and a `restart` child with `source='restart_all_child'`, its parent action ID, a full source fence, and `retry_of IS NULL`. It also inserts valid reset and terminate retries that reference eligible predecessors. It proves the schema rejects a non-null `retry_of` for `restart`, `restart_all`, and Restart All child actions. These checks cover row-local admission shape only; store tests separately prove predecessor failure state, retryability, same kind and agent, and single-successor eligibility. A separate populated 025 fixture with `lifecycle_state='starting'` must reject before any parent/table/index/ledger mutation. The normal fixture proves every copied legacy row has a null launch link and every backfilled transition has `execution_owner='legacy025'`.

Failure-injection tests fail after the parent copy, after the parent drop, and at the pre-commit foreign-key check. Each proves rollback preserves the 026 schema/data, records no 027 migration, and restores `foreign_keys=ON`. A separate test proves `PRAGMA foreign_keys=OFF` issued after `BEGIN IMMEDIATE` does not count as this procedure.

### 5.0.1 Value-validation ownership

The earlier statement that every timestamp and UUID check was implemented by migration 027 was too broad. Migration 027 preserves 022–026 values and does not add schema checks to their existing columns. Its SQL directly enforces only the `CHECK` constraints written in this section.

`router-proxy/internal/store/managed.go` owns canonical-value validation before every lifecycle/control write. It validates canonical lower-case UUIDs for newly allocated action, artifact, request, and result IDs; non-empty UTC RFC3339 timestamps for every new `*_at` value; and the existing SHA-256 and `text_1_128` bounds before insert or update. The 027 backfill is the exception: it copies existing 025 operation IDs as action IDs, so its preflight requires each backfilled action ID to be non-empty and unique, but does not claim a new historical UUID schema constraint. Every validation failure aborts its enclosing immediate transaction. Direct SQL test fixtures may use malformed legacy values only when they are testing migration preservation; they cannot establish a valid 027 store write.

SHA-256 fields are 64 lower-case hexadecimal characters. `text_1_128` below means non-empty UTF-8 text at most 128 bytes.

### 5.1 `managed_lifecycle_actions`

```sql
CREATE TABLE managed_lifecycle_actions (
  action_id TEXT PRIMARY KEY,
  operation_id TEXT NOT NULL UNIQUE
    REFERENCES managed_http_operations(operation_id) ON DELETE CASCADE,
  agent_id TEXT,
  kind TEXT NOT NULL CHECK (kind IN ('restart','reset','terminate','restart_all')),
  source TEXT NOT NULL CHECK (source IN ('direct','restart_all_child')),
  parent_action_id TEXT
    REFERENCES managed_lifecycle_actions(action_id) ON DELETE CASCADE,
  retry_of TEXT UNIQUE
    REFERENCES managed_lifecycle_actions(action_id) ON DELETE RESTRICT,
  execution_owner TEXT NOT NULL DEFAULT 'lifecycle'
    CHECK (execution_owner IN ('legacy025','lifecycle')),
  state TEXT NOT NULL CHECK (state IN (
    'admitted','awaiting_settle','stopping_source','starting_target',
    'awaiting_target_ready','old_artifact_cleanup','terminating','cleanup',
    'succeeded','failed','cancelled','superseded'
  )),
  stage TEXT NOT NULL CHECK (stage IN (
    'admission','source_settle','source_stop','target_start','target_ready',
    'old_artifact_cleanup','abort','terminate_reap','direct_mail_retire',
    'agent_cleanup','durable_write','complete'
  )),
  source_pi_session_id TEXT,
  source_session_epoch INTEGER,
  source_process_pid INTEGER,
  source_process_start_marker TEXT,
  source_process_incarnation TEXT,
  source_runtime_generation TEXT,
  source_manifest_revision INTEGER,
  source_manifest_hash TEXT,
  target_pi_session_id TEXT,
  target_session_epoch INTEGER,
  target_process_pid INTEGER,
  target_process_start_marker TEXT,
  target_process_incarnation TEXT,
  target_runtime_generation TEXT,
  target_manifest_revision INTEGER,
  target_manifest_hash TEXT,
  planned_target_runtime_generation TEXT,
  target_artifact_id TEXT
    REFERENCES managed_session_artifacts(artifact_id) ON DELETE SET NULL,
  terminal_code TEXT,
  terminal_message TEXT,
  retryable INTEGER,
  completed_at TEXT,
  recovery_owner TEXT,
  recovery_claimed_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,

  CHECK ((kind='restart_all') = (agent_id IS NULL)),
  CHECK ((source='restart_all_child') = (parent_action_id IS NOT NULL)),
  CHECK (kind <> 'restart_all' OR source='direct'),
  CHECK (source <> 'restart_all_child' OR kind='restart'),
  CHECK ((kind='restart_all') OR length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  CHECK ((kind IN ('restart','reset')) = (planned_target_runtime_generation IS NOT NULL)),
  CHECK (planned_target_runtime_generation IS NULL
    OR length(CAST(planned_target_runtime_generation AS BLOB)) BETWEEN 1 AND 128),
  CHECK (target_runtime_generation IS NULL
    OR target_runtime_generation = planned_target_runtime_generation),
  CHECK (
    (source_pi_session_id IS NULL AND source_session_epoch IS NULL
      AND source_process_pid IS NULL AND source_process_start_marker IS NULL
      AND source_process_incarnation IS NULL AND source_runtime_generation IS NULL
      AND source_manifest_revision IS NULL AND source_manifest_hash IS NULL)
    OR
    (length(CAST(source_pi_session_id AS BLOB)) > 0 AND source_session_epoch >= 1
      AND source_process_pid > 0 AND length(CAST(source_process_start_marker AS BLOB)) > 0
      AND length(CAST(source_process_incarnation AS BLOB)) > 0
      AND length(CAST(source_runtime_generation AS BLOB)) > 0
      AND source_manifest_revision >= 1 AND length(source_manifest_hash)=64
      AND source_manifest_hash=lower(source_manifest_hash)
      AND source_manifest_hash NOT GLOB '*[^0-9a-f]*')
  ),
  CHECK (
    (target_pi_session_id IS NULL AND target_session_epoch IS NULL
      AND target_process_pid IS NULL AND target_process_start_marker IS NULL
      AND target_process_incarnation IS NULL AND target_runtime_generation IS NULL
      AND target_manifest_revision IS NULL AND target_manifest_hash IS NULL)
    OR
    (length(CAST(target_pi_session_id AS BLOB)) > 0 AND target_session_epoch >= 1
      AND target_process_pid > 0 AND length(CAST(target_process_start_marker AS BLOB)) > 0
      AND length(CAST(target_process_incarnation AS BLOB)) > 0
      AND length(CAST(target_runtime_generation AS BLOB)) > 0
      AND target_manifest_revision >= 1 AND length(target_manifest_hash)=64
      AND target_manifest_hash=lower(target_manifest_hash)
      AND target_manifest_hash NOT GLOB '*[^0-9a-f]*')
  ),
  CHECK ((state IN ('succeeded','failed','cancelled','superseded')) = (completed_at IS NOT NULL)),
  CHECK ((state IN ('succeeded','failed','cancelled','superseded')) = (terminal_code IS NOT NULL)),
  CHECK ((state IN ('succeeded','failed','cancelled','superseded')) = (terminal_message IS NOT NULL)),
  CHECK ((state IN ('succeeded','failed','cancelled','superseded')) = (retryable IS NOT NULL)),
  CHECK (retryable IS NULL OR retryable IN (0,1)),
  CHECK (retry_of IS NULL OR kind IN ('reset','terminate')),
  CHECK (
    execution_owner <> 'legacy025'
    OR (kind='restart' AND source='direct' AND retry_of IS NULL)
  ),
  CHECK ((recovery_owner IS NULL) = (recovery_claimed_at IS NULL))
);
CREATE UNIQUE INDEX managed_lifecycle_one_active_lane
  ON managed_lifecycle_actions(agent_id)
  WHERE agent_id IS NOT NULL
    AND state IN ('admitted','awaiting_settle','stopping_source','starting_target',
                  'awaiting_target_ready','old_artifact_cleanup','terminating','cleanup');
CREATE INDEX managed_lifecycle_recovery_idx
  ON managed_lifecycle_actions(state, recovery_claimed_at, created_at);
CREATE INDEX managed_lifecycle_agent_terminal_idx
  ON managed_lifecycle_actions(agent_id, completed_at DESC, action_id DESC);
```

Every initial action and every 025 restart backfill has `retry_of IS NULL`. A non-null `retry_of` is valid only for a reset or terminate retry. The `UNIQUE(retry_of)` constraint prevents a retry fork; together with `CHECK (retry_of IS NULL OR kind IN ('reset','terminate'))`, it also keeps restart, Restart All, and Restart All child actions out of retry lineage. A Restart All parent is direct. A Restart All child is a `restart` action with its parent action ID.

The owner check constrains only `legacy025`: it may own only a direct, non-retry restart. `lifecycle` may own a direct restart admitted after 027, including the row-local migration proof. The migration and legacy store mutations separately require an exact joined 025 transition, matching action/operation ID, mapped stage, and full fence before they write `legacy025`; the table-local check cannot prove that cross-table bridge.

The store also requires a retry predecessor to be terminal `failed`, `retryable=1`, the same kind and agent, and without a prior successor. SQLite's row-local checks cannot express that predecessor lookup.

027 backfills every nonterminal `managed_restart_transitions` row into one `restart` action before it creates the active-lane index. `reserved` and `prepared` map to `admitted/admission`; `settling` maps to `awaiting_settle/source_settle`; `stopping` maps to `stopping_source/source_stop`; `stopped` maps to `starting_target/target_start`; and `starting` maps to `awaiting_target_ready/target_ready`. The action uses `operation_id` as `action_id`, `execution_owner='legacy025'`, its transition's complete old fence, its `target_runtime_generation` as `planned_target_runtime_generation`, any complete target process fence, and the transition timestamps. The planned target generation is not a process fence; the target process/session/epoch/runtime/manifest group remains all-null until identity verification. The migration fails if its operation is not pending or this one-to-one insert cannot be made.

The bridge is durable. Every legacy store mutation joins the transition to its action by operation/action ID, requires `execution_owner='legacy025'`, the mapped action state/stage, and the exact old or target fence. It updates the transition and action in the same immediate transaction. `RollbackManagedRestartPreparation` deletes the transition and terminalizes action and operation as failed. `CompleteManagedRestartReady` deletes the transition and terminalizes action and operation as succeeded. `RecordManagedRestartFailure`, restart shutdown, old-group recovery, target reset, and every nonterminal stage change keep the transition/action pair nonterminal, retain `execution_owner='legacy025'`, and update the action's mapped state/stage and timestamp. `RecoverManagedLifecycleControls` never claims legacy-owned rows. A higher reset or terminate atomically marks the legacy action and operation superseded before its external work; all later old-executor callbacks fail the owner/fence predicate and make no durable or process change. Direct restart admitted after 027 writes `execution_owner='lifecycle'` and creates no `managed_restart_transitions` row.

The lifecycle state determines the only valid stage pairs:

| State | Allowed stage |
| --- | --- |
| `admitted` | `admission` |
| `awaiting_settle` | `source_settle` |
| `stopping_source` | `source_stop` |
| `starting_target` | `target_start` |
| `awaiting_target_ready` | `target_ready` |
| `old_artifact_cleanup` | `old_artifact_cleanup` |
| `terminating` | `abort` or `terminate_reap` |
| `cleanup` | `direct_mail_retire`, `agent_cleanup`, or `durable_write` |
| terminal states | the stage that failed or `complete` |

The store validates this pair before every insert or update. A direct action requires a full source fence except a terminate action admitted with no binding and a Restart All parent. Both exceptions keep the source fence all-null: the terminate has no binding, and the direct Restart All parent has no source agent or process. Every Restart All child requires a full source fence. A reset requires a full source fence and its own `planned_target_runtime_generation`. A restart/reset target fence is full only after target process identity is verified; its runtime generation must equal the earlier plan.

### 5.2 `managed_lifecycle_batch_items`

```sql
CREATE TABLE managed_lifecycle_batch_items (
  parent_action_id TEXT NOT NULL
    REFERENCES managed_lifecycle_actions(action_id) ON DELETE CASCADE,
  snapshot_position INTEGER NOT NULL CHECK (snapshot_position BETWEEN 0 AND 31),
  agent_id TEXT NOT NULL CHECK (length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  status TEXT NOT NULL CHECK (status IN ('accepted','coalesced','rejected')),
  child_action_id TEXT UNIQUE
    REFERENCES managed_lifecycle_actions(action_id) ON DELETE SET NULL,
  child_operation_id TEXT UNIQUE
    REFERENCES managed_http_operations(operation_id) ON DELETE SET NULL,
  error_code TEXT,
  error_message TEXT,
  created_at TEXT NOT NULL,
  CHECK ((status IN ('accepted','coalesced')) = (child_action_id IS NOT NULL)),
  CHECK ((status IN ('accepted','coalesced')) = (child_operation_id IS NOT NULL)),
  CHECK ((status='rejected') = (error_code IS NOT NULL)),
  CHECK ((status='rejected') = (error_message IS NOT NULL)),
  UNIQUE(parent_action_id, snapshot_position),
  UNIQUE(parent_action_id, agent_id)
);
CREATE INDEX managed_lifecycle_batch_parent_idx
  ON managed_lifecycle_batch_items(parent_action_id, snapshot_position);
```

`child_action_id` and `child_operation_id` must name the same child action. The store checks this join in the same parent transaction.

### 5.2.1 S2 batch-reference cardinality amendment

Migration 027's two global child-reference uniqueness constraints prevent more than one Restart All parent from coalescing the same pending direct restart. S2 owns forward migration `028_managed_restart_all_batch_references.sql`. It rebuilds only `managed_lifecycle_batch_items` in one immediate transaction: create a replacement with the same columns, foreign keys, checks, and parent-scoped unique constraints, but no `UNIQUE(child_action_id)` or `UNIQUE(child_operation_id)`; copy every row with an explicit column list; replace the old table; recreate `managed_lifecycle_batch_parent_idx`; and add nonunique partial indexes on each non-null child reference.

The migration verifies row count and two-directional row equality, `PRAGMA foreign_key_check`, and `PRAGMA integrity_check` before it records itself. A failure rolls back to the unchanged 027 table, indexes, rows, and migration ledger. It never changes 027 or any earlier migration.

Only a pending equal-priority direct restart with a matching action/operation/fence may be shared this way. An accepted Restart All child remains owned by its one `parent_action_id`; a Restart All child, terminal restart, non-direct restart, or mismatched action/operation pair is a fixed rejected item. Retention and termination must traverse every batch item that references the shared coalesced action or operation.

### 5.3 `managed_session_artifacts`

```sql
CREATE TABLE managed_session_artifacts (
  artifact_id TEXT PRIMARY KEY,
  agent_id TEXT NOT NULL CHECK (length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  pi_session_id TEXT NOT NULL CHECK (length(CAST(pi_session_id AS BLOB)) BETWEEN 1 AND 128),
  root_relative_path TEXT NOT NULL
    CHECK (length(CAST(root_relative_path AS BLOB)) BETWEEN 1 AND 4096)
    CHECK (root_relative_path NOT LIKE '/%')
    CHECK (root_relative_path <> '..')
    CHECK (root_relative_path NOT LIKE '../%')
    CHECK (root_relative_path NOT LIKE '%/../%')
    CHECK (root_relative_path NOT LIKE '%/..'),
  path_sha256 TEXT NOT NULL CHECK (length(path_sha256)=64
    AND path_sha256=lower(path_sha256) AND path_sha256 NOT GLOB '*[^0-9a-f]*'),
  discovery_state TEXT NOT NULL
    CHECK (discovery_state IN ('materialized','not_materialized','ambiguous','missing')),
  file_device TEXT,
  file_inode TEXT,
  cleanup_state TEXT NOT NULL
    CHECK (cleanup_state IN ('retained','pending','unlinked')),
  discovered_at TEXT NOT NULL,
  cleanup_updated_at TEXT NOT NULL,
  CHECK ((discovery_state='materialized') =
    (file_device IS NOT NULL AND file_inode IS NOT NULL)),
  CHECK ((discovery_state<>'materialized') =
    (file_device IS NULL AND file_inode IS NULL)),
  CHECK (discovery_state<>'ambiguous' OR cleanup_state='retained'),
  CHECK (cleanup_state<>'unlinked' OR discovery_state<>'ambiguous'),
  UNIQUE(agent_id, session_epoch),
  UNIQUE(path_sha256)
);
CREATE INDEX managed_session_artifacts_cleanup_idx
  ON managed_session_artifacts(cleanup_state, agent_id, session_epoch);
```

The table contains a private root-relative identity only. It never accepts an HTTP path or an unvalidated persisted absolute path. Before every unlink, the artifact seam opens the configured session root, resolves the stored relative path beneath it, rejects symlinks and root escape, checks the regular-file type, and compares the captured device/inode where a file was materialized. `ambiguous` is durable evidence that no unlink is allowed. `not_materialized` is valid: current Pi may report a session file identity before it has persisted a physical file.

### 5.4 `managed_control_requests`

```sql
CREATE TABLE managed_control_requests (
  request_id TEXT PRIMARY KEY,
  result_id TEXT NOT NULL UNIQUE,
  agent_id TEXT NOT NULL CHECK (length(CAST(agent_id AS BLOB)) BETWEEN 1 AND 128),
  tool_call_id TEXT NOT NULL CHECK (length(CAST(tool_call_id AS BLOB)) BETWEEN 1 AND 128),
  capability_id TEXT NOT NULL CHECK (length(CAST(capability_id AS BLOB)) BETWEEN 1 AND 128),
  tool_name TEXT NOT NULL CHECK (length(CAST(tool_name AS BLOB)) BETWEEN 1 AND 128),
  definition_id TEXT NOT NULL CHECK (length(CAST(definition_id AS BLOB)) BETWEEN 1 AND 128),
  operation_id TEXT NOT NULL CHECK (length(CAST(operation_id AS BLOB)) BETWEEN 1 AND 128),
  context_kind TEXT NOT NULL CHECK (context_kind='root'),
  canonical_arguments_sha256 TEXT NOT NULL CHECK (length(canonical_arguments_sha256)=64
    AND canonical_arguments_sha256=lower(canonical_arguments_sha256) AND canonical_arguments_sha256 NOT GLOB '*[^0-9a-f]*'),
  request_fingerprint TEXT NOT NULL CHECK (length(request_fingerprint)=64
    AND request_fingerprint=lower(request_fingerprint) AND request_fingerprint NOT GLOB '*[^0-9a-f]*'),
  state TEXT NOT NULL CHECK (state IN (
    'received','executing','side_effect_committed','completed','append_pending',
    'awaiting_append','delivered','failed','cancelled'
  )),
  pi_session_id TEXT NOT NULL,
  session_epoch INTEGER NOT NULL CHECK (session_epoch >= 1),
  process_pid INTEGER NOT NULL CHECK (process_pid > 0),
  process_start_marker TEXT NOT NULL,
  process_incarnation TEXT NOT NULL,
  runtime_generation TEXT NOT NULL,
  manifest_revision INTEGER NOT NULL CHECK (manifest_revision >= 1),
  manifest_hash TEXT NOT NULL CHECK (length(manifest_hash)=64
    AND manifest_hash=lower(manifest_hash) AND manifest_hash NOT GLOB '*[^0-9a-f]*'),
  result_status TEXT,
  result_content TEXT,
  result_details_json TEXT,
  result_payload_sha256 TEXT,
  decoded_payload_bytes INTEGER CHECK (decoded_payload_bytes BETWEEN 1 AND 65536),
  result_entry_id TEXT UNIQUE,
  append_attempt INTEGER NOT NULL DEFAULT 0 CHECK (append_attempt BETWEEN 0 AND 5),
  next_append_at TEXT,
  received_at TEXT NOT NULL,
  handler_owner TEXT,
  handler_started_at TEXT,
  handler_deadline_at TEXT,
  side_effect_committed_at TEXT,
  handler_completed_at TEXT,
  append_owner TEXT,
  append_started_at TEXT,
  append_watermark TEXT,
  append_submitted_at TEXT,
  receipt_entry_identity TEXT,
  delivered_at TEXT,
  terminal_at TEXT,
  error_code TEXT,
  error_detail TEXT,
  retryable INTEGER,

  UNIQUE(agent_id, request_id),
  CHECK ((state IN ('delivered','failed','cancelled')) = (terminal_at IS NOT NULL)),
  CHECK ((state IN ('delivered','failed','cancelled')) = (retryable IS NOT NULL)),
  CHECK (retryable IS NULL OR retryable IN (0,1)),
  CHECK ((result_status IS NULL) = (result_content IS NULL)),
  CHECK (result_content IS NULL OR length(CAST(result_content AS BLOB)) <= 16384),
  CHECK (result_details_json IS NULL OR length(CAST(result_details_json AS BLOB)) <= 16384),
  CHECK ((result_content IS NULL AND result_details_json IS NULL)
    OR length(CAST(result_content AS BLOB)) + length(CAST(result_details_json AS BLOB)) <= 65536),
  CHECK (result_payload_sha256 IS NULL OR (length(result_payload_sha256)=64
    AND result_payload_sha256=lower(result_payload_sha256)
    AND result_payload_sha256 NOT GLOB '*[^0-9a-f]*')),
  CHECK ((result_payload_sha256 IS NULL) = (decoded_payload_bytes IS NULL)),
  CHECK (error_code IS NULL OR length(CAST(error_code AS BLOB)) BETWEEN 1 AND 128),
  CHECK (error_detail IS NULL OR length(CAST(error_detail AS BLOB)) <= 16384),
  CHECK (state NOT IN ('completed','append_pending','awaiting_append','delivered') OR result_entry_id IS NOT NULL),
  CHECK ((result_entry_id IS NULL) OR result_entry_id = 'mcr1_' || replace(result_id,'-','')),
  CHECK (append_attempt=0 OR result_entry_id IS NOT NULL),
  CHECK (state<>'append_pending' OR (append_attempt BETWEEN 1 AND 5
    AND append_owner IS NOT NULL AND append_started_at IS NOT NULL AND append_watermark IS NOT NULL)),
  CHECK (state<>'awaiting_append' OR (append_attempt BETWEEN 1 AND 5
    AND append_owner IS NOT NULL AND append_started_at IS NOT NULL
    AND append_watermark IS NOT NULL AND append_submitted_at IS NOT NULL)),
  CHECK (next_append_at IS NULL OR (state='completed' AND append_attempt BETWEEN 1 AND 4)),
  CHECK (state<>'delivered' OR (delivered_at IS NOT NULL AND receipt_entry_identity IS NOT NULL))
);
CREATE UNIQUE INDEX managed_control_request_dedup_idx
  ON managed_control_requests(agent_id, request_id);
CREATE INDEX managed_control_nonterminal_agent_idx
  ON managed_control_requests(agent_id, received_at)
  WHERE state NOT IN ('delivered','failed','cancelled');
CREATE INDEX managed_control_nonterminal_global_idx
  ON managed_control_requests(received_at)
  WHERE state NOT IN ('delivered','failed','cancelled');
CREATE INDEX managed_control_append_due_idx
  ON managed_control_requests(next_append_at, request_id)
  WHERE state='completed' AND next_append_at IS NOT NULL;
CREATE INDEX managed_control_terminal_retention_idx
  ON managed_control_requests(agent_id, terminal_at DESC, request_id DESC)
  WHERE state IN ('delivered','failed','cancelled');
```

A duplicate request ID with the same tool-call, tuple, canonical arguments hash, context kind, fingerprint, and full fence is the existing row. A changed identity field or fence is `CONFLICT`. Reservation happens in one immediate transaction that checks fewer than 64 nonterminal rows for that agent and fewer than 512 router-wide. The 65th per-agent or 513th router-wide reservation fails before a handler call with the fixed bounded `CONFLICT` result. `completed` commits the deterministic entry ID before any append allocation. A retryable explicit append rejection returns the row to `completed` with its durable due time; only a due row may allocate attempts 2–5. `append_watermark` and `receipt_entry_identity` are bounded router-owned receipt metadata, never a history cursor.

`result_content`, `result_details_json`, and `error_detail` pass the established secret redaction before storage. `error_detail` has no raw RPC/process text.

### 5.5 Foreign-key retention graph and deletion order

027 starts with this post-026 ownership inventory:

| Record | FK or owner after 027 | Successful termination disposition |
| --- | --- | --- |
| `managed_restart_transitions` | `managed_agents`, operation, and its legacy-owned action bridge | Terminalization deletes it before its operation. A live legacy row blocks operation expiry. |
| `managed_agents` | `launch_operation_id` references `managed_http_operations(operation_id)` with `ON DELETE RESTRICT` | A failed launch keeps this protection link while the failed agent, name, and slot remain. Termination cleanup clears it before deleting the agent. |
| `managed_readiness_sources` | `managed_agents` | Delete in termination cleanup. |
| `managed_lifecycle_actions` | operation; parent action; retry predecessor; no agent FK | Retain terminal actions with their operations. Delete explicit retry/child descendants first. |
| `managed_lifecycle_batch_items` | parent action plus nullable child action/operation references | Retain for a live parent. Delete explicitly before referenced child rows. |
| `managed_session_artifacts` | nullable lifecycle action target reference; no agent FK | Unlink first, then delete the row. |
| `managed_control_requests` | no agent FK | Cancel, then delete during successful termination cleanup. |
| `recipient_sources`, snapshots, direct attempts, diagnostics | migration 026 direct-mail owner | The hook retires/promotes the source, makes old snapshots invalid targets without retargeting them, deletes this agent's attempts/diagnostics, and leaves ordinary message/snapshot history intact. |
| `managed_http_operations` | external replay owner; `managed_agents.launch_operation_id` is `ON DELETE RESTRICT` | Never cascade from `managed_agents`. A terminal launch and termination operation both remain replayable until eligible for sweep. |

`expires_at` is a 24-hour eligibility floor. It never authorizes an FK-unsafe delete. `Store.Sweep` runs `SweepManagedOperationGraph` in its existing immediate transaction:

1. Select terminal operations with `expires_at <= now`, ordered by `(expires_at, operation_id)`. For each candidate, build a closure over its lifecycle action, retry successors, parent-action children, and every accepted/coalesced batch-item reference. Include the batch parent operation for every referenced item. Also inspect a same-operation 025 transition. Then inspect `managed_agents` for every `launch_operation_id` that names an operation in the closure. This is a reverse protection edge, not a request to add the agent to a deletable closure.
2. The closure is prunable only when every included operation is terminal and expired, every included lifecycle action is terminal, no included operation has a 025 transition, and no managed agent retains a matching `launch_operation_id`. An unexpired or nonterminal retry successor, child, coalesced target, or batch parent blocks the whole closure. A retained launch link blocks the whole closure. This preserves a batch item while its parent response still has required retained work and prevents a parent action's `ON DELETE CASCADE` from deleting a live child.
3. A terminal operation with a linked 025 transition is an invariant error. The sweep rolls back and leaves every row unchanged. Legacy success, legacy preparation rollback, and supersession terminalization must delete the transition in their own transaction before terminalizing that operation.
4. For a prunable closure, explicitly delete batch items first. Delete lifecycle actions in postorder: retry successors and parent-action children before their predecessor or parent. Delete their operations in the same postorder. The sweep does not rely on action/operation cascades. Artifact deletion is separate and occurs only after safe unlink and after no action references it.

Tests use populated graphs. They cover an expired child held by an unexpired parent, an expired parent held by an unexpired or nonterminal child, a retry successor before its predecessor, and both accepted and coalesced batch references. They prove a full eligible closure deletes items, actions, then operations in that order. A terminal operation still linked to a 025 transition fails the sweep. They also terminalize an expired launch through the all-null identity-unsafe recovery path while its failed `managed_agents` row retains `launch_operation_id`. The sweep must leave the operation, agent, and link intact with `foreign_key_check` clean. A termination-cleanup fixture clears the link and deletes the agent; only then may the now-unprotected expired launch operation sweep. Injecting failure after the first batch-item deletion proves the transaction restores every row and `foreign_key_check` remains clean.

Every post-027 launch inserts its operation first and records that exact pending ID in `managed_agents.launch_operation_id` in the same immediate reservation transaction. Its callback must prove the linked operation remains pending and the agent is not a tombstone before every external start and durable write. Termination reservation atomically sets the tombstone and terminalizes this pending launch with private `terminal_code='LAUNCH_CANCELLED_BY_TERMINATE'` and `terminal_message='managed launch cancelled by termination'`. The immutable launch `202` stays unchanged and exact-replayable for at least 24 hours. Before deleting the agent, cleanup clears the launch link; the `ON DELETE RESTRICT` link prevents an accidental operation delete while the agent still exists.

Successful termination runs this ordered sequence after external group work. It cancels controls and uses the direct-mail hook. After safe artifact unlink, its final immediate transaction deletes every batch item that references the terminating agent's action or operation before it deletes that child. The immutable parent response is not changed. It terminalizes/supersedes any legacy bridge and blocks later old-owner callbacks, deletes retry descendants before predecessors, then deletes the legacy transition, readiness source, prior lifecycle/control rows, histories, and policy state through their owners. It clears `launch_operation_id`, deletes `managed_agents`, and terminalizes the termination action/operation. The already terminal launch operation and terminal termination operation remain replayable until their own graph-safe sweep. The final transaction releases the name and slot once. Any failure leaves the tombstone and name/slot held.

## 6. Root-control state graph and retention

The root-control ledger has these only transitions:

```text
received -> executing -> side_effect_committed -> completed
completed -> append_pending -> awaiting_append -> delivered
awaiting_append -> completed -> append_pending
received|executing|side_effect_committed|completed|append_pending|awaiting_append -> failed|cancelled
```

The handler has a 30-second deadline. Model turns and deferred compaction are not handler work. The router commits `completed` with the deterministic `mcr1_<result UUID without hyphens>` entry ID, then reserves `append_pending`, complete fence, payload hash, append owner, watermark, and `append_attempt=1` before the private append command. Frozen root-control cancellation codes include `CANCELLED_BY_RESET` and `CANCELLED_BY_TERMINATE`.

A retryable rejection or an absent exact entry under a current matching live binding moves the request back to `completed` with one durable due time. When due, it allocates the next `append_pending` attempt:

| Next attempt | Due from failed observation |
| --- | --- |
| 2 | 1 second |
| 3 | 5 seconds |
| 4 | 30 seconds |
| 5 | 120 seconds |

Attempt 5 has no further due time. Its failed terminal outcome is `RESULT_DELIVERY_FAILED` with `retryable:false`. A reset fence mismatch writes `cancelled/CANCELLED_BY_RESET`. Termination writes `cancelled/CANCELLED_BY_TERMINATE` before removal. A same-session restart can retry only when the complete stored fence matches a current live binding.

Terminal control rows retain the newest 200 per agent and no more than 30 days. Write-time pruning and `Store.Sweep` use `(terminal_at DESC, request_id DESC)`. Nonterminal control rows and nonterminal lifecycle rows are never clock-pruned.

## 7. Pi and process seams

### 7.1 Abort

`router-proxy/internal/managed/rpc.go` gains the narrow `Abort(context.Context) error` method. Its only wire command is `{"type":"abort"}`. `managed.Manager` owns a five-second context derived from the existing graceful-stop limit and calls it before any terminate group action.

The installed Pi 0.85.0 source supports this command: `dist/modes/rpc/rpc-mode.js` handles `case "abort"` by awaiting `session.abort()` and responds with `command:"abort", success:true`; `dist/modes/rpc/rpc-types.d.ts` declares the command and response. A timeout, EOF, malformed response, or explicit RPC error records the internal abort-stage failure and then continues exact group shutdown. Abort failure alone never reports a successful terminate before cleanup.

### 7.2 Create versus resume

`CommandSpec` gains an explicit `SessionLaunchMode`:

- `ResumeExisting`: requires a retained, verified session artifact identity. It passes the recorded `--session-id` and proves `get_state.sessionId` matches it.
- `CreateNew`: requires a fresh canonical UUID that has no matching managed artifact row and no matching session identity under the controlled session root before start. It passes `--session-dir` and `--session-id` for that new UUID. It never uses `--session`, `--continue`, `--resume`, or a caller path.

Current Pi's `createSessionManager` in `dist/main.js` opens an exact existing `--session-id` and creates one when it is absent. The explicit mode is therefore a router precondition and proof boundary, not a claim that the two modes use different CLI flags.

After startup, the router reads only the private `get_state` response's `sessionId` and `sessionFile`, then calls the artifact seam. It validates the session ID, contains the reported file under the configured session root, resolves it safely, and records the root-relative identity. A missing physical target is `not_materialized`, not an inferred filename and not permission to delete anything. Current `SessionManager` can defer file persistence until it has an assistant entry. The artifact proof must cover this state.

### 7.3 Exact entry lookup and deterministic append

The private stock-Pi prerequisite has two typed seams. `get_entry` takes one exact entry ID, calls `session.sessionManager.getEntry(entryId)`, serializes at most 64 KiB, and returns `{"entry":null}` or one entry. It must not call `get_entries`, `get_tree`, a suffix scan, or a history traversal.

The paired append seam adds one validated optional `entryId` to the extension `sendMessage` path. It carries that value through `AgentSession.sendCustomMessage` to `SessionManager.appendCustomMessageEntry`. An explicit ID is accepted once. An occupied ID returns `already_present` only when the stored hidden custom entry exactly matches the requested custom type, content, display flag, and closed details. Any other occupied ID is a closed error. During a provider turn, the append outcome resolves only after the deferred entry has persisted. The resulting `message_end` exposes the persisted ID for receipt validation. The seam does not add an application command, custom type, payload field, route, model turn, P3, or P4.

`mecha-control-result-v1` derives `mcr1_<result UUID without hyphens>` and passes it through this seam with `triggerTurn:false` and no `deliverAs`. The router validates the exact persisted ID, `mecha.control.result.v1`, `display:false`, closed details, result/request IDs, payload hash, and the full session/runtime/manifest fence before marking delivery.

## 8. Recovery ownership and exact outcomes

### 8.1 Legacy 025 bridge

A backfilled action with `execution_owner='legacy025'` belongs only to `RecoverManagedRestartTransitions` and the old direct-restart executor. That executor lists a transition only when its joined action is nonterminal, has that owner, the matching action/stage pair, and the same action/operation ID. It owns all six paths: `reserved` and `prepared` use `admitted/admission`; `settling` uses `awaiting_settle/source_settle`; `stopping` uses `stopping_source/source_stop`; `stopped` uses `starting_target/target_start`; and `starting` uses `awaiting_target_ready/target_ready`.

Every old mutation bridges atomically. A success deletes transition, marks the action `succeeded/complete`, and terminalizes the operation. A preparation rollback deletes transition, marks the action failed at its current mapped stage, and terminalizes the operation. An old failure that retains a transition keeps the action nonterminal at the same mapped stage and owner. `RecoverManagedLifecycleControls` excludes it. A later lifecycle reset or terminate first supersedes the bridge action and terminalizes its operation in its admission transaction. The old transition can remain only as retained cleanup input for that higher action. Its executor's owner predicate is then false, so an old success, failure, or target-start callback is a no-op. New direct restarts are lifecycle-owned from reservation and never create a 025 transition.

| Legacy path | Required atomic bridge update | Later owner |
| --- | --- | --- |
| `reserved` preparation succeeds or fails before an external stop | preserve `admitted/admission`, or on `RollbackManagedRestartPreparation` delete transition and terminalize action/operation failed | old executor while retained; none after rollback |
| `prepared` / `settling` settle, old-group-absent, or recovery probe | keep `admitted/admission` or set `awaiting_settle/source_settle`; old-group-absent moves both to `starting_target/target_start` | old executor |
| `stopping` exact reap | move both to `stopping_source/source_stop`, then `starting_target/target_start` only after group absence | old executor |
| `stopped` target start/install | move both to `starting_target/target_start`; persist the same target fence in both rows before readiness | old executor |
| `starting` target readiness, target reap, or target reset | move both to `awaiting_target_ready/target_ready`, or back to `starting_target/target_start` after verified target absence | old executor |
| any legacy `RecordManagedRestartFailure` or shutdown record | retain transition/action, mapped stage, exact fence, and `execution_owner='legacy025'`; update action timestamp | old executor on later startup |
| `CompleteManagedRestartReady` | delete transition and terminalize linked action/operation `succeeded/complete` | none |
| higher reset or terminate admitted from any legacy state | terminalize old action/operation `superseded` before external work; retain/delete transition only as that higher action's safe cleanup requires | higher lifecycle action |

### 8.2 Pending-launch recovery

Every post-027 initial launch has the durable `managed_agents.launch_operation_id` link. `RecoverPendingManagedLaunches` selects only a row where `lifecycle_state='starting'`, that link is nonnull, and the linked operation is pending. It runs before lifecycle recovery, never starts or attaches to a process, and never scans ordinary desired-running rows. It probes only the stored durable group identity:

| Probe | Durable result | Signal | Retry |
| --- | --- | --- | --- |
| complete stored group is absent | agent `failed`; linked operation terminal `LAUNCH_RECOVERY_CANCELLED` / `managed launch cancelled during recovery` | none | no generic relaunch |
| complete stored group is owned | reap exact group, then the same failed agent and terminal launch outcome | exact owned group only | no generic relaunch |
| all-null/unpersisted group, present, mismatch, or unknown | agent `failed`; linked operation terminal `LAUNCH_RECOVERY_IDENTITY_UNSAFE` / `managed launch identity could not be verified`; retain name/slot | none | no generic relaunch |

A durable write failure leaves the agent and operation pending. The next startup repeats this probe. An all-null identity is never read as proof that no child started: it is an unsafe outcome that keeps the failed row held. This is a terminalization pass, not desired-state recovery. The pre-027 migration preflight rejects an unmatched `starting` row, so this path never guesses its operation.

`Router.Listen` calls these stages in this order before listener bind:

1. `Manager.RecoverPendingManagedLaunches(ctx)`.
2. `Manager.RecoverManagedRestartTransitions(ctx)`.
3. `Manager.RecoverManagedLifecycleControls(ctx)`.
4. `Store.ReconcileManagedRecipientSources(ctx)`.
5. `Store.ReconcileMessageTargets(ctx)`.
6. The existing bounded sweep.

`RecoverManagedLifecycleControls` loads only nonterminal `execution_owner='lifecycle'` rows. It does not scan desired-running agents and does not start generic crash recovery. At startup it clears stale recovery claims, then claims one action at a time with `ClaimManagedLifecycleRecovery(actionID, routerStartupID)`. The immediate transaction requires the same nonterminal state, action ID, ownership value, and complete source or target fence recorded by the row. It writes `recovery_owner` and `recovery_claimed_at`. Every stage transition clears that claim in the same transaction. A stale callback needs the current action ID and full durable fence. It is a no-op.

| Recovery condition | Durable result | Public projection | Retry |
| --- | --- | --- | --- |
| Reset needs old settlement but router has no old binding and old group is present | terminal `failed`, `RESET_RECOVERY_NEEDS_SETTLE` / `reset recovery requires a live settlement`, stage `source_settle` | `desired=running`, `lifecycle=failed`, `activity=failed`, admission closed, name/slot retained | false |
| Reset source-stop stage and exact group is absent | advance to `starting_target` | unchanged until next durable stage | n/a |
| Reset source-stop stage and exact group is owned | reap with existing group-only path, then advance | unchanged until next durable stage | n/a |
| Reset source or target probe is mismatch/unknown | terminal `failed`, `RESET_PROCESS_IDENTITY_UNSAFE` / `reset process identity could not be verified` | failed, closed, name/slot retained | false |
| Reset target group has no router binding but is absent | return to `starting_target` | closed | n/a |
| Reset target group is owned | reap it, return to `starting_target` | closed | true only after verified absence |
| Reset target group is present, mismatch, or unknown | terminal `failed`, `RESET_TARGET_CLEANUP_FAILED` / `reset replacement cleanup did not complete` | failed, closed, name/slot retained | false |
| Terminate has a known owned group and no RPC binding | record bounded internal abort-unavailable diagnostic, reap exact group, continue cleanup | no public success until cleanup commits | n/a |
| Terminate probe is mismatch/unknown | terminal `failed`, `TERMINATION_CLEANUP_FAILED` / `managed agent termination cleanup failed`, stage `terminate_reap` | `desired=terminated`, `lifecycle=failed`, `activity=failed`, admission closed, name/slot retained | false |
| Terminate cleanup resumes with no live group | continue retained cleanup idempotently | tombstone until final commit | depends on remaining stage |

Termination and launch share the agent lane from launch reservation through every callback. If termination wins before process start, its reservation atomically tombstones the agent and terminalizes the launch. The callback sees that closed link and returns without starting a process. If launch starts first, it must durably install the exact process identity or safely reap that process before it releases the lane. Termination then acts only on the stored exact group. A callback arriving after tombstone/final deletion verifies its operation link and agent row first; it may clean up only its own verified group and cannot recreate the agent, reopen admission, change a terminal operation, or start a replacement. Both the original launch and termination operations keep their immutable `202` bodies for replay. No result path rewrites either response.

The complete external-action matrix is:

| Event | Action state and stage | Code/message | Public state | Operation | Retained ownership | Projection |
| --- | --- | --- | --- | --- | --- | --- |
| Old reset settlement unavailable after reopen | failed / `source_settle` | `RESET_RECOVERY_NEEDS_SETTLE` / `reset recovery requires a live settlement`, false | running, failed, failed, closed | terminal, 24-hour expiry | old fence and old artifact retained; name/slot held | one bump |
| Reset target start or five-gate readiness fails after verified target cleanup | failed / `target_start` or `target_ready` | `RESET_TARGET_START_FAILED` / `reset replacement did not become ready`, true | running, failed, failed, closed | terminal, 24-hour expiry | old artifact and target artifact identity retained; name/slot held | one bump |
| Reset target identity fails and target group is absent | failed / `target_start` | `RESET_TARGET_IDENTITY_UNSAFE` / `reset replacement identity could not be verified`, true | running, failed, failed, closed | terminal, 24-hour expiry | old artifact and target identity retained | one bump |
| Old artifact unlink fails after target readiness | failed / `old_artifact_cleanup` | `RESET_OLD_ARTIFACT_CLEANUP_FAILED` / `reset session cleanup did not complete`, true | running, failed, idle, closed | terminal, 24-hour expiry | target fence/artifact and old artifact cleanup record retained; name/slot held | one bump |
| Terminate before launch process start | termination reservation terminalizes launch as `LAUNCH_CANCELLED_BY_TERMINATE`; callback is stale | `managed launch cancelled by termination`, false | terminated tombstone, then row removed on success | launch and termination operations terminal, 24-hour eligibility | no process exists; both immutable replay rows retained | one termination bump |
| Terminate after launch process installation | termination reaps only persisted exact group; launch is already terminalized | `managed launch cancelled by termination`, false | terminated tombstone, then row removed on success | launch and termination operations terminal, 24-hour eligibility | exact group until reaped; both immutable replay rows retained | one termination bump |
| Terminate abort fails, then exact reap and cleanup succeed | succeeded / `complete` | `TERMINATED` / `managed agent terminated`, false | row removed | terminal, 24-hour expiry | abort diagnostic only; all owned records removed | one removal bump |
| Terminate group, artifact, mail-retirement, or agent cleanup fails with a safe remaining stage | failed / failing stage | `TERMINATION_CLEANUP_FAILED` / `managed agent termination cleanup failed`, true | terminated, failed, failed, closed | termination operation terminal; launch already terminal | tombstone, fence/artifact/direct-mail stage and name/slot retained | one bump |
| Direct-mail retirement fails | failed / `direct_mail_retire` | `TERMINATION_CLEANUP_FAILED` / `managed agent termination cleanup failed`, true | terminated, failed, failed, closed | terminal, 24-hour expiry | tombstone and direct-mail owner state retained | one bump |
| Reset source/target identity is mismatch or unknown | failed / relevant stage | `RESET_PROCESS_IDENTITY_UNSAFE` or `RESET_TARGET_CLEANUP_FAILED`, false | running, failed, failed, closed | terminal, 24-hour expiry | no signal; retained fence and artifacts | one bump |
| Terminate identity is mismatch or unknown | failed / `terminate_reap` | `TERMINATION_CLEANUP_FAILED` / `managed agent termination cleanup failed`, false | terminated, failed, failed, closed | terminal, 24-hour expiry | no signal; tombstone/name/slot/fence retained | one bump |
| Process work succeeded but the next durable write fails | no invented terminal outcome; retained prior nonterminal state and stage | none until recovery commits one | prior closed projection | pending; no expiry | persisted pre-action fence is recovery input | no bump from failed write |

The durable-write fault tests inject failure after source reaping, target start, target readiness, artifact unlink, direct-mail retirement, and final delete. Recovery must use the persisted pre-action stage and exact probe result. It must not repeat an unsafe signal or create a second target.

## 9. Required proof and implementation ownership

| Path | Owner |
| --- | --- |
| `router/contract/manifest.json`, `router/contract/http-outcomes.json` | The three route shapes, static `restart-all` precedence, local unauthenticated/no-store behavior, replay and retry outcomes. |
| `router-proxy/internal/transport/managed_http.go` | Strict parsing, worker-wide idempotency, route-specific retry selection, immutable replay. |
| `router-proxy/internal/transport/router.go` | The exact pre-bind recovery call order. |
| `router-proxy/internal/managed/rpc.go` | `Abort`, exact-entry client, bounded response validation. |
| tested stock Pi `rpc-types.ts`, `rpc-mode.ts`, `session-manager.ts`, `agent-session.ts`, and `extensions/types.ts` | Typed exact lookup plus validated explicit custom-message entry ID, duplicate matching, deferred persistence, and typed append outcome. |
| `pi-extensions/mecha-rpc/rpc-client.ts`, `index.ts` | Derive and pass the reserved root result ID without changing the application vocabulary or payload shape. |
| `router-proxy/internal/managed/process.go` | `SessionLaunchMode` create/resume preconditions and no caller session path. |
| `router-proxy/internal/managed/session_artifacts.go` | Discovery, containment, no-symlink unlink, artifact ledger, retry, and redaction. |
| `router-proxy/internal/managed/supervisor.go` | Per-agent precedence, launch/terminate serialization, legacy bridge execution, reset/terminate/Restart All orchestration, pending-launch and lifecycle recovery, stale-callback fencing. |
| `router-proxy/internal/store/migrations/027_managed_control_requests.sql` | The exact historical schema, rebuild, checks, indexes, and 022–026 preservation above. |
| `router-proxy/internal/store/migrations/028_managed_restart_all_batch_references.sql` | The narrow forward rebuild in section 5.2.1 that permits multiple coalesced-parent references while preserving the 027 batch graph. |
| `router-proxy/internal/store/managed.go` | Immediate lifecycle/control/batch/artifact/launch transactions, legacy bridge terminalization, recovery claim, graph retention, and delete order. |
| `router-proxy/internal/store/delivery.go` | The accepted `RetireManagedRecipientSourceForTermination` boundary. |
| focused store/manager/RPC/process/artifact/transport tests | Every check, full-or-null group, 65th/513th control capacity, retry schedule, graph retention order/rollback, six-stage legacy bridge result, launch/terminate race, complete-owned/absent and all-null/unpersisted/present/mismatch/unknown launch recovery, and durable-write fault. |
| `router-proxy/internal/transport/managed_root_control_native_test.go` | Installed-Pi S4 root-control proof. |

### 9.1 S4 root-control ownership amendment

S4 owns the root-control handler registry. It is not a later P2 prerequisite and cannot be represented by a placeholder, a default handler, or an unowned operation name. `ManifestTool` carries `capability_id`, `name`, `operation_id`, `definition_id`, and `definition_sha256` from the closed manifest. Manager construction validates that every installed manifest tool resolves to one registry entry with the same complete identity. Missing, duplicate, surplus, or mismatched entries fail construction before a request is admitted.

| Path | S4 change and owner |
| --- | --- |
| `router-proxy/internal/managed/manifest.go` | Preserve and validate the complete manifest identity used by root admission. |
| `router-proxy/internal/managed/root_control.go` | Root-only admission, reservation, full-fence execution/recovery ownership, and result-ledger transitions. It accepts only completed `mecha.control.request.v1` events with `context_ref:"root"`. |
| `router-proxy/internal/managed/root_control_handlers.go` | `RootControlHandler`, `RootControlHandlerRegistry`, all 16 explicit entries, root/child availability checks, and correlation-aware execution adapters. Each adapter receives the immutable request ID, result ID, tool-call ID, complete fence, and canonical arguments. |
| `router-proxy/internal/managed/root_control_handlers_test.go` | Exact 16-entry registry, complete-identity, duplicate/missing/surplus rejection, every handler's single owner, and root rejection of the child-only operation. |
| `router-proxy/internal/managed/root_control_test.go` | Reservation, handler claim/completion, duplicate/recovery, cancellation, full-fence stale rejection, and result append/reconciliation. |
| `router-proxy/internal/managed/rpc.go`, `router-proxy/internal/managed/rpc_test.go` | Private deterministic control-result append, typed exact `get_entry`, and receipt validation. No history query is added. |
| `router-proxy/internal/managed/supervisor.go`, `router-proxy/internal/managed/supervisor_root_control_test.go` | Start/recovery sequencing and reset-fence cancellation before any stale append. |
| `pi-extensions/mecha-rpc/rpc-client.ts`, `pi-extensions/mecha-rpc/tool-registry.ts`, `pi-extensions/mecha-rpc/index.ts`, `tests/mecha_rpc_extension.test.ts` | The manifest-backed generated-tool root emitter remains the path under test. After it builds and validates the full envelope, it synchronously registers the root pending ID before `emitControlRequest`; send failure rolls back that same registration, while success retains it through an accepted or `already_present` result append. Capacity rejects a new registration before emission and never evicts a live ID. The test-only probe uses the same registered full-envelope seam. No new production command, tool, custom type, route, provider, or model behavior is added. |
| `tests/fixtures/mecha-rpc/s4-native-root-control-probe.ts` | Test-only extension. Its private slash command uses a narrow test-only full-envelope hook that drives the installed `goal` generated-tool path. It never imports or calls `emitLegacyCommandControl`. After the full manifest is installed, the hook reads that installed definition and builds only `mecha.control.request.v1` details with `protocol`, a fresh `request_id` and `tool_call_id`, `capability_id:"goal"`, `tool_name:"goal"`, the installed `definition_id`, `operation_id:"goal.control"`, the current `manifest_sha256`, `runtime_generation`, and `session_epoch`, `context_ref:"root"`, and closed arguments `{"action":"status"}`. It calls the shared registered full-envelope seam, which places the request ID in `rootPendingRequestIds` before it calls `emitControlRequest`; a send failure removes that ID. It is loaded only by the native test, absent from the production managed argv, and never starts a model turn. |
| `router-proxy/internal/transport/managed_root_control_native_test.go` | Actual-router/installed-Pi S4 proof and the only writer of the S4 evidence artifact. |

The registry has these exact entries. `mecha_parent_message` still has one handler so the 16-entry closure is complete, but that handler returns the fixed closed `CHILD_ONLY_OPERATION` result for a root request before any business action or append reservation. The other 15 entries are root-available. A handler can use its existing manager/store adapter, but no adapter may claim a request outside its listed operation or finalize a result without the request ID and complete fence.

| Operation ID | Tool name | Root availability |
| --- | --- | --- |
| `goal.control` | `goal` | available |
| `goal.snooze` | `goal_snooze` | available |
| `context.control` | `mecha_context` | available |
| `ops.git_status` | `mecha_git` | available |
| `mail.send` | `mecha_mailbox_send` | available |
| `mail.parent` | `mecha_parent_message` | child-only; fixed root rejection |
| `ops.pr_status` | `mecha_pr` | available |
| `ops.priority` | `mecha_priority` | available |
| `recall.control` | `mecha_recall` | available |
| `reminder.control` | `mecha_reminder` | available |
| `policy.inline_allowance` | `mecha_request_inline_tool_allowance` | available |
| `router.control` | `mecha_router` | available |
| `notifications.settled` | `mecha_settled_notifications` | available |
| `ops.state` | `mecha_state` | available |
| `ops.verification_handoff` | `mecha_verification_handoff` | available |
| `ops.worker` | `mecha_worker` | available |

The native test uses the accepted G4 installed Pi, fixed managed argv plus the test-only probe, an isolated session root, and no browser, credentials, UI, model turn, or provider work. Before it starts the router or Pi, it requires `MECHA_PI_BIN`, `MECHA_PI_PACKAGE_ROOT`, and `MECHA_PI_SOURCE_LOCK`. It parses the G4 lock and verifies its schema; the public base and scoped Pi commit/tree/merge-base/remote/clean state and changed-path allowlist; package-lock, tarball, installed-package, and executable hashes; package name/version; and the locked `pi-packages` commit/tree. `MECHA_PI_BIN` must be an absolute executable whose real path exactly matches the lock's installed executable and is inside the locked package root. `MECHA_PI_PACKAGE_ROOT` must resolve to the lock's installed package root. Every spawn and version/provenance call uses that resolved executable. An unset, non-absolute, non-contained, mismatched, global, or `PATH` fallback fails before Pi starts.

The test may send Pi a `prompt` RPC frame only to invoke the probe's private slash command. That is command dispatch, not a model prompt. A provider-call interceptor starts before Pi and must report exactly zero calls after the proof. The test proves:

1. The supported `abort` RPC returns the typed successful response under the five-second bound.
2. A `CreateNew` reset starts a fresh session ID, reports that ID through `get_state`, and records either a safely discovered materialized artifact or the verified `not_materialized` state. It keeps an old controlled artifact through target failure. After target readiness it deletes that old artifact only after containment and ownership checks.
3. An ambiguous, symlinked, missing-after-materialization, or root-escaping artifact fails safely and performs no unlink.
4. The probe emits one complete manifest-backed root request after five-gate readiness. Its strict details shape has every S4 root-envelope field, the installed `goal` tuple, current runtime/manifest fence, `context_ref:"root"`, and valid closed `{"action":"status"}` arguments. It contains none of `legacy_command`, `raw_arguments`, or `availability`. The router resolves the installed complete identity, including its definition hash, to one `goal.control` registry handler. The test forces Pi to publish the completed request event and the router to invoke `/mecha-control-result-v1` before `emitControlRequest` resolves. The request ID is already in `rootPendingRequestIds`, so that early result succeeds. A controlled send failure emits no request event and rolls back only its pre-emission pending ID.
5. The root control ledger commits `mcr1_<UUID-without-hyphens>` before append. The persisted hidden entry ID equals that reserved value. The test interrupts after the hidden entry persists but before durable receipt settlement, reconstructs the router/RPC recovery path against the same controlled session, and uses private `get_entry(result_entry_id)` to reconcile the one entry. It proves first append, exact duplicate/already-present acceptance, and rejection of changed payload, details, or fence values. It records the RPC method trace and rejects any `get_entries`, `get_tree`, history, or suffix request. The root pending ID is consumed only after `accepted` or `already_present`.
6. Reset cancels the old full fence with `CANCELLED_BY_RESET` and cannot append its old-session result into the new session.

Run the proof exactly as follows after the four section-2 gates are accepted:

```sh
cd /Users/wolfgang/code/mecha-developer/router-proxy
MECHA_PI_BIN=/private/tmp/pi-g4-install/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js \
MECHA_PI_PACKAGE_ROOT=/private/tmp/pi-g4-install/node_modules/@earendil-works/pi-coding-agent \
MECHA_PI_SOURCE_LOCK=/tmp/rpc-phase2-pi-g4-source-lock.json \
go test ./internal/transport -run '^TestManagedRootControlInstalledPi$' -count=1 -v
```

The test emits one `MECHA_ROOT_CONTROL_S4_ARTIFACT=<path>` line. The path is deterministic from the tested tree and S4 candidate hash:

```text
/tmp/mecha-rpc-acceptance/<mecha-head>/phase2-root-control-s4/<candidate-content-sha256>/root-control-s4-v1.json
```

Run this read-back separately, with the exact path printed by the completed test:

```sh
artifact='<path printed by TestManagedRootControlInstalledPi>'
test -r "$artifact"
shasum -a 256 "$artifact"
jq -e '.protocol == "mecha.phase2.root-control-s4/v1"
  and .result == "pass"
  and .assertions.g4_lock_verified
  and .assertions.locked_executable
  and .assertions.no_path_fallback
  and .assertions.no_model_turn
  and .assertions.zero_provider_calls
  and .assertions.full_manifest_root_envelope
  and .assertions.no_legacy_emission
  and .assertions.root_pending_before_emission
  and .assertions.forced_result_before_send_resolution
  and .assertions.root_pending_before_result
  and .assertions.send_failure_rolls_back_pending
  and .assertions.exact_get_entry_recovery
  and .assertions.duplicate_already_present
  and .assertions.changed_receipt_rejected
  and .assertions.reset_fence_cancelled
  and .assertions.handler_registry_complete' "$artifact"
```

The artifact contains only the tested repository head/tree, candidate-path hashes, accepted G1–G4 evidence hashes, the G4 lock digest and public-safe source/package/executable identity, the 16-operation registry hash, a bounded RPC-method trace, and boolean assertions. It contains no raw session content, artifact paths, credentials, environment values, PID data, request payloads, or result payloads.

## 10. Definition of done

The slice is ready for implementation only after the gates in section 2 are accepted. The 027 migration must apply from an accepted populated 026 database through the foreign-key-safe sequence in section 5.0. It must preserve 022–026 rows and indexes, reject unmatched pre-027 pending launches without mutation, pass the rebuild success and rollback tests in that section, and pass every named constraint, graph-retention, six-stage legacy-bridge, launch/terminate race, recovery, exact-entry, process, artifact, handler-registry, and fault test. The S4 native proof must run on the independently verified G4 executable, emit only the complete manifest-backed root envelope through the installed registry, and prove root-pending registration before emission under the forced result-before-send-resolution schedule. When S2 adds Restart All, migration 028 must also preserve populated 027 batch rows, remove only the two global child-reference uniqueness constraints, retain parent-scoped uniqueness, and pass its upgrade and rollback proof. The source-fence proof must cover the direct Restart All parent's intentional all-null fence and every Restart All child's full fence. The final diff must remain inside this lifecycle/control footprint and keep the local unauthenticated boundary, migration order, active managed-mail scope, and P3/P4 exclusions intact.
