---
name: generate-persona
description: "Generate a behavioral persona from any data source (chat logs, PR reviews, text files, git history), guided by intended use case. Replaces generate-reviewer-persona and generate-supervisor-persona."
---

# Generate Persona Skill

Reverse-engineer a behavioral persona from data, guided by the user's intended use case.

## Usage

```
/generate-persona [--use PURPOSE] [--source SOURCE] [--output PATH]
```

- `--use`: code-review, supervision, teaching, architecture, or general (default: ask user)
- `--source`: chat-logs, github-prs, text-files, git-history (default: ask user)
- `--output`: output file path (default: `personas/<name>.md` or `personas/<name>-reviewer.md` for code-review)

If the output file already exists, the skill operates in **update mode** — reading the existing persona as a prior and merging new evidence into it. To generate fresh, delete the output file first or specify a different `--output` path.

Examples:
```
/generate-persona --use code-review --source github-prs
/generate-persona --use supervision --source chat-logs
/generate-persona --use general --source text-files
/generate-persona --use architecture --source git-history
```

## Process

### Step 1: Determine Use Case

If `--use` was not specified, ask the user:

> What will this persona be used for?
> - **code-review** — PR reviewer replica (severity calibration, patterns they catch, technical preferences)
> - **supervision** — decision-making and delegation guidance (priority framework, trust calibration, escalation rules)
> - **teaching** — instructional style and knowledge scaffolding
> - **architecture** — design philosophy and technical vision
> - **general** — broad behavioral profile, no domain-specific emphasis

If the user doesn't specify or says "general", produce a general-purpose behavioral persona.

Record the chosen use case as `USE_CASE` for later steps.

### Step 2: Determine Data Source

If `--source` was not specified, ask the user:

> What data should I analyze?
> - **chat-logs** — Claude Code session logs (`.jsonl` files in `~/.claude/projects/`)
> - **github-prs** — PR review comments for a GitHub user (requires username and repo)
> - **text-files** — any directory of text documents (meeting notes, emails, Slack exports, etc.)
> - **git-history** — commit messages and diffs for a specific author

If the user doesn't specify, suggest **chat-logs** as the default.

Collect source-specific parameters:
- **chat-logs**: optional `--project <path>` to filter by project, or `--all` for everything
- **github-prs**: required `<username>` and `<owner/repo>`, optional `--months N` (default: 18)
- **text-files**: required `<directory-path>`, optional glob pattern (default: `**/*.{md,txt}`)
- **git-history**: required `<author-name-or-email>` and `<repo-path>`, optional `--months N` (default: 12)

Also ask for the **persona name** (used for the output filename and document title).

### Step 3: Extract Data

Use the appropriate adapter script from `tools/` to extract and normalize source data into JSONL records.

```bash
mkdir -p /tmp/persona-analysis
```

All adapters emit JSONL records to stdout with this contract:

```json
{
  "text": "normalized text chunk",
  "metadata": {
    "source_type": "claude-chat-logs",
    "source": "/path/or/identifier",
    "date": "2026-04-04T12:34:56Z",
    "context": {
      "session_file": "...",
      "speaker": "human",
      "project": "..."
    }
  }
}
```

Required fields: `text`, `metadata.source_type`, `metadata.source`.
Optional but preferred: `metadata.date`, `metadata.context`.

#### Adapter: Chat Logs

```bash
python3 .claude/skills/generate-persona/tools/extract_claude_logs.py <paths...> \
  > /tmp/persona-analysis/chunks.jsonl
```

- Reads `.jsonl` session logs from `~/.claude/projects/`
- Focuses on human-authored messages
- Skips sessions with fewer than 5 human messages (configurable via `--min-human-messages`)
- Automatically excludes subagent logs

#### Adapter: GitHub PR Reviews

```bash
python3 .claude/skills/generate-persona/tools/extract_github_reviews.py <username> <owner/repo> \
  [--months 18] > /tmp/persona-analysis/chunks.jsonl
```

- Fetches inline PR review comments (`pulls/comments` endpoint)
- Fetches issue-level comments (`issues/comments` endpoint) where reviewers leave general feedback
- Filters by username and date cutoff
- Note: The `pulls/{N}/reviews` endpoint may be blocked by hook policies in some environments — the two endpoints above capture the bulk of review signal

#### Adapter: Text Files

