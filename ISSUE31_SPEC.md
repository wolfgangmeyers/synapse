# Issue #31: managed-agent recovery across router restarts

## Problem

An orderly router restart can leave managed-agent rows visible while workers are unavailable. Orderly recovery must remain distinct from crash recovery. It must work with unmodified stock Pi 0.73.1, preserve exact session continuity, and keep all process-identity checks fail-closed. Failed rows need safe retry and termination actions without database edits. The historical source of the original `SIGTERM` remains unknown.

## Wolfgang's direct acceptance

Use a task-local isolated router and Web UI. Launch a managed stock-Pi agent and prove it works on a real development task. Restart only that task-local router while the task is in progress. Through an isolated BCB, prove the agent automatically continues from the correct point within one 30-second deadline, with no stuck state or manual reconnect. Repeat this as five consecutive fresh trials.

Never touch shared router, UI, or browser state. Unrelated framework or provenance abstractions are out of scope unless this exact sequence directly requires them.

## Changes for this sign-off

This revision keeps the signed-off recovery, isolation, UI, deadline, evidence, and five-trial requirements. It adds the complete stock-Pi 0.73.1 contract:

- Official Pi is selected by the npm and upstream git identity below. A Wolfgang or other scoped fork is not accepted. The lightweight tag is discovery evidence only.
- Managed launch uses stock `--session <absolute-path>` for fresh and resumed sessions. `--session-id` and `--approve` are removed.
- Pi generates the fresh `sessionId`. The router binds it from `get_state` to a provisional durable row, then materializes and verifies the session file before workload admission.
- Stock event shapes, `abort` plus `get_state` quiescence, extension transcript reconciliation, official compaction APIs, and explicit fenced receipts replace fork-only `agent_settled`, `get_entry`, `get_entries`, explicit entry IDs, `session_compact_failed`, and top-level `pi.compact` assumptions.
- Migration `060` remains the existing failed-root operator migration. New stock-session and receipt work uses migrations `061` and `062`.
- `pi-subagents` is adapted in a separate pinned task-local checkout. Stock mode withholds automatic parent completion nudges because public Pi 0.73.1 APIs cannot reconstruct the parent `agent_settled` boundary.
- Current evidence based on `--managed=false`, a local barrier JSON file, runner-synthesized completion, hard-coded `response_persisted`, or hard-coded `mutated:false` is invalid and must be replaced.

## Pinned compatibility target

The official package is `@mariozechner/pi-coding-agent` `0.73.1`. It was resolved once at `2026-09-22T22:54:32Z`. npm declares `https://github.com/badlogic/pi-mono.git`. GitHub redirects that identity to repository id `1035029907`, currently `https://github.com/earendil-works/pi`. The redirect is the canonical repository identity. Selection still comes from the official `@mariozechner` npm package and its matching git commit. It does not select an Earendil-scoped package or a Wolfgang fork.

The immutable source identity is:

- lightweight tag: `v0.73.1` (not authority by itself)
- commit: `781152fc24841dc54b22284514604048ebe5e2c9`
- root tree: `e45fa7f9bfca63ae980fcc47ee2a1f2bd417cb91`
- `packages/coding-agent` tree: `b8e81d39531531f87299deadcb1acdcff1445324`

The independently pinned npm artifact is:

- tarball: `https://registry.npmjs.org/@mariozechner/pi-coding-agent/-/pi-coding-agent-0.73.1.tgz`
- integrity: `sha512-gXQh3SaZmWTfVMc4Ao5+LGbVeKvzyO7tolok0nLsZgq9nGjZx/EEU3NM8C+qUnB4Nvs2rswG5qOVgLzQkq0fHQ==`
- npm shasum: `a14f26f45d3553556149432d743242675771862a`
- tarball SHA256: `7bf5d492670c04fd7c599dee7e6eaabff964084affd216766107e6741df7a2e1`
- tarball `package.json` SHA256: `f8355b4a90ef7892ff7c8ddfc377fb20e6703e843b78e9a87b77d619d76d6d56`
- installed package-tree SHA256: `603f58914b282bcce794ba1e62434d579704e204939735a703856cc6b5e7341a` over 711 sorted file-hash records
- installed `dist/cli.js` SHA256: `e959f463b06ddd15ed882783ac02f39ecaeef950ef967da86380f39dda6595fa`
- installed CLI result: `pi --version` prints `0.73.1`

