# Implementation Plan: Worker Progress Tracking

## 1. Reference & Context
- **Specification**: `PROGRESS_TRACKING_SPEC.md`
- **Product Requirements**: `PROGRESS_TRACKING_PRD.md`
- **System Operating Policy**: `docs/wolfgang-agent-operating-policy.md`
- **Canonical dev-loop workflow**: Specification/Plan -> Implementation -> Validation -> Review -> Integration.

---

## 2. Architecture & Key Design Decisions

### 2.1 Database Schema & Migrations (`agent_progress_history`)
- **Migration Script**: `router-proxy/internal/store/migrations/106_agent_progress_history.sql` (or next sequential index).
- **Table Definition**:
  ```sql
  CREATE TABLE IF NOT EXISTS agent_progress_history (
      id INTEGER PRIMARY KEY AUTOINCREMENT,
      agent_id TEXT NOT NULL,
      percent INTEGER NOT NULL CHECK (percent >= 0 AND percent <= 100),
      note TEXT NULL,
      created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
  );

  CREATE INDEX IF NOT EXISTS idx_agent_progress_history_agent_id_created 
  ON agent_progress_history (agent_id, id ASC);
  ```
- **Managed Agents State Extension**:
  - In `managed_agents` table or session metadata table, store current progress snapshot:
    - `progress_percent INTEGER NULL CHECK (progress_percent IS NULL OR (progress_percent >= 0 AND progress_percent <= 100))`
    - `progress_note TEXT NULL`
    - `progress_updated_at TEXT NULL`
  - Alternatively, if `managed_agents` projection derives progress, add columns to `managed_agents` via migration or derive from an active snapshot table `agent_current_progress (agent_id TEXT PRIMARY KEY, percent INT, note TEXT, updated_at TEXT)`. Using an explicit `agent_current_progress` table or columns on `managed_agents` keeps context resets independent of progress.
- **Bounded Retention FIFO Policy**:
  - Enforce FIFO ring-buffer behavior capped at 1,000 entries per agent session.
  - On inserting a new entry:
    ```sql
    DELETE FROM agent_progress_history
    WHERE id IN (
        SELECT id FROM agent_progress_history
        WHERE agent_id = ?
        ORDER BY id DESC
        LIMIT -1 OFFSET 1000
    );
    ```
  - Executed within the same SQLite transaction as `SetAgentProgress`.

### 2.2 Store Access Layer (`internal/store`)
- **Data Models**:
  ```go
  type AgentProgressSnapshot struct {
      Percent   int       `json:"percent"`
      Note      *string   `json:"note"`
      UpdatedAt time.Time `json:"updated_at"`
  }

  type AgentProgressHistoryEntry struct {
      Percent   int       `json:"percent"`
      Note      *string   `json:"note"`
      Timestamp time.Time `json:"timestamp"`
  }
  ```
- **Methods on `*Store`**:
  - `SetAgentProgress(ctx context.Context, agentID string, percent int, note *string) (AgentProgressSnapshot, error)`:
    - Validates inputs (`0 <= percent <= 100`, `note == nil || len(*note) <= 120`).
    - Begins transaction.
    - Upserts current progress snapshot record with `time.Now().UTC()`.
    - Appends record to `agent_progress_history`.
    - Prunes entries beyond the 1,000 entry limit for `agentID` via FIFO deletion.
    - Commits transaction and returns snapshot.
  - `ClearAgentProgress(ctx context.Context, agentID string) error`:
    - Resets current progress snapshot record to `null` (deletes from current snapshot table or clears columns).
    - Preserves all records in `agent_progress_history` (history audit trail is retained until agent deletion).
  - `GetAgentProgress(ctx context.Context, agentID string) (*AgentProgressSnapshot, int, error)`:
    - Returns current snapshot (or `nil` if unset) and the count of retained history entries in `agent_progress_history`.
  - `GetAgentProgressHistory(ctx context.Context, agentID string) ([]AgentProgressHistoryEntry, error)`:
    - Verifies agent session exists; returns `ErrAgentNotFound` if absent.
    - Queries `agent_progress_history` ordered by `id ASC` (chronological order).
    - Returns empty slice `[]AgentProgressHistoryEntry{}` if agent exists but has 0 records.
  - `DeleteAgentProgressSession(ctx context.Context, agentID string) error`:
    - Cascades cleanup of snapshot and history when an agent session is permanently deleted.

