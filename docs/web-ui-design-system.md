# Web UI Design System & Architecture

This document defines the visual design system, interaction patterns, component conventions, and architectural standards for the Mecha Web UI (`web/router-ui-react`).

All frontend engineers, subagents, and contributors modifying the Web UI must follow the guidelines detailed below.

---

## 1. Dark Glassmorphic Design System

The Mecha Web UI uses a dark cyberpunk glassmorphism aesthetic characterized by layered translucent surfaces, heavy backdrop blur, subtle luminous borders, and high-contrast neon accents.

### Palette & Tone Mapping
- **Base Canvas & Dark Indigo/Slate**:
  - Sidebar background base: `#070b16`
  - Application canvas background: `#0d1424`
  - Header background: `#121c30`
  - Slate-900 surface / cards: `#0f172a`
  - Dark container fills: `rgba(18, 28, 48, 0.75)` (headers), `rgba(15, 23, 42, 0.55)` (cards/tabs), `rgba(13, 20, 36, 0.45)` (selector pills).
- **Borders & Dividers**:
  - Subtle translucent edge borders: `rgba(255, 255, 255, 0.08)`, `rgba(56, 189, 248, 0.22)`, and `rgba(99, 102, 241, 0.2)`.
- **Neon Accents & Ambient Glows**:
  - **Cyan / Sky Blue** (`#38bdf8` / `#93c5fd`): Brand sigil glow, header borders, active indicators.
  - **Teal** (`#2dd4bf` / `#0d9488`): Reminders active state, chat-only mode, scroll lock, active indicators.
  - **Blue** (`#3b82f6` / `#60a5fa`): Workflow states, System 1 monitors, primary actions.
  - **Purple / Indigo** (`#6366f1` / `#a855f7` / `#c084fc`): Active agent card borders, brand gradient, compacting state glow.
  - **Pink / Magenta** (`#f472b6` / `#be185d`): Active goal states and goal triggers.
  - **Emerald / Green** (`#34d399` / `#4ade80`): Connected connection dots, inline tool enablement.
  - **Red / Danger** (`#ef4444` / `#f87171`): Interrupt worker button, failure states.

### Glassmorphism Rules
- Standard containers and floating panels must include backdrop filters:
  ```css
  backdrop-filter: blur(16px);
  -webkit-backdrop-filter: blur(16px);
  ```
- Layering should maintain visual depth: elevated cards and floating dialogs use darker fills with higher blur values rather than opaque solid backgrounds.

---

## 2. Sidebar Architecture & Visual Standards

The sidebar (`Sidebar.tsx`) functions as the persistent agent navigation hub and connection cockpit.

### Background Art Integration
The sidebar layers atmospheric cityscape and mecha artwork behind a dark multi-stop vertical gradient:
```css
background-color: #070b16;
background-image:
  linear-gradient(180deg, rgba(7, 11, 22, 0.88) 0%, rgba(7, 11, 22, 0.6) 40%, rgba(7, 11, 22, 0.35) 70%, rgba(7, 11, 22, 0.85) 100%),
  url('/sidebar-bg.jpg');
background-size: cover;
background-position: center bottom;
background-repeat: no-repeat;
```
This ensures artwork richness while maintaining high text contrast and legibility for all controls.

### Brand Header (`BrandHeader.tsx`)
- **Glowing Cyan Sigil**: 38×38px SVG icon styled with dual drop shadows:
  ```css
  filter: drop-shadow(0 0 8px #38bdf8) drop-shadow(0 0 16px rgba(129, 140, 248, 0.6));
  ```
- **Gradient Typography**: "Mecha-Developer" title rendered with `linear-gradient(90deg, #38bdf8 0%, #818cf8 55%, #c084fc 100%)`, `-webkit-background-clip: text`, and subtle text shadows.
- **Subtitle**: "AI AGENTS FOR REAL BUILDERS" in bold uppercase `#818cf8` with wide letter spacing (`0.14em`).

