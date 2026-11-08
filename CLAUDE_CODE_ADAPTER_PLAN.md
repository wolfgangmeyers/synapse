# Claude Code Adapter Plan

Status: **Draft contract revised; existing implementation and prior evidence require reassessment**. Not accepted or manual-ready.

Workflow: **End-to-end testing**, authorized by Wolfgang in this `claude-integration` worktree/branch after Plan Review. No dev-loop or its gates. This docs-only update authorizes no execution, commits or push.

## Outcome and scope

Launch a router-managed worker on the host's signed-in Claude CLI. Keep the familiar conversation, tools, delivery status, context/reset controls and goals. Reuse the existing embedded runtime rather than add another supervisor.

Smallest first implementation outcome: a Pi-backed adapter handle that preserves current behavior. Next add only the shared persistence helpers Claude actually needs, then the Claude adapter and its UI integration. No generic engine framework, capability registry, broad SDK cleanup, store-format migration or wholesale rewrite of session construction. Native Pi subagents and legacy tmux Claude workers stay unchanged.

Requirements:
- [Runtime boundary draft](docs/specs/runtime-engine-boundary.spec.md): invariants, code audit and scenario verification matrix.
- [Claude worker draft](docs/specs/claude-code-workers-spec.md): proposed user behavior and AC1–AC14.
- [Detailed Claude design](docs/specs/claude-code-workers-plan.md): CLI probes, protocol tables and component details. This root plan controls sequencing and scope where older details conflict. Protocol findings are historical evidence, not guarantees about every CLI release.

Claude scenarios and the verification matrix remain the acceptance contract. Partial implementation and focused checks do not establish acceptance.

## Current implementation evidence

Specifications remain **Draft**. No Claude or runtime-boundary scenario is accepted until its required full runs pass. This map records verified handoff evidence, not execution during this docs-only update. Authored tests and scenario tags are not passing evidence.

| Area | Evidence and remaining verification |
|---|---|
| Integrated compile | **PASS** for managed/store/tagged cmd with Go **1.26.9**, on the earlier frozen 926-file snapshot. Not full test-suite evidence or validation of later edits. |
| UI | **PASS**: 151 tests, full TypeScript check and Vite build (52s). `/tmp/ui-build-validation-HeF9zz`. Not full-system acceptance. |
| Normal managed | Last complete run: **89 pass, 1 fail**, 214.839s. Cancel expected aborted vs toolUse: `/tmp/claude-sole-validation.qoINHR/normal-managed.log`. Later actual cancel-race fix in `runtime.go` and its test: targeted GREEN, 30 repeats across 16 relevant tests (`/tmp/claude-cancel-isolated.bgSEqY`). **No full rerun** after the fix. |
| Orphan cleanup | Zombie-reaping assertion fix only, production unchanged: RED 3/3, GREEN 30/30, suite passed twice (36.063s). |
| Review 6b fixture | Missing-recipient reconciliation fixture fixed, GREEN ×3. `/tmp/claude-review6b-fix.DhvkWk`. |
| Store prerequisites | Fixture default goal policy initialized for the invocation fence, **not recipient routing**. Focused normal run: 22 tests + 21 subtests **PASS**, 65.161s. `/tmp/claude-store-prereq.H7GWeO`. |
| Store targeted race | 8 tests + 7 subtests completed with passes **across split runs**, no race diagnostics. Historical: PASS 30.377s (`/tmp/claude-store-narrow-race.WNLD2P/historical`). Reservations: initial run timed out at 600.089s during SQLite fixture migration after 4 tests + 7 subtests passed. Remaining 3 passed in 85.690s (`/tmp/claude-store-race-remainder.dzfFhn`). **Not one green full-suite run.** |
| Scoped browser run | **65/65 passed**, exit 0, with matching recorded source hashes: `/home/wolfgang/.mecha-developer/test-runs/claude-browser-final.cHX8YNPp` (`scope.json`, `run.log`, `result`). Includes nine Pi boundary cases; see [coverage and gaps](tests/e2e/runtime-engine-boundary-coverage.md). This supersedes the earlier four-case smoke failure, not the full behavioral matrix. |
| Scoped Claude units | Four filtered Go runs passed: managed, store, transport and tagged mailbox-router, with matching recorded source hashes: `/home/wolfgang/.mecha-developer/test-runs/claude-units-detached.8wazTZFO` (`commands.log`, `result`). These are not full package suites. |
| Pi boundary/reset | Seam/shared authority and reset atomic-release fix have partial focused evidence only. RE-008 expects approved reset release/redelivery, not the historical stranded-reservation defect. Full boundary acceptance pending: [coverage and gaps](tests/e2e/runtime-engine-boundary-coverage.md). |

