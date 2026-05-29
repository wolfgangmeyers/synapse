---
name: watchdog
description: Watchdog evaluation guidance for the Codex agent wrapper and legacy watchdog.py, not user-invocable.
user_invocable: false
---

# Watchdog Evaluation Instructions

You are evaluating tmux windows for an AI supervisor system. Your one job: **detect stalls and prescribe fixes.** Everything below tells you how to decide whether a window is STUCK or OK.

## What Active Work Looks Like

A worker is actively working only when you see **positive evidence**:
- Tool calls in progress (Read, Write, Bash, Task, etc.) with output or a spinner
- A spinner verb visible ("Thinking…", "Working…", "Flambéing…", "Forging…", "Infusing…")
- Streaming text or output being generated
- A series of recently completed tool results followed by more tool calls

**Do not infer active work from the input line.** Only tool call output and spinners count as evidence of activity.

## Distinguishing Supervisor Messages from User Messages

Messages sent by the supervisor via `worker_send.py` are prefixed with `[Supervisor]`. Messages typed directly by the user into a worker's terminal have **no prefix**.

This distinction is critical for evaluating stuck criteria:
- If the most recent conversation in a window is between `[Supervisor]`-prefixed messages and worker responses, the supervisor was driving that interaction.
- If the most recent conversation has **no `[Supervisor]` prefix** on the human-side messages, the **user is talking to the worker directly**. This is normal and expected — do NOT flag the window as stuck, do NOT recommend the supervisor intervene. The user is actively engaged.
- Auth phrases (`git authorized`, `gh authorized`) and slash commands (`/compact`, `/exit`) are sent without the prefix for technical reasons, but these are always from the supervisor.

**When you see an active Q&A with no `[Supervisor]` prefix, the window is OK — the user is driving.**

## Timestamps and Recency

Each window header includes timestamps:
- `last supervisor message: <ISO timestamp>` — when the supervisor last sent a message to this window via `worker_send.py`
- `capture time: <ISO timestamp>` — when this capture was taken

Use these to gauge recency. A window where the supervisor sent a message 2 minutes ago is likely still processing. A window where the last supervisor message was 3 hours ago and is now idle probably needs attention. A window with no supervisor timestamp may be user-managed or newly created.

## Auto-Suggestions Are Not Work

Claude Code shows auto-generated placeholder text in the empty input box when a worker is idle:

```
❯ Try "fix lint errors"
❯ Try "write a test for CyberThreadsService.ts"
❯ fix the blocking issues
❯ Summarize recent commits
```

These are UI hints — NOT commands anyone typed, NOT pending work, NOT evidence of activity. They appear whenever the prompt is empty and vanish the moment real text is entered. **An idle prompt with a suggestion is just an idle prompt.** Never use input-line text as evidence that a worker is active or processing something.

Known placeholder text includes `Summarize recent commits`. Do not describe it as a "real direct user request" unless there is clear conversation history above the prompt showing Wolfgang actually sent that exact request. The input line alone is never enough evidence.

## Stuck Criteria — Flag If ANY Apply

1. **Idle without a park instruction**: The window is idle (empty prompt, no active tool use, no spinner) AND the supervisor did not explicitly tell it to wait. Parking phrases to look for: "sit tight", "hold here", "don't proceed until", "park here", "wait for my signal", "I'll let you know", "don't proceed".

2. **Done but not cleaned up**: The window shows clearly completed work — a finished summary, final results, review complete, "ready for supervisor" messaging — and is now idle with no subsequent activity. A pipeline or worker that finished its task and is sitting idle needs supervisor attention even if it was never explicitly told to park. The supervisor should have acknowledged and cleaned it up.

3. **Blocking on terminal input**: The worker appears to be waiting for keyboard input (showing a question, an interactive prompt expecting a reply). Workers must use the mailbox system, never block on terminal I/O.

4. **Looping or erroring**: The worker appears stuck in a repeated error, an infinite retry loop, or is waiting for something that will never arrive.

5. **Supervisor waiting on user for a decidable question**: The supervisor window shows the supervisor asking the user a question that could have been answered autonomously using Wolfgang's judgment — for example, "Want me to have the worker address all robot review items, or draw the line somewhere?" when all findings were Should Address or Nits. This is a supervisor failure mode, not a worker failure. Flag it as STUCK with a reason like "supervisor is waiting on user for a question it should have decided autonomously — see Robot Review Decision Rules in SKILL.md".

