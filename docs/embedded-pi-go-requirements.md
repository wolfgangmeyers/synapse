# Embedded Pi Go for router-managed roots

## One-screen overview

**Scope:** Replace only the router-owned Pi RPC process for managed roots with an embedded `github.com/sky-valley/pi` session. Keep router HTTP, SQLite, mailbox, and UI contracts. Supervisor-owned tmux workers, their extensions, and their WebSocket delivery path stay unchanged. This is a requirements document, not a claim that the replacement is ready.

**Why this needs work:** `coding.NewSession` can run in the Go host, but embedding removes a process boundary without supplying the router's durable mail/control receipts, child-agent service, TypeScript extensions, or complete provider-login flow. The Go recorder also delays creating a *new* session file until the first assistant message. Those are release blockers, not details to hide behind an in-memory session.

**Does sky-valley/pi have subagents? No built-in; Go primitives only.** `CustomTools` can expose a host-implemented child manager. Parallel tool-call batches are concurrent tool executions, not child agents. `ParentSessionID` is session metadata, not a lifecycle service. [Go session options](../../pi-go/coding/session.go) · [tool loop](../../pi-go/agent/loop.go) · [session metadata](../../pi-go/protocol/messages.go)

### Decisions to make before building

| Decision | Choice needed / release implication |
| --- | --- |
| Failure isolation | **Open:** accept a shared router/agent crash domain with tested containment, or keep a separate host/process boundary. In-process Go is not process isolation. |
| Session compatibility | **Open:** require existing Pi JSONL sessions to resume in place, or ship an explicit migration/opt-in boundary. Prove both old and new files before conversion. |
| First-release parity | **Open:** decide which current managed-root tools, child behaviors, UI controls, and histories are mandatory at launch. Missing TypeScript extension behavior cannot silently disappear. |
| Provider support | **Open:** enumerate required providers, login/logout, refresh, model availability, and credential storage. Go auth primitives do not prove Web UI OAuth parity. |
| Uncertain append | **Open:** choose an evidence-based retry/reconciliation policy for an append that may have reached a transcript without a saved router receipt. A timeout or failed lookup is not proof of absence. Do not promise exactly-once delivery without proving it. |

## Requirements

### Identity and sessions