Pending: QA workflow and final integrated-tree verification remain open; the scoped browser and four filtered unit runs are not full acceptance or proof for subsequent edits. Full normal-managed and applicable race/regression runs remain required. Specs remain **Draft**.

Landing state: no git operations, all work uncommitted and isolated. Main base: `4cfa57338311c09158a4869ce2d6fdb87c40043e`. **Next landing window is urgent: coordinate before any refs/index changes.** This evidence refresh changes only this plan, then freezes. No execution, build, commit or push is authorized by this update.

## Practical design recommendation

Use one small adapter handle for Pi and Claude only. Shared orchestration runs/continues/steers/interrupts through it and reads the few outcome/context fields it needs. Engine resource close is separate from interrupt and turn completion. Pi close can be a no-op; Claude closes its process after every foreground execution.

Do not start by moving all Pi construction. Wrap the existing session after setup, then change the concrete active-handle consumers. Existing `agent.AgentMessage`, SDK-shaped checkpoints and private model helpers can remain internal compatibility code. Map outcomes/snapshots only where that removes a real dependency. Returning `coding.RunResult` everywhere would preserve Pi coupling, but replacing every message type would create unnecessary work.

Keep router input IDs/source/binding separate from text/image rendering. Preserve original ACK bytes in router-held correlation state. Queue acceptance or stdin write is not consumption. Pi uses current checked persistence callbacks; Claude uses its echoed user-input evidence. Both must reach checked persistence before ACK/history publication. Equal-text inputs must remain distinguishable.

A small shared persistence helper/sink is useful only if Claude otherwise duplicates checked consumption, history/final handling or provider-entry policy. Extract that minimum with Pi coverage. Full normalized events are optional, not an architecture milestone or purity requirement. No second transcript/event store.

The router retains lane admission, fences, ACKs, history/publication, goals/receipts, reset policy, and tool-effect certainty. Claude owns process/protocol mechanics. It must not fake a whole Pi session or use a direct MCP handler to bypass shared intent/completion/UNKNOWN guarantees.

### Acceptable UX differences

- Same sidebar, launch/conversation controls and timeline vocabulary; no requirement for identical internal event streams or pixel-identical timing.
- Mid-turn mail and direct prompts that arrive during a running turn are reserved and steered into the same process's stdin (CC-019). Input not yet written stays durably queued for a later execution. A direct HTTP prompt that waits for the lane is not durably accepted before reservation; cancellation or restart can drop the unaccepted request. No new queue is implied. ACK still requires the exact checked echo, not merely acceptance or a write.
- Claude reports usage; Pi estimates projected context. Context bars should reflect the appropriate measurement.
- Reset may give Claude a bounded text seed rather than structured Pi messages. Scratchpad and recent history remain available.
- Timeline updates per completed step, not per token.
- Claude v1 hides unsupported workflow, inline-tool, worker-tool and native subagent controls. The context-threshold editor stays available.
- Every foreground execution pays CLI and configured MCP startup cost. Background work cannot survive it. Model changes apply on the next resumed process; no in-place switching.

## Historical code findings that constrain the work

Base inspected: `cbd9352c`. Recheck names against the implementation base before changing code.