```bash
python3 .claude/skills/generate-persona/tools/extract_text_files.py <paths...> \
  [--max-chars 4000] > /tmp/persona-analysis/chunks.jsonl
```

- Recursively scans directories for `.txt`, `.md`, `.rst`, `.log` files
- Chunks large documents into smaller units for analysis

#### Adapter: Git History

```bash
python3 .claude/skills/generate-persona/tools/extract_git_history.py --author "<author>" \
  [--repo <path>] [--max-count 50] > /tmp/persona-analysis/chunks.jsonl
```

- Reads commit messages plus diffs
- Each commit becomes one or more chunks depending on size
- Useful for architecture, code quality, or decision-trace personas

### Step 4: Analyze Patterns

For large data sources, split extracted files into batches and analyze in parallel using subagents. For small data sources (single file or <50KB total), analyze directly.

**Batching rule:** Group session files by total size, aiming for roughly equal work per batch (1-3 files each). Launch one subagent per batch, all in parallel.

**Subagent prompt:**

---

You are analyzing text data to reverse-engineer how a person thinks, makes decisions, communicates, and works. Focus exclusively on the HUMAN's behavior (in conversations) or the AUTHOR's patterns (in reviews/commits/documents).

For each observation, first identify the behavior, then reason backwards to the mental model or value that would produce it. The goal is to understand *why* they do what they do.

Analyze these **core dimensions** (all use cases):

**Communication Style**
- Tone: direct, diplomatic, technical, casual, formal
- Instruction style: terse vs. detailed, imperative vs. collaborative
- How they give corrections: tone, specificity, patterns
- Emotional range and triggers

**Decision-Making**
- How they choose between options, what they optimize for
- What they push back on vs. accept easily
- How they handle uncertainty, mistakes, disagreements
- What triggers frustration vs. patience

**Values and Principles**
- What they care about deeply, what they find unacceptable
- Recurring themes and priorities
- Non-negotiables vs. flexible preferences

**Domain Expertise**
- Topics they engage deeply with
- Areas where they show authority vs. deference
- Recurring frameworks or mental models they apply

Now analyze these **use-case-specific dimensions** based on `USE_CASE`:

**If code-review:**
- Severity calibration: what's blocking vs. nit vs. ignored
- Technical patterns they catch repeatedly
- Architecture and design preferences
- Testing philosophy and expectations
- API design opinions
- Code organization preferences
- How they acknowledge good work
- How they handle disagreement on review comments

**If supervision:**
- Delegation model: what they control vs. hand off
- Trust calibration: how they adjust autonomy based on signals
- Verification approach: personal review, automated gates, trust levels
- Iteration style: big-bang vs. layered
- Priority framework: how they triage competing demands
- Escalation criteria: what triggers halt-and-ask
- Systems thinking: pipelines vs. components

**If teaching:**
- Scaffolding approach: how they build understanding progressively
- Explanation style: analogies, examples, first-principles
- How they assess comprehension
- How they adjust for different experience levels
- What they consider essential vs. nice-to-know

**If architecture:**
- Design philosophy: simplicity vs. flexibility, YAGNI vs. extensibility
- How they reason about tradeoffs
- Long-term vision: where they want systems to go
- Technology evaluation criteria
- How they handle legacy systems and migration

For each pattern you identify, state the **generalized rule**, not a specific example. Reason about what underlying value or mental model produces the pattern. Do not copy quotes from the source data — the deliverable is extracted principles, not collected evidence.

Write your analysis to: `/tmp/persona-analysis/notes-batch-<N>.md`

---

### Step 5: Synthesize Persona

After all analysis subagents complete, read all batch analysis files. Identify:

- **Convergent patterns**: Observations across multiple batches — high-confidence traits
- **Unique insights**: Single-batch findings that ring true given other evidence
- **Contradictions**: Resolve by identifying the contextual variable (e.g., "minimalist with code changes, maximalist with knowledge acquisition")
- **Gaps**: Dimensions with insufficient evidence — note them but don't fabricate

**Updating an existing persona:** If the output file already exists, read it first and treat it as a prior. Preserve structure and principles that the new evidence supports. Add, revise, or remove principles based on what the analysis found. Flag contradictions between existing principles and new evidence to the user rather than silently overwriting. This allows re-running the skill against fresh data to iteratively refine a persona.

**Generating a new persona:** Use the appropriate template for the use case.

#### Template: Code Review Persona

