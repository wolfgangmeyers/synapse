# Conversation UI Simplification Plan

**Target File:** `/home/wolfgang/code/mecha-developer/CONVERSATION_UI_SIMPLIFICATION_PLAN.md`  
**Author:** Mecha-Developer  
**Guiding Principle from Wolfgang:**  
> *"When in doubt, simplify! This will almost always help. It just needs to continue to meet my requirements. If you see cryptographic hashing and shit like that, get rid of it."*

---

## Section 1: Problem Summary & Root Cause Analysis

### 1.1 The Observed Flash / Reversion Bug
**Symptom:**  
When an agent conversation opens, the user briefly sees speech bubbles for user and assistant messages. A fraction of a second later, the assistant responses revert into envelope activity cards (`presentation.kind = "mailbox_message"`).

### 1.2 Root Cause Analysis
The flash / clobber is caused by **three conflicting data pipelines racing and clobbering each other** across `App.tsx`, `use-conversation.ts`, and `MessageList.tsx`:

1. **Dual Pipelines in `App.tsx`:**  
   `App.tsx` simultaneously feeds `MessageList`:
   - `messages`: from `useConversation` (projected from initial state / conversation endpoint).
   - `timelineEvents`: from `useConversation` / `fetchConversationTimeline`.
   - `activityEntries`: from `session.state.managedHistory` (`/v1/history?view=complete`).

2. **The Asynchronous Reversion Mechanism:**
   - **Render 1 (Instant):** `messages` renders first. All messages in `messages` are displayed as `<MessageBubble>` chat bubbles.
   - **Render 2 (100–300ms later):** `fetchConversationTimeline` (or `/v1/history`) completes. `MessageList` prioritizes `timelineEvents`.
   - **The Classification Defect in `timelineEventToFeedItems`:**  
     Inside `MessageList.tsx`:
     ```typescript
     // Checking if message belongs to user conversation:
     const isFromUser = (ownerId !== undefined && fromMeta === ownerId) ||
                        (currentUser !== undefined && fromMeta === currentUser) ||
                        (event.role === "user" && fromMeta === undefined);
     const isToUser = (ownerId !== undefined && toMeta === ownerId) ||
                      (currentUser !== undefined && toMeta === currentUser);

     if (isFromUser || isToUser) {
       return [/* MessageBubble */];
     }
     // Otherwise: inter-agent / worker mailbox message -> render as envelope activity card!
     ```
     For a standard assistant text response turn, `event.role === "assistant"`.
     `fromMeta` is the agent's ID (e.g. `worker-1`), so `isFromUser` is `false`.
     `toMeta` is either undefined, empty, or `"user"`. But `ownerId` / `currentUser` is often resolved asynchronously, defaulted to `"user"`, or compared against mismatched casing/agent IDs. If `toMeta` is undefined (which is true for standard assistant turns that are simply replies to the session rather than explicit addressed mail envelopes), `isToUser` evaluates to `false`!
     Because `isFromUser` and `isToUser` are both `false`, `timelineEventToFeedItems` misclassifies the assistant response as an inter-agent mailbox message and wraps it into an activity card with `presentation.kind = "mailbox_message"`.
   - **Clobbering via `seenIds`:**  
     Because `effectiveEvents` are processed first in `items = useMemo(...)`, `seenIds.add(item.id)` marks the ID as consumed. The subsequent `messages` loop skips the chat bubble because `seenIds.has(message.id)` is true. The chat bubble disappears and is replaced by the envelope card!

3. **Accidental Complexity & Spaghetti Deduplication:**
   `MessageList.tsx` contains over 200 lines of brittle heuristics:
   - Comparing normalized string bodies by stripping markdown image references.
   - Fuzzy timestamp matching within 120 seconds (`Math.abs(entryTime - mTime) <= 120_000`).
   - Redundant ID prefixes (`hist:${id}`, `complete:${id}`, `activity-msg-${id}`).
   - Running parallel fetches both in the parent hook and internally inside `MessageList` (`internalEvents`).

4. **Image Attachment Broken Previews:**
   - On backend recording: `recordTimelineMessageTx` in `conversation_timeline.go` only marshals `from`, `to`, `subject`, and `source_kind` into `metadata`; it completely omits `attachments`!
   - On frontend display: `AttachmentGallery` and `urlFor` use strict regexes on relative URLs (`/^\/v1\/attachments\/[A-Za-z0-9._-]+$/`). If `routerUrl` is missing, or the path differs, or attachments were only in `event.metadata.attachments`, previews return empty and fail to render.