| Area | Current coupling / action |
|---|---|
| `router-proxy/internal/managed/embedded_session.go`, `runEmbeddedSessionTurnScoped` | Constructs Pi session, hooks, restore and transform; reads SDK result/history/model. Preserve setup initially. Shared prologue/epilogue must keep execution settlement and publication ordering. |
| `embeddedPersistence.AppendMessage`, `CommitCheckpoint`, `restore` | SDK-shaped messages drive ACK/final/history/effect validation. Retain internal compatibility encoding, no format migration. |
| `supervisor.go`, lane `activeSession`, manager `activeEmbeddedSessions`, `InterruptAgent`, test sentinels | Replace concrete worker registry handles; preserve locks and identity-checked removal. |
| `embedded_mail.go`, `embedded_prompt.go` | Preserve reservation/rendering/correlation and scheduling. Current steer matching includes byte-match then head fallback. Do not change it incidentally. |
| `embedded_wake_persistence.go`, `embedded_restart_router.go` | Continue does not append a fresh user. Claimed wakes still require exact binding/scope and checked final. |
| `embedded_context_reset.go`, `embedded_automatic_fit.go` | Shared thresholds consume context facts. Keep Pi projection/fitting and auxiliary semantics intact. |
| `embedded_workflow_transition.go`, `SyncEmbeddedAgentTools` | Calls `session.Agent.SetTools`; retain Pi refresh through a private Pi helper, not a general capability API. |
| `embedded_subagent_runner.go` | Separate Pi sessions are outside this extraction. |
| `router-proxy/internal/store/embedded_router.go`, `embedded_reply.go`, `embedded_restart_wake_origin.go` | Checked SDK-shaped bodies are an actual storage constraint, not solved by an interface. Claude encoding must satisfy validators. |

## Implementation phases

Each phase states an observable exit condition. These are implementation dependencies, not dev-loop gates. Current partial evidence is summarized above; no phase exit is accepted by this status update.

### 1. Pi-backed adapter, no behavior changes

- Define the minimal active-turn handle around run/continue, steer, interrupt and required snapshot/outcome reads. Keep resource release distinct from cancellation.
- Wrap the currently constructed Pi session; retain tools, hooks, ordered persistence, restore and `TransformContext` setup.
- Adapt registry, mail/prompt, wake/restart and reset consumers. Preserve workflow refresh with a private Pi binding/helper. Do not migrate native subagents.
- Preserve `StopError` plus error handling, synthesis/thinking nudges, model-window fallback, context estimation, callback join and fence semantics.
- Preserve extraction-time steering acceptance/reservation/matching behavior. The historical stranded-reservation defect was not fixed incidentally in this phase; the separately authorized reset fix below now supersedes that baseline. Keep Pi match-then-head correlation unchanged. No Claude branches, tool normalization, synthetic Claude model or migration.

**Exit:** all applicable runtime draft RE-001–RE-008 cases show unchanged Pi observations except the explicitly approved reset-release behavior; existing Go suites remain green with only mechanical test adaptation and no weakened assertions. Review demonstrates no shared lifecycle caller reaches into `session.Agent` just to interrupt/run it.

### 2. Minimum shared persistence operations

- Trace the Claude events to existing consumed-user/ACK, assistant/tool-history, checked final and provider-entry operations.
- Reuse the existing private persistence bridge if practical. Otherwise extract a small synchronous error-returning helper/sink and keep SDK conversion internal. Do not build a universal event schema.
- Preserve sequence/digest checks, original bytes, history ordering, stop metadata and error propagation. Failed/stale writes stop later work.
- For Pi preserve the entire provider-entry conditional structure: notice start, owner renewal, execution/headroom checks, non-mail marker, goal-policy read and applied receipt. Auxiliary work retains its current exceptions. Do not move checks after provider entry.
- Document the chosen helper boundary and any retained compatibility types so Claude translation has one clear target.

**Exit:** valid Pi translations match phase 1; invalid identity/stale-binding/append-failure cases fail closed (RE-009 if a new helper is added). No new store or format. Full event normalization is not required.

### 3. Claude protocol/process adapter and deterministic fake

