# Scratchpad Session Memory & Context Reset Requirements

## Overview

Traditional LLM context compaction is lossy and slow. When a conversation nears the token limit, compaction algorithms summarize past turns. Summarization drops subtle constraints, loses code snippets, burns extra tokens, and causes agents to drift during long workflows.

We are replacing lossy compaction entirely. Instead of automated summarization, the system uses two primitives:
1. **Worker-controlled scratchpad session memory:** Persistent structured notes in `router.sqlite`. The worker decides what to write, what to search, and what to pin.
2. **Clean context reset:** A hard reset that clears conversation history down to 0 tokens while keeping system prompt, skills, and tools intact. Pinned entries and recent scratchpad notes are injected into the genesis turn as a structured handoff note.

---

## Resolved Human Decisions

| Area | Decision |
| --- | --- |
| **Compaction strategy** | No lossy compaction. Eliminate automated turn summarization. Replace with worker-curated scratchpad memory and clean context reset. |
| **Storage layer** | Persistent in the router database (`router.sqlite`), scoped by `agent_id` (and session). Survives agent restarts and crashes. |
| **Data model** | Flat entry ledger with `id`, `agent_id`, `content`, `pinned`, and `created_at`. No nested hierarchies. |
| **Pinned entries** | Pinned entries represent permanent working constraints, goals, or architectural facts. They are never dropped during fetch or handoff. |
| **Tool interface** | Single `scratchpad` tool with five core actions: `append`, `fetch`, `paginate`, `search`, and `pin`/`unpin`. |
| **Context reset behavior** | Clears conversation context to 0 tokens, injects system prompt, skills, and tools, ports over the last few messages (last 4–6 messages / recent turns) so the agent preserves recent actions and scratchpad updates, and automatically wakes the agent with a post-reset prompt: `"Context reset completed. You are now operating with fresh context headroom. Your recent conversation window has been preserved. Proceed with your next step."` |
| **Genesis handoff** | Injects all pinned entries plus a bounded window of recent entries (5–10 items) directly into the first turn after reset, alongside the ported recent messages. |
| **Threshold monitoring** | Context threshold: **50% of context window for 1M+ models**, **80% for <1M models**. Auto-reset triggers when token threshold is hit, OR when worker invokes `context_reset` tool. |

---

## Requirements

### 1. Storage & Entry Schema

- **S1. SQLite persistence:** Scratchpad entries are stored in `router.sqlite` in an append-friendly table (e.g. `scratchpad_entries`).
- **S2. Agent scoping:** Entries are strictly scoped by `agent_id` (and session identifier). Agents cannot access or overwrite another worker's scratchpad.
- **S3. Entry structure:** Each entry contains:
  - `id`: Unique entry identifier (UUID or stable text ID).
  - `agent_id`: String identifying the owning agent.
  - `content`: Text body containing notes, decisions, plans, or code references.
  - `pinned`: Boolean flag (`true` / `false` or SQLite `1`/`0`). Pinned entries represent high-priority facts or constraints.
  - `created_at`: ISO 8601 UTC timestamp.
- **S4. Durability & cascade:** Entries survive agent process restarts. Deleting a managed agent cascades and removes associated scratchpad rows.

### 2. Tool Surface (`scratchpad` Tool)

The router exposes a dedicated `scratchpad` tool with the following actions:

- **T1. `append`:**
  - Parameters: `content` (string, required), `pinned` (boolean, optional, default `false`).
  - Creates and stores a new entry.
  - Returns the created entry ID, timestamp, and status.
- **T2. `fetch`:**
  - Retrieves active working context.
  - Returns all `pinned` entries for the agent, plus the most recent unpinned entries up to a bounded limit (default 5–10 items).
  - Pinned entries are highlighted or grouped at the top of the output.
- **T3. `paginate`:**
  - Parameters: `limit` (integer, default 10, max 50), `offset` (integer, default 0).
  - Returns historical entries ordered chronologically or reverse-chronologically with pagination metadata (`total_count`, `has_more`).
- **T4. `search`:**
  - Parameters: `query` (string, required), `regex` (boolean, optional, default `false`).
  - Searches entry content using substring matching or regex search.
  - Returns matching entries sorted with the most recent matches at the top.