- **I1.** Keep the same managed-agent ID, source, desired state, working directory, selected model, launch request/operation replay, and router-owned session epoch across a normal restart. A repeated `X-Request-ID` must not launch a second root.
- **I2.** Bind every turn, event, tool call, receipt, and control result to the current agent/session/epoch/runtime generation. Reject late work from an older run even if its agent name matches.
- **I3.** Resume the exact saved conversation on ordinary restart. Reset creates a new conversation without inheriting stale tools, children, mail attempts, or callbacks. Decide and test how existing Pi session files are handled.
- **I4.** An accepted initial instruction or inbound mail needs durable evidence before its transcript append is complete: check write errors and explicitly flush/sync (or commit an equivalently durable storage transaction). `StartSession` may create a fresh file before the first assistant message, but neither that creation nor a returned entry ID is a receipt. [Go recorder](../../pi-go/coding/session_store.go) · [router launch](router-architecture/workers.html#direct)
- **I5.** Preserve full transcript and bounded quiet/complete histories as distinct browser views. Compare ordering, tool results, compaction, and resume with the current RPC path.

### Mail and replies

- **M1.** Keep SQLite as the queue and routing authority: duplicate message IDs, limits, expiry, fixed destination once chosen, and the tmux WebSocket path retain their existing behavior.
- **M2.** For direct managed mail, record the attempt and generation before append. Acknowledge only after checked writes and explicit flush/sync (or an equivalently durable storage transaction) establish a verifiable entry in the intended session; an entry ID or initial file creation is insufficient. Reconcile an interrupted append against the exact message/attempt before retrying; mark genuinely uncertain outcomes as uncertain.
- **M3.** Serialize direct deliveries per root. Attribute a final assistant reply to its own incoming message/turn, not merely the latest receipt. Queue one stable reply to the original sender only after that turn settles; streamed text alone is not a reply. Test late events across two senders. [Current risk](router-architecture/failures.html#append)
- **M4.** Keep explicit `mecha_mailbox_send` separate from automatic replies. Preserve the tool's recipient, request correlation, validation, and independent queue admission.

### Tools and children

- **T1.** Before the first model request, install a fixed, approved tool manifest with the effective names, argument bounds, and per-action validation of the agreed first-release parity set. No untrusted caller can add executable tools by claiming an identity.
- **T2.** Host-implemented tools may return `queued` before router admission. Persist request IDs and results, enforce generation fences, and deliver correlated result/error entries into the correct conversation. A saved host result is not a transcript receipt or proof an external worker exists.
- **T3.** For an external effect such as `mecha_worker launch`, reconcile the host's saved result and supervisor/tmux state before considering a retry. Unknown outcome stays unknown. Do not relaunch from a timeout. The existing tmux launch path remains the owner of tmux workers. [Control sequence](router-architecture/workers.html#tool)
- **T4.** If first-release parity includes native children, implement a real host child manager: create/cancel, parent/run ownership, bounded concurrency, result retrieval, and restart/session-switch behavior. Publish authoritative child snapshots and ordered, fenced deltas; never infer child state from parent text or `ParentSessionID` alone.
- **T5.** Map required current TypeScript extension features to host Go implementations or explicitly defer them through the parity decision. No assumption that `.ts` extensions run inside `pi-go`. [Current extension bridge](router-architecture/architecture.html#map) · [prior RPC research](../RPC_RESEARCH.html)

### Provider auth and UI

- **A1.** Preserve the managed HTTP/UI contract for launch, restart, reset, terminate, models, provider status/login/logout, goal and inline-tool controls, and full/current agent views. Keep the router's saved state authoritative and live updates fenced by run.
- **A2.** Implement and test the selected provider credential flows end to end, including OAuth interaction/refresh where required. Go provider/auth types are not themselves a Web UI sign-in coordinator. Keep secrets out of SQLite, browser status projections, logs, and transcripts. [Go auth](../../pi-go/ai/auth_types.go) · [current UI/control routes](router-architecture/architecture.html#map)
- **A3.** On auth or model changes, make the next turn use the intended available provider/model. Test idle and active roots, failed credentials, reconnect, and a router restart; a model catalog row is not proof a request succeeds.

### Failures, security, and rollout

- **F1.** Preserve saved launch/restart/reset/terminate operation outcomes and bounded crash retry. Router health during startup does not imply roots or mail are ready. Recover saved state before accepting normal work. [Failure paths](router-architecture/failures.html#restart)
- **F2.** Test agent panic, blocked provider/tool calls, cancellation, memory/CPU pressure, and router crash. Pick the failure-isolation decision above before production use; embedding shares the router failure domain.
- **F3.** Keep runtime/tool permissions and working-directory trust at least as restrictive as the current managed path. Router managed controls are network-reachable without HTTP authentication today: do not expand exposure or mistake CORS/Origin for authorization.
- **F4.** Resolve the build baseline: `pi-go` at `e6b56e7` requires Go 1.26, router `go.mod` requires 1.22, and local `go` is 1.25.1. Choose a supported toolchain/dependency strategy and reproduce the build before integration. [Go module](../../pi-go/go.mod) · [router module](../router-proxy/go.mod)
- **F5.** Keep RPC as a rollback path for selected test roots until evidence supports a wider switch. No automatic migration of tmux workers or deletion of sessions on rollback.

## Acceptance gates

1. **Decision record:** resolve the five table rows and list the exact first-release tool/provider/child parity set. Unresolved rows block a production cutover.
2. **Contract comparison:** run the same deterministic model fixtures against RPC and embedded roots for launch, restart, reset, tools, history, UI snapshots, and mixed managed/tmux mail. Differences are reviewed, not silently accepted.
3. **Real persistence seam:** with the real Go recorder, crash before first assistant message and around each mail/control append and SQLite acknowledgment. Verify recovery from durable evidence, no wrong-session acknowledgment, no duplicate external launch, and an explicit uncertain state where proof is missing. [The pinned I4/M2 probe](embedded-pi-go-i4m2-gate.md) failed; this gate is blocked until a checked durable append primitive exists.
4. **End-to-end control:** a root tool requests a real tmux launch, gets its correlated result, and sends/receives mail. Inject a host crash after tmux starts and before result save; verify unknown remains unknown and no second launch occurs. [Proposed tests](router-architecture/tests.html#limits)
5. **Isolation, auth, and rollout:** demonstrate the chosen crash policy, provider sign-in/out and secret boundaries, reproducible build, opt-in rollout, and clean RPC rollback with existing tmux workers unaffected.

## Source trail

- Current router behavior and test limits: [overview](router-architecture/index.html), [architecture](router-architecture/architecture.html), [workers](router-architecture/workers.html), [failures](router-architecture/failures.html), [tests](router-architecture/tests.html), [migration proposal](router-architecture/migration.html).
- Historical Pi RPC findings, not a Go-port proof: [RPC_RESEARCH.html](../RPC_RESEARCH.html). Current Go-port evidence: [`coding/session.go`](../../pi-go/coding/session.go), [`coding/session_store.go`](../../pi-go/coding/session_store.go), [`agent/loop.go`](../../pi-go/agent/loop.go), [`protocol/messages.go`](../../pi-go/protocol/messages.go), [`go.mod`](../../pi-go/go.mod) at `e6b56e7`.