- Build a deterministic fake Claude binary first. Script stream-json input echoes, assistant/tool/result events, interrupts, resume failure, usage limits, malformed output, crash/hang and child cleanup. Record argv/env/stdin for assertions without real credentials.
- Implement CLI process launch with cwd and normal user configuration. Use the verified headless flags from the detailed design; scrub tmux/nested-session markers. Resolve the binary from configured override/PATH/local installation.
- One fresh process per foreground execution; serialized stdin writer, bounded stdout lines (16 MiB), 64 KiB stderr ring, exit watcher and process-group stop/reap before the next execution.
- Translate only main-thread events needed by v1. Correlate echoed input before checked consumption. Keep tool raw identity distinct from display mapping. Ignore subagent internals without counting their usage twice.
- Run/continue through the adapter; interrupt current work independently of process close. Preserve partial user-interrupt output; discard partial output generated solely by turn-ending router-tool interruption as specified.
- Require supported control protocol/capability evidence and fail visibly on incompatible CLI output. Unknown benign events may be ignored; malformed required records must not fabricate success.

**Exit:** fake-driven process/protocol tests prove ordering, input/image rendering, consumption, outcomes, interruption, bounded buffers and process cleanup. No real LLM calls.

### 4. Router tools and effect authority

- Add router-hosted HTTP MCP transport and per-process grants bound to agent/current binding/process. Revoke on stop/exit/reset; reject stale calls.
- Keep tokens out of argv/logs/transcripts; private temporary config files only. Apply existing body/schema/deadline limits and workflow/admission rules where supported.
- Offer v1 mailbox/image/scratchpad/progress/reminder/goal/snooze/reset/context/self-termination tools. Reuse shared handlers and effect records rather than replicate business policy.
- Use the settled narrow shared witness/Execute/result path in detailed plan §6, including canonical names, duplicate argument checks and checked result persistence. No direct enqueue/browser-goal shortcuts.
- Require current checked foreground user and assistant producer for every router tool, including reads. Reject stale or absent scopes; never fabricate authority.

**Exit:** real HTTP MCP → router → SQLite tests for every tool, repeated calls, stale grants, schema failures and controlled crash uncertainty. No repeated effects or bypassed fences.

### 5. Worker lifetime, recovery, reset and separate steering fix

- Start a fresh process for each foreground execution; resume the binding with the selected model. Revoke and reap at execution end, including children; unconfirmed cleanup fences admission. No between-turn work or in-place switching.
- Persist a binding-started marker only if needed for reliable session-id/resume selection. Missing transcript reseeds once from bounded router history, not an infinite retry.
- Use the next free migration number if adding that marker: `111` is occupied; inspected checkout ends at `123`. Re-scan at implementation time. Do not migrate SDK/store transcript formats.
- Reset uses current policy: instruction, bounded scratchpad, preserved recent history and wake prompt. Bound the complete Claude seed to 200k Unicode code points, dropping oldest preserved turns whole and shortening each historical tool result to 8,000 code points. Keep warning/reset threshold precedence and a fresh-session loop guard.
- Router restart classifies interrupted/uncertain work through shared recovery, stops verified leftover process groups and sweeps temporary files. Never repeat uncertain tool effects from a resume guess.
- **Separate change:** implement the reset-coordinator/atomic store release sequence specified in the detailed plan. Verify reset/echo races on both engines before claiming Claude redelivery. Excluded from phase 1; Pi head-fallback remains unchanged.

**Exit:** deterministic restarts/resets/termination show preserved context, no old writers/orphans/repeated effects, no lost queued mail, and per-execution cleanup; uncertain writes remain held rather than replayed.

### 6. Availability, catalog, goals and failure handling

- Use only the on-demand local CLI preflight and engine/model selection specified below. No provider settings/auth flows, polling/events or credential changes. Keep root/default on Pi and reject cross-engine changes.

