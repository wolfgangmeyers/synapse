# Plan: Proximity Auto-Scrolling for SubagentViewerDialog

## 1. Overview & Requirements

Currently, `SubagentViewerDialog.tsx` unconditionally auto-scrolls to the bottom on every history change via:
```tsx
useLayoutEffect(() => {
  if (!selectedSubagentId) return;
  const scrollToBottom = () => {
    if (conversationContainerRef.current) {
      conversationContainerRef.current.scrollTop = conversationContainerRef.current.scrollHeight;
    }
    if (messagesEndRef.current && typeof messagesEndRef.current.scrollIntoView === "function") {
      messagesEndRef.current.scrollIntoView({ behavior: "auto" });
    }
  };

  scrollToBottom();
  const frame = requestAnimationFrame(scrollToBottom);
  return () => cancelAnimationFrame(frame);
}, [selectedSubagentId, historyEntries.length]);
```

Because subagent history is polled every 2000ms while active, any user attempting to inspect backlogged tools, thinking traces, or previous assistant outputs gets violently snapped back down to the bottom every 2 seconds.

### Requirements:
1. **Identical Proximity Behavior**: Implement the exact same proximity auto-scroll semantics used in `MessageList.tsx` for `SubagentViewerDialog.tsx`.
2. **Threshold**: Reuse `DEFAULT_AUTO_SCROLL_THRESHOLD = 180` (1-2 messages height) and `shouldFollowToBottom` from `../state/conversation-projection`.
3. **Follow-to-Bottom on Incoming History**: Auto-scroll to the bottom when near the bottom (`scrollHeight - (scrollTop + clientHeight) <= 180px`) when new history entries or message polls arrive.
4. **Scroll Lock / Freeze Backlog Inspection**: When scrolled up (`distance > 180px`), freeze scroll position so the user can inspect previous tool calls, code diffs, or thinking traces without disruption.
5. **Resume Auto-Scroll**: When the user scrolls back down within 180px of the bottom, resume auto-scrolling on subsequent history updates.
6. **Force Reset to Bottom**:
   - Force reset and scroll to bottom upon initially selecting/drilling into a subagent.
   - Force reset and scroll to bottom when switching between different subagents.
7. **Clean Lifecycle Cleanup**: Reset proximity state when closing the dialog or navigating back to the subagent card list.

---

## 2. Architecture & Design

### 2.1 State & Refs in `SubagentViewerDialog.tsx`
- Import `DEFAULT_AUTO_SCROLL_THRESHOLD` and `shouldFollowToBottom` from `../state/conversation-projection`.
- Add refs in `SubagentViewerDialog`:
  ```tsx
  const isNearBottomRef = useRef(true);
  const prevSelectedSubagentIdRef = useRef<string | null>(null);
  ```

### 2.2 Scroll Handler on Conversation Viewport
Attach `onScroll` to the conversation view container:
```tsx
const handleScroll = (): void => {
  const element = conversationContainerRef.current;
  if (!element) return;
  const { scrollTop, scrollHeight, clientHeight } = element;
  isNearBottomRef.current = shouldFollowToBottom(
    { scrollTop, scrollHeight, clientHeight },
    DEFAULT_AUTO_SCROLL_THRESHOLD
  );
};
```
In JSX:
```tsx
<div
  ref={conversationContainerRef}
  className="subagent-conversation-view"
  style={{ overflowY: "auto" }}
  onScroll={handleScroll}
>
```

