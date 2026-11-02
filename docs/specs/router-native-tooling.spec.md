# Spec: Router native tooling repair

- **ID**: `SPEC-ROUTER-TOOLS-001`
- **Status**: `Active`
- **Domain**: `Mailbox`
- **Target E2E Suite**: `tests/e2e/router-native-tooling.spec.ts`

## 1. Intent & User Outcome

Agents can send durable mail using the checked-in Python adapter and native worker-send tool. Retired supervisor capabilities remain discoverable but report unavailable rather than trying missing scripts or inventing replacement behavior.

## 2. Core Invariants

1. Mail requires a recipient agent ID and body. Explicit `--from` uses the caller's verified identity without tmux; otherwise the current pane's valid mailbox window name supplies the sender using a bounded tmux query. Missing/invalid current identity fails; arbitrary environment sender labels are not substituted. The adapter posts to the configured HTTP(S) base URL only, rejects credentials/path/query/fragment and redirects, reads at most 512 KiB, and never starts or restarts a service.
2. Success prints the router's accepted message ID and actual delivery state, including deduplicated IDs. Rejection, invalid acceptance, oversized response and transport failure exit nonzero.
3. Managed mail and worker-send forward `X-Mecha-Managed-Control-Request-ID` unchanged. Correlation is provenance, not authentication. Ordinary direct mail needs no control header.
4. Existing operation names and registries remain intact; `mecha_worker` exposes only list, send and cleanup, never launch. State, priority, verification handoff, PR status, task git status, and worker list/cleanup have no supported implementation in this checkout. They return stable structured `ROOT_OPERATION_UNAVAILABLE` errors before subprocess execution. No aliases, inferred task mappings or legacy state are introduced.
5. Native worker-send resolves current identity (or explicit configured agent identity), invokes checked-in `tools/mailbox_send.py`, and preserves subject/body/recipient. Existing native mailbox-send already uses its router client and is unchanged.
6. Router health/retry/dismiss, reminder ownership/scheduling and goal controls retain their existing APIs. Host results replay by request ID and canonical input/fence; conflicting canonical arguments or fence reuse fails; key order alone does not conflict. The host ledger retains at most 200 records for at most 30 days, pruning on execution. Reconcile does not repeat an operation.
7. This repair does not define new semantics for missing legacy capabilities. Other process-local controls, providers, initializers and UI scrolling are outside scope.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [RNT-001] Send durable mail
- **User Goal**: Send mail without legacy supervisor installation.
- **Given**: An isolated router and proxy with SQLite storage.
- **When**: The user runs the actual Python sender with explicit sender, recipient, body and optional subject through the proxy.
- **Then**: The command reports accepted ID and actual state; router reads expose the persisted message. Without `--from`, a valid current tmux pane supplies the window identity; explicit sender works without tmux. Native worker-send uses the same adapter and current identity.

### Scenario: [RNT-002] Fail honestly at the mail boundary
- **User Goal**: Distinguish acceptance from failed delivery attempts.
- **Given**: Missing/invalid current tmux identity (and no explicit sender), malformed URL, unreachable router, router rejection or malformed/oversized acceptance.
- **When**: The user sends mail.
- **Then**: The command exits nonzero, never claims acceptance and does not invoke service recovery. Redirects are not followed.

### Scenario: [RNT-003] Retired operations remain discoverable but unavailable
- **User Goal**: Know which operations are actually supported.
- **Given**: Existing root/native operation registries.
- **When**: A valid retired script-backed operation is requested.
- **Then**: It returns structured `ROOT_OPERATION_UNAVAILABLE` without spawning Python or any missing legacy script. Supported worker-send remains usable.

### Scenario: [RNT-004] Correlate and replay host operations
- **User Goal**: Recover a result without repeating an external operation.
- **Given**: A host request ID, canonical arguments and agent fence.
- **When**: Mail or worker-send executes and is replayed/reconciled.
- **Then**: Its correlation header reaches the HTTP boundary; identical replay/reconcile returns the recorded result without another send; conflicting canonical arguments or fence reuse fails; key order alone does not conflict. A forged header cannot create quiet provenance; it does not reject ordinary mail. Quiet classification requires the router's existing single-use, sender/recipient-bound authorization for the configured quiet recipient.

### Scenario: [RNT-005] Preserve supported controls and bounded history
- **User Goal**: Keep useful router controls without unbounded local records.
- **Given**: An isolated router and host ledger containing expired and excess records.
- **When**: Health and reminder controls execute and a new result is recorded.
- **Then**: Health and owned reminder create/list/update/delete work through the router; expired records are removed and no more than 200 remain.

### Scenario: [RNT-006] Worker tool cannot launch workers
- **User Goal**: Keep worker creation behind the existing administration tools.
- **Given**: An embedded agent or Pi extension exposes `mecha_worker`.
- **When**: The agent inspects its tool definition or submits an old `action: launch` call (including replayed arguments).
- **Then**: Only list, send and cleanup are advertised. Launch is rejected before any worker reservation or subprocess execution. Launch-only fields are absent from the embedded definition. Existing list/send/cleanup behavior and permissions remain unchanged (including unavailable legacy adapters). Admin `worker_spawn`, its permissions, and public managed-agent creation remain unchanged. No other tool permissions change.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| RNT-001 | Real sender and native worker-send persistence | `tests/e2e/router-native-tooling.spec.ts` | `persists Python and native worker mail through proxy` |
| RNT-002 | CLI failures, limits, redirects | `tests/e2e/router-native-tooling.spec.ts` | `rejects invalid mail boundaries without recovery` |
| RNT-003 | Stable unavailable operations | `tests/e2e/router-native-tooling.spec.ts` | `reports retired operations unavailable before subprocess` |
| RNT-004 | Correlation and replay | `tests/e2e/router-native-tooling.spec.ts` | `forwards correlation and replays without resending` |
| RNT-005 | Controls and retention | `tests/e2e/router-native-tooling.spec.ts` | `preserves router controls and prunes host history` |
| RNT-006 | Reject removed worker launch, preserve other actions | `tests/e2e/worker-tool-actions.spec.ts` | `rejects stale launches and preserves worker operations and admin spawn` |
