# Plan: Scratchpad Entry Truncation Tool & Storage

## 1. Problem Statement & User Requirements

### Problem Statement
Long-lived managed agents accumulate thousands of scratchpad entries over extended operation. During context resets, session reboots, and inspections, this unconstrained growth leads to:
- Excessive database row accumulation.
- Slow query response times when fetching or paginating scratchpad notes.
- Inability to clean up stale notes without issuing hundreds of individual batch `delete` tool calls with explicit IDs.

### User Requirements
> "I have really old agents that have accumulated thousands of scratchpad entries. We need a way to truncate old entries, maybe beyond a certain entry id. But pinned entries would be excluded from those that are cleared. We need a tool for this."

Key requirements:
1. **Truncate / Prune Action**: Provide an operation on the `scratchpad` tool (`action: "truncate"`) to truncate older unpinned entries.
2. **Pinned Entry Exclusion**: Pinned entries (`pinned = 1`) must **never** be deleted by truncation under any circumstances, preserving critical user decisions, milestones, and architectural notes.
3. **Threshold Boundary & Mutual Exclusivity**: Exactly one threshold parameter must be provided:
   - `before_id`: Truncate unpinned entries created strictly before the specified entry ID's timestamp. The entry `before_id` itself (pinned or unpinned) is never deleted.
   - `keep_last`: Retain only the $N$ most recent unpinned entries (`keep_last > 0`) and delete all older unpinned entries.
   - Mutual exclusivity is strictly enforced: providing both or neither is rejected with an error.
4. **Agent Isolation**: The embedded tool strictly operates on the calling agent's scratchpad. No cross-agent targeting (`target_agent_id`) in the tool schema; agents cannot touch other agents' scratchpads. Supervisor/administrative tooling can target specific agents via the router HTTP endpoint.
5. **Durable Reporting**: Return the exact count of deleted entries to the calling model and HTTP caller.
6. **HTTP API Parity**: Provide a corresponding REST endpoint on the router (`POST /v1/managed-agents/{id}/scratchpad/truncate`) for scripts, supervisor automation, or headless callers.
7. **Compliance**: Adhere to `docs/behavioral-specification-standard.md` by writing a formal behavioral specification, Go unit/integration tests, and E2E test coverage.

---

## 2. Architecture & Codebase Findings

### 2.1 Storage & Schema (`scratchpad_entries`)
- **Table Definition** (`router-proxy/internal/store/migrations/089_scratchpad.sql`):
  ```sql
  CREATE TABLE IF NOT EXISTS scratchpad_entries (
      id TEXT PRIMARY KEY,
      agent_id TEXT NOT NULL,
      content TEXT NOT NULL,
      pinned INTEGER NOT NULL DEFAULT 0 CHECK (pinned IN (0, 1)),
      created_at TEXT NOT NULL,
      updated_at TEXT NOT NULL
  );

  CREATE INDEX IF NOT EXISTS idx_scratchpad_agent_created ON scratchpad_entries(agent_id, created_at DESC);
  CREATE INDEX IF NOT EXISTS idx_scratchpad_agent_pinned ON scratchpad_entries(agent_id, pinned, created_at DESC);
  ```
- **ID Representation**: `id` is a `TEXT` field (UUID or deterministic ID string, e.g., generated via `s.ids.Next()` or `uuid.New().String()`). It is **not** an auto-incrementing integer.
- **Ordering**: Chronological order is defined by `created_at` timestamp string (ISO-8601 UTC `YYYY-MM-DDTHH:MM:SS.sssZ`). Therefore, comparing entries "before" a given `before_id` requires referencing the target entry's `created_at` timestamp.

### 2.2 Tool Registration & Runtimes
- `scratchpad` is an embedded agent tool implemented in Go (`router-proxy/internal/managed/embedded_scratchpad.go`), registered into embedded agent sessions in `embedded_session.go` and `registerEmbeddedTools()`.
- Runtimes (`pi-extensions/mecha-messaging`): Pi messaging extensions reference scratchpad for context threshold guidance (`compaction-policy.ts` and `mecha_context`), while the actual tool execution is handled natively by the Go router session engine.
- Existing actions on `scratchpad`:
  `['append', 'fetch', 'paginate', 'search', 'pin', 'unpin', 'delete']`

### 2.3 Router Transport Endpoints
- `GET /v1/managed-agents/{id}/scratchpad` exists in `router-proxy/internal/transport/managed_http.go`.
- Currently, no HTTP endpoint exists for mutating or truncating scratchpad entries outside of model tool invocations.

---

## 3. Detailed Design

### 3.1 Store Layer (`router-proxy/internal/store/scratchpad.go`)

Add a new method `TruncateScratchpadEntries`:

