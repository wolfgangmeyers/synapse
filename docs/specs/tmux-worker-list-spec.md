# Specification: Unified Worker Discovery for Embedded and Tmux Workers in `mecha_worker action="list"`

**Specification ID:** SPEC-TMUX-WORKER-LIST-001  
**Status:** DRAFT (Dev-Loop Phase 1)  
**Authors:** Wolfgang Meyers, Mecha Core Team  
**Date:** October 2026  
**Target Delivery:** Router Go Backend (`router-proxy/internal/store`, `router-proxy/internal/managed`, `router-proxy/internal/transport`)

---

## 1. Problem

Currently, the tool handler for `mecha_worker(action="list")` (implemented via `Store.ListEmbeddedToolWorkers` in `router-proxy/internal/store/embedded_tool_admission.go` and wired through `embedded_addressed_tools.go` in package `managed`) strictly queries SQLite `managed_agents`:
```sql
SELECT ... FROM managed_agents
WHERE runtime='embedded'
  AND EXISTS (
    SELECT 1 FROM embedded_sessions e
    WHERE e.agent_id=managed_agents.agent_id
      AND e.session_id=managed_agents.pi_session_id
      AND e.epoch=managed_agents.session_epoch
      AND e.generation=managed_agents.runtime_generation
      AND e.current=1
      AND e.role='worker'
  )
ORDER BY name
```

This implementation causes critical operational blindness:
1. **Tmux workers are completely invisible**: Workers executing in the `mecha` tmux session (e.g. pi/claude/codex/cursor instances launched in tmux windows, connected to `/v1/agents` over WebSocket, or registered in `recipient_sources` with `source_kind = 'tmux'`) are entirely omitted from the output.
2. **Supervisors cannot manage mixed agent pools**: As the operator's agent system operates with embedded workers by default while keeping tmux workers active for backup or specialized roles, any supervisor or coordinator invoking `mecha_worker(action="list")` fails to discover existing tmux worker windows or connected tmux agents.
3. **No discriminator between runtimes**: Callers receiving the worker list have no `kind` or `runtime` discriminator to determine whether a worker is an in-process embedded worker or a tmux-based worker.

---

## 2. Requirements

### 2.1 Schema & Backwards-Compatibility (REQ-1)
1. The JSON tool result payload format returned to the calling agent must preserve full backwards compatibility:
   - Root JSON object: `{"workers": [...]}`.
   - Each item in `workers` must include:
     - `worker_id` (string): The agent / worker identity.
     - `lifecycle_state` (string): Current lifecycle state (`"running"`, `"idle"`, `"starting"`, or `"disconnected"`).
     - `desired_state` (string): Desired lifecycle state (`"running"` or `"stopped"`).
2. Each worker object must additionally include a `kind` field:
   - Values: `"embedded"` | `"tmux"`.
   - Enables calling agents to unambiguously distinguish embedded workers from tmux workers.

### 2.2 Tmux Worker Discovery from Database (REQ-2)
1. Discover tmux workers durably registered or connected to the router in SQLite:
   - Query `recipient_sources` where:
     - `source_kind = 'tmux'`
     - `state IN ('active', 'reserved')`
   - Filter out reserved system/infrastructure identities, including but not limited to:
     - `'system'`
     - `'reminders'`
     - `'router'`
     - `'user'`
     - `'supervisor'`
     - `'web_ui'`
     - `'managed-runtime-readiness'`
     - `'reflex-router-proxy-launcher'`
2. Determine lifecycle and desired state from lease and agent status:
   - Inspect `agent_leases` and `agents` for each discovered tmux recipient:
     - If an active lease exists whose expiration timestamp (`expires_at`) is greater than current store clock (`now`):
       - `lifecycle_state: "running"`
       - `desired_state: "running"`
     - If no active lease exists, or the lease expiration timestamp (`expires_at`) is less than or equal to current store clock (`now`):
       - If `agents.disconnected_at IS NOT NULL`:
         - `lifecycle_state: "disconnected"`
         - `desired_state: "running"`
       - Otherwise:
         - `lifecycle_state: "idle"`
         - `desired_state: "running"`
   - Pure read-only semantics: the listing query must NOT execute passive side-effect deletions or modifications of expired leases in `agent_leases` or timestamps in `agents`.

