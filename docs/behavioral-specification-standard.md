# Behavioral Specification Standard

This document defines the mandatory format and structure for all system behavioral specifications in `mecha-developer`. Every behavioral change, feature, or subsystem must have an authoritative specification file in `docs/specs/` and corresponding Playwright E2E tests in `tests/e2e/`.

---

## 1. Specification File Schema & Conventions

Specification files live in `docs/specs/<domain>-<feature>.spec.md`. They are written in clear, precise English organized around **user scenarios**, avoiding implementation minutiae while providing unambiguous observable contracts for E2E tests.

### Required Header & Metadata
```markdown
# Spec: [Subsystem / Feature Name]

- **ID**: `SPEC-[DOMAIN]-[NUMBER]` (e.g., `SPEC-CONV-001`)
- **Status**: `Draft` | `Active` | `Deprecated`
- **Domain**: `Conversation` | `Composer` | `Workflows` | `AgentManagement` | `Settings` | `Mailbox`
- **Target E2E Suite**: `tests/e2e/<suite-name>.spec.ts`
```

---

## 2. Standard Document Sections

Every specification file MUST contain the following four sections:

### Section 1: Intent & User Outcome
A compact, non-technical plain English summary of what the user experiences, why the feature exists, and the primary value delivered.

### Section 2: Core Invariants
A numbered list of strict, unchangeable rules and system guarantees that must **never** be violated under any circumstance.
*Example:*
1. Regular assistant turn replies MUST ALWAYS render as incoming speech bubbles (`MessageBubble`), NEVER as envelope activity cards.
2. Typing into the Composer textarea MUST NEVER trigger a re-render of the message feed.
3. Prepending older history messages MUST NEVER cause the scroll container to jump or lose the user's reading position.

### Section 3: User Scenarios (Behavioral Contract)
Scenarios must be written using the structured **Given / When / Then** format, covering both **Happy Paths** and **Non-Happy / Error / Edge Paths**:

#### Happy Path Scenario Example:
```markdown
### Scenario: [SCN-001] User sends message with image attachment
- **User Goal**: Send a text prompt with an attached screenshot to an agent.
- **Given**:
  - The conversation view is open for agent `test-worker`.
  - The agent is idle and listening.
- **When**:
  - User types "Inspect this screenshot" into the composer textarea.
  - User attaches `screenshot.png` via file picker or paste.
  - User clicks "Send" (or presses Enter).
- **Then**:
  - A user message bubble immediately appears in the feed with the text "Inspect this screenshot".
  - An image preview thumbnail appears directly below the message text in the bubble.
  - The raw filesystem path or data URL text is NOT displayed in the bubble text.
  - The composer textarea and attachment tray are cleared.
  - The agent receives the message and image attachment in its turn input.
```

#### Non-Happy / Edge Path Scenario Example:
```markdown
### Scenario: [SCN-002] Network disconnection during message send
- **User Goal**: Ensure messages are not lost if the router connection drops.
- **Given**:
  - The conversation view is open.
  - The WebSocket connection to the router drops.
- **When**:
  - User submits a new message.
- **Then**:
  - The UI displays an inline delivery retry indicator on the pending bubble.
  - The message is not discarded from the input or timeline.
  - Once the connection reconnects, the message is automatically dispatched.
```

### Section 4: Automated E2E Verification Matrix
A table mapping every Scenario ID directly to its Playwright E2E test file and test title:

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| `SCN-001` | Send message with image attachment | `tests/e2e/conversation-attachments.spec.ts` | `displays thumbnail and hides raw path text` |
| `SCN-002` | Disconnection during message send | `tests/e2e/conversation-resilience.spec.ts` | `preserves draft and shows retry on disconnect` |

---

## 3. Maintenance & Discovery Rule

1. **Specification Precedes Implementation**: Before implementing behavioral changes, define or update the specification file in `docs/specs/`.
2. **100% E2E Test Coverage**: Every scenario in a specification must have a corresponding, non-flaky Playwright E2E test that exercises the real browser, WebSocket, router, and SQLite store with a deterministic mock LLM.
3. **Discoverability**: All specifications are indexed in `docs/specs/INDEX.md` and linked directly from the root `AGENTS.md`.
