---
name: supervisor-sweep
description: Craft and maintain the supervisor general sweep reminder so each sweep captures active work, blockers, open-PR feedback, CI, and merge-conflict checks. Use when creating, repairing, or evaluating the supervisor's general sweep reminder.
---

# Supervisor Sweep Skill

Use this skill to write and maintain the supervisor's recurring general sweep reminder. The reminder is the backstop for ongoing work. It must tell the supervisor exactly what to check and what action to take.

## Purpose

The general sweep makes sure active work keeps moving. It is not a status report. Each sweep must inspect current work, find anything blocked, and unblock or route it when the next safe action is available.

## Reminder cadence

The skill default is an exact two-hour general sweep cadence (7_200_000 ms). On each machine, `~/AGENTS.md` is the source of truth for the actual cadence; read it before creating or repairing the sweep reminder.

Maintain exactly one scheduled recurring general sweep reminder for the supervisor at that machine cadence. Repair it if it is missing, duplicated, failed, or stale. Do not create task-specific reminders that duplicate the general sweep unless a specific active risk needs its own reminder.

On each machine, `~/AGENTS.md` is also the source of truth for sweep scope exclusions. Read it before creating or repairing the sweep reminder.

## Dependabot PR exclusion

Do not include Dependabot PRs in the general sweep, `pr-watch` triage, or any agent-driven review/approval workflow. Skip PRs authored by `dependabot[bot]` or `app/dependabot` without fetching feedback, running robot review, approving, or routing workers to them. Dependabot updates are handled by team scripts and merged separately; agent review there is wasted tokens.

## Required reminder contents

The general sweep reminder must include these instructions:

1. Read this `supervisor-sweep` skill before executing the sweep.
2. Check supervisor state, live worker windows, mailbox, reminders, and active/open PRs. Skip Dependabot PRs per the exclusion above.
3. For each ongoing task, decide whether it is moving, parked on a real external gate, or blocked by something the supervisor can fix or route.
4. Unblock actionable work. Use the smallest safe local action, worker redirect, subagent check, or reminder repair that moves the task forward.
5. For pushed open PRs waiting for merge, go-ahead, CI, or feedback — excluding Dependabot PRs — always run the big three checks:
   - **Feedback**: fetch human and bot feedback, including top-level comments and inline review threads. Validate feedback before accepting it. Implement valid feedback or route it to a worker. Reply to invalid bot feedback with the reason it is invalid; internal disposition alone is not enough. When directing a worker to implement feedback, remind it to update robot-review heuristics for reusable review lessons and the KB for reusable domain knowledge.
   - **CI failures**: inspect failing or pending checks directly. Route failures to the owning worker with the failing job, expected head, and closest local validation command.
   - **Merge conflicts**: check mergeability, behind-state, and conflict signals. Track them and route conflict resolution to the owning worker.
6. Report action evidence or one exact real blocker. Do not reply with generic status, acknowledgments, or “already on it.”

## Wording template

```text
General supervisor sweep: read `.claude/skills/supervisor-sweep/SKILL.md`, then execute the sweep. Check supervisor state, live worker windows, mailbox, reminders, and active/open PRs to make sure ongoing work is not blocked. Skip Dependabot PRs (`dependabot[bot]`, `app/dependabot`); team scripts handle those merges. Unblock actionable work with the smallest safe local action, worker redirect, subagent check, or reminder repair.

For pushed open PRs waiting on merge, go-ahead, CI, or feedback — excluding Dependabot PRs — always check the big three: (1) human/bot feedback, including top-level comments and inline review threads; validate it, implement valid feedback or route it to the owning worker, reply to invalid bot feedback with the reason, and when routing workers remind them to update robot-review heuristics for reusable review lessons and the KB for reusable domain knowledge; (2) CI failures; inspect direct check evidence and route fixes to the owning worker; (3) merge conflicts/behind-state; track and route conflict resolution.

Report action evidence or one exact real blocker. Do not send generic status or acknowledgments.
```

## Sweep loop

1. Read the current reminder body and this skill.
2. Read supervisor state and active priorities.
3. Check live worker windows only where worker state needs direct evidence.
4. List scheduled reminders and repair missing, duplicate, stale, or overly broad/narrow sweep coverage.
5. For each active task, record the next action, owner, and real blocker if any.
6. For each open PR except Dependabot PRs, check feedback, CI, and mergeability directly.
7. Route actionable work to workers or bounded subagents. Include the feedback/CI/conflict evidence and the required KB/robot-review-heuristics instruction when feedback is routed.
8. Update state with what changed.
9. If nothing is actionable, record the verified external wait and keep the sweep reminder active.

## Anti-patterns

- Treating the sweep as a status digest.
- Checking only PR heads while skipping feedback, CI, or mergeability.
- Treating unvalidated bot feedback as true.
- Silently closing invalid bot feedback without replying with the reason.
- Routing review feedback without telling the worker to capture reusable lessons in robot-review heuristics or KB.
- Leaving a task blocked on local setup, stale process state, missing tooling, or unclear ceremony when the supervisor can safely fix or route it.
- Creating duplicate process reminders for checks already covered by the general sweep.
- Reviewing, approving, or routing workers for Dependabot PRs during the sweep or `pr-watch` triage.
