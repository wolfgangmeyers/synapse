---
name: voice-review-builder
description: "Collect and distill Wolfgang's feedback on PR review comments to improve review voice and review quality. Use when continuing a comment-by-comment calibration session, fetching GitHub PR comments for discussion, appending Wolfgang's rewrite/reasoning notes, or turning those notes into updates for personas/wolfgang-voice.md and .claude/skills/wolfgang-voice-review/SKILL.md."
---

# Voice Review Builder

Use this skill to run a calibration loop over PR review comments. The goal is to learn how Wolfgang wants review comments to sound and what review findings he would keep, rewrite, downgrade, or drop.

This is a data-collection and distillation skill. It is not the final review filter. The downstream artifacts are:

- `personas/wolfgang-voice.md`
- `.claude/skills/wolfgang-voice-review/SKILL.md`

## Starting Context

At the start of a session:

1. Read `personas/wolfgang-voice.md`.
2. Read `wolfgang-voice-review-notes.md` if it exists.
3. If continuing a known PR, re-fetch the comment list rather than relying on memory.
4. Continue from the last completed note entry.

Use `wolfgang-voice-review-notes.md` as the default working notes file unless the user names another file.

## Fetching Comments

When the user gives a GitHub PR URL, fetch review comments with `gh`.

Get review summaries:

```bash
gh api repos/OWNER/REPO/pulls/NUMBER/reviews --paginate \
  --jq '[.[] | select(.user.login == "wolfgangmeyers") | {kind:"review", id, state, submitted_at, html_url, body}]'
```

Get inline review comments:

```bash
gh api repos/OWNER/REPO/pulls/NUMBER/comments --paginate \
  --jq '[.[] | select(.user.login == "wolfgangmeyers") | {kind:"inline", id, pull_request_review_id, created_at, updated_at, html_url, path, line, original_line, body, diff_hunk}]'
```

Get standalone PR issue comments:

```bash
gh api repos/OWNER/REPO/issues/NUMBER/comments --paginate \
  --jq '[.[] | select(.user.login == "wolfgangmeyers") | {kind:"issue", id, created_at, updated_at, html_url, body}]'
```

If the user wants to review agent-generated comments instead of Wolfgang-authored comments, change the author filter or use the raw comments they provide.

## Comment Loop

Process one comment at a time. Do not advance until Wolfgang has finished giving feedback on the current comment.

For each comment, show:

1. Comment number and total count.
2. PR and comment link.
3. File and line for inline comments.
4. Original comment text.
5. Plain-English explanation of what the comment means.
6. A short prompt for feedback.

The explanation is required. Assume some implementation or ecosystem details may be unfamiliar. Explain the failure mode in practical terms, but keep it concise.

If Wolfgang asks follow-up questions about the issue, answer those before asking for rewrite feedback again. Do not move to the next comment while there is active discussion on the current one.

## Capturing Feedback

After Wolfgang provides the preferred wording, reasoning, or critique, append a note entry before continuing.

Each entry should include:

- Original comment.
- Wolfgang rewrite or preferred wording.
- Why the original was off.
- Distilled rules.

Use this shape:

```markdown
### Comment N: short descriptive title

Original comment:

> ...

Wolfgang rewrite:

> ...

Why the original is off:

- ...

Distilled rules:

- ...
```

If Wolfgang decides a finding should be dropped, do not append a note entry for it. Skip it and continue to the next comment unless Wolfgang explicitly asks to record why it was dropped.

If Wolfgang only gives partial feedback, capture what is clear and ask targeted follow-up questions only when needed.

After appending or changing notes for a comment, briefly state what changed and wait for Wolfgang to approve moving on. Do not print the full updated entry unless Wolfgang asks; he can see file edits. Do not present the next comment in the same response unless Wolfgang explicitly asks for it.

Distilled rules must be reusable review-writing behavior, not facts from the current bug. Keep technical details such as field names, function names, API order, protocol-specific requirements, or "this must happen before X" in `Why the original is off` or the rewrite itself unless Wolfgang explicitly frames them as a general rule. Do not promote terminology from a single rewrite, such as names for specific API modes, into a distilled rule unless it would help on an unrelated PR.

If the discussion changes the technical point of view, document that explicitly. Some feedback is not merely "make this sound more like Wolfgang"; it changes what the review should ask for, what guarantee is realistic, or whether the finding should be posted at all.

Treat this loop as a quality gate as well as a voice pass. If the review finding is false, overclaimed, or not worth posting, drop it and move on.

## What To Distill

Look for durable rules in these categories:

- Whether the comment should be posted at all.
- Whether the finding is blocking, should-address, optional, or not worth mentioning.
- How much mechanism to explain.
- Whether to lead with failure mode, concrete fix, or release/process decision.
- Whether the comment sounds like a generic review bot.
- Whether the tone is too formal, too exhaustive, too accusatory, or too indirect.
- Whether the comment should be shorter.
- How to handle evidence, local validation, line references, and suggestions.

Before appending a distilled rule, ask: "Would this rule help on an unrelated PR?" If not, keep it as comment-specific context. Do not preserve examples forever unless the user explicitly asks. The skill should evolve toward distilled rules, not a pile of anecdotes.

## Updating Downstream Artifacts

Only update `personas/wolfgang-voice.md` or `.claude/skills/wolfgang-voice-review/SKILL.md` when the user asks to distill/apply the notes, or when a batch is clearly complete and the user agrees.

When distilling:

1. Read all relevant entries from `wolfgang-voice-review-notes.md`.
2. Update `personas/wolfgang-voice.md` only for general voice rules.
3. Update `.claude/skills/wolfgang-voice-review/SKILL.md` for PR-review-specific filtering and rewriting rules.
4. Keep the final rules concise and operational.

## Recovery After Compaction

If context was compacted or the session restarted:

1. Read this skill.
2. Read `personas/wolfgang-voice.md`.
3. Read `wolfgang-voice-review-notes.md`.
4. Re-fetch the current PR comments if a PR is named in the notes or user prompt.
5. Resume at the next unprocessed comment.