---

## Section 2: Requirements & Strict Display Rules

### Direct Role Mapping (No Guesswork)
1. **User Messages** (`role === "user"`, or sent by operator/owner):
   - Render as `<MessageBubble isUser={true}>` (right-aligned blue bubble with user avatar).
2. **Assistant Text Responses** (`role === "assistant"` where `kind === "message"` and it is the agent answering the operator):
   - Render as `<MessageBubble isUser={false}>` (left-aligned purple bubble with agent avatar).
   - Any direct agent turn response to the operator is a chat bubble, NEVER an envelope card.
3. **Inter-Agent Mailbox Messages** (messages addressed between different agents, e.g. `supervisor` -> `worker` or worker -> subagent):
   - Render as Envelope Activity Cards (`presentation.kind = "mailbox_message"` in `CompleteTimelineEntry`).
   - Clear From / To headers and collapsible or compact card styling.
4. **System / Subagent / Reminder / Lifecycle Events**:
   - Render as Notification / Activity Cards via `CompleteTimelineEntry` (tools, context resets, thinking dropdowns, reminder alerts).
5. **Image Attachments**:
   - Direct resolution: whether a data URL (`data:image/...`), blob URL (`blob:...`), or router attachment path (`/v1/attachments/...`), resolve against `routerUrl` cleanly.
   - Render image thumbnails in `MessageBubble` / `AttachmentGallery` without broken URLs.
6. **Styling Preservation**:
   - **Retain 100% of existing CSS / design system**: dark glassmorphic styling, avatars, timestamps, glowing timeline rail, orb colors, spacing, and buttons.
   - No visual regressions to the UI shell.

---

## Section 3: Exact File Changes & Simplification Steps

### Step 1: Make Timeline Events the Single Source of Truth
**File:** `web/router-ui-react/src/components/MessageList.tsx`
- **Eliminate Dual-Pipeline Deduplication:**
  Remove internal parallel fetch (`internalEvents`), fuzzy 120-second timestamp comparisons, regex normalization deduplication, and multi-source collision code.
- **Unambiguous Classification Function:**
  Replace the convoluted conditional block with a clean, 20-line classifier:
  ```typescript
  export function classifyTimelineEvent(
    event: ConversationTimelineEvent,
    ownerId?: string,
    peerId?: string
  ): FeedItem {
    const meta = event.metadata ?? {};
    const from = typeof meta.from === "string" ? meta.from : undefined;
    const to = typeof meta.to === "string" ? meta.to : undefined;

    // 1. System / notifications / subagent completion
    if (from === "system" || event.content?.startsWith("[Direct message from system]") || event.content?.startsWith("[Subagent Completed]")) {
      return makeActivityItem(event, "notification");
    }

    // 2. Direct Conversation Messages
    if (event.kind === "message") {
      const isInterAgent = from && to && from !== ownerId && to !== ownerId && from !== "user" && to !== "user";

      // If it's an inter-agent mailbox message -> envelope card
      if (isInterAgent) {
        return makeActivityItem(event, "mailbox_message", { sender: from, recipient: to, subject: meta.subject });
      }

      // Otherwise, it is an operator <-> agent conversation turn -> chat bubble!
      const isOutgoing = event.role === "user" || from === ownerId || from === "user";
      return {
        type: "message",
        id: event.id,
        timestamp: event.created_at,
        sequence: event.seq,
        message: {
          id: event.id,
          body: event.content ?? "",
          direction: isOutgoing ? "outgoing" : "incoming",
          from: from ?? (isOutgoing ? (ownerId ?? "user") : (peerId ?? event.agent_id)),
          to: to ?? (isOutgoing ? (peerId ?? event.agent_id) : (ownerId ?? "user")),
          sequence: event.seq,
          created_at: new Date(event.created_at).toISOString(),
          recorded_at: new Date(event.created_at).toISOString(),
          attachments: meta.attachments ?? (event as any).attachments,
        },
      };
    }

    // 3. Operational Activity (tools, context resets, thinking)
    return makeActivityItem(event, event.kind);
  }
  ```
