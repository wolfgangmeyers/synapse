# Unified Memory System Proposal

**Status:** Proposal
**Date:** 2026-07-11

## Goal

Provide durable, useful memory across Claude and Codex sessions without relying
on a global cron, a specific terminal window, or duplicate knowledge stores.
The system should capture important information as conversations evolve and
surface only the few memories that are useful to the current work.

## Current State

Three existing pieces each solve part of the problem:

| Component | Strength | Gap |
|---|---|---|
| KB | Rich, human-readable, source-attributed project knowledge | Ingestion and retrieval are mostly manual and lexical |
| Codex memory | Compact typed facts and an eager session-start index | It is a separate small store, captured manually; its hard-coded store path does not match this machine |
| Recall | Debounced relevance evaluation and duplicate cooldown | It has no configured stores, so it currently does no recall; it captures terminal panes and chooses from index summaries |

The current split risks duplicate facts and stale entries. Recall is not a
working memory service yet: its configuration has an empty `stores` array.

## Decision

Build one memory service with two views over a single catalog:

- **Agent memory:** concise, high-priority facts that should be loaded at
  session start: user preferences, corrections, standing rules, and external
  references.
- **Knowledge base:** longer, curated project and domain knowledge: decisions,
  investigations, architecture, and reusable operating lessons.

Keep Markdown as the human-editable source of truth. Maintain a derived SQLite
index for retrieval; the index is rebuildable and must never become the only
copy of a memory.

The existing `MEMORY.md` and `INDEX.md` remain compatibility views generated
from the same catalog. Existing skills can become thin interfaces to the new
service rather than independent stores.

## Memory Record

Every record has common metadata in frontmatter or a sidecar record:

```text
id, tier, type, scope, title, summary, tags,
source_ref, source_span, confidence, sensitivity,
created_at, updated_at, last_verified_at, expires_at,
supersedes, content_hash
```

`type` starts with the useful Codex-memory categories: `user`, `feedback`,
`project`, and `reference`. `scope` permits global, user, repository, and
session-local visibility. `source_ref` and `source_span` provide provenance;
`supersedes` prevents contradictory facts from accumulating.

Agent-memory records stay short. KB records may have structured sections,
source links, and supporting detail. Neither tier stores secrets, raw terminal
transcripts, temporary task state, or facts that should be read from current
code or GitHub.

## Capture and Reconciliation

Use a structured extraction pipeline, not direct freeform writes by a model.

1. A conversation checkpoint supplies only the new, unprocessed transcript
   span.
2. An extractor returns JSON candidates conforming to the record schema.
3. A reconciler deduplicates candidates, updates a matching record, creates a
   new record, or rejects the candidate.
4. The writer updates Markdown and the derived index in one durable operation.

Capture triggers:

- immediately after an explicit user correction, preference, or durable
  decision;
- immediately before a manual or automatic compaction, while the complete
  context still exists;
- after a configurable idle threshold if new conversation content remains
  unprocessed;
- after a worker/task completion;
- at session shutdown when the transcript is available.

Extraction before compaction is important. A post-compaction summary may omit
the non-obvious decision or correction that should have become memory. If a
pre-compaction hook is unavailable for an agent surface, process the persisted
transcript using an idempotent cursor rather than scraping a terminal pane.

Automatic writes should be limited to high-confidence user preferences,
corrections, and clearly stated durable decisions. Lower-confidence project
knowledge becomes a reviewable candidate. Reconciliation uses content hashes,
scope/type keys, and supersession links so repeated extraction improves a
record instead of appending duplicates.

## Retrieval and Recall

At session start, load only the compact agent-memory index. Load detailed
records on demand.

For proactive recall:

1. Filter records by scope, expiry, sensitivity, and relevance to the active
   project or user.
2. Use SQLite full-text search and embeddings over record summaries and bodies
   to retrieve a small candidate set.
3. Use a small model to rerank those candidates against the recent structured
   conversation context.
4. Inject at most one to three records, each with a short reason and source.
5. Persist per-session cooldown and acknowledgement state to avoid repetition.

Most recall checks should produce nothing. Precision matters more than showing
that the system is active.

The server-owned job system should own idle extraction, periodic staleness
checks, index rebuilds, and proactive recall. Jobs use closed, repository-owned
handlers and durable run state; they do not execute arbitrary commands or
depend on global cron, tmux, or a legacy supervisor process.

## Storage Layout

```text
~/.mecha-developer/memory/
  records/                    # canonical Markdown records
  knowledge/                  # canonical long-form KB records
  agent-index.md              # generated eager-load view
  knowledge-index.md          # generated KB view
  memory.sqlite               # derived FTS, embedding, provenance, and job state
```

Migration should import the existing KB topics and current Claude/Codex memory
records exactly once, retaining their source paths in provenance metadata.
Existing locations can be compatibility symlinks or generated read-only views
until all agent surfaces use the unified API.

## First Implementation Slice

1. Establish the server-owned scheduled-job foundation already underway for
   Mailbox V2.
2. Add a unified catalog reader and rebuildable SQLite FTS index without moving
   existing files yet.
3. Make the session-start agent index a generated view of existing compact
   facts; fix the incorrect hard-coded Codex memory path as part of this work.
4. Add an idempotent transcript-cursor extraction job in dry-run mode, with
   candidate JSON and no automatic writes.
5. Add reviewed promotion plus a retrieval test harness.
6. Configure proactive recall only after the catalog has real stores and
   precision/false-positive tests.

## Acceptance Criteria

- Claude and Codex read the same canonical agent-memory and knowledge records.
- A durable extraction cursor prevents processing the same conversation span
  twice.
- A compaction or idle checkpoint produces deduplicated, provenance-backed
  candidates.
- A relevant memory can be retrieved from either tier without loading the full
  corpus into every session.
- Recall has configured stores, durable cooldowns, and tests proving both
  relevant delivery and non-delivery for unrelated context.
- The system operates from repository-local/server-owned processes with no
  required global cron or terminal-pane scraping path.
- All memory writes and retrieval decisions are traceable and reversible.