- **T5. `pin` and `unpin`:**
  - Parameters: `id` (string, required), `pinned` (boolean, required).
  - Updates the pinned status of an existing entry.
  - Fails cleanly if the entry ID does not exist or belongs to another agent.

### 3. Context Reset & Genesis Handoff

- **R1. Clean 0-token reset & preservation:**
  - When a context reset occurs, the prior conversation messages and tool call results are wiped to 0 tokens.
  - Base system instructions, loaded skills, and tool definitions are preserved and reinjected.
  - Ports the **last few messages** (last 4–6 messages / recent turns) over to the new session context so the agent sees its recent actions and scratchpad updates.
- **R2. Genesis handoff generation:**
  - Before starting the fresh session turn, the runtime queries the scratchpad store for the agent.
  - It gathers:
    1. All entries where `pinned = true`.
    2. The 5–10 most recent entries.
  - It formats these entries into a structured session memory handoff note.
- **R3. Genesis turn injection & auto-wake:**
  - The handoff note, genesis prompt, and ported recent messages are injected into the fresh context.
  - The runtime automatically wakes the agent with a post-reset prompt: `"Context reset completed. You are now operating with fresh context headroom. Your recent conversation window has been preserved. Proceed with your next step."`
  - The agent begins its new turn with full awareness of its pinned goals, decisions, immediate next steps, and recent context, with fresh headroom.

### 4. Threshold Monitoring & Auto-Reset

- **M1. Context tracking:**
  - The runtime (`pi-go` / `router-proxy`) tracks token usage after each assistant turn.
- **M2. Threshold evaluation:**
  - Hard threshold: **50% of context window for 1M+ models**, **80% for <1M models**.
  - Evaluated via `contextThresholdExceeded(model Model, currentTokens int) bool`.
- **M3. Auto-reset and explicit tool invocation:**
  - Auto-reset triggers automatically when the token threshold is exceeded after any turn.
  - Workers can also trigger reset explicitly at any time by calling the `context_reset` tool.
  - When triggered, `ExecuteContextReset(ctx, binding, reason)` executes the reset lifecycle.

---

## Phased Implementation Plan

### Phase 1: Storage Layer & Scratchpad Tool
- Write database migration in `router-proxy/internal/store/migrations/` creating `scratchpad_entries`.
- Implement store interface methods in `router-proxy/internal/store/`:
  - `AppendScratchpadEntry(ctx, agentID, content, pinned)`
  - `GetScratchpadHandoff(ctx, agentID, recentLimit)` (fetches all pinned + N recent)
  - `ListScratchpadEntries(ctx, agentID, limit, offset)`
  - `SearchScratchpadEntries(ctx, agentID, query, isRegex)`
  - `SetScratchpadPinned(ctx, agentID, entryID, pinned)`
- Register the `scratchpad` tool in `pi-go` / `router-proxy` tool manifest with `append`, `fetch`, `paginate`, `search`, and `pin`/`unpin`.
- Unit and store tests covering entry creation, retrieval, search ranking, and scoping.

### Phase 2: Context Reset & Genesis Handoff Injection
- Implement clean context reset mechanism in `pi-go` / `router-proxy` (`ExecuteContextReset`).
- Ensure conversation history is cleared down to 0 tokens while preserving system prompt, skills, and tools.
- Port the last 4–6 messages / recent turns over to the new session epoch so recent actions and scratchpad updates are preserved.
- Implement handoff note assembler that pulls pinned entries + 5–10 recent entries.
- Wire genesis turn injection so the handoff note and ported recent messages appear in the reset session.
- Automatically wake the agent with: `"Context reset completed. You are now operating with fresh context headroom. Your recent conversation window has been preserved. Proceed with your next step."`
- Integration tests verifying context token reset, message porting, and auto-wake prompt.

### Phase 3: Threshold Monitoring & Auto-Reset
- Add context usage tracking at turn boundaries (`contextThresholdExceeded`).
- Implement hard threshold evaluation: **50% of context window for 1M+ models**, **80% for <1M models**.
- Auto-trigger `ExecuteContextReset` when threshold is hit.
- Expose `context_reset` tool for workers to invoke explicitly.
- End-to-end integration tests verifying auto-reset at 50% and 80%, message porting, auto-wake execution, and explicit `context_reset` tool invocation.
