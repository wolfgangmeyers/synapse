# UI Symbol and File Map (Part 1: Components and Domain)

This document provides an exhaustive symbol and file inventory for the React Web UI inside `web/router-ui-react/src`:
1. `src/components/` (all 61 `.tsx` and `.ts` files)
2. `src/domain/` (all 13 `.ts` and `.tsx` files)

## Overview Statistics

- **Total Files Audited**: 74
- **Component Files**: 61
- **Domain Files**: 13
- **Total Lines of Code (LOC)**: 15685
- **Total Top-Level Symbols Cataloged**: 476

---

## 1. Components Directory (`web/router-ui-react/src/components`)

### `web/router-ui-react/src/components/ActivityDetailDialog.tsx`

- **Total Lines of Code**: 181 LOC
- **Symbols Defined**: 11

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ActivityDetailDialogProps` | Interface | Exported | L8–L14 | 7 | Defines props contract, configuration flags, and callback handlers for `ActivityDetailDialog`. | None identified (clean/focused). |
| `displayTime` | Function | Unexported (Private) | L16–L19 | 4 | Helper or utility function executing `displayTime` operations and domain transformations. | None identified (clean/focused). |
| `toolInputText` | Function | Unexported (Private) | L21–L23 | 3 | Helper or utility function executing `toolInputText` operations and domain transformations. | None identified (clean/focused). |
| `parseToolInputObject` | Function | Unexported (Private) | L25–L41 | 17 | Helper or utility function executing `parseToolInputObject` operations and domain transformations. | None identified (clean/focused). |
| `renderToolParameters` | Function | Unexported (Private) | L43–L58 | 16 | Helper or utility function executing `renderToolParameters` operations and domain transformations. | None identified (clean/focused). |
| `getToolCommand` | Function | Unexported (Private) | L60–L78 | 19 | Helper or utility function executing `getToolCommand` operations and domain transformations. | None identified (clean/focused). |
| `cleanOutput` | Function | Unexported (Private) | L80–L84 | 5 | Helper or utility function executing `cleanOutput` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^exit=0[ \t]*(?:\r?\n\|$)/`). |
| `formatError` | Function | Unexported (Private) | L86–L89 | 4 | Helper or utility function executing `formatError` operations and domain transformations. | None identified (clean/focused). |
| `isRedundantSummary` | Function | Unexported (Private) | L91–L101 | 11 | Helper or utility function executing `isRedundantSummary` operations and domain transformations. | None identified (clean/focused). |
| `getMailboxSendDetails` | Function | Unexported (Private) | L103–L114 | 12 | Helper or utility function executing `getMailboxSendDetails` operations and domain transformations. | None identified (clean/focused). |
| `ActivityDetailDialog` | Component | Exported | L117–L180 | 64 | Details for the router-projected metadata shown by Complete activity cards. (Modal dialog component rendering UI shell, form fields, and action buttons for ActivityDetail.) | None identified (clean/focused). |

### `web/router-ui-react/src/components/AgentAvatar.tsx`

- **Total Lines of Code**: 110 LOC
- **Symbols Defined**: 9

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `avatarModules` | Constant | Unexported (Private) | L3–L7 | 5 | Module constant or configuration value for `avatarModules`. | None identified (clean/focused). |
| `avatarAssets` | Constant | Unexported (Private) | L9–L9 | 1 | Module constant or configuration value for `avatarAssets`. | None identified (clean/focused). |
| `brightAvatarColors` | Constant | Unexported (Private) | L11–L48 | 38 | Module constant or configuration value for `brightAvatarColors`. | None identified (clean/focused). |
| `hashAgentName` | Function | Unexported (Private) | L50–L57 | 8 | Helper or utility function executing `hashAgentName` operations and domain transformations. | None identified (clean/focused). |
| `avatarCandidateScore` | Function | Unexported (Private) | L59–L61 | 3 | Helper or utility function executing `avatarCandidateScore` operations and domain transformations. | None identified (clean/focused). |
| `selectRendezvousAvatarKey` | Function | Exported | L63–L76 | 14 | Helper or utility function executing `selectRendezvousAvatarKey` operations and domain transformations. | None identified (clean/focused). |
| `AgentAvatarAppearance` | Interface | Exported | L78–L81 | 4 | TypeScript type/interface representing `AgentAvatarAppearance` domain model or contract. | None identified (clean/focused). |
| `avatarForAgent` | Function | Exported | L83–L92 | 10 | Helper or utility function executing `avatarForAgent` operations and domain transformations. | None identified (clean/focused). |
| `AgentAvatar` | Component | Exported | L94–L109 | 16 | React UI component rendering and managing visual presentation and interaction for `AgentAvatar`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/AgentControlsDropdown.tsx`

- **Total Lines of Code**: 165 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `AgentControlItem` | Interface | Exported | L4–L10 | 7 | TypeScript type/interface representing `AgentControlItem` domain model or contract. | None identified (clean/focused). |
| `AgentControlsDropdownProps` | Interface | Exported | L12–L34 | 23 | Defines props contract, configuration flags, and callback handlers for `AgentControlsDropdown`. | None identified (clean/focused). |
| `AgentControlsDropdown` | Component | Exported | L36–L164 | 129 | React UI component rendering and managing visual presentation and interaction for `AgentControlsDropdown`. | **Monolithic** (129 LOC > 100). **Timers**: Uses timer scheduling (`setTimeout` / `setInterval`). |

### `web/router-ui-react/src/components/AgentList.tsx`

- **Total Lines of Code**: 33 LOC
- **Symbols Defined**: 4

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `AgentListProps` | Interface | Exported | L7–L23 | 17 | Defines props contract, configuration flags, and callback handlers for `AgentList`. | None identified (clean/focused). |
| `arrivalFor` | Function | Unexported (Private) | L24–L24 | 1 | Helper or utility function executing `arrivalFor` operations and domain transformations. | None identified (clean/focused). |
| `reminderCountFor` | Function | Unexported (Private) | L25–L25 | 1 | Helper or utility function executing `reminderCountFor` operations and domain transformations. | None identified (clean/focused). |
| `AgentList` | Component | Exported | L26–L32 | 7 | React UI component rendering and managing visual presentation and interaction for `AgentList`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/AgentMonitorsDialog.tsx`

- **Total Lines of Code**: 561 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `AgentMonitorsDialogProps` | Interface | Exported | L6–L12 | 7 | Defines props contract, configuration flags, and callback handlers for `AgentMonitorsDialog`. | None identified (clean/focused). |
| `DEFAULT_NEW_MONITOR` | Constant | Unexported (Private) | L14–L28 | 15 | Module constant or configuration value for `DEFAULT_NEW_MONITOR`. | None identified (clean/focused). |
| `AgentMonitorsDialog` | Component | Exported | L30–L560 | 531 | Modal dialog component rendering UI shell, form fields, and action buttons for AgentMonitors. | **Monolithic** (531 LOC > 100). |

### `web/router-ui-react/src/components/AgentRow.tsx`

- **Total Lines of Code**: 123 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `AgentRowProps` | Interface | Exported | L10–L25 | 16 | Defines props contract, configuration flags, and callback handlers for `AgentRow`. | None identified (clean/focused). |
| `activeManagedSubagents` | Function | Unexported (Private) | L26–L32 | 7 | Helper or utility function executing `activeManagedSubagents` operations and domain transformations. | None identified (clean/focused). |
| `AgentRow` | Component | Exported | L34–L122 | 89 | React UI component rendering and managing visual presentation and interaction for `AgentRow`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/AlienText.tsx`

- **Total Lines of Code**: 49 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `AlienTextProps` | Interface | Exported | L4–L11 | 8 | Defines props contract, configuration flags, and callback handlers for `AlienText`. | None identified (clean/focused). |
| `AlienText` | Component | Exported | L13–L48 | 36 | React UI component rendering and managing visual presentation and interaction for `AlienText`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/AppShell.tsx`

