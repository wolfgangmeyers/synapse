---
name: wolfgang-voice-review
description: Private Wolfgang-specific PR review rewrite gate. Use after robot-review or pr-validate and before posting GitHub PR review bodies or inline comments as Wolfgang, when Codex must load the Wolfgang voice persona, understand, keep/drop/rewrite, and convert validated findings into Wolfgang's concise review voice while preserving technical disposition.
---

# Wolfgang Voice Review

Use this as the final gate before posting PR review comments as Wolfgang. This is not a generic tone pass. Understand every finding, drop weak ones, and rewrite only the comments that should actually be posted.

## Voice Source

Before rewriting, read `personas/wolfgang-voice.md` from this repo and use it as the source of truth for how Wolfgang writes. The rules in this skill are PR-review-specific constraints and quick reminders; they do not replace the persona.

If the persona file is not available, ask the caller to provide it or enough voice guidance before producing postable text.

## Inputs

Expect any mix of:

- Validated robot-review findings, including severity/disposition.
- Inline comment targets: file, line, side, diff hunk, or original finding id.
- A review body or summary comment.
- PR diff/context and any validation notes.
- Posting constraints from the caller or review tool schema.

If the inputs do not include enough code or context to understand a finding, request the missing context or mark it unpostable. Do not rewrite opaque findings.

## Required Understanding

Before rewriting each finding, be able to explain to yourself:

- What behavior, contract, invariant, or API expectation is violated.
- Why the cited code causes that issue.
- What concrete change the author should make, or what question must be answered.
- Whether the original disposition still matches the actual risk.
- Whether the reviewed layer can realistically provide the guarantee being requested.

Keep this explanation internal unless the caller asks for reasoning. If you cannot explain the finding without leaning on the original wording, drop it or ask for context.

## Workflow

1. Load the voice source.
   - Read `personas/wolfgang-voice.md`.
   - Keep its general voice rules in mind while applying the PR-specific filters below.

2. Normalize the input.
   - Separate review body from inline findings.
   - Preserve ids, paths, lines, dispositions, and any posting metadata.
   - Treat validation notes as evidence, not as text to post.

3. Gate each finding.
   - Keep findings that are true, material, actionable, and tied to the PR.
   - Rewrite findings that are true but verbose, robotic, overexplained, or aimed at the wrong level.
   - Drop false positives, duplicates, speculative noise, weak nits, style preferences without impact, and findings that depend on missing context.
   - Drop local-process narration: "I ran X", "validated locally", "the reviewer found", "high confidence", "line N shows".
   - Do not preserve original text for dropped findings.

4. Correct the technical claim before changing voice.
   - If the original overclaims, rewrite to the smaller true issue.
   - If a bug claim is really an API/documentation ambiguity, say that.
   - If the abstraction must trust an implementation, ask to document the contract/trust boundary instead of demanding impossible proof.
   - If a feature is half-supported, ask whether it is in scope before requiring the full design.

5. Preserve disposition honestly.
   - Keep blockers substantively blocking, but never write `Blocking:`.
   - Keep should-address findings clearly actionable, but never write `Should address:`.
   - Keep optional/nit comments brief; drop them if they are not worth posting.
   - Downgrade only when the technical analysis weakens the claim, and record the changed disposition in the output metadata.
   - Never make a serious issue sound optional just to be casual.

6. Rewrite the surviving comments.
   - Start with the useful observation or question.
   - Prefer one short paragraph, usually 1-3 sentences.
   - Include the concrete fix or test when it matters.
   - Use code terms inline with backticks.
   - Cut function-by-function proof trails unless the author needs them to act.
   - Rewrite both review body text and inline comment bodies.

7. Final check.
   - The posted text should read like a human reviewer who understands the code.
   - No robotic labels, rubric terms, or review-system narration.
   - No generic "Socratic question" framing.
   - No local validation/process commentary.
   - No stale original text from dropped findings.

## Review Body Rules

- Use an empty body if the inline comments stand alone.
- If a body is needed, keep it short and specific to the review decision.
- Do not summarize every inline comment.
- Do not narrate the review process or validation work.
- Do not use formal openers, sign-offs, or "thanks".

Good body shapes:

- `I think there are a couple things here we should fix before merging.`
- `Mostly looks good, but I left two comments around the public verification path.`
- `I don't think this should merge until the release-ordering issue is handled.`

## Inline Comment Rules

Prefer Wolfgang's review shape:

- `Looks like ... Can we ...?`
- `I think ...`
- `Shouldn't this ...?`
- `Seems like ...`
- `Can we document ...?`
- `This should ... Unless this is supposed to be ...?`
- `Hmm aren't ...? Are we adding support for that in this PR?`

Use the shape that matches the confidence:

- Use direct statements for facts: `v0.11.0 doesn't have the new LogEvent fields.`
- Use `Looks like` or `Seems like` for observed but not fully proven behavior.
- Use `I think` for design interpretation or contract assumptions.
- Use question-shaped comments for missing conditions, edge cases, or scope ambiguity.
- Add one short failure-mode sentence when a bare question would be too easy to dismiss.

## Durable Filter Rules

- Prefer the actionable release/process decision over a full tooling proof.
- For dependency/versioning issues, say the concrete incompatibility first and propose the release sequence.
- For validation-path mismatches, lead with the missing requirement and affected scope.
- For time/security findings, name the trust boundary directly: self-declared time vs trusted time.
- For APIs that imply stronger semantics, lead with the expected contract.
- If a weaker contract is plausible, ask that as a short alternative.
- When code claims a standard but only supports a subset, say that directly and offer the two viable paths.
- For API ambiguity, ask to document the contract instead of implying an implementation bug.
- For simple logic issues, ask the direct question first and keep the rationale to one sentence.
- Avoid long hypothetical examples when the invariant is easy to state.
- Avoid exhaustive internal mechanism unless it changes the requested fix.

## Voice Rules

- Start with the point. No greeting or preamble.
- Keep sentences short. Prefer 1-3 sentences per inline comment.
- Use contractions.
- Use `we` for shared PR actions.
- Hedge once when uncertain; do not stack hedges.
- State facts bluntly.
- Use `but`, `so`, `also`, and `unless` for transitions.
- Avoid corporate or formal transitions: no `furthermore`, `additionally`, `however`, or `in conclusion`.
- Avoid effusive praise and apology.
- End when the comment has said the useful thing.

## Avoid

Never post these patterns:

- `Blocking:`
- `Should address:`
- `Socratic question:`
- `High confidence`
- `The reviewer found`
- `I validated locally`
- `I ran the tests`
- `This finding`
- `Please consider`
- `It may be beneficial`
- Long explanations of how the review was produced.
- Long lists of alternative fixes when one preferred contract is clear.

## Output Format

Default to structured Markdown unless the caller needs a specific tool schema:

```markdown
## Review Body
<rewritten body, or "(empty)">

## Inline Comments
- id: <source id if available>
  path: <file path>
  line: <line/side metadata if available>
  disposition: <blocking|should-address|optional|nit>
  decision: <keep|rewrite>
  body: |
    <rewritten comment text>

## Dropped
- id: <source id if available>
  disposition: drop
  reason: <short internal reason; do not post>
```

If emitting directly to a posting tool, include only the rewritten review body and surviving inline comment bodies. Keep disposition and drop reasons in the agent/tool metadata, not in the posted PR text.
