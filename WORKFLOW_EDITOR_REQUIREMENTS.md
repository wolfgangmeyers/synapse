# Visual Workflow Editor Requirements

This document specifies the requirements for the visual workflow editor in the Mecha Web UI. It replaces form-based state and transition lists with an interactive canvas powered by React Flow (`@xyflow/react`).

---

## 1. Top-Level: Workflow Management

Operators need to manage multiple distinct workflows without getting lost in technical details.

1. **Workflow Directory**:
   - View all existing workflows in a simple list or card grid.
   - Each workflow card displays its name, short description, total number of states, and last updated time.
2. **Create Workflow**:
   - A clear "New Workflow" button prompts for a name and optional description, then opens the canvas for that workflow.
3. **Select and Edit**:
   - Clicking any workflow opens its dedicated visual canvas editor.
4. **Workflow Actions**:
   - Rename, duplicate, or delete a workflow from the list.
   - A breadcrumb or "Back to Workflows" button returns to the list from the canvas at any time.

---

## 2. Visual Canvas

The canvas provides an intuitive, high-level map of the entire workflow.

1. **Interactive Grid**:
   - States appear as distinct interactive cards (nodes) positioned on a canvas with a subtle background grid.
   - Operators can pan by dragging the canvas background and zoom with the mouse wheel or trackpad.
   - Canvas controls in the corner provide one-click Zoom In, Zoom Out, and Fit to Screen.
2. **Adding States**:
   - An "Add State" button in the canvas toolbar immediately drops a new state node onto the canvas.
   - New nodes appear near the center of the current view and can be dragged anywhere.
3. **Persistent Layout**:
   - Dragging a node updates its position on the grid.
   - Node positions are saved with the workflow definition so the visual layout remains stable across sessions.

---

## 3. State Nodes

Each state card represents one step in the workflow and provides immediate visual context.

1. **Card Header & Badges**:
   - **State Name**: Large, bold label identifying the step (e.g., "Code Review", "Build & Test", "Deploy").
   - **Entry Badge**: Prominent badge identifying the entry state where the workflow begins.
   - **Tool Badges**: Compact summary showing tool access (e.g., "All Tools" or "4 Tools").
   - **Reminder Indicator**: Small icon showing if scheduled reminders are active on this state.
   - **Exit Status**: Badge showing whether an agent is allowed to finish or exit the workflow from this state.
2. **On-Node Actions**:
   - **Gear Icon**: Clicking the gear opens the State Configuration Dialog to adjust settings.
   - **Delete Icon**: Clicking the trash icon prompts for confirmation and removes the state along with any connected transitions.
3. **Connection Points (Handles)**:
   - **Output Handle**: Visual dot on the right edge used to drag transitions out to other states.
   - **Input Handle**: Visual dot on the left edge that receives incoming transition arrows.

---

## 4. State Configuration Dialog (Gear Modal)

Clicking the gear icon opens a focused dialog. It contains only settings that an operator cares about:

1. **State Identity**:
   - **Name**: Short, descriptive name for the step.
   - **Start State**: Checkbox or toggle to make this the entry state for the workflow.
2. **Agent Instructions (Entry Text)**:
   - Plain-English instructions displayed to the agent upon entering this state (e.g., "Review the pull request diff, run unit tests, and check for lint errors.").
3. **Allowed Tools**:
   - Simple choice: "Allow all tools" or "Restrict tools".
   - When restricted, a clean checklist of available tools lets operators toggle only what the agent needs for this specific step.
4. **Reminders**:
   - List of state-scoped reminders with an "Add Reminder" button.
   - Operators configure:
     - Timing (delay before reminder fires, e.g., "5 minutes").
     - Type: One-shot or recurring.
     - Reminder message body: The nudge message sent to the agent if it stays in this state too long.
5. **Workflow Exit Permission**:
   - Toggle: "Allow agent to complete or exit workflow from this state".

---

## 5. Visual Drag-and-Drop Transitions

Transitions define how an agent moves from one state to another. They are drawn directly on the canvas without manual dropdowns.

1. **Drawing Transitions**:
   - Operators click and drag from the output handle of one state node and drop onto the input handle of another state node.
   - A directed arrow immediately connects the two states.
   - Loops and return paths (e.g., moving back to "Code Review" after "Tests Failed") are drawn the same way.
2. **No Manual Dropdowns**:
   - Connecting lines on screen replaces target-state select menus. What you see is what runs.
3. **Transition Details & Labels**:
   - Clicking any connection arrow highlights it and opens a popover or mini-dialog.
   - **Description / Condition**: Optional label displayed along the arrow (e.g., "Review approved", "Tests failed", "Needs manual sign-off").
   - **Delete Transition**: A "Remove Connection" button deletes the transition arrow.

---

## 6. Operator Experience & Guardrails

The editor ensures workflows are valid before they can run, using simple visual warnings rather than cryptic error codes.

1. **Visual Health Checks**:
   - A state without any outgoing transitions or exit permission is flagged with a warning banner.
   - Exactly one state must be designated as the entry point; if none or multiple are marked, a prompt highlights them.
   - Unreachable states (islands disconnected from the start) are visually flagged.
2. **Clean Save & Validation**:
   - A single "Save Workflow" button validates the layout and persists changes.
   - If something is missing (e.g., an unlinked state), the editor highlights the exact node on canvas with a friendly suggestion.
3. **Tidy / Auto-Layout (Optional)**:
   - A "Tidy Layout" button automatically reorganizes nodes into a clean left-to-right flow for messy or complex graphs.