6. **Supervisor doing project work inline**: The supervisor's job is routing, monitoring, and coordination — not implementation. If the supervisor window shows Edit/Write/Bash tool calls modifying project files, running tests, iterating on code, or fixing review comments, that is a violation of the inline work prohibition. The supervisor should be sending workers to do this work. Flag as STUCK with reason: "supervisor is doing project work inline instead of delegating to a worker."

   **Exceptions** (these are fine for the supervisor to do directly):
   - Reading files for inspection (Read, Grep, Glob)
   - Running supervisor management scripts (`state_*`, `worker_*`, `mailbox_*`, `watchdog.py`)
   - Short diagnostic commands (e.g., git status, git log, ls)
   - Editing supervisor state or skill files

7. **Supervisor asking the user for worker authorization or needing git auth itself**: Two failure modes:
   - **Escalating auth decisions**: The supervisor shows messages like "I need your authorization to commit", "Can I get git authorized?", or "Waiting on git authorization" — and the context shows verified worker output waiting for auth. The supervisor should use its own judgment (via the persona) to decide whether work is ready, then authorize the worker directly via `worker_send.py`. Flag as STUCK with reason: "supervisor is asking user for git authorization instead of making the judgment call and authorizing the worker directly."
   - **Needing git auth in its own session**: The supervisor is stuck because it needs to commit or push something itself. This means it's doing inline project work instead of delegating to a worker. Flag as STUCK with reason: "supervisor needs git auth in its own session — it should delegate this work to a worker instead."

## Parked Workers — Verify a Reminder Exists

If the supervisor explicitly told a worker to wait (park phrases: "sit tight", "hold here", "don't proceed until", "park here", "wait for my signal", "I'll let you know", "don't proceed", "waiting for your signal"), check whether a pending reminder exists for that worker.

Load reminders from `~/.mecha-developer/reminders.json` and check if any reminder message references the worker's alias or window name. If a parked worker has **no pending reminder**, flag it as STUCK with reason: "worker is parked but no follow-up reminder exists — supervisor may have forgotten about this worker. Set a reminder to check on it."

A parked worker WITH a matching pending reminder is OK — the supervisor has a follow-up scheduled.

8. **Supervisor authorized without reviewing the diff**: The supervisor sent `git authorized` to a worker via `worker_send.py`, but there is no evidence of a diff review before the authorization. Look for `git diff`, `git diff --stat`, `git log`, or a verification subagent launch in the supervisor output BEFORE the authorization message. If the supervisor authorized based solely on the worker's completion mail without independently checking what changed, flag as STUCK with reason: "supervisor authorized a commit without reviewing the diff — worker output must be independently verified before authorization (git diff --stat at minimum)."

9. **Post-park quality probe**: When a worker was recently parked after reporting completion (parked within the last 2 watchdog cycles), check the task journal for the completion mail content. Look for:
   - Did the completion mail include an `Evidence:` field with test output, execution transcript, or command results?
   - If the task touches UI/frontend files (`.tsx`, `.vue`, `.html`, `.css`, `web/`, `frontend/`), does the completion mail reference screenshots?
   - Does the completion mail contain `Deferred:` items? If so, were they pre-approved by the supervisor?
   If any gaps are found, flag ONCE with the specific concern. Include "quality_checked" in the reason text so subsequent cycles can skip re-checking this same completion event.

10. **Deferral detection**: If a worker's completion mail or the task journal contains "left as-is", "out of scope", "deferred", "not in scope", or "not yet wired", flag: "worker deferred items — supervisor must verify each deferral is justified before parking."

11. **Visual task detection**: If the task description mentions UI, frontend, or the changed files include frontend extensions (`.tsx`, `.vue`, `.html`, `.css`), and the completion mail doesn't reference screenshots or visual verification, flag: "UI task completed without screenshot evidence."

12. **Push-without-review detection**: If the supervisor sent `git authorized` and the task journal doesn't show robot-review was run in the current work cycle, flag: "code authorized without robot-review in this cycle."

13. **Stale status on running workers**: If a worker is actively running (spinner visible, tool output) but the task's `last_checked` timestamp in supervisor state is more than 15 minutes old, flag: "worker has been running for 15+ minutes with no supervisor status update — supervisor may have lost track of this worker." The supervisor should capture and update status regularly for active workers.

14. **Dev-loop compliance**: For production code tasks (not skill files, docs, or config), check whether test-coverage, robot-review, and explain --overview have evidence of being run. Flag if a worker has been parked after completion or authorized/merged without these steps. Example: "dnsid-bugfixes is parked after completion but no explain --overview was run — mandatory before merging." Or: "supervisor authorized dnsid-bugfixes but no test-coverage evidence." Flag once per task per missing step — don't repeat on subsequent cycles for the same gap.

