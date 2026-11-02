# Spec: Conversation Scroll Anchoring & Proximity Auto-Scrolling

- **ID**: `SPEC-CONV-003`
- **Status**: `Active`
- **Domain**: `Conversation`
- **Target E2E Suite**: `tests/e2e/conversation-scroll-and-pagination.spec.ts`, `tests/e2e/conversation-scroll-regressions.spec.ts`

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
3. **Viewport Position Preservation**: If `distance > 180`, incoming events or messages MUST preserve the visible reading position. Appending below the viewport MUST NOT adjust `scrollTop`; prepending or trimming above it MUST compensate as needed for a surviving visible item.
4. **Conversation Switch Reset**: Switching the active conversation/peer (`conversationKey` change) MUST reset `isNearBottomRef` to `true` and scroll to the bottom.
5. **Double-rAF DOM Settlement**: Layout changes and auto-scrolling use synchronous layout alignment followed by a `requestAnimationFrame` callback to ensure settled DOM layout.
6. **Conversation Chunking Constants**: `DEFAULT_CONVERSATION_CHUNK_LIMIT = 50`, `CONVERSATION_UNLOAD_THRESHOLD = 150` (3x chunk limit).
7. **Inbound-Only Natural Trimming**: When live inbound messages or timeline events increase the active in-memory collection beyond `CONVERSATION_UNLOAD_THRESHOLD` (150), excess items at the top/front are sliced off (`.slice(-CONVERSATION_UNLOAD_THRESHOLD)`).
8. **No Bottom Trimming on Scroll-Up**: Prepending older chunks via `loadOlder()` MUST NOT trim messages or timeline events from the bottom.
9. **Older History Availability (`hasOlder`)**: `hasOlder` remains active (`true`) whenever the oldest in-memory item sequence > 1 or cursor (`nextBeforeSeq` / `nextBefore`) > 0.
10. **No Zombie Resurrection**: Unloaded messages and activity entries must not be resurrected from secondary stores (`messagesByPeer`, `managedHistory`). Fallback entries with `sequence < oldestEventSeq` (or sequence <= 0 when oldestEventSeq > 1) are skipped.

11. **True bottom**: The 180px threshold decides whether following is enabled, not whether following succeeded. After followed content settles, bottom distance MUST be at most 2 CSS pixels.
12. **Bounded pagination suspension**: Older-history loading may suspend following only while the request and prepend alignment are pending. Failure, empty results, or duplicate-only results MUST release suspension even when the oldest visible ID does not change.
13. **Layout growth**: Following applies to height changes of existing items (streaming text and delayed images), not only newly appended IDs. Away from the bottom, those changes MUST preserve the reading position.
14. **Isolation and cancellation**: A pending pagination request or scheduled scroll from one conversation MUST NOT affect another conversation. Scheduled alignment MUST respect a user scroll away before it executes.

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

#### Scenario: [SCN-SCROLL-006] Subagent viewer dialog proximity auto-scrolling
- **Given**: Wolfgang is viewing the detailed activity timeline of a subagent inside the subagent viewer dialog.
- **When**: The user's scroll position in the subagent conversation view is within the proximity threshold (`DEFAULT_AUTO_SCROLL_THRESHOLD = 180`) of the bottom.
- **Then**: New streaming activity or polled history entries stick and follow the viewport to the bottom.
- **When**: Wolfgang scrolls upward past 180px from the bottom to inspect prior subagent turns.
- **Then**: Incoming subagent timeline updates preserve the user's scroll position without snapping or jumping to the bottom.
- **When**: Wolfgang scrolls back down within 180px of the bottom.
- **Then**: Proximity auto-scrolling automatically resumes on subsequent updates.

#### Scenario: [SCN-UNLOAD-001] Natural unloading when exceeding 150 messages during streaming updates
- **Given**: Wolfgang is viewing an active conversation with agent `supervisor`.
- **And**: The conversation currently holds 150 messages or timeline events in memory.
- **When**: A new inbound message or timeline event arrives via WebSocket or streaming update.
- **Then**: The newest message is appended to the bottom.
- **And**: The oldest item is trimmed from the top of the collection, preserving the collection size at `CONVERSATION_UNLOAD_THRESHOLD` (150).
- **And**: The active `hasOlder` indicator remains `true` because the oldest in-memory item sequence is > 1.
- **And**: Secondary caches (such as Redux store `messagesByPeer`) are reconciled with the trimmed set to prevent zombie resurrection.

