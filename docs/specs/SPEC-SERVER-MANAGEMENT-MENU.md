# Spec: Server Management Dropdown Menu

- **ID**: `SPEC-SETTINGS-002`
- **Status**: `Draft`
- **Domain**: `Settings`
- **Target E2E Suite**: `tests/e2e/server-management-menu.spec.ts`

---

## 1. Intent & User Outcome
Wolfgang accesses administrative and operational server actions via the server menu (Mission Control ellipsis menu in the sidebar). Previously rendered as an unstyled single-column vertical list with missing icons, the menu now presents a clean, visually unified two-column grid matching the Agent Controls popup aesthetic: dark glassmorphic styling, subtle translucent tile backgrounds with hover effects, consistent icons for each item, and an optional header ("SERVER CONTROLS" or "MANAGED ACTIONS"), without pin buttons.

---

## 2. Core Invariants
1. The server menu MUST open when the user clicks the 3-dot trigger button (`.managed-actions-trigger`) in the RouterSelector card.
2. The server menu MUST render its items in a 2-column grid layout (`repeat(2, 1fr)`), styled consistently with the agent controls tiles (`.agent-control-tile` / `.managed-action-tile`).
3. Each item in the server menu MUST include a relevant FontAwesome icon on the left of the item text.
4. Pin buttons MUST NOT be displayed in the server menu tiles.
5. Clicking an item MUST trigger that item's action and immediately dismiss the menu.
6. The menu MUST dismiss cleanly when the user clicks outside or presses the Escape key.
7. Event propagation MUST be properly stopped so clicking inside the menu or on the trigger does not inadvertently activate the router selector or background elements.

---

## 3. User Scenarios (Behavioral Contract)

### Scenario: [SCN-SERVER-001] Open server menu and observe 2-column layout and icons
- **User Goal**: Inspect available server administration actions with a clean, two-column visual presentation.
- **Given**:
  - The application is open and connected to the router.
  - The sidebar displays the RouterSelector card with the managed actions trigger button.
- **When**:
  - User clicks the managed actions trigger (`.managed-actions-trigger`).
- **Then**:
  - The server menu popup opens.
  - The menu contains a header titled "SERVER CONTROLS".
  - The actions are arranged in a 2-column grid.
  - Every action item displays its designated icon:
    - "Launch Agent": icon `fa-solid fa-robot` (or `fa-solid fa-user-plus` / `fa-solid fa-plus`)
    - "Launch Workflow": icon `fa-solid fa-play`
    - "Restart router": icon `fa-solid fa-arrows-rotate`
    - "Provider settings": icon `fa-solid fa-key`
    - "MCP Servers": icon `fa-solid fa-network-wired` (or `fa-solid fa-server`)
    - "Tool Defaults": icon `fa-solid fa-sliders`
    - "Knowledge Base": icon `fa-solid fa-book`
    - "Workflows": icon `fa-solid fa-diagram-project`
    - "System 1 Config": icon `fa-solid fa-brain`
    - "Context Thresholds": icon `fa-solid fa-chart-pie` (or `fa-solid fa-gauge-high`)
  - No pin buttons (`.agent-control-pin-btn`) are rendered inside any tile.

### Scenario: [SCN-SERVER-002] Click an action in the server menu
- **User Goal**: Trigger an administrative action (e.g., open Provider settings modal).
- **Given**:
  - The server menu is open.
- **When**:
  - User clicks "Provider settings".
- **Then**:
  - The Provider settings modal opens.
  - The server menu closes immediately.

### Scenario: [SCN-SERVER-003] Dismiss server menu on Escape or outside click
- **User Goal**: Dismiss the menu without triggering an action.
- **Given**:
  - The server menu is open.
- **When**:
  - User presses `Escape` or clicks outside the menu container.
- **Then**:
  - The server menu closes without invoking any actions.

---

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-SERVER-001` | 2-column layout, icons, and no pins | `tests/e2e/server-management-menu.spec.ts` | `renders server menu in 2-column grid with icons and no pins` |
| `SCN-SERVER-002` | Action invocation and auto-close | `tests/e2e/server-management-menu.spec.ts` | `invokes action and closes menu on item click` |
| `SCN-SERVER-003` | Outside click and Escape dismissal | `tests/e2e/server-management-menu.spec.ts` | `dismisses server menu on outside click or Escape` |
