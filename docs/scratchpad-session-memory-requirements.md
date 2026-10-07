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
| **Context reset behavior** | Clears conversation context to 0 tokens. System prompt, loaded skills, and tool definitions are preserved. |
| **Genesis handoff** | Injects all pinned entries plus a bounded window of recent entries (5–10 items) directly into the first turn after reset. |
| **Threshold monitoring** | At 80% context window utilization, the router fires a threshold reminder prompting the worker to update its scratchpad and trigger a context reset. |

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

- **R1. Clean 0-token reset:**
  - When a context reset occurs, all prior conversation messages and tool call results are wiped from the active context.
  - Token count resets to 0 conversation tokens.
  - Base system instructions, loaded skills, and tool definitions are preserved without modification.
- **R2. Genesis handoff generation:**
  - Before starting the fresh session turn, the runtime queries the scratchpad store for the agent.
  - It gathers:
    1. All entries where `pinned = true`.
    2. The 5–10 most recent entries.
  - It formats these entries into a structured session memory handoff note.
- **R3. Genesis turn injection:**
  - The handoff note is injected as the initial developer/system message in the fresh context.
  - The agent begins its new turn with full awareness of its pinned goals, decisions, and immediate next steps, with zero token baggage from completed work.

### 4. Threshold Monitoring & Reminders

- **M1. Context tracking:**
  - The runtime (`pi-go` / `router-proxy`) tracks token usage after each assistant turn.
- **M2. 80% threshold reminder:**
  - When context reaches or exceeds 80% of the model's context window, the system issues a reminder.
  - The reminder prompts the worker:
    1. Review current working state.
    2. Append key findings, decisions, and next actions to `scratchpad`.
    3. Pin critical requirements or constraints.
    4. Trigger context reset to start a fresh, clean window.

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
- Implement clean context reset mechanism in `pi-go` / `router-proxy`.
- Ensure conversation history is completely cleared while preserving system prompt, skills, and tools.
- Implement handoff note assembler that pulls pinned entries + 5–10 recent entries.
- Wire genesis turn injection so the handoff note is delivered as the first turn context of the reset session.
- Integration tests verifying context token count drops to 0 and scratchpad handoff appears in the new turn.

### Phase 3: Threshold Reminders
- Add context usage tracking at turn boundaries.
- Implement 80% context threshold detection.
- Deliver proactive prompt/reminder nudging the agent to update scratchpad and trigger reset.
- End-to-end integration tests verifying threshold warning, worker update, reset, and clean resumption.
