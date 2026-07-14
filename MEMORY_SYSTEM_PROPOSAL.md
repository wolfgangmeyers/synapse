# Unified Memory System Proposal

**Status:** Proposed
**Date:** 2026-07-13

## Goal

Keep one durable, human-readable memory system that captures useful knowledge in
one place and recalls only what directly matters to current work. It works
across agent sessions without eager memory loading, duplicate stores, terminal
scraping, or unrestricted local-file access.

## Binding Decisions

- KB Markdown under `~/.mecha-developer/kb/` is the only durable memory. Normal
  KB updates remain the canonical capture and edit path.
- There is no agent-memory tier, Codex-memory or Claude-memory clone, generated
  eager-load index, or automatic migration. Material from an old store may be
  deliberately incorporated through a normal KB update.
- A rebuildable SQLite catalog supports recall but is not memory: it contains
  canonical frontmatter plus a derived copy of each record's Markdown body and
  is always rebuildable from Markdown.
- Recall activation and runtime state—including timers, cooldown, coalescing,
  and pending evaluation—exist only in the local Pi extension for that agent
  process. They are never router, KB-record, or durable session state.
- The old tmux/cron/Claude recall system is discarded. It is removed as an
  initial independently shippable phase; the new system does not coexist with
  it.

## Canonical Record Format

Each KB record is Markdown with frontmatter containing exactly these fields:

```yaml
id: dnsid-release-process
title: DNSid release process
relevant_at: 2026-07-13T00:00:00Z
related:
  - dnsid-deployment-checks
```

The Markdown body is the memory. It is the complete reusable learned assertion
or contract, including material conditions, limits, exceptions, decisions,
evidence, and links; it is not a separate supporting appendix to a frontmatter
summary. The catalog and APIs may call the derived body value `memory`, but no
`memory` frontmatter field exists.

Do not add provenance, type, scope, tags, confidence, sensitivity, expiry,
supersession, hashes, scores, or any other frontmatter field.

`relevant_at` is the sole ordering field and there is exactly one
ordering/list: `relevant_at DESC, id ASC`. New or edited KB records set it to
now. A record selected by recall is touched through the router API and also sets
it to now. No separate recency or relevance field exists.

`related` is the sole recall graph: an explicit list of record IDs. Normal KB
updates alone create or edit these links. Recall may follow links but never
writes them. A Markdown `See Also` section may exist for people, but is not part
of recall.

## Catalog, Lookup, And Policy

The router owns the rebuildable catalog, narrow memory lookup and touch APIs,
and central shared recall policy. It does not own activation or runtime state,
run the evaluator, or inject recall messages. Its catalog contains only:

```text
id, title, memory, relevant_at, related
```

The router has one `recall` policy block; no second recall configuration file is
allowed. It is supplied to Pi extensions and provides these defaults and hard,
configurable expansion bounds:

```yaml
recall:
  inactivity_seconds: 30
  maximum_window_seconds: 120
  candidate_limit: 100
  evaluator: openai-codex/gpt-5.6-luna
  maximum_messages: 3
  expansion:
    maximum_ids: 6
    maximum_terms: 3
    maximum_matched_records: 12
    maximum_bytes: 16384
    maximum_related_edges: 24
    maximum_graph_depth: 2
```

Normal lookup returns at most `candidate_limit` entries in the sole canonical
order. Bounded expansion accepts only requested stable IDs and literal terms;
the router controller validates every request, enforces all policy bounds, and
returns only the approved matching records and permitted `related` traversal.
This API never exposes paths, commands, unrestricted files, or the corpus.

There is no vector search, embedding model, Sentence Transformers, FTS, or
full-text index. Literal matching may inspect record bodies inside the router to
produce bounded candidates and expansions. A candidate's `memory` value is its
Markdown body, bounded by the candidate and expansion limits; the router never
exposes an unrestricted corpus or filesystem path.

## Recall Activation And Flow

The extension exposes `mecha_recall` with `enable`, `disable`, and `status`
actions. Invoking it changes only that loaded extension instance's in-memory
state. Recall is disabled by default for every new Pi process and after an
extension reload.

The supervisor must enable its own recall when it starts. For every managed
worker launch, it decides whether the assigned task could benefit from prior
knowledge; if so, it uses the worker-launch recall option to initialize recall
in that worker's local extension instance. This option is process-local startup
input, not router configuration or persisted state.