- **Total Lines of Code**: 15 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `AppShellProps` | Interface | Exported | L3–L7 | 5 | Defines props contract, configuration flags, and callback handlers for `AppShell`. | None identified (clean/focused). |
| `AppShell` | Component | Exported | L9–L14 | 6 | React UI component rendering and managing visual presentation and interaction for `AppShell`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ArchivedAgentView.tsx`

- **Total Lines of Code**: 13 LOC
- **Symbols Defined**: 1

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ArchivedAgentView` | Component | Exported | L4–L12 | 9 | Top-level view page component orchestrating visual layout, subcomponents, and operations for ArchivedAgentView. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ArrivalDialog.tsx`

- **Total Lines of Code**: 25 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ArrivalDialogProps` | Interface | Exported | L7–L13 | 7 | Defines props contract, configuration flags, and callback handlers for `ArrivalDialog`. | None identified (clean/focused). |
| `ArrivalDialog` | Component | Exported | L15–L24 | 10 | Modal dialog component rendering UI shell, form fields, and action buttons for Arrival. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ArrivalIndicator.tsx`

- **Total Lines of Code**: 16 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ArrivalIndicatorProps` | Interface | Exported | L4–L4 | 1 | Defines props contract, configuration flags, and callback handlers for `ArrivalIndicator`. | None identified (clean/focused). |
| `ArrivalIndicator` | Component | Exported | L5–L15 | 11 | React UI component rendering and managing visual presentation and interaction for `ArrivalIndicator`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/AttachmentGallery.tsx`

- **Total Lines of Code**: 50 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `AttachmentGalleryProps` | Interface | Exported | L5–L5 | 1 | Defines props contract, configuration flags, and callback handlers for `AttachmentGallery`. | None identified (clean/focused). |
| `urlFor` | Function | Unexported (Private) | L6–L16 | 11 | Helper or utility function executing `urlFor` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/v1/`, `/^https?:\/`, `/v1/`). |
| `AttachmentGallery` | Component | Exported | L17–L49 | 33 | React UI component rendering and managing visual presentation and interaction for `AttachmentGallery`. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^https?:\/`, `/.test(item.url) \|\| item.url.startsWith("blob:") \|\| item.url.startsWith("/`, `/attachments/`). |

### `web/router-ui-react/src/components/BrandHeader.tsx`

- **Total Lines of Code**: 57 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `BrandHeaderProps` | Interface | Exported | L3–L9 | 7 | Defines props contract, configuration flags, and callback handlers for `BrandHeader`. | None identified (clean/focused). |
| `BrandHeader` | Component | Exported | L11–L56 | 46 | React UI component rendering and managing visual presentation and interaction for `BrandHeader`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/CompleteTimelineEntry.tsx`

- **Total Lines of Code**: 849 LOC
- **Symbols Defined**: 35

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `CompleteTimelineTone` | Type Alias | Exported | L11–L11 | 1 | TypeScript type/interface representing `CompleteTimelineTone` domain model or contract. | None identified (clean/focused). |
| `CompleteTimelineVisual` | Interface | Exported | L13–L21 | 9 | TypeScript type/interface representing `CompleteTimelineVisual` domain model or contract. | None identified (clean/focused). |
| `cleanOneLine` | Function | Exported | L23–L33 | 11 | Helper or utility function executing `cleanOneLine` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^Mailbox message batch\r?\n+/i`, `/^(?:(?:From\|To\|Subject):[^\n]*\r?\n)+/gi`, `/\[Reply via mailbox[^\]]*\]/gi`). |
| `cleanOutput` | Function | Unexported (Private) | L35–L39 | 5 | Helper or utility function executing `cleanOutput` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^exit=0[ \t]*(?:\r?\n\|$)/`). |
| `getMailboxSendDetails` | Function | Exported | L41–L51 | 11 | Helper or utility function executing `getMailboxSendDetails` operations and domain transformations. | None identified (clean/focused). |
| `isMailboxSendToUser` | Function | Exported | L53–L106 | 54 | Helper or utility function executing `isMailboxSendToUser` operations and domain transformations. | None identified (clean/focused). |
| `getThinkingContent` | Function | Exported | L108–L121 | 14 | Helper or utility function executing `getThinkingContent` operations and domain transformations. | None identified (clean/focused). |
| `toolParamPreview` | Function | Unexported (Private) | L123–L170 | 48 | Helper or utility function executing `toolParamPreview` operations and domain transformations. | None identified (clean/focused). |
| `extractSubjectFromText` | Function | Unexported (Private) | L172–L180 | 9 | Helper or utility function executing `extractSubjectFromText` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^(?:From:[^\n]*\r?\n)?Subject:[ \t]*([^\r\n]+)/im`). |
| `extractSubject` | Function | Unexported (Private) | L182–L197 | 16 | Helper or utility function executing `extractSubject` operations and domain transformations. | None identified (clean/focused). |
| `normalizedToolName` | Function | Unexported (Private) | L199–L202 | 4 | Helper or utility function executing `normalizedToolName` operations and domain transformations. | None identified (clean/focused). |
| `isContextResetEntry` | Function | Exported | L204–L210 | 7 | Helper or utility function executing `isContextResetEntry` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^Context reset\b/i`). |
| `extractEpoch` | Function | Exported | L212–L225 | 14 | Helper or utility function executing `extractEpoch` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/[Ee]poch\s*(\d+)/`, `/[Ee]poch\s*(\d+)/`). |
| `titleCase` | Function | Unexported (Private) | L227–L229 | 3 | Helper or utility function executing `titleCase` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/[._-]/`). |
| `managedContextTooSmallErrorCode` | Constant | Unexported (Private) | L231–L231 | 1 | Module constant or configuration value for `managedContextTooSmallErrorCode`. | None identified (clean/focused). |
| `ROUTINE_TOOLS` | Constant | Unexported (Private) | L232–L232 | 1 | Module constant or configuration value for `ROUTINE_TOOLS`. | None identified (clean/focused). |
| `isRoutineTool` | Function | Unexported (Private) | L234–L238 | 5 | Helper or utility function executing `isRoutineTool` operations and domain transformations. | None identified (clean/focused). |
| `hasSafeClassifiedReason` | Function | Unexported (Private) | L240–L242 | 3 | Helper or utility function executing `hasSafeClassifiedReason` operations and domain transformations. | None identified (clean/focused). |
| `getToolIcon` | Function | Unexported (Private) | L244–L270 | 27 | Helper or utility function executing `getToolIcon` operations and domain transformations. | None identified (clean/focused). |
| `toolVisual` | Function | Unexported (Private) | L272–L339 | 68 | Helper or utility function executing `toolVisual` operations and domain transformations. | None identified (clean/focused). |
| `ChildTerminalOutcome` | Type Alias | Unexported (Private) | L341–L341 | 1 | TypeScript type/interface representing `ChildTerminalOutcome` domain model or contract. | None identified (clean/focused). |
| `childTerminalOutcome` | Function | Unexported (Private) | L343–L363 | 21 | Helper or utility function executing `childTerminalOutcome` operations and domain transformations. | None identified (clean/focused). |
| `childVisual` | Function | Unexported (Private) | L365–L372 | 8 | Helper or utility function executing `childVisual` operations and domain transformations. | None identified (clean/focused). |
| `subagentCompleteVisual` | Function | Unexported (Private) | L374–L416 | 43 | Helper or utility function executing `subagentCompleteVisual` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^Subagent (?:completed\|finished\|failed):\s*(.+)$/i`). |
| `compactionVisual` | Function | Unexported (Private) | L418–L439 | 22 | Helper or utility function executing `compactionVisual` operations and domain transformations. | None identified (clean/focused). |
| `completeTimelineVisual` | Function | Exported | L441–L575 | 135 | Helper or utility function executing `completeTimelineVisual` operations and domain transformations. | **Monolithic** (135 LOC > 100). |
| `isChildOrRecoveryPresentation` | Function | Unexported (Private) | L577–L579 | 3 | Helper or utility function executing `isChildOrRecoveryPresentation` operations and domain transformations. | None identified (clean/focused). |
| `isActivePresentation` | Function | Unexported (Private) | L581–L586 | 6 | Helper or utility function executing `isActivePresentation` operations and domain transformations. | None identified (clean/focused). |
| `isActiveManagedSubagentLifecycle` | Function | Exported | L588–L590 | 3 | Helper or utility function executing `isActiveManagedSubagentLifecycle` operations and domain transformations. | None identified (clean/focused). |
| `isTerminalSuccessPresentation` | Function | Unexported (Private) | L592–L601 | 10 | Helper or utility function executing `isTerminalSuccessPresentation` operations and domain transformations. | None identified (clean/focused). |
| `isTerminalDangerPresentation` | Function | Unexported (Private) | L603–L613 | 11 | Helper or utility function executing `isTerminalDangerPresentation` operations and domain transformations. | None identified (clean/focused). |
| `formattedTime` | Function | Unexported (Private) | L615–L619 | 5 | Helper or utility function executing `formattedTime` operations and domain transformations. | None identified (clean/focused). |
| `ThinkingTraceCard` | Component | Exported | L621–L709 | 89 | React UI component rendering and managing visual presentation and interaction for `ThinkingTraceCard`. | None identified (clean/focused). |
| `CompleteTimelineEntryProps` | Interface | Exported | L711–L715 | 5 | Defines props contract, configuration flags, and callback handlers for `CompleteTimelineEntry`. | None identified (clean/focused). |
| `CompleteTimelineEntry` | Component | Exported | L717–L848 | 132 | React UI component rendering and managing visual presentation and interaction for `CompleteTimelineEntry`. | **Monolithic** (132 LOC > 100). |

### `web/router-ui-react/src/components/Composer.tsx`

- **Total Lines of Code**: 106 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ComposerProps` | Interface | Exported | L6–L18 | 13 | Defines props contract, configuration flags, and callback handlers for `Composer`. | None identified (clean/focused). |
| `normalizeNewlines` | Function | Unexported (Private) | L19–L19 | 1 | Helper or utility function executing `normalizeNewlines` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/\\r\\n\|\\n\|\\r/g`). |
| `Composer` | Component | Exported | L20–L104 | 85 | React UI component rendering and managing visual presentation and interaction for `Composer`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ConnectionStatus.tsx`

- **Total Lines of Code**: 16 LOC
- **Symbols Defined**: 5

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ConnectionState` | Type Alias | Exported | L4–L4 | 1 | Defines data model and mutable/immutable properties representing ConnectionState. | None identified (clean/focused). |
| `ConnectionStatusProps` | Interface | Exported | L5–L5 | 1 | Defines props contract, configuration flags, and callback handlers for `ConnectionStatus`. | None identified (clean/focused). |
| `connectionLabels` | Constant | Exported | L6–L6 | 1 | Module constant or configuration value for `connectionLabels`. | None identified (clean/focused). |
| `labels` | Constant | Unexported (Private) | L7–L7 | 1 | Module constant or configuration value for `labels`. | None identified (clean/focused). |
| `ConnectionStatus` | Component | Exported | L9–L15 | 7 | React UI component rendering and managing visual presentation and interaction for `ConnectionStatus`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ContextThresholdDialog.tsx`

- **Total Lines of Code**: 393 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ContextThresholdDialogProps` | Interface | Exported | L12–L18 | 7 | Defines props contract, configuration flags, and callback handlers for `ContextThresholdDialog`. | None identified (clean/focused). |
| `ContextThresholdDialog` | Component | Exported | L20–L392 | 373 | Modal dialog component rendering UI shell, form fields, and action buttons for ContextThreshold. | **Monolithic** (373 LOC > 100). **Complex Effects**: Uses 3 `useEffect` hooks. |

### `web/router-ui-react/src/components/Conversation.tsx`

- **Total Lines of Code**: 81 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ConversationProps` | Interface | Exported | L17–L66 | 50 | Defines props contract, configuration flags, and callback handlers for `Conversation`. | None identified (clean/focused). |
| `isManagedAgent` | Function | Unexported (Private) | L68–L71 | 4 | Helper or utility function executing `isManagedAgent` operations and domain transformations. | None identified (clean/focused). |
| `Conversation` | Component | Exported | L73–L80 | 8 | React UI component rendering and managing visual presentation and interaction for `Conversation`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ConversationHeader.tsx`

- **Total Lines of Code**: 916 LOC
- **Symbols Defined**: 7

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ConversationView` | Type Alias | Exported | L21–L21 | 1 | TypeScript type/interface representing `ConversationView` domain model or contract. | None identified (clean/focused). |
| `ConversationHeaderProps` | Interface | Exported | L23–L52 | 30 | Defines props contract, configuration flags, and callback handlers for `ConversationHeader`. | None identified (clean/focused). |
| `activeManagedSubagents` | Function | Unexported (Private) | L55–L61 | 7 | Helper or utility function executing `activeManagedSubagents` operations and domain transformations. | None identified (clean/focused). |
| `failedManagedAgent` | Function | Unexported (Private) | L63–L66 | 4 | Helper or utility function executing `failedManagedAgent` operations and domain transformations. | None identified (clean/focused). |
| `operationalStatus` | Function | Unexported (Private) | L68–L81 | 14 | Helper or utility function executing `operationalStatus` operations and domain transformations. | None identified (clean/focused). |
| `isManagedAgent` | Function | Unexported (Private) | L83–L86 | 4 | Helper or utility function executing `isManagedAgent` operations and domain transformations. | None identified (clean/focused). |
| `ConversationHeader` | Component | Exported | L88–L915 | 828 | React UI component rendering and managing visual presentation and interaction for `ConversationHeader`. | **Monolithic** (828 LOC > 100). **Hidden State**: Accesses `localStorage`/`sessionStorage` directly. **Timers**: Uses timer scheduling (`setTimeout` / `setInterval`). **State Bloat**: Uses 8 `useState` hooks. **Complex Effects**: Uses 5 `useEffect` hooks. |

### `web/router-ui-react/src/components/DialogShell.tsx`

- **Total Lines of Code**: 108 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `DialogShellProps` | Interface | Exported | L3–L16 | 14 | Defines props contract, configuration flags, and callback handlers for `DialogShell`. | None identified (clean/focused). |
| `DialogShell` | Component | Exported | L19–L107 | 89 | Native dialog semantics with a small focus restoration contract shared by controls. (React UI component rendering and managing visual presentation and interaction for `DialogShell`.) | **Regex / Complex Parsing**: Uses regular expression parsing (`/[^a-z0-9]+/gi`). |

### `web/router-ui-react/src/components/EdgeConfigModal.tsx`

- **Total Lines of Code**: 82 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `EdgeConfigModalProps` | Interface | Exported | L4–L11 | 8 | Defines props contract, configuration flags, and callback handlers for `EdgeConfigModal`. | None identified (clean/focused). |
| `EdgeConfigModal` | Component | Exported | L13–L81 | 69 | Modal dialog component rendering UI shell, form fields, and action buttons for EdgeConfig. | None identified (clean/focused). |

### `web/router-ui-react/src/components/EmptyState.tsx`

- **Total Lines of Code**: 6 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `EmptyStateProps` | Interface | Exported | L2–L2 | 1 | Defines props contract, configuration flags, and callback handlers for `EmptyState`. | None identified (clean/focused). |
| `EmptyState` | Component | Exported | L3–L5 | 3 | React UI component rendering and managing visual presentation and interaction for `EmptyState`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ErrorBanner.tsx`

- **Total Lines of Code**: 31 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ErrorBannerProps` | Interface | Exported | L3–L8 | 6 | Defines props contract, configuration flags, and callback handlers for `ErrorBanner`. | None identified (clean/focused). |
| `ErrorBanner` | Component | Exported | L10–L30 | 21 | React UI component rendering and managing visual presentation and interaction for `ErrorBanner`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/GoalEditorDialog.tsx`

- **Total Lines of Code**: 81 LOC
- **Symbols Defined**: 5

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `GoalEditorDialogProps` | Interface | Exported | L5–L16 | 12 | Defines props contract, configuration flags, and callback handlers for `GoalEditorDialog`. | None identified (clean/focused). |
| `editorFromState` | Function | Unexported (Private) | L18–L30 | 13 | Helper or utility function executing `editorFromState` operations and domain transformations. | None identified (clean/focused). |
| `Toggle` | Component | Unexported (Private) | L32–L36 | 5 | React UI component rendering and managing visual presentation and interaction for `Toggle`. | None identified (clean/focused). |
| `GoalEditorDialog` | Component | Exported | L38–L76 | 39 | Modal dialog component rendering UI shell, form fields, and action buttons for GoalEditor. | None identified (clean/focused). |
| `NumberField` | Component | Unexported (Private) | L78–L80 | 3 | React UI component rendering and managing visual presentation and interaction for `NumberField`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/HeaderActionButton.tsx`

- **Total Lines of Code**: 43 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `HeaderActionTone` | Type Alias | Exported | L3–L3 | 1 | TypeScript type/interface representing `HeaderActionTone` domain model or contract. | None identified (clean/focused). |
| `HeaderActionButtonProps` | Interface | Exported | L5–L11 | 7 | Defines props contract, configuration flags, and callback handlers for `HeaderActionButton`. | None identified (clean/focused). |
| `HeaderActionButton` | Component | Exported | L13–L42 | 30 | React UI component rendering and managing visual presentation and interaction for `HeaderActionButton`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ImagePreview.tsx`

