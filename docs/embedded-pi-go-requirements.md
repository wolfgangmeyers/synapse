# Embedded Pi Go for router-managed roots

## One-screen overview

**Scope:** Run managed roots in-process in the Go router and launch independent managed workers inside that router, each with its own identity, mailbox, session, and lifecycle. Keep router HTTP, SQLite, mailbox, and UI contracts. Existing supervisor-owned tmux workers, their extensions, and their WebSocket delivery path remain supported but are not the native managed-worker launch path. Native recursive subagents are deferred. This is a requirements document, not a claim that the replacement is ready.

**Why this needs work:** `coding.NewSession` can run in the Go host, but embedding removes a process boundary without supplying durable ordered conversation/checkpoint writes, SQLite mail receipts, managed-worker lifecycle, TypeScript extension replacements, or Codex OAuth login/refresh. The Go recorder also delays creating a *new* session file until the first assistant message. Those are release blockers, not details to hide behind an in-memory session.

**Does sky-valley/pi have subagents? No built-in; Go primitives only.** `CustomTools` can expose a host-implemented child manager. Parallel tool-call batches are concurrent tool executions, not child agents. `ParentSessionID` is session metadata, not a lifecycle service. [Go session options](../../pi-go/coding/session.go) · [tool loop](../../pi-go/agent/loop.go) · [session metadata](../../pi-go/protocol/messages.go)

### Resolved v1 decisions

| Decision | V1 contract |
| --- | --- |
| Failure isolation | Native roots and managed workers run in-process in the router. Accept the shared crash domain with tested containment; no process-isolation claim. |
| Sessions | After router crash, reopen the router-owned session and compaction checkpoint, then resume prior conversation in order. No old Pi session import or automatic migration. Reset explicitly starts a new conversation. |
| Workers and tools | Native managed workers are independent router-launched agents with their own identity/mailbox/lifecycle, not tmux launches or recursive subagents. Recursive native subagents are deferred. Preserve agreed managed controls and history; map needed TypeScript behavior to Go rather than assuming extension compatibility. |
| Provider | Codex OAuth only: login, refresh, durable credentials, Codex Responses streaming/tools/model selection. Google, Cursor, and generic OpenAI API-key parity are outside v1. |
| Receipts and uncertainty | SQLite is authoritative for accepted mail and ACK. The Pi transcript is conversation evidence, not a mail receipt. After crash, visibly flag an interrupted mid-turn external effect as uncertain; never auto-replay it. |

## Requirements

### Identity and sessions

- **I1.** Keep the same managed-agent ID, source, desired state, working directory, selected model, launch request/operation replay, and router-owned session epoch across a normal restart. A repeated `X-Request-ID` must not launch a second root.
- **I2.** Bind every turn, event, tool call, receipt, and control result to the current agent/session/epoch/runtime generation. Reject late work from an older run even if its agent name matches.
- **I3.** After router crash, reopen the ordered prior conversation and its compaction checkpoint for each root and managed worker. Resume without silently dropping or reordering context. Reset creates a new conversation without inheriting stale tools, workers, mail attempts, or callbacks. Do not import old Pi sessions or silently treat a fresh start as recovery.
- **I4.** Persist ordered conversation entries and compaction checkpoints with checked write errors and explicit flush/sync (or an equivalently durable transaction) before reporting a transcript append complete. Reopen and verify after crash, including before first assistant output. Neither `StartSession` file creation nor a returned entry ID is a durable append. Transcript durability is not mail acceptance or ACK; SQLite owns those receipts. [Go recorder](../../pi-go/coding/session_store.go) · [historical gate](embedded-pi-go-i4m2-gate.md)
- **I5.** Preserve full transcript and bounded quiet/complete histories as distinct browser views. Compare ordering, tool results, compaction, and resume with the current RPC path.

### Mail and replies