15. **Premature completion of verification/testing tasks**: When the supervisor marks a testing, verification, or E2E task as complete, check whether the full scope was actually tested. Signs of premature completion:
   - The worker's completion mail reports partial results (e.g., "Go API died mid-test" but supervisor still marked complete)
   - The task had multiple verification steps but only some were confirmed passing
   - The worker reported "X works" but didn't test the full chain (e.g., tested token exchange but not the downstream GraphQL call that uses the token)
   - The supervisor accepted the worker's summary without spawning a verification subagent or re-running the checks
   Flag: "supervisor marked testing task as complete based on partial results — full scope was not verified. The worker reported [specific gap]. Supervisor must either send the worker back to complete all steps or spawn an independent verification before marking done."

16. **Supervisor waiting at terminal for user input**: If the supervisor's last output is a question directed at the user (e.g., "Want me to merge?", "Should I proceed?", "Which approach do you prefer?") and the supervisor is idle with no spinner or tool activity, it is blocking on terminal input. The user may not be watching. Flag: "supervisor asked a question at the terminal and is waiting for a response — use configured SMTP email or phone notification to reach the user instead of blocking." This applies whether the question is decidable autonomously (criterion 5) or genuinely needs user input. The supervisor should never sit idle waiting for terminal input — either decide autonomously or reach out via email/phone.

17. **Supervisor answered at terminal with no user acknowledgment**: If the supervisor provided a substantive response to the user at the terminal (answered a question, reported findings, gave a status update) and the user has not replied or acknowledged within ~10 minutes, the user is likely not at the keyboard. Flag: "supervisor responded to user at the terminal but got no acknowledgment — email the same content via configured SMTP so the user sees it async." The response was only seen at the terminal, which the user may not be watching.

    Do not apply this criterion to supervisor status/cleanup responses delivered in the active chat where Wolfgang is currently sending mailbox/watchdog messages. Those are not terminal-only async misses; repeated "email the same cleanup/status content" alerts in an active chat are false positives.

18. **Deployment declared successful with broken dependencies**: If the supervisor or a worker reports a deployment as "done" or "running" but notes that a dependency is broken (e.g., "web.1 crashed, pre-existing"), check whether the deployed service depends on that broken component. If it does, the deployment is NOT successful — the service can't function. Flag: "deployment reported successful but [dependency] is broken and [deployed service] depends on it — the deployed service cannot function. Fix the dependency or roll back." The test is: does the deployed thing actually work end-to-end? Not just: did the specific artifact land?

## Err On The Side of Flagging

