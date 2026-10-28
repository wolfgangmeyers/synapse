# ADR 0005: Dark Glassmorphic Cyberpunk Design System & View Hierarchy

- **Status**: Accepted
- **Deciders**: Wolfgang
- **Date**: 2025-10-28

---

## Context & Problem Statement

The Web UI (`web/router-ui-react`) provides the control surface for managing autonomous agents, reviewing timeline feeds, inspecting tools, and configuring system settings.

Previous iterations suffered from aesthetic fragmentation and UX clutter:
1. **Inconsistent Component Styling**: Large blocky rectangular buttons mixed haphazardly with modern compact controls.
2. **Cluttered Modal Layouts**: Configuration modals (MCP Servers, Routers, Providers) mixed creation/edit forms directly into list views, resulting in dense, intimidating, and messy screens.
3. **Inconsistent Visual Tokens**: Varying shades of gray, clashing accent colors, and inconsistent border radii degraded readability and brand cohesion.

A cohesive design system is required to maintain high information density, sleek cyberpunk aesthetics, and predictable navigation patterns.

---

## Decision

Adopt and enforce the **Dark Glassmorphic Cyberpunk Design System** across all UI components in `web/router-ui-react`:

1. **Design Tokens & Dark Glassmorphic Aesthetics**:
   - Deep obsidian background canvases (`#0a0b10`, `#0d0f17`).
   - Translucent glass panels with backdrop blur (`backdrop-filter: blur(12px)`), subtle neon borders (`rgba(56, 189, 248, 0.15)`), and soft outer glows.
   - High-contrast neon accents: Cyan/Sky (`#38bdf8`) for primary actions, Emerald (`#34d399`) for success/active states, Amber (`#fbbf24`) for warnings, and Rose (`#f43f5e`) for destructive/error actions.
2. **Circular Action Buttons (`.icon-btn-round`)**:
   - Small icon-only action buttons across cards, headers, toolbars, and list items must use the compact circular button standard.
   - Fixed dimensions: `24px x 24px`, `border-radius: 999px`.
   - Subtle semi-translucent background, subtle border, smooth hover transitions, and tooltips.
   - Large rectangular buttons for secondary/icon actions are strictly prohibited.
3. **Modal View Hierarchy (Strict Separation of List vs. Form Views)**:
   - Configuration dialogs (MCP servers, Routers, Providers, Settings) must never display permanent inline input form fields on the list view.
   - **List View**: Displays configured items, badges, health indicators, empty state if none exist, and a primary "+ Add <Item>" action in the toolbar/header.
   - **Form View**: Clicking "+ Add" or "Edit" transitions cleanly to a dedicated form view with explicit "Save / Submit" and "Back / Cancel" actions.
   - Canceling or saving returns the user to the refreshed list view.

---

## Invariants & Guarantees

- **No Rectangular Icon Buttons**: Icon-only action triggers in table rows, cards, and headers must use `.icon-btn-round`.
- **Clean List Views**: Modals must default to list view without raw input fields cluttering the overview.
- **Glassmorphic Palette Invariant**: All panels, cards, and headers must reference the unified design tokens in `app.css`. No ad-hoc hex colors or light-mode overrides.
- **Continuous Conversation Timeline**: The conversation timeline maintains continuous visual flow across context resets and compaction boundaries.

---

## Consequences

### Positive
- Unified, professional cyberpunk aesthetic tailored for high-density developer workflows.
- Eliminates visual clutter in configuration dialogs, making them intuitive and error-resistant.
- Compact `.icon-btn-round` buttons preserve screen real estate in dense timeline cards and conversation headers.

### Negative
- Multi-step modal navigation requires managing view state (`list` vs. `add` vs. `edit`) inside modal components.

---

## References

- Design System Specification: `docs/web-ui-design-system.md`
- Master UI Blueprint: `docs/master-conversation-architecture-simplification.md`
- CSS Tokens: `web/router-ui-react/src/app.css`
- Core UI Spec: `docs/specs/INDEX.md`