When enabled, the Pi extension owns activity observation, timing, cooldown, and
coalescing. For each active window, it starts a 30-second inactivity debounce
after activity and fixes a maximum 2-minute evaluation deadline from that
window's first activity. New activity resets the 30-second timer but never
extends the 2-minute deadline. It uses router-supplied shared limits rather
than duplicated constants.

When evaluation is permitted, the extension launches an isolated,
noninteractive local Pi evaluator. It supplies a short bounded context snapshot
and up to 100 normal candidates, each with stable `id`, `title`, `memory`,
`relevant_at`, and `related`, where `memory` is the bounded Markdown body
content. The evaluator receives no filesystem paths or unrestricted body
access.

The evaluator may request catalog expansion through the extension and
controller-validated router API, but has no direct path, file, shell, or command
access. Its fixed instructions require it to:

- default to no-op;
- select only directly relevant records, never merely thematic similarities;
- produce concise factual results grounded in supplied records;
- make no KB writes, link creation, or command requests; and
- emit JSON only.

After a valid selection, the extension calls the router touch API for the
selected records, then directly calls its local `pi.sendMessage()` with at most
three concise recall messages. Recall never uses Mailbox V2. Neither evaluator
nor router logs raw context snapshots or Markdown bodies. The extension drops
all activation and runtime state when its Pi process exits.

## Maintainability Constraints

Keep router and extension work in small, cohesive modules: catalog rebuild and
lookup/touch APIs, policy and session state, activity timing, evaluator launch,
and message delivery. Do not turn an existing router or extension file into a
memory monolith. Keep catalog rebuilding and bounded literal search simple,
local operations over the KB.

## Phased Implementation

Each phase is independently shippable and preserves KB Markdown as the sole
durable memory.

1. **Retire legacy recall.** Remove the tmux/cron/Claude recall path and its
   configuration; do not retain a compatibility or parallel recall path.
2. **Define records and catalog.** Validate the four-field frontmatter
   contract, treat the Markdown body as the memory, make normal KB writes set
   `relevant_at`, and build/rebuild the derived five-column SQLite catalog with
   deterministic ordering.
3. **Add router control-plane APIs.** Implement the central shared-policy block
   and bounded lookup/expansion and touch APIs. The router has no recall
   activation, cooldown, coalescing, or other per-agent session state.
4. **Add extension recall.** Implement the local activation tool and
   worker-launch option, policy-driven activity debounce and fixed window
   timing, local cooldown/coalescing and noninteractive evaluator launch,
   validated expansion, record touches, and direct `pi.sendMessage()` delivery.
   Update supervisor instructions to enable itself and to choose recall per
   worker launch.

## Acceptance Criteria

- Every canonical KB record has exactly `id`, `title`, `relevant_at`, and
  `related` frontmatter. Its Markdown body is the complete reusable memory,
  not a lossy synopsis or supporting appendix. The derived catalog maps that
  body to `memory`; `relevant_at DESC, id ASC` is the only ordering/list.
- Creating, editing, or selecting a record sets `relevant_at` to now. Recall
  never creates or edits `related` links.
- Deleting and rebuilding the SQLite catalog loses no memory and recreates only
  the five catalog fields.
- The old recall implementation is removed before new recall implementation;
  no legacy/coexisting delivery path remains.
- The router owns the catalog, narrow lookup/touch APIs, and the sole shared
  policy block, but neither activation, per-agent runtime state, evaluator
  execution, nor recall-message injection.
- The extension exposes local `enable`, `disable`, and `status` activation;
  starts disabled; and loses all recall state at process exit or reload. The
  supervisor enables itself and chooses whether to initialize recall per worker
  launch.
- When locally enabled, the extension follows the supplied policy: 30-second
  resettable inactivity, a non-extendable two-minute window deadline, up to 100
  candidates, and at most three direct local `pi.sendMessage()` recalls.
- Evaluator input includes only bounded context and bounded candidate bodies
  derived from Markdown; it has no filesystem or command access and produces
  JSON-only, directly relevant, factual no-op-or-recall results.
- Expansion is controller-validated and bounded by IDs, terms, matched records,
  bytes, related edges, and graph depth; it cannot expose the corpus.
- No vector, embedding, Sentence Transformers, FTS, or full-text index is
  created or required. Logs omit raw context snapshots and Markdown bodies.
