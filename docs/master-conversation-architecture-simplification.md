# Master Conversation Architecture Simplification & Untangling Blueprint

## 1. Executive Summary & Core Diagnosis

The conversation view in `web/router-ui-react` suffers from **accidental complexity and competing multi-tier pipelines**. The system repeatedly regresses (speech bubbles reverting to envelope cards, missing image attachments, scroll jumping, pagination loops, and input typing freezes) because the frontend was never cleanly migrated to the unified `conversation_timeline` architecture. Instead, `conversation_timeline` was layered as a *fourth* parallel data stream on top of three legacy streams.

By eliminating redundant streams, purging overengineered heuristic classifiers, isolating composer state, and adhering strictly to a single client-server contract, we can eliminate over **2,000 lines of entangled code (-64%)** and permanently resolve all six user-reported issues.

---

## 2. Monolithic Files & Accidental Complexity Inventory

From the symbol map across 74 component/domain files and 15,685 LOC:

1. **`CompleteTimelineEntry.tsx` (849 LOC)**:
   - Contains 35 symbols and 12 distinct heuristic classifiers (`cleanOneLine`, `isMailboxSendToUser`, `extractSubjectFromText`, `subagentCompleteVisual`, etc.).
   - Relies heavily on fragile regex parsing (`/^Mailbox message batch/`, `/^(?:(?:From|To|Subject):[^\n]*\r?\n)+/gi`).
   - Guesses presentation models from unstructured history text rather than relying on typed schema attributes.
2. **`MessageList.tsx` (673 LOC)**:
   - Monolithic `MessageList` component (337 LOC) and `timelineEventToFeedItems` (193 LOC).
   - Contains a 120-second fuzzy timestamp deduplication window (`Math.abs(entryTime - mTime) <= 120_000`).
   - Contains regex body normalizer `normalizeMessageBodyForComparison` to strip attachment footers.
   - Generates synthetic message objects with fabricated IDs (`activity-msg-...`).
   - Fires redundant internal API fetches (`internalEvents`) that race against parent hooks.
3. **`App.tsx` (971 LOC)**:
   - Manages global composer draft state (`composerDraft`), re-rendering all 971 lines and the entire tree on every keystroke.
   - Triggers secondary uncoordinated history queries (`session.transport.http.managedHistory(..., "complete", 100)`).
   - Passes 40+ props through the layout hierarchy down into conversation components.
4. **`ConversationHeader.tsx` (916 LOC)**:
   - Monolithic component (828 LOC) with 8 `useState` hooks, 5 `useEffect` hooks, and direct `localStorage` access.
5. **`StateConfigModal.tsx` (974 LOC)**:
   - Monolithic dialog (956 LOC) with 25 separate `useState` hooks.
6. **`use-router-session.ts` (252 LOC) & `reducer.ts` (289 LOC)**:
   - Maintains legacy `managedHistory` and `messagesByPeer` stores in global state, caching up to 5,000 entries and repeatedly merging them against active views.

---

## 3. Client-Server Contract Redundancies & Overengineering

From the contract audit between `router-proxy` (Go) and `web/router-ui-react` (TypeScript):

1. **Triple Competing Endpoints**:
   - `/v1/conversation?agent_id=...` (Modern, unified SQLite monotonic `seq` timeline).
   - `/v1/history?view=complete` (Legacy raw execution logs).
   - `/v1/conversations` (Legacy tmux peer-to-peer chat).
   - *Resolution*: The frontend conversation view must query **only** `/v1/conversation`.
2. **Dual WebSocket Streams**:
   - `/v1/agents` (Main WebSocket streaming mailbox and agent status frames).
   - `/v1/ui-live` (Live tool and managed history stream).
   - *Resolution*: The router already emits `timeline_event` frames; the client must consume them directly.
3. **Attachment Contract Inconsistency**:
   - Composer sends `{ type: "image", data_url: "data:..." }`.
   - Backend stores attachments in metadata and serves relative paths `/v1/attachments/<id>`.
   - `AttachmentGallery.tsx` strictly expects `{ url: "..." }` and tests against `/^\/v1\/attachments\/[a-zA-Z0-9_-]+$/`.
   - When given `data_url`, `item.url` is `undefined`, so the regex fails and attachments in user messages are completely dropped.
