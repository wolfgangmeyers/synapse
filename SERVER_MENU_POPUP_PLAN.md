# Implementation Plan: Server Management Dropdown Menu Alignment

- **Specification**: [`SPEC-SETTINGS-002: Server Management Dropdown Menu`](docs/specs/SPEC-SERVER-MANAGEMENT-MENU.md)
- **Target Suite**: `tests/e2e/server-management-menu.spec.ts`

---

## 1. Objective & Scope

Align the server menu popup (`.managed-actions-menu`) layout, styling, and UX to match the Agent Controls popup (`.agent-controls-dropdown`):
- **Layout**: 2-column grid (`repeat(2, 1fr)`) with consistent item spacing and tile sizing.
- **Visual Design**: Dark glassmorphic styling, translucent tile backgrounds, border accents, and hover highlights consistent with Web UI design standards.
- **Icons**: Every server menu item must display its designated FontAwesome icon cleanly aligned to the left of the label.
- **Pins**: Strictly **no pin buttons** (`.agent-control-pin-btn`) in the server menu tiles (unlike agent controls which support pinning to the conversation header).
- **Interactions**: Clean escape-key dismissal, outside-click handling, and click-to-execute behavior.

---

## 2. Icon & Action Mapping

| Label | FontAwesome Icon Class | Action Handler | Condition |
| :--- | :--- | :--- | :--- |
| **Launch Agent** | `fa-solid fa-robot` | `onLaunchManaged` | `Boolean(onLaunchManaged)` |
| **Launch Workflow** | `fa-solid fa-play` | `onLaunchBackgroundWorkflow` | Always rendered if server menu is present |
| **Restart router** | `fa-solid fa-arrows-rotate` | `onRestartRouter` | `Boolean(onRestartRouter)` |
| **Provider settings** | `fa-solid fa-key` | `onProviderSettings` | `Boolean(onProviderSettings)` |
| **MCP Servers** | `fa-solid fa-network-wired` | `onMCPServers` | `Boolean(onMCPServers)` |
| **Tool Defaults** | `fa-solid fa-sliders` | `onToolDefaults` | `Boolean(onToolDefaults)` |
| **Knowledge Base** | `fa-solid fa-book` | `onKnowledgeBase` | `Boolean(onKnowledgeBase)` |
| **Workflows** | `fa-solid fa-diagram-project` | `onWorkflows` (or fallback to `#/workflows`) | Always rendered |
| **System 1 Config** | `fa-solid fa-brain` | `onSystemOneConfig` | `Boolean(onSystemOneConfig)` |
| **Context Thresholds** | `fa-solid fa-gauge-high` | `onContextThresholds` | `Boolean(onContextThresholds)` |

---

## 3. Component Updates (`web/router-ui-react/src/components/Sidebar.tsx` or `ServerControlsDropdown.tsx`)

### Architecture Choice: Dedicated Component or Inlined in Sidebar
To match the clean modularity of `AgentControlsDropdown.tsx` and keep `Sidebar.tsx` focused on agent listing and layout, create `ServerControlsDropdown.tsx` (or inline cleanly within `Sidebar.tsx`).

Key structure of the menu:
```tsx
<div id={`${managedActionsId}-menu`} className="managed-actions-menu" role="menu" aria-label="Server controls menu">
  <div className="managed-actions-header">
    <span className="managed-actions-title">Server Controls</span>
  </div>
  <div className="managed-actions-grid">
    {items.map((item) => (
      <div key={item.id} className="managed-action-tile">
        <button
          type="button"
          className="managed-action-tile-btn"
          role="menuitem"
          onClick={(event) => runManagedAction(item.action, event)}
          aria-label={item.label}
          title={item.label}
        >
          <i className={`${item.icon} managed-action-tile-icon`} aria-hidden="true" />
          <span className="managed-action-tile-label">{item.label}</span>
        </button>
      </div>
    ))}
  </div>
</div>
```

