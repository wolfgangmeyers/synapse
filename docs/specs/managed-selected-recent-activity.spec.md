# Spec: Selected managed activity pagination

- **ID**: `SPEC-CONV-SELECTED-MANAGED-001`
- **Status**: Draft; acceptance incomplete
- **Domain**: Conversation
- **Target E2E Suite**: `tests/e2e/selected-managed-activity-pagination.spec.ts`, `tests/e2e/combined-history-window.spec.ts`, `tests/e2e/combined-history-live-window.spec.ts`

## Intent & User Outcome

A selected managed agent shows activity from both conversation timeline and managed history, including records present in only one source. Older activity can be loaded without losing the reading position.

## Core Invariants

1. Initial requests fetch at most 50 entries per source. Only the selected managed agent's activity is retained; switching scope, agent, or unmounting invalidates pending work.
2. Each source has an independent cursor. Loading older requests available pages from both sources, preserving previously loaded entries and live updates. Failed pages remain retryable.
3. Managed-only entries are not hidden by a timeline timestamp cutoff. Matching live updates win over stale snapshots; duplicates render once.
4. The viewport anchors before each source commits a prepend. Initial activity is not announced as new while either initial request is pending.
5. After both initial requests settle, the combined, deduplicated rendered feed has at most 50 historical items, selected by feed order across both sources. Live activity and live conversation events received during the selection, and pending or failed messages, are not hidden by this limit. Before both requests settle, historical candidates wait while live and pending items remain visible.
6. Historical candidates outside the active bounded window are discarded, not retained as selection-local deferred pages. Loading older refetches discarded candidates from each affected source using a recoverable boundary. Independent source cursors must not skip interleaved records, including when one source contributes no visible items. Failures remain retryable and switching selection discards the window.

## User Scenarios

### Scenario: [SCN-MANAGED-PAGE-001] Browse managed-only history
- **Given** a selected managed agent has more than 50 records, including records absent from the timeline.
- **When** the user loads older history.
- **Then** records from each available source are merged without a hidden timestamp cutoff, and the reading position is preserved.

### Scenario: [SCN-MANAGED-PAGE-002] Retry and switch
- **Given** an older page fails or remains pending.
- **When** the user retries or switches agent or router.
- **Then** a failed page remains retryable, and stale responses cannot alter the new view.

### Scenario: [SCN-MANAGED-PAGE-009] Combined initial window and on-demand recovery
- **Given** both sources contain distinct historical feed items, including overlap, and live and pending items arrive during initial loading.
- **When** initial requests settle, the user loads older, switches away and returns.
- **Then** at most 50 historical items appear initially across both sources, with live and pending items visible and overlap deduplicated. Load older refetches discarded candidates from the affected sources, with no gaps or duplicates. A new selection fetches fresh history rather than restoring discarded candidates.

## Automated E2E Verification Matrix

Existing tests are partial evidence only; they have not been run for this documentation reconciliation. Scenarios not listed here also require acceptance evidence.

| Scenario | Suite | Status |
| --- | --- | --- |
| SCN-MANAGED-PAGE-001 | `tests/e2e/selected-managed-activity-pagination.spec.ts` | Existing targeted test; full acceptance outstanding |
| SCN-MANAGED-PAGE-002 | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | Draft: acceptance test outstanding |
| SCN-MANAGED-PAGE-009 | `tests/e2e/combined-history-window.spec.ts`, `tests/e2e/combined-history-live-window.spec.ts` | Existing targeted tests; full acceptance outstanding |

### Scenario: [SCN-MANAGED-PAGE-003] Bounded live window and hidden pages
- **Given** a selected agent receives sustained live activity, or a page contains only entries hidden from the feed.
- **When** the live window exceeds 150 entries or the user opens the conversation.
- **Then** live retention and live-ID bookkeeping remain bounded; discarded durable entries remain reachable using the managed history cursor. The load-older control remains available even if no entries on the current page are visible.

