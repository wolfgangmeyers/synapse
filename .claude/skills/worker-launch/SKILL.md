---
name: worker-launch
description: Managed worker launch, resume, follow-up handoff, parking, shutdown, and Mailbox V2 coordination guidance for Wolfgang's supervisor workflow. Use when starting, adopting, relaunching, redirecting, parking, compacting, or handing additional work to managed workers.
---

# Worker Launch Skill

Use this skill from the supervisor when a task needs a durable managed worker or when an existing worker needs a resume, redirect, or follow-up handoff. The supervisor owns state, reminders, verification, and authorization gates. Workers own implementation, validation evidence, and completion mail.

## Non-negotiables

When a Pi worker needs to send a message, call `mecha_mailbox_send({ recipient, body, subject? })`; the result includes the accepted message ID and state. The native tool is send-only and cannot execute shell commands. Keep the script examples below for launcher-side fallback sends.

- Launch and restore workers only through `~/.claude/skills/supervisor/tools/worker_launch.py`.
- Coordinate through Mailbox V2. In Pi sessions with the Mailbox extension, use the native top-level `mecha_mailbox_send` tool first. Use `mailbox_send.py` as the fallback for Claude sidecars, bare Codex, older Pi sessions, or supervisor tooling that must run outside the model session. Bare Codex and Cursor launched by `worker_launch.py` do not have launcher-provided Mailbox delivery; use Pi/Claude or install and verify a separate bridge. Do not type into worker terminals or tmux panes.
- Use `worker_park.py`, `worker_shutdown.py`, and `worker_kill.py` for lifecycle actions. Do not send ad hoc park/exit text.
- Keep `TASK.md` task-specific. Generic worker behavior belongs in `worker/SKILL.md`, not in each task.
- Read worker state before acting. Use the recorded `worktree`, `branch`, `window`, `agent`, and `session`; do not infer them from memory.
- Set or keep reminder coverage for active work, then cancel or downgrade it when it stops protecting an active risk.

## Shell fallback when launch tooling fails

Run the launcher from a shell with an explicit interpreter and absolute tool path:

```bash
python3 ~/.claude/skills/supervisor/tools/worker_launch.py \
  --window "$WORKER_NAME" --dir "$WORKTREE_PATH" --agent pi --skip-permissions
```

Before retrying, check `python3 .../worker_launch.py --help`, the launcher stderr, and the target pane with `worker_capture.py`. The launcher must create the tmux window, apply the runtime configuration, and establish the supported Mailbox path. Do not replace it with `tmux new-window`, `pi`, `claude`, `codex`, or `cursor` commands typed by hand: those bypass state, identity, trust setup, and possibly Mailbox delivery. If the installed `~/.claude` path is stale, invoke the canonical checkout copy at `/Users/wolfgang/code/mecha-developer/.claude/skills/supervisor/tools/worker_launch.py` and repair the install separately.

`mailbox_send.py` is the shell-invoked delivery fallback when the native `mecha_mailbox_send` tool is unavailable:

```bash
python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \
  --to "$WORKER_NAME" --body "$BODY"
```

It requires a reachable loopback Mailbox V2 router and an agent-ID recipient. The sender is inferred from the current tmux window; pass `--from` only for a verified launcher-side or service identity. It queues a message but does not prove the runtime consumed it. Bare Codex and Cursor still need a separately verified delivery bridge.

These scripts are not worker-launch fallbacks: `run_router.sh` starts the local router, `cron-bootstrap.sh` supplies cron's shell environment, `watchdog-agent.sh` runs the optional watchdog, and `claude-activity.sh` is a Claude hook. Use each only for its named service. Do not run `run_router.sh` against an unknown or shared database, and do not print or place tokens in command lines, logs, task files, or documentation.

## Launch a new implementation worker