```go
type ScratchpadTruncateOptions struct {
    // BeforeID: Truncate unpinned entries created strictly before this entry's created_at.
    // If BeforeID is provided, the entry must exist and belong to the specified agent.
    BeforeID string `json:"before_id,omitempty"`

    // KeepLast: Keep the N most recent unpinned entries and delete all older unpinned entries.
    // Must be greater than 0 if provided.
    KeepLast int `json:"keep_last,omitempty"`
}

// TruncateScratchpadEntries deletes unpinned entries for an agent based on BeforeID or KeepLast.
// Mutually exclusive: exactly one of BeforeID or KeepLast (> 0) must be specified.
// Pinned entries (pinned = 1) are never deleted under any condition.
// The entry specified by BeforeID is never deleted (whether pinned or unpinned).
// Returns the number of entries deleted.
func (s *Store) TruncateScratchpadEntries(ctx context.Context, agentID string, opts ScratchpadTruncateOptions) (int, error)
```

#### SQL Implementation Details:
1. **Validation & Mutual Exclusivity**:
   - `agentID` must not be empty.
   - Enforce strictly one option:
     - Reject if `opts.BeforeID != ""` AND `opts.KeepLast > 0` ("cannot specify both before_id and keep_last").
     - Reject if `opts.BeforeID == ""` AND `opts.KeepLast <= 0` ("must specify either before_id or keep_last greater than 0").
     - Reject if `opts.KeepLast < 0` ("keep_last must be greater than 0").
2. **When `BeforeID` is specified**:
   - Look up the entry and its cutoff timestamp, ensuring it belongs to `agentID`:
     ```sql
     SELECT created_at FROM scratchpad_entries WHERE agent_id = ? AND id = ?
     ```
     If not found, return an error (`scratchpad entry not found for agent`).
   - Execute deletion:
     ```sql
     DELETE FROM scratchpad_entries
     WHERE agent_id = ? AND pinned = 0 AND created_at < ?
     ```
   - **Semantics**:
     - The entry `before_id` itself is **never deleted**, regardless of whether it is pinned or unpinned (since `created_at < cutoff` is strictly less than).
     - Pinned entries are never deleted (`pinned = 0`).
     - Any entries created strictly prior to the `before_id` entry's timestamp are deleted.
3. **When `KeepLast` is specified**:
   - Execute atomic single-query deletion retaining the $N$ newest unpinned entries:
     ```sql
     DELETE FROM scratchpad_entries
     WHERE agent_id = ? AND pinned = 0
       AND id NOT IN (
           SELECT id FROM scratchpad_entries
           WHERE agent_id = ? AND pinned = 0
           ORDER BY created_at DESC
           LIMIT ?
       )
     ```
   - If the agent has fewer than or equal to `KeepLast` unpinned entries, zero rows are deleted.
   - Pinned entries are excluded from both the subquery and outer delete (`pinned = 0`), preserving all pinned entries completely.
4. **Return**:
   - `res.RowsAffected()` returns the exact count of deleted rows.

---

### 3.2 Tool Definition & Schema (`router-proxy/internal/managed/embedded_scratchpad.go`)

Update `embeddedScratchpadSchema()`:
- Add `"truncate"` to the `action` enum:
  `ai.Prop("action", ai.EnumOf("append", "fetch", "paginate", "search", "pin", "unpin", "delete", "truncate"))`
- Add parameters:
  - `ai.Opt("before_id", ai.String("Scratchpad entry ID before which all older unpinned entries are deleted. The entry itself is not deleted. Mutually exclusive with keep_last."))`
  - `ai.Opt("keep_last", ai.Integer("Number of most recent unpinned scratchpad entries to retain (must be > 0); older unpinned entries are deleted. Mutually exclusive with before_id."))`
- **Agent Isolation**: Do **not** include `target_agent_id`. The tool strictly acts on the current agent session's `agentID`.

Update tool description:
- `"Persistent scratchpad memory for notes, milestones, and context across session resets. Supports append, fetch (recent + pinned), paginate, search, pin, unpin, delete, and truncate (prune older unpinned entries)."`

Update handler `Execute`:
```go
case "truncate":
    beforeID, _ := args["before_id"].(string)
    beforeID = strings.TrimSpace(beforeID)

    hasKeepLast := false
    keepLast := 0
    if val, ok := args["keep_last"]; ok && val != nil {
        hasKeepLast = true
        switch v := val.(type) {
        case int:
            keepLast = v
        case float64:
            keepLast = int(v)
        case int64:
            keepLast = int(v)
        }
    }

    // Enforce mutual exclusivity
    if (beforeID != "" && hasKeepLast) || (beforeID == "" && !hasKeepLast) {
        return agent.AgentToolResult{}, errors.New("exactly one of before_id or keep_last must be specified for truncate")
    }
    if hasKeepLast && keepLast <= 0 {
        return agent.AgentToolResult{}, errors.New("keep_last must be greater than 0")
    }

    deletedCount, err := m.store.TruncateScratchpadEntries(ctx, agentID, store.ScratchpadTruncateOptions{
        BeforeID: beforeID,
        KeepLast: keepLast,
    })
    if err != nil {
        return agent.AgentToolResult{}, fmt.Errorf("truncate scratchpad: %w", err)
    }

    output := fmt.Sprintf("Truncated scratchpad: %d unpinned entries deleted (pinned entries preserved).", deletedCount)
    return agent.AgentToolResult{
        Content: ai.ContentList{
            ai.TextContent{Text: output},
        },
    }, nil
```