- **Total Lines of Code**: 6 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ImagePreviewProps` | Interface | Exported | L4–L4 | 1 | Defines props contract, configuration flags, and callback handlers for `ImagePreview`. | None identified (clean/focused). |
| `ImagePreview` | Component | Exported | L5–L5 | 1 | React UI component rendering and managing visual presentation and interaction for `ImagePreview`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ImportWorkflowModal.tsx`

- **Total Lines of Code**: 205 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ImportWorkflowModalProps` | Interface | Exported | L5–L9 | 5 | Defines props contract, configuration flags, and callback handlers for `ImportWorkflowModal`. | None identified (clean/focused). |
| `ImportWorkflowModal` | Component | Exported | L11–L204 | 194 | Modal dialog component rendering UI shell, form fields, and action buttons for ImportWorkflow. | **Monolithic** (194 LOC > 100). |

### `web/router-ui-react/src/components/InlineToolSettingsDialog.tsx`

- **Total Lines of Code**: 36 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `InlineToolSettingsDialogProps` | Interface | Exported | L4–L14 | 11 | Defines props contract, configuration flags, and callback handlers for `InlineToolSettingsDialog`. | None identified (clean/focused). |
| `InlineToolSettingsDialog` | Component | Exported | L16–L29 | 14 | Modal dialog component rendering UI shell, form fields, and action buttons for InlineToolSettings. | None identified (clean/focused). |
| `Toggle` | Component | Unexported (Private) | L31–L35 | 5 | React UI component rendering and managing visual presentation and interaction for `Toggle`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/MCPServersDialog.tsx`

- **Total Lines of Code**: 409 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `MCPServersDialogProps` | Interface | Exported | L5–L9 | 5 | Defines props contract, configuration flags, and callback handlers for `MCPServersDialog`. | None identified (clean/focused). |
| `MCPServersDialog` | Component | Exported | L11–L408 | 398 | Modal dialog component rendering UI shell, form fields, and action buttons for MCPServers. | **Monolithic** (398 LOC > 100). |

### `web/router-ui-react/src/components/ManagedAgentDialog.tsx`

- **Total Lines of Code**: 173 LOC
- **Symbols Defined**: 4

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `normalizeManagedAgentName` | Function | Exported | L8–L8 | 1 | Helper or utility function executing `normalizeManagedAgentName` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/\s+/g`). |
| `ManagedAgentDialogProps` | Interface | Exported | L10–L22 | 13 | Defines props contract, configuration flags, and callback handlers for `ManagedAgentDialog`. | None identified (clean/focused). |
| `isGoogleProvider` | Function | Exported | L24–L26 | 3 | Helper or utility function executing `isGoogleProvider` operations and domain transformations. | None identified (clean/focused). |
| `ManagedAgentDialog` | Component | Exported | L28–L172 | 145 | Modal dialog component rendering UI shell, form fields, and action buttons for ManagedAgent. | **Monolithic** (145 LOC > 100). **Regex / Complex Parsing**: Uses regular expression parsing (`/\s+/g`, `/button><button type="button" className="secondary" disabled={pending} onClick={() => { void onAction(agent.agent_id, "reset"); }}>Reset</`, `/button></`). **Timers**: Uses timer scheduling (`setTimeout` / `setInterval`). **State Bloat**: Uses 10 `useState` hooks. **Complex Effects**: Uses 4 `useEffect` hooks. |

### `web/router-ui-react/src/components/ManagedChildList.tsx`

- **Total Lines of Code**: 40 LOC
- **Symbols Defined**: 4

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `childTiming` | Function | Unexported (Private) | L4–L9 | 6 | Helper or utility function executing `childTiming` operations and domain transformations. | None identified (clean/focused). |
| `childRecovery` | Function | Unexported (Private) | L11–L18 | 8 | Helper or utility function executing `childRecovery` operations and domain transformations. | None identified (clean/focused). |
| `displayedChildStatus` | Function | Unexported (Private) | L20–L22 | 3 | Helper or utility function executing `displayedChildStatus` operations and domain transformations. | None identified (clean/focused). |
| `ManagedChildList` | Component | Exported | L24–L39 | 16 | React UI component rendering and managing visual presentation and interaction for `ManagedChildList`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ManagedDirectoryPicker.tsx`

- **Total Lines of Code**: 257 LOC
- **Symbols Defined**: 6

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ManagedDirectory` | Interface | Unexported (Private) | L4–L7 | 4 | TypeScript type/interface representing `ManagedDirectory` domain model or contract. | None identified (clean/focused). |
| `DirectoryListing` | Interface | Unexported (Private) | L9–L14 | 6 | TypeScript type/interface representing `DirectoryListing` domain model or contract. | None identified (clean/focused). |
| `ManagedDirectoryPickerProps` | Interface | Exported | L16–L22 | 7 | Defines props contract, configuration flags, and callback handlers for `ManagedDirectoryPicker`. | None identified (clean/focused). |
| `pathFromText` | Function | Unexported (Private) | L24–L34 | 11 | Helper or utility function executing `pathFromText` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^['"]\|['"]$/g`, `/") \|\| candidate === "~" \|\| candidate.startsWith("~/`). |
| `localFolderFromDrop` | Function | Unexported (Private) | L36–L62 | 27 | Helper or utility function executing `localFolderFromDrop` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/uri-list", "text/`, `/ virtual root (for example, `/`). |
| `ManagedDirectoryPicker` | Component | Exported | L66–L256 | 191 | The launch form keeps its freeform input stable. Folder navigation belongs to  the separate native dialog, and selection is committed only from that dialog. (React UI component rendering and managing visual presentation and interaction for `ManagedDirectoryPicker`.) | **Monolithic** (191 LOC > 100). **State Bloat**: Uses 8 `useState` hooks. |

### `web/router-ui-react/src/components/MessageBubble.tsx`