```bash
cd <project-path>
git fetch origin main

REPO_DIR=$(basename "$(pwd)")
PARENT_DIR=$(dirname "$(pwd)")
ALIAS="<short-descriptive-task-alias>"
WORKTREE_PATH="$PARENT_DIR/$REPO_DIR-$ALIAS"
BRANCH="wolfgang/$ALIAS"
WORKER_NAME="$ALIAS"

git worktree add -b "$BRANCH" "$WORKTREE_PATH" origin/main

python3 ~/.claude/skills/supervisor/tools/worker_launch.py \
  --window "$WORKER_NAME" \
  --dir "$WORKTREE_PATH" \
  --agent pi \
  --notify-on-settled supervisor \
  --skip-permissions

PI_SESSION_ID="<stable Pi session id shown in worker_launch.py's suggested state_add command>"
python3 ~/.claude/skills/supervisor/tools/state_add.py \
  --alias "$ALIAS" \
  --type worker \
  --agent pi \
  --session "$PI_SESSION_ID" \
  --project "<project-path>" \
  --window "$WORKER_NAME" \
  --branch "$BRANCH" \
  --worktree "$WORKTREE_PATH" \
  --description "<what the worker is doing>" \
  --purpose "<why this task exists>" \
  --requirements "<detailed requirements if available>"

python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window supervisor \
  --every 30m \
  --message "Drive <primary usable artifact> to completion. Inspect actual artifact evidence from ${ALIAS}: build/run/check output, not planning or adjacent work. If the artifact is untouched or not runnable when it should be, redirect the next vertical slice. Cancel this recurrence only when the artifact is delivered, Wolfgang defers it, the worker is parked on a real external/environment decision, or the worker is shut down."
python3 ~/.claude/skills/supervisor/tools/state_update_field.py "$ALIAS" supervisor_reminder_id "<reminder-id>"
```

Window name must match the task alias and must be recognizable. Avoid generic names such as `worker-1`, `test-window`, or branch-only slugs. Use short task nouns and include the ticket or PR number when one exists.

After the agent registers, send the task through Mailbox V2 when the runtime has verified delivery. The supervisor handoff must explicitly encourage bounded background subagents when useful. Reason: subagents keep the worker's main context smaller, reduce compaction frequency, preserve continuity on long tasks, and keep the worker responsive to supervisor direction instead of blocking inside long-running reads, tests, or analysis.

```bash
BODY=$(cat <<'EOF'
Read ~/.agents/skills/worker/SKILL.md first. Your task: <TASK DESCRIPTION>.

Use bounded background subagents proactively for focused exploration, implementation slices, validation/log triage, and robot-review where useful. Keep them bounded with clear outcomes/non-goals and retain final judgment. This keeps your main context small, reduces compaction churn, preserves task continuity, and keeps you reachable for direction changes while long-running work happens in the background.

If this may run longer than five minutes, create the two required recurring Mailbox V2 self-reminders before substantive work:

python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window self \
  --every 30m \
  --message "Drive <primary usable artifact> to completion [by <deadline>]. Execute the next atomic action toward a runnable vertical slice now. Do not report status, plan, or count supporting work as progress. If a blocker does not prevent the artifact, defer it or use the conservative default and continue."

python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window self \
  --every 30m \
  --message "Send the supervisor artifact evidence for <primary usable artifact>: files changed, build/run/check state, validation, next executable action toward completion, and exact blockers classified as hard or deferred."

Do not use placeholder or generic reminder messages. Mail me both IDs from the command output lines (`Reminder <id> is scheduled ...`). Reply by mail with blockers, meaningful checkpoints, validation results, and completion evidence.
EOF
)
python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \
  --to "$WORKER_NAME" \
  --body "$BODY"

# After the worker mails both self-reminder IDs:
python3 ~/.claude/skills/supervisor/tools/state_update_field.py "$ALIAS" worker_self_reminder_ids "<artifact-reminder-id>,<status-reminder-id>"

python3 ~/.claude/skills/supervisor/tools/worker_send_requirements.py --alias "$ALIAS"
```

For production-service changes, explicitly instruct the worker to load the `workflows` skill and fetch `dev-loop` with `get_workflow.py dev-loop`. The canonical workflow is the source of truth. Do not paste a summary of it into the task.

For feature or bug work, point Context at `~/.mecha-developer/kb/start-here.md`. The worker must read that page and its always-read links before planning. Do not paste those topics into the task.

For non-production work, the fallback loop is: tests or checks first where meaningful, implement, run focused validation, run focused robot-review, address valid findings, then mail evidence.

## Runtime-specific launch notes

`worker_launch.py` accepts explicit `--agent pi|claude|codex|cursor`; the machine runtime config supplies the default when `--agent` is omitted. Record the actual agent in supervisor state.

