---
name: explain
description: "Describe code changes in plain English as a complete functional specification — no code in the output, precise enough to evaluate correctness, test coverage, and whether requirements are met without reading the source."
---

# Explain Skill

Produces a plain-language description of code changes that works as a standalone document. No code appears in the output — the description replaces it. The goal is a reliable mental model: accurate enough that you can evaluate whether the implementation is correct, whether the tests cover what matters, and whether anything is missing, all without looking at the source.

## Usage

```
/explain [--base=<branch>] [--requirements="..."] [--overview] [files_or_paths]
```

- `--base=<branch>`: Describe changes from `git diff <branch>...HEAD`. If omitted, describes uncommitted working tree changes.
- `--requirements="..."`: Optional. Requirements the implementation is meant to satisfy. Included as context so the description can be evaluated against them.
- `--overview`: Produce a high-level architectural explanation instead of exhaustive per-function detail. Best for large changesets (20+ files). Focuses on design decisions, component interactions, data flow, and trade-offs — the things a senior engineer's intuition catches. Omits per-branch/per-error-path detail that robot-review already covers.
- `files_or_paths`: Optional. Specific files or directories. If omitted, uses the full diff.

**Auto-detection**: If no `--overview` flag and the changeset touches 20+ files, default to overview mode with a note that `--detailed` can be used for exhaustive output.

**Examples:**
- `/explain` — describe uncommitted changes
- `/explain --base=main` — describe all changes on the current branch vs main
- `/explain --base=main --requirements="Add importShopifyProductAsSubscriptionTier mutation"` — with requirements context
- `/explain --base=main --overview` — high-level architectural explanation of the full branch
- `/explain src/handlers/subscription.go` — describe changes in a specific file

---

## What It Produces

A functional specification derived from the actual code — not from names, comments, or intent. Organized as: what was built, what each changed piece does, what the tests verify, and what behaviors are and aren't covered.

The output must satisfy three questions:
- Is the implementation correct and complete?
- Are the tests adequate?
- Is anything missing?

---

## Process

### 1. Gather Changes

- If `--base=<branch>`: run `git diff <branch>...HEAD`
- If specific files/paths: read those files directly (full content if new, diff if modified)
- Otherwise: run `git diff HEAD` for uncommitted changes; also check `git diff --cached` for staged changes

### 2. Read Context

Read the full file for each changed file — not just the diff — to understand surrounding types, interfaces, and structure. For any existing function or method that new code calls, locate and read its implementation so it can be described accurately inline.

### 3. Produce the Description

Apply the following instructions exactly. This is not a prompt to forward to another model — execute it directly.

---

**Instructions for producing the description:**

Your output will be used — without the code — to decide whether the implementation is correct, whether the tests are adequate, and whether anything is missing. The reader has no access to the source. The description is the only thing they have.

**What this requires:**

Every behavior that exists in the code must be described. Every branch taken, every error handled or not handled, every loop and what it accumulates. If the implementation is incomplete or partial, that must be visible in the description — not hidden behind vague language. A reader who understands the domain should be able to reconstruct the logic from the description alone, and should be able to spot a gap if one exists.

**Non-negotiable rules:**

1. **No code.** No snippets, no pseudocode, no inline expressions. Function and type names may appear as anchors (e.g., "the `importShopifyProduct` mutation"), but the description cannot depend on them — it must convey the behavior in words. If you cannot describe what a piece of code does without showing it, describe what makes it hard to characterize.

2. **Describe what the code does, not what it's called.** A function named `validateInput` that only checks one of three expected fields must be described as checking one field — not as "validates input." Names are not descriptions.

3. **Every branch.** For conditional logic: name the condition and describe what happens in each path. For loops: describe what one iteration does, what terminates the loop, and what the accumulated result is. Do not collapse multiple paths into "handles various cases."

4. **Every error path.** For each place an error can occur, state the error and exactly how it is handled: returned to the caller, wrapped with context, returned as nil, logged and swallowed. "Returns an error" is not sufficient — say what error and under what condition.

5. **Existing functions called by new code** must be described inline on first use: "calls `stripGID` (an existing helper that removes the `gid://shopify/Product/` prefix from a Shopify global ID and returns the bare numeric string)." Don't assume the reader knows what existing code does.

6. **Skip trivial changes.** Import additions, variable renames with no semantic effect, and minor formatting do not need to appear.

**Tests are described with the same rigor as implementation.** For each test case:
- Describe the scenario: what situation is being constructed
- Describe exactly what is asserted — not "verifies the output" but "asserts the returned struct has `ShopifyProductID` set to `9876` (the numeric portion after stripping the GID prefix) and `TierID` matching the tier that was created in setup"
- Describe the mock setup: what external dependencies are simulated, what responses they return, what calls are verified

**Coverage assessment is mandatory.** After describing all tests, produce an explicit assessment: for each meaningful behavior in the implementation — happy path, each distinct error path, each conditional branch — state whether it has a test. Name the uncovered behaviors. Do not summarize ("most paths are covered") — be specific.

---

