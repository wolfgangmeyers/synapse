# Implementation Plan: Conversation Window Natural Unload & Bounded Cache

## Problem Statement
The WebUI conversation view currently loads messages in chunks of 50 items by default (`DEFAULT_CONVERSATION_CHUNK_LIMIT = 50`).
However, as new streaming messages and activity entries roll in via WebSockets or optimistic user sends, the local cache retains messages up to 5,000 items (`HISTORY_LIMIT = 5000`). Over prolonged streaming sessions, unbounded accumulation degrades UI performance, rendering speed, and DOM responsiveness until a hard page refresh.

Wolfgang's requirement:
- Window loaded by default: **50 messages**
- Unload threshold: **3x default window** = **150 messages**
- When new messages arrive and total stored messages exceed the unload threshold (150), the oldest messages beyond that threshold must naturally unload from the local active cache.
- The UI must continue to support scrolling up or clicking "Load older history" to re-fetch unloaded history from the backend on demand.

---

## 1. Architecture & Impact Analysis

### Stores & Caches to Bound
1. **`useConversation` hook (`web/router-ui-react/src/hooks/use-conversation.ts`)**:
   - Holds `timelineEvents` and `messages`.
   - On incoming WebSocket message/event append, when count exceeds `CONVERSATION_UNLOAD_THRESHOLD` (150), slice off the oldest excess items from the front: `.slice(-CONVERSATION_UNLOAD_THRESHOLD)`.
   - Trimming must **only** trigger on new incoming messages/events, **never** on `loadOlder()`. Prepending older history expands the reading window cleanly without trimming from the bottom.
   - Synchronize Redux store: Call `onReconcile(peer, trimmedMessages, highWaterSequence)` when trimming `messages` so that `state.messagesByPeer` in Redux is updated, preventing `App.tsx` (`mergeMessages(selectMessages(...), conversation.messages)`) from resurrecting pruned messages as zombies.
   - Update `hasOlder`: Derived from `(timelineEvents.length > 0 && timelineEvents[0].seq > 1) || (nextBeforeSeq !== null && nextBeforeSeq > 0) || (nextBefore !== null && nextBefore > 0)`. When items are trimmed from the front, `oldestSeq > 1` ensures `hasOlder` remains `true` so scroll-up or clicking "Load older history" can re-fetch unloaded history using `beforeSeq = oldestSeq`.

2. **`MessageList` Component (`web/router-ui-react/src/components/MessageList.tsx`)**:
   - Prevent zombie resurrection from `activityEntries`: When building `items`, if `effectiveEvents` has been trimmed (meaning `oldestEventSeq > 1`), do not inject fallback `activityEntries` or `messages` whose timestamps or sequences precede the oldest loaded timeline event.
   - Retain existing smooth scroll anchoring when prepending older history via `loadOlder()`.

3. **Global Constants (`web/router-ui-react/src/domain/conversation.ts` or `src/domain/history.ts`)**:
   - `export const DEFAULT_CONVERSATION_CHUNK_LIMIT = 50;`
   - `export const CONVERSATION_UNLOAD_THRESHOLD = 3 * DEFAULT_CONVERSATION_CHUNK_LIMIT; // 150`
   - Keep long-term persistent store limits intact while enforcing active conversation window limits in memory.

---

## 2. Behavioral Specification Update

Per `docs/behavioral-specification-standard.md`, update `docs/specs/conv-scroll-and-pagination.spec.md` (`SPEC-CONV-003`):
- **Constants**:
  - `DEFAULT_CONVERSATION_CHUNK_LIMIT = 50`
  - `CONVERSATION_UNLOAD_THRESHOLD = 150` (3x chunk limit)
- **Invariants**:
  - **Inbound-Only Trimming**: When incoming messages or live timeline events increase in-memory count beyond 150, the oldest items beyond the 150 threshold are dropped (`.slice(-150)`).
  - **No Bottom Trimming on Scroll-Up**: Calling `loadOlder()` prepends older chunks without trimming from the bottom, preserving the user's reading position.
  - **Older History Availability (`hasOlder`)**: If the oldest remaining item in memory has `sequence > 1`, `hasOlder` MUST evaluate to `true`, allowing seamless paging backward.
  - **No Zombie Resurrection**: Unloaded messages and activity entries must not be resurrected by secondary stores (`messagesByPeer`, `managedHistory`).
  - **Scrollback Re-fetching**: Clicking "Load older history" or scrolling to the top fetches the preceding chunk using the oldest sequence cursor (`beforeSeq = oldestSeq`).
- **Scenarios**:
  - `[SCN-UNLOAD-001]` Natural unloading when exceeding 150 messages during streaming updates.
  - `[SCN-UNLOAD-002]` Re-fetching unloaded chunks via "Load older history" or scrollback.

---

## 3. Step-by-Step Implementation Steps

1. **Update Behavioral Specification**:
   - Edit `docs/specs/conv-scroll-and-pagination.spec.md` to document the 150-message unload threshold, invariants, and user scenarios.

2. **Define Constants**:
   - In `web/router-ui-react/src/domain/history.ts` (or `conversation.ts`):
     - `export const DEFAULT_CONVERSATION_CHUNK_LIMIT = 50;`
     - `export const CONVERSATION_UNLOAD_THRESHOLD = 3 * DEFAULT_CONVERSATION_CHUNK_LIMIT; // 150`

3. **Update `use-conversation.ts`**:
   - In the WebSocket subscription handler (`transport.subscribe`):
     - Check length when appending live events to `timelineEvents`:
       ```ts
       setTimelineEvents((current) => {
         const next = [...current, liveEvt];
         return next.length > CONVERSATION_UNLOAD_THRESHOLD ? next.slice(-CONVERSATION_UNLOAD_THRESHOLD) : next;
       });
       ```
     - Check length when appending to `messages`:
       ```ts
       setMessages((current) => {
         // ... append ...
         const trimmed = next.length > CONVERSATION_UNLOAD_THRESHOLD ? next.slice(-CONVERSATION_UNLOAD_THRESHOLD) : next;
         if (trimmed.length !== next.length) {
           onReconcile?.(peer, trimmed, 0);
         }
         return trimmed;
       });
       ```
   - In `loadOlder()`:
     - Ensure prepending does NOT trim from the bottom.
   - Update `hasOlder`:
     ```ts
     const oldestSeq = timelineEvents.length > 0 ? timelineEvents[0].seq : undefined;
     const hasOlder = (oldestSeq !== undefined && oldestSeq > 1)
       || (nextBeforeSeq !== null && nextBeforeSeq !== undefined && nextBeforeSeq > 0)
       || (nextBefore !== null && nextBefore !== undefined && nextBefore > 0);
     ```

4. **Update `MessageList.tsx`**:
   - Prevent zombie items: If `effectiveEvents` has an `oldestSeq > 1` (or minimum timestamp `minTimestamp`), filter fallback items from `messages` and `activityEntries` so older items preceding the window are not re-injected.

5. **Verification & Testing**:
   - Update and run Vitest component tests in `web/router-ui-react/tests/component/message-list-timeline.test.tsx` and unit tests in `web/router-ui-react/tests/unit/conversation-projection.test.ts`.
   - Run Playwright E2E tests in `tests/e2e/conversation-scroll-and-pagination.spec.ts`.
   - Verify all tests pass cleanly.
