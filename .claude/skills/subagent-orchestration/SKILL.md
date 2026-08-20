---
name: subagent-orchestration
description: Delegate bounded work to native or background subagents with clear contracts, evidence gates, context limits, safe handoffs, and explicit stop rules.
---

# Subagent Orchestration

Load this skill before a worker or supervisor delegates work to a native or background subagent.
It covers short-lived subagents inside a task. It does not replace `worker`, `worker-launch`,
`supervisor`, `decompose`, or `workflows`.

## Ownership

The parent owns the task. A subagent owns only the bounded outcome in its handoff.

The parent keeps:

- task scope, requirements, and current state;
- sequencing and dependencies;
- integration and conflict decisions;
- approval, external, and irreversible gates;
- final validation and readiness judgment;
- user, supervisor, and mailbox communication.

A subagent supplies evidence for a decision. It does not decide that the whole task is ready, close
the task, manage reminders, reply to the user, or hand the entire workload to another agent.

A managed worker owns a durable implementation lane. That worker can orchestrate subagents for
repo exploration, narrow implementation slices, test triage, validation, or review. The worker
integrates their results and remains responsible for the lane.

## Choose the smallest useful shape

Use one subagent when the task is one bounded question or slice with one clear output. Examples:

- inspect one API contract and report mismatches;
- find the tests covering one behavior;
- run one focused validation command and explain a failure;
- implement one isolated helper with named files and tests.

Decompose a large batch into bounded steps when it has independent areas, sequential dependencies,
several distinct evidence needs, or enough context that one agent would lose the thread. Give each
step one owner and one observable result. Use `decompose` when the work needs independently shippable
phases. Use the canonical workflow from `workflows` when the task must follow a named workflow.

Do not decompose an atomic change into artificial phases. Do not ask one subagent to plan,
implement, test, review, and decide readiness for the whole task.

For a batch, write the dependency order explicitly:

```text
research contracts -> choose approach -> implement slice -> validate slice -> integrate -> final validation
```

Parallelize only steps that do not share mutable files, state, or decisions. Keep dependent steps
sequential. A later step must consume the earlier step's verified output, not its optimistic summary.

## Write a contract before launching

Every handoff states the smallest observable outcome and the non-goals. Include:

```text
Goal: one result the parent can inspect.
Inputs: exact files, commit, branch, logs, or commands.
Allowed work: read-only, named files, named commands, or a specific mutation.
Non-goals: what the subagent must leave alone.
Dependencies: required predecessor artifact or condition.
Evidence: output, test result, artifact path, or source line needed to prove the result.
Stop/escalate: the condition that ends the work or needs the parent.
Return: concise findings, evidence pointers, gaps, and one recommendation.
```

For a multi-step task, define the contract between each predecessor and successor:

```text
Predecessor produces: [named artifact or decision].
Successor may assume: [verified facts only].
Successor must prove: [next acceptance condition].
If missing or contradictory: stop and report the exact gap.
```

Do not hide requirements in a long prompt. Do not leave the subagent to infer the base revision,
file ownership, allowed commands, or what counts as done.

## Evidence gates

A result is a claim until the parent checks the evidence. Require direct, compact proof:

- source claims: path, symbol, and relevant line;
- test claims: exact command and meaningful output;
- artifact claims: the actual file or generated output that will be used;
- git claims: branch, commit, or immutable diff identity;
- runtime claims: the real request, process output, or observed state.

The parent accepts a step only when its contract is met, the evidence covers the acceptance
condition, and the change stayed within scope. Read the cited source or artifact yourself. Do not
accept a clean check over a scratch file when the shipped artifact is assembled elsewhere.

For mutations, use exact paths and a rollback method. Avoid overlapping writers. Commit before
reviewing or handing off a mutation when a stable target is needed. Reviewers and validators should
use that immutable commit or diff, not a mutable working tree.

If a check can be made to pass without testing the behavior, fix the check or add the missing
assertion. For important tests, prove the test fails when its assertion or relevant behavior is
removed. The parent decides whether the evidence is sufficient.