---

### 3.3 HTTP Endpoint (`router-proxy/internal/transport/managed_http.go`)

Add REST endpoint for administrative/supervisor automation:
- **Route**: `POST /v1/managed-agents/{id}/scratchpad/truncate`
- **Request Body**:
  ```json
  {
    "before_id": "optional-entry-uuid",
    "keep_last": 20
  }
  ```
  *(Exactly one of `before_id` or `keep_last` must be provided; `keep_last` must be > 0).*
- **Response** (`200 OK`):
  ```json
  {
    "agent_id": "worker-1",
    "deleted_count": 450,
    "pinned_preserved": true
  }
  ```
- **Error Responses**:
  - `400 BAD_REQUEST`:
    - Neither `before_id` nor `keep_last` provided.
    - Both `before_id` and `keep_last` provided.
    - `keep_last <= 0`.
  - `404 NOT_FOUND`:
    - Managed agent not found.
    - `before_id` was specified but does not exist for this agent.

---

## 4. Behavioral Specification (`docs/specs/scratchpad-truncation.spec.md`)

In accordance with `docs/behavioral-specification-standard.md`:
- Spec ID: `SPEC-SCRATCHPAD-001`
- Domain: `AgentManagement`
- Status: `Draft` (moving to `Active` upon test implementation)
- Target E2E Suite: `tests/e2e/scratchpad-truncation.spec.ts`

### Scenarios to specify:
1. **SCN-SP-001 (Happy Path - Truncate by `before_id`)**:
   - *Given*: Agent has 10 unpinned entries (E1..E10) and 2 pinned entries (P1, P2) created in chronological order.
   - *When*: Scratchpad tool is called with `action: "truncate", before_id: E6.id`.
   - *Then*: Entries E1 through E5 (unpinned) are deleted; entry E6 itself, E7..E10, and pinned entries P1, P2 remain intact. Return message reports 5 deleted entries.
2. **SCN-SP-002 (Happy Path - Truncate by `keep_last`)**:
   - *Given*: Agent has 100 unpinned entries and 5 pinned entries.
   - *When*: Scratchpad tool is called with `action: "truncate", keep_last: 20`.
   - *Then*: The oldest 80 unpinned entries are deleted via single SQL query; the 20 most recent unpinned entries and all 5 pinned entries remain. Return message reports 80 deleted entries.
3. **SCN-SP-003 (Safety - Pinned entries never deleted)**:
   - *Given*: Agent has pinned entries created before, during, and after `before_id`.
   - *When*: Truncation is executed with `before_id` or `keep_last`.
   - *Then*: None of the pinned entries are deleted, even if their creation timestamps precede the cutoff or exceed `keep_last`.
4. **SCN-SP-004 (Edge Case - Non-existent or Cross-Agent `before_id`)**:
   - *Given*: Scratchpad tool is called with a non-existent `before_id`, or an entry ID belonging to a different agent.
   - *When*: Tool executes.
   - *Then*: Tool returns an error indicating the entry was not found for the agent; zero entries are deleted.
5. **SCN-SP-005 (Validation - Mutual Exclusivity & Arguments)**:
   - *Given*: Scratchpad tool is called with:
     - Both `before_id` and `keep_last`, OR
     - Neither `before_id` nor `keep_last`, OR
     - `keep_last: 0` or negative.
   - *When*: Tool executes.
   - *Then*: Tool returns a clear validation error without modifying the database.
6. **SCN-SP-006 (Agent Isolation in Embedded Tool)**:
   - *Given*: Agent A invokes `scratchpad` tool with `action: "truncate"`.
   - *When*: Tool executes.
   - *Then*: Only Agent A's entries are truncated; no parameter allows Agent A to target Agent B.
7. **SCN-SP-007 (HTTP API Truncation)**:
   - *Given*: Router running with managed agent.
   - *When*: Caller sends `POST /v1/managed-agents/{id}/scratchpad/truncate` with valid `keep_last: 10`.
   - *Then*: Oldest unpinned entries beyond the 10 newest are deleted, returning `{"agent_id": "...", "deleted_count": N, "pinned_preserved": true}` with HTTP 200.