- **Total Lines of Code**: 62 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `MessageBubbleProps` | Interface | Exported | L11–L11 | 1 | Defines props contract, configuration flags, and callback handlers for `MessageBubble`. | None identified (clean/focused). |
| `MessageBubble` | Component | Exported | L12–L61 | 50 | React UI component rendering and managing visual presentation and interaction for `MessageBubble`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/MessageList.tsx`

- **Total Lines of Code**: 673 LOC
- **Symbols Defined**: 12

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `MessageListProps` | Interface | Exported | L11–L28 | 18 | Defines props contract, configuration flags, and callback handlers for `MessageList`. | None identified (clean/focused). |
| `FeedItem` | Type Alias | Unexported (Private) | L30–L30 | 1 | TypeScript type/interface representing `FeedItem` domain model or contract. | None identified (clean/focused). |
| `TimelineOrbTone` | Type Alias | Exported | L32–L32 | 1 | TypeScript type/interface representing `TimelineOrbTone` domain model or contract. | None identified (clean/focused). |
| `getItemOrbTone` | Function | Exported | L34–L52 | 19 | Helper or utility function executing `getItemOrbTone` operations and domain transformations. | None identified (clean/focused). |
| `activityTime` | Function | Unexported (Private) | L54–L57 | 4 | Helper or utility function executing `activityTime` operations and domain transformations. | None identified (clean/focused). |
| `activitySequence` | Function | Unexported (Private) | L59–L64 | 6 | Helper or utility function executing `activitySequence` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^complete:(\d+)$/`). |
| `isMeaningfulCompleteActivity` | Function | Unexported (Private) | L66–L99 | 34 | Helper or utility function executing `isMeaningfulCompleteActivity` operations and domain transformations. | None identified (clean/focused). |
| `dateLabel` | Function | Unexported (Private) | L101–L104 | 4 | Helper or utility function executing `dateLabel` operations and domain transformations. | None identified (clean/focused). |
| `normalizeMessageBodyForComparison` | Function | Unexported (Private) | L106–L111 | 6 | Helper or utility function executing `normalizeMessageBodyForComparison` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/(?:\r?\n\r?\n)?Attached images?:\s*(?:\d+\.\s*[^\r\n]+\s*)+$/gi`). |
| `timelineEventToFeedItems` | Function | Unexported (Private) | L113–L305 | 193 | Helper or utility function executing `timelineEventToFeedItems` operations and domain transformations. | **Monolithic** (193 LOC > 100). **Regex / Complex Parsing**: Uses regular expression parsing (`/ 1. Subagent completion notifications /`, `/ If sender is system or content has [Direct message from system] /`, `/ If explicitly addressed between agents (neither sender nor recipient is user/`). **Type Cast**: Uses `as any` or double casting. |
| `isAlreadyInMessages` | Function | Unexported (Private) | L307–L334 | 28 | Helper or utility function executing `isAlreadyInMessages` operations and domain transformations. | None identified (clean/focused). |
| `MessageList` | Component | Exported | L336–L672 | 337 | React UI component rendering and managing visual presentation and interaction for `MessageList`. | **Monolithic** (337 LOC > 100). **Complex Effects**: Uses 3 `useEffect` hooks. |

### `web/router-ui-react/src/components/ModelPicker.tsx`

- **Total Lines of Code**: 107 LOC
- **Symbols Defined**: 4

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ModelPickerEmptyOption` | Interface | Exported | L5–L9 | 5 | TypeScript type/interface representing `ModelPickerEmptyOption` domain model or contract. | None identified (clean/focused). |
| `ModelPickerProps` | Interface | Exported | L11–L25 | 15 | Defines props contract, configuration flags, and callback handlers for `ModelPicker`. | None identified (clean/focused). |
| `ADMITTED_EMBEDDED_PROVIDERS` | Constant | Unexported (Private) | L27–L27 | 1 | Module constant or configuration value for `ADMITTED_EMBEDDED_PROVIDERS`. | None identified (clean/focused). |
| `ModelPicker` | Component | Exported | L29–L106 | 78 | React UI component rendering and managing visual presentation and interaction for `ModelPicker`. | **Complex Effects**: Uses 3 `useEffect` hooks. |

### `web/router-ui-react/src/components/ProgressHistoryModal.tsx`

- **Total Lines of Code**: 170 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ProgressHistoryEntry` | Interface | Exported | L3–L7 | 5 | TypeScript type/interface representing `ProgressHistoryEntry` domain model or contract. | None identified (clean/focused). |
| `ProgressHistoryModalProps` | Interface | Unexported (Private) | L9–L13 | 5 | Defines props contract, configuration flags, and callback handlers for `ProgressHistoryModal`. | None identified (clean/focused). |
| `ProgressHistoryModal` | Component | Exported | L15–L168 | 154 | Modal dialog component rendering UI shell, form fields, and action buttons for ProgressHistory. | **Monolithic** (154 LOC > 100). |

### `web/router-ui-react/src/components/PromptControls.tsx`

- **Total Lines of Code**: 125 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `PromptControlsProps` | Interface | Exported | L6–L13 | 8 | Defines props contract, configuration flags, and callback handlers for `PromptControls`. | None identified (clean/focused). |
| `PromptControls` | Component | Exported | L15–L121 | 107 | React UI component rendering and managing visual presentation and interaction for `PromptControls`. | **Monolithic** (107 LOC > 100). |

### `web/router-ui-react/src/components/PromptTemplateDialog.tsx`

- **Total Lines of Code**: 349 LOC
- **Symbols Defined**: 4

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `PromptTemplateDialogProps` | Interface | Exported | L8–L24 | 17 | Defines props contract, configuration flags, and callback handlers for `PromptTemplateDialog`. | None identified (clean/focused). |
| `Mode` | Type Alias | Unexported (Private) | L26–L26 | 1 | TypeScript type/interface representing `Mode` domain model or contract. | None identified (clean/focused). |
| `PromptTemplateDialog` | Component | Exported | L28–L346 | 319 | Modal dialog component rendering UI shell, form fields, and action buttons for PromptTemplate. | **Monolithic** (319 LOC > 100). **Timers**: Uses timer scheduling (`setTimeout` / `setInterval`). **State Bloat**: Uses 6 `useState` hooks. |
| `Actions` | Component | Unexported (Private) | L348–L348 | 1 | React UI component rendering and managing visual presentation and interaction for `Actions`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ProviderAuthDialog.tsx`

- **Total Lines of Code**: 572 LOC
- **Symbols Defined**: 11

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ProviderAuthClient` | Type Alias | Exported | L6–L9 | 4 | TypeScript type/interface representing `ProviderAuthClient` domain model or contract. | None identified (clean/focused). |
| `ProviderAuthDialogProps` | Interface | Exported | L11–L18 | 8 | Defines props contract, configuration flags, and callback handlers for `ProviderAuthDialog`. | None identified (clean/focused). |
| `GoogleAuthMethod` | Type Alias | Exported | L20–L20 | 1 | TypeScript type/interface representing `GoogleAuthMethod` domain model or contract. | None identified (clean/focused). |
| `googleMethodToProvider` | Constant | Unexported (Private) | L22–L26 | 5 | Module constant or configuration value for `googleMethodToProvider`. | None identified (clean/focused). |
| `googleMethodLabels` | Constant | Unexported (Private) | L28–L32 | 5 | Module constant or configuration value for `googleMethodLabels`. | None identified (clean/focused). |
| `providerLabels` | Constant | Unexported (Private) | L34–L40 | 7 | Module constant or configuration value for `providerLabels`. | None identified (clean/focused). |
| `isGoogleAuthMethod` | Function | Unexported (Private) | L42–L44 | 3 | Helper or utility function executing `isGoogleAuthMethod` operations and domain transformations. | None identified (clean/focused). |
| `computeInitialGoogleMethod` | Function | Unexported (Private) | L46–L55 | 10 | Helper or utility function executing `computeInitialGoogleMethod` operations and domain transformations. | None identified (clean/focused). |
| `getInitialPromptValue` | Function | Unexported (Private) | L57–L62 | 6 | Helper or utility function executing `getInitialPromptValue` operations and domain transformations. | None identified (clean/focused). |
| `requiresPromptInput` | Function | Unexported (Private) | L64–L73 | 10 | Helper or utility function executing `requiresPromptInput` operations and domain transformations. | None identified (clean/focused). |
| `ProviderAuthDialog` | Component | Exported | L75–L571 | 497 | Modal dialog component rendering UI shell, form fields, and action buttons for ProviderAuth. | **Monolithic** (497 LOC > 100). **Regex / Complex Parsing**: Uses regular expression parsing (`/\b([A-Z0-9]{4,}(?:-[A-Z0-9]{4,})+)\b/`, `/www.w3.org/`, `/svg" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"><polyline points="20 6 9 17 4 12" /`). **Timers**: Uses timer scheduling (`setTimeout` / `setInterval`). **State Bloat**: Uses 5 `useState` hooks. **Complex Effects**: Uses 5 `useEffect` hooks. |

### `web/router-ui-react/src/components/ReminderDialog.tsx`

- **Total Lines of Code**: 93 LOC
- **Symbols Defined**: 9

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ReminderDialogMode` | Type Alias | Exported | L8–L8 | 1 | TypeScript type/interface representing `ReminderDialogMode` domain model or contract. | None identified (clean/focused). |
| `ReminderDialogState` | Interface | Exported | L10–L18 | 9 | Defines data model and mutable/immutable properties representing ReminderDialogState. | None identified (clean/focused). |
| `ReminderDialogProps` | Interface | Exported | L20–L39 | 20 | Defines props contract, configuration flags, and callback handlers for `ReminderDialog`. | None identified (clean/focused). |
| `initialState` | Function | Unexported (Private) | L41–L41 | 1 | Helper or utility function executing `initialState` operations and domain transformations. | None identified (clean/focused). |
| `lockFor` | Function | Unexported (Private) | L42–L42 | 1 | Helper or utility function executing `lockFor` operations and domain transformations. | None identified (clean/focused). |
| `ReminderDialog` | Component | Exported | L44–L88 | 45 | Modal dialog component rendering UI shell, form fields, and action buttons for Reminder. | None identified (clean/focused). |
| `ReminderCountdown` | Component | Unexported (Private) | L89–L89 | 1 | React UI component rendering and managing visual presentation and interaction for `ReminderCountdown`. | None identified (clean/focused). |
| `formatCountdown` | Function | Unexported (Private) | L90–L90 | 1 | Helper or utility function executing `formatCountdown` operations and domain transformations. | None identified (clean/focused). |
| `Toggle` | Component | Unexported (Private) | L91–L91 | 1 | React UI component rendering and managing visual presentation and interaction for `Toggle`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/RouterLogViewer.tsx`

- **Total Lines of Code**: 214 LOC
- **Symbols Defined**: 25

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `DISPLAY_KEYS_STORAGE` | Constant | Unexported (Private) | L6–L6 | 1 | Module constant or configuration value for `DISPLAY_KEYS_STORAGE`. | None identified (clean/focused). |
| `DEFAULT_DISPLAY_KEYS` | Constant | Exported | L7–L7 | 1 | Module constant or configuration value for `DEFAULT_DISPLAY_KEYS`. | None identified (clean/focused). |
| `secretKeyPattern` | Constant | Unexported (Private) | L8–L8 | 1 | Module constant or configuration value for `secretKeyPattern`. | None identified (clean/focused). |
| `secretAssignmentPattern` | Constant | Unexported (Private) | L9–L9 | 1 | Module constant or configuration value for `secretAssignmentPattern`. | None identified (clean/focused). |
| `bearerPattern` | Constant | Unexported (Private) | L10–L10 | 1 | Module constant or configuration value for `bearerPattern`. | None identified (clean/focused). |
| `LogRecord` | Type Alias | Unexported (Private) | L11–L11 | 1 | TypeScript type/interface representing `LogRecord` domain model or contract. | None identified (clean/focused). |
| `DisplayLogField` | Interface | Unexported (Private) | L12–L12 | 1 | TypeScript type/interface representing `DisplayLogField` domain model or contract. | None identified (clean/focused). |
| `DisplayLogLine` | Interface | Unexported (Private) | L13–L13 | 1 | TypeScript type/interface representing `DisplayLogLine` domain model or contract. | None identified (clean/focused). |
| `DisplayKeyStorage` | Interface | Exported | L15–L18 | 4 | TypeScript type/interface representing `DisplayKeyStorage` domain model or contract. | None identified (clean/focused). |
| `decodeRouterLogBytes` | Function | Exported | L20–L43 | 24 | Helper or utility function executing `decodeRouterLogBytes` operations and domain transformations. | None identified (clean/focused). |
| `errorMessage` | Function | Unexported (Private) | L45–L45 | 1 | Helper or utility function executing `errorMessage` operations and domain transformations. | None identified (clean/focused). |
| `diagnosticLabel` | Function | Unexported (Private) | L46–L46 | 1 | Helper or utility function executing `diagnosticLabel` operations and domain transformations. | None identified (clean/focused). |
| `asRecord` | Function | Unexported (Private) | L47–L47 | 1 | Helper or utility function executing `asRecord` operations and domain transformations. | None identified (clean/focused). |
| `redactText` | Function | Unexported (Private) | L48–L56 | 9 | Helper or utility function executing `redactText` operations and domain transformations. | None identified (clean/focused). |
| `redactValue` | Function | Unexported (Private) | L57–L64 | 8 | Helper or utility function executing `redactValue` operations and domain transformations. | None identified (clean/focused). |
| `displayValue` | Function | Unexported (Private) | L65–L69 | 5 | Helper or utility function executing `displayValue` operations and domain transformations. | None identified (clean/focused). |
| `humanEvent` | Function | Unexported (Private) | L70–L74 | 5 | Helper or utility function executing `humanEvent` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/[^a-zA-Z0-9 _-]/g`). |
| `selectedTimestamp` | Function | Unexported (Private) | L75–L82 | 8 | Helper or utility function executing `selectedTimestamp` operations and domain transformations. | None identified (clean/focused). |
| `displayLogLine` | Function | Unexported (Private) | L83–L102 | 20 | Helper or utility function executing `displayLogLine` operations and domain transformations. | None identified (clean/focused). |
| `storage` | Function | Unexported (Private) | L104–L106 | 3 | Helper or utility function executing `storage` operations and domain transformations. | None identified (clean/focused). |
| `loadDisplayKeys` | Function | Exported | L108–L121 | 14 | Helper or utility function executing `loadDisplayKeys` operations and domain transformations. | None identified (clean/focused). |
| `saveDisplayKeys` | Function | Exported | L123–L125 | 3 | Helper or utility function executing `saveDisplayKeys` operations and domain transformations. | None identified (clean/focused). |
| `useDebouncedValue` | Hook | Exported | L127–L134 | 8 | Custom React hook managing state, side-effects, or subscriptions for `useDebouncedValue`. | **Timers**: Uses timer scheduling (`setTimeout` / `setInterval`). |
| `RouterLogViewerProps` | Interface | Exported | L136–L144 | 9 | Defines props contract, configuration flags, and callback handlers for `RouterLogViewer`. | None identified (clean/focused). |
| `RouterLogViewer` | Component | Exported | L146–L213 | 68 | React UI component rendering and managing visual presentation and interaction for `RouterLogViewer`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/RouterManagerDialog.tsx`

- **Total Lines of Code**: 85 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `RouterManagerDialogProps` | Interface | Exported | L6–L18 | 13 | Defines props contract, configuration flags, and callback handlers for `RouterManagerDialog`. | None identified (clean/focused). |
| `Editor` | Type Alias | Unexported (Private) | L20–L20 | 1 | TypeScript type/interface representing `Editor` domain model or contract. | None identified (clean/focused). |
| `RouterManagerDialog` | Component | Exported | L22–L83 | 62 | Modal dialog component rendering UI shell, form fields, and action buttons for RouterManager. | None identified (clean/focused). |

### `web/router-ui-react/src/components/RouterSelector.tsx`

- **Total Lines of Code**: 62 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `RouterSelectorProps` | Interface | Exported | L4–L12 | 9 | Defines props contract, configuration flags, and callback handlers for `RouterSelector`. | None identified (clean/focused). |
| `RouterSelector` | Component | Exported | L14–L61 | 48 | React UI component rendering and managing visual presentation and interaction for `RouterSelector`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ScratchpadViewer.tsx`

- **Total Lines of Code**: 170 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ScratchpadViewerProps` | Interface | Exported | L7–L13 | 7 | Defines props contract, configuration flags, and callback handlers for `ScratchpadViewer`. | None identified (clean/focused). |
| `formatTimestamp` | Function | Unexported (Private) | L15–L20 | 6 | Helper or utility function executing `formatTimestamp` operations and domain transformations. | None identified (clean/focused). |
| `ScratchpadViewer` | Component | Exported | L22–L169 | 148 | React UI component rendering and managing visual presentation and interaction for `ScratchpadViewer`. | **Monolithic** (148 LOC > 100). |

### `web/router-ui-react/src/components/Sidebar.tsx`

- **Total Lines of Code**: 176 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `SidebarProps` | Interface | Exported | L14–L47 | 34 | Defines props contract, configuration flags, and callback handlers for `Sidebar`. | None identified (clean/focused). |
| `browserStorage` | Function | Unexported (Private) | L48–L50 | 3 | Helper or utility function executing `browserStorage` operations and domain transformations. | None identified (clean/focused). |
| `Sidebar` | Component | Exported | L52–L175 | 124 | React UI component rendering and managing visual presentation and interaction for `Sidebar`. | **Monolithic** (124 LOC > 100). |

### `web/router-ui-react/src/components/StateConfigModal.test.tsx`

- **Total Lines of Code**: 216 LOC
- **Symbols Defined**: 0

*No top-level exported or unexported symbol declarations found (executable script or re-export file).*

### `web/router-ui-react/src/components/StateConfigModal.tsx`

- **Total Lines of Code**: 974 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `StateConfigModalProps` | Interface | Exported | L10–L16 | 7 | Defines props contract, configuration flags, and callback handlers for `StateConfigModal`. | None identified (clean/focused). |
| `StateConfigModal` | Component | Exported | L18–L973 | 956 | Modal dialog component rendering UI shell, form fields, and action buttons for StateConfig. | **Monolithic** (956 LOC > 100). **State Bloat**: Uses 25 `useState` hooks. |

### `web/router-ui-react/src/components/StatusMessage.tsx`

- **Total Lines of Code**: 4 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `StatusMessageProps` | Interface | Exported | L2–L2 | 1 | Defines props contract, configuration flags, and callback handlers for `StatusMessage`. | None identified (clean/focused). |
| `StatusMessage` | Component | Exported | L3–L3 | 1 | React UI component rendering and managing visual presentation and interaction for `StatusMessage`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/SubagentBadge.tsx`