## Context and attention budget

Subagents exist to reduce context load, not to move a transcript into the parent window.

- Ask for conclusions, short evidence pointers, and explicit gaps.
- Do not request full file dumps or command transcripts unless they are the artifact.
- Give each subagent a separate focus area. Do not launch duplicate reads of the same question.
- Keep the parent focused on decisions, integration, and final checks.
- Pass verified artifacts and contracts to the next step. Do not resend the whole conversation.
- When a worker approaches its context limit, stop at a safe boundary and write a handoff with the
  current artifact, evidence, unresolved questions, and exact next action. Compact or resume only
  after the current step is settled. Never restart from memory.

A large result is a context problem. Ask the subagent to reduce it to the few facts that change the
parent's decision, then read the cited lines directly.

## Async execution and no polling loops

Launch independent subagents in the background by default so the parent stays reachable. Continue
with other unblocked work. Do not block the parent on a synchronous subagent call, and do not run a
sleep-and-poll loop to watch it.

Use the runtime's completion notification, mailbox message, or one scheduled reminder to trigger
inspection. If the tool requires retrieving a completed result, retrieve it once after the
completion signal. A queued message is not proof that it was read, and a completion notice is not
proof that the evidence is correct.

The reminder must name the artifact and next action. Cancel it when the result is received or the
subagent is stopped. Do not create repeated reminders that only report that work is still running.

## Safe handoffs

Before a successor starts, the parent verifies:

1. the predecessor reached its stated stop condition;
2. the output artifact exists at the expected path or commit;
3. the evidence supports the predecessor's claims;
4. the successor has the exact base, inputs, allowed files, and acceptance condition.

Keep file ownership disjoint whenever possible. If two steps must touch one file, sequence them and
hand off the latest immutable state. Never let a read-only auditor mutate the implementation tree.
Never use a subagent handoff to smuggle in authorization for a commit, push, external service,
production, deployment, IAM, deletion, or other gated operation.

A safe handoff is short:

```text
Completed: [artifact and exact revision].
Evidence: [commands, outputs, and paths].
Open: [only unresolved requirement or risk].
Next owner: [agent].
Next action: [one executable step].
```

## Stop, continue, or escalate

Stop the subagent when its contract is satisfied, when it reaches the stated stopping condition, or
when continuing would expand scope. Report the evidence and leave integration to the parent.

Continue with a conservative reversible choice when the issue is ordinary uncertainty, missing
polish, or local setup that does not prevent the requested artifact. Do not turn a subagent failure
into a whole-task blocker without checking the failure directly.

Escalate to the parent when:

- a requirement, external contract, security boundary, data-loss risk, or irreversible decision is
  unclear or contradicted;
- the required input or predecessor artifact is missing;
- the allowed mutation would overlap another owner or cannot be rolled back;
- the same failure persists after a direct diagnostic and one sound retry;
- the requested outcome cannot be proved with the available evidence.

An escalation names the exact condition, what was checked, the evidence, and the smallest decision or
next action needed. Do not send a vague "blocked" status. The parent decides whether to change the
plan, retry, reassign, or stop.

## Parent closeout

Before declaring the task complete, the parent:

1. reconciles every subagent result with direct evidence;
2. maps each changed file and output to a requirement or validated blocker;
3. checks the final target, not an earlier draft or scratch artifact;
4. runs the required tests, coverage, review, and live proof for the task;
5. records unresolved decisions and hands off only concrete next actions.

The parent owns the final answer. Subagent summaries are inputs to that answer, not a substitute for
it.

## Common failures

- One giant prompt hands off the whole task.
- A worker delegates ownership instead of delegating lifting.
- Parallel agents write the same files or inspect the same question.
- A successor starts from an unverified summary.
- A parent blocks synchronously or polls in a tight loop.
- Full transcripts consume the context budget.
- A subagent makes a readiness, approval, or external-service decision.
- A blocker is reported without a requirement, contract, or direct evidence.