### Unified RouterSelector Widget (`RouterSelector.tsx`)
The `RouterSelector` consolidates router connection state, active user identity, and router/agent administration into a single cohesive control card:
- Left squircle cube icon (`<i className="fa-solid fa-cube" />`).
- Two-line layout: bold headline router name (e.g., "Mission control") paired with subtitle (`"Signed in as <user>"` or connection status).
- Right-anchored circular 3-dot ellipsis menu trigger (`.icon-btn-round.managed-actions-trigger`).
- Menu trigger uses event `stopPropagation` to prevent triggering the router selection modal when opening the actions dropdown.
- Strict z-index layering (`z-index: 100`) ensures the managed actions dropdown floats cleanly above agent list items.

### Translucent Agent Cards (`AgentRow.tsx`)
- Agent rows render as dark glassmorphic cards with `background: rgba(15, 23, 42, 0.55)` and `backdrop-filter: blur(12px)`.
- **Active Selection**: indigo glow border (`border: 1.5px solid #6366f1`) and dual outer/inner shadow:
  ```css
  box-shadow: 0 0 18px rgba(99, 102, 241, 0.42), inset 0 0 14px rgba(99, 102, 241, 0.18);
  ```
- **Compacting State**: purple glow border (`#c084fc`) with breathing animation.

### Embedded Default & Agent Tagging Policy
- Router-managed/embedded agents are the default system mode. Redundant "Embedded" or "RPC" tags are omitted from agent cards.
- Only external tmux agents display an explicit source tag (`"tmux"`).

---

## 3. Conversation Header & Action Toolbar

The conversation header (`ConversationHeader.tsx`) anchors the active agent's viewport, providing status, subagents, action toggles, model/prompt selection, and resource metrics.

### Avatar Frame & Scaling
- The agent avatar is scaled up to **95px × 95px**.
- Circular glowing border:
  ```css
  border: 2px solid rgba(56, 189, 248, 0.5);
  box-shadow: 0 0 16px rgba(56, 189, 248, 0.35);
  ```
- Integrated sigil badge anchors the bottom-right corner of the avatar frame.

### Two-Column Partitioned Layout
- **Left Column**:
  - Top row: Agent title, human name, and adjacent action button toolbar.
  - Middle row: Active subagent badges (`.conversation-header-subagents-row`).
  - Bottom row: Context window usage metrics (`.conversation-header-metrics-row`).
  - Tight vertical spacing: whitespace between the subagent row and metrics row is compressed to maximize conversation viewport area.
- **Right Column**:
  - Unified model selector and prompt template selector cards.

### Unified `HeaderActionButton` Component (`HeaderActionButton.tsx`)
All icon buttons in the conversation header toolbar must use the standardized `HeaderActionButton` component implementing `.icon-btn-round`:
- **Dimensions**: **24px × 24px**, circular (`border-radius: 999px`).
- **Styling**: Translucent border, subtle background, centered FontAwesome icon with `aria-hidden="true"`.
- **Active State Color Variations**:
  - **Teal** (`.header-action-button--active-teal`): Reminders, chat-only mode, scroll lock.
    - Border: `rgba(45, 212, 191, 0.75)`, background: `rgba(13, 148, 136, 0.32)`, text: `#2dd4bf`, glow: `0 0 10px rgba(45, 212, 191, 0.45)`.
  - **Pink** (`.header-action-button--active-pink`): Goal state.
    - Border: `rgba(244, 114, 182, 0.75)`, background: `rgba(190, 24, 93, 0.34)`, text: `#f472b6`, glow: `0 0 10px rgba(244, 114, 182, 0.45)`.
  - **Blue** (`.header-action-button--active-blue`): Workflows, System 1 monitors.
    - Border: `rgba(59, 130, 246, 0.75)`, background: `rgba(37, 99, 235, 0.28)`, text: `#60a5fa`, glow: `0 0 10px rgba(59, 130, 246, 0.45)`.
  - **Green** (`.header-action-button--active-green`): Inline tools toggle.
    - Border: `rgba(76, 175, 80, 0.75)`, background: `rgba(46, 125, 50, 0.3)`, text: `#4ade80`, glow: `0 0 10px rgba(74, 222, 128, 0.45)`.
  - **Danger Red** (`.header-action-button--active-danger`): Interrupt worker button.
    - Border: `rgba(239, 68, 68, 0.7)`, background: `rgba(239, 68, 68, 0.22)`, text: `#f87171`, glow: `0 0 10px rgba(239, 68, 68, 0.4)`.