---

## 5. Test Plan

### 5.1 Go Store Tests (`router-proxy/internal/store/scratchpad_test.go`)
- `TestScratchpadTruncate_BeforeID`:
  - Insert 10 unpinned entries and 3 pinned entries.
  - Pick entry 6 as `before_id` (test both unpinned and pinned entry as `before_id`).
  - Verify entries 1..5 deleted.
  - Verify entry 6 is **never** deleted.
  - Verify entries 7..10 and all 3 pinned entries are preserved.
- `TestScratchpadTruncate_KeepLast_SingleQuery`:
  - Insert 25 unpinned entries and 4 pinned entries.
  - Truncate with `keep_last: 10`.
  - Verify oldest 15 unpinned entries deleted, 10 newest unpinned + 4 pinned preserved.
  - Verify that when unpinned count <= `keep_last`, 0 entries are deleted.
- `TestScratchpadTruncate_MutualExclusivity`:
  - Verify error returned when both `before_id` and `keep_last` provided.
  - Verify error returned when neither provided.
  - Verify error returned when `keep_last <= 0`.
- `TestScratchpadTruncate_AgentIsolationAndMissingEntry`:
  - Create entries for Agent A and Agent B.
  - Passing Agent B's entry ID as `before_id` for Agent A fails with entry not found error.
  - Truncating Agent A's entries never affects Agent B's entries.

### 5.2 Go Managed Tool Tests (`router-proxy/internal/managed/embedded_scratchpad_test.go`)
- `TestEmbeddedScratchpadTruncateTool`:
  - Verify tool execution for `action: "truncate"` with `keep_last`.
  - Verify tool execution for `action: "truncate"` with `before_id`.
  - Verify tool fails on mutual exclusivity violation (both or neither).
  - Verify tool schema contains only `before_id` and `keep_last` (no `target_agent_id`).
  - Verify tool execution is strictly isolated to the caller's `agentID`.

### 5.3 Go HTTP Transport Tests (`router-proxy/internal/transport/managed_scratchpad_http_test.go`)
- `TestManagedScratchpadTruncatePOST`:
  - POST to `/v1/managed-agents/{id}/scratchpad/truncate` with valid `keep_last` -> HTTP 200 with deleted count.
  - POST with valid `before_id` -> HTTP 200 with deleted count.
  - POST with both `before_id` and `keep_last` -> HTTP 400 Bad Request.
  - POST with neither -> HTTP 400 Bad Request.
  - POST with invalid agent ID -> HTTP 404 Not Found.
  - POST with cross-agent or nonexistent `before_id` -> HTTP 404 Not Found.

### 5.4 Fast E2E Tests (`tests/e2e/scratchpad-truncation.spec.ts`)
- Stand up test router with mock LLM harness.
- Populate agent with mock scratchpad entries (unpinned and pinned).
- Run agent turn calling `scratchpad` tool with `action: "truncate", keep_last: 10`.
- Verify turn result reflects deleted count and agent scratchpad REST query confirms pinned entries preserved while unpinned entries pruned.
- Exercise `POST /v1/managed-agents/{id}/scratchpad/truncate` endpoint end-to-end.

---

## 6. Non-Goals & Overengineering Avoidance

1. **No Web UI Frontend Scope**:
   Web UI changes in `web/router-ui-react` (such as `ScratchpadViewer.tsx` toolbar buttons or new React components) are explicitly out of scope for this plan. Scope is strictly store, router HTTP endpoint, embedded tool, specification, and automated tests.
2. **No Cross-Agent Targeting in Embedded Tool**:
   The embedded `scratchpad` tool must not allow agents to truncate other agents' scratchpads (`target_agent_id` is excluded from tool schema). Isolation is enforced at the session boundary.
3. **No New Standalone Tool**:
   Do **not** introduce a separate `scratchpad_truncate` tool. Expanding the existing `scratchpad` tool (`action: "truncate"`) keeps the tool manifest compact and intuitive for LLMs, matching existing actions (`append`, `fetch`, `paginate`, `search`, `pin`, `unpin`, `delete`).
4. **No Complex Schema Migrations**:
   The existing `scratchpad_entries` table already has `pinned`, `created_at`, `agent_id`, and indexes on `(agent_id, created_at DESC)` and `(agent_id, pinned, created_at DESC)`. No database migrations or schema alterations are needed.
5. **No Automatic Background Pruner**:
   Do not introduce background cron jobs or periodic sweep daemons that silently delete scratchpad entries without agent/user direction. Truncation must be an explicit, deterministic operation triggered by the agent or supervisor.
6. **Simple Clean Boundary**:
   Keep the interface minimal: mutually exclusive `before_id` and `keep_last`. Avoid adding complex regex or full-text query filters to truncation.