Codex workers need especially explicit launch-time handling because they do not support Claude Code skill slash commands such as `/review-pr`, and the current launcher starts bare Codex without a Mailbox V2 delivery sidecar or initial prompt injection. Mail sent to a Codex worker may queue but never reach the pane unless a separate delivery mechanism is installed and verified. Do not launch a fresh Codex worker for work that depends on post-launch Mailbox delivery; prefer Pi or Claude. Use Codex only when the session already has the needed task context, or after you have installed and verified a separate delivery bridge. Use plain skill names such as `robot-review` instead of `/robot-review`, and make sure the reachable task context includes exact paths, acceptance criteria, test/check commands, source branch and push target when relevant, and a clear completion trigger. For existing PR fixes, tell Codex to push with `git push origin HEAD:<source-branch>` only through a context path proven to reach the Codex session.

Do not ask Codex for an upfront plan unless the task truly needs a planning phase. If the canonical `dev-loop` applies, tell it to load the `workflows` skill and fetch the workflow record directly.

Detailed Codex launch/session/config rules:

- Launch with `--agent codex`; `worker_launch.py` always runs Codex with `--no-alt-screen`. With the launcher default `--skip-permissions`, it also adds non-interactive approvals and the configured full-access sandbox; if launched with `--no-skip-permissions`, those approval/sandbox flags are not added.
- Record `--agent codex` in `state_add.py` so future relaunches use the same runtime.
- Codex resumes with `codex resume <SESSION_UUID>`; verify the current Codex session ID/source before recording or relaunching it.
- Mailbox delivery for Codex is not provided by `worker_launch.py`; do not assume supervisor mail reaches the Codex pane unless you have independently installed and verified a delivery bridge.
- Codex reads skills from `~/.agents/skills/` and must be told to read skill files directly.
- Before relying on native Codex subagents, verify the current Codex capability/configuration in that session. If unavailable, the supervisor owns robot-review and validation. Relay validated findings only through a verified delivery bridge, a relaunched prompt/task file, or another mechanism proven to reach the Codex pane; do not assume Mailbox V2 delivery works for bare Codex.

## TASK.md content

Use `TASK.md` when the instruction is long or needs reviewable detail. Keep it task-specific.

Every `TASK.md` must have exactly four required sections total, in this order. Do not add optional extra top-level sections:

```markdown
## Context
<project path, branch, worktree, and relevant background for this task>

## Task
<requirements only — no boilerplate worker rules>

## Completion Protocol
<status-report cadence, exact test/check commands, focused review requirements, commit/push/PR instructions if authorized, completion mail format, and explicit instruction to park and wait>

## If Compacted
Re-read this TASK.md. Check the compaction summary for the last completed step. Resume from the next incomplete step. If the summary does not explicitly name the last completed step or current phase, mail the supervisor immediately — do not guess and do not restart from step 1.
```

All four sections are required. Missing `Context` makes a resumed worker guess at branch/worktree/background. Missing `Task` separates the worker from the actual requirements. Missing `Completion Protocol` makes workers finish without the required checks, completion mail, or park/wait behavior. Missing `If Compacted` makes workers stop, repeat work, or restart from the wrong step after compaction.

Do not include generic rules such as use subagents, maintain todos, mail if stuck, or do not push. Those live in `worker/SKILL.md` unless this task needs a task-specific override.

## Verify launch

Always verify the worker actually started after launch or resume:

```bash
python3 ~/.claude/skills/supervisor/tools/worker_capture.py --window "$WORKER_NAME" --tail 20
```

Look for active tool calls, file reads, or task acceptance. A shell prompt, empty pane, startup error, or UI auto-suggestion is not evidence the worker accepted the task. If startup failed, use `worker_kill.py`, diagnose, relaunch, and verify again.

## Post-delegation reflection

After every launch or follow-up handoff, ask whether the worker has everything needed to finish without coming back: requirements, acceptance criteria, test/check commands, review expectations, edge cases, repo-specific context, and any standing orders. If you find a gap, send a follow-up immediately through the runtime's verified delivery path instead of waiting for the omission to surface as a blocker.

## Reminder coverage

For any worker expected to run longer than five minutes, set a 30-minute recurring supervisor reminder at launch. The reminder must name the concrete artifact and require direct artifact evidence, not generic activity. Worker self-reminder handshakes require a runtime with verified Mailbox delivery; do not assign that safety contract to bare Codex/Cursor without a verified bridge.

```bash
python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window supervisor \
  --every 30m \
  --message "Drive <primary usable artifact> to completion. Inspect actual artifact evidence from <alias>: build/run/check output, not planning or adjacent work. If the artifact is untouched or not runnable when it should be, redirect the next vertical slice. Cancel this recurrence only when the artifact is delivered, Wolfgang defers it, the worker is parked on a real external/environment decision, or the worker is shut down."
```