### Outside Click & Keyboard Listeners
- Attach an outside click listener on document capture/next tick when `managedActionsOpen` is true.
- Close on `Escape` keydown.
- Stop click propagation when clicking inside the dropdown.

---

## 4. Container Dimensions, Positioning & CSS Overflow Safety

### Container Dimensions & Positioning Analysis
- **Sidebar Width**: The sidebar has `.shell { grid-template-columns: minmax(320px, 390px) minmax(0, 1fr); }` with `.sidebar { padding: 18px 16px; overflow-y: auto; overflow-x: hidden; position: relative; }`. On screens `<= 390px`, padding is `14px`.
  - Available width inside `.sidebar` ranges from `320px - 32px = 288px` (at minimum 320px sidebar width) up to `390px - 32px = 358px` (default).
- **Positioning Context**:
  - The menu lives inside `.managed-actions` within `.router-selector-actions` inside `.router-selector`.
  - `.managed-actions` has `position: relative; flex: 0 0 auto;`.
  - `.router-selector` spans full width (`flex: 1 1 auto; min-width: 0; width: 100%`) within `.connection-actions`.
  - The trigger button is on the right side of `.router-selector`. When `.managed-actions-menu` is positioned with `right: 0`, it aligns to the right edge of `.managed-actions`.
- **Overflow & Clipping Safeguards**:
  - `.sidebar` has `overflow-x: hidden` and `overflow-y: auto`. If `.managed-actions-menu` extends beyond the sidebar's bounds or if fixed `width: 320px` exceeds the available container space in narrow viewports, it could cause horizontal scrollbars or clipping.
  - To guarantee zero clipping and zero unwanted scrollbars, the menu container sizing must use:
    ```css
    width: min(320px, calc(100vw - 32px));
    max-width: calc(100% + 280px); /* prevents exceeding sidebar left bounds when aligned right */
    box-sizing: border-box;
    ```
    Or more precisely:
    ```css
    width: min(320px, 100%);
    /* In the trigger container: */
    width: 320px;
    max-width: calc(100vw - 32px);
    ```
    Even better: Since `.managed-actions` is positioned at the right edge of `.router-selector` (which occupies ~288px to 358px), positioning `.managed-actions-menu` at `right: 0` means the 320px dropdown extends leftward. In a 358px sidebar interior, `right: 0` with `width: 320px` leaves `358px - 320px = 38px` of breathing room on the left, fitting cleanly inside `.sidebar`.
    To ensure complete safety even at the minimum 320px sidebar width (where content area is 288px):
    ```css
    width: min(320px, calc(100vw - 32px));
    max-width: calc(100% + 250px);
    ```
    Specifically, we define:
    `width: min(320px, calc(100% + 260px));` or `width: min(320px, 100vw - 32px)` with `box-sizing: border-box`. If the sidebar content width is 288px, `width: min(320px, 288px)` or `width: min(320px, calc(100vw - 32px))` ensures the popup never bleeds outside the left edge of `.sidebar` or triggers horizontal scrolling.
- **Tile Sizing in 2-Column Grid (`repeat(2, 1fr)`)**:
  - With container width `320px` (or `~288px` minimum), padding `10px` or `12px` leaves `296px` (or `264px`) inner grid width.
  - With `gap: 8px` (or `6px` on smaller widths), each column has `(296 - 8) / 2 = 144px` width (or `~129px` at minimum).
  - Button styling within tiles:
    - `padding: 8px 10px;`
    - `gap: 8px;`
    - Icon width: `16px; flex-shrink: 0;`
    - Text label: `font-size: 0.81rem; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0;`
    - Tile container: `overflow: hidden; min-width: 0;`
  - This allows longer labels (e.g. "Provider settings", "Context Thresholds") to display with clean truncation/ellipsis without wrapping or busting tile boundaries.
- **Z-Index Layering**:
  - `.connection-actions` has `z-index: 100`.
  - `.managed-actions-menu` uses `z-index: 10001;` matching `.agent-controls-dropdown`.
  - Modal dialog backdrops (`z-index: 999` / `1000`) or lightboxes will cleanly overlay it.

