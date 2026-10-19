# Worker Progress Tracking Specification

## 1. Problem Statement

Long-running agent goals and multi-step tasks currently suffer from operational progress invisibility:
- **No glanceable status**: Operators and human supervisors monitoring several concurrent agents cannot quickly distinguish whether a task is 10% complete or nearly finished without reading conversation turns, inspecting raw tool execution logs, or interrupting the worker.
- **Visual clutter vs. absence**: Unset or inactive progress must not render artificial or misleading placeholder progress bars (e.g. empty `0%` progress bars for non-progress workflows). When a worker is not actively running a measured workflow, progress must be cleanly unset/hidden.
- **Auditability over time**: Debugging stalls, estimating completion velocity, and reviewing operational milestones require historical progress tracking (a chronological timeline from the first recorded progress entry to the latest) capturing both percentage and milestone notes.
- **Downstream headless automation**: External scripts, chat integrations, issue comments, and status monitors need to inspect and share progress visually without loading or rendering the full browser Web UI. This creates a hard requirement for server-rendered PNG chart generation directly from the router.

---

## 2. Requirements

### R1: Worker Progress Tool (`mecha_progress`)
The router-managed worker runtime shall expose a dedicated built-in tool named `mecha_progress` supporting three explicit actions:
- **`set`**:
  - `percent`: required integer from `0` to `100` inclusive. Non-integer values (floats, strings), negative values (`< 0`), and values greater than `100` (`> 100`) must be rejected with a descriptive validation error.
  - Non-monotonic updates (e.g. updating from 60 back to 40 after a step failure or re-evaluation) are explicitly allowed and must be recorded.
  - `note`: optional UTF-8 string explaining the current milestone, maximum 120 characters. Strings exceeding 120 characters must be rejected with a validation error (do not silently trim or truncate).
  - Exact return object on success:
    ```json
    {
      "ok": true,
      "percent": 45,
      "note": "Compiling assets and running unit tests",
      "updated_at": "2026-10-17T16:30:00Z"
    }
    ```
- **`get`**:
  - Returns current progress status, last updated timestamp (`RFC3339 UTC`), and the count of retained history entries.
  - Exact return object when progress is set:
    ```json
    {
      "ok": true,
      "percent": 45,
      "note": "Compiling assets and running unit tests",
      "updated_at": "2026-10-17T16:30:00Z",
      "count": 3
    }
    ```
  - Exact return object when progress is unset/cleared:
    ```json
    {
      "ok": true,
      "percent": null,
      "note": null,
      "updated_at": null,
      "count": 0
    }
    ```
- **`clear`**:
  - Resets current progress snapshot to `null`.
  - Exact return object on success:
    ```json
    {
      "ok": true,
      "cleared": true
    }
    ```

### R2: Progress Persistence and Bounded Retention
- **Session-bound**: Progress state and historical entries are attached to the agent session identity in the durable router store.
- **Context reset independence**: Progress state and history entries must survive context resets, context compaction, and prompt resets without loss or corruption.
- **Goal decoupling**: Progress is owned exclusively by the worker via explicit tool invocations. Goal lifecycle events (start, pause, completion, snoozing) must not overwrite, clear, or corrupt progress state.
- **Explicit lifecycle clearing**: Current progress snapshot is set to `null` only upon an explicit `clear` action in `mecha_progress` or permanent deletion of the agent session. Clearing the current progress does not delete the accumulated history log.
- **Bounded retention policy**:
  - In accordance with repository data retention requirements, history storage per agent session is bounded to a maximum of **1,000 entries**.
  - When the 1,001st update is recorded for an agent session, the oldest entry is evicted using FIFO ordering (ring buffer behavior).
- **History audit record format**: Every successful `set` operation appends a discrete history record:
  - `percent`: integer between 0 and 100.
  - `note`: optional string up to 120 characters (or `null` if omitted).
  - `timestamp`: RFC3339 UTC timestamp.