The command prints `Reminder <id> is scheduled for supervisor at <timestamp>`. Record that supervisor reminder ID in the task state immediately, for example:

```bash
python3 ~/.claude/skills/supervisor/tools/state_update_field.py "$ALIAS" supervisor_reminder_id "<reminder-id>"
```

Do not rely on memory or only on the router reminder list; the active task state must show which supervisor reminder protects the work.

Mandatory worker reminder handshake: for every worker expected to run longer than five minutes on a runtime with verified Mailbox delivery, the launch instruction must require the worker to create and report two Mailbox V2 self-reminder IDs before substantive work. Do not assign this self-reminder handshake to bare Codex/Cursor without a verified delivery bridge. Give the worker this command shape:

```bash
python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window self \
  --every 30m \
  --message "Drive <primary usable artifact> to completion [by <deadline>]. Execute the next atomic action toward a runnable vertical slice now. Do not report status, plan, or count supporting work as progress. If a blocker does not prevent the artifact, defer it or use the conservative default and continue."

python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window self \
  --every 30m \
  --message "Send the supervisor artifact evidence for <primary usable artifact>: files changed, build/run/check state, validation, next executable action toward completion, and exact blockers classified as hard or deferred."
```

Each successful command prints `Reminder <id> is scheduled for <target> at <timestamp>`. The worker must mail both IDs to the supervisor before starting substantive work. The supervisor must verify both IDs arrived by Mailbox and record them in task state while the task still needs active worker momentum, for example:

```bash
python3 ~/.claude/skills/supervisor/tools/state_update_field.py "$ALIAS" worker_self_reminder_ids "<artifact-reminder-id>,<status-reminder-id>"
```

Do not substitute a generic keep-working or status reminder. Replace or delete old reminders when the policy changes or when the task parks, completes, or no longer needs that safety check.

## Resume, import, and relaunch

### Import an existing Pi session

Use this when adopting ongoing work into supervisor management.

```bash
WORKER_NAME="<descriptive-alias>"
python3 ~/.claude/skills/supervisor/tools/worker_launch.py \
  --window "$WORKER_NAME" \
  --dir "<project-or-worktree-path>" \
  --agent pi \
  --resume "<session-id-or-path>" \
  --notify-on-settled supervisor \
  --skip-permissions

python3 ~/.claude/skills/supervisor/tools/state_add.py \
  --alias "<alias>" \
  --type worker \
  --agent pi \
  --session "<session-id-or-path>" \
  --project "<project-path>" \
  --description "<what the session is working on>" \
  --purpose "<why this work matters>" \
  --window "$WORKER_NAME"

python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> branch "<current branch>"
python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> worktree "<worktree path>"
python3 ~/.claude/skills/supervisor/tools/state_update_status.py <alias> "Imported from existing session"
```

After registration, orient it by mailbox when the runtime has verified delivery. Tell it to read `worker/SKILL.md`, identify the current task and next action, and reply by mail once resumed. If it may run longer than five minutes, set the supervisor reminder and, for runtimes with verified delivery, require/report the two worker self-reminder IDs before substantive work. Record `supervisor_reminder_id` and `worker_self_reminder_ids` in state. If no session ID exists, do not open an interactive picker or type in a terminal. Ask for the ID or inspect managed session records.

### Relaunch a worker for an existing task

Relaunch into the recorded worktree. Do not create a new worktree for an existing open PR or task.