- **Total Lines of Code**: 105 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `SubagentBadgeProps` | Interface | Exported | L7–L20 | 14 | Defines props contract, configuration flags, and callback handlers for `SubagentBadge`. | None identified (clean/focused). |
| `SubagentBadge` | Component | Exported | L22–L104 | 83 | React UI component rendering and managing visual presentation and interaction for `SubagentBadge`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/SubagentViewerDialog.tsx`

- **Total Lines of Code**: 592 LOC
- **Symbols Defined**: 10

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `Client` | Type Alias | Exported | L10–L10 | 1 | TypeScript type/interface representing `Client` domain model or contract. | None identified (clean/focused). |
| `SubagentViewerItem` | Type Alias | Exported | L12–L12 | 1 | TypeScript type/interface representing `SubagentViewerItem` domain model or contract. | None identified (clean/focused). |
| `SubagentViewerDialogProps` | Interface | Exported | L14–L21 | 8 | Defines props contract, configuration flags, and callback handlers for `SubagentViewerDialog`. | None identified (clean/focused). |
| `formatDuration` | Function | Unexported (Private) | L23–L49 | 27 | Helper or utility function executing `formatDuration` operations and domain transformations. | None identified (clean/focused). |
| `formatTimestamp` | Function | Unexported (Private) | L51–L56 | 6 | Helper or utility function executing `formatTimestamp` operations and domain transformations. | None identified (clean/focused). |
| `getSubagentTime` | Function | Unexported (Private) | L58–L64 | 7 | Helper or utility function executing `getSubagentTime` operations and domain transformations. | None identified (clean/focused). |
| `isToolOrActivityMeta` | Function | Unexported (Private) | L66–L94 | 29 | Helper or utility function executing `isToolOrActivityMeta` operations and domain transformations. | None identified (clean/focused). |
| `formatLastActivityPreview` | Function | Exported | L96–L123 | 28 | Helper or utility function executing `formatLastActivityPreview` operations and domain transformations. | None identified (clean/focused). |
| `getMessageEntryDetails` | Function | Unexported (Private) | L125–L166 | 42 | Helper or utility function executing `getMessageEntryDetails` operations and domain transformations. | None identified (clean/focused). |
| `SubagentViewerDialog` | Component | Exported | L168–L591 | 424 | Modal dialog component rendering UI shell, form fields, and action buttons for SubagentViewer. | **Monolithic** (424 LOC > 100). **Timers**: Uses timer scheduling (`setTimeout` / `setInterval`). **Complex Effects**: Uses 4 `useEffect` hooks. |

### `web/router-ui-react/src/components/SystemOneConfigDialog.tsx`

- **Total Lines of Code**: 334 LOC
- **Symbols Defined**: 5

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `SystemOneConfigDialogProps` | Interface | Exported | L5–L9 | 5 | Defines props contract, configuration flags, and callback handlers for `SystemOneConfigDialog`. | None identified (clean/focused). |
| `DEFAULT_EXAMPLE_STATE` | Constant | Unexported (Private) | L11–L16 | 6 | Module constant or configuration value for `DEFAULT_EXAMPLE_STATE`. | None identified (clean/focused). |
| `DEFAULT_EXAMPLE_QUESTIONS` | Constant | Unexported (Private) | L18–L31 | 14 | Module constant or configuration value for `DEFAULT_EXAMPLE_QUESTIONS`. | None identified (clean/focused). |
| `SystemOneConfigDialog` | Component | Exported | L33–L290 | 258 | Modal dialog component rendering UI shell, form fields, and action buttons for SystemOneConfig. | **Monolithic** (258 LOC > 100). **State Bloat**: Uses 8 `useState` hooks. |
| `DecisionAnswerDetail` | Component | Unexported (Private) | L292–L333 | 42 | React UI component rendering and managing visual presentation and interaction for `DecisionAnswerDetail`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/TerminateConfirmationDialog.tsx`

- **Total Lines of Code**: 110 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `TerminateConfirmationDialogProps` | Interface | Exported | L5–L13 | 9 | Defines props contract, configuration flags, and callback handlers for `TerminateConfirmationDialog`. | None identified (clean/focused). |
| `TerminateConfirmationDialog` | Component | Exported | L15–L109 | 95 | Modal dialog component rendering UI shell, form fields, and action buttons for TerminateConfirmation. | None identified (clean/focused). |

### `web/router-ui-react/src/components/ToolDefaultsDialog.tsx`

