---
name: wolfgang-voice-review
description: Private Wolfgang-specific rewrite gate for anything posted under his name that outlives the conversation. Use after robot-review or pr-validate and before posting GitHub PR review bodies and inline comments, Linear ticket comments and descriptions, RFCs and documents, commit messages, or published pages. Loads the Wolfgang voice persona, keeps/drops/rewrites validated findings into his concise voice while preserving technical disposition, and never changes what a sentence claims.
---

# Wolfgang Voice Review

Use this as the final gate before posting PR review comments as Wolfgang. This is not a generic tone pass. Understand every finding, drop weak ones, and rewrite only the comments that should actually be posted.

It also gates anything else posted under his name that outlives the conversation: Linear ticket comments and descriptions, RFCs and other documents, commit messages, and published pages. The PR-specific sections below apply to reviews. **Never Change What A Sentence Claims** and **Sentence Shapes To Remove** apply to everything.

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

## Never Change What A Sentence Claims

A rewrite may change how something reads. It may never change what it asserts.

This skill reviews prose against a persona. It has no way to know what Wolfgang has read, agreed to,
approved, or decided. So a rewrite that changes the grammatical subject can invent a fact, and no
style check catches it.

The case that caused this rule, on 2026-08-06: a ticket said `Wolfgang has signed off the
disposition`. That was correctly flagged, because it reads like someone else writing about him in a
document posted under his name. The repair switched it to first person, `I have signed off the
approach`. That fixed the voice and asserted an approval he had never given, on an RFC he had not
read.

Before accepting any rewrite:

- If the subject, person, tense, or voice changed, reread the new sentence and ask what it now
  claims. Person and tense both carry claims.
- Never write a first-person statement of approval, agreement, decision, or knowledge unless the
  caller has confirmed it happened. When in doubt, ask the caller.
- For anything not yet approved, write the condition instead of the claim: `The approach has been
  approved` as a thing still to happen, never `I approved the approach`.
- `The RFC recommends X` is safe. `So use X` reads as settled and usually is not.

Flag any sentence you cannot verify rather than rewriting it into confidence.

## Sentence Shapes To Remove

These four accounted for most of what Wolfgang rejected on 2026-08-06, across chat, tickets, and a
published page. None involves an unusual word, so a vocabulary check misses all of them.

- **Two balanced halves, the second landing as the payoff.** `Not X. It is Y.` `X costs A, Y costs
  B.` `It stops being X and becomes Y.` `Taken at face value that argues for ranking. Taken at face
  value is the mistake.` Say the one true thing and stop.
- **`rather than` and `instead of` as padding.** Nearly every use deletes cleanly. `Verify it against
  the code rather than trusting the sender` means `verify it against the code`. If the contrast
  matters, make it its own short sentence.
- **A short emphatic fragment after the point.** `Six words.` `Zero.` `Eight paid calls found it.`
  It underlines something already said.
- **A clause tacked onto a finished sentence.** A trailing `which is…`, a `with…` carrying a second
  idea, a `that…` explaining why the fact matters. If the tail is worth saying it is its own
  sentence, and usually it is not.

Also watch: em dashes as a pause inside a sentence (they should be almost absent), semicolons (the
persona uses none), the rule of three, a closing line telling the reader how to feel, and negative
constructions where a positive is shorter. Keep sentences carrying a negative under a quarter of the
total.

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