```bash
python3 ~/.claude/skills/supervisor/tools/state_query.py --alias <alias>

WORKER_NAME="<same-or-new-descriptive-window-name>"
RECORDED_AGENT="<agent-from-state>"
RECORDED_SESSION="<session-from-state-if-available>"
LAUNCH_ARGS=(--window "$WORKER_NAME" --dir "<worktree-path-from-state>" --agent "$RECORDED_AGENT" --skip-permissions)
[ -n "$RECORDED_SESSION" ] && LAUNCH_ARGS+=(--resume "$RECORDED_SESSION")
if [ "$RECORDED_AGENT" = "pi" ]; then
  LAUNCH_ARGS+=(--notify-on-settled supervisor)
fi
python3 ~/.claude/skills/supervisor/tools/worker_launch.py "${LAUNCH_ARGS[@]}"

python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> window "$WORKER_NAME"
python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> agent "$RECORDED_AGENT"
[ -n "$RECORDED_SESSION" ] && python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> session "$RECORDED_SESSION"

python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window supervisor \
  --every 30m \
  --message "Drive <primary usable artifact> to completion. Inspect actual artifact evidence from <alias>: build/run/check output, not planning or adjacent work. Cancel this recurrence only when the artifact is delivered, Wolfgang defers it, the worker is parked on a real external/environment decision, or the worker is shut down."
python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> supervisor_reminder_id "<reminder-id>"

# Only when the recorded runtime has verified Mailbox delivery:
python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \
  --to "$WORKER_NAME" \
  --body "You are resuming work on <task/PR>. Branch: <branch>. Worktree: <worktree-path>. Next action: <specific next action>. Reply by mail with evidence or blockers."

python3 ~/.claude/skills/supervisor/tools/worker_send_requirements.py --alias <alias>

# After the worker mails both self-reminder IDs:
python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> worker_self_reminder_ids "<artifact-reminder-id>,<status-reminder-id>"
```

Run the Mailbox commands above only for runtimes with verified delivery; `worker_send_requirements.py` also sends through Mailbox V2. If no session exists in state, omit `--resume` only after recording why the task has no resumable session. After any relaunch, keep the reminder coverage current: if `supervisor_reminder_id` is missing or stale, create the supervisor recurrence and record it; if the runtime has verified delivery and `worker_self_reminder_ids` is missing or stale, require the worker to create/report the two self-reminders and record them. If using `--resume`, always send a kick message after launch for runtimes with verified delivery. A resumed worker can sit at a prompt until told what to continue; for bare Codex/Cursor, use only a verified delivery bridge or a relaunch context proven to reach the session.

### Recover workers after supervisor or tmux disruption

`worker_recover_all.py` is only the process/session recovery step; it does not prove the task is moving again. After running it, inspect every recovered alias and apply the same reminder and handoff coverage as relaunch:

```bash
python3 ~/.claude/skills/supervisor/tools/worker_recover_all.py
python3 ~/.claude/skills/supervisor/tools/worker_list.py
python3 ~/.claude/skills/supervisor/tools/state_query.py --alias <alias>

python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window supervisor \
  --every 30m \
  --message "Drive <primary usable artifact> to completion. Inspect actual artifact evidence from <alias>: build/run/check output, not planning or adjacent work. Cancel this recurrence only when the artifact is delivered, Wolfgang defers it, the worker is parked on a real external/environment decision, or the worker is shut down."
python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> supervisor_reminder_id "<reminder-id>"

# Only when the recovered runtime has verified Mailbox delivery:
python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \
  --to "<window-from-state>" \
  --body "Recovered your worker session for <task/PR>. Read ~/.agents/skills/worker/SKILL.md, continue from the current task state, recreate/report your two self-reminders if needed, and reply by mail with the next concrete artifact evidence."
python3 ~/.claude/skills/supervisor/tools/worker_send_requirements.py --alias <alias>

# After the worker mails both self-reminder IDs:
python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> worker_self_reminder_ids "<artifact-reminder-id>,<status-reminder-id>"
```

For bare Codex/Cursor, do not send the Mailbox commands unless a separate delivery bridge is verified. Use a proven context path or relaunch with task context that reaches the session, then record how the worker received the handoff.

## Follow-up handoffs and interventions

Use Mailbox V2 for every follow-up, redirect, correction, added task, or recovery handoff only when the target runtime has verified delivery. For bare Codex/Cursor, use a verified delivery bridge or relaunch context proven to reach the session.

Additive work:

```bash
python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \
  --to "$WORKER_NAME" \
  --body "After completing the current task, also do: <additional work>. Reply by mail when done."
```

Correction:

```bash
python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \
  --to "$WORKER_NAME" \
  --body "Stop. <specific correction>. Reply by mail with the new plan or result."
```

New task for an idle worker:

```bash
python3 ~/.claude/skills/supervisor/tools/worker_capture.py --window "$WORKER_NAME" --tail 10
# write task-specific TASK.md in the project/worktree if useful
python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \
  --to "$WORKER_NAME" \
  --body "New task in TASK.md. Read it and execute it. Reply by mail when done."
```

Before redirecting work into a new component, shared runtime, protocol, deployment, or verification framework, name the acceptance criterion and direct evidence that the existing path failed. If neither exists, keep the worker on the existing path and finish.