If `--requirements` were provided, include this context at the start of your working notes (not necessarily in the output header, but use it to ensure the description is precise enough that a reader could evaluate each requirement):

```
Requirements provided:
[requirements text]

Ensure the description is specific enough that a reader can determine whether each requirement above is satisfied by what was implemented.
```

---

### 4. Output Format

```
## Summary
[2–4 sentences. What was built, at a functional level. What problem it solves or what capability it adds.]

## Implementation

### [filename]

**[function or method name]** (`path/to/file:line`)
[Full description: purpose, inputs, outputs, logic (all branches, loops), error handling, calls to existing functions with inline descriptions. Length scales with complexity — a simple setter gets one sentence; a function with three branches and two error paths gets a paragraph per case.]

[Repeat for each changed or added function in the file.]

[Repeat the ### section for each changed file.]

## Tests

### [test filename]

**[test name]** (`path/to/file:line`)
Scenario: [what situation is set up and why]
Verifies: [exact assertions in plain terms — field values, return values, error types, mock call counts]
Setup: [mocks used, what they return, what calls are expected]

[Repeat for each test case.]

## Coverage Assessment

[For each meaningful behavior in the implementation — list them — state whether it has test coverage and which test covers it. For behaviors with no test, name them explicitly.]
```

---

---

## Overview Mode (`--overview`)

For large changesets, produces a concise architectural explanation that helps a senior engineer evaluate whether the right approach was taken — not whether the code is clean (that's robot-review's job).

### What overview mode covers

Three sections, not seven. Every sentence earns its place — no catalog sections, no structural padding.

1. **Context** — narrative explanation of what existed before, what this PR does, and how the pieces connect. Written like you're explaining to a colleague, not filling out a template.
2. **Decisions** — the 4-8 choices that shaped the PR, each with: why it was made, what the alternative was, and what breaks if the decision turns out wrong. This is where intuition catches problems — wrong abstraction layer, over-engineering, missing separation of concerns, coupling that constrains future work.
3. **Questions** — the sharpest questions a senior engineer would ask after reading the context and decisions. Not generic ("is this tested?") but specific to this PR's approach ("What happens to in-flight sessions if the agent service restarts mid-conversation?"). **The questions ARE the primary value of overview mode** — they surface architectural and production risks that robot review completely misses. Robot review reads the diff line-by-line and catches implementation issues (error handling, concurrency bugs, test gaps). Questions catch system-level issues that only become visible when reasoning about the deployed whole: multi-instance behavior, failure cascading, resource growth over time, token/session lifecycle gaps, cleanup guarantees across process boundaries. Each question should: (a) identify a specific assumption or gap in the architecture, (b) explain the concrete mechanism by which it could fail in production, (c) be specific enough to act on immediately. These questions have directly led to fixes for: multi-instance coordination bugs (in-memory maps that don't sync), JWT expiry causing mid-run 401s, unbounded conversation history growth, and resource leak paths across panic recovery boundaries.

### What overview mode skips

- Per-function descriptions (use detailed mode or read the code)
- Branch-by-branch logic (robot-review catches these)
- Error handling specifics (robot-review catches these)
- Test descriptions (mention coverage gaps at the component level, not per-test)
- Trivial changes (imports, renames, formatting)
- Dependency catalogs (fold meaningful choices into Decisions)
- File-level "what changed" lists (fold into Context narrative)

### Overview output format

```
## Context
[5-8 sentences, narrative tone. What existed before, what this PR adds or changes,
and how the pieces fit together at a component level. Be concrete — "the Go handler
receives the webhook, validates the signature, and forwards to the TS agent via
HTTP POST" not "the system processes webhooks." Include data flow and where state lives.]

## Decisions
1. **[Decision]** — [why]. Alternative was [X], which would have [trade-off].
   If this turns out wrong: [consequence — what breaks or gets harder].
2. ...
[4-8 items. Each decision includes the alternative not taken and the cost of being wrong.
This is what surfaces "wrong architectural bet" concerns.]

## Questions
1. [Specific, sharp question about this PR's approach — not code quality]
2. ...
[3-6 items. These are the things a senior engineer's intuition would probe.
Phrased as questions because risks-as-questions are more actionable than risks-as-statements.]
```

### Process for overview mode

1. Run `git diff --stat <base>...HEAD` to get the full file list
2. Group files by component/package — don't read every file, read representative ones
3. For each component: read the main entry point and key types/interfaces
4. Focus on understanding interactions between components, not internals
5. **Before writing anything**: explicitly identify the 4-8 decisions that shaped the PR. This forces the hard analytical work upfront rather than defaulting to description
6. Produce the overview following the 3-section format above

---

## Notes

- For large changesets in detailed mode, work file by file rather than trying to hold everything at once.
- If a section of code is genuinely ambiguous or unclear from reading, say so in the description — "the condition for this branch is unclear from the implementation" is valid output and useful signal.
- The coverage assessment (detailed mode) is the most commonly skipped section and the most important for evaluating test quality. Do not abbreviate it.
- Overview mode auto-triggers at 20+ files. If the user explicitly asks for detailed on a large changeset, honor it but warn about length.