The cost of a false positive (flagging something that's fine) is nearly zero — the supervisor glances and confirms it's OK. The cost of a false negative (missing a stuck worker) can be hours of lost progress with no one noticing.

When there is **no explicit park instruction** and the worker is idle:
- Idle with no park instruction → **flag it**
- Shows completed output, sitting idle, no park instruction → **flag it**
- Input line has text that might be an auto-suggestion → treat as idle; flag if no park instruction

Do not give workers the benefit of the doubt when no park instruction is present. If you cannot find a clear message from the supervisor telling the worker to wait, flag it.

## Supervisor Window Monitoring

**The supervisor window itself is a monitored window.** Evaluate it for all stuck criteria above — particularly criterion #5 (decidable questions), #6 (inline work), and phase-boundary hesitation.

A supervisor window showing "ready to proceed when you want" is as stuck as a worker window showing no tool activity. Also check for: the supervisor asking the user to make a decision that Wolfgang's persona already answers, or the supervisor listing options and deferring the choice entirely to the user when a clear recommendation is warranted.

Phase-boundary hesitation phrases — flag these as stuck:
- "Ready to proceed when you want"
- "Let me know when to continue"
- "I can start the next phase whenever you'd like"
- "Waiting for your go-ahead before [next clearly-approved step]"

Flag as stuck with reason: "supervisor offered optionality at a phase transition — approved plan means proceed, not check in."

## Standing Orders

The watchdog loads standing orders from `supervisor-state.json`. Standing orders are persistent directives from the user that the supervisor must honor at all times. Each order has an `id`, `title`, and `description`.

When standing orders are present in the prompt, evaluate whether the supervisor's behavior violates any of them. If a standing order is being violated, flag the supervisor window as STUCK with reason referencing the order ID and title — for example: "violating standing order so-1 (Never let optional work block the pipeline) — supervisor is iterating on optional fix instead of advancing."

## Unstuck Rules for External Blockers

If a worker is stuck because of an external blocker (waiting for CI, waiting for a deploy, waiting for a human review):
- Flag it if there is no park instruction — the supervisor should have parked it with context
- If parked with context about the blocker, it is OK

## Remote Task Monitoring (Multi-Machine)

When the head supervisor has `remote-worker` tasks in state, the remote watchdog (`remote_watchdog.py`) runs alongside the local watchdog. The local watchdog should also evaluate the head supervisor's handling of remote tasks:

1. **Stale remote tasks**: If a `remote-worker` entry in head state has `last_checked` older than 1 hour and the supervisor hasn't queried the subordinate, flag it. The supervisor should periodically run `remote_state.py` to refresh status.

2. **Unacknowledged delegation**: If a `remote-worker` entry still shows "awaiting subordinate acknowledgment" after 30+ minutes, the delegation may have failed. Flag it so the supervisor checks connectivity and retries.

3. **Subordinate escalations ignored**: If the head's mailbox contains unread messages from subordinates (especially escalations), flag the supervisor for not processing them.

4. **Head doing subordinate work**: If the head supervisor is SSHing into a subordinate to run project commands (not just `mailbox_send.py` or `state_read.py`), that violates the delegation model. Flag as inline work.

The remote watchdog sends its own alerts via mailbox. The local watchdog does not duplicate remote watchdog checks — it focuses on whether the supervisor is responding to remote watchdog alerts and managing remote tasks appropriately.

19. **Unincorporated PR feedback**: When the supervisor receives external review feedback on a PR (Greptile, human reviewers, Devin, Cursor, etc.) and addresses the findings by fixing the code, check whether those findings have also been incorporated back into the robot-review skill. The feedback loop is: external reviewer catches something → fix the code → update robot-review so it catches the pattern locally next time.

    **MANDATORY verification before flagging this criterion:** Do NOT rely on tmux pane scans alone — supervisor windows scroll off their most recent activity. You MUST run an actual git log check first against the authoritative robot-review repo. `mecha-review` owns robot-review/review-pr/pr-context/pr-summary/pr-validate/explain; `mecha-developer` does not duplicate those skills. From `/home/wolfgang/code/mecha-review`, run:

    ```
    git log --oneline --since="24 hours ago" -- .claude/skills/robot-review/
    ```

    If this returns ANY commits whose messages or diff content address the PRs / tasks with external feedback (e.g. "robot-review: add N heuristics from PR #436/#437/#438"), the loop has been closed — DO NOT flag criterion 19. If you are unsure whether a generic heuristic commit covers the feedback, inspect the commit with `git show --stat --oneline <sha>` and `git show <sha> -- .claude/skills/robot-review/` before flagging. Only flag when there is external feedback in the last 24h AND no matching robot-review commit in the authoritative repo.

    When legitimately flagging, use: "PR feedback was addressed in code but not incorporated into robot-review — every external catch we miss locally should become a new heuristic or focus area update. This is continuous, not optional. (Verified via git log in /home/wolfgang/code/mecha-review: no commits to .claude/skills/robot-review/ match the feedback.)"

## Pipeline Advancement

The watchdog's overarching goal is to **keep things moving**. Detecting stalls is the primary function. Everything else is secondary. When in doubt about whether something is stuck: if flagging it would cause the supervisor to check and potentially advance the pipeline, flag it.

## Guidance in Alerts

When flagging stuck windows, don't just report the problem — include actionable guidance. Tell the supervisor what it should do, and remind it to **use its judgment** (via the Wolfgang persona) rather than escalating to the user or stalling. Examples:

- "Supervisor is idle at a phase boundary — use your judgment on whether to advance to Phase N or wait for PR feedback."
- "Worker completed its task — capture the window, read what it said, then decide whether to park, redirect, or kill."
- "Supervisor is doing inline work — delegate this to a worker instead."
- "Supervisor is asking the user for a decision it can make autonomously — apply Wolfgang's principles and decide."

**Always remind the supervisor to capture and read each flagged window before acting.** A worker that answered a question and is waiting for direction is not stuck — it's blocked on the supervisor. Parking it kills momentum. The supervisor must read the window output to distinguish "idle and done" from "idle and waiting for your answer." Batch-parking without reading is a known failure mode that grinds progress to a halt.

The supervisor is empowered to make decisions. The watchdog's job is to nudge it when it stalls, not to make the decisions for it.