The retained evidence root is `/home/wolfgang/.cache/mecha-issue31/official-pi/`. Its `RESULT.json` SHA256 is `6673484f96bdb02b5cb41f274d11553cbc831324b0fbb0953a997d4f1ccd9255`. Its `SHA256SUMS` SHA256 is `dcd67804d07241cb7f774625d703ed8e2424872a656ab511bfa312e0eacc5256`. Source and npm artifacts are pinned independently because npm contains generated `dist` files that are absent from the git tree. No source-build byte-reproducibility claim is made.

`pi-subagents` provenance remains separate. The compatibility checkout starts at commit `185381bf4f52a1d86dbb249e5a54346e237d36d6`, repository tree `8cf3f66002fa10c8e40afc5e44c351b56e66754b`, and `packages/pi-subagents` tree `a2e9e77fb9ada0cb455de965fcc882124953c939`. Its adapted tree and extension entrypoint are recorded in an independent extension lock. They are not part of the official Pi lock.

## Requirements

1. **Official stock runtime.** Acceptance uses only the pinned task-local official package and its absolute CLI/package paths. Preflight verifies the commit, both git trees, npm integrity and hashes, installed tree and CLI hashes, version output, retained-cache hashes, and separate extension provenance. It performs no live tag or `latest` resolution. Missing fields, mismatches, drift, a historical scoped/G4 lock, global Pi, or `PATH` fallback fail before trial allocation and write diagnostic evidence.

2. **Private session-path reservation and binding.** Before every fresh launch, reserve one canonical absolute `.jsonl` path beneath the private managed session root. The path must be contained, non-symlinked, collision-free, and bound to the exact agent, session epoch, launch operation, and provisional artifact row. Start stock Pi with `--session <that-absolute-path>`. Keep `pi_session_id` null or provisional until `get_state` returns a nonempty `sessionId` and the exact reserved `sessionFile`. Bind both values atomically to the same launch operation and process fence. A stale callback, different path, duplicate session ID, or failed CAS fails closed.

3. **Materialization before workload.** A fresh managed session runs one router-owned priming turn before any user workload or public-ready transition. The priming assistant response must make the reserved file durable. The router then validates the canonical path, regular-file type, containment, bounded JSONL header, header session ID, device/inode, and `get_state` values. Empty, missing, malformed, replaced, symlinked, or mismatched files fail readiness. A retained session uses the same exact absolute path with `--session` and passes the checks again after replacement.

4. **Two-phase orderly stop marker.** For every router-owned managed child, persist durable `expected-stop` intent before any RPC close or process-stop action. The marker is bound to the exact retained `resume_artifact_id`. Confirm it only after exact process-group absence is proved. A marker or artifact-binding failure leaves the child untouched, reports the close error, and aborts teardown for that child. If the router dies between intent and confirmation, recovery applies crash safety. It may relaunch only from a confirmed marker with the same bound artifact. No replacement starts while the old process is present or absence is indeterminate.

5. **Orderly replacement and continuity.** Normal startup relaunches each desired-running child only from a confirmed marker, using current launch configuration and the marker-bound retained path. It assigns a new PID/start marker/process incarnation and runtime generation. The replacement must report the exact retained `pi_session_id`, path, header, and device/inode. Old process identity is never reused. Historical recipe or manifest identity does not override current configured launch inputs or the retained-session checks.

6. **Identity and signals.** A process identity is `(PID, process start marker, process incarnation, process group)`. Revalidate it immediately before every signal or ownership action. A changed or indeterminate tuple is unsafe. Reap, reuse, or relabel only an absent process or the exact tuple proved router-owned. Send a process-group signal only after verifying and re-verifying the leader tuple and managed group ownership. Any group-membership ambiguity is unsafe.

7. **Failed-row retry.** `ORPHAN_IDENTITY_UNSAFE` and `RECOVERY_RECIPE_MISMATCH` retries require a durable operation reservation keyed by `request_id` plus canonical request body. Reserve atomically from the exact failed action and row version, then prove the retained process absent or exactly owned before any action. Per-agent serialization and exact-version CAS block competing retry, termination, launch, and alias reuse. A crash resumes or returns the same operation and result. Revalidate ownership immediately before each signal or action.

