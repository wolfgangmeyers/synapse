# Plan: Unified Worker Discovery for Embedded and Tmux Workers in `mecha_worker action="list"`

**Plan ID:** PLAN-TMUX-WORKER-LIST-001  
**Status:** DRAFT (Dev-Loop Phase 2)  
**Authors:** Wolfgang Meyers, Mecha Core Team  
**Date:** October 2026  
**Related Spec:** `docs/specs/tmux-worker-list-spec.md`  

---

## 1. Overview & Architecture

Currently, the `mecha_worker(action="list")` tool implementation only queries SQLite for `runtime='embedded'` agents. This plan describes how to expand worker discovery to include tmux workers from both persistent router state (`recipient_sources` + `agent_leases`) and live tmux session windows via `TmuxAdapter`, uniting and deduplicating them into a backwards-compatible schema enriched with `kind: "embedded" | "tmux"`.

```
                        mecha_worker(action="list")
                                     │
                                     ▼
                      Manager.ListWorkers(ctx, invocation)
                                     │
             ┌───────────────────────┴───────────────────────┐
             ▼                                               ▼
 Store.ListEmbeddedToolWorkers                  Live Tmux Inspection
  - embedded: managed_agents                     (via TmuxWindowLister)
  - tmux: recipient_sources                      - windows in 'mecha'
    + agent_leases (active lease check)          - exclude supervisor, webui, etc.
             │                                               │
             └───────────────────────┬───────────────────────┘
                                     ▼
                           Merge & Deduplicate
                           - Sort by worker_id
                           - Generate output:
                             { "workers": [
                               {
                                 "worker_id": "...",
                                 "lifecycle_state": "running",
                                 "desired_state": "running",
                                 "kind": "embedded" | "tmux"
                               }
                             ]}
```

---

## 2. Key Design Decisions

1. **Output Schema Representation**:
   Define a unified Go struct for tool items and JSON serialization:
   ```go
   type ToolWorkerItem struct {
       WorkerID       string `json:"worker_id"`
       LifecycleState string `json:"lifecycle_state"`
       DesiredState   string `json:"desired_state"`
       Kind           string `json:"kind"` // "embedded" | "tmux"
   }
   ```
   This preserves backwards compatibility for consumers expecting `worker_id`, `lifecycle_state`, and `desired_state`, while providing the new `kind` field.

2. **Durable Database Worker Discovery (`Store`)**:
   In `router-proxy/internal/store/embedded_tool_admission.go`:
   - Enhance or complement `ListEmbeddedToolWorkers`:
     - Query embedded workers from `managed_agents` (as currently done).
     - Query tmux recipients from `recipient_sources`:
       ```sql
       SELECT r.recipient_name,
              CASE
                WHEN l.lease_id IS NOT NULL AND l.expires_at > ? THEN 'running'
                ELSE 'idle'
              END AS lifecycle_state,
              'running' AS desired_state
       FROM recipient_sources r
       LEFT JOIN agent_leases l ON l.agent_id = r.recipient_name
       WHERE r.source_kind = 'tmux'
         AND r.state IN ('active', 'reserved')
         AND r.recipient_name NOT IN (
           'system', 'reminders', 'router', 'user', 'supervisor', 'web_ui',
           'managed-runtime-readiness', 'reflex-router-proxy-launcher'
         )
       ORDER BY r.recipient_name
       ```
     - For each tmux recipient with an active unexpired lease, mark `lifecycle_state = "running"`. If no active lease or expired, mark `lifecycle_state = "idle"`.
     - Assign `Kind = "embedded"` for managed agents and `Kind = "tmux"` for tmux recipients.

3. **Live Tmux Window Lister Abstraction (`Manager`)**:
   - Define a `TmuxWindowLister` interface in `internal/managed` (or decouple via a func field on `ManagerOptions`):
     ```go
     type TmuxWindowLister interface {
         Windows(ctx context.Context, session string) ([]transport.TmuxWindow, error)
     }
     ```
   - Wire `TmuxAdapter` into `Manager` during router initialization in `cmd/mailbox-router/main.go` and `transport.New`.
   - If `TmuxWindowLister` is available, query the configured or default session (`"mecha"`).
   - Filter out reserved window names (`supervisor`, `webui`, `web_ui`, `router`).
   - For windows found in tmux that aren't yet in the database results:
     - Add them with `lifecycle_state: "starting"`, `desired_state: "running"`, `kind: "tmux"`.
   - Graceful degradation: if `TmuxWindowLister` is nil, returns an error (e.g., tmux not running, `ErrTmuxUnavailable`), log and continue with the database-discovered workers without failing the request.

4. **Deduplication & Sorting**:
   - Deduplicate by `worker_id`. If a worker appears in both live tmux windows and database `recipient_sources`, the database record with active lease takes precedence over the unattached window.
   - If an identity collision occurs between embedded and tmux, embedded takes precedence.
   - Sort items deterministically by `worker_id` ascending.
   - Set `w.domainContent` to the marshaled JSON bytes so the tool invocation witness retains the exact committed admission payload.