## Compact or hand off worker context

Only compact at natural phase boundaries: the worker is idle, the previous phase is committed or settled, and the next phase is clear. Do not compact mid-task.

```bash
python3 ~/.claude/skills/supervisor/tools/worker_compact.py --window "$WORKER_NAME"
python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \
  --to "$WORKER_NAME" \
  --body "Phase <N> is done. Next: <description and needed carried-forward context>. Reply by mail when resumed."
```

For a deterministic clean-slate handoff, write a handoff file with bootstrap pointers, in-flight work, pending decisions, and next action. Then deliver it by Mailbox V2:

```bash
python3 ~/.claude/skills/supervisor/tools/handoff.py --handoff-file PATH --window "$WORKER_NAME"
```

If the worker is unavailable, restore its recorded session with `worker_launch.py` before sending the handoff pointer.

## Completion, parking, and cleanup

Workers mail completion and then wait. They never kill themselves.

On completion mail, park first:

```bash
python3 ~/.claude/skills/supervisor/tools/worker_park.py --window "$WORKER_NAME" --alias "$ALIAS"
```

Then verify before accepting the park as final:

1. Read the task requirements and completion evidence.
2. For iterative tasks, verify the termination condition directly, not just the worker's summary.
3. Ask the worker for missing evidence before reconstructing from a terminal tail. Every question must say `Reply by mail.` Useful prompts: `Did you run test-coverage and robot-review? Coverage, reviewer names, findings, sign-off status. Reply by mail.` `Which robot-review focus areas were covered, and why was any relevant area skipped? Reply by mail.` `Did tests and lint actually run? Results? Reply by mail.`
4. Map every changed file to an acceptance criterion or validated blocker.
5. Run or delegate the required review checks.
6. Mandatory after parking: launch a background subagent to read the explain skill and run its overview mode on the worker's changes against the base branch. For Claude-only environments this may be `/explain --overview`; otherwise use the equivalent plain-language skill invocation. It must evaluate Decisions/Questions against task requirements and `personas/wolfgang.md` principles. Valid concerns mean unpark and send the worker back. Do not run explain in the supervisor's main context.
7. If anything is missing, unpark or redirect the worker immediately by Mailbox V2.

Default to parking while a task or PR can still need review fixes. Kill only when the work is definitively closed or when freeing resources while preserving the task and worktree state.

Shutdown frees resources; it does not complete the task:

```bash
python3 ~/.claude/skills/supervisor/tools/worker_kill.py --window "$WORKER_NAME" || exit 1
python3 ~/.claude/skills/supervisor/tools/state_update_field.py <alias> window "(none — worker exited)"
```

For verified final shutdown:

```bash
python3 ~/.claude/skills/supervisor/tools/worker_shutdown.py --window "$WORKER_NAME" --verified
```

If `worker_shutdown.py` or `worker_kill.py` exits non-zero, resolve the mailbox sidecar before updating state. The tool output names the case:

- `could not confirm`: retry the tool; the process table could not be read.
- `survived SIGKILL`: re-running will not help. Kill only the printed PIDs, then confirm they are gone.

Use the scoped pattern the tool prints, such as `pgrep -fl 'claude_sidecar.mjs --agent-id <window> '`. Do not run a bare `pgrep -fl claude_sidecar.mjs` and kill every result; that cuts mailbox delivery for unrelated live Claude workers.

Only remove a worktree after the PR is merged or the task is explicitly abandoned. Before removing or stashing a dirty worktree, classify every tracked and untracked source/artifact change as committed/merged, intentionally abandoned, or still needed.

## Abandon a task

Use abandon only when there is no intent to continue the task.

1. Shut down the worker with `worker_shutdown.py`. If it exits non-zero, resolve the mailbox sidecar before marking anything abandoned.
2. Run the dirty worktree cleanup gate above. If source or artifact changes remain and are not explicitly classified as abandoned, do not remove the worktree.
3. Remove the worktree only after that classification:

```bash
cd <project-path>
git worktree remove --force "$WORKTREE_PATH"
```

4. Mark state abandoned:

```bash
python3 ~/.claude/skills/supervisor/tools/state_update_state.py <alias> abandoned --reason "<reason>"
```

## Existing PR fixes

For review feedback, lint, or CI fixes on an existing PR, fetch the PR head, create a worktree, identify the source branch, and tell the worker to push back to that branch.