8. **Failed-row termination.** Termination uses the same idempotency key, atomically reserves the exact failed action and row version, sets `desired_running=false`, and retires or deletes that row. It never signals a retained PID based only on stored metadata. Repeated requests return the same result. A newer generation cannot be affected. The alias becomes available only after reservation, artifact cleanup, and retirement commit.

9. **Stock RPC and event contract.** The router accepts the source-declared Pi 0.73.1 shapes. In particular, `agent_end` is `{type, messages}` and has no `willRetry`, while `message_update` carries `message` and `assistantMessageEvent`. Stock queue, retry, tool, session, and compaction frames must not become unknown backlog traffic. `agent_end` is informational only. It is never treated as `agent_settled`, workload completion, parent idle, persisted response, or permission to stop.

10. **Controlled restart quiescence.** Before a controlled restart stops an active source, close ingress, send official RPC `abort`, require its successful response, then poll official `get_state`. Continue only when `isStreaming=false`, `isCompacting=false`, and `pendingMessageCount=0` under the exact binding and absolute deadline. Queued work without a router-owned replay record, transport loss, malformed state, or timeout fails closed. Generic natural settlement is not inferred.

11. **Router-owned control-result delivery.** Keep `request_id`, `result_id`, canonical payload hash, immutable result, operation stage, and full runtime fence in the router ledger. The extension uses `context.sessionManager.getEntries()` to perform a bounded transcript scan for the logical result. It appends through stock `pi.sendMessage` only when no exact result exists. An exact replay appends nothing. A mismatched or duplicate logical result fails closed. A separate reconciliation step emits a fenced persisted receipt only after an exact transcript match. The router marks delivery only after that receipt on the current stdout binding. No RPC `get_entry` or `get_entries`, caller-selected entry ID, or prompt-command acknowledgment is completion evidence. Direct-mail recovery uses the same extension-owned scan rule where it currently depends on `get_entries`.

12. **Official compaction.** Router-managed compaction uses official RPC `compact` and validates `compaction_start`/`compaction_end`. Extension-managed compaction uses `ExtensionContext.compact({onComplete,onError})`, `session_before_compact`, its abort signal, and success-only `session_compact`. Stock mode does not depend on `session_compact_failed` or top-level `pi.compact`. Failure and cancellation must reopen any paused router-owned delivery state through callbacks or abort handling.

13. **`pi-subagents` stock profile.** Build and test compatibility only in the separately pinned task-local checkout. Change namespaces to `@mariozechner/*`, use stock TypeBox imports, and adapt named child registrations to stock `ExtensionFactory[]` without patching official Pi core. Child lifecycle completion may use the package-owned promise returned by the child prompt/run. Public Pi 0.73.1 APIs cannot reconstruct exact parent `agent_settled` after retries, compaction, and queued continuations. The stock profile therefore disables or indefinitely withholds automatic parent completion nudges that need that boundary. It must prove no premature nudge and no duplicate nudge. Acceptance must not claim the disabled nudge feature works.

14. **Failure truth.** Failed or unavailable operations persist an honest failure code, set `online=false` and `accepting=false`, and leave the UI failed or unavailable through final evidence capture. A marker failure leaves the original child owned and live until isolated cleanup. Cleanup cannot convert a failed trial into success.

15. **UI state and freshness.** A passing lifecycle requires router `lifecycle=ready`, `online=true`, and `activity` outside `unavailable`, `starting`, and `failed`. BCB must display `Connected` and `ready`. The UI exposes a safe post-boundary projection version. If unavailable, it exposes an opaque hash of replacement runtime generation plus process incarnation. The value must change from the pre-boundary value and equal the current router projection. BCB observation uses the harness `performance.now()` clock and must occur after the restart boundary. Public and DOM evidence contains no raw PID, session ID, process identity, incarnation, or generation.

16. **Explicit acceptance completion.** Each workload has a unique trial token. The router must acknowledge a pre-signal in-progress event tied to the token, active provider request sequence, isolated router PID, agent, and exact current fence. After replacement, success requires a persisted extension-produced receipt carrying `trial_id`, token, agent ID, provider request sequence, replacement runtime generation, replacement process incarnation, and post-boundary monotonic observation time. The router compares it with the current durable fence. Pre-boundary, cached, duplicate, unbound, stale, wrong-sequence, or wrong-generation receipts fail. The runner never synthesizes completion. `agent_end` never satisfies this requirement.

