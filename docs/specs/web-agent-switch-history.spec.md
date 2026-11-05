# Spec: Web Agent Switch History

- **ID**: `SPEC-CONV-006`
- **Status**: `Draft` (implementation and acceptance pending)
- **Domain**: `Conversation`
- **Target E2E Suite**: `tests/e2e/web-agent-switch-history.spec.ts` and related suites (partial coverage; acceptance pending)

## 1. Intent & User Outcome

When a user selects an agent in the web app, its conversation starts from a freshly fetched, bounded recent history window, not a previously cached timeline. Live activity and pending messages remain visible, and the user can still page back into older history. Android and unrelated behavior are out of scope.

## 2. Core Invariants

1. On every agent switch, including returning to an earlier agent, completed per-agent history is not retained in the web client for later restoration. Known cached historical entries must never render, even temporarily while fresh history loads, after a failure, or during pagination. Unversioned stale redelivery after an empty or failed load remains an unresolved ambiguity (see the known limitation below). Entries evicted from the initial window or previously loaded by pagination must be fetched again, not resurrected from a cache.
2. The initial historical window comes from fresh requests for the selected agent and is bounded by 50 historical feed items after deduplication across sources; each source requests at most 50. For a single source the same 50-item bound applies. Explicit older-history pagination may expand the visible window, consistent with `SPEC-CONV-003`.
3. Active live activity and pending outgoing messages for the selected agent survive a switch or history refresh. Completed activity while an agent is inactive is not retained as per-agent history: it is recovered from the fresh latest page on return, and activity outside that page requires pagination. Live events received during the fresh request are reconciled with its snapshot by identity and ordering: no event is lost or duplicated whether the snapshot includes it or not, and subsequent updates continue normally.
4. Transient outgoing items retained across switches are limited to pending and failed outgoing items, not completed history. Retain at most 150 items for at most 24 hours each; remove them when confirmed or expired. Failed items retain their submitted text and attachments for recovery until expiry or eviction. Expiry or capacity exhaustion must not silently claim delivery: expose the existing failure indication or another visible non-delivery indication. Confirmed items thereafter come from fresh history. These bounds apply to the web client's transient state, not router storage.
5. Responses, failures, pagination results, and live events for another agent or an obsolete selection must not overwrite, append to, or change the loading and pagination state of the selected conversation.
6. Fresh history retains correct ordering and pagination cursors. Loading older history preserves the reading position and existing newer items; empty, duplicate-only, and failed pagination releases loading state as specified in `SPEC-CONV-003`.

## 3. User Scenarios

### Scenario: [SCN-SWITCH-001] Return to an agent with cached history
- **User Goal**: See only the current recent history when returning to an agent.
- **Given**: Agent A has more than 50 historical items; the user opened A, paged into older history, and switched to B. A's earlier timeline was previously displayed in the browser.
- **When**: The user selects A again while the fresh history response is delayed, and then the response arrives.
- **Then**: At no point does the old cached history render. The completed initial window contains only the fresh response's most recent at most 50 historical items, in order, and the view settles at the bottom. Older items remain available through pagination.

### Scenario: [SCN-SWITCH-002] Live and pending activity survives a switch
- **User Goal**: Keep track of work in progress while navigating between agents.
- **Given**: Agent A has a pending outgoing message and an active streaming response; the user switches to B.
- **When**: A receives further activity while inactive, and the user returns to A; live updates arrive before and after the fresh snapshot is produced and before its response is rendered.
- **Then**: A's pending message and still-active live activity remain visible, including updates during loading; no B activity appears. Snapshot/live overlap appears once, updates absent from the snapshot are not lost, and subsequent streaming updates continue normally. Completed inactive activity is recovered from the fresh latest page, not retained locally; if it falls outside that page, the user can reach it by loading older history.

### Scenario: [SCN-SWITCH-003] Page backwards from the fresh window
- **User Goal**: Read older messages without losing the recent conversation.
- **Given**: The fresh initial window for A has older history available.
- **When**: The user loads older history, including more than one page.
- **Then**: Each preceding page is freshly fetched using the current history boundary, never restored from cached history, and prepended in order without duplicates or gaps; newer and pending/live items remain, and the reading position is preserved. Availability and loading controls reflect the response, including the end of history.

### Scenario: [SCN-SWITCH-004] Rapid switches and stale responses
- **User Goal**: Never see another agent's history or a superseded request.
- **Given**: The user selects A, then B, then A again before the earlier requests complete.
- **When**: The earlier A and B history or pagination responses complete after the latest A selection, in any order.
- **Then**: Neither stale response changes the current A timeline, its loading indicator, or its pagination state. Only the latest A selection's fresh result and A's current live/pending activity are shown.

