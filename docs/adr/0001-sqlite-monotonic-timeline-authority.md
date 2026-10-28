# ADR 0001: SQLite Monotonic Sequence Timeline Authority

- **Status**: Accepted
- **Deciders**: Wolfgang
- **Date**: 2025-10-28

---

## Context & Problem Statement

The conversation and execution feeds in `mecha-developer` historically suffered from severe fragmentation, presentation regressions, and UI instability:

1. **Multiple Competing Endpoints & Storage**: The system queried three disparate sources: `/v1/conversation?agent_id=...` (modern sequence events), `/v1/history?view=complete` (raw JSON execution logs), and `/v1/conversations` (tmux peer chat).
2. **Accidental Frontend Re-Classification**: The web UI (`web/router-ui-react`) maintained hundreds of lines of fragile regular expressions (`isMailboxSendToUser`, `extractSubjectFromText`, `subagentCompleteVisual`) attempting to reconstruct user vs assistant vs tool turns from unstructured textual logs.
3. **Flashing & Reversion**: On load, messages initially rendered as speech bubbles, but secondary fetches from `/v1/history` (100–300ms later) overwrote feed state or failed fuzzy 120-second timestamp comparisons, converting assistant speech bubbles into envelope activity cards.
4. **Scroll Jumps & Pagination Instabilities**: Unstable synthetic IDs (`activity-msg-*`, `hist:*`, `msg:*`) mutated across renders, breaking React key identity and throwing scroll positions unpredictably when loading older history.

We need a unified, authoritative, monotonic data stream for all conversation and execution feeds.

---

## Decision

Establish the `conversation_timeline` table in SQLite (`router-proxy/internal/store/migrations/112_conversation_timeline.sql`) as the single source of truth for all conversation and agent execution feeds.

1. **Single Database Table**: All events (user messages, assistant turns, tool executions, agent thoughts, system events, errors, lifecycle transitions) are appended to `conversation_timeline`.
2. **Strict Per-Agent Monotonic Sequence (`seq`)**: Every event for an `agent_id` is assigned a strictly increasing integer sequence number `seq` (`1, 2, 3, ...`), indexed by `UNIQUE (agent_id, seq)`.
3. **Single HTTP/WS Query Surface**:
   - HTTP: `GET /v1/conversation?agent_id=<id>&limit=<n>&before_seq=<seq>&after_seq=<seq>`
   - WebSocket: Direct streaming of `timeline_event` frames multiplexed over `/v1/agents`.
4. **Deprecate Legacy Aggregations**:
   - Raw logs from `/v1/history?view=complete` and legacy peer chat tables (`conversations`, `messagesByPeer`) are deprecated for conversation views.
   - Frontend components must bind strictly to `seq`-based feed items. No 120-second timestamp fuzzy matching or synthetic ID fabrication.

---

## Invariants & Guarantees

- **Monotonic Ordering**: `seq` is strictly increasing per `agent_id`. It never rewinds, gaps during steady-state writes, or reorders across transactions.
- **Stable React Key**: Every feed item has a permanent, deterministic key: `${agent_id}:${seq}` or server-issued UUID `id`. Synthetic IDs (`hist:*`, `activity-msg-*`) are strictly forbidden.
- **Typed Schema Invariant**: Presentation classification (role, kind, visual treatment) is resolved by typed columns (`kind`, `role`, `metadata`) stored at write time, never by client-side regex heuristics on raw text.
- **Bounded Pagination**: Forward and backward pagination rely solely on `before_seq` and `after_seq`. `LIMIT` clauses prevent unbounded memory consumption.
- **Attachment Integrity**: Image and file attachments are stored with canonical relative URLs (`/v1/attachments/<id>`) in `metadata.attachments`.

---

## Consequences

### Positive
- Permanently eliminates the flash-and-revert bug where speech bubbles turned into envelope cards.
- Stable keys eliminate erratic scroll jumping when hydrating or prepending older items.
- Eliminates over 2,000 lines of fragile heuristic classification regex and duplicate Redux/store caching in the frontend.
- Standardizes both REST and WebSocket feeds on a single typed payload.

### Negative
- Existing historical data created prior to migration 112 requires backfill into `conversation_timeline`.
- Legacy tmux peer chat components must be upgraded or routed through the timeline adapter.

---

## References

- Migration: `router-proxy/internal/store/migrations/112_conversation_timeline.sql`
- Store Implementation: `router-proxy/internal/store/conversation_timeline.go`
- Architecture Blueprint: `docs/master-conversation-architecture-simplification.md`
- Behavioral Spec: `docs/specs/conv-feed-rendering.spec.md`