17. **Deadline.** Use `node:perf_hooks` `performance.now()`. Create one hard absolute 30-second deadline at the timestamp immediately before `child.kill("SIGTERM")` for the isolated router. The call must return true. Every stop, restart, HTTP, provider, receipt, and BCB wait receives the same deadline. Timeout wins every race. At timeout, capture final router, DOM, console, log, session, and process evidence before cleanup, return nonzero, and reject later success.

18. **Isolation and shared-state proof.** Every trial allocates fresh loopback ports, config, database, data/log/session/PID files, process and environment namespaces, provider, BCB bridge, browser profile, dedicated tab, and router process. Only that isolated router receives `SIGTERM`. Capture shared router, web UI, configuration, process, and user-tab identity before trial one and after trial five, then compare them. A literal `mutated:false` is not evidence. No reload, refresh, reconnect command, click, submit, manual action, or shared service mutation is allowed after the boundary.

19. **Five independent trials and cleanup.** Run five consecutive successful trials. Each uses a unique token and entirely fresh resources, proves the router-acknowledged in-progress boundary, resumes the exact session with changed process identity and runtime fence, reconnects the existing isolated page automatically, and receives the explicit completion receipt before the deadline. The final successful-trial check before that deadline must show no tested agent in `Starting`, `Unavailable`, `failed`, or another stale lifecycle state. A failure stops the sequence. Preserve evidence, fix the cause, allocate fresh resources, and rerun from trial one. Cleanup removes only isolated resources and proves process absence, closed ports, and removed task/profile resources. Cleanup failure fails the run. Evidence is retained before cleanup. Fewer than five complete trials is incomplete.

20. **Artifacts, inventory, and tests.** Each trial artifact contains `trial_id`, `agent_id`, `logical_agent`, `pi_session_id`, `workload_token`, `router_pid`, process identity, restart boundary, before/after runtime generation and process incarnation, before/after lifecycle, router projection version, replacement identity, acknowledged barrier, persisted completion receipt, provider request sequence, BCB DOM and console evidence, monotonic timestamps, elapsed recovery, deadline, final timeout evidence, cleanup result, redaction result, and shared-resource before/after comparison. It also records the official Pi lock and separate extension provenance with manifest paths/checksums, verified identities, status, missing fields, mismatches, and drift. Documentation inventories every checked-in production, test, and acceptance path that can stop or restart a router and every documented operator/service-manager command. Automated tests cover every requirement and name the negative cases.

## Constraints

- Do not patch, rebuild, or claim changes to official Pi core.
- Do not infer the historical `SIGTERM` sender.
- Do not weaken crash-time identity, group, artifact, CAS, or alias checks.
- Do not edit database rows by hand.
- Do not expose private runtime identity in public API or DOM evidence.
- Do not change unrelated UI behavior, shared browser state, or shared services.
- Automated tests use deterministic local mocks at external boundaries. They do not call an LLM or external service.
- BCB evidence comes from a real browser, real DOM and console, isolated Go router, stock task-local Pi process, and separately pinned compatible extensions.

## Acceptance criteria

- The full official lock and independent extension lock pass before trial allocation.
- Fresh and resumed managed launches use the exact reserved absolute `--session` path with no `--session-id` or `--approve`.
- Fresh launch binds Pi's generated session ID, completes the priming turn, and validates path, header, device/inode, and session identity before workload admission.
- Shutdown writes marker intent and artifact binding before stop, confirms after exact absence, and reports marker failure without killing the child.
- Startup relaunches only a confirmed marker's exact retained artifact and proves exact session continuity plus changed runtime identity.
- Crash recovery, retry, and termination remain fail-closed, durable, serialized, exact-version idempotent, and process-safe.
- Stock event decoding, abort/quiescence, transcript reconciliation, persisted receipts, official compaction, and the fail-closed subagent profile replace every fork-only dependency without equating `agent_end` with settlement.
- UI proof is post-boundary and current. Completion comes from the explicit token/provider-sequence/current-fence receipt.
- The hard monotonic deadline, timeout precedence, evidence-before-cleanup, cleanup failure, rerun-from-one, redaction, and no-shared-mutation rules all pass.
- Five fresh independent trials pass. No weaker evidence counts.