- **Total Lines of Code**: 268 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ToolDefaultsDialogProps` | Interface | Exported | L5–L9 | 5 | Defines props contract, configuration flags, and callback handlers for `ToolDefaultsDialog`. | None identified (clean/focused). |
| `ToolDefaultsDialog` | Component | Exported | L11–L267 | 257 | Modal dialog component rendering UI shell, form fields, and action buttons for ToolDefaults. | **Monolithic** (257 LOC > 100). |

### `web/router-ui-react/src/components/WorkerToolsDialog.tsx`

- **Total Lines of Code**: 327 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `WorkerToolsDialogProps` | Interface | Exported | L5–L11 | 7 | Defines props contract, configuration flags, and callback handlers for `WorkerToolsDialog`. | None identified (clean/focused). |
| `WorkerToolsDialog` | Component | Exported | L13–L326 | 314 | Modal dialog component rendering UI shell, form fields, and action buttons for WorkerTools. | **Monolithic** (314 LOC > 100). |

### `web/router-ui-react/src/components/WorkflowCanvas.tsx`

- **Total Lines of Code**: 845 LOC
- **Symbols Defined**: 8

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `WorkflowCanvasProps` | Interface | Exported | L35–L39 | 5 | Defines props contract, configuration flags, and callback handlers for `WorkflowCanvas`. | None identified (clean/focused). |
| `WorkflowEdge` | Interface | Unexported (Private) | L41–L43 | 3 | TypeScript type/interface representing `WorkflowEdge` domain model or contract. | None identified (clean/focused). |
| `WorkflowDefaultEdgeOptions` | Interface | Unexported (Private) | L45–L47 | 3 | TypeScript type/interface representing `WorkflowDefaultEdgeOptions` domain model or contract. | None identified (clean/focused). |
| `NODE_WIDTH` | Constant | Unexported (Private) | L49–L49 | 1 | Module constant or configuration value for `NODE_WIDTH`. | None identified (clean/focused). |
| `NODE_HEIGHT` | Constant | Unexported (Private) | L50–L50 | 1 | Module constant or configuration value for `NODE_HEIGHT`. | None identified (clean/focused). |
| `computeDagreLayout` | Function | Unexported (Private) | L52–L79 | 28 | Helper or utility function executing `computeDagreLayout` operations and domain transformations. | None identified (clean/focused). |
| `HealthIssue` | Interface | Unexported (Private) | L81–L84 | 4 | TypeScript type/interface representing `HealthIssue` domain model or contract. | None identified (clean/focused). |
| `WorkflowCanvas` | Component | Exported | L86–L842 | 757 | React UI component rendering and managing visual presentation and interaction for `WorkflowCanvas`. | **Monolithic** (757 LOC > 100). **Regex / Complex Parsing**: Uses regular expression parsing (`/[^a-z0-9_-]+/g`, `/^-\|-$/g`). **Timers**: Uses timer scheduling (`setTimeout` / `setInterval`). **Type Cast**: Uses `as any` or double casting. **State Bloat**: Uses 8 `useState` hooks. **Complex Effects**: Uses 3 `useEffect` hooks. |

### `web/router-ui-react/src/components/WorkflowLaunchModal.tsx`

- **Total Lines of Code**: 339 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `WorkflowLaunchModalProps` | Interface | Exported | L8–L16 | 9 | Defines props contract, configuration flags, and callback handlers for `WorkflowLaunchModal`. | None identified (clean/focused). |
| `WorkflowLaunchModal` | Component | Exported | L18–L338 | 321 | Modal dialog component rendering UI shell, form fields, and action buttons for WorkflowLaunch. | **Monolithic** (321 LOC > 100). **Complex Effects**: Uses 3 `useEffect` hooks. |

### `web/router-ui-react/src/components/WorkflowStateNode.test.tsx`

- **Total Lines of Code**: 139 LOC
- **Symbols Defined**: 0

*No top-level exported or unexported symbol declarations found (executable script or re-export file).*

### `web/router-ui-react/src/components/WorkflowStateNode.tsx`

- **Total Lines of Code**: 131 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `WorkflowStateNodeData` | Interface | Exported | L5–L11 | 7 | TypeScript type/interface representing `WorkflowStateNodeData` domain model or contract. | None identified (clean/focused). |
| `WorkflowStateNodeComponent` | Component | Unexported (Private) | L13–L128 | 116 | React UI component rendering and managing visual presentation and interaction for `WorkflowStateNodeComponent`. | **Monolithic** (116 LOC > 100). **Type Cast**: Uses `as any` or double casting. |
| `WorkflowStateNode` | Constant | Exported | L130–L130 | 1 | Module constant or configuration value for `WorkflowStateNode`. | None identified (clean/focused). |

### `web/router-ui-react/src/components/WorkflowStatesPage.tsx`

- **Total Lines of Code**: 947 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `WorkflowStatesPageProps` | Interface | Exported | L15–L19 | 5 | Defines props contract, configuration flags, and callback handlers for `WorkflowStatesPage`. | None identified (clean/focused). |
| `formatDuration` | Function | Unexported (Private) | L21–L30 | 10 | Helper or utility function executing `formatDuration` operations and domain transformations. | None identified (clean/focused). |
| `WorkflowStatesPage` | Component | Exported | L32–L943 | 912 | Top-level view page component orchestrating visual layout, subcomponents, and operations for WorkflowStatesPage. | **Monolithic** (912 LOC > 100). **State Bloat**: Uses 16 `useState` hooks. |

### `web/router-ui-react/src/components/WorkflowVariablesModal.tsx`

- **Total Lines of Code**: 416 LOC
- **Symbols Defined**: 3

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `WorkflowVariablesModalProps` | Interface | Exported | L5–L10 | 6 | Defines props contract, configuration flags, and callback handlers for `WorkflowVariablesModal`. | None identified (clean/focused). |
| `VARIABLE_NAME_REGEX` | Constant | Unexported (Private) | L12–L12 | 1 | Module constant or configuration value for `VARIABLE_NAME_REGEX`. | None identified (clean/focused). |
| `WorkflowVariablesModal` | Component | Exported | L14–L415 | 402 | Modal dialog component rendering UI shell, form fields, and action buttons for WorkflowVariables. | **Monolithic** (402 LOC > 100). **Regex / Complex Parsing**: Uses regular expression parsing (`/ Screen view mode: "list" (overview of variables) or "form" (dedicated add/`). **State Bloat**: Uses 5 `useState` hooks. |

### `web/router-ui-react/src/components/WorkflowsPage.tsx`

- **Total Lines of Code**: 661 LOC
- **Symbols Defined**: 2

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `WorkflowsPageProps` | Interface | Exported | L8–L12 | 5 | Defines props contract, configuration flags, and callback handlers for `WorkflowsPage`. | None identified (clean/focused). |
| `WorkflowsPage` | Component | Exported | L14–L658 | 645 | Top-level view page component orchestrating visual layout, subcomponents, and operations for WorkflowsPage. | **Monolithic** (645 LOC > 100). **Regex / Complex Parsing**: Uses regular expression parsing (`/[^a-z0-9_-]+/g`, `/^-\|-$/g`, `/repo.git or https:/`). **State Bloat**: Uses 15 `useState` hooks. |

## 2. Domain Directory (`web/router-ui-react/src/domain`)

### `web/router-ui-react/src/domain/arrivals.ts`

- **Total Lines of Code**: 12 LOC
- **Symbols Defined**: 11

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ARRIVAL_VISIBLE_MS` | Constant | Exported | L1–L1 | 1 | Module constant or configuration value for `ARRIVAL_VISIBLE_MS`. | None identified (clean/focused). |
| `ARRIVAL_SOURCE_KINDS` | Constant | Exported | L2–L2 | 1 | Module constant or configuration value for `ARRIVAL_SOURCE_KINDS`. | None identified (clean/focused). |
| `ArrivalSourceKind` | Type Alias | Exported | L3–L3 | 1 | TypeScript type/interface representing `ArrivalSourceKind` domain model or contract. | None identified (clean/focused). |
| `Arrival` | Interface | Exported | L4–L4 | 1 | TypeScript type/interface representing `Arrival` domain model or contract. | None identified (clean/focused). |
| `ArrivalEvent` | Interface | Exported | L5–L5 | 1 | TypeScript type/interface representing `ArrivalEvent` domain model or contract. | None identified (clean/focused). |
| `sourceKind` | Function | Unexported (Private) | L6–L6 | 1 | Helper or utility function executing `sourceKind` operations and domain transformations. | None identified (clean/focused). |
| `createArrival` | Function | Exported | L7–L7 | 1 | Helper or utility function executing `createArrival` operations and domain transformations. | None identified (clean/focused). |
| `replaceArrival` | Function | Exported | L8–L8 | 1 | Helper or utility function executing `replaceArrival` operations and domain transformations. | None identified (clean/focused). |
| `expireArrivals` | Function | Exported | L9–L9 | 1 | Helper or utility function executing `expireArrivals` operations and domain transformations. | None identified (clean/focused). |
| `dismissArrival` | Function | Exported | L10–L10 | 1 | Helper or utility function executing `dismissArrival` operations and domain transformations. | None identified (clean/focused). |
| `replaceAgentArrival` | Constant | Exported | L11–L11 | 1 | Module constant or configuration value for `replaceAgentArrival`. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/goals.ts`

- **Total Lines of Code**: 136 LOC
- **Symbols Defined**: 19

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `GoalAction` | Type Alias | Exported | L1–L1 | 1 | TypeScript type/interface representing `GoalAction` domain model or contract. | None identified (clean/focused). |
| `GoalEvaluationSummary` | Interface | Exported | L2–L7 | 6 | TypeScript type/interface representing `GoalEvaluationSummary` domain model or contract. | None identified (clean/focused). |
| `GoalControlSettings` | Interface | Exported | L8–L16 | 9 | TypeScript type/interface representing `GoalControlSettings` domain model or contract. | None identified (clean/focused). |
| `GoalState` | Interface | Exported | L17–L26 | 10 | Defines data model and mutable/immutable properties representing GoalState. | None identified (clean/focused). |
| `GoalEditorValues` | Interface | Exported | L27–L37 | 11 | TypeScript type/interface representing `GoalEditorValues` domain model or contract. | None identified (clean/focused). |
| `DEFAULT_GOAL_SETTINGS` | Constant | Exported | L38–L46 | 9 | Module constant or configuration value for `DEFAULT_GOAL_SETTINGS`. | None identified (clean/focused). |
| `DEFAULT_GOAL_STATE` | Constant | Exported | L47–L52 | 6 | Module constant or configuration value for `DEFAULT_GOAL_STATE`. | None identified (clean/focused). |
| `record` | Function | Unexported (Private) | L53–L55 | 3 | Helper or utility function executing `record` operations and domain transformations. | None identified (clean/focused). |
| `safeInteger` | Function | Unexported (Private) | L56–L58 | 3 | Helper or utility function executing `safeInteger` operations and domain transformations. | None identified (clean/focused). |
| `positive` | Function | Unexported (Private) | L59–L61 | 3 | Helper or utility function executing `positive` operations and domain transformations. | None identified (clean/focused). |
| `positiveOrZero` | Function | Unexported (Private) | L62–L64 | 3 | Helper or utility function executing `positiveOrZero` operations and domain transformations. | None identified (clean/focused). |
| `nullablePositive` | Function | Unexported (Private) | L65–L67 | 3 | Helper or utility function executing `nullablePositive` operations and domain transformations. | None identified (clean/focused). |
| `validateGoalControlSettings` | Function | Exported | L68–L79 | 12 | Helper or utility function executing `validateGoalControlSettings` operations and domain transformations. | None identified (clean/focused). |
| `normalizeGoalState` | Function | Exported | L80–L130 | 51 | Helper or utility function executing `normalizeGoalState` operations and domain transformations. | None identified (clean/focused). |
| `normalizeGoalProjection` | Function | Exported | L131–L131 | 1 | Helper or utility function executing `normalizeGoalProjection` operations and domain transformations. | None identified (clean/focused). |
| `goalSettingsFromEditor` | Function | Exported | L132–L132 | 1 | Helper or utility function executing `goalSettingsFromEditor` operations and domain transformations. | None identified (clean/focused). |
| `goalControlPayload` | Function | Exported | L133–L133 | 1 | Helper or utility function executing `goalControlPayload` operations and domain transformations. | None identified (clean/focused). |
| `goalErrorMessage` | Function | Exported | L134–L134 | 1 | Helper or utility function executing `goalErrorMessage` operations and domain transformations. | None identified (clean/focused). |
| `goalResultMatches` | Function | Exported | L135–L135 | 1 | Helper or utility function executing `goalResultMatches` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/history.ts`

- **Total Lines of Code**: 93 LOC
- **Symbols Defined**: 16

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `HISTORY_LIMIT` | Constant | Exported | L3–L3 | 1 | Module constant or configuration value for `HISTORY_LIMIT`. | None identified (clean/focused). |
| `HISTORY_RETENTION_MS` | Constant | Exported | L4–L4 | 1 | Module constant or configuration value for `HISTORY_RETENTION_MS`. | None identified (clean/focused). |
| `MessageAttachment` | Interface | Exported | L6–L6 | 1 | TypeScript type/interface representing `MessageAttachment` domain model or contract. | None identified (clean/focused). |
| `HistoryMessage` | Interface | Exported | L7–L22 | 16 | TypeScript type/interface representing `HistoryMessage` domain model or contract. | None identified (clean/focused). |
| `messageKey` | Function | Exported | L24–L26 | 3 | Helper or utility function executing `messageKey` operations and domain transformations. | None identified (clean/focused). |
| `messageTime` | Function | Exported | L28–L31 | 4 | Helper or utility function executing `messageTime` operations and domain transformations. | None identified (clean/focused). |
| `compareMessages` | Function | Exported | L33–L35 | 3 | Helper or utility function executing `compareMessages` operations and domain transformations. | None identified (clean/focused). |
| `mergeMessages` | Function | Exported | L37–L41 | 5 | Helper or utility function executing `mergeMessages` operations and domain transformations. | None identified (clean/focused). |
| `pendingMatchesCanonical` | Function | Unexported (Private) | L43–L52 | 10 | Helper or utility function executing `pendingMatchesCanonical` operations and domain transformations. | None identified (clean/focused). |
| `removeReconciledPending` | Function | Unexported (Private) | L54–L57 | 4 | Helper or utility function executing `removeReconciledPending` operations and domain transformations. | None identified (clean/focused). |
| `reconcileHistoryMessages` | Function | Exported | L59–L67 | 9 | Helper or utility function executing `reconcileHistoryMessages` operations and domain transformations. | None identified (clean/focused). |
| `pruneHistoryMessages` | Function | Exported | L69–L72 | 4 | Helper or utility function executing `pruneHistoryMessages` operations and domain transformations. | None identified (clean/focused). |
| `stripDataUrls` | Function | Exported | L74–L77 | 4 | Helper or utility function executing `stripDataUrls` operations and domain transformations. | None identified (clean/focused). |
| `pendingMessage` | Function | Exported | L79–L83 | 5 | Helper or utility function executing `pendingMessage` operations and domain transformations. | None identified (clean/focused). |
| `settlePendingMessages` | Function | Exported | L85–L87 | 3 | Helper or utility function executing `settlePendingMessages` operations and domain transformations. | None identified (clean/focused). |
| `replacePendingMessage` | Function | Exported | L89–L92 | 4 | Helper or utility function executing `replacePendingMessage` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/images.ts`

- **Total Lines of Code**: 82 LOC
- **Symbols Defined**: 18

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `MAX_IMAGES` | Constant | Exported | L1–L1 | 1 | Module constant or configuration value for `MAX_IMAGES`. | None identified (clean/focused). |
| `MAX_IMAGE_EDGE` | Constant | Exported | L2–L2 | 1 | Module constant or configuration value for `MAX_IMAGE_EDGE`. | None identified (clean/focused). |
| `IMAGE_JPEG_QUALITY` | Constant | Exported | L3–L3 | 1 | Module constant or configuration value for `IMAGE_JPEG_QUALITY`. | None identified (clean/focused). |
| `MAX_IMAGE_BYTES` | Constant | Exported | L4–L4 | 1 | Module constant or configuration value for `MAX_IMAGE_BYTES`. | None identified (clean/focused). |
| `MAX_TOTAL_IMAGE_BYTES` | Constant | Exported | L5–L5 | 1 | Module constant or configuration value for `MAX_TOTAL_IMAGE_BYTES`. | None identified (clean/focused). |
| `HISTORY_IMAGE_EDGE` | Constant | Exported | L6–L6 | 1 | Module constant or configuration value for `HISTORY_IMAGE_EDGE`. | None identified (clean/focused). |
| `HISTORY_IMAGE_QUALITY` | Constant | Exported | L7–L7 | 1 | Module constant or configuration value for `HISTORY_IMAGE_QUALITY`. | None identified (clean/focused). |
| `ImageInput` | Interface | Exported | L9–L9 | 1 | TypeScript type/interface representing `ImageInput` domain model or contract. | None identified (clean/focused). |
| `ImageAttachment` | Interface | Exported | L10–L10 | 1 | TypeScript type/interface representing `ImageAttachment` domain model or contract. | None identified (clean/focused). |
| `canAddImages` | Function | Exported | L12–L12 | 1 | Helper or utility function executing `canAddImages` operations and domain transformations. | None identified (clean/focused). |
| `pendingAttachmentText` | Function | Exported | L13–L15 | 3 | Helper or utility function executing `pendingAttachmentText` operations and domain transformations. | None identified (clean/focused). |
| `hasSendableContent` | Function | Exported | L16–L16 | 1 | Helper or utility function executing `hasSendableContent` operations and domain transformations. | None identified (clean/focused). |
| `attachmentReferencesFromBody` | Function | Exported | L17–L28 | 12 | Helper or utility function executing `attachmentReferencesFromBody` operations and domain transformations. | None identified (clean/focused). |
| `resolveAttachmentUrl` | Function | Exported | L29–L32 | 4 | Helper or utility function executing `resolveAttachmentUrl` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^\/`, `/attachments\/`). |
| `convertImage` | Function | Exported | L34–L52 | 19 | Helper or utility function executing `convertImage` operations and domain transformations. | None identified (clean/focused). |
| `dataUrlByteSize` | Function | Exported | L54–L60 | 7 | Helper or utility function executing `dataUrlByteSize` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/;base64/i`, `/\s/g`). |
| `validateImageBatch` | Function | Exported | L62–L67 | 6 | Helper or utility function executing `validateImageBatch` operations and domain transformations. | None identified (clean/focused). |
| `createHistoryThumbnail` | Function | Exported | L69–L81 | 13 | Helper or utility function executing `createHistoryThumbnail` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/markdown.tsx`