- Shared goal receipts/eligibility drive evaluation. Use only the usable configured Pi router default; no fallback search. Antigravity is removed, not a fallback. No available evaluator means visible unavailability and no continuation loop.
- Preserve goal caps, snooze and empty-turn breaker with Claude-specific tool-name handling confined to this integration. Unprompted/aborted/error turns do not gain ordinary goal evaluation.
- Usage limits hold queued work until reported reset time (bounded fallback delay when absent), without premature ACK/dead-letter retries. Resume interrupted work before queue dispatch only when known safe; hold uncertain submitted inputs. Keep holds in memory as proposed; restart may rediscover the limit.
- Make missing CLI/sign-in, malformed stream, process death and unsupported versions visible without killing the worker record.

**Exit:** fake availability/evaluator/limit/failure cases prove catalog/default restrictions, goals and queue safety through real APIs and persisted state.

### 7. Near-parity Web UI and documentation

- Follow `docs/web-ui-design-system.md`; reuse existing controls/components. Add launch choices, scoped model picker and clear host sign-in failure guidance only; no provider/settings row.
- Keep timeline, tool cards, activity, context editor, reset, goals and lifecycle controls. Hide only unsupported v1 controls. Use current circular icon-button styling.
- Leave provider-auth responses/parsers/settings unchanged. Direct cross-origin browser controls omit credentials and retain permissive non-credentialed CORS.
- Document configuration/binary/process lifetime/MCP route, supported differences and legacy separation in existing runtime docs. Do not claim unsupported subagent/workflow parity.
- Reconcile the draft specs and detailed plan against implemented behavior and test names. Mark status honestly.

**Exit:** required browser matrix passes, no UI errors, required controls work through actual router/proxy/WS/SQLite, and docs match delivered scope.

## Verification and test execution

Use Playwright with a headless browser, real frontend, Go router, transparent Go router-proxy, real WebSocket feed and isolated temporary SQLite database. Fake only external Claude/provider/evaluator boundaries. No automated test invokes real Claude, an LLM or an external service.

Extend existing `tests/e2e/harness.ts` patterns where useful; do not replace owned lifecycle/persistence with internal JavaScript mocks. Use controlled local barriers for races/crashes, bounded event-driven waits and per-test cleanup. Recovery cases restart the real router against the same database. Assert UI plus WS plus SQLite ACK/entry/effect/wake records, not screenshots alone. Cover proxy transport and a direct cross-origin browser launch/control path.

Required suites and supporting checks:
- `tests/e2e/runtime-engine-boundary.spec.ts`: partial coverage of Pi compatibility RE-001–RE-008; conditional RE-009 remains a requirement where applicable.
- `tests/e2e/claude-code-workers.spec.ts`: planned full Claude acceptance matrix, all deterministic.
- Go process/translator/MCP/store and Vitest guard/selection checks: partial focused evidence only, as summarized above. Existing suites still require applicable regression validation.

For remaining validation, use current package scripts and harness entrypoints and record exact results. Do not invent a test command or report a planned suite as passing. Manual real-CLI smoke checks are optional separate evidence, never a substitute for required automated coverage.

## Requirements traceability / Plan Review checklist

Every Claude row below maps to scenario-structured spec cases in the companion spec. Names are planned test titles, not existing evidence.