- **M1.** Keep SQLite authoritative for accepted mail, attempts, routing, and ACK: duplicate message IDs, limits, expiry, fixed destination once chosen, and the existing tmux WebSocket path retain their behavior. Route native managed-worker mail through its own identity/mailbox.
- **M2.** For direct managed mail, persist the accepted message and delivery attempt/generation in SQLite before delivery. Commit ACK in SQLite only for the exact message/attempt after a checked, durable ordered conversation append to the intended session. A transcript entry ID, file creation, or transcript lookup is not a mail receipt. On interruption reconcile SQLite state and durable conversation evidence before any retry; keep unknown outcomes explicit and never replay an uncertain external effect.
- **M3.** Serialize direct deliveries per managed agent (root or worker). Attribute a final assistant reply to its own incoming message/turn, not merely the latest receipt. Queue one stable reply to the original sender only after that turn settles; streamed text alone is not a reply. Test late events across two senders. [Current risk](router-architecture/failures.html#append)
- **M4.** Keep explicit `mecha_mailbox_send` separate from automatic replies. Preserve the tool's recipient, request correlation, validation, and independent queue admission.

### Tools and children

- **T1.** Before the first model request, install a fixed, approved tool manifest with the effective names, argument bounds, and per-action validation of the agreed first-release parity set. No untrusted caller can add executable tools by claiming an identity.
- **T2.** Host-implemented tools may return `queued` before router admission. Persist request IDs and results, enforce generation fences, and deliver correlated result/error entries into the correct conversation. A saved host result is not a transcript receipt or proof an external worker exists.
- **T3.** For an external effect such as `mecha_worker launch`, reconcile the host's saved result and router-managed worker state before considering a retry. Unknown outcome stays unknown, visibly marked to the user after an interrupted mid-turn effect. Do not auto-replay or relaunch from a timeout or crash. Native managed workers launch inside the router; the separate existing tmux path continues to own tmux workers. [Historical control sequence](router-architecture/workers.html#tool)
- **T4.** Implement independent router-managed workers with their own IDs, mailboxes, sessions, launch/cancel lifecycle, bounded concurrency, result retrieval, and restart/session-switch behavior. Publish authoritative worker snapshots and ordered, fenced deltas; never infer worker state from root text or `ParentSessionID` alone. Recursive native subagents are deferred.
- **T5.** Map v1-required TypeScript extension features to host Go implementations; explicitly exclude others from v1. No assumption that `.ts` extensions run inside `pi-go`. [Current extension bridge](router-architecture/architecture.html#map) · [prior RPC research](../RPC_RESEARCH.html)

### Provider auth and UI

- **A1.** Preserve the managed HTTP/UI contract for launch, restart, reset, terminate, models, provider status/login/logout, goal and inline-tool controls, and full/current agent views. Keep the router's saved state authoritative and live updates fenced by run.
- **A2.** Implement and test Codex OAuth login/refresh with durable credentials, plus Codex Responses streaming, tool calls, and model selection. Go provider/auth types are not themselves a Web UI sign-in coordinator. Keep secrets out of SQLite, browser status projections, logs, and transcripts. No Google, Cursor, or generic OpenAI API-key parity in v1. [Go auth](../../pi-go/ai/auth_types.go) · [current UI/control routes](router-architecture/architecture.html#map)
- **A3.** On auth or model changes, make the next turn use the intended available provider/model. Test idle and active roots, failed credentials, reconnect, and a router restart; a model catalog row is not proof a request succeeds.

### Failures, security, and rollout

- **F1.** Preserve saved launch/restart/reset/terminate operation outcomes and bounded crash retry. Router health during startup does not imply roots or mail are ready. Recover saved state before accepting normal work. [Failure paths](router-architecture/failures.html#restart)
- **F2.** Test agent panic, blocked provider/tool calls, cancellation, memory/CPU pressure, and router crash. Pick the failure-isolation decision above before production use; embedding shares the router failure domain.
- **F3.** Keep runtime/tool permissions and working-directory trust at least as restrictive as the current managed path. Router managed controls are network-reachable without HTTP authentication today: do not expand exposure or mistake CORS/Origin for authorization.
- **F4.** Resolve the build baseline: `pi-go` at `e6b56e7` requires Go 1.26, router `go.mod` requires 1.22, and local `go` is 1.25.1. Choose a supported toolchain/dependency strategy and reproduce the build before integration. [Go module](../../pi-go/go.mod) · [router module](../router-proxy/go.mod)
- **F5.** Keep RPC as a rollback path for selected test roots until evidence supports a wider switch. No automatic migration of tmux workers or deletion of sessions on rollback.

## Acceptance gates

1. **V1 scope:** verify the resolved decisions above against the exact approved tool manifest, independent managed-worker lifecycle, and Codex-only provider contract. Unimplemented mandatory behaviors block a production cutover.
2. **Contract comparison:** run deterministic model fixtures against RPC and embedded roots for launch, restart, reset, tools, history, UI snapshots, and mixed managed/tmux mail. Add independent native managed-worker lifecycle and crash/resume fixtures. Differences are reviewed, not silently accepted; deferred provider and recursive-subagent parity is not required.
3. **Real persistence seam:** with the real Go recorder, crash before first assistant output and around ordered conversation/checkpoint writes, mail delivery, and SQLite ACK. Verify checked durable append, checkpoint/reopen order, SQLite-owned accepted-mail/ACK state, no wrong-session ACK, no duplicate external effect, and a visible uncertain state where proof is missing. [The pinned I4/M2 probe](embedded-pi-go-i4m2-gate.md) is a historical failure of the unchecked recorder seam. Its old transcript-as-mail-receipt interpretation is superseded by SQLite-owned acceptance and ACK; its observed write failures still block durable conversation recovery.
4. **End-to-end control:** a root tool requests a real independent router-managed worker, gets its correlated result, and sends/receives mail through that worker's identity. Inject a router crash after launch and before result save; verify unknown is visibly marked and no second launch occurs. Test the existing tmux path separately. [Historical test limits](router-architecture/tests.html#limits)
5. **Isolation, auth, and rollout:** demonstrate shared-crash containment, Codex OAuth login/refresh and secret boundaries, reproducible build, opt-in rollout, and clean RPC rollback with existing tmux workers unaffected.

## Source trail

- Current router behavior and test limits: [overview](router-architecture/index.html), [architecture](router-architecture/architecture.html), [workers](router-architecture/workers.html), [failures](router-architecture/failures.html), [tests](router-architecture/tests.html), [migration proposal](router-architecture/migration.html).
- Historical Pi RPC findings, not a Go-port proof: [RPC_RESEARCH.html](../RPC_RESEARCH.html). Current Go-port evidence: [`coding/session.go`](../../pi-go/coding/session.go), [`coding/session_store.go`](../../pi-go/coding/session_store.go), [`agent/loop.go`](../../pi-go/agent/loop.go), [`protocol/messages.go`](../../pi-go/protocol/messages.go), [`go.mod`](../../pi-go/go.mod) at `e6b56e7`.