### 2.3 Tool Implementation (`embedded_progress_tool.go`)
- **Tool Name**: `mecha_progress`
- **Location**: `router-proxy/internal/managed/embedded_progress_tool.go`
- **Roles**: Registered for embedded workers and root agents (`role == "worker"` and `role == "root"`).
- **JSON Schema (`ai.Schema`)**:
  - `action`: `ai.EnumOf("set", "get", "clear")` (required).
  - `percent`: `ai.Opt(ai.Integer())`
  - `note`: `ai.Opt(ai.String())`
  - `AdditionalAllowed = false`
- **Input Validation**:
  - For `set`:
    - `percent` is required. Must be whole integer between `0` and `100`. Reject strings, floats, `< 0`, `> 100` with descriptive error.
    - Non-monotonic values are accepted without rejection (e.g. from 60 back to 40).
    - `note` is optional. If present, must not exceed 120 UTF-8 characters. Do not silently trim; reject with error if `len > 120`.
  - For `get` and `clear`:
    - Must not supply unexpected arguments or invalid types.
- **Return Payloads**:
  - `set`: `{"ok": true, "percent": N, "note": "...", "updated_at": "RFC3339"}`
  - `get` (active): `{"ok": true, "percent": N, "note": "...", "updated_at": "RFC3339", "count": C}`
  - `get` (unset): `{"ok": true, "percent": null, "note": null, "updated_at": null, "count": 0}`
  - `clear`: `{"ok": true, "cleared": true}`
- **Live Event Dispatch**:
  - On `set` or `clear`, invoke `m.broadcastAgentProgress(agentID, snapshot)` or push updated projection through existing WebSocket event broadcast channels (`agent_progress` frame and agent state update).

### 2.4 HTTP Transport Handlers (`internal/transport`)
- **Routes in `http.go`**:
  - `GET /v1/agents/{id}/progress/history`
  - `GET /v1/agents/{id}/progress/chart.png`
- **Handler Logic**:
  - Extract agent `{id}` from path.
  - Verify agent existence via Store.
  - **404 Handling**: If agent does not exist, return HTTP 404 with JSON:
    ```json
    {"error": "agent not found"}
    ```
  - **History Handler (`GET /v1/agents/{id}/progress/history`)**:
    - Returns HTTP 200 with `application/json`.
    - Returns chronological array of `{ "percent": int, "note": string|null, "timestamp": string }`.
    - Returns `[]` if agent has 0 history entries.
  - **Chart PNG Handler (`GET /v1/agents/{id}/progress/chart.png`)**:
    - Returns HTTP 200 with `Content-Type: image/png` and `Cache-Control: no-cache`.
    - Fetches history slice from store.
    - If history is empty (0 entries), invokes renderer to generate dark-theme placeholder PNG with centered "No progress recorded".
    - If history has entries (1 to 1,000), invokes renderer with slice.

### 2.5 Pure Go Server-Side PNG Chart Renderer (`internal/managed/chart`)
- **Package**: `router-proxy/internal/managed/chart` (or `internal/chart`)
- **Pure Go Standard Library & Sub-repo**:
  - Uses `image`, `image/color`, `image/draw`, `image/png`.
  - Font rendering via `golang.org/x/image/font/basicfont` (`basicfont.Face7x13`).
  - No Cgo, no ImageMagick, no external CLI processes, zero outside binaries.
- **Dimensions & Theme**:
  - Image size: 800 x 400 pixels (`image.NewRGBA(image.Rect(0, 0, 800, 400))`).
  - Dark Theme Palette:
    - Background: Dark slate/charcoal `#18181b` (RGB 24, 24, 27).
    - Grid lines: Subtle `#27272a` (RGB 39, 39, 42).
    - Axes & borders: Muted gray `#52525b` (RGB 82, 82, 91).
    - Labels & text: Light gray/white `#f4f4f5` (RGB 244, 244, 245) or `#a1a1aa`.
    - Chart line: Vibrant accent `#3b82f6` (blue) or `#10b981` (emerald).
    - Data points: Filled circular dots with white center and accent outline.
- **Rendering Elements**:
  - Padding: Left 60px (Y-axis labels), Bottom 40px (X-axis labels), Top 30px, Right 30px.
  - Y-Axis: Fixed 0% to 100% with horizontal gridlines at 0%, 25%, 50%, 75%, 100%.
  - X-Axis: Time from first entry (`t_min`) to latest entry (`t_max`).
  - Single Data Point Edge Case:
    - If `len(entries) == 1`: Calculate Y position for the single point. Center X coordinate in the chart area. Draw single point marker and annotate with percentage and note. Avoid zero-division (`t_max == t_min`).
  - Empty State Handling:
    - If `len(entries) == 0`: Fill 800x400 dark background. Render centered string: `"No progress recorded"`.

