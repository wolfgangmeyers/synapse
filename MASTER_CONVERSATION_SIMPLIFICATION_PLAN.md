# Master Conversation Architecture Simplification & Defect Resolution Plan

## 1. Executive Summary & Goals
The conversation UI in `web/router-ui-react` suffers from 7 interrelated defects stemming from architectural fragmentation: multiple competing data streams, an unbounded global array, top-level state pollution, brittle heuristics (fuzzy timestamp matching), and monolithic presentation logic.

This plan details the resolution of all 7 issues while retiring ~2,000 lines of dead/accidental complexity, preserving 100% of existing CSS styling, adhering strictly to `docs/specs/SPEC-CONV-001` through `005`, and validating via the Playwright E2E test harness without pushing to git remote.

---

## 2. The 7 Issues & Targeted Concrete Architecture

### Issue 1: Flash & Shift (Assistant Speech Bubbles Reverting to Envelope Cards)
- **Symptom**: Navigating back/forward or receiving assistant turn responses initially renders speech bubbles, which then shift into envelope cards ("From <agent>").
- **Root Cause**:
  1. Redundant secondary hydration streams in `App.tsx` (`session.transport.http.managedHistory(...)`) and `useConversation` clobber feed state.
  2. Classification logic lacked precise discriminator: assistant responses must check `event.kind === "message"` with `(event.role === "assistant" || sender === agentId)` where recipient is `user`/`wolfgang`/empty. Tool calls and thinking events must NEVER be classified as speech bubbles.
- **Solution**:
  - Establish `conversation_timeline` as the single authoritative stream. Remove `managedHistory` polling and duplicate merge loops from `App.tsx` and `useConversation`.
  - In `MessageList.tsx`, implement strict, unambiguous classification:
    - User Bubble: `event.kind === "message"` AND (`event.role === "user"` OR `sender === currentUser` OR `sender === "user"`).
    - Assistant Bubble: `event.kind === "message"` AND (`event.role === "assistant"` OR `sender === agentId`) AND `recipient !== otherAgent`.
    - Envelope Card: `event.kind === "message"` AND is true inter-agent mail (`sender !== user && recipient !== user && sender !== recipient`).
    - Tool/Action Activity: `event.kind === "tool_call" || event.kind === "tool_result" || event.kind === "action"`.
    - System / Notification: `event.kind === "system" || event.kind === "notification" || event.kind === "context_reset"`.

### Issue 2: Missing Image Attachments in User Messages
- **Symptom**: Images pasted or attached in the composer do not display in the conversation view.
- **Root Cause**:
  1. Frontend `AttachmentGallery` strictly validates `item.url` against regex `/^\/v1\/attachments\/.../`. When composer sends `{ type: "image", data_url: ... }`, `url` is undefined, dropping the image.
  2. Backend `recordTimelineMessageTx` omitted `attachments` from event metadata serialization.
- **Solution**:
  - Update `domain/images.ts` and `AttachmentGallery.tsx` to handle `item.data_url || item.url`.
  - Verify Go backend `recordTimelineMessageTx` includes `attachments` in metadata JSON.
  - Suppress raw attachment filesystem path strings from rendered message bubble text when an image gallery preview is present.

### Issue 3: Random Sudden Scrolling Up
- **Symptom**: Conversation view abruptly scrolls up to an earlier position during chat or stream updates.
- **Root Cause**:
  1. `scrollTop < 100` trigger fires on initial load or short feeds when `scrollTop === 0`, prepending older items without compensating scroll height delta (`scrollTop += newHeight - prevHeight`).
  2. Keys like `event.event_id:index` change dynamically, breaking React reconciliation and jumping scroll.
- **Solution**:
  - Guard scroll-top pagination trigger against `scrollTop === 0` on unscrollable containers (e.g. `scrollHeight <= clientHeight`).
  - Calculate height delta on prepend: `container.scrollTop += (container.scrollHeight - prevScrollHeight)`.
  - Enforce strictly stable React keys: `key={event.event_id || \`seq-${event.sequence}\`}` without dynamic array index suffixes.

### Issue 4: Accumulating Conversation & Broken Pagination
- **Symptom**: Reducer array grows up to 5,000 items; initial load dumps huge history slices.
- **Root Cause**: Unbounded `managedHistory.slice(-5000)` in `reducer.ts` and absence of cursor-based pagination.
- **Solution**:
  - Do NOT implement complex virtual DOM windowing (which breaks native scrolling).
  - Implement clean cursor-based fetch pagination: initial load fetches latest 50 events (`/v1/conversation?limit=50`).
  - Scrolling to top triggers fetch of preceding 50 events using `before_seq` cursor.
  - Deprecate legacy unbounded `managedHistory` accumulating arrays.