---

## 3. Detailed Implementation Steps

### Phase 1: Store Layer Updates (`internal/store`)
1. Create or update `ListEmbeddedToolWorkers` in `router-proxy/internal/store/embedded_tool_admission.go`:
   - Query embedded workers and tmux workers from SQLite.
   - Return structured list of records (e.g. `[]ToolWorkerItem` or enhanced `ManagedAgentRecord` with `Runtime`).
   - Filter reserved identities: `'system'`, `'reminders'`, `'router'`, `'user'`, `'supervisor'`, `'web_ui'`, `'managed-runtime-readiness'`, `'reflex-router-proxy-launcher'`.
   - Marshal domain payload containing `worker_id`, `lifecycle_state`, `desired_state`, `kind`.

### Phase 2: Live Window Integration & Manager Layer (`internal/managed`)
1. In `router-proxy/internal/managed/supervisor.go` and `config.go`:
   - Add optional `TmuxWindowLister` to `ManagerOptions` and `Manager`.
2. In `router-proxy/internal/managed/embedded_addressed_tools.go`:
   - When executing `action == "list"`:
     - Invoke `m.store.ListEmbeddedToolWorkers(ctx, invocation)`.
     - If `m.tmuxLister != nil`, query windows in `"mecha"`, merge any additional starting windows, and update domain content if necessary, or pass the lister into the store invocation flow.

### Phase 3: Wiring in Mailbox Router (`cmd/mailbox-router` & `internal/transport`)
1. In `router-proxy/cmd/mailbox-router/main.go`:
   - Pass the existing `tmuxAdapter` into `managed.ManagerOptions{TmuxLister: ...}`.
2. In `router-proxy/internal/transport/router.go`:
   - Ensure test helpers and options forward the tmux adapter to `managed.Manager` where appropriate.

---

## 4. Testing & Regression Prevention Plan

Per the dev-loop requirements, integration tests that prevent regressions are **mandatory** and non-negotiable.

### 4.1 Unit Tests (`router-proxy/internal/store/embedded_tool_admission_test.go`)
- **`TestEmbeddedToolListWorkersIncludesTmuxRecipients`**:
  - Insert an embedded worker in `managed_agents` + `embedded_sessions`.
  - Insert active tmux recipient in `recipient_sources` with an active lease in `agent_leases`.
  - Insert idle tmux recipient in `recipient_sources` with no lease (or expired lease).
  - Insert reserved recipient (e.g., `supervisor`, `reminders`, `web_ui`) in `recipient_sources`.
  - Invoke `Store.ListEmbeddedToolWorkers`.
  - Assertions:
    - Embedded worker is present with `kind: "embedded"`, `lifecycle_state: "running"`.
    - Active tmux worker is present with `kind: "tmux"`, `lifecycle_state: "running"`.
    - Idle tmux worker is present with `kind: "tmux"`, `lifecycle_state: "idle"`.
    - Reserved recipients are completely excluded.
    - JSON payload matches `{ "workers": [...] }` schema and contains `kind`.

### 4.2 Integration Tests (`router-proxy/internal/managed/embedded_addressed_tools_test.go` or `router-proxy/internal/transport/embedded_worker_list_integration_test.go`)
- **`TestEmbeddedWorkerListActionIntegration`**:
  - Setup a full within-service test environment with a running `Manager` and `Store`.
  - Register an active embedded worker.
  - Register a connected tmux worker with active lease.
  - Provide a mock `TmuxWindowLister` that returns a newly created tmux window `"worker-starting"` in session `"mecha"`.
  - Execute the `mecha_worker` tool with `action: "list"` through the real agent tool execution path (`agent.AgentTool.Execute`).
  - **Regression guarded against**:
    - Ensures that tool execution end-to-end yields all three workers (embedded, connected tmux, starting tmux window).
    - Ensures that tool result JSON is parseable by agents and correctly contains all required fields (`worker_id`, `lifecycle_state`, `desired_state`, `kind`).
    - Verifies that if `TmuxWindowLister` fails or returns an error, the call does not crash or fail, but successfully returns the database-discovered workers.

---

## 5. Dev-Loop Verification Checklist

- [ ] Write failing unit test in `embedded_tool_admission_test.go` (TDD).
- [ ] Implement database queries and schema updates in `embedded_tool_admission.go`.
- [ ] Write failing integration test in `embedded_worker_list_integration_test.go` (TDD).
- [ ] Implement live tmux window merging in `managed`.
- [ ] Run `go test -v ./router-proxy/internal/store/...` and `go test -v ./router-proxy/internal/managed/...`.
- [ ] Run `test-coverage` to ensure no coverage gaps.
- [ ] Run `robot-review` and `pr-validate` rounds.