### R3: Agent State Projection & Real-Time Dispatch
- **Wire schema projection**:
  - The public agent projection (e.g. `/v1/agents`, `/v1/managed-agents`) includes the current progress snapshot in the agent model:
    ```json
    {
      "progress": {
        "percent": 45,
        "note": "Compiling assets and running unit tests",
        "updated_at": "2026-10-17T16:30:00Z"
      }
    }
    ```
  - When progress is unset or cleared, the `progress` property must be strictly `null` (never an object with null properties):
    ```json
    {
      "progress": null
    }
    ```
- **Real-time WebSocket events**:
  - Any change to progress via `set` or `clear` immediately dispatches a real-time event frame across active WebSocket client connections (e.g., `agent_progress` or updated agent snapshot) to prevent stale UI states.

### R4: Web UI Progress Bar
- **Agent Header**:
  - Renders a compact progress bar in the conversation header when `agent.progress !== null`.
  - Visually fills from 0% to 100% according to `progress.percent`.
  - Displays the numeric percentage and current milestone note directly in the header.
  - Remains completely hidden when `agent.progress === null`.
- **Agent List Row**:
  - Renders a compact progress indicator in the agent row when `agent.progress !== null`.
  - Displays percentage fill and value; hovering over the bar reveals a tooltip with the latest milestone note and timestamp.
  - Remains completely hidden when `agent.progress === null`.
- **Clean unset rule**: Neither the header nor the list row shall render a blank or 0% bar when progress is unset.

### R5: Web UI Progress History Dialog
- **Modal invocation**: Clicking the progress bar in either the agent header or the agent list row opens an interactive Progress History modal dialog.
- **Data loading**: Loads history directly from `GET /v1/agents/{id}/progress/history`.
- **Chronological line graph**:
  - Spans the horizontal X-axis across time, framed from the agent's first recorded progress update to the most recent.
  - Fixes the vertical Y-axis from 0% to 100%.
  - Renders discrete marker points for each recorded entry connected by a clean line.
- **Data point inspection**:
  - Hovering over or selecting any point reveals the exact timestamp, recorded percentage, and milestone note.
- **Dismissal**:
  - Closes cleanly via Escape key, an explicit close button (`.icon-btn-round`), or by clicking outside the modal backdrop.

### R6: Progress History REST Endpoint
- **HTTP route**: `GET /v1/agents/{id}/progress/history`
- **Success response (HTTP 200)**: Returns a JSON array of recorded history points in chronological order:
  ```json
  [
    {
      "percent": 10,
      "note": "Analyzing requirements",
      "timestamp": "2026-10-17T16:00:00Z"
    },
    {
      "percent": 45,
      "note": "Compiling assets and running unit tests",
      "timestamp": "2026-10-17T16:30:00Z"
    }
  ]
  ```
- **Empty state (HTTP 200)**: If the agent exists but has recorded zero history entries, returns an empty JSON array: `[]`.
- **Error contract (HTTP 404)**: If the agent session does not exist, returns HTTP 404 Not Found with JSON error body:
  ```json
  {
    "error": "agent not found"
  }
  ```

### R7: Server-Side PNG Chart Rendering Endpoint
- **HTTP route**: `GET /v1/agents/{id}/progress/chart.png`
- **Output**: Generates and returns a server-rendered PNG image (`Content-Type: image/png`).
- **Visual formatting**:
  - Default dimensions: **800x400** pixels.
  - Dark theme matching Web UI palette: dark background, subtle gridlines, high-contrast axes and text, distinct line/point accent styling.
  - Labeled axes: 0% to 100% on the vertical Y-axis, formatted timestamps on the horizontal X-axis.
  - Plotted points for recorded milestone entries.
- **Single data point edge case**:
  - When history contains exactly 1 data point, render a single centered marker point at the corresponding Y percentage level with milestone note and timestamp labels, preventing zero-delta time division errors.
- **Zero data points placeholder (HTTP 200)**:
  - When the agent exists but has 0 history entries, returns HTTP 200 with an 800x400 dark-theme placeholder PNG displaying a centered, legible message: "No progress recorded".
