---
name: scope-anchor
description: "Write and maintain whole-scope recurring reminders for long-running priorities so the supervisor does not mistake a waiting substep for completion. Use when a priority spans multiple phases/PRs, the user asks how to keep a long-running task moving, or a reminder needs to preserve full scope and done conditions."
---

# Scope Anchor Skill

Use this skill for long-running priorities where ordinary PR/reminder pings can lose the full promise. A scope anchor is not a replacement for the reminders tool. It is the wording and operating pattern for a recurring reminder that keeps the supervisor oriented until the whole priority is done or explicitly descoped.

## When to create one

Create or refresh a scope anchor when any of these are true:

- A priority has multiple phases, PRs, deploys, migrations, or follow-up slices.
- The current subtask is waiting, but later scope remains.
- A user asks whether something is done and the answer depends on the full plan, not just the current PR.
- A scope mistake happened: a phase was forgotten, a follow-up was treated as optional without approval, or a reminder only tracked the next check.

## Reminder cadence

Default to hourly (`--every 1h`) for active long-running priorities. Use shorter intervals only when there is an active handoff or CI/review turn that genuinely needs it. Use longer intervals only when the priority is deliberately parked and the user would not expect movement sooner.

Keep the ordinary task/PR reminders too. The scope anchor answers: "Are we done with the whole thing?" PR reminders answer: "Did this specific PR move?"

## Required contents

A scope-anchor reminder must include:

1. **Priority name and source of truth** — ticket, RFC, plan, or user instruction.
2. **Full promised scope** — all phases/steps, not just the current one.
3. **Completed scope** — what is done, with PRs/commits if useful.
4. **Remaining scope** — every incomplete phase, follow-up, deploy gate, review gate, or decision.
5. **Done condition** — explicit sentence: "Not done until ..."
6. **Next-action rule** — what to do when the reminder fires.
7. **Preauthorization rule** — for this long-running priority, Wolfgang has implicitly preauthorized necessary local tools, installs, setup, worker launches, reversible runtime changes, and other ordinary execution steps needed to keep moving. Do not stop for permission unless the action is destructive, irreversible, materially out of scope, or would expose secrets/data.
8. **False-blocker rule** — if a blocker is missing tooling, local setup, unclear ceremony, or another solvable execution dependency, solve it and continue. Only wait on real external gates, unresolved product/scope decisions, safety/data-loss risks, or explicit human review/merge decisions.
9. **Waiting rule** — what counts as a legitimate wait, and what must still be rechecked.
10. **Descope rule** — only mark a remaining item out of scope if the user explicitly descopes it or the source of truth changes.

## Wording template

```text
Scope anchor: <PRIORITY/TICKET>. Source of truth: <RFC/PLAN/USER INSTRUCTION>.

Full scope:
1. <phase/step 1>
2. <phase/step 2>
3. <phase/step 3>
...

Completed:
- <completed step + evidence>

Remaining:
- <incomplete step + next expected action>

Done condition: not done until every listed step is complete, merged/deployed if required, or explicitly descoped by Wolfgang/source-of-truth update.

Preauthorization: Wolfgang has implicitly preauthorized necessary tooling, installs, setup, worker launches, reversible local/runtime changes, and ordinary execution steps needed to keep this priority moving. Do not wait for authorization unless the action is destructive, irreversible, materially out of scope, or risks secrets/data.

On firing: compare current state to the full scope. If a remaining step can move, start or route it. If blocked by missing tools/setup/permission ceremony, fix that and continue. If waiting, verify the wait is a real external gate or safety/scope decision, record evidence, and keep/re-arm the anchor. Do not report "done" because a current PR is green while later phases remain.
```

## Operating loop on each firing

1. Re-read the scope anchor before checking details.
2. Check the source of truth if there is any doubt.
3. Mark completed/remaining in supervisor state.
4. If the next remaining step is actionable, start it or delegate it.
5. If progress is blocked by missing tooling, local setup, install needs, worker launch needs, or permission ceremony, use the standing preauthorization to do the smallest safe thing and continue.
6. If the priority is legitimately waiting on a real external gate, unresolved scope/product decision, safety/data-loss concern, or human review/merge decision, record the blocker and keep the anchor alive.
7. If all steps are complete, say what evidence proves that and cancel the anchor.

## Anti-patterns

- Anchoring only the current PR and omitting later phases.
- Saying "no more PRs" without checking the source-of-truth phase list.
- Letting a one-shot verification reminder stand in for the whole-scope anchor.
- Waiting for Wolfgang to authorize ordinary tools, installs, setup, worker launches, or reversible local changes needed to continue an already-approved long-running task.
- Treating missing tooling or local setup as a hard blocker instead of fixing it.
- Marking work done because implementation is complete while reviews, merges, deploy gates, or later phases remain.
- Silently dropping remaining phases as "follow-ups" without explicit user/source-of-truth approval.
