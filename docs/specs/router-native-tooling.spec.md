# Spec: Router native tooling repair

- **ID**: `SPEC-ROUTER-TOOLS-001`
- **Status**: `Active`
- **Domain**: `Mailbox`
- **Target E2E Suite**: `tests/e2e/router-native-tooling.spec.ts`

## 1. Intent & User Outcome

Agents can send durable mail using the checked-in Python adapter and native `mecha_mailbox_send` tool. Retired supervisor capabilities remain discoverable but report unavailable rather than trying missing scripts or inventing replacement behavior.

## 2. Core Invariants

1. Mail requires a recipient agent ID and body. Explicit `--from` uses the caller's verified identity without tmux; otherwise the current pane's valid mailbox window name supplies the sender using a bounded tmux query. Missing/invalid current identity fails; arbitrary environment sender labels are not substituted. The adapter posts to the configured HTTP(S) base URL only, rejects credentials/path/query/fragment and redirects, reads at most 512 KiB, and never starts or restarts a service.
2. Success prints the router's accepted message ID and actual delivery state, including deduplicated IDs. Rejection, invalid acceptance, oversized response and transport failure exit nonzero.
3. Managed mail forward `X-Mecha-Managed-Control-Request-ID` unchanged. Correlation is provenance, not authentication. Ordinary direct mail needs no control header.
4. `mecha_worker` is absent from every agent tool registry, contract and workflow catalog. Stale direct calls fail as unknown tools before any effects. State, priority, verification handoff, PR status, task git status, and worker list/cleanup have no supported implementation in this checkout. They return stable structured `ROOT_OPERATION_UNAVAILABLE` errors before subprocess execution. No aliases, inferred task mappings or legacy state are introduced.
5. Native `mecha_mailbox_send` uses its router client and preserves subject/body/recipient. Admin `worker_*`, subagents and self-termination remain unchanged; no permissions or restrictions are added.
6. Router health/retry/dismiss, reminder ownership/scheduling and goal controls retain their existing APIs. Host results replay by request ID and canonical input/fence; conflicting canonical arguments or fence reuse fails; key order alone does not conflict. The host ledger retains at most 200 records for at most 30 days, pruning on execution. Reconcile does not repeat an operation.
7. Saved workflow allowlists and historical transcripts may retain the inert retired token. Never delete a sole allowlist token into an unrestricted empty list. Live catalogs omit it, overrides cannot re-enable it, and no executor accepts it.
8. This repair does not define new semantics for missing legacy capabilities. Other process-local controls, providers, initializers and UI scrolling are outside scope.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [RNT-001] Send durable mail
- **User Goal**: Send mail without legacy supervisor installation.
- **Given**: An isolated router and proxy with SQLite storage.
- **When**: The user runs the actual Python sender with explicit sender, recipient, body and optional subject through the proxy.
- **Then**: The command reports accepted ID and actual state; router reads expose the persisted message. Without `--from`, a valid current tmux pane supplies the window identity; explicit sender works without tmux. Native mailbox-send persists mail using the existing router client.

### Scenario: [RNT-002] Fail honestly at the mail boundary
- **User Goal**: Distinguish acceptance from failed delivery attempts.
- **Given**: Missing/invalid current tmux identity (and no explicit sender), malformed URL, unreachable router, router rejection or malformed/oversized acceptance.
- **When**: The user sends mail.
- **Then**: The command exits nonzero, never claims acceptance and does not invoke service recovery. Redirects are not followed.

### Scenario: [RNT-003] Retired operations remain discoverable but unavailable
- **User Goal**: Know which operations are actually supported.
- **Given**: Existing root/native operation registries.
- **When**: A valid retired script-backed operation is requested.
- **Then**: It returns structured `ROOT_OPERATION_UNAVAILABLE` without spawning Python or any missing legacy script. Native mailbox-send remains usable.

### Scenario: [RNT-004] Correlate and replay host operations
- **User Goal**: Recover a result without repeating an external operation.
- **Given**: A host request ID, canonical arguments and agent fence.
- **When**: Mail executes and is replayed/reconciled.
- **Then**: Its correlation header reaches the HTTP boundary; identical replay/reconcile returns the recorded result without another send; conflicting canonical arguments or fence reuse fails; key order alone does not conflict. A forged header cannot create quiet provenance; it does not reject ordinary mail. Quiet classification requires the router's existing single-use, sender/recipient-bound authorization for the configured quiet recipient.

### Scenario: [RNT-005] Preserve supported controls and bounded history
- **User Goal**: Keep useful router controls without unbounded local records.
- **Given**: An isolated router and host ledger containing expired and excess records.
- **When**: Health and reminder controls execute and a new result is recorded.
- **Then**: Health and owned reminder create/list/update/delete work through the router; expired records are removed and no more than 200 remain.

### Scenario: [RNT-006] Removed worker tool has no effects
- **User Goal**: Use mailbox and administration tools without the retired worker tool.
- **Given**: An embedded agent and standalone Pi tool definitions.
- **When**: Tools are enumerated and stale `mecha_worker` calls for launch, list, send and cleanup are submitted.
- **Then**: `mecha_worker` is absent; all stale calls fail without mail, worker reservations or subprocesses. Native mailbox-send still persists a message. Existing admin spawn and terminate remain usable with their existing permissions. Subagents and self-termination are unchanged.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| RNT-001 | Real sender and native mailbox-send persistence | `tests/e2e/router-native-tooling.spec.ts`; `tests/e2e/worker-tool-actions.spec.ts` | `persists Python and native worker mail through proxy`; `removes worker tool and preserves mailbox and admin lifecycle` (native mailbox-send) |
| RNT-002 | CLI failures, limits, redirects | `tests/e2e/router-native-tooling.spec.ts` | `rejects invalid mail boundaries without recovery` |
| RNT-003 | Stable unavailable operations | `tests/e2e/router-native-tooling.spec.ts` | `reports retired operations unavailable before subprocess` |
| RNT-004 | Correlation and replay | `tests/e2e/router-native-tooling.spec.ts` | `forwards correlation and replays without resending` |
| RNT-005 | Controls and retention | `tests/e2e/router-native-tooling.spec.ts` | `preserves router controls and prunes host history` |
| RNT-006 | Remove worker tool, preserve mailbox and admin controls | `tests/e2e/worker-tool-actions.spec.ts` | `removes worker tool and preserves mailbox and admin lifecycle` |
