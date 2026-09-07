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
- P3/P4, browser/session authentication, provider work, UI work, generic desired-running recovery, tmux lifecycle, Phase 1 extension/API changes, and direct-mail redesign stay out of scope.

## 2. Entry gates

No 027 implementation starts until all four gates are recorded on a clean commit:

1. Same-session restart replacement and its `Router.Listen` recovery are accepted.
2. The five-gate `readinessGate(ctx, binding, fence)` is accepted.
3. Migration 026 direct managed-mail delivery is accepted on a clean commit. The current working-tree 026 changes are not this gate.
4. The installed Pi used by native tests has the private exact `get_entry(entry_id)` RPC described in section 8. The current installed Pi 0.85.0 has `abort`, `get_state`, and `--session-id` support. It does not have this exact-entry RPC.

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

Restart All has one bounded parent transaction. This closes concurrent replay without storing a placeholder response.

1. Start `BEGIN IMMEDIATE`.
2. Resolve `(POST, X-Request-ID)` under `router-managed-worker-v1`. An exact matching row returns its immutable response. A path/body mismatch returns `IDEMPOTENCY_CONFLICT`.
3. Read eligible managed IDs once, ordered by `agent_id ASC`. The ordered list is the snapshot. Its zero-based list position is `snapshot_position`.
4. Compute one item for every snapshot ID from the durable rows while the writer lock is held. No row is written yet. An item is `accepted` with a preallocated internal child UUID, `coalesced` with the existing equal-priority operation, or `rejected` with a fixed safe code and message.
5. Insert the parent operation with its complete immutable `202` response, then its `restart_all` lifecycle action. Insert accepted child operations/actions and every batch item after the parent exists. This all stays inside the same transaction.
6. Terminalize the parent operation and parent lifecycle action as `BATCH_ADMITTED` in that transaction. Commit.
7. After commit, enqueue accepted child execution through each agent's existing lane. The parent transaction never waits for a lane, process, settlement, or readiness check.

A crash before commit leaves no parent, child operation, action, or item. A crash after commit leaves the full immutable batch and recoverable child records. A concurrent caller waits on SQLite's writer lock, then observes the immutable parent. This creates at most one child admission per `(parent_action_id, agent_id)` and one item per snapshot position.

The maximum snapshot size is the existing managed-agent cap of 32. The parent response describes admission only. Later child replacement success is visible through each agent projection.

## 5. Migration 027: exact durable contract

Migration `027_managed_control_requests.sql` is forward-only. It does not alter migrations 022–026. The new independent tables are created in this order: `managed_session_artifacts`, `managed_lifecycle_actions`, `managed_lifecycle_batch_items`, and `managed_control_requests`; their indexes follow. The managed-agent rebuild comes last.

### 5.0 Populated SQLite rebuild procedure

`managed_agents` has populated foreign-key children after 026. They are `managed_readiness_sources` from 024, `managed_restart_transitions` from 025, and `managed_direct_delivery_attempts` plus `managed_direct_delivery_diagnostics` from 026. A normal parent `DROP TABLE` fails with foreign keys enabled. `PRAGMA defer_foreign_keys` does not fix that drop. `PRAGMA foreign_keys` cannot change inside `BEGIN IMMEDIATE`.

The store has one SQLite connection (`SetMaxOpenConns(1)`) and holds its store mutex during migrations. Migration 027 gets a dedicated runner on that same connection. It is the only migration allowed to suspend foreign-key enforcement. The runner must use this sequence:

1. Verify `PRAGMA foreign_keys=1` and `PRAGMA legacy_alter_table=0`. Verify that 024, 025, and 026 are recorded in `schema_migrations`, and that the four child tables above exist. Capture each child's `sqlite_master` table and index SQL, row count, and `PRAGMA foreign_key_list` result.
2. Outside any transaction, execute `PRAGMA foreign_keys=OFF`, then read it back as `0`. Do not use a separate connection.
3. Start `BEGIN IMMEDIATE`. Create the four new 027 tables and their indexes. Create `managed_agents_027_new` with the complete 025 `managed_agents` column list, constraints, and the sole change `desired_state IN ('running','terminated')`. Do not create, detach, copy, drop, or recreate any child table.
4. Copy the parent with an explicit ordered 025 column list. Save a temporary full-row snapshot before the copy. The copied row count must equal the snapshot count, and two directional `EXCEPT` queries over every copied column must be empty.
5. Drop `managed_agents`; rename `managed_agents_027_new` to `managed_agents`; recreate only `managed_agents_slot_idx` with its 025 definition. With `legacy_alter_table=OFF`, SQLite rewrites the existing child foreign-key metadata to the replacement parent name while leaving all child rows and DDL intact.
6. Before committing, require zero rows from `PRAGMA foreign_key_check` and `PRAGMA integrity_check` equal to `ok`. Compare every captured child table/index SQL, row count, and foreign-key list with its pre-rebuild value. The child foreign key must still name `managed_agents(agent_id)`.
7. Insert the 027 migration record, then commit. Outside the transaction, execute `PRAGMA foreign_keys=ON` and read it back as `1`. Run `PRAGMA foreign_key_check` again before returning the store.