### 2.3 Live Tmux Window Discovery via TmuxAdapter (REQ-3)
1. When a live `TmuxAdapter` / `TmuxWindowLister` is wired into the manager / execution path:
   - Query live windows in the `mecha` tmux session (or configured audit tmux session).
   - Filter out known non-worker windows (e.g., `supervisor`, `webui`, `web_ui`, `router`).
   - If a tmux window exists in the session but has not yet connected or acquired a lease:
     - If already present in `recipient_sources`, the database state (with active lease check) takes precedence.
     - If not yet connected/registered in SQLite:
       - `worker_id`: window name
       - `kind: "tmux"`
       - `lifecycle_state: "starting"`
       - `desired_state: "running"`
2. Graceful degradation:
   - If `TmuxWindowLister` is nil, unconfigured, times out, or returns an error (e.g. `ErrTmuxUnavailable`, session not found, or exit status 1):
     - Log a warning or trace event.
     - Fall back gracefully to database-discovered workers without returning an error to the tool caller.

### 2.4 Worker Union, Deduplication and Admission Witness (REQ-4)
1. Embedded workers, database tmux workers, and live tmux window workers must be unified into a single coherent list.
2. Deduplication and collision rules:
   - In the event an identity collision occurs between embedded and tmux sources (e.g. same agent name), embedded worker records take precedence.
   - For tmux workers, a database record with an active unexpired lease takes precedence over live starting/unconnected tmux window records.
3. Witness content attestation:
   - The unified worker list JSON bytes must be set directly on `w.domainContent` within the execution witness path before completing the tool invocation.
   - This ensures the exact committed content matches what `CompleteEmbeddedToolInvocation` and `AppendEmbeddedToolInvocationResult` expect without failing witness verification.
4. Ordering:
   - The final worker list must be deterministically sorted by `worker_id` ascending.

### 2.5 Security, Witness Fence & Isolation (REQ-5)
1. The tool execution must preserve the strict one-use `EmbeddedToolInvocation` fence witness and transactional integrity in `embedded_tool_admission.go`.
2. Discovery of tmux workers must be strictly read-only and must never mutate leases, messages, agents, or recipient registrations during a list operation.
3. System identities, private paths, internal session IDs, process PIDs, and credentials must not be leaked into the tool output.

---

## 3. Constraints

1. **No Breaking Schema Changes**: Existing LLM prompts and tools expect `workers: [{worker_id, lifecycle_state, desired_state}]`. Adding `kind` is non-breaking. Existing fields must remain present with valid strings.
2. **Performance & Non-Blocking**: Tmux process listing via CLI (`tmux list-windows`) must be non-blocking with an appropriate timeout; failures or absence of tmux must degrade gracefully to database-only discovery without failing the `mecha_worker(action="list")` call.
3. **No External Dependencies**: Tmux interaction uses the existing internal `TmuxAdapter` interface (`transport.TmuxAdapter`).
4. **Deterministic Testing**: All unit and behavioral integration tests must run without requiring a real external tmux server or live LLM invocations, using mocks and deterministic clocks.

---

## 4. Acceptance Criteria

- [ ] **AC-1**: `Store.ListEmbeddedToolWorkers` (or unified worker lister) queries both embedded workers from `managed_agents` and tmux workers from `recipient_sources` / `agent_leases`.
- [ ] **AC-2**: Output JSON contains `workers` array where each worker object contains `worker_id`, `lifecycle_state`, `desired_state`, and `kind` (`"embedded"` | `"tmux"`).
- [ ] **AC-3**: Reserved/system identities (`supervisor`, `system`, `reminders`, `router`, `user`, `web_ui`, etc.) are excluded from worker listings.
- [ ] **AC-4**: A tmux worker with an active, unexpired lease is reported with `lifecycle_state: "running"`, `desired_state: "running"`.
- [ ] **AC-5**: A tmux worker with no active lease or an expired lease is reported with `lifecycle_state: "idle"` or `"disconnected"`.
- [ ] **AC-6**: When `TmuxAdapter` is available, unattached/starting windows in session `mecha` are discovered and reported as `lifecycle_state: "starting"` or `"idle"`.
- [ ] **AC-7**: Unit tests in `router-proxy/internal/store/embedded_tool_admission_test.go` verify DB-level querying, filtering of system identities, lease join, and JSON output structure.
- [ ] **AC-8**: Behavioral functional integration tests verify end-to-end execution of `mecha_worker(action="list")` when both embedded workers and tmux workers are present.