```markdown
# <Name>: PR Reviewer Profile

Brief intro paragraph describing their review philosophy and experience level.

---

## Core Review Philosophy

### 1. [Principle Name]
Description of what they care about.

**Patterns:**
- "Example quote or paraphrased behavior"
- "Another example"

**The underlying value:** Why this matters to them.

[Repeat for 5-8 core principles]

---

## Communication Style

### Tone
Description with examples.

### Common Phrases
- List characteristic vocabulary

### Severity Markers
How they distinguish blocking issues from nits.

---

## Common Patterns They Catch

1. **Pattern Name** — brief description
2. ...

---

## Technical Preferences

### [Area]
- Bullet points of preferences

---

## Summary

[Name] reviews with a focus on:
- **X** over Y
- **A** over B
...
```

#### Template: Supervision / General / Teaching / Architecture Persona

```markdown
# <Name>: Decision-Making Principles

Brief description of what this document is for.

---

## Core Principles

### 1. [Principle Name]

One-paragraph description of the principle — what it is and why it matters.

**This means:**
- Actionable bullet points that an agent can follow
- Each bullet is a generalized rule, not a specific example
- Rules should be concrete enough to apply to novel situations

**The underlying value:** One or two sentences on the deeper motivation.

---

[Repeat for 8-12 core principles]

---

## Working Style

### [Aspect Name]
Description of how they work in this dimension.

[Cover: delegation model, trust calibration, iteration style, context awareness]

---

## How They Think

### [Pattern Name]
Description of the cognitive pattern.

[Cover: systems thinking, generalization instinct, learning approach, meta-cognition]

---

## Communication Style

Description of how they communicate across different contexts: approval, correction, errors, instructions, emotional range. Articulate the rules governing each.

---

## Decision Framework

Ordered checklist of questions to ask when making a judgment call.

---

## What Warrants Escalation

Bulleted list of situations that should trigger a halt-and-ask.

---

## Summary

[Name] optimizes for:
- **X** over Y
[List of 8-12 value pairs]

One-sentence closing principle.
```

**Template selection:**
- `code-review` → Code Review Persona template
- `supervision`, `general`, `teaching`, `architecture` → Decision-Making Principles template (adjust section names as appropriate for the use case — e.g., a teaching persona might rename "What Warrants Escalation" to "When to Pause and Reassess")

### Step 6: Quality Checklist

Before finishing, verify:

- [ ] Every principle is a generalized rule, not a description of a specific incident
- [ ] No direct quotes copied from source data as filler — quotes are used as evidence for extracted rules, not as the rules themselves
- [ ] Principles that appeared across multiple sessions/sources are weighted more heavily than one-offs
- [ ] Contradictions are resolved with contextual explanations, not ignored
- [ ] The "This means" or "Patterns" bullets are concrete enough that an agent could apply them to a novel situation
- [ ] Communication style is described as rules governing behavior, not a collection of example phrases
- [ ] The document could guide an agent's decisions in a novel context, not just the ones in the source data
- [ ] Technical preferences are specific to their domain, not generic best practices
- [ ] Summary captures the essence as value pairs, not just a list
- [ ] If updating an existing persona, existing principles are preserved unless contradicted by evidence
- [ ] Output path is correct (`personas/<name>.md`, `personas/<name>-reviewer.md` for code-review, or user-specified path)

## Extensibility

### Adding a new data source

1. Create a new `tools/extract_<source_name>.py` script
2. The script must emit JSONL records to stdout using `_common.emit_record()` with the contract defined in Step 3
3. Add a subsection under **Step 3: Extract Data** documenting the script invocation and any source-specific notes
4. No changes needed to the analysis pipeline (Step 4) or synthesis (Step 5) — they work on the normalized JSONL output

### Adding a new use case

1. Add the use case name to the list in **Step 1**
2. Add use-case-specific dimensions to the subagent prompt in **Step 4**
3. Add template guidance in **Step 5** (or note which existing template to use)

## Wrapper Mapping

Legacy entry points map to this skill as follows:

- `generate-reviewer-persona` => `generate-persona` with `use="code review"` and `source="github-pr-reviews"`
- `generate-supervisor-persona` => `generate-persona` with `use="supervision"` and `source="claude-chat-logs"`

## Reference

- See `personas/evan-reviewer.md` for an example of code-review persona quality
- See `personas/wolfgang.md` for an example of supervision persona quality
- Use the `augment-persona` skill for iterative improvement after initial generation
