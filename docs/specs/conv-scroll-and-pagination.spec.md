# Spec: Conversation Scroll Anchoring & Proximity Auto-Scrolling

- **ID**: `SPEC-CONV-003`
- **Status**: `Active`
- **Domain**: `Conversation`
- **Target E2E Suite**: `tests/e2e/conversation-scroll-and-pagination.spec.ts`

---

## 1. Intent & User Outcome

In conversation interfaces with rapid streaming updates or high message volume, manual scroll locks create friction and cognitive overhead. Users frequently forget they activated a scroll lock, missing incoming live activity, or struggle to re-engage auto-scrolling when they return to the bottom.

This specification defines the behavior for eliminating the manual scroll lock toggle and adopting a proximity-based sticky auto-scroll model:
1. When viewing near the bottom of the conversation feed (within 180px threshold), new inbound messages and agent activity stick and follow the viewport to the bottom.
2. When the user scrolls up past 180px from the bottom to read conversation history, the viewport position is locked and does not jump or jerk as new activity streams in.
3. When the user scrolls back down within the 180px threshold, sticky auto-scrolling automatically resumes.
4. When switching to another agent or conversation, the view resets and forces an immediate scroll to the bottom.

---

## 2. Core Invariants

1. **Elimination of Manual Toggle**: No manual "Scroll Lock" button or control shall exist in the conversation header toolbar or agent controls dropdown.
2. **Proximity Threshold (`DEFAULT_AUTO_SCROLL_THRESHOLD = 180`)**: Distance from bottom is defined as `scrollHeight - (scrollTop + clientHeight)`. When `distance <= 180`, the user is considered "near bottom".
3. **Viewport Position Preservation**: If `distance > 180`, incoming events or messages MUST NOT adjust `scrollTop`. The user's reading position MUST be preserved.
4. **Conversation Switch Reset**: Switching the active conversation/peer (`conversationKey` change) MUST reset `isNearBottomRef` to `true` and scroll to the bottom.
5. **Double-rAF DOM Settlement**: Layout changes and auto-scrolling use synchronous layout alignment followed by a `requestAnimationFrame` callback to ensure settled DOM layout.

---

## 3. User Scenarios (Behavioral Contract)

### Happy Path Scenarios

#### Scenario: [SCN-SCROLL-001] Auto-scroll followership when near bottom
- **Given**: Wolfgang is viewing the active conversation with agent `supervisor`.
- **And**: The scroll position is at or within 180px of the bottom (`scrollHeight - (scrollTop + clientHeight) <= 180`).
- **When**: New activity entries or messages arrive in the conversation.
- **Then**: The viewport automatically scrolls to the bottom, displaying the newest entry.

#### Scenario: [SCN-SCROLL-002] Reading position preserved when scrolled up
- **Given**: Wolfgang is viewing an active conversation with agent `supervisor`.
- **When**: Wolfgang scrolls upward by more than 180px to read earlier messages.
- **And**: The agent performs tool calls, outputs thinking logs, or streams new tokens.
- **Then**: The scroll position does not jump or snap to the bottom. The content Wolfgang is reading remains stably in view.

#### Scenario: [SCN-SCROLL-003] Auto-scroll resumes when returning to bottom
- **Given**: Wolfgang scrolled up to read past history and auto-scroll was suspended.
- **When**: Wolfgang scrolls back down to within 180px of the bottom.
- **Then**: Proximity detection identifies that the user is near the bottom.
- **When**: The next message or event arrives.
- **Then**: Auto-scrolling immediately resumes and keeps the feed pinned to the bottom.

#### Scenario: [SCN-SCROLL-004] Clean reset on agent switch
- **Given**: Wolfgang is viewing agent `agent-1` and has scrolled up into older history.
- **When**: Wolfgang selects agent `agent-2` from the sidebar.
- **Then**: The conversation feed resets, automatically scrolls to the bottom of `agent-2`'s conversation, and sets the proximity state to near bottom.

### Edge & Error Scenarios

#### Scenario: [SCN-SCROLL-005] Empty or short conversation feeds
- **Given**: An agent conversation has very few messages and `scrollHeight <= clientHeight`.
- **When**: New events are added.
- **Then**: Distance from bottom is `<= 0`, which is `<= 180`, properly triggering auto-scroll without error.

---

## 4. Playwright End-to-End Suite Mapping

| Scenario ID | Test Suite | Test Case Description |
| :--- | :--- | :--- |
| `SCN-SCROLL-001` | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | Verify auto-scroll sticks to bottom on new messages when near bottom |
| `SCN-SCROLL-002` | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | Verify scroll position remains pinned when scrolled up > 180px |
| `SCN-SCROLL-003` | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | Verify scrolling back down resumes auto-scroll followership |
| `SCN-SCROLL-004` | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | Verify switching agents scrolls to bottom regardless of prior scroll state |