#### Scenario: [SCN-UNLOAD-002] Re-fetching unloaded chunks via "Load older history" or scrollback
- **Given**: Wolfgang is viewing a conversation where older items were trimmed due to natural unloading.
- **And**: `hasOlder` is `true` because `oldestSeq > 1`.
- **When**: Wolfgang clicks "Load older history" or scrolls to the top of the viewport.
- **Then**: The system issues a fetch for the preceding chunk using `beforeSeq = oldestSeq`.
- **And**: The fetched older chunk is prepended to the top of the conversation.
- **And**: Existing items at the bottom are NOT trimmed, expanding the reading window without jumping or losing context.

### Edge & Error Scenarios

#### Scenario: [SCN-SCROLL-005] Empty or short conversation feeds
- **Given**: An agent conversation has very few messages and `scrollHeight <= clientHeight`.
- **When**: New events are added.
- **Then**: Distance from bottom is `<= 0`, which is `<= 180`, properly triggering auto-scroll without error.

#### Scenario: [SCN-SCROLL-007] Pagination failure or no-progress result releases following
- **User Goal**: Return to live activity after an unsuccessful history request.
- **Given**: A scrollable conversation has older history available.
- **When**: An older-history request fails, returns no items, or returns only already visible items.
- **Then**: The visible history is not discarded and the reading position remains stable.
- **When**: Wolfgang returns within 180px of the bottom and two successive messages arrive.
- **Then**: Each message settles at true bottom (distance <= 2px), without reload or another successful pagination request.

#### Scenario: [SCN-SCROLL-008] Pagination state cannot leak across conversation switches
- **Given**: An older-history request is pending in conversation A.
- **When**: Wolfgang opens scrollable conversation B before that request completes.
- **Then**: B opens at true bottom and follows new messages.
- **When**: A's request completes unsuccessfully or without new items.
- **Then**: B remains at true bottom and continues following.
- **When**: Wolfgang returns to A.
- **Then**: A opens at true bottom and follows subsequent messages.

#### Scenario: [SCN-SCROLL-009] Existing item grows without an ID change
- **Given**: A scrollable conversation is at true bottom and its latest item contains a pending image or streaming response.
- **When**: That item's layout grows without adding a new timeline item.
- **Then**: The viewport follows to true bottom after layout settles.
- **When**: Wolfgang scrolls more than 180px away and another existing item's image or text grows.
- **Then**: The reading position stays within 2px of its previous scrollTop.
- **When**: Wolfgang returns near bottom and further same-ID growth occurs.
- **Then**: Following resumes to true bottom.

#### Scenario: [SCN-SCROLL-010] User scrolling away cancels queued following
- **Given**: Content growth has scheduled bottom alignment while the user is near bottom.
- **When**: Wolfgang scrolls more than 180px away before that alignment runs.
- **Then**: The queued alignment does not pull the viewport back down.

#### Scenario: [SCN-SCROLL-011] Return to bottom during successful pagination
- **Given**: An older-history request started while Wolfgang was reading earlier messages.
- **When**: Wolfgang returns to bottom before the delayed successful response arrives.
- **Then**: Prepending respects the newer bottom intent instead of restoring the request-start reading position.
- **And**: The viewport remains at true bottom and follows subsequent live messages.

#### Scenario: [SCN-SCROLL-012] Successful older history preserves a visible reading anchor during live updates
- **Given**: Wolfgang is reading history with a visible message away from bottom.
- **When**: An older-history request is delayed and a live message arrives before it completes successfully.
- **Then**: The same reading message remains at its previous viewport offset within 2px after both updates settle.
- **And**: Newly fetched older messages and the concurrent live message remain available.

#### Scenario: [SCN-SCROLL-013] Successful stale pagination cannot contaminate another conversation
- **Given**: An older-history request for conversation A is pending.
- **When**: Wolfgang switches to B and A's successful response subsequently arrives.
- **Then**: No A messages appear in B, B's visible reading anchor is unchanged, and returning to bottom resumes B's following.
- **And**: Returning to A opens at true bottom and follows new messages.