| Requirement | Phases | Planned verification |
|---|---|---|
| Boundary AC1–AC3 / RE-001–003 | 1–3 | Pi text/image/ACK parity; steer acceptance vs consumption; interrupt vs close |
| Boundary AC4–AC7 / RE-004–007 | 1–2, 4–5 | checked effects/history; goals/tool refresh; reset/wake; stale/failing writes |
| Boundary AC8–AC9 / RE-008–009 | 1–2 | unchanged Pi error/nudge behavior plus approved atomic reset release/redelivery; optional helper validation |
| Claude AC1–AC2 | 6–7 | `checks local CLI on demand and launches only signed-in Claude workers without provider settings`, including worktree and initial instruction |
| Claude AC3 | 2–4, 7 | `renders text image tool steps and final without automatic mail` |
| Claude AC4 | 3, 5 | `ACKs exact echoes and redelivers only definitely unsubmitted mail across reset; uncertain writes stay held`, startup/recovery exceptions and duplicate text |
| Claude AC5 | 4 | `runs each router tool once and rejects stale process grants`, failure/UNKNOWN cases and between-turn scope |
| Claude AC6–AC7 | 3, 5 | `interrupts keeps partial output and cleans up restart termination and router crash` |
| Claude AC8 | 5–7 | `switches Claude models without cross-engine selection`, next-process model selection |
| Claude AC9 | 5, 7 | `preserves scratchpad and recent context for tool threshold and browser resets`, warning/loop guard/compaction |
| Claude AC10 | 4, 6–7 | `continues NOT_DONE stops DONE honors snooze cap and evaluator unavailability` |
| Claude AC11 | 6 | `holds usage-limited work without ACK retry or dead-letter loss then resumes` |
| Claude AC12 | 3–5 | `scrubs environment bounds diagnostics and revokes tokens and temporary files` |
| Claude AC13 | 1–2 plus separate fix | Pi regression matrix; compare shared UX only, allow explicit engine differences |
| Claude AC14 | 3–5 | `starts one process per foreground execution and reaps children`, including cleanup fences and model changes |

Final implementation review maps every changed production file to these requirements or a demonstrated blocker. Remove unsupported refactoring. Implementation acceptance requires the applicable phase exits and scenario matrix, not merely consistent documentation.

## Retention and cleanup

No new event ledger/cache/archive. Existing router transcript/history/effect retention applies. Temporary per-process config/prompt files are deleted on exit and swept at startup. A process record is removed only after confirming its matching group has no executable members; uncertain cleanup retains the record and fences admission for operator resolution. At most one process record exists per worker. Stderr is capped at 64 KiB and stdout records at 16 MiB. Correlation/event bounds and overflow behavior are fixed below and in the detailed plan; test both count and byte limits. Process and owned children end with each foreground execution. Claude transcript retention follows configured CLI cleanup (default proposed 30 days); missing transcripts use bounded reseeding. Test expiry/cleanup and overflow paths, not just happy shutdown.

## Settled Plan Review decisions

### Local CLI selection and preflight

Claude is installed and logged in on the router host. Login/logout and credentials are managed externally with `claude`; inherit normal CLI user/project configuration. No Claude provider settings row, connect/login/logout route, provider-auth response/event, periodic polling, credential persistence or fixed-provider-count changes.

Keep `claude-code/<alias>` only as an internal engine/model selection key in the existing launch/model pickers. Offer the known aliases without promising current sign-in availability. On launch and each process acquisition before each execution, resolve `MECHA_CLAUDE_BIN`/PATH/local installation and run a bounded local auth-status check (5 s, no prompt). Missing executable, signed-out, malformed or timed-out status produces a helpful launch/turn error directing the user to install/sign in on the host. Do not create a worker on failed launch preflight. No new polling/cache subsystem; no account identity retained. Engine-specific model choices never become the root/default Pi model or cross-engine switches. Automated tests use a fake executable/auth response only.

### Checked Claude submission and foreground authority

Pi's `StreamFn` remains a true before-provider-dispatch gate. Claude CLI does not expose an equivalent per-request callback. Split only the minimum shared checks: **before any stdin submission**, under current lane/process binding, renew ownership, check lifecycle/execution admission, input/image/seed bounds and known context headroom. Denial writes no stdin and no ACK. Reserve and correlate input before the serialized write; a write alone never consumes it.

On the exact input echo, synchronously persist the original user bytes and ACK through the checked path, bind the execution user, then commit non-mail entry and eligible goal-applied evidence before accepting later output/finals. The echo proves consumption, not permission to submit retroactively. If checked echo/entry persistence rejects (including ownership changing after submit), stop/reap the process and revoke MCP, discard subsequent output and report the error. Already committed ACK stays committed; a write-started input without checked echo stays held, not eligible for automatic selection. Never automatically replay a consumed input or an UNKNOWN effect. No ordinary final or goal evaluation follows failed entry. Do not refresh entry/receipts merely on assistant arrival. A checked successful goal-tool result applies only at the next observed response boundary, never at goal-set time; absent reliable response-boundary evidence, leave it pending until the next submitted input.