### 2.3 Conditional Auto-Scrolling in `useLayoutEffect`
Replace the unconditional auto-scroll hook with proximity-aware logic:
```tsx
useLayoutEffect(() => {
  if (!selectedSubagentId) {
    prevSelectedSubagentIdRef.current = null;
    return;
  }

  const isSubagentSwitch = prevSelectedSubagentIdRef.current !== selectedSubagentId;
  prevSelectedSubagentIdRef.current = selectedSubagentId;

  const element = conversationContainerRef.current;
  if (!element) return;

  const scrollToBottom = () => {
    if (conversationContainerRef.current) {
      conversationContainerRef.current.scrollTop = conversationContainerRef.current.scrollHeight;
    }
    if (messagesEndRef.current && typeof messagesEndRef.current.scrollIntoView === "function") {
      messagesEndRef.current.scrollIntoView({ behavior: "auto" });
    }
  };

  // If switching subagents, always reset proximity to true and scroll to bottom
  if (isSubagentSwitch) {
    isNearBottomRef.current = true;
    scrollToBottom();
    const frame = requestAnimationFrame(scrollToBottom);
    return () => cancelAnimationFrame(frame);
  }

  // If user is scrolled up past 180px threshold, lock viewport and skip auto-scroll
  if (!isNearBottomRef.current) {
    return;
  }

  // Follow to bottom when near bottom
  scrollToBottom();
  const frame = requestAnimationFrame(scrollToBottom);
  return () => cancelAnimationFrame(frame);
}, [selectedSubagentId, historyEntries.length]);
```

### 2.4 Cleanup on Dialog Close or Exit
Update the dialog reset effect:
```tsx
useEffect(() => {
  if (!open) {
    setSelectedSubagentId(null);
    prevSelectedSubagentIdRef.current = null;
    isNearBottomRef.current = true;
    setHistoryEntries([]);
    setHistoryError(null);
    setLoading(false);
  }
}, [open]);
```
And reset `isNearBottomRef.current = true` when back button is pressed (`setSelectedSubagentId(null)`).

---

## 3. Behavioral Specification Updates

File: `docs/specs/conv-scroll-and-pagination.spec.md` (`SPEC-CONV-003`)

Add explicit scenario for Subagent Drilldown View:

### Scenario: `[SCN-SCROLL-006]` Subagent drill-down proximity auto-scrolling
- **Given**: Wolfgang is viewing a running subagent in `SubagentViewerDialog`.
- **When**: Wolfgang is near the bottom (`<= 180px`), new subagent history entries stream or poll in.
- **Then**: Viewport auto-scrolls to the bottom to display newest tool outputs and responses.
- **When**: Wolfgang scrolls upward (`> 180px`) to read previous tool logs or agent reasoning.
- **And**: Polling fetches new history entries or tool updates.
- **Then**: The scroll position remains frozen; the viewport does not jump or snap to the bottom.
- **When**: Wolfgang scrolls back within 180px of the bottom and new history entries arrive.
- **Then**: Auto-scrolling resumes.
- **When**: Wolfgang switches to another subagent or re-opens the drill-down view.
- **Then**: Viewport resets and scrolls directly to the bottom.

Update Scenario Mapping table in `SPEC-CONV-003` to include `SCN-SCROLL-006`.

---

## 4. Testing Plan

### Component Tests: `web/router-ui-react/tests/component/subagent-viewer-dialog.test.tsx`
Add a dedicated test block `Proximity Auto-Scrolling`:
1. **Initial Drilldown / Switch**:
   - Render dialog with subagent selected.
   - Verify `scrollTop` is set to `scrollHeight` upon opening / selecting subagent.
2. **Auto-scroll Near Bottom**:
   - Set mock container metrics with `scrollTop` near bottom (`scrollHeight - (scrollTop + clientHeight) <= 180`).
   - Trigger incoming history update.
   - Verify `scrollTop` updates to new `scrollHeight`.
3. **Scroll Freeze When Scrolled Up**:
   - Simulate scroll event with `scrollTop` far from bottom (`distance > 180px`).
   - Verify `isNearBottom` becomes false.
   - Trigger incoming history entries.
   - Verify `scrollTop` remains locked at previous position (does NOT jump to `scrollHeight`).
4. **Auto-scroll Resumption**:
   - Simulate user scrolling back down to within 180px of bottom.
   - Trigger subsequent history entries update.
   - Verify auto-scroll engages and sets `scrollTop` to bottom.

---

## 5. Verification Commands
1. Run component test suite:
   ```bash
   npm --prefix web/router-ui-react test tests/component/subagent-viewer-dialog.test.tsx
   ```
2. Run full UI test suite:
   ```bash
   npm --prefix web/router-ui-react test
   ```
3. Typecheck and build production bundle:
   ```bash
   npm --prefix web/router-ui-react run build
   ```