The runner must roll back the open transaction for any SQL error, parity failure, foreign-key failure, integrity failure, or migration-record failure. It must then restore and verify `PRAGMA foreign_keys=ON`. Rollback leaves the old parent, all 024/025/026 children, their rows, indexes, and migration ledger untouched, with no 027 record. If the post-commit re-enable or verification fails, the runner closes the database and returns an error; it must not return a store using a connection with foreign keys disabled.

The required migration tests use a real accepted-026 fixture with populated rows in `managed_agents`, all four child tables, `managed_http_operations`, and an in-flight 025 restart transition. They prove: successful 026-to-027 upgrade; parent row equality; child row/count/DDL/index/FK-list equality; both foreign-key checks and integrity check clean; widened `desired_state` accepts `terminated` and still rejects any other value; expected 027 tables and indexes exist; and reopen leaves foreign keys enabled. Failure-injection tests fail after the parent copy, after the parent drop, and at the pre-commit foreign-key check. Each proves rollback preserves the 026 schema/data, records no 027 migration, and restores `foreign_keys=ON`. A separate test proves `PRAGMA foreign_keys=OFF` issued after `BEGIN IMMEDIATE` does not count as this procedure.

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
  CHECK ((kind='restart_all') = (retry_of IS NULL)),
  CHECK ((source='restart_all_child') = (parent_action_id IS NOT NULL)),
  CHECK ((kind='restart_all') OR parent_action_id IS NULL),
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

The store also requires a retry predecessor to be terminal `failed`, `retryable=1`, the same kind and agent, and without a prior successor. SQLite's row-local checks cannot express that predecessor lookup.

027 backfills every nonterminal `managed_restart_transitions` row into one `restart` action before it creates the active-lane index. `reserved` and `prepared` map to `admitted/admission`; `settling` maps to `awaiting_settle/source_settle`; `stopping` maps to `stopping_source/source_stop`; `stopped` maps to `starting_target/target_start`; and `starting` maps to `awaiting_target_ready/target_ready`. The action uses `operation_id` as `action_id`, its transition's complete old fence, its `target_runtime_generation` as `planned_target_runtime_generation`, any complete target process fence, and the transition timestamps. The planned target generation is not a process fence; the target process/session/epoch/runtime/manifest group remains all-null until identity verification. The migration fails if its operation is not pending or this one-to-one insert cannot be made. Existing restart recovery remains its owner until a later lifecycle action supersedes it.

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

The store validates this pair before every insert or update. A direct action requires a full source fence except a terminate action that was admitted with no binding. That no-binding terminate keeps the source fence all-null. A reset requires a full source fence and its own `planned_target_runtime_generation`. A restart/reset target fence is full only after target process identity is verified; its runtime generation must equal the earlier plan.

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
  operation_name TEXT NOT NULL CHECK (length(CAST(operation_name AS BLOB)) BETWEEN 1 AND 128),
  tool_name TEXT NOT NULL CHECK (length(CAST(tool_name AS BLOB)) BETWEEN 1 AND 128),
  definition_sha256 TEXT NOT NULL CHECK (length(definition_sha256)=64
    AND definition_sha256=lower(definition_sha256) AND definition_sha256 NOT GLOB '*[^0-9a-f]*'),
  canonical_input_sha256 TEXT NOT NULL CHECK (length(canonical_input_sha256)=64
    AND canonical_input_sha256=lower(canonical_input_sha256) AND canonical_input_sha256 NOT GLOB '*[^0-9a-f]*'),
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
  handler_started_at TEXT,
  side_effect_committed_at TEXT,
  handler_completed_at TEXT,
  append_pending_at TEXT,
  append_submitted_at TEXT,
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
  CHECK ((state IN ('append_pending','awaiting_append','delivered')) = (result_entry_id IS NOT NULL)),
  CHECK ((result_entry_id IS NULL) OR result_entry_id = 'mcr1_' || replace(result_id,'-','')),
  CHECK ((state='awaiting_append') = (append_submitted_at IS NOT NULL)),
  CHECK ((state='append_pending') OR next_append_at IS NULL),
  CHECK ((state='append_pending' AND append_attempt BETWEEN 1 AND 4)
    OR (state<>'append_pending'))
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
  WHERE state='append_pending';