4. **Synthetic ID Chaos**:
   - Server monotonic `seq` (integer) vs server UUID vs synthetic client IDs (`activity-msg-*`, `hist:*`, `msg:*`).
   - Mismatched IDs cause React keys to change across hydration passes, triggering erratic scroll jumps and re-renders.

---

## 4. Root Cause Breakdown for User-Reported Symptoms

### Issue 1: Flash & Shift (Initial Speech Bubbles -> Reversion to Envelope Cards)
- **Root Cause**: Fast initial load comes from `/v1/conversation` via `useConversation` (correctly classifying assistant turns as bubbles). 100–300ms later, `App.tsx` finishes fetching `session.transport.http.managedHistory(...)`. This triggers the legacy loop in `MessageList.tsx`, where `isMailboxSendToUser()` fails or the 120s timestamp comparison fails to match the entries. The assistant reply falls through and is converted into a generic activity card with an envelope icon: `"From conversation-simplification"`.

### Issue 2: Missing Image Attachments in User Messages
- **Root Cause**: `Composer.tsx` and `App.tsx` produce `{ type: "image", data_url: ... }`. The backend saves this in SQLite `event.metadata.attachments`. But `AttachmentGallery.tsx` expects `item.url` and validates it against a regex. Because `item.url` is `undefined` (it's in `data_url`), the gallery discards the attachment.

### Issue 3: Random Sudden Scrolling Up
- **Root Cause**: `MessageList.tsx` triggers `handleInternalLoadOlder()` whenever `scrollTop < 100`. On short conversations or initial layout where `scrollHeight <= clientHeight`, `scrollTop` is `0` (`0 < 100`), instantly triggering an accidental load of older messages. When older items are prepended, DOM height expands by thousands of pixels, but `scrollTop` remains at its original value, suddenly throwing the user up into ancient history. Furthermore, shifting synthetic keys (`activity-msg-*` swapping with `hist:*`) cause React to remount DOM nodes, destabilizing the scroll offset.

### Issue 4: Accumulating Conversation & Broken Pagination
- **Root Cause**: Five competing pipelines (`timelineEvents`, `messages`, `activityEntries`, `internalEvents`, and direct WS frames) continuously re-append items. `reducer.ts` slices history at 5,000 items, dumping hundreds or thousands of unvirtualized DOM nodes into the page.

### Issue 5: Typing Lag & Freezes (Keystroke Main-Thread Blocks)
- **Root Cause**: `composerDraft` and `composerSelection` state live at the top of `App.tsx` (line 219). Every keystroke fires `setComposerDraft`, re-rendering all 971 lines of `App.tsx`, `Conversation.tsx`, and all 500+ un-memoized feed DOM nodes in `MessageList.tsx`.

### Issue 6: Envelope Card Title Bloat
- **Root Cause**: In mailbox envelope activity cards, the header/title takes the full multi-paragraph `event.content` or `presentation.summary`, rendering a massive block of text in the header followed by the exact same text repeated in the body.

---

## 5. Radical Simplification Architecture

```
                    ┌──────────────────────────────────────────────┐
                    │      Router Backend (router-proxy)           │
                    │   Single Authority: conversation_timeline    │
                    └──────────────────────┬───────────────────────┘
                                           │
                        WebSocket / HTTP Timeline Feed
                     (timeline_event / /v1/conversation)
                                           │
                                           ▼
                    ┌──────────────────────────────────────────────┐
                    │       useConversation (Single Source)        │
                    │   Maintains: events: ConversationTimelineEvent[]
                    │   Cursor: before_seq, Monotonic ordering     │
                    └──────────────────────┬───────────────────────┘
                                           │
                                     timelineEvents
                                           │
                                           ▼
┌────────────────────────┐          ┌──────────────────────────────┐
│  Composer (Isolated)   │          │   MessageList (Pure)         │
│ - Local draft state    │          │ - Stable event.id keys       │
│ - Zero App re-renders  │          │ - Pure mapping to Bubbles    │
│ - Direct send dispatch │          │ - Stable scroll compensation │
└────────────────────────┘          └──────────────────────────────┘
```

### Key Pillars:
1. **Single Source of Truth**:
   - Only `conversation_timeline` is queried for the conversation view.
   - Completely delete `activityEntries` and `internalEvents` from `MessageList.tsx`.
   - Remove `managedHistory` and `messagesByPeer` polling from `App.tsx` and `reducer.ts`.
2. **Pure Timeline Event Mapping**:
   - `event.kind === "message"`:
     - If `event.role === "user"` or from current user -> Outgoing `MessageBubble` (`isUser={true}`).
     - If `event.role === "assistant"` or from agent -> Incoming `MessageBubble` (`isUser={false}`).
     - If between two distinct external workers -> Envelope card with compact title (`Message from <sender> to <recipient>`).
   - `event.kind === "tool"`: Render compact `ToolCard` (command, status, expandable output).
   - `event.kind === "thinking"`: Render collapsible `ThinkingTrace`.
   - `event.kind === "context_reset"`: Render `ContextResetDivider`.
3. **Composer State Isolation**:
   - Move draft state into `Composer.tsx`.
   - Keystrokes in the textarea only re-render the 100 LOC `Composer` component, keeping typing at 60fps.
4. **Scroll Anchoring with Height Delta Compensation**:
   - Only trigger pagination when `scrollTop < 80` AND `scrollHeight > clientHeight + 100`.
   - When prepending older items, compensate scroll height:
     ```ts
     const prevScrollHeight = container.scrollHeight;
     // prepend items...
     requestAnimationFrame(() => {
       container.scrollTop += (container.scrollHeight - prevScrollHeight);
     });
     ```
5. **Standardized Attachment Contract**:
   - Support both `item.data_url` and `item.url`.
   - Pass attachments to both user and assistant speech bubbles without regex gating.
6. **Retire `CompleteTimelineEntry.tsx`**:
   - Delete the 849-line heuristic file completely. Replace with small, focused components (`ToolCard`, `ThinkingTrace`, `ContextResetDivider`, ~40 lines each).

---

## 6. Code Elimination Metrics

| File | Current LOC | Target LOC | Delta | Notes |
|---|---|---|---|---|
| `CompleteTimelineEntry.tsx` | 849 | 0 | **-849** | Completely removed. |
| `MessageList.tsx` | 673 | 180 | **-493** | Stripped fuzzy matching, normalizers, synthetic IDs, and internal fetches. |
| `App.tsx` | 971 | 550 | **-421** | Removed history polling, composer state bubbling, duplicate merging. |
| `use-conversation.ts` | 226 | 130 | **-96** | Refactored into pure timeline hook on `/v1/conversation`. |
| `reducer.ts` | 289 | 210 | **-79** | Purged `managedHistory` and `messagesByPeer`. |
| `Conversation.tsx` | 81 | 50 | **-31** | Clean container without 40 pass-through props. |
| **Total** | **3,089** | **1,120** | **-1,969 LOC (-64%)** | Massive net reduction in complexity. |

---

## 7. Step-by-Step Implementation & Verification Plan

1. **Step 1: Isolate Composer State**
   - Make `Composer.tsx` self-contained with local state and debounced draft persistence. Decouple from `App.tsx`.
2. **Step 2: Attachment Normalization**
   - Update `AttachmentGallery.tsx` and `MessageBubble.tsx` to handle `item.data_url || item.url` cleanly, removing the restrictive regex.
3. **Step 3: Clean Scroll Anchoring & Pagination**
   - Refactor `MessageList.tsx` scroll handling: compensate height delta on prepending older items, and guard against `scrollTop === 0` false triggers on short chats.
4. **Step 4: Pure Timeline Event Rendering in `MessageList.tsx`**
   - Remove `activityEntries`, `internalEvents`, `isMailboxSendToUser`, 120s fuzzy deduplication, and synthetic IDs.
   - Render directly from `timelineEvents`: user bubbles, assistant bubbles, tool cards, and compact envelope cards.
5. **Step 5: Purge Legacy Reducer State in `reducer.ts` and `App.tsx`**
   - Delete `session.transport.http.managedHistory(...)` polling in `App.tsx`.
   - Remove `messagesByPeer` and `managedHistory` from the session reducer.
6. **Step 6: Comprehensive Test Suite & Visual Verification**
   - Run vitest suites (`message-list-timeline.test.tsx`, `attachment-gallery.test.tsx`, `use-conversation.test.tsx`).
   - Run Go store tests (`go test ./...` in `router-proxy`).
   - Verify zero git push invariant.