- **Error contract (HTTP 404)**:
  - If the agent session does not exist, returns HTTP 404 Not Found with JSON error body `{"error": "agent not found"}`.
- **Dependencies & portability**:
  - Must be implemented using pure Go standard libraries (`image`, `image/color`, `image/draw`, `image/png`) and standard sub-repositories (such as `golang.org/x/image/font`). Requires **no Cgo, external image CLI tools, or external system dependencies**.

---

## 3. Constraints

- **Workstation environment**: Designed for single-user workstations without multi-tenant auth gates, session cookies, or CORS authority blocks.
- **Non-interference**: Must not alter, disrupt, or introduce latency into existing agent execution loops, goal evaluation loops, context compaction cycles, or RPC stream settlement.
- **Visual styling**:
  - Conforms to the existing dark theme and typography of the Web UI.
  - Compact icon action buttons must strictly follow the `.icon-btn-round` styling convention (24px x 24px, 999px border-radius).
- **Graceful degradation & strict validation**:
  - Faulty client arguments or out-of-range tool inputs must yield structured tool error strings back to the agent without panics, silent trimmings, or process crashes.
- **Data retention**: Bounded retention (max 1,000 entries per session with FIFO eviction) is strictly enforced to avoid unbounded memory or disk growth.
- **Mandatory dev-loop testing**:
  - Must include within-service functional integration tests exercising the entire pipeline end-to-end: tool invocation → store persistence → projection/WebSocket broadcast → history JSON retrieval → PNG chart generation.

---

## 4. Acceptance Criteria

- **AC1 (maps to R1)**: `mecha_progress` supports `set`, `get`, and `clear` with exact specified JSON outputs. Inputs with `percent < 0`, `percent > 100`, or non-integer types are rejected with descriptive validation errors. `note` strings exceeding 120 characters are rejected with a validation error (not silently trimmed). Non-monotonic `set` updates are permitted and recorded.
- **AC2 (maps to R2)**: Progress snapshot and history entries persist in durable storage across agent context resets, compactions, and prompt resets. Calling `clear` sets current progress snapshot to `null` while preserving historical records until agent deletion.
- **AC3 (maps to R2)**: Progress history enforces bounded retention capped at 1,000 entries per session. When an agent reaches 1,001 entries, the oldest entry is evicted FIFO.
- **AC4 (maps to R3, R4)**: The public agent projection (`/v1/agents`, `/v1/managed-agents`) provides `agent.progress` as `null` when unset or cleared, and as an object with `percent`, `note`, and `updated_at` when set. Updates immediately trigger WebSocket broadcast frames. The Web UI renders progress bars in the header and list row only when `agent.progress !== null`.
- **AC5 (maps to R5)**: Clicking the progress bar in the Web UI opens the history modal displaying a chronological line graph from first to latest entry, with interactive inspection of timestamps and notes.
- **AC6 (maps to R6)**: `GET /v1/agents/{id}/progress/history` returns HTTP 200 with a JSON array of history points. If agent has 0 history entries, it returns HTTP 200 with `[]`. If the agent does not exist, it returns HTTP 404 with JSON error body `{"error": "agent not found"}`.
- **AC7 (maps to R7)**: `GET /v1/agents/{id}/progress/chart.png` returns a valid 800x400 dark-theme PNG. Returns HTTP 404 JSON when the agent does not exist; returns HTTP 200 with a "No progress recorded" placeholder PNG when the agent exists with 0 history entries; and correctly renders a centered marker point without division-by-zero errors when history contains exactly 1 entry. Implemented in pure Go without Cgo or external CLI tools.
- **AC8 (maps to R1-R7)**: Comprehensive end-to-end within-service functional integration tests verify the full lifecycle: tool execution (`set`, `get`, `clear`, validation errors), FIFO retention eviction at 1,000 entries, state snapshot projection, history JSON API, and PNG chart rendering.