### 2.6 Web UI Components (`web/router-ui-react`)
- **Projection Types (`src/state/agent-projection.ts`)**:
  - Extend `Agent` interface:
    ```typescript
    export interface AgentProgress {
      percent: number;
      note: string | null;
      updated_at: string;
    }

    export interface Agent extends ModelAgent {
      // ...
      progress?: AgentProgress | null;
    }
    ```
- **Agent Row (`src/components/AgentRow.tsx`)**:
  - When `agent.progress` is non-null:
    - Render a compact progress bar with visual fill from 0% to 100%.
    - Show percentage text (e.g. `45%`).
    - Tooltip on hover displaying `note` and `updated_at`.
    - Click handler triggers `onOpenProgressHistory(agent.agent_id)`.
  - When `agent.progress === null` or undefined:
    - Entire progress element remains hidden (no blank or 0% placeholder).
- **Conversation Header (`src/components/ConversationHeader.tsx`)**:
  - When `agent.progress` is non-null:
    - Render compact progress bar with numeric percent, visual bar fill, and current milestone note directly visible.
    - Click handler triggers progress history modal.
  - When `agent.progress === null` or undefined:
    - Entire progress element remains hidden.
- **Progress History Modal (`src/components/ProgressHistoryModal.tsx`)**:
  - Fetches history data from `GET /v1/agents/{id}/progress/history`.
  - Renders inline interactive SVG chart:
    - Time along horizontal X-axis (first entry to latest entry).
    - Fixed 0% to 100% vertical Y-axis.
    - Connected line path and interactive circle data points.
    - Tooltip/popover on hover/click over points showing timestamp, percentage, and note.
  - Modal Dismissal:
    - Escape key listener.
    - Backdrop click listener.
    - Close button styled strictly with `.icon-btn-round` (24px x 24px, 999px border-radius).

---

## 3. Requirements & Acceptance Criteria Traceability

| Requirement | Acceptance Criterion | Plan Section & Implementation Artifact |
|---|---|---|
| **R1**: Worker Progress Tool (`mecha_progress`) | **AC1**: `mecha_progress` supports `set`, `get`, `clear`. Strict validations (`0 <= percent <= 100`, `note <= 120`). Non-monotonic allowed. Exact JSON response schemas. | Section 2.3 (`embedded_progress_tool.go`). Exact JSON responses for `set`, `get`, `clear`. Schema rejects non-integers, out-of-range percent, and notes > 120 chars. |
| **R2**: Progress Persistence & Bounded Retention | **AC2**: Persists across context resets, compactions, prompt resets. Goal lifecycle decoupling. `clear` sets snapshot to `null` while retaining history. | Section 2.1 & 2.2 (`internal/store`). Stored in dedicated SQLite tables decoupled from chat transcript compaction and goal continuation tables. |
| **R2**: Bounded Retention FIFO | **AC3**: FIFO eviction at 1,000 entries. 1,001st update evicts oldest entry. | Section 2.1 & 2.2 (`internal/store/migrations/106_...` and `SetAgentProgress`). SQL query prunes oldest records beyond 1,000 offset. |
| **R3**: State Projection & Real-Time Dispatch | **AC4**: Wire schema returns `progress: null` when unset/cleared, and object when set. Real-time WebSocket dispatch on updates. | Section 2.3 & 2.6 (`agent-projection.ts`, WebSocket broadcast event `agent_progress`). UI hides bar when `null`. |
| **R4**: Web UI Progress Bar | **AC4**: Compact progress bar in `AgentRow.tsx` and `ConversationHeader.tsx` only when `agent.progress !== null`. Displays numeric percent and note. | Section 2.6 (`AgentRow.tsx`, `ConversationHeader.tsx`). Hidden when unset. |
| **R5**: Web UI Progress History Dialog | **AC5**: Modal opens on progress bar click. SVG line graph from first to latest entry, 0-100% Y-axis, tooltips for timestamp/note, dismiss via Escape/backdrop/`.icon-btn-round` close button. | Section 2.6 (`ProgressHistoryModal.tsx`). |
| **R6**: Progress History REST Endpoint | **AC6**: `GET /v1/agents/{id}/progress/history` returns 200 with chronological JSON array; 200 with `[]` if 0 entries; 404 with `{"error": "agent not found"}` if agent missing. | Section 2.4 (`internal/transport/http.go`). Handlers map store history to JSON response. |
| **R7**: Server-Side PNG Chart Endpoint | **AC7**: `GET /v1/agents/{id}/progress/chart.png` returns 800x400 dark-theme PNG. 404 for missing agent; 200 with "No progress recorded" placeholder for 0 entries; single-point division-by-zero protection. Pure Go (no Cgo/CLI). | Section 2.5 (`internal/managed/chart` and `internal/transport/http.go`). Native Go `image` and `basicfont`. |
| **R1-R7**: End-to-End Integration | **AC8**: Comprehensive within-service functional integration test covering the entire lifecycle. | Section 4 (`embedded_progress_integration_test.go`). |

