# Product Requirements Document: Worker Progress Tracking

## 1. Why We Need This

Right now, when an agent works on a long task, it's hard to tell how far along it actually is without reading through chat messages or tool logs. A supervisor or human watching multiple agents has to guess whether a task is 10% done or almost finished.

This feature gives workers a simple, direct way to report their progress (0 to 100%) alongside a brief note about what they are doing. Humans see this immediately as a progress bar in the UI, can click in to inspect how progress changed over time, and automated tools can grab a clean image of the chart to share anywhere.

---

## 2. User Experience

### In the Agent List
- Each agent row shows a compact progress bar alongside the agent's name and status.
- The bar visually fills from 0% to 100% and displays the current number (e.g. `65%`).
- Hovering over the bar shows the latest note (e.g. "Running integration tests").
- **Crucial rule:** If the worker has not set progress, or cleared it, no progress bar appears at all. There is no empty 0% placeholder bar cluttering the list when progress isn't applicable.

### In the Agent Header
- At the top of the agent conversation screen, the progress bar appears in the header area.
- It displays the current percentage, the visual fill, and the latest status note directly so you don't have to hover to see what the worker is doing.
- Just like the list view, if progress is unset, this area stays completely hidden.

### The Progress History Popup
Clicking the progress bar anywhere (list row or header) opens a lightweight popup dialog with a history graph:
- **Visual line chart:** Shows time on the horizontal axis and percentage (0 to 100%) on the vertical axis.
- **Default time range:** Automatically frames the full window from the very first progress entry to the most recent one.
- **Data points:** Each update appears as a distinct dot on the line.
- **Inspection on hover/click:** Pointing at or tapping any dot reveals:
  - The exact timestamp (date and time).
  - The percentage recorded.
  - The short note left by the worker at that step.
- **Quick close:** Can be closed with an Escape key press, a close button, or by clicking outside the popup.

---

## 3. Worker Tool: How Agents Report Progress

Workers get a single, dedicated tool for managing their progress. It supports three simple actions:

### Action 1: Set Progress
- **Percentage:** A required whole number from `0` to `100` inclusive.
  - Decimals, negative values, and numbers above 100 are rejected with a clear explanation back to the worker.
- **Note:** An optional short status message explaining the current milestone.
  - Max length: 120 characters. If a worker sends more, the tool either trims it or asks the worker to keep it concise.
  - Example: `progress set 45 "Finished API routes, writing unit tests"`

### Action 2: Get Status
- Returns the current progress percentage, latest note, when it was last updated, and the total count of progress updates recorded so far.
- If progress is currently unset, it tells the worker that no progress is active.

### Action 3: Clear Progress
- Resets progress back to unset (null).
- Immediately removes the progress bar from both the agent header and the agent list row.
- Example use case: A worker finishes a long phased task and goes back to waiting for user instructions, or switches to open-ended conversation where percentage tracking makes no sense.

---

## 4. Lifecycle & Persistence

- **Belongs to the agent session:** Progress is tied directly to the agent itself, not to an individual goal or temporary prompt.
- **Survives context resets:** When an agent's chat context is compacted or reset, its progress history remains intact. A long-running agent running for days does not lose its progress curve just because chat history got trimmed.
- **Independent of goals:** Goals come and go, but progress tracking is controlled purely by the worker. Starting, pausing, or completing a goal does not automatically wipe progress unless the worker explicitly chooses to clear it.
- **When history clears:**
  - Progress updates and history only disappear if the worker calls the clear action, or if the agent session itself is deleted or permanently cleaned up.

---

## 5. Downstream Automation: Server-Side Chart Image

External systems, chat bots, and automated health checks need to inspect progress without loading the full web app.

- **Image endpoint:** A dedicated server URL that generates and returns a clean PNG image of the progress line chart.
- **Output:**
  - A crisp, readable PNG rendering of the same line chart seen in the UI popup.
  - Displays the line climbing toward 100%, axis marks for percentage and time, and markers for recorded data points.
  - Returns appropriate dimensions suitable for embedding in notifications, issue comments, or status digests.
- **Empty state handling:** If an agent has no recorded progress, the endpoint returns an image clearly indicating "No progress recorded" rather than an error or blank square.

---

## 6. Human Decision Points

These are the specific product questions and trade-offs that need human review and input:

### 1. What should happen when an agent reaches 100%?
- **Option A:** The bar stays visible at 100% until the worker explicitly calls `clear` or the agent is deleted.
- **Option B:** The bar automatically disappears after a set cooling period (e.g. 10 minutes) or upon the agent completing its current run.
- *Trade-off:* Option A leaves proof of completion visible, but might leave stale 100% bars around. Option B keeps things clean automatically, but might hide the accomplishment before a human looks at the screen.

### 2. Is progress allowed to go backwards?
- If an agent is at 80%, discovers an unexpected bug, and drops back to 40%:
  - **Option A:** Allow it freely. Real work has setbacks; the graph will show a dip.
  - **Option B:** Warn or reject lower values unless explicitly flagged as a rework step.
  - *Recommendation:* Option A. Truth in reporting is better than forced false monotonicity.

### 3. Does "Clear Progress" erase the historical graph or just hide the bar?
- **Option A (Full wipe):** Clearing progress deletes all past data points for that agent. The next `set` starts a brand new graph from scratch.
- **Option B (Hide current, keep history):** Clearing sets current progress to unset (hiding the bar in the UI), but preserves past milestones in the chart so a new milestone run shows previous history or starts a new distinct phase on the timeline.
- *Trade-off:* Option A is simpler and prevents old unrelated tasks from cluttering the graph. Option B provides an audit trail across the lifetime of the agent.

### 4. Graph start time baseline
- When rendering the timeline on the popup and server image:
  - **Option A:** Start the horizontal axis at the timestamp of the *first progress update*.
  - **Option B:** Start the horizontal axis at the timestamp when the *agent session was first created*.
  - *Trade-off:* Option A focuses tightly on the actual work duration. Option B reveals how long the agent sat idle before work began.

### 5. Progress bar styling and color transitions
- **Option A:** Single uniform accent color (consistent with the rest of the application theme).
- **Option B:** Dynamic status colors (e.g. blue for in-progress, green when reaching 100%, orange/yellow if no progress updates occur for an extended period).
- *Trade-off:* Option A is understated and doesn't introduce visual noise. Option B gives fast glanceable health signals across a busy dashboard.