#### Scenario: [SCN-SCROLL-014] Front trimming preserves a surviving visible reading anchor
- **Given**: The conversation has more than 150 persisted messages and Wolfgang has loaded a 150-message reading window or an expanded paginated window larger than 150.
- **And**: Wolfgang is reading a visible message away from bottom that will survive the next front trim.
- **When**: A live message arrives and the in-memory window is trimmed from the front to 150 messages.
- **Then**: The same surviving message remains visible at its previous viewport offset within 2px after layout settles and throughout a bounded observation interval.
- **And**: The live message is retained, removed oldest messages disappear, and the retained window is bounded at 150.
- **And**: This does not require preserving the position of content removed by the trim, and does not depend on native browser scroll anchoring being enabled.

---

## 4. Playwright End-to-End Suite Mapping

| Scenario ID | Test Suite | Test Case Description |
| :--- | :--- | :--- |
| `SCN-SCROLL-001` | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | Verify auto-scroll sticks to bottom on new messages when near bottom |
| `SCN-SCROLL-002` | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | Verify scroll position remains pinned when scrolled up > 180px |
| `SCN-SCROLL-003` | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | Verify scrolling back down resumes auto-scroll followership |
| `SCN-SCROLL-004` | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | Verify switching agents scrolls to bottom regardless of prior scroll state |
| `SCN-SCROLL-006` | `web/router-ui-react/tests/component/subagent-viewer-dialog.test.tsx` | Verify subagent viewer dialog follows proximity auto-scrolling when near bottom and locks when scrolled up |
| `SCN-UNLOAD-001` | `web/router-ui-react/tests/component/message-list-timeline.test.tsx`, `web/router-ui-react/tests/unit/use-conversation-unload.test.ts` | Natural unloading when exceeding 150 messages during streaming updates |
| `SCN-UNLOAD-002` | `web/router-ui-react/tests/component/message-list-timeline.test.tsx`, `web/router-ui-react/tests/unit/use-conversation-unload.test.ts` | Re-fetching unloaded chunks via "Load older history" or scrollback |
| `SCN-SCROLL-007` | `tests/e2e/conversation-scroll-regressions.spec.ts` | pagination failure/empty/duplicate releases follow after returning near bottom |
| `SCN-SCROLL-008` | `tests/e2e/conversation-scroll-regressions.spec.ts` | pending pagination failure/empty/duplicate switch isolation |
| `SCN-SCROLL-009` | `tests/e2e/conversation-scroll-regressions.spec.ts` | delayed same-ID image growth following/reading/resumed |
| `SCN-SCROLL-010` | `tests/e2e/conversation-scroll-regressions.spec.ts` | supporting scheduler check cancels queued alignment |
| `SCN-SCROLL-011` | `tests/e2e/conversation-scroll-regressions.spec.ts` | successful delayed pagination respects return to bottom |
| `SCN-SCROLL-012` | `tests/e2e/conversation-scroll-regressions.spec.ts` | successful prepend preserves visible reading anchor with concurrent live update |
| `SCN-SCROLL-013` | `tests/e2e/conversation-scroll-regressions.spec.ts` | successful stale pagination cannot contaminate another conversation |
| `SCN-SCROLL-014` | `tests/e2e/conversation-scroll-regressions.spec.ts` | front trim preserves surviving reading anchor in bounded/expanded window |

Regression verification uses real REST-seeded messages persisted by the isolated SQLite router, real WebSocket notifications and confirmed delivery receipts, and the reverse proxy. Targeted pagination HTTP responses and delayed image HTTP responses are controlled. Failure coverage also fails the legacy pagination fallback and waits for both requests. Following measures true-bottom geometry, not just proximity or visibility. Reading checks observe settled geometry over a bounded interval.

SCN-SCROLL-010 is a supporting scheduler check in the same real-boundary fixture: it temporarily holds browser animation-frame callbacks to establish ordering, changes DOM scroll position, then releases callbacks. It does not claim native user-input E2E coverage. No component functions or stores are mocked. Component coverage was updated for pagination and same-ID conversation changes. The synthetic prepend check was retired in favor of real-boundary pagination and visible-anchor regressions in this suite.