---

## 4. Mandatory Regression-Prevention Integration Tests

### File Location
`router-proxy/internal/managed/embedded_progress_integration_test.go`

### Full Within-Service Path Test
The integration test exercises the full lifecycle against a live router store and managed agent runner without external mocking:
1. **Agent Session Setup**: Launch a managed worker session in the test environment.
2. **Tool Invocation (`set`)**: Execute `mecha_progress` with `action: "set"`, `percent: 25`, `note: "Initializing schema"`.
3. **Store Persistence Verification**: Verify `GetAgentProgress` returns snapshot and `GetAgentProgressHistory` returns 1 entry.
4. **Live Event Dispatch**: Verify the WebSocket broadcast queue receives the `agent_progress` event frame with matching payload.
5. **Non-Monotonic Update**: Execute `mecha_progress` with `percent: 60`, then `percent: 40`. Verify both updates persist chronologically.
6. **Context Reset & Compaction Independence**: Trigger an agent context reset and compaction event; verify `GetAgentProgress` and history remain intact.
7. **REST History API (`GET /v1/agents/{id}/progress/history`)**: Request the endpoint via HTTP; verify HTTP 200 and chronological array matching store history.
8. **REST PNG Chart API (`GET /v1/agents/{id}/progress/chart.png`)**:
   - Request endpoint with history populated; verify HTTP 200, `Content-Type: image/png`, valid PNG header bytes (`\x89PNG\r\n\x1a\n`), and 800x400 dimensions via `png.DecodeConfig`.
   - Single point test: verify chart renders without division-by-zero panics.
9. **Tool Invocation (`get`)**: Verify `mecha_progress` with `action: "get"` returns current percent, note, updated timestamp, and accurate count.
10. **Tool Invocation (`clear`)**: Execute `action: "clear"`; verify current snapshot becomes `null`, projection reflects `progress: null`, but history records remain in store and REST history endpoint still returns accumulated records.
11. **Empty Agent History & 404 Tests**:
    - For agent with cleared/empty history: Chart endpoint returns 200 with "No progress recorded" placeholder image. History endpoint returns `[]`.
    - For non-existent agent: Chart and history endpoints return HTTP 404 `{"error": "agent not found"}`.
12. **FIFO Retention Eviction at 1,000 Limit**:
    - Loop 1,005 `set` operations on the agent session.
    - Verify total retained entries in `agent_progress_history` equals exactly 1,000.
    - Verify the first 5 entries were evicted FIFO and the newest 1,000 entries remain in strict chronological order.

### Specific Regressions Guarded Against
1. **Silent Truncation vs. Error Rejection Regression**:
   - Guards against accidentally trimming notes > 120 chars or rounding float percentages instead of rejecting with a validation error.
2. **Context Reset Data Loss Regression**:
   - Guards against context compaction or memory resets wiping or resetting the agent's progress state or history.
3. **Empty/0% Bar Clutter Regression**:
   - Guards against the UI projection emitting `{ progress: { percent: 0, ... } }` or `{ progress: {} }` when unset, which causes unsightly empty progress bars in the dashboard.
4. **Clear Action History Wipe Regression**:
   - Guards against `clear` mistakenly dropping historical audit records from `agent_progress_history` instead of only clearing the current snapshot.
5. **Division-by-Zero Single Point Chart Panic**:
   - Guards against `t_max - t_min == 0` causing a divide-by-zero panic when generating a PNG chart for an agent with exactly one progress update.
6. **Unbounded Storage Leak Regression**:
   - Guards against runaway agent loops generating unbounded history rows by enforcing strict FIFO deletion at 1,000 records.
7. **External CLI / Cgo Dependency Regression**:
   - Guards against introducing Cgo or external system binaries (`graphviz`, `imagemagick`, `cairo`) for chart rendering, ensuring pure Go portability on single-user workstations.
