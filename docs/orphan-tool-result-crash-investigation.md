# Orphan tool-result crash investigation

## Why this matters

When Pi replays a session that contains a tool result without a matching assistant tool call, the provider can reject the request with an error like:

```text
Codex error: No tool call found for function call output with call_id ...
```

For Wolfgang's supervisor session this is not just a local annoyance. When the supervisor crashes this way, Wolfgang can lose control from both the mobile app and the web user interface. Recovery may require going physically to the machine or SSHing in to repair the session. The watchdog has not reliably noticed or repaired this failure mode so far.

The goal is to get to the root cause, not only hide the symptom.

## Historical local reproduction/evidence

The original investigation recorded this local defensive branch and worktree (historical references, not a statement that they still exist):

- Worktree: `/Users/wolfgang/code/mecha-developer/.worktrees/supervisor-crash-guard`
- Branch: `codex/supervisor-crash-guard`
- Commit: `044d00c09b4dc503f66ded31e47ddc47a3484cae`
- Commit message: `Guard orphan tool results before provider replay`

That commit adds a Pi extension `context` hook that scans the message list before provider replay and removes `toolResult` messages whose provider call id has no matching earlier successful assistant `toolCall`. It also adds tests for preserving valid tool results, dropping orphan results, and treating tool calls from errored assistant messages as unsafe.

The original guard was a safety net, not a root-cause fix. The current checkout contains orphan quarantine in `pi-extensions/mecha-messaging/diagnostics-history.ts` and the `context` hook in `index.ts`, including structured diagnostics. The checkout also contains commit `9452fb7c` ("Guard orphan tool results before provider replay") in its history; this alone does not establish the disposition of the original worktree or whether the current guard is installed or running in any deployed supervisor session. Verify deployment separately.

## Diagnostic requirements to verify

The original guard reported quarantine through deduplicated UI notifications. The current checkout attempts bounded structured diagnostic writes to `MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH` when set to a nonempty path, otherwise to `~/.mecha-developer/logs/orphan-tool-results.jsonl` when `HOME` is available. Writes are best-effort: errors are swallowed so they cannot affect provider flow.

The recorded `quarantined_count` counts fresh orphan call IDs after deduplication by session and call ID, not all tool results quarantined in the request. A request containing only previously reported IDs produces no orphan diagnostic record, even though quarantine still occurs. Verify diagnostics and their sufficiency in the relevant session; do not assume every field below is recorded.

Useful diagnostic fields:

- session id
- session file path if available
- orphan tool result call id
- orphan tool name
- message index in the replayed context
- previous/next message roles and indexes
- nearest previous assistant message stop reason
- whether the nearest assistant message was `error` or `aborted`
- whether a matching assistant tool call appears later in the context
- count of all quarantined tool results in that provider request (a diagnostic goal, not the current `quarantined_count`)
- current Pi model/provider if available
- active agent/window id if available
- whether the message came from parent supervisor, subagent, worker, or post-compaction replay if detectable

Write the diagnostics to a durable extension/router log or structured diagnostic store, not only to the UI, so the evidence survives a crash/restart and can be inspected remotely.

## Investigation questions

An agent should investigate these questions before treating the crash guard as complete:

1. Where can a `toolResult` be appended to Pi session history without its corresponding assistant `toolCall`?
2. Does this happen after a provider error, aborted turn, compaction, subagent/native child handoff, mailbox steer/follow-up delivery, or tool-call output replay?
3. Are compound tool call ids like `providerCallId|frontendCallId` normalized consistently everywhere?
4. Can an errored assistant message containing a tool call be persisted while its tool result is also persisted, then later replayed to the provider in a shape the provider rejects?
5. Does self-compaction, post-compaction follow-up injection, or the mailbox delivery coordinator ever reorder or duplicate session entries?
6. Why has watchdog not detected or repaired this failure? Is the supervisor process still alive but wedged, or does the session crash in a way the watchdog cannot observe?
7. What remote-safe recovery should exist when this happens, so Wolfgang does not need physical or SSH access?

## Acceptance criteria for a real fix

A complete fix should include:

- Root-cause explanation for how orphan tool results get into replay context.
- A minimal code fix that prevents new orphan tool results from being persisted or replayed.
- Defensive quarantine before provider requests, if still needed, with durable structured diagnostics.
- Tests for the root cause and the defensive path.
- Watchdog or router-level detection/recovery plan for the failure mode where the supervisor is no longer controllable from mobile/web.
- Clear operator instructions for remote recovery if the guard fires repeatedly.

## Notes for future agents

Do not solve this only by silently dropping orphan tool results. Dropping may restore control, but it can also hide the root cause. Preserve enough metadata to diagnose the failing path while avoiding message body/content dumps and secrets.