### Scenario: [SCN-SWITCH-005] Empty or failed fresh history
- **User Goal**: Avoid mistaking stale content for current history when loading fails.
- **Given**: A has previously displayed history and live or pending activity.
- **When**: The user switches to A and its fresh request fails or returns an empty result.
- **Then**: Cached historical entries never render; A's live and pending activity remains. A failure does not replace current live or pending activity with old history; an empty successful result shows an appropriate empty state when no activity is present. Any subsequent fresh load displays only its fresh bounded result. A failed initial timeline or managed activity request exposes a retry button. It retries each failed source (including both when both fail) without hiding live or pending activity; a failed older managed activity page retries that page.

### Scenario: [SCN-SWITCH-006] Pagination failure and agent switch
- **User Goal**: Continue using either conversation after an interrupted older-history request.
- **Given**: A's fresh window is visible and an older-history request is in flight.
- **When**: The request fails, returns empty or duplicate-only results, or completes after the user switches to B.
- **Then**: A's existing items are not lost and no cached page is substituted; failed or no-progress loading releases its loading state and permits normal following or retry where applicable. A's late result does not affect B, and returning to A starts another fresh bounded window rather than restoring the old paginated view.

### Scenario: [SCN-SWITCH-007] Bound transient pending outbox
- **User Goal**: Keep pending sends visible without accumulating unbounded client history or implying delivery.
- **Given**: Pending outgoing items are retained across agent switches and the deterministic test clock and mock transport can hold them pending.
- **When**: Items are confirmed, terminally fail, reach 24 hours of age, or a new pending item would exceed the 150-item bound.
- **Then**: Confirmed and expired items leave transient retention; terminally failed items remain visibly failed with their submitted content until expiry or eviction; at no point are more than 150 retained. Expiry and capacity exhaustion produce a visible non-delivery indication rather than a false delivered state. Confirmed history is recovered by a fresh page or pagination, not the transient outbox.

### Scenario: [SCN-SWITCH-015] Switch between sources with the same agent ID
- **Given**: A tmux agent and a router-managed agent share an ID, and the tmux conversation has loaded or has requests in flight.
- **When**: The user selects the router-managed agent (or switches back).
- **Then**: The selection starts a fresh conversation request using the raw agent ID. The previous selection's history, pagination state, and late responses cannot appear or change the new selection, even while its request is pending.
- **E2E**: `tests/e2e/source-qualified-conversation.spec.ts`: `same-ID source switch refetches and fences the previous selection`.

## 4. Automated E2E Verification Matrix

These existing tests provide partial coverage, not acceptance. They have not been verified by this documentation reconciliation. Full real-boundary E2E coverage remains required, including delayed responses and observation throughout loading.

| Scenario | Existing test evidence / remaining gap |
| :--- | :--- |
| `SCN-SWITCH-001`–`003` | `tests/e2e/web-agent-switch-history.spec.ts`: `drops loaded history on switch, reconciles a delayed page and keeps live updates`; partial, not all live/pending and multi-page cases. |
| `SCN-SWITCH-004` | `tests/e2e/web-history-isolation-edge.spec.ts`: `rapid A-B-A ignores both delayed snapshots while preserving current live activity`; stale pagination still needs coverage. |
| `SCN-SWITCH-005` | Same suite: `stalled initial timeline retries and keeps live content on an empty response` and `failure` / `empty fresh history never restores cached history and keeps live events`; unversioned redelivery ambiguity remains. |
| `SCN-SWITCH-006`–`007` | Complete switch-interrupted pagination and outbox bounds/expiry browser acceptance outstanding. |
| `SCN-SWITCH-008` | `tests/e2e/web-pending-send-boundary.spec.ts`: `accepted` / `failure response after switching preserves correct send state without stale history`; full ordering and attachment cases remain outstanding. |
| `SCN-SWITCH-009`–`014` | Targeted full-boundary browser acceptance outstanding; focused tests do not establish complete acceptance. |

### Scenario: [SCN-SWITCH-008] Send outcomes during navigation
- **User Goal**: Know whether a send was accepted without losing track of failures.
- **Given**: A text or image send is in flight while the user switches agents.
- **When**: It fails or the router accepts it, before or after a fresh history response.
- **Then**: A failure shows a visible non-delivery notice, including when the user switched away. An accepted send remains visible in the originating view until canonical history replaces it, without duplicates for prefixed history IDs. If the user has left that view, completed messages are recovered from fresh history rather than retained for later restoration. A notice does not claim that an uncertain send was delivered.