```bash
cd <project-path>
PR_NUMBER=<pr-number>
BRANCH_NAME="pr-${PR_NUMBER}-fix"

git fetch origin pull/${PR_NUMBER}/head:${BRANCH_NAME}
WORKTREE_PATH="<project-path>-pr${PR_NUMBER}"
git worktree add "$WORKTREE_PATH" "$BRANCH_NAME"

SOURCE_BRANCH=$(gh pr view $PR_NUMBER --json headRefName -q .headRefName)
WORKER_NAME="<descriptive-name>"

python3 ~/.claude/skills/supervisor/tools/worker_launch.py \
  --window "$WORKER_NAME" \
  --dir "$WORKTREE_PATH" \
  --agent pi \
  --notify-on-settled supervisor \
  --skip-permissions

PI_SESSION_ID="<stable Pi session id shown in worker_launch.py's suggested state_add command>"
python3 ~/.claude/skills/supervisor/tools/state_add.py \
  --alias "$WORKER_NAME" \
  --type worker \
  --agent pi \
  --session "$PI_SESSION_ID" \
  --project "<project-path>" \
  --window "$WORKER_NAME" \
  --branch "$BRANCH_NAME" \
  --worktree "$WORKTREE_PATH" \
  --pr "https://github.com/<org>/<repo>/pull/${PR_NUMBER}" \
  --description "Fix review/CI feedback for PR #${PR_NUMBER}" \
  --purpose "Keep the existing PR moving without opening a duplicate branch"

python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window supervisor \
  --every 30m \
  --message "Drive PR #${PR_NUMBER} review/CI fix to completion. Inspect actual artifact evidence from ${WORKER_NAME}: diff, pushed head, build/run/check output, not planning or adjacent work. Cancel this recurrence only when the fix is delivered, Wolfgang defers it, the worker is parked on a real external/environment decision, or the worker is shut down."
python3 ~/.claude/skills/supervisor/tools/state_update_field.py "$WORKER_NAME" supervisor_reminder_id "<reminder-id>"

BODY=$(cat <<EOF
Read ~/.agents/skills/worker/SKILL.md first. Fix <issue description> in PR #${PR_NUMBER}. Push updates with: git push origin HEAD:${SOURCE_BRANCH}.

If this may run longer than five minutes, create the two required recurring Mailbox V2 self-reminders before substantive work:

python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window self \
  --every 30m \
  --message "Drive PR #${PR_NUMBER} review/CI fix to completion. Execute the next atomic action toward a runnable, pushable fix now. Do not report status, plan, or count supporting work as progress. If a blocker does not prevent the fix, defer it or use the conservative default and continue."

python3 ~/.claude/skills/reminders/tools/reminders.py set \
  --window self \
  --every 30m \
  --message "Send the supervisor artifact evidence for PR #${PR_NUMBER}: files changed, build/run/check state, validation, next executable action toward completion, and exact blockers classified as hard or deferred."

Mail me both IDs from the command output lines (Reminder <id> is scheduled ...). Reply by mail with validation and review evidence.
EOF
)
python3 ~/.claude/skills/supervisor/tools/mailbox_send.py \
  --to "$WORKER_NAME" \
  --body "$BODY"

# After the worker mails both self-reminder IDs:
python3 ~/.claude/skills/supervisor/tools/state_update_field.py "$WORKER_NAME" worker_self_reminder_ids "<artifact-reminder-id>,<status-reminder-id>"
```

Do not create a new PR branch for fixes meant to update an existing PR.

## Tool quick reference

- `worker_launch.py`: start or restore a managed worker.
- `mecha_mailbox_send`: primary Pi tool for task instructions, redirects, questions, auth, and handoffs.
- `mailbox_send.py`: fallback/launcher-side sender for runtimes without the native Pi tool.
- `worker_capture.py`: inspect current terminal output for launch verification and status checks.
- `worker_park.py`: park a worker through the managed checklist.
- `worker_shutdown.py --verified`: graceful final shutdown after verified completion.
- `worker_kill.py`: immediate cleanup for failed launches or resource freeing; resolve sidecar warnings before updating state.
- `worker_compact.py`: request worker context compaction through Mailbox V2.
- `handoff.py`: deliver a deterministic handoff file pointer through Mailbox V2.
- `worker_send_requirements.py`: send current state requirements to a worker.
- `worker_list.py`: list managed windows.
