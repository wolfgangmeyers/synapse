---
name: anxiety
description: Read-only companion monitoring for a target worker. Keeps approved requirements visible and reports real drift to the supervisor.
user_invocable: true
---

# Anxiety

Anxiety has two roles. Choose the role from the session that loaded this skill.

## Supervisor role

Use this role only when you are the supervisor.

1. Read the target's current worker state. Use its recorded window and worktree.
2. Launch exactly one managed companion named `<target>-anxiety` in that same worktree. Check for an existing companion first. Reuse it; do not create a duplicate.
3. Send the companion the full validated requirements. Include the target name, target window, target worktree, current task state, and the exact monitoring rules below.
4. Tell the companion to read the Mailbox and Reminders skills before monitoring. Tell it to list and reconcile its existing self-reminders before creating or reusing one: remove stale or duplicate monitor reminders and retain exactly one current recurring reminder in its own window every 10 minutes. The reminder body must contain the full requirements and the exact monitoring rules below, including the stop condition and cancellation instruction.
5. Verify the companion received the handoff. Do not launch another anxiety worker, helper, subagent, or recursive monitor.

Use the normal managed worker lifecycle and Mailbox V2 delivery path. The companion is an exception to ordinary implementation ownership: it shares the target worktree only for read-only inspection. It must not edit files, stage changes, commit, reset, run process control, or make any other worktree or runtime mutation.

## Companion role

Use this role only when you are the `<target>-anxiety` companion.

Read these first:

- `worker/SKILL.md`
- `mailbox/SKILL.md`
- `reminders/SKILL.md`

You monitor one target. You do not launch the target or any other worker. You do not delegate to subagents. You do not edit files, write task files, stage changes, commit, reset, run tests, review code, control processes, or change the target runtime. The shared worktree is read-only. The only control-plane exception is managing your own recurring reminder as required here.

First list your existing self-reminders and reconcile them before creating or reusing one: remove stale or duplicate monitor reminders, then retain exactly one current recurring Mailbox V2 reminder in your own window with `--every 10m`. Its body must include the complete current requirements, this exact instruction, and the stop condition and cancellation instruction below:

> Inspect the target's current work and responses against a requirement-to-evidence trace. When useful, safely capture the target tmux window and read-only worktree evidence. Ask targeted questions that identify the requirement served, the existing service or file being changed, the reason for any new service or expanded scope, the evidence of progress, the next concrete action, and blockers. Treat missing status alone as insufficient evidence of a problem. If actual work or an explanation violates a requirement, immediately Mailbox the supervisor with the violated requirement, observed evidence, target explanation, impact, and corrective decision or action needed. If the supervisor is blocking the target, send the supervisor the same kind of message with the blocking instruction and its impact. Stay read-only. Do not launch workers or mutate the worktree.

Use the current full requirements in the reminder body. Update the same reminder when the requirements or task state changes. Keep one reminder, not a new reminder per update. The reminder body must also say: cancel this recurring reminder when monitoring ends; do not leave it running after the target finishes, the task is stopped, or the supervisor ends monitoring. Delete it when monitoring ends.

## Monitoring loop

Keep a small requirement-to-evidence trace:

- requirement
- evidence expected
- evidence observed
- target explanation
- current disposition: on track, concern, or blocked

On each reminder or meaningful target message:

1. Read the current requirements and task state.
2. Inspect the target's tmux window with a read-only capture when that can reveal actual work. Read the target's mailbox responses and visible command output. Inspect read-only file or diff state only when it helps establish what changed.
3. Map the observation to the trace. Name the requirement it serves or misses.
4. Ask the target focused questions when the trace is incomplete or drift is possible:
   - Which approved requirement does this work serve?
   - Which existing service or file does it change?
   - Why is a new service or other expanded scope needed?
   - What concrete evidence shows progress on that requirement?
   - What is the next concrete action?
   - What is blocking it?
5. Do not send a generic status request. A quiet pane or missing update is not, by itself, a requirement violation. Treat it as a concern only when read-only evidence shows stalled work, an expected action is overdue, a target claims progress without supporting evidence, or the target's explanation conflicts with the requirements.
6. If the target is off track, send the supervisor one concise Mailbox message immediately. Include all five facts: the violated requirement, observed evidence, target explanation (or that none was provided), impact, and the corrective decision or action needed. For example, if the requirement says to extend an existing service and the target is building a new service, name the existing service requirement and the new-service evidence. Do not wait for another reminder.
7. If the supervisor blocks the target, send the supervisor a Mailbox message immediately. Include the supervisor's blocking instruction, the affected requirement, observed target state, impact, and the decision or action needed to unblock it. Do not silently work around the supervisor.
8. Send no acknowledgment-only messages. Batch related evidence. Do not repeat the same concern until new evidence, a changed explanation, or a required decision exists.

Mailbox messages go to the supervisor. Messages to the target should contain the focused question and the requirement it traces to. Reply only when there is new evidence, a blocker, a meaningful state change, or a decision request. When monitoring ends, cancel the recurring reminder and report the final trace or exact unresolved concern to the supervisor.

## Boundaries

This skill monitors. It does not implement, test, review, commit, or change the target. It does not create workers recursively. It does not turn a lack of status into a finding. Every escalation needs direct evidence and a named requirement.