This guarantees router-controlled submission admission and checked publication, not a before-every-request gate for Claude native tools. Native CLI work is bounded to the foreground process lifetime. Reject unsolicited output outside that execution; no notification scope or fabricated authority.

### Narrow shared MCP execution path

Retain SDK-shaped checked persistence internally. Extract the existing `BeforeToolExecute` admission closure and its per-turn witness/result maps just far enough to call from Pi and Claude; do not add an effect framework or new ledger. For Claude router tools translate `mcp__mecha__<name>` to the canonical shared name in **both** persisted assistant calls and results. Keep raw identity only in transient protocol/display metadata. Built-in tools have display mapping but cannot acquire router-effect authority.

For each call: authenticate current process/binding, serialize with its admitted turn, renew owner, bind execution user, validate original schema/arguments and workflow policy. Require `_meta["claudecode/toolUseId"]` (no JSON-RPC-ID fallback for effects); scope the ID to binding and process incarnation, use the same canonical ID in assistant producer, effect and result. Persist the actual assistant tool-call producer before admission; if HTTP beats stdout, wait for that producer with a 5 s deadline, never synthesize it. Use the current `RecordEmbeddedToolInvocation` / `StartEmbeddedToolInvocation` / domain admission / completion witness path for addressed effects. Reuse actual tool `Execute` closures and their checked result callbacks, not copied store calls. After domain completion, append the exact result via checked persistence before returning MCP success or publishing successful completion. The current Pi `AppendMessage` writes some presentation history before its checked append; leave that behavior in phase 1, but make the Claude helper return the checked result first and only then project/publish success. Reuse the store validators, not that early presentation ordering. stdout repeats of an MCP result must match the stored canonical payload and must not append/publish it twice. Failed appends cancel further work; domain-committed but unchecked results remain uncertain for replay.

| Tool | Existing shared path to reuse (no direct shortcut) |
|---|---|
| `mecha_mailbox_send`, `mecha_send_image` | Addressed/image Execute, `RecordEmbeddedToolInvocation`, `StartEmbeddedToolInvocation`, `EnqueueEmbeddedToolMail`/`EnqueueEmbeddedToolImage`, actual Host completion and checked result append. No plain `Enqueue`; attachment writes only after fresh admission. |
| `goal`, `goal_snooze` | Existing goal invocation validation/Start/domain/Complete plus `goalResult` and `CheckEmbeddedGoalResult`; never `UpdateEmbeddedBrowserGoal` for an agent tool. A set remains pending until checked application evidence, not forever and not immediately applied. |
| `mecha_reminder` | Existing fresh-intent/admitted reminder Execute and `reminderResult` pre/post-append handling; no direct unadmitted reminder call. |
| `scratchpad`, `mecha_progress`, `mecha_context` | Existing schema/role-checked Execute, checked producer/result persistence; read-only status has no effect intent. |
| `context_reset`, `mecha_worker_terminate` | Existing turn-scoped request/Execute, checked result, then stop turn and shared lifecycle action. |

Duplicate handling runs before fresh admission or attachments. Compare canonical name and original arguments; changed arguments conflict. Serialize duplicates and never execute an uncertain effect again. A checked result may be returned within the current execution; historical result reconstruction or replay after process exit/restart is not required. Missing evidence and stale grants never authorize execution.

All router tools, including reads, require a current process grant and checked foreground user/assistant producer. Wait at most 5 s for a racing producer; otherwise reject. No idle status exception, background scope or synthetic authority.

### Separate reset-safe reservation fix (after Pi extraction)

Implemented separately from the seam; full regression acceptance is pending. The original stranded-reservation defect is historical evidence, not expected behavior to preserve forever. RE-008 follows this current contract.

