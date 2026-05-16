---
name: kb
description: "Manage the persistent markdown knowledge base at ~/.mecha-developer/kb/. Trigger: user asks to save, look up, search, or organize information in the knowledge base. Do NOT use for temporary notes or one-off context — only for persistent, reusable knowledge worth keeping across sessions."
---

# Knowledge Base

Manage a persistent markdown knowledge base at `~/.mecha-developer/kb/`. Ingest information, search it, and keep it organized.

## Usage

```
/kb ingest <text or file path>
/kb search <query>
/kb list [domain]
/kb show <topic>
/kb edit <topic> <instructions>
/kb reorganize
/kb status
```

## Storage

```
~/.mecha-developer/kb/
  INDEX.md                # Central index -- all topics grouped by domain
  <topic-slug>.md         # One file per topic (flat by default)
  <domain>/               # Created during reorganization when a domain grows
    INDEX.md              # Per-directory index
    <topic-slug>.md
```

**Slugs**: lowercase, hyphen-separated, 2-4 words max. Derive from the topic title. Examples: `jwt-authentication.md`, `deploy-checklist.md`, `redis-caching.md`.

On first use, create `~/.mecha-developer/kb/` and `INDEX.md` if they don't exist:

```markdown
# Knowledge Base Index

## Ungrouped
```

### INDEX.md Entry Format

Each entry is a bullet with a markdown link and a short description:

```markdown
## Auth

- [JWT Authentication](jwt-authentication.md) -- token types, expiry, signing algorithm
- [OAuth2 Integration](oauth2-integration.md) -- provider config and callback handling

## Infrastructure

- [Docker Builds](docker-builds.md) -- multi-stage patterns, cache optimization

## Ungrouped

- [Deploy Checklist](deploy-checklist.md) -- pre-deploy validation steps
```

When a domain graduates to a subdirectory, the central INDEX.md links to the directory index:

```markdown
## Auth

See [Auth/INDEX.md](auth/INDEX.md)
```

## Topic File Format

Every topic file follows this structure:

```markdown
# Topic Title

**Tags**: tag1, tag2, tag3
**Sources**: where the information came from
**Created**: YYYY-MM-DD
**Updated**: YYYY-MM-DD

---

## Summary

One to three sentences capturing the key insight or purpose.

## Content

[Freeform markdown -- headings, lists, code blocks, whatever fits.]

## See Also

- [Related Topic](relative-path.md) -- why it's related
```

## Commands

### ingest

Parse the argument: if it's a file/directory path that exists, read it. Otherwise treat it as raw text content.

Process:
1. Read `INDEX.md` to understand current KB topology.
2. Analyze the content. Identify the topic(s), key facts, and relevant domain.
3. Search existing topic files for overlap -- check titles, tags, summaries. Use Grep across `~/.mecha-developer/kb/`.
4. **If a matching topic exists**: integrate the new information into the existing content structure. Add new subsections under `## Content` if the info is a distinct subtopic. Do not create dated subsections -- the `Updated` field tracks recency. The goal is a clean reference document, not a changelog. Update the `Updated` field. Add new tags if applicable.
5. **If new topic**: create a new file using the topic file format. Add it to INDEX.md under `Ungrouped` unless it clearly belongs to an existing domain section already in INDEX.md. Never create a new domain section during ingest -- domains emerge during `/kb reorganize`.
6. Add `See Also` links to related topics that already exist in the KB.
7. Check reorganization thresholds (see below). If hit, suggest `/kb reorganize`.

**Multiple topics from one source**: If the content clearly covers multiple distinct topics (e.g., a Slack thread with three separate decisions), create separate topic files for each. Run steps 3-6 for each identified topic. All share the same source attribution.

**Critical rule -- distill, don't dump**: Synthesize ingested content into clear, structured knowledge. Extract the rules, patterns, and facts. Don't paste raw text. If the raw source needs preservation (Slack export, meeting notes), put it in a collapsed `<details>` block below the distilled content.

**Slack and data dump handling**: When ingesting Slack exports or large text dumps:
- Extract the actionable knowledge, decisions, and facts
- Record the source and date
- Discard conversational noise (greetings, reactions, tangents)
- Preserve key quotes only when they capture something the distilled version can't

### search

1. Read `INDEX.md` -- scan topic titles, domain names, descriptions for matches.
2. Grep all `*.md` files in `~/.mecha-developer/kb/` for query terms (use Grep tool).
3. Rank: title match > tag match > summary match > body match.
4. Display top 10 results with topic title, summary, file path, and matching context.
5. If no results: report "No results for '<query>'." Suggest alternative terms if obvious.

### list

- No argument: read and display `INDEX.md`.
- With domain argument: show only that domain's section. If the domain has a subdirectory with its own INDEX.md, read that instead.

### show

Find the topic file by matching the argument against INDEX.md entries or globbing `**/*<slug>*.md` in the KB directory. Read and display it.

### edit

Find the topic file. Apply the requested changes. Update the `Updated` date. If the topic's summary or domain changed, update INDEX.md accordingly.

### reorganize

Always present the plan and get user approval before executing.

Analyze the KB and propose changes:

1. **Split oversized files**: Any topic file exceeding 300 lines -- analyze its headings and split into subtopic files. The original becomes a hub page linking to subtopics.
2. **Graduate domains**: Any domain section in INDEX.md with more than 8 topics -- create a subdirectory, move files, create per-directory INDEX.md, update central INDEX.md to link to directory.
3. **Cluster ungrouped topics**: Read all ungrouped topic summaries and tags. Identify natural clusters of 3+ topics. Propose domain assignments. Clusters smaller than 3 remain in Ungrouped.
4. **Fix broken links**: Grep all KB files for markdown links. Verify each target exists. Report and fix broken references.

When files move, update all markdown links across the KB that referenced the old path.

### status

1. Count total topics, topics per domain, ungrouped count.
2. Flag oversized files (>300 lines).
3. Check for broken cross-references.
4. Report whether reorganization is suggested.

## Reorganization Thresholds

Suggest `/kb reorganize` when any of these are true:
- Ungrouped section has more than 10 topics
- Any topic file exceeds 300 lines
- Any domain section has more than 15 topics

## Error Handling

- **No argument to ingest**: Ask the user what they want to ingest.
- **Topic not found (show/edit)**: Report "No topic matching '<query>' found." Run a search and show the closest matches.
- **Multiple matches (show/edit)**: List all matches with file paths and ask the user to pick one.
- **Binary/non-text file**: Report that the file cannot be ingested as text. Suggest the user provide a text description instead.
- **File too large (>1000 lines)**: Warn the user. Process the first 1000 lines and note truncation.
- **Search with no results**: Report "No results for '<query>'." Suggest alternative terms if obvious.

## Principles

- **Flat-first**: Start with flat files. Directories emerge from observed clustering during reorganization, not upfront guessing.
- **Distill, don't dump**: Every piece of ingested content must be synthesized. Raw data goes in collapsed blocks if needed. The KB contains knowledge, not transcripts.
- **Standard markdown links**: Cross-references use `[text](relative-path.md)`. No wiki-style links, no tag indexes, no resolution layers.
- **Single source of truth**: INDEX.md is the authoritative topology. Topic files are the authoritative content. No duplicate indexes.
- **Manual reorganization**: Never auto-reorganize. Suggest it, but wait for explicit `/kb reorganize` invocation and user approval of the plan.