### Issue 5: Typing Lag / Keystroke Freezes
- **Symptom**: Typing into the message composer input exhibits significant lag and frame drops.
- **Root Cause**: `composerDraft` state lives at the root of `App.tsx:219`. Every keystroke triggers a full re-render of `App.tsx` (971 LOC), `Conversation.tsx`, and all 500+ feed DOM nodes.
- **Solution**:
  - Move draft text state entirely inside `Composer.tsx` as local React state (`useState`).
  - Parent `App.tsx` only receives callbacks on submit (`onSend(text, attachments)`) or debounced draft persistence if needed.
  - Wrap `MessageList` in `React.memo` so parent renders never re-render the message list.

### Issue 6: Envelope Card Title Bloat
- **Symptom**: Envelope cards display the entire multi-paragraph message body inside the card header/title (in bold), duplicating the body below and breaking visual density.
- **Root Cause**: `presentation.summary` or `title` was populated with the full `event.content`.
- **Solution**:
  - Card header title is strictly:
    - Subject line (if present), e.g. "Subject: <title>"
    - Or compact one-liner: "Message from <sender> to <recipient>"
  - Full message text belongs strictly in the card body / expandable content section.

### Issue 7: Monolithic Complexity Reduction (-2000 LOC) & CompleteTimelineEntry Replacement
- **Symptom**: Massive files (`CompleteTimelineEntry.tsx` 849 LOC, `MessageList.tsx` 673 LOC, `App.tsx` 971 LOC) with 120s fuzzy deduplication, regex body normalizers, and synthetic IDs.
- **Solution Component Breakdown**:
  Replace monolithic `CompleteTimelineEntry.tsx` with targeted, modular components under `src/components/timeline/`:
  1. `ToolActivityItem.tsx` (~80 LOC): renders tool call/result, execution badge, duration, collapsed stdout/stderr modal.
  2. `MailboxEnvelopeItem.tsx` (~70 LOC): renders inter-agent envelope card with compact header (`To <recipient>` / `From <sender>`), subject, and expandable body.
  3. `SystemNoticeItem.tsx` (~50 LOC): renders context resets, compaction alerts, and subagent completion notices.
  4. `ThinkingTraceItem.tsx` (~60 LOC): renders collapsible thinking trace accordion.
  - In `MessageList.tsx`:
    - Delete the 120-second fuzzy timestamp deduplication window (`fuzzyMatchHistoryItem`).
    - Delete regex message body normalizers.
    - Wire `ConversationTimelineEvent` directly to `MessageBubble` (user/assistant) or the dedicated modular timeline components above.

---

## 3. Phased Implementation Roadmap

1. **Phase 1: Composer Decoupling (Issue 5)**
   - Move draft state into `Composer.tsx`.
   - Verify typing lag is completely eliminated.
2. **Phase 2: Single Authoritative Timeline Pipeline (Issue 1 & 4)**
   - Remove duplicate `managedHistory` fetching from `App.tsx`.
   - Bind `MessageList` strictly to `useConversationTimeline(agentId)`.
   - Wire cursor pagination (`limit=50&before_seq=...`).
3. **Phase 3: Scroll Delta Compensation & Stable Keys (Issue 3)**
   - Implement `scrollHeight` delta compensation on prepend.
   - Use stable `event_id` / `sequence` keys without index suffixes.
4. **Phase 4: Attachment Resolution (Issue 2)**
   - Support `data_url` in `AttachmentGallery`.
   - Verify Go backend metadata preserves attachments.
   - Suppress path strings in bubble text when gallery preview exists.
5. **Phase 5: Envelope Headers & Modular Timeline Components (Issues 6 & 7)**
   - Extract `ToolActivityItem`, `MailboxEnvelopeItem`, `SystemNoticeItem`, `ThinkingTraceItem`.
   - Retire `CompleteTimelineEntry.tsx` (-849 LOC) and fuzzy deduplication (-493 LOC).
6. **Phase 6: E2E and Component Verification**
   - Run Vitest component suite (`npm test`).
   - Run Playwright E2E suite (`npm run test:e2e`).
   - Invariant check: ZERO commits pushed to git remote.