Owner: the shared reset coordinator (`ExecuteContextReset` and its store epoch transition), not either adapter, not `popSteerAttempt`, and not an exit watcher. All reset triggers use the same sequence. Mark reset pending under the lane's short admission lock, reject new submit/steer/MCP admissions, interrupt active work and join callbacks; Claude additionally closes/reaps the old process and revokes its grant. Do not wait while holding a callback-needed lock. Browser reset waits outside `lane.serial`, then acquires it; tool/threshold reset already on the lane uses a non-reentrant internal reset helper after callbacks settle.

Only after drain, snapshot preserved context. In the **same immediate SQLite transaction** as `ResetEmbeddedSessionEpoch`, fence the exact old agent/session/epoch/generation/owner, delete only that binding's `embedded_attempts` with no `embedded_acks` and no checked consumed-user entry (using its append key), and install the next epoch. Never delete ACKed/consumed attempts or retarget an attempt. Message incarnation, accepted payload, target snapshot, queued state and expiry remain unchanged. Before stdin, the exact accepted message incarnation gets the durable `messages.claude_stdin_started` bit; it lives on the message row, not the attempt. Deleting an unACKed attempt and its append key does not clear that bit. Thus an uncertain queued message remains held through epoch transition and restart until existing expiry; rollback preserves both attempt and bit. Release eligible definitely unsubmitted reservations; write-started inputs without checked echo remain held, including across reset. Rollback leaves both old binding and reservations unchanged. Clear old correlation only after drain/transaction; acquire new owner, seed/wake, clear reset-pending and offer live queued mail with a fresh attempt.

An echo racing reset has two outcomes: checked append wins before drain/transition (one ACK, preserve consumed history, no redelivery), or transition wins (old callback rejected by binding/attempt fence, zero old ACK; fresh delivery only if definitely unsubmitted, otherwise held). Late callbacks cannot delete a replacement reservation, ACK the new attempt, publish history/usage or start effects. Store fencing is required even after callback join for delayed external events. Restart after a committed transition recovers queued work; no in-memory cleanup dependency. Expired/retargeted messages follow existing expiry rules, never resurrect. Do not change Pi match-then-head correlation in this fix.

Regression: use barriers at reserve, echo/checked append, drain and epoch commit for Pi and Claude. Cover all reset triggers, equal-text distinct inputs, successful consumption vs unconsumed release, failed reset rollback, late old echo, repeated reset, crash after epoch commit, expired mail and new reservation protection. Assert SQLite attempts/ACKs, WS history and actual new input; no sleeps or real LLM.

### Concrete bounds and evaluator choice

Per process: at most 64 pending input correlations and 6 MiB total rendered pending input bytes (including base64 images/seed); reserve capacity before reserving/writing, otherwise leave mail queued and direct HTTP prompts waiting for the lane. Maximum stdout line 16 MiB; stderr ring 64 KiB. HTTP-before-producer buffering is at most 128 records and 16 MiB total, whichever first. Input correlation or byte capacity causes backpressure: leave additional mail queued and direct HTTP prompts waiting before reservation/write. An oversized stdout line or exhausted HTTP-before-producer buffer instead causes visible protocol failure and stop/reap; never drop required events or ACK unread input. Drain/clear all transient maps on settlement/close; no accumulating cache. Tests hit count and byte limits separately.

For Claude goal evaluation use only the currently configured router default if it is a usable Pi model with positive context window and constructible authenticated stream. No search/fallback model, no Antigravity, no new evaluator setting. Missing/unusable default records `goal evaluation unavailable` and schedules no continuation. Pi workers retain their current evaluator behavior.

Exact private Go signatures may follow existing callers; retain SDK compatibility encoding and extract only the admission/result helpers Claude needs. No architecture decision remains deferred to a generic framework.

Wolfgang has authorized the work. Active workflow: **End-to-end testing (70%)**, not accepted or manual-ready. The settled decisions above remain the contract. Current partial evidence does not establish completion.

Review evidence: [validated findings and code locations](docs/specs/claude-code-workers-plan.md#16-validated-plan-review-findings). All Claude ACs map to CC-001–CC-018 in the [scenario matrix](docs/specs/claude-code-workers-spec.md#11-automated-e2e-verification-matrix).
