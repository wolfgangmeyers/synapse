---
name: subagent-orchestration
description: Use native/background subagents without handing off parent ownership. Load before a supervisor or worker delegates bounded work to subagents. This is not for managed worker launch or long-lived worker management.
---

# Subagent Orchestration

Use this when a parent agent is about to delegate work to native or background subagents.

This applies to supervisors and workers. It is about parent-to-subagent delegation inside the current lane. It is not about launching or managing long-lived workers.

## Rule

The parent owns the task.

Subagents handle bounded pieces. The parent keeps orchestration, state, approvals, integration judgment, and final validation.

Do not hand an entire workload to one subagent. Split the work into small observable questions. Delegate the parts that reduce context or unblock parallel checks, then integrate the results yourself.

## Use subagents for

- Bounded exploration of a repo, PR, log, test failure, or document set.
- Parallel reads where each subagent can inspect a separate area.
- Test and log triage with a clear failure or command output.
- Focused review passes by risk area.
- Alternative analysis when two approaches need independent evidence.
- Artifact verification: diff checks, generated output checks, posted-content checks, or command-output checks.
- Narrow implementation slices only when the mutation path and ownership are explicit.

## Do not use subagents for

- Whole-task handoff.
- Long-running implementation ownership.
- External approval, PR state, deployment, spending, credential, or lifecycle decisions.
- Work that needs continuous parent judgment.
- Stateful orchestration, reminders, mailbox replies, or user coordination.
- Mutation without exact paths, allowed commands, rollback expectations, and stopping rules.
- One giant prompt that asks a subagent to plan, implement, test, review, and decide readiness.

Use managed workers for durable implementation lanes. Use subagents for bounded pieces inside a lane.

## Decomposition pattern

1. State the parent-owned outcome.
2. State explicit non-goals.
3. Split the work into small questions with observable outputs.
4. Launch independent subagents in parallel when their work does not depend on each other.
5. Integrate the results in the parent context.
6. Verify claims against direct evidence.
7. Launch follow-up subagents only for specific remaining unknowns.
8. Make the final judgment yourself.

If a subagent result changes the plan, the parent decides what changes. The subagent does not become the owner.

## Parent responsibilities

The parent owns:

- Task context and current state.
- Decomposition and sequencing.
- Reminders and async follow-up.
- Mailbox replies and user-facing status.
- Approval gates.
- Conflict resolution between subagent findings.
- Final diff, artifact, or PR judgment.
- Final validation evidence.
- Irreversible or external decisions.
- Saving durable lessons to the KB or skills when the lesson is reusable.

## Prompt shape

A subagent prompt should include:

- Exact goal.
- Input paths, PRs, branches, commits, logs, or commands.
- Allowed commands.
- Mutation rules: read-only, exact files allowed, or no mutation.
- Non-goals.
- Time or turn bounds.
- Output schema.
- Evidence requirements.
- Stop conditions.

Good output schema:

```text
Findings:
- [claim] [evidence path/line/command]

Checked:
- [what files, commands, logs, commits, or artifacts were inspected]

Not checked:
- [explicit gaps]

Recommendation:
- [one bounded next step, or none]
```

Make the evidence small. Ask for paths, line numbers, command names, and short excerpts. Do not ask for transcripts unless the transcript itself is the artifact.

## Result handling

Treat subagent summaries as claims.

Verify the evidence before relying on them. Check enough source, diff, command output, log, PR state, or artifact content to confirm the claim. If subagents disagree, reconcile the disagreement in the parent context. Send a follow-up subagent only for the specific fact still unknown.

Do not copy large raw output into the parent context. Pull the minimum evidence needed for the parent judgment. If the full transcript matters, fetch it only for the section you need.

If a result teaches a reusable lesson, save the distilled version where it belongs: KB for project facts, a skill for workflow rules, or a review heuristic for review misses.

## Context budget

Subagents should reduce parent context load.

Do not have every subagent paste every file it read. Ask for conclusions, evidence pointers, and gaps. The parent can read the cited lines directly when needed.

## Common failure modes

- One giant subagent owns the whole task.
- Async subagents are launched with no reminder or poll path.
- The parent blocks synchronously and becomes unreachable.
- Duplicate subagents inspect the same thing and produce inconsistent state.
- Results are accepted without evidence.
- A subagent mutates outside its scope.
- A subagent does integration judgment that belongs to the parent.
- The parent pulls full transcripts into context by default.

## Good pattern

Parent outcome: review a risky PR before posting comments.

Non-goals: no GitHub writes, no code changes.

Subagents:

1. API contract check: read the route, request validation, and generated client diff. Report mismatches with file lines.
2. Persistence check: read migrations and data access paths. Report data-loss or backward-compatibility risk.
3. Test check: map changed behavior to tests. Report missing assertions and the command that should fail.

Parent then reads the cited lines, reconciles findings, runs or checks the relevant validation, and decides what feedback is real.

## Bad pattern

"Review this PR, decide whether it is safe, write the comments, and tell me when to approve it."

That hands off the parent job. Split it into focused checks, then make the decision in the parent thread.
