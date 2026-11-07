# Spec: Claude router defaults and execution

- **ID**: `SPEC-CLAUDE-DEFAULT-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/claude-router-default.spec.ts` (partial; additional suites below)

**Root authority boundary:** Root processes disable built-in CLI tools with `--tools ""` and restrict MCP configuration with `--strict-mcp-config`; router tools use the checked role-scoped bridge and its existing policy. This limits model-facing tools, not OS-level authority: local user and managed hooks, plugins, and configuration are trusted workstation inputs, not sandboxed adversaries. Fake CLI argument checks do not prove CLI enforcement; see [local CLI evidence and limits](../sidecar-test-inventory.md#local-cli-contract-evidence). An incompatible managed MCP configuration must fail closed rather than grant broader tools.

## 1. Intent & User Outcome

A user can explicitly select a Claude Code alias as router default and run embedded roots, workers, managed children and goal evaluation using a locally installed, signed-in Claude CLI, without Pi provider credentials. This is a proposed contract, not a claim that these paths work today. Existing worker, subagent and managed-runtime contracts continue except where this draft explicitly supersedes their older default/root/evaluator restrictions.

## 2. Core Invariants

1. A catalog alias is a selectable name, not proof of CLI installation or sign-in. Claude authentication belongs to the host CLI, not Pi provider settings. Credentials never appear in histories or diagnostics.
2. Explicit selection overrides saved selection. An unset default retains the existing automatic Pi choice: first available non-Claude catalog model, then `openai-codex/gpt-5.6-sol`. Static Claude aliases never become the automatic choice. No selected Claude model silently falls back to Pi.
3. A saved unavailable Pi model remains that selected model; selection and independent embedded admission are distinct. Preserve invalid-explicit errors and the existing explicit unavailable-model admission path.
4. Role, engine, model and tool authority are resolved and validated before durable creation. Existing executions retain their persisted model snapshot; changing defaults does not change running agents or permit cross-engine model switching.
5. Managed children are router-owned, parent-bound and restricted by existing child and workflow tool policy, not Claude native subagents or general workers. Failed, cancelled or stale work cannot publish duplicate completion or gain authority. Bash runs with workstation user authority and is not a filesystem sandbox.
6. Claude foreground goal evaluation uses the configured default, never an invented fallback; Pi foreground evaluation retains its existing model and failure semantics. Claude evaluation is a separate short-lived, tool-free execution without external MCP or foreground-worker grants. It retains bounded input, context/headroom checks, one-minute deadline, output cap, claim/fence, history, continuation cap, and process cleanup. Failure is not a fabricated NOT_DONE.
7. Legacy retirement preserves supported contracts, shared non-Claude functionality and user data. Inventory and migration precede removal; uncertain dependencies block removal, not justify breaking users.

### Supplemental candidate evidence (not acceptance)

`tests/e2e/claude-selector-supplement.spec.ts` exercises settings selector wording, save and reload, and clearing to automatic Pi. `tests/e2e/claude-child-snapshot-restriction-supplement.spec.ts` holds a child across a default change and checks its model snapshot and nested-child denial using a fake CLI. The latter passed three repeated runs in `/tmp/mecha-scoped.0ILuhZ/child-fixed.log` on the source candidate; those runs were not on this combined patch. Selector results must be independently rerun for this patch. Neither test completes CD-001/002/006/007 acceptance.

The opt-in `tests/contract/run-claude-isolation.sh` checks a real CLI against a deterministic local provider under bubblewrap; its README describes the narrow version, host assumptions and untested authority surfaces. It is separate from browser acceptance and is not proof of the complete isolation contract.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [CD-001] Select and persist a Claude default
- **User Goal**: Use Claude without configuring Pi providers.
- **Given**: No Pi providers, known Claude aliases, and a locally signed-in CLI.
- **When**: The user selects an alias in either default selector, saves, repeats the same idempotent request, reloads and restarts the router, then launches an embedded worker without an explicit model.
- **Then**: Both UI and API show the saved alias; replay creates no duplicate effect; the worker uses that alias and completes chat with durable history and live notification. The UI explains local install/sign-in and does not claim provider authentication. Clearing the setting persists across reload; invalid or non-catalog aliases fail without changing the setting; conflicting idempotency reuse fails.

### Scenario: [CD-002] Preserve selection precedence and automatic Pi behavior
- **User Goal**: Predict which engine starts.
- **Given**: Saved Claude or Pi defaults, explicit selections, and separately an unset default with available Pi models or an empty Pi catalog.
- **When**: The user launches with explicit Pi or Claude, omits the model, clears the setting, or selects unavailable and invalid explicit models.
- **Then**: Explicit valid choices override the saved choice; unset chooses the existing first available Pi model or built-in Pi fallback, never Claude. Both accessible and visible selector options say `Automatic (Pi model)` and explain this fallback. An unavailable saved Pi model remains selected and embedded admission independently decides eligibility; explicit unavailable and invalid paths preserve their existing distinct admission/errors. No provider is silently substituted.

### Scenario: [CD-003] Launch and recover an embedded root
- **User Goal**: Use a Claude root with normal root capabilities.
- **Given**: A saved Claude alias, no Pi providers, a signed-in CLI and the existing root policy.
- **When**: The user launches an embedded root without a model, exchanges messages, delegates an allowed task, restarts the router, and stops the root; separately an explicit Pi root is launched.
- **Then**: Claude runs with the selected alias, root-specific ownership, messaging and no tools or MCP grants beyond root policy; delegation, durable history, recovery and stop work. The saved execution snapshot survives default changes and restart. The explicit Pi root behaves as before and cross-engine model changes remain rejected. A checked root tool that takes longer than producer rendezvous remains governed by the configured tool timeout; a disabled tool has no effect. Cancellation or grant revocation stops ongoing execution and cannot repeat an uncertain effect.

### Scenario: [CD-004] Reject unavailable Claude before false success
- **User Goal**: Understand and correct local launch failures.
- **Given**: The effective model is Claude, explicitly or by default, and the CLI is missing, signed out, returns malformed auth, times out, or becomes unavailable after launch preflight.
- **When**: A root or worker is launched or acquires a process.
- **Then**: A bounded, actionable install/sign-in or acquisition error is visible; failed launch preflight creates no durable agent reservation, and later failure is not reported as a successful turn. No Pi fallback, fabricated final, unsafe replay or phantom successful launch occurs. Invalid aliases and request validation still reject.

For a worker mail turn, a definite Claude process acquisition, start, or pre-submission exit failure that reaches the existing retry threshold atomically records a sanitized, actionable failure in Complete history and dead-letters only the accepted message incarnation. Uncertain stdin outcomes remain held. A failed settlement leaves the message unsettled; while the manager remains running, subsequent dispatch retries only the same fenced settlement, not the worker. An unclassified failure without an existing stdin marker is left unsettled and blocked from repeated execution for the lifetime of the manager; it is not counted as a definite start failure or assigned a false stdin marker. When stdin was already marked and execution cleanup completed, the manager atomically records the existing uncertainty hold and diagnostic, then offers subsequent mail. Settlement failures retry the fenced transaction with at most one outstanding timer per lane. Unclassified Claude failures are not dismissed by message ID. Publication follows commit. Retry counts and pending settlement are manager-local: a crash can lose the terminal decision and restart the retry count. No durable terminal decision is claimed. The existing CD-004 contract requires recipient Complete history, not a synthetic mailbox notification to an agent sender; managed-runtime-contracts.md specifies complete-history visibility for failed operations and delivery. Do not invent a sender notification contract from this scenario.

### Scenario: [CD-005] Respect the RPC protocol boundary
- **User Goal**: Avoid silently running the wrong runtime.
- **Given**: A caller requests RPC with an effective Claude model.
- **When**: The request is admitted under the documented RPC client/protocol contract.
- **Then**: Claude satisfies that contract without changing engines, or, if the documented protocol cannot be satisfied, an actionable error names embedded as the alternative before any reservation or other durable side effect. Implementation must inventory actual callers and document the concrete incompatibility before choosing rejection; it cannot assume RPC support or incompatibility.

### Scenario: [CD-006] Delegate to a Claude managed child
- **User Goal**: Delegate through the router with a Claude default.
- **Given**: A permitted parent, configured Claude router default, no Pi providers, and a signed-in CLI.
- **When**: The parent spawns a managed child that performs an allowed child tool call and completes, and then retrieves its result.
- **Then**: A new child of either a Pi or Claude parent uses the current Claude default, persisted at admission, and existing child-scoped tools and limits, not native Claude subagents or general worker authority. Later default changes do not alter the child snapshot. Parent ownership, status, checked result and history persist; exactly one completion notice is delivered and acknowledged through the normal path and is visible via router/SQLite/WebSocket. A Pi parent with a Pi or unset default retains existing parent-model inheritance; explicit parent selection remains unchanged.

### Scenario: [CD-007] Restrict, stop and fail managed children
- **User Goal**: Keep delegated work controlled and observable.
- **Given**: A parent and child subject to tool, workflow, turn and time limits.
- **When**: A denied tool or workflow spawn is attempted, a running child is stopped or cancelled, times out, fails, or a completion is repeated; also a Claude parent attempts a spawn with no configured router default.
- **Then**: Denied work has no forbidden effect; for a Claude parent, a missing default explains the requirement and creates no child. A Pi parent with an unset default instead retains parent-model inheritance as described in CD-006. Cancellation/timeout reaps the child and exposes the correct status; failures are not successful results, and no duplicate completion or notification occurs. Native subagents remain distinct. A scoped CLI bridge must preserve these guarantees before shipping; no silent Pi fallback.

### Scenario: [CD-008] Evaluate an applied goal using Claude
- **User Goal**: Continue an incomplete goal without Pi providers.
- **Given**: A checked applied goal on a Claude worker and a configured, signed-in Claude default.
- **When**: A normal eligible turn settles, evaluation returns NOT_DONE, one continuation runs, and the next evaluation returns COMPLETED.
- **Then**: A separate bounded CLI invocation uses the saved alias, bounded goal/transcript only, no tool authority or external MCP and no foreground grant. The checked verdict and continuation are durable and visible; COMPLETED prevents further wakes. Snooze, delay, continuation cap, empty-turn breaker and checked goal application remain in force. Pi foreground evaluation retains its foreground model and stream regardless of the configured default, including when it changes after launch. Claude foreground evaluation remains unavailable when the default is unset.

### Scenario: [CD-012] Clean up short-lived CLI process trees
- **Given**: A Claude sign-in check or goal evaluation starts a local CLI process that creates descendants.
- **When**: The CLI exits, is cancelled, or times out, including when a descendant holds an output pipe open.
- **Then**: The router confirms the owned process group is no longer executing before reporting success; it never signals a group after losing the leader's identity. Unconfirmed cleanup fails closed.

### Scenario: [CD-009] Keep evaluator failures and stale verdicts safe
- **User Goal**: Prevent unsafe automatic work on uncertain evaluation.
- **Given**: An eligible goal evaluation with missing/unusable default, missing CLI/auth, process failure, timeout, invalid output, or a claim superseded by goal revision, cancellation, runtime replacement or restart.
- **When**: The evaluation finishes or an old result arrives.
- **Then**: For Claude foreground evaluation, missing/unusable defaults and execution failures are visibly unavailable and do not cause automatic continuation; invalid output retains existing inconclusive semantics. Pi foreground evaluator failures retain their existing continuation semantics. Stale or cancelled claims cannot persist a verdict or failure diagnostic or wake a replacement; failure diagnostics identify Claude only for Claude evaluations and explain when the router default must be configured; cleanup reaps the process. Failure-history or execution-clear storage errors are reported using fixed, non-sensitive operational diagnostics; stale claims never publish history. A competing completed evaluation cannot publish the losing verdict or change counters. Claude foreground execution failures are not converted to NOT_DONE or an unkeyed wake; Pi foreground evaluator errors retain the existing NOT_DONE fallback and continuation behavior. A usable configured Pi evaluator and the unset-default behavior retain their existing semantics.

### Scenario: [CD-010] Audit other default consumers and recovery
- **User Goal**: Avoid hidden engine changes in background work.
- **Given**: Default-consuming launches, workflows, recovery and persisted selected-model snapshots.
- **When**: The default changes, a consumer starts or recovers, or its engine cannot satisfy that consumer's protocol.
- **Then**: Existing executions retain their snapshots; a new consumer executes its resolved engine on an eligible path or fails explicitly before side effects at a documented, narrowly justified boundary. Provider auth, Pi launch, CORS and idempotency behavior remain unchanged.

### Scenario: [CD-011] Retire legacy Claude paths without losing supported behavior
- **User Goal**: Migrate away from obsolete launch paths without losing mail or other workers.
- **Given**: In-repo missing sidecar/launcher references and a separate potentially live launcher, recovery scripts, hooks, symlinks, jobs, services and tmux workers.
- **When**: Owners inventory and classify entry points and migrate or explicitly retire each supported Claude flow before removing an obsolete path.
- **Then**: Needed mailbox identity, non-Claude launches/recovery, sender/footer, sanitization, batch, retry, injection/ACK, idempotency/conflict, durable history, worker outbox and recipient isolation remain covered where supported. Each removed assertion maps to an equivalent named test or a documented obsolete sidecar-only contract; actual supported tmux injection uses a real client, not a fake sidecar. Shared helpers stay if used. Active instructions and broken links are corrected, historical material labeled, and archives, sessions, mail and user data remain untouched. An unresolved live dependency blocks removal. Cross-repository implementation requires its own scope.

## 4. Automated E2E Verification Matrix

Existing titles below are executable test definitions, not claims that they satisfy the entire scenario. An earlier focused run reported passes for the root authority, default child, default evaluator, RPC boundary, catalog admission and root bootstrap suites (`/tmp/claude-core-tests.XUO1hm/results.txt` and individual E2E logs); this mapping audit did not rerun them or establish that the current checkout is identical. The target suite exists: `/tmp/mecha-e2e-retry.log` records both of its tests passing, but that run also records a failure in `Pi foreground goal evaluator: failed-claude`; `/tmp/evaluator-pi.log` and `/tmp/mecha-failed-repro.log` record passes for that case in other runs. These logs do not establish results for the current checkout. All scenarios remain incomplete. Browser acceptance must exercise real REST, WebSocket, router/proxy and isolated SQLite with deterministic local fake CLI/provider boundaries, bounded event-based waits and no external LLMs. Supporting Go tests do not replace browser acceptance. Fake CLI argument checks cannot prove tool/MCP isolation; establish that separately from supported CLI documentation and, where feasible, an opt-in real-CLI contract check. CD-011 also needs a supported real private-tmux/mailbox integration test and a reviewed assertion coverage map; a fake sidecar is not production injection.

| Scenario | Existing executable mapping (partial only) | Outstanding acceptance gaps |
| --- | --- | --- |
| CD-001 | `tests/e2e/claude-router-default.spec.ts`: `saved Claude default survives reload and explicit Claude selection takes precedence` (API save, reload, worker launch and answer); `unavailable default Claude fails without fallback and clearing restores Pi selection` (clear and reload); `router-proxy/internal/transport/claude_default_persistence_test.go`: `TestClaudeDefaultHTTPValidationPersistenceReplayAndClear` (supporting Go test) | Browser selector interaction, router restart, durable worker history and live notification, authentication messaging and full idempotency behavior need E2E acceptance. |
| CD-002 | `tests/e2e/claude-router-default.spec.ts`: `saved Claude default survives reload and explicit Claude selection takes precedence`; `unavailable default Claude fails without fallback and clearing restores Pi selection` (partial Claude precedence and non-Claude automatic selection); `tests/e2e/claude-catalog-admission.spec.ts`: `Claude aliases stay selectable and save as default while signed-out launch is refused` | Full explicit/saved precedence, automatic Pi fallback, selector wording and unavailable Pi admission distinctions need E2E acceptance. |
| CD-003 | `tests/e2e/claude-root-authority.spec.ts`: `CD-003 root uses checked role tools and resumes its snapshot after router restart`; `tests/e2e/managed-root-control.spec.ts`: `saved Claude default bootstraps an omitted-model root without Pi providers` (passed in isolated run; `/tmp/mecha-scoped.0ILuhZ/test-logs/omitted-model-bootstrap.log`) | Full explicit Pi and cross-engine regression, default-change snapshot, grant revocation and uncertain-effect cases remain; CLI enforcement is not established by fake CLI arguments. |
| CD-004 | `tests/e2e/claude-router-default.spec.ts`: `unavailable default Claude fails without fallback and clearing restores Pi selection` (signed-out and missing-CLI worker preflight); `tests/e2e/claude-catalog-admission.spec.ts`: `Claude aliases stay selectable and save as default while signed-out launch is refused` | Root and worker failure matrix, auth timeout/malformed response, post-preflight acquisition and no-fallback behavior need complete E2E acceptance. |
| CD-005 | `tests/e2e/claude-rpc-boundary.spec.ts`: `CD-005 rejects explicit and saved Claude RPC before reservation` (also omitted runtime); [RPC boundary inventory](../claude-rpc-boundary-inventory.md). | The test checks a managed-agent row and fake CLI starts, not every possible durable side effect or external client; full acceptance remains incomplete. |
| CD-006 | `tests/e2e/claude-default-child.spec.ts`: `delegates a policy-scoped Claude-default child and retrieves its persisted result`; `tests/e2e/claude-pi-parent-default.spec.ts`: `Pi parent delegates using the current Claude default`; `router-proxy/internal/store/claude_pi_parent_default_regression_test.go`: admission snapshot and Pi inheritance | Full policy and limit coverage, persisted history and configured Pi-default child regression remain. |
| CD-007 | `tests/e2e/claude-managed-subagent-lifecycle.spec.ts`: `Claude managed subagent lifecycle and restriction`; `Claude spawn requires a router default`; `tests/e2e/claude-default-child.spec.ts`: `stops a Claude-default child running a tool` | The stop test checks stopped status, one termination message and no completion message; it does not assert process reaping. Claude-default denial, workflow restriction, timeout, failure and duplicate-completion paths still need complete acceptance; do not infer them from the older lifecycle contract. |
| CD-008 | `tests/e2e/claude-default-evaluator.spec.ts`: `evaluates Claude goals without tools and completes the loop`; `tests/e2e/pi-claude-default-goal.spec.ts`: Pi foreground model remains selected with changed Claude, unset and explicit Pi defaults | Full bounds, headroom, deadline, cap, snooze/delay and Pi regression acceptance remain; fake CLI arguments do not establish real CLI isolation. |
| CD-009 | `tests/e2e/claude-default-evaluator.spec.ts`: `missing evaluator default shows actionable configuration diagnostic`; `fences a superseded goal evaluation`; `failed Claude evaluator is visible and never schedules a continuation`; `invalid evaluator output retains inconclusive continuation semantics` | Missing/unusable default, auth, timeout, cancellation, replacement and restart fencing need complete acceptance. Invalid output follows the existing inconclusive continuation semantics, not the execution-failure path. |
| CD-010 | No mapped executable acceptance test for the consumer inventory. | Inventory and test other default consumers, changed defaults and incompatible protocols, plus provider, Pi, CORS and idempotency regressions. |
| CD-012 | `router-proxy/internal/managed/claude_short_command_cleanup_regression_test.go`: `TestClaudeShortCommandCleansInheritedPipeBeforeReturning` (supporting Go test). | Browser acceptance and the full exit, cancellation, timeout and unconfirmed-cleanup matrix remain. |
| CD-011 | No mapped executable acceptance test. | Inventory and classify live dependencies, migrate supported flows, map retired assertions, and test real private-tmux/mailbox behavior before removal. |

### Settlement review safety

A manager-local unclassified or stale-settlement block applies only to the exact message ID, incarnation, and owner binding. A successor may proceed; the same fenced attempt must not execute again merely because settlement reports stale. Unclassified failures surface a sanitized actionable Complete-history diagnostic. Retry scheduling retains the earliest requested deadline with at most one timer per lane. The stdin marker can survive a crash before its uncertainty diagnostic commits; existing recovery does not guarantee that diagnostic is reconstructed. The message remains held until expiry, potentially without that explanation. Pending settlements and unclassified blocks are not durable across manager restart.