- **Total Lines of Code**: 136 LOC
- **Symbols Defined**: 20

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ALLOWED` | Constant | Unexported (Private) | L4–L4 | 1 | Module constant or configuration value for `ALLOWED`. | None identified (clean/focused). |
| `isAllowedMarkdownLink` | Function | Exported | L5–L13 | 9 | Helper or utility function executing `isAllowedMarkdownLink` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^mailto:[^\s/`). |
| `normalizeLabel` | Function | Unexported (Private) | L14–L14 | 1 | Helper or utility function executing `normalizeLabel` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/\s+/g`). |
| `definitionLabel` | Function | Unexported (Private) | L15–L15 | 1 | Helper or utility function executing `definitionLabel` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^ {0,3}\[([^\]]+)\]:/`). |
| `textWithoutMarkdownImages` | Function | Exported | L16–L22 | 7 | Helper or utility function executing `textWithoutMarkdownImages` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/!\[[^\]\n]*\]\([^\)\n]*(?:\s+"[^"\n]*")?\)/g`, `/!\[([^\]\n]*)\]\[([^\]\n]*)\]/g`, `/!\[([^\]\n]+)\](?![\[(])/g`). |
| `escapeHtml` | Function | Unexported (Private) | L24–L24 | 1 | Helper or utility function executing `escapeHtml` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/&/g`, `/</g`, `/>/g`). |
| `extractFallbackCodeBlocks` | Function | Unexported (Private) | L25–L44 | 20 | Helper or utility function executing `extractFallbackCodeBlocks` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^\s*```[^\n]*$/`, `/^\s*```\s*$/`, `/\n$/`). |
| `replaceFallbackMarkdownLinks` | Function | Unexported (Private) | L45–L80 | 36 | Helper or utility function executing `replaceFallbackMarkdownLinks` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/(!?)\[([^\]\n]+)\]\(/g`, `/&amp;/g`). |
| `fallbackMarkdown` | Function | Unexported (Private) | L81–L98 | 18 | Helper or utility function executing `fallbackMarkdown` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/`([^`\n]+)`/g`, `/\*\*([^*]+)\*\*/g`). |
| `MarkdownToken` | Interface | Unexported (Private) | L99–L99 | 1 | TypeScript type/interface representing `MarkdownToken` domain model or contract. | None identified (clean/focused). |
| `Linkify` | Interface | Unexported (Private) | L100–L100 | 1 | TypeScript type/interface representing `Linkify` domain model or contract. | None identified (clean/focused). |
| `MarkdownRendererSelf` | Interface | Unexported (Private) | L101–L101 | 1 | TypeScript type/interface representing `MarkdownRendererSelf` domain model or contract. | None identified (clean/focused). |
| `MarkdownRendererRule` | Type Alias | Unexported (Private) | L102–L102 | 1 | TypeScript type/interface representing `MarkdownRendererRule` domain model or contract. | None identified (clean/focused). |
| `MarkdownRenderer` | Interface | Unexported (Private) | L103–L103 | 1 | TypeScript type/interface representing `MarkdownRenderer` domain model or contract. | None identified (clean/focused). |
| `MarkdownConstructor` | Type Alias | Unexported (Private) | L104–L104 | 1 | TypeScript type/interface representing `MarkdownConstructor` domain model or contract. | None identified (clean/focused). |
| `isMarkdownConstructor` | Function | Unexported (Private) | L105–L105 | 1 | Helper or utility function executing `isMarkdownConstructor` operations and domain transformations. | None identified (clean/focused). |
| `createMarkdownRenderer` | Function | Unexported (Private) | L106–L119 | 14 | Helper or utility function executing `createMarkdownRenderer` operations and domain transformations. | None identified (clean/focused). |
| `markdownToSafeHtml` | Function | Exported | L120–L129 | 10 | Helper or utility function executing `markdownToSafeHtml` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/<a href=/g`, `/\n$/`). |
| `markdownToReact` | Function | Exported | L132–L134 | 3 | Markdown-it disables raw HTML and validates rendered links before this component inserts the markup. (Helper or utility function executing `markdownToReact` operations and domain transformations.) | None identified (clean/focused). |
| `MarkdownBody` | Constant | Exported | L135–L135 | 1 | Module constant or configuration value for `MarkdownBody`. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/models.ts`

- **Total Lines of Code**: 39 LOC
- **Symbols Defined**: 17

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `ModelSummary` | Interface | Exported | L1–L1 | 1 | TypeScript type/interface representing `ModelSummary` domain model or contract. | None identified (clean/focused). |
| `ModelProjection` | Interface | Exported | L2–L2 | 1 | TypeScript type/interface representing `ModelProjection` domain model or contract. | None identified (clean/focused). |
| `AgentModelFields` | Interface | Exported | L3–L3 | 1 | TypeScript type/interface representing `AgentModelFields` domain model or contract. | None identified (clean/focused). |
| `ModelAgent` | Interface | Exported | L4–L4 | 1 | TypeScript type/interface representing `ModelAgent` domain model or contract. | None identified (clean/focused). |
| `MAX_MODEL_SUMMARIES` | Constant | Unexported (Private) | L5–L5 | 1 | Module constant or configuration value for `MAX_MODEL_SUMMARIES`. | None identified (clean/focused). |
| `REJECTIONS` | Constant | Unexported (Private) | L6–L6 | 1 | Module constant or configuration value for `REJECTIONS`. | None identified (clean/focused). |
| `isRecord` | Function | Unexported (Private) | L7–L7 | 1 | Helper or utility function executing `isRecord` operations and domain transformations. | None identified (clean/focused). |
| `safeInteger` | Function | Unexported (Private) | L8–L8 | 1 | Helper or utility function executing `safeInteger` operations and domain transformations. | None identified (clean/focused). |
| `stringValue` | Function | Unexported (Private) | L9–L9 | 1 | Helper or utility function executing `stringValue` operations and domain transformations. | None identified (clean/focused). |
| `normalizeModelSummary` | Function | Exported | L10–L10 | 1 | Helper or utility function executing `normalizeModelSummary` operations and domain transformations. | None identified (clean/focused). |
| `normalizeModelSummaries` | Function | Exported | L11–L11 | 1 | Helper or utility function executing `normalizeModelSummaries` operations and domain transformations. | None identified (clean/focused). |
| `modelKey` | Function | Exported | L12–L12 | 1 | Helper or utility function executing `modelKey` operations and domain transformations. | None identified (clean/focused). |
| `modelOptionLabel` | Function | Exported | L13–L13 | 1 | Helper or utility function executing `modelOptionLabel` operations and domain transformations. | None identified (clean/focused). |
| `mergeAgentModelSnapshot` | Function | Exported | L14–L27 | 14 | Helper or utility function executing `mergeAgentModelSnapshot` operations and domain transformations. | None identified (clean/focused). |
| `mergeAgentModelProjection` | Function | Exported | L28–L36 | 9 | Helper or utility function executing `mergeAgentModelProjection` operations and domain transformations. | None identified (clean/focused). |
| `modelControlStatusTitle` | Function | Exported | L37–L37 | 1 | Helper or utility function executing `modelControlStatusTitle` operations and domain transformations. | None identified (clean/focused). |
| `isConfirmedModelRejection` | Function | Exported | L38–L38 | 1 | Helper or utility function executing `isConfirmedModelRejection` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/monitor-presets.ts`

- **Total Lines of Code**: 113 LOC
- **Symbols Defined**: 5

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `MonitorPreset` | Interface | Exported | L3–L8 | 6 | TypeScript type/interface representing `MonitorPreset` domain model or contract. | None identified (clean/focused). |
| `MONITOR_PRESETS` | Constant | Exported | L10–L99 | 90 | Module constant or configuration value for `MONITOR_PRESETS`. | None identified (clean/focused). |
| `getMonitorPresets` | Function | Exported | L101–L103 | 3 | Helper or utility function executing `getMonitorPresets` operations and domain transformations. | None identified (clean/focused). |
| `getMonitorPresetById` | Function | Exported | L105–L107 | 3 | Helper or utility function executing `getMonitorPresetById` operations and domain transformations. | None identified (clean/focused). |
| `getMonitorPresetDefaults` | Function | Exported | L109–L112 | 4 | Helper or utility function executing `getMonitorPresetDefaults` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/prompt-template-import.ts`

- **Total Lines of Code**: 113 LOC
- **Symbols Defined**: 14

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `PROMPT_TEMPLATE_IMPORT_TIMEOUT_MS` | Constant | Exported | L3–L3 | 1 | Module constant or configuration value for `PROMPT_TEMPLATE_IMPORT_TIMEOUT_MS`. | None identified (clean/focused). |
| `MAX_PROMPT_TEMPLATE_IMPORTS` | Constant | Exported | L4–L4 | 1 | Module constant or configuration value for `MAX_PROMPT_TEMPLATE_IMPORTS`. | None identified (clean/focused). |
| `PromptTemplateImportOperations` | Interface | Exported | L5–L9 | 5 | TypeScript type/interface representing `PromptTemplateImportOperations` domain model or contract. | None identified (clean/focused). |
| `PromptTemplateImportProgress` | Interface | Exported | L10–L10 | 1 | TypeScript type/interface representing `PromptTemplateImportProgress` domain model or contract. | None identified (clean/focused). |
| `PromptTemplateImportResult` | Interface | Exported | L11–L11 | 1 | TypeScript type/interface representing `PromptTemplateImportResult` domain model or contract. | None identified (clean/focused). |
| `PromptTemplateImportPlanItem` | Interface | Exported | L12–L12 | 1 | TypeScript type/interface representing `PromptTemplateImportPlanItem` domain model or contract. | None identified (clean/focused). |
| `PromptTemplateImportError` | Class | Exported | L13–L15 | 3 | Class encapsulating state, lifecycle, and methods for `PromptTemplateImportError`. | None identified (clean/focused). |
| `PromptTemplateImportErrorMetadata` | Interface | Unexported (Private) | L16–L19 | 4 | TypeScript type/interface representing `PromptTemplateImportErrorMetadata` domain model or contract. | None identified (clean/focused). |
| `NormalizedPromptTemplateImportError` | Class | Unexported (Private) | L20–L28 | 9 | Class encapsulating state, lifecycle, and methods for `NormalizedPromptTemplateImportError`. | None identified (clean/focused). |
| `normalizeImportRejection` | Function | Unexported (Private) | L29–L38 | 10 | Helper or utility function executing `normalizeImportRejection` operations and domain transformations. | None identified (clean/focused). |
| `timeout` | Function | Unexported (Private) | L39–L44 | 6 | Helper or utility function executing `timeout` operations and domain transformations. | **Timers**: Uses timer scheduling (`setTimeout` / `setInterval`). |
| `isPromptTemplateNameConflict` | Function | Unexported (Private) | L45–L48 | 4 | Helper or utility function executing `isPromptTemplateNameConflict` operations and domain transformations. | None identified (clean/focused). |
| `planPromptTemplateImport` | Function | Exported | L49–L66 | 18 | Helper or utility function executing `planPromptTemplateImport` operations and domain transformations. | None identified (clean/focused). |
| `runPromptTemplateImport` | Function | Exported | L67–L112 | 46 | Helper or utility function executing `runPromptTemplateImport` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/prompt-templates.ts`

- **Total Lines of Code**: 130 LOC
- **Symbols Defined**: 27

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `PROMPT_TEMPLATE_EXPORT_FORMAT` | Constant | Exported | L3–L3 | 1 | Module constant or configuration value for `PROMPT_TEMPLATE_EXPORT_FORMAT`. | None identified (clean/focused). |
| `PROMPT_TEMPLATE_EXPORT_VERSION` | Constant | Exported | L4–L4 | 1 | Module constant or configuration value for `PROMPT_TEMPLATE_EXPORT_VERSION`. | None identified (clean/focused). |
| `MAX_PROMPT_TEMPLATES` | Constant | Exported | L5–L5 | 1 | Module constant or configuration value for `MAX_PROMPT_TEMPLATES`. | None identified (clean/focused). |
| `MAX_PROMPT_TEMPLATE_NAME_BYTES` | Constant | Exported | L6–L6 | 1 | Module constant or configuration value for `MAX_PROMPT_TEMPLATE_NAME_BYTES`. | None identified (clean/focused). |
| `MAX_PROMPT_TEMPLATE_BODY_BYTES` | Constant | Exported | L7–L7 | 1 | Module constant or configuration value for `MAX_PROMPT_TEMPLATE_BODY_BYTES`. | None identified (clean/focused). |
| `MAX_PROMPT_TEMPLATE_ID_BYTES` | Constant | Exported | L8–L8 | 1 | Module constant or configuration value for `MAX_PROMPT_TEMPLATE_ID_BYTES`. | None identified (clean/focused). |
| `TIMESTAMP` | Constant | Unexported (Private) | L9–L9 | 1 | Module constant or configuration value for `TIMESTAMP`. | None identified (clean/focused). |
| `textEncoder` | Constant | Unexported (Private) | L10–L10 | 1 | Module constant or configuration value for `textEncoder`. | None identified (clean/focused). |
| `PromptTemplate` | Interface | Exported | L11–L11 | 1 | TypeScript type/interface representing `PromptTemplate` domain model or contract. | None identified (clean/focused). |
| `PromptTemplateExport` | Interface | Exported | L12–L12 | 1 | TypeScript type/interface representing `PromptTemplateExport` domain model or contract. | None identified (clean/focused). |
| `objectValue` | Function | Unexported (Private) | L13–L13 | 1 | Helper or utility function executing `objectValue` operations and domain transformations. | None identified (clean/focused). |
| `canonicalTimestamp` | Function | Unexported (Private) | L14–L14 | 1 | Helper or utility function executing `canonicalTimestamp` operations and domain transformations. | None identified (clean/focused). |
| `isPortablePromptTemplateId` | Function | Exported | L15–L15 | 1 | Helper or utility function executing `isPortablePromptTemplateId` operations and domain transformations. | None identified (clean/focused). |
| `promptTemplateExportPayload` | Function | Exported | L16–L16 | 1 | Helper or utility function executing `promptTemplateExportPayload` operations and domain transformations. | None identified (clean/focused). |
| `promptTemplateUpdatePayload` | Function | Exported | L17–L23 | 7 | Helper or utility function executing `promptTemplateUpdatePayload` operations and domain transformations. | None identified (clean/focused). |
| `promptTemplateCreatePayload` | Function | Exported | L25–L33 | 9 | Helper or utility function executing `promptTemplateCreatePayload` operations and domain transformations. | None identified (clean/focused). |
| `parsePromptTemplateImport` | Function | Exported | L35–L35 | 1 | Helper or utility function executing `parsePromptTemplateImport` operations and domain transformations. | None identified (clean/focused). |
| `parsePromptTemplateTokens` | Function | Exported | L36–L41 | 6 | Helper or utility function executing `parsePromptTemplateTokens` operations and domain transformations. | None identified (clean/focused). |
| `renderPromptTemplate` | Function | Exported | L43–L49 | 7 | Helper or utility function executing `renderPromptTemplate` operations and domain transformations. | None identified (clean/focused). |
| `ImportProgress` | Interface | Exported | L51–L51 | 1 | TypeScript type/interface representing `ImportProgress` domain model or contract. | None identified (clean/focused). |
| `promptTemplateImportProgress` | Function | Exported | L52–L52 | 1 | Helper or utility function executing `promptTemplateImportProgress` operations and domain transformations. | None identified (clean/focused). |
| `PROMPT_TEMPLATE_USAGE_STORAGE_KEY` | Constant | Exported | L54–L54 | 1 | Module constant or configuration value for `PROMPT_TEMPLATE_USAGE_STORAGE_KEY`. | None identified (clean/focused). |
| `slugifyPromptTemplate` | Function | Exported | L56–L75 | 20 | Helper or utility function executing `slugifyPromptTemplate` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/-+$/`). |
| `promptTemplateKey` | Function | Exported | L78–L81 | 4 | Helper or utility function executing `promptTemplateKey` operations and domain transformations. | None identified (clean/focused). |
| `loadPromptTemplateUsage` | Function | Exported | L83–L95 | 13 | Helper or utility function executing `loadPromptTemplateUsage` operations and domain transformations. | **Hidden State**: Accesses `localStorage`/`sessionStorage` directly. |
| `recordPromptTemplateUsage` | Function | Exported | L97–L118 | 22 | Helper or utility function executing `recordPromptTemplateUsage` operations and domain transformations. | **Hidden State**: Accesses `localStorage`/`sessionStorage` directly. |
| `sortPromptTemplatesByMRU` | Function | Exported | L120–L128 | 9 | Helper or utility function executing `sortPromptTemplatesByMRU` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/reminders.ts`

- **Total Lines of Code**: 20 LOC
- **Symbols Defined**: 17

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `MIN_REMINDER_DELAY_MINUTES` | Constant | Exported | L3–L3 | 1 | Module constant or configuration value for `MIN_REMINDER_DELAY_MINUTES`. | None identified (clean/focused). |
| `MAX_REMINDER_DELAY_MINUTES` | Constant | Exported | L4–L4 | 1 | Module constant or configuration value for `MAX_REMINDER_DELAY_MINUTES`. | None identified (clean/focused). |
| `REMINDER_VISIBLE_STATES` | Constant | Exported | L5–L5 | 1 | Module constant or configuration value for `REMINDER_VISIBLE_STATES`. | None identified (clean/focused). |
| `Reminder` | Interface | Exported | L6–L6 | 1 | TypeScript type/interface representing `Reminder` domain model or contract. | None identified (clean/focused). |
| `ReminderCreateInput` | Interface | Exported | L7–L7 | 1 | TypeScript type/interface representing `ReminderCreateInput` domain model or contract. | None identified (clean/focused). |
| `ReminderLockAction` | Type Alias | Exported | L8–L8 | 1 | TypeScript type/interface representing `ReminderLockAction` domain model or contract. | None identified (clean/focused). |
| `ReminderLockState` | Interface | Exported | L9–L9 | 1 | Defines data model and mutable/immutable properties representing ReminderLockState. | None identified (clean/focused). |
| `formatReminderCountdown` | Function | Exported | L10–L10 | 1 | Helper or utility function executing `formatReminderCountdown` operations and domain transformations. | None identified (clean/focused). |
| `reminderRecurrenceLabel` | Function | Exported | L11–L11 | 1 | Helper or utility function executing `reminderRecurrenceLabel` operations and domain transformations. | None identified (clean/focused). |
| `activeRemindersForPeer` | Function | Exported | L12–L12 | 1 | Helper or utility function executing `activeRemindersForPeer` operations and domain transformations. | None identified (clean/focused). |
| `validateReminderDelay` | Function | Exported | L13–L13 | 1 | Helper or utility function executing `validateReminderDelay` operations and domain transformations. | None identified (clean/focused). |
| `canonicalUtc` | Function | Exported | L14–L14 | 1 | Helper or utility function executing `canonicalUtc` operations and domain transformations. | None identified (clean/focused). |
| `createReminderPayload` | Function | Exported | L15–L15 | 1 | Helper or utility function executing `createReminderPayload` operations and domain transformations. | None identified (clean/focused). |
| `reminderLockKey` | Function | Exported | L16–L16 | 1 | Helper or utility function executing `reminderLockKey` operations and domain transformations. | None identified (clean/focused). |
| `reminderLockErrorMessage` | Function | Exported | L17–L17 | 1 | Helper or utility function executing `reminderLockErrorMessage` operations and domain transformations. | None identified (clean/focused). |
| `lockResultState` | Function | Exported | L18–L18 | 1 | Helper or utility function executing `lockResultState` operations and domain transformations. | None identified (clean/focused). |
| `lockAfterCreateStatus` | Function | Exported | L19–L19 | 1 | Helper or utility function executing `lockAfterCreateStatus` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/router-manager.ts`

- **Total Lines of Code**: 20 LOC
- **Symbols Defined**: 11

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `SavedRouter` | Interface | Exported | L4–L4 | 1 | TypeScript type/interface representing `SavedRouter` domain model or contract. | None identified (clean/focused). |
| `normalizeRouterUrl` | Function | Exported | L5–L10 | 6 | Helper or utility function executing `normalizeRouterUrl` operations and domain transformations. | **Regex / Complex Parsing**: Uses regular expression parsing (`/^[a-z]+:\/`, `/i.test(candidate) ? candidate : `http:/`, `/ or https:/`). |
| `routerDisplayName` | Function | Exported | L11–L11 | 1 | Helper or utility function executing `routerDisplayName` operations and domain transformations. | None identified (clean/focused). |
| `normalizeRouterInput` | Constant | Exported | L12–L12 | 1 | Module constant or configuration value for `normalizeRouterInput`. | None identified (clean/focused). |
| `isRouterMap` | Function | Unexported (Private) | L13–L13 | 1 | Helper or utility function executing `isRouterMap` operations and domain transformations. | None identified (clean/focused). |
| `agentIdForRouter` | Function | Exported | L14–L14 | 1 | Helper or utility function executing `agentIdForRouter` operations and domain transformations. | None identified (clean/focused). |
| `createConnectionIdentity` | Function | Exported | L15–L15 | 1 | Helper or utility function executing `createConnectionIdentity` operations and domain transformations. | None identified (clean/focused). |
| `normalizeSavedRouter` | Function | Exported | L16–L16 | 1 | Helper or utility function executing `normalizeSavedRouter` operations and domain transformations. | None identified (clean/focused). |
| `ensureRouter` | Function | Exported | L17–L17 | 1 | Helper or utility function executing `ensureRouter` operations and domain transformations. | None identified (clean/focused). |
| `removeRouter` | Function | Exported | L18–L18 | 1 | Helper or utility function executing `removeRouter` operations and domain transformations. | None identified (clean/focused). |
| `switchRouterState` | Function | Exported | L19–L19 | 1 | Helper or utility function executing `switchRouterState` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/template-engine.ts`

- **Total Lines of Code**: 102 LOC
- **Symbols Defined**: 7

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `TEMPLATE_TOKEN_REGEX` | Constant | Unexported (Private) | L1–L1 | 1 | Module constant or configuration value for `TEMPLATE_TOKEN_REGEX`. | None identified (clean/focused). |
| `TEMPLATE_INCLUSION_REGEX` | Constant | Unexported (Private) | L2–L2 | 1 | Module constant or configuration value for `TEMPLATE_INCLUSION_REGEX`. | None identified (clean/focused). |
| `MAX_TEMPLATE_DEPTH` | Constant | Unexported (Private) | L3–L3 | 1 | Module constant or configuration value for `MAX_TEMPLATE_DEPTH`. | None identified (clean/focused). |
| `getTemplateBody` | Function | Unexported (Private) | L5–L14 | 10 | Helper or utility function executing `getTemplateBody` operations and domain transformations. | None identified (clean/focused). |
| `parseTemplateTokens` | Function | Exported | L16–L53 | 38 | Helper or utility function executing `parseTemplateTokens` operations and domain transformations. | None identified (clean/focused). |
| `expandTemplates` | Function | Unexported (Private) | L55–L91 | 37 | Helper or utility function executing `expandTemplates` operations and domain transformations. | None identified (clean/focused). |
| `renderTemplate` | Function | Exported | L93–L100 | 8 | Helper or utility function executing `renderTemplate` operations and domain transformations. | None identified (clean/focused). |

### `web/router-ui-react/src/domain/workflow-states.ts`

- **Total Lines of Code**: 187 LOC
- **Symbols Defined**: 22

| Symbol Name | Type | Scope | Line Range | LOC | Purpose / Responsibility | Identified Issues |
| :--- | :--- | :--- | :--- | :--- | :--- | :--- |
| `WorkflowReminderType` | Type Alias | Exported | L1–L1 | 1 | TypeScript type/interface representing `WorkflowReminderType` domain model or contract. | None identified (clean/focused). |
| `WorkflowTransitionConfig` | Interface | Exported | L3–L6 | 4 | TypeScript type/interface representing `WorkflowTransitionConfig` domain model or contract. | None identified (clean/focused). |
| `WorkflowReminderConfig` | Interface | Exported | L8–L15 | 8 | TypeScript type/interface representing `WorkflowReminderConfig` domain model or contract. | None identified (clean/focused). |
| `WorkflowMonitorConfig` | Interface | Exported | L17–L33 | 17 | TypeScript type/interface representing `WorkflowMonitorConfig` domain model or contract. | None identified (clean/focused). |
| `WorkflowState` | Interface | Exported | L35–L51 | 17 | Defines data model and mutable/immutable properties representing WorkflowState. | None identified (clean/focused). |
| `WorkflowStatePosition` | Interface | Exported | L53–L57 | 5 | TypeScript type/interface representing `WorkflowStatePosition` domain model or contract. | None identified (clean/focused). |
| `WorkflowVariableType` | Type Alias | Exported | L59–L59 | 1 | TypeScript type/interface representing `WorkflowVariableType` domain model or contract. | None identified (clean/focused). |
| `WorkflowVariableDefinition` | Interface | Exported | L61–L68 | 8 | TypeScript type/interface representing `WorkflowVariableDefinition` domain model or contract. | None identified (clean/focused). |
| `Workflow` | Interface | Exported | L70–L79 | 10 | TypeScript type/interface representing `Workflow` domain model or contract. | None identified (clean/focused). |
| `CreateWorkflowRequest` | Interface | Exported | L81–L85 | 5 | TypeScript type/interface representing `CreateWorkflowRequest` domain model or contract. | None identified (clean/focused). |
| `UpdateWorkflowRequest` | Interface | Exported | L87–L92 | 6 | TypeScript type/interface representing `UpdateWorkflowRequest` domain model or contract. | None identified (clean/focused). |
| `WorkflowWithStates` | Interface | Exported | L94–L97 | 4 | TypeScript type/interface representing `WorkflowWithStates` domain model or contract. | None identified (clean/focused). |
| `WorkflowTransitionExport` | Interface | Exported | L99–L102 | 4 | TypeScript type/interface representing `WorkflowTransitionExport` domain model or contract. | None identified (clean/focused). |
| `WorkflowStateExport` | Interface | Exported | L104–L118 | 15 | TypeScript type/interface representing `WorkflowStateExport` domain model or contract. | None identified (clean/focused). |
| `WorkflowInfoExport` | Interface | Exported | L120–L124 | 5 | TypeScript type/interface representing `WorkflowInfoExport` domain model or contract. | None identified (clean/focused). |
| `WorkflowExportBundle` | Interface | Exported | L126–L132 | 7 | TypeScript type/interface representing `WorkflowExportBundle` domain model or contract. | None identified (clean/focused). |
| `WorkflowStateInput` | Interface | Exported | L134–L147 | 14 | TypeScript type/interface representing `WorkflowStateInput` domain model or contract. | None identified (clean/focused). |
| `WorkflowStateResponse` | Interface | Exported | L149–L151 | 3 | TypeScript type/interface representing `WorkflowStateResponse` domain model or contract. | None identified (clean/focused). |
| `WorkflowStatesResponse` | Interface | Exported | L153–L155 | 3 | TypeScript type/interface representing `WorkflowStatesResponse` domain model or contract. | None identified (clean/focused). |
| `WorkflowToolsResponse` | Interface | Exported | L157–L159 | 3 | TypeScript type/interface representing `WorkflowToolsResponse` domain model or contract. | None identified (clean/focused). |
| `KnownToolDefinition` | Interface | Exported | L161–L166 | 6 | TypeScript type/interface representing `KnownToolDefinition` domain model or contract. | None identified (clean/focused). |
| `KNOWN_WORKFLOW_TOOLS` | Constant | Exported | L168–L186 | 19 | Module constant or configuration value for `KNOWN_WORKFLOW_TOOLS`. | None identified (clean/focused). |