### Scenario: [SCN-MANAGED-PAGE-004] Timeline recovery and cursor failure
- **Given** timeline messages have been trimmed from the live window, or an older timeline request fails.
- **When** the user loads older history.
- **Then** recovered message events appear in both timeline and conversation messages. A timeline cursor is never sent to the legacy conversation endpoint; the timeline page remains retryable on failure.

### Scenario: [SCN-MANAGED-PAGE-005] Concurrent trim and failed pages
- **Given** an older page is pending while live activity trims its boundary, or a history request fails.
- **When** the response arrives or the user scrolls near the top.
- **Then** the disconnected page is not prepended, the new boundary remains retryable, and a failure is visible with an explicit retry rather than repeated automatic requests.

### Scenario: [SCN-MANAGED-PAGE-006] Queued refresh and deselection
- **Given** live messages arrive during a refresh that queues another refresh.
- **When** the second snapshot omits those messages, or the selected peer is cleared.
- **Then** live messages remain visible across both snapshots and deselection releases loading.

### Scenario: [SCN-MANAGED-PAGE-007] Reconnect and expanded history
- **Given** selected managed activity has live or paginated entries, or a conversation has an expanded window.
- **When** the connection returns and the latest snapshot is reconciled, or an existing activity is updated.
- **Then** missed durable updates appear and live updates received during the request take precedence. Preserve the loaded window and older cursor only when the reconnect page overlaps the pre-disconnection window; otherwise apply the gap recovery in SCN-MANAGED-PAGE-008. A switch away and back always starts a fresh bounded window under SPEC-CONV-006. A failed outgoing message remains visible even if older than the timeline window.

### Scenario: [SCN-MANAGED-PAGE-008] Reconnect gap and live retention
- **Given** a selected conversation or managed activity has a previously loaded window.
- **When** reconnect returns a latest page with no overlap and older durable records exist.
- **Then** the disconnected old window is replaced by the fresh page, and older paging can recover the missing records rather than presenting a false continuous window.
- **When** live activity exceeds 150 entries, including after expansion by older paging.
- **Then** the oldest entries are trimmed to 150, a surviving visible reading anchor keeps its viewport offset, and removed durable records remain available through older paging.
- **Verification**: `tests/e2e/conversation-scroll-regressions.spec.ts` (anchor); focused hook tests (reconnect recovery). Full reconnect browser acceptance remains outstanding.

### Scenario: [SCN-MANAGED-PAGE-010] Failure after outbox eviction
- **Given** an in-flight message has been evicted from the bounded outbox without being confirmed by history.
- **When** its send fails later.
- **Then** a failure notice appears even if the earlier eviction notice was dismissed. A history-confirmed send does not produce a false failure notice.
- **Verification**: focused outbox reducer tests; browser acceptance outstanding.

### Scenario: Reconnect before initial history settles
- **Given** the first managed-history request is pending when live transport connects.
- **When** the reconnect snapshot settles first with an older cursor.
- **Then** older history remains pageable, and the superseded initial response cannot replace it.
- **When** a live update precedes a later snapshot containing the same entry.
- **Then** the live version remains visible rather than being replaced by a stale snapshot.

### Scenario: Hidden candidates and overlapping pagination
- **Given** a bounded combined window contains filtered or deduplicated source records, or an older request is pending while candidates are discarded.
- **When** the window settles or the pending request completes.
- **Then** non-visible durable candidates are released with a recoverable cursor, and a page based on the old boundary cannot skip them.
- **When** an older request fails or is invalidated before committing.
- **Then** the bounded window stays bounded and the user can retry. A snapshot captured before live trimming cannot restore a stale version of a trimmed live record.
- **Verification**: focused component and hook regression tests; browser acceptance outstanding.

### Scenario: [SCN-MANAGED-PAGE-011] History created during live subscription
- **Given** a managed conversation is selected while its live connection opens or reconnects.
- **When** the router persists activity during subscription after an earlier history request has completed, without delivering that activity as a live entry to this connection.
- **Then** the selected conversation refreshes after the subscription snapshot and shows the durable activity once. A failed connection continues to recover normally, and stale requests cannot overwrite the refreshed view.
- **Verification**: focused transport regression; deterministic browser acceptance outstanding.