---

## 5. CSS Specifications (`web/router-ui-react/src/app.css`)

Update/replace existing `.managed-actions-menu` styles with matching dark glassmorphism:

```css
/* Server / Managed Actions Menu */
.managed-actions-menu {
  position: absolute;
  top: calc(100% + 8px);
  right: 0;
  width: min(320px, calc(100vw - 32px));
  max-width: 320px;
  background: #0f172a !important;
  border: 1px solid rgba(255, 255, 255, 0.15);
  border-radius: 12px;
  box-shadow: 0 16px 36px rgba(0, 0, 0, 0.65), 0 0 0 1px rgba(255, 255, 255, 0.08);
  padding: 12px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  animation: dropdown-fade-in 0.15s ease-out;
  z-index: 10001;
  box-sizing: border-box;
}

.managed-actions-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 2px 4px 6px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.08);
}

.managed-actions-title {
  font-size: 0.8rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--muted, #94a3b8);
}

.managed-actions-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 8px;
}

.managed-action-tile {
  position: relative;
  display: flex;
  align-items: center;
  background: rgba(255, 255, 255, 0.04);
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 8px;
  transition: all 0.15s ease;
  overflow: hidden;
  min-width: 0;
}

.managed-action-tile:hover {
  background: rgba(255, 255, 255, 0.08);
  border-color: rgba(255, 255, 255, 0.16);
}

.managed-action-tile-btn {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  height: auto;
  min-height: 38px;
  padding: 8px 10px;
  background: transparent !important;
  border: none !important;
  color: var(--text, #f1f5f9) !important;
  font-size: 0.81rem;
  font-weight: 500;
  text-align: left;
  cursor: pointer;
  border-radius: 0;
  min-width: 0;
  overflow: hidden;
}

.managed-action-tile-btn:focus-visible {
  outline: 2px solid var(--primary);
  outline-offset: -2px;
}

.managed-action-tile-icon {
  font-size: 0.95rem;
  color: var(--text-secondary, #c2c8dc);
  flex-shrink: 0;
  width: 16px;
  text-align: center;
}

.managed-action-tile-label {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
  flex: 1 1 auto;
}
```

---

## 6. Testing Strategy (`tests/e2e/server-management-menu.spec.ts`)

Build an authoritative Playwright E2E suite covering:
1. **Layout & Icon Verification (`SCN-SERVER-001`)**:
   - Open Web UI connected to the test router proxy.
   - Click `.managed-actions-trigger`.
   - Verify `.managed-actions-menu` is visible.
   - Verify CSS `grid-template-columns` evaluates to 2 equal columns.
   - Verify container fits cleanly in `.sidebar` without overflow (assert `.sidebar.scrollWidth <= .sidebar.clientWidth`).
   - Assert all 10 actions are present with their corresponding icon classes (`fa-robot`, `fa-play`, `fa-arrows-rotate`, `fa-key`, `fa-network-wired`, `fa-sliders`, `fa-book`, `fa-diagram-project`, `fa-brain`, `fa-gauge-high`).
   - Verify no `.agent-control-pin-btn` elements exist inside `.managed-actions-menu`.
2. **Action Execution (`SCN-SERVER-002`)**:
   - Click "Provider settings".
   - Verify Provider settings dialog opens.
   - Verify `.managed-actions-menu` closes automatically.
3. **Dismissal via Escape and Outside Click (`SCN-SERVER-003`)**:
   - Open `.managed-actions-menu`.
   - Press `Escape` -> Verify menu closes.
   - Open `.managed-actions-menu`.
   - Click outside (e.g. document body or sidebar header) -> Verify menu closes.

---

## 7. Verification Steps

1. Run `npx playwright test tests/e2e/server-management-menu.spec.ts` once implemented.
2. Run `npm --prefix web/router-ui-react run build` to verify type checking and JSX bundling.
3. Ensure no regressions in existing sidebar or agent controls tests.