- **Result:** Assistant responses (`role: "assistant"`, not addressed to another agent) unambiguously become `type: "message"`. They NEVER flip into mailbox envelope cards.
- **Eliminate Clobbering:** `timelineEvents` forms the authoritative stream. Live outgoing optimistic messages from the composer are simply appended until acknowledged.

### Step 2: Fix Hook Data Flow and Stop Clobbering
**File:** `web/router-ui-react/src/hooks/use-conversation.ts`
- Clean up `useConversation` so `timelineEvents` is the primary array.
- On refresh, `fetchConversationTimeline` sets `timelineEvents`.
- In WebSocket / live event updates, live conversation frames update `timelineEvents` directly.
- Avoid maintaining duplicate divergent representations of the same event that race on initial load.

### Step 3: Clean Image URL Resolution in Attachments
**Files:**
- `router-proxy/internal/store/conversation_timeline.go`
- `web/router-ui-react/src/domain/images.ts`
- `web/router-ui-react/src/components/AttachmentGallery.tsx`
- **Backend Fix:** In `recordTimelineMessageTx` (and timeline migration), include `attachments` in the timeline event `metadata` JSON so persisted messages carry their attachments into the timeline.
- **Frontend URL Resolution:**
  In `images.ts` / `AttachmentGallery.tsx`, simplify `urlFor`:
  - If URL starts with `data:image/` or `blob:` or `http://` or `https://`, return as-is.
  - If URL starts with `/v1/attachments/`, prepend `routerUrl` cleanly using standard URL constructor.
  - No rejection over filename extensions or regex mismatches; if it's an attachment path, resolve it against the router origin.

### Step 4: Clean Up Prop Pipeline in App and Conversation
**Files:**
- `web/router-ui-react/src/App.tsx`
- `web/router-ui-react/src/components/Conversation.tsx`
- Pass `timelineEvents` directly as the unified stream to `MessageList`.
- Remove redundant duplicate feeds where both legacy `messages` and `activityEntries` were competing to override timeline events.

---

## Section 4: Testing & Verification Strategy

### 4.1 Unit & Component Tests (`vitest`)
1. **MessageList Render Test:**
   - Provide timeline events containing:
     - 1 operator user message (`role: "user"`).
     - 1 agent assistant response (`role: "assistant"`).
     - 1 inter-agent message (from: `supervisor`, to: `worker-2`).
     - 1 tool execution event (`kind: "tool"`).
   - Assert:
     - User message renders as `.bubble-wrap.outgoing` with user avatar circle.
     - Assistant response renders as `.bubble-wrap.incoming` with agent avatar.
     - Inter-agent message renders as envelope card (`presentation.kind = "mailbox_message"`).
     - Tool execution renders as tool activity card.
   - Assert NO chat bubbles are converted into envelope cards.
2. **Initial Render Stability Test:**
   - Simulate mounting with initial `messages` then receiving `timelineEvents` 50ms later.
   - Assert no visual jump or conversion of assistant messages into envelope cards.
3. **Image Attachment Preview Test:**
   - Render `AttachmentGallery` with both `data:image/...` (optimistic live send) and `/v1/attachments/...` (persisted history).
   - Assert both produce valid `<img>` elements with reachable `src` attributes.

### 4.2 Router Behavioral Parity Test (`go test`)
- Run `go test ./router-proxy/internal/store/...` to verify `recordTimelineMessageTx` preserves attachments in event metadata.
- Verify `fetchConversationTimeline` returns events with complete metadata.

### 4.3 Visual & Dev Server Verification
- Launch the UI against the local router.
- Verify the conversation timeline for a managed agent:
  - User messages show as blue chat bubbles.
  - Agent answers show as purple chat bubbles.
  - Inter-agent messages show as envelope cards.
  - Image attachments show clear thumbnails immediately upon sending and persist on page reload.
  - No initial render flash or reversion.

---

## Summary of Guiding Simplifications
- **No dual pipelines:** Single timeline source of truth.
- **No cryptographic hashing or fuzzy matching:** Clean sequence/ID sorting.
- **No regex gymnastics:** Direct role-based classification (`role === "assistant"` -> bubble, inter-agent -> envelope).
- **Exact styling retained:** 100% glassmorphism, avatars, icons, and timeline rail preserved.