CREATE INDEX managed_control_terminal_retention_idx
  ON managed_control_requests(agent_id, terminal_at DESC, request_id DESC)
  WHERE state IN ('delivered','failed','cancelled');
```

A duplicate request ID with the same fingerprint and full fence is the existing row. A changed fingerprint or fence is `CONFLICT`. Reservation happens in one immediate transaction that checks fewer than 64 nonterminal rows for that agent and fewer than 512 router-wide. The 65th per-agent or 513th router-wide reservation fails before a handler call with the fixed bounded `CONFLICT` result.

`result_content`, `result_details_json`, and `error_detail` pass the established secret redaction before storage. `error_detail` has no raw RPC/process text.

### 5.5 Foreign-key and deletion order

027 starts with this post-026 ownership inventory:

| Record | FK or owner after 026 | Successful termination disposition |
| --- | --- | --- |
| `managed_restart_transitions` | `managed_agents` and operation | Existing restart owner resolves/deletes it before lifecycle cleanup. |
| `managed_readiness_sources` | `managed_agents` | Delete in termination cleanup. |
| `managed_lifecycle_actions` | operation only; no agent FK | Retain the termination action until its operation expires. Delete prior agent actions during cleanup. |
| `managed_lifecycle_batch_items` | parent lifecycle action | Cascade when the parent operation/action expires. |
| `managed_session_artifacts` | lifecycle action nullable target reference; no agent FK | Unlink first, then delete the row. |
| `managed_control_requests` | no agent FK | Cancel, then delete during successful termination cleanup. |
| `recipient_sources`, snapshots, direct attempts, diagnostics | migration 026 direct-mail owner | The hook retires/promotes the source, makes old snapshots invalid targets without retargeting them, deletes this agent's attempts/diagnostics, and leaves ordinary message/snapshot history intact. |
| `managed_http_operations` | external replay owner | Never cascade from `managed_agents`. Terminate's own operation stays until 24-hour expiry. |

Successful termination runs this ordered transaction sequence after external group work: cancel controls; retire direct mail through its hook, which settles/removes its own direct attempts and diagnostics; delete router-owned session artifacts after verified unlink; delete restart transition/readiness source/prior agent lifecycle rows/control rows, deleting retry descendants before their predecessors; delete histories and policy state through their existing owners; delete `managed_agents`; terminalize the termination operation. The final durable transaction releases the name and slot once. Any failure leaves the tombstone and name/slot held.

## 6. Root-control state graph and retention

The root-control ledger has these only transitions:

```text
received -> executing -> side_effect_committed -> completed
completed -> append_pending -> awaiting_append -> delivered
awaiting_append -> append_pending
received|executing|side_effect_committed|completed|append_pending|awaiting_append -> failed|cancelled
```

The handler has a 30-second deadline. Model turns and deferred compaction are not handler work. The router reserves `append_pending`, the deterministic `mcr1_<result UUID without hyphens>` entry ID, complete fence, payload hash, and `append_attempt=1` before the private append command.

An absent exact entry under a current matching live binding moves the request back to `append_pending` with these durable delays:

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

### 7.3 Exact entry lookup

The private stock-Pi change is one typed `get_entry` RPC command. It takes an exact entry ID, calls `session.sessionManager.getEntry(entryId)`, serializes at most 64 KiB, and returns one entry or an explicit absent result. It must not call `get_entries`, `get_tree`, a suffix scan, or a history traversal. The router validates entry ID, `mecha.control.result.v1`, `display:false`, closed details, result/request IDs, payload hash, and the full session/runtime/manifest fence before marking delivery.

## 8. Recovery ownership and exact outcomes

`Router.Listen` calls these stages in this order before listener bind:

1. `Manager.RecoverManagedRestartTransitions(ctx)`.
2. `Manager.RecoverManagedLifecycleControls(ctx)`.
3. `Store.ReconcileManagedRecipientSources(ctx)`.
4. `Store.ReconcileMessageTargets(ctx)`.
5. The existing bounded sweep.

`RecoverManagedLifecycleControls` loads only nonterminal lifecycle rows. It does not scan desired-running agents and does not start generic crash recovery. At startup it clears stale recovery claims, then claims one action at a time with `ClaimManagedLifecycleRecovery(actionID, routerStartupID)`. The immediate transaction requires the same nonterminal state, action ID, and complete source or target fence recorded by the row. It writes `recovery_owner` and `recovery_claimed_at`. Every stage transition clears that claim in the same transaction. A stale callback needs the current action ID and full durable fence. It is a no-op.

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

The complete external-action matrix is:

| Event | Action state and stage | Code/message | Public state | Operation | Retained ownership | Projection |
| --- | --- | --- | --- | --- | --- | --- |
| Old reset settlement unavailable after reopen | failed / `source_settle` | `RESET_RECOVERY_NEEDS_SETTLE` / `reset recovery requires a live settlement`, false | running, failed, failed, closed | terminal, 24-hour expiry | old fence and old artifact retained; name/slot held | one bump |
| Reset target start or five-gate readiness fails after verified target cleanup | failed / `target_start` or `target_ready` | `RESET_TARGET_START_FAILED` / `reset replacement did not become ready`, true | running, failed, failed, closed | terminal, 24-hour expiry | old artifact and target artifact identity retained; name/slot held | one bump |
| Reset target identity fails and target group is absent | failed / `target_start` | `RESET_TARGET_IDENTITY_UNSAFE` / `reset replacement identity could not be verified`, true | running, failed, failed, closed | terminal, 24-hour expiry | old artifact and target identity retained | one bump |
| Old artifact unlink fails after target readiness | failed / `old_artifact_cleanup` | `RESET_OLD_ARTIFACT_CLEANUP_FAILED` / `reset session cleanup did not complete`, true | running, failed, idle, closed | terminal, 24-hour expiry | target fence/artifact and old artifact cleanup record retained; name/slot held | one bump |
| Terminate abort fails, then exact reap and cleanup succeed | succeeded / `complete` | `TERMINATED` / `managed agent terminated`, false | row removed | terminal, 24-hour expiry | abort diagnostic only; all owned records removed | one removal bump |
| Terminate group, artifact, mail-retirement, or agent cleanup fails with a safe remaining stage | failed / failing stage | `TERMINATION_CLEANUP_FAILED` / `managed agent termination cleanup failed`, true | terminated, failed, failed, closed | terminal, 24-hour expiry | tombstone, fence/artifact/direct-mail stage and name/slot retained | one bump |
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
| `router-proxy/internal/managed/process.go` | `SessionLaunchMode` create/resume preconditions and no caller session path. |
| `router-proxy/internal/managed/session_artifacts.go` | Discovery, containment, no-symlink unlink, artifact ledger, retry, and redaction. |
| `router-proxy/internal/managed/supervisor.go` | Per-agent precedence, reset/terminate/Restart All orchestration, lifecycle recovery, stale-callback fencing. |
| `router-proxy/internal/store/migrations/027_managed_control_requests.sql` | The exact schema, rebuild, checks, indexes, and 022–026 preservation above. |
| `router-proxy/internal/store/managed.go` | Immediate lifecycle/control/batch/artifact transactions, terminalization, recovery claim, retention, and delete order. |
| `router-proxy/internal/store/delivery.go` | The accepted `RetireManagedRecipientSourceForTermination` boundary. |
| focused store/manager/RPC/process/artifact/transport tests | Every check, full-or-null group, 65th/513th control capacity, retry schedule, race, recovery, and durable-write fault. |
| `router-proxy/internal/transport/managed_native_test.go` | Installed-Pi proof. |

The native test uses a configured installed Pi, the fixed managed argv/extensions, an isolated session root, and no browser, credentials, UI, or model prompt. It proves:

1. The supported `abort` RPC returns the typed successful response under the five-second bound.
2. A `CreateNew` reset starts a fresh session ID, reports that ID through `get_state`, and records either a safely discovered materialized artifact or the verified `not_materialized` state. It keeps an old controlled artifact through target failure. After target readiness it deletes that old artifact only after containment and ownership checks.
3. An ambiguous, symlinked, missing-after-materialization, or root-escaping artifact fails safely and performs no unlink.
4. The root control ledger reserves `mcr1_<UUID-without-hyphens>` before append. The private `get_entry` finds that one persisted entry after a router/process reconnection. Changed payload/details/fence values fail closed. No history or suffix query is used.
5. Reset cannot append an old-session control result into the new session.

The test records only public-safe assertions. It does not print raw session content, artifact paths, credentials, environment values, PID data, or RPC payloads.

## 10. Definition of done

The slice is ready for implementation only after the gates in section 2 are accepted. The 027 migration must apply from an accepted populated 026 database through the foreign-key-safe sequence in section 5.0. It must preserve 022–026 rows and indexes, pass the rebuild success and rollback tests in that section, and pass every named constraint, race, retention, exact-entry, process, artifact, and fault test. The final diff must remain inside this lifecycle/control footprint and keep the local unauthenticated boundary, migration order, active managed-mail scope, and P3/P4 exclusions intact.