### Scenario: [SCN-SWITCH-009] Bounded attachment memory and late responses
- **User Goal**: Avoid losing track of sends or exhausting browser memory when image sends stall.
- **Given**: Image sends are retained or in flight, or a send started in an earlier visit to the same agent.
- **When**: The total retained and in-flight data URL storage would exceed 16 MiB (measured as two bytes per URL character), or the user switches away and back before the response, or canonical history/live activity confirms an accepted send.
- **Then**: A send exceeding the budget is not dispatched; its text is retained as failed without its image data and a notice explains that the images were not sent and need to be reattached. In-flight image data counts until its request settles even if its outbox item is evicted. An old visit's response cannot add a completed row to the new visit. Once confirmed by canonical history or live activity, an accepted bridge row is discarded and cannot reappear after refresh.

### Scenario: [SCN-SWITCH-010] Recover a trimmed boundary in a long history
- **Given**: Live activity has trimmed the visible window, and the retained oldest event is persisted more than three pages behind the latest event.
- **When**: The user loads older history.
- **Then**: One scoped `before_event_id` request returns the immediately preceding page, excluding the boundary. The existing window remains intact, with no gap or duplicate, and subsequent pages use the returned sequence cursor. A missing or not-yet-persisted boundary returns HTTP 409 `timeline_boundary_not_found`; the window and retry availability remain unchanged. The endpoint requires `agent_id` and rejects `before_event_id` combined with `before_seq` as HTTP 400. An event belonging to another agent is treated as missing.
- **E2E**: Long-history trimmed boundary recovery: targeted E2E acceptance outstanding.

### Scenario: [SCN-SWITCH-011] Reconnect gaps and stalled sends
- A reconnect page only joins retained history when it overlaps history retained before disconnection, not a live event received afterward. Without overlap and with older pages available, show the fresh page and paginate from its cursor. Apply the same rule to legacy conversation pages.
- At most 150 sends may be in flight. Additional sends are not dispatched and produce a visible notice. A send that has not settled within 30 seconds is released and marked uncertain; the user is told to check history before retrying. A timeout does not establish whether the router accepted the message. Late responses cannot claim delivery.
- E2E verification of these scenarios remains required.

### Scenario: [SCN-SWITCH-014] Reconnect while history is stalled
- **Given**: A conversation or managed activity history request has not completed.
- **When**: The live connection reconnects.
- **Then**: A fresh history request starts without waiting for the stalled request. A late stalled response cannot replace the fresh result or release its loading state. Live activity remains visible.
- **E2E**: Reconnect with stalled history: targeted E2E acceptance outstanding.

### Scenario: [SCN-SWITCH-IMAGE-CACHE] Accepted image thumbnail recovery
- **Given**: An image send is accepted and its thumbnail is cached locally.
- **When**: History appears before thumbnail caching finishes, or the user switches away and returns.
- **Then**: The accepted image resolves from the same canonical cache key when available; a cache miss or failure does not change send status.
- **E2E**: `tests/e2e/web-pending-send-boundary.spec.ts` — accepted image remains available from the thumbnail cache after returning to history.

### Scenario: [SCN-SWITCH-012] Thumbnail processing after acceptance
- **Given**: The router accepts an image send, but local thumbnail processing stalls or fails.
- **When**: The send deadline passes or processing finishes late.
- **Then**: The accepted send remains accepted; thumbnail caching is best effort and cannot mark it uncertain or undo acceptance.
- **E2E**: Send processing deadline: targeted E2E acceptance outstanding.

### Scenario: [SCN-SWITCH-013] Fresh history bypasses caches
- **Given**: A browser requests managed history, conversation history, or timeline history.
- **When**: The request succeeds or fails.
- **Then**: The browser requests no-store and the router responds with Cache-Control: no-store, preventing stale cached pages from masquerading as fresh history.
- **E2E**: History cache headers on success and failure: targeted E2E acceptance outstanding.

### Scenario: Managed reconnect and concurrent overflow
- **Given** managed history was visible before the live connection dropped, **when** a reconnect page overlaps only activity received after the drop, **then** the old disconnected window is not joined across the gap; older history remains reachable from the fresh page.
- **Given** a latest managed page arrives while live activity fills the retained window, **when** their combined size exceeds 150, **then** the retained edge is recoverable by pagination or the user is explicitly asked to retry; the gap is never silently hidden.
- **Known limitation:** After an empty or failed history load, unversioned stale redelivery can look like fresh activity and appear in the view. ID history cannot distinguish it from a legitimate update. Apply such updates rather than discard them; avoiding the ambiguity requires a reliable source version or equivalent protocol support. The empty/failure scenario above is not fully satisfied in this case.