### Unified Prompt & Model Selectors
- Matched dark glass pill styling: height 44px, `border-radius: 12px`, `background: rgba(13, 20, 36, 0.45)`, border `1px solid rgba(59, 130, 246, 0.22)`, `backdrop-filter: blur(16px)`.
- Labels in `#e2e8f0` with active values in `#f8fafc`.
- Integrated `.prompt-manage-button` uses the circular 24×24px gear button for template management.

---

## 4. Timeline & Activity Cards

The conversation stream (`Conversation.tsx`, `MessageList.tsx`, `CompleteTimelineEntry.tsx`) balances narrative human-agent dialogue with machine tool execution.

### Prominent Silver Date Marker Pill
- Activity is visually grouped by calendar date using centered date dividers (`.timeline-date-divider`).
- **Silver Date Label Pill (`.timeline-date-label`)**:
  - Background: `rgba(30, 41, 59, 0.65)` with `backdrop-filter: blur(12px)`.
  - Border: `1px solid rgba(203, 213, 225, 0.22)`.
  - Typography: `#e2e8f0`, `font-size: 0.8rem`, uppercase tracking `0.06em`.
  - Leading calendar icon in muted slate (`#94a3b8`).
  - Subtle shadow: `0 2px 8px rgba(0, 0, 0, 0.25), 0 0 12px rgba(148, 163, 184, 0.08)`.
- **Horizontal Gradient Fade Lines (`.timeline-date-line`)**:
  - 1px divider lines extending to container margins with smooth gradient fade:
    ```css
    linear-gradient(90deg, transparent, rgba(203, 213, 225, 0.28) 20%, rgba(203, 213, 225, 0.28) 80%, transparent)
    ```

### Continuous Timeline Across Context Resets
- Context compaction or epoch boundaries insert a `.timeline-context-reset-divider`.
- Resets do **not** clear historical messages or tool cards from the timeline; the full operational history remains scrollable and inspectable.

### Message & Card Hierarchy
- Human prompts render on the right with dark blue/indigo translucent surfaces.
- Agent narrative responses render as companion chat bubbles with clear avatar branding.
- Tool executions, diffs, command stdout/stderr, and subagent actions render as structured activity cards directly adjacent to relevant turns.

---

## 5. Modal & Dialog Conventions

All modals (`MCPServersDialog`, `ToolDefaultsDialog`, `RouterManagerDialog`, `PromptTemplateDialog`, `AgentMonitorsDialog`, `WorkflowLaunchModal`, `ProviderSettingsDialog`, etc.) must adhere to these standards.

### Standardized Shell & Backdrop
- Modal container (`.dialog-shell`) uses `padding: 22px`, `border-radius: 18px`, and deep translucent dark fills with 16px blur filters.
- Native `::backdrop` or overlay wrapper uses `background: rgba(5, 7, 12, 0.68)` with backdrop blur.
- Dialog headers include a title, description, and top-right `.icon-btn-round` close button.

### Strict Separation of List vs. Form Views
- **No Cluttered Inline Forms**: Dialogs must never clutter list views with permanently visible inline input form fields.
- **List View (Default)**:
  - Lists existing configured items with status indicators and action buttons (Edit, Delete).
  - Empty state message when no items exist.
  - Primary "+ Add <Item>" button placed in the header or toolbar.
- **Form View (Dedicated State)**:
  - Triggered by clicking "+ Add <Item>" or "Edit".
  - Replaces the list view with clean input fields, clear labels, and helper text.
  - Provides explicit "Cancel" / "Back" navigation and a primary "Save" / "Submit" button.
  - Automatically transitions back to the refreshed list view upon saving or canceling.

### Form Inputs & Button Standards
- Form inputs use dark translucent fills (`rgba(15, 23, 42, 0.7)`), 1px subtle borders (`rgba(255, 255, 255, 0.12)`), rounded corners (`8px`), and bright focus rings (`rgba(56, 189, 248, 0.4)`).
- Action buttons across dialogs use `.icon-btn-round` for compact icon actions (24px × 24px) or standard rounded pills (`border-radius: 8px` or `999px`) for labeled text actions.
