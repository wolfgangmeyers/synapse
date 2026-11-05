# Spec: Claude router defaults and execution

- **ID**: `SPEC-CLAUDE-DEFAULT-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/claude-router-default.spec.ts` (planned)

## 1. Intent & User Outcome

A user can explicitly select a Claude Code alias as router default and run embedded roots, workers, managed children and goal evaluation using a locally installed, signed-in Claude CLI, without Pi provider credentials. This is a proposed contract, not a claim that these paths work today. Existing worker, subagent and managed-runtime contracts continue except where this draft explicitly supersedes their older default/root/evaluator restrictions.

## 2. Core Invariants

1. A catalog alias is a selectable name, not proof of CLI installation or sign-in. Claude authentication belongs to the host CLI, not Pi provider settings. Credentials never appear in histories or diagnostics.
2. Explicit selection overrides saved selection. An unset default retains the existing automatic Pi choice: first available non-Claude catalog model, then `openai-codex/gpt-5.6-sol`. Static Claude aliases never become the automatic choice. No selected Claude model silently falls back to Pi.
3. A saved unavailable Pi model remains that selected model; selection and independent embedded admission are distinct. Preserve invalid-explicit errors and the existing explicit unavailable-model admission path.
4. Role, engine, model and tool authority are resolved and validated before durable creation. Existing executions retain their persisted model snapshot; changing defaults does not change running agents or permit cross-engine model switching.
5. Managed children are router-owned, parent-bound and confined by existing child and workflow policy, not Claude native subagents or general workers. Failed, cancelled or stale work cannot publish duplicate completion or gain authority.
6. Goal evaluation uses the configured default, never an invented fallback. Claude evaluation is a separate short-lived, tool-free execution without external MCP or foreground-worker grants. It retains bounded input, context/headroom checks, one-minute deadline, output cap, claim/fence, history, continuation cap, and process cleanup. Failure is not a fabricated NOT_DONE.
7. Legacy retirement preserves supported contracts, shared non-Claude functionality and user data. Inventory and migration precede removal; uncertain dependencies block removal, not justify breaking users.

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
- **Then**: Claude runs with the selected alias, root-specific ownership, messaging and no tools or MCP grants beyond root policy; delegation, durable history, recovery and stop work. The saved execution snapshot survives default changes and restart. The explicit Pi root behaves as before and cross-engine model changes remain rejected.

### Scenario: [CD-004] Reject unavailable Claude before false success
- **User Goal**: Understand and correct local launch failures.
- **Given**: The effective model is Claude, explicitly or by default, and the CLI is missing, signed out, returns malformed auth, times out, or becomes unavailable after launch preflight.
- **When**: A root or worker is launched or acquires a process.
- **Then**: A bounded, actionable install/sign-in or acquisition error is visible; failed launch preflight creates no durable agent reservation, and later failure is not reported as a successful turn. No Pi fallback, fabricated final, unsafe replay or phantom successful launch occurs. Invalid aliases and request validation still reject.

### Scenario: [CD-005] Respect the RPC protocol boundary
- **User Goal**: Avoid silently running the wrong runtime.
- **Given**: A caller requests RPC with an effective Claude model.
- **When**: The request is admitted under the documented RPC client/protocol contract.
- **Then**: Claude satisfies that contract without changing engines, or, if the documented protocol cannot be satisfied, an actionable error names embedded as the alternative before any reservation or other durable side effect. Implementation must inventory actual callers and document the concrete incompatibility before choosing rejection; it cannot assume RPC support or incompatibility.

### Scenario: [CD-006] Delegate to a Claude managed child
- **User Goal**: Delegate through the router with a Claude default.
- **Given**: A permitted parent, configured Claude router default, no Pi providers, and a signed-in CLI.
- **When**: The parent spawns a managed child that performs an allowed confined tool call and completes, and then retrieves its result.
- **Then**: The child uses its persisted Claude alias and existing child-scoped tools and limits, not native Claude subagents or general worker authority. Parent ownership, status, checked result and history persist; exactly one completion notice is delivered and acknowledged through the normal path and is visible via router/SQLite/WebSocket. A configured Pi-default child retains existing behavior.

### Scenario: [CD-007] Restrict, stop and fail managed children
- **User Goal**: Keep delegated work controlled and observable.
- **Given**: A parent and child subject to tool, workflow, turn and time limits.
- **When**: A denied tool or workflow spawn is attempted, a running child is stopped or cancelled, times out, fails, or a completion is repeated; also spawn with no configured router default.
- **Then**: Denied work has no forbidden effect; missing default explains the requirement and creates no child. Cancellation/timeout reaps the child and exposes the correct status; failures are not successful results, and no duplicate completion or notification occurs. Native subagents remain distinct. A scoped CLI bridge must preserve these guarantees before shipping; no silent Pi fallback.

### Scenario: [CD-008] Evaluate an applied goal using Claude
- **User Goal**: Continue an incomplete goal without Pi providers.
- **Given**: A checked applied goal on a Claude worker and a configured, signed-in Claude default.
- **When**: A normal eligible turn settles, evaluation returns NOT_DONE, one continuation runs, and the next evaluation returns COMPLETED.
- **Then**: A separate bounded CLI invocation uses the saved alias, bounded goal/transcript only, no tool authority or external MCP and no foreground grant. The checked verdict and continuation are durable and visible; COMPLETED prevents further wakes. Snooze, delay, continuation cap, empty-turn breaker and checked goal application remain in force. Configured Pi evaluation remains unchanged.

### Scenario: [CD-009] Keep evaluator failures and stale verdicts safe
- **User Goal**: Prevent unsafe automatic work on uncertain evaluation.
- **Given**: An eligible goal evaluation with missing/unusable default, missing CLI/auth, process failure, timeout, invalid output, or a claim superseded by goal revision, cancellation, runtime replacement or restart.
- **When**: The evaluation finishes or an old result arrives.
- **Then**: Missing/unusable defaults and execution failures are visibly unavailable and do not cause automatic continuation; invalid output retains existing inconclusive semantics. Stale or cancelled claims cannot persist a verdict or wake a replacement; cleanup reaps the process. No failure is converted to NOT_DONE or an unkeyed wake. A usable configured Pi evaluator and the unset-default behavior retain their existing semantics.

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

All titles below are **planned**, not existing or passing evidence. Each test must use a headless browser, real REST, WebSocket, router/proxy and isolated SQLite, with deterministic local fake CLI/provider at external boundaries; use bounded event-based waits and no external LLMs. CLI argument observations alone cannot prove tool/MCP isolation: establish that separately from supported CLI documentation and, where feasible, an opt-in real-CLI contract check. Pure selection/process tests and UI tests supplement, not replace, browser acceptance. CD-011 additionally needs a supported real private-tmux/mailbox integration test and a reviewed assertion coverage map; no test may pretend a fake sidecar is production injection.

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| --- | --- | --- | --- |
| CD-001 | Persist and clear Claude default | `tests/e2e/claude-router-default.spec.ts` | `saves replays reloads and clears a Claude default without providers` |
| CD-002 | Selection precedence and Pi fallback | `tests/e2e/claude-router-default.spec.ts` | `preserves explicit precedence and automatic Pi selection` |
| CD-003 | Embedded root lifecycle | `tests/e2e/claude-router-default.spec.ts` | `runs recovers and stops a Claude root under root policy` |
| CD-004 | Local CLI failures | `tests/e2e/claude-router-default.spec.ts` | `rejects unavailable Claude without phantom launch or fallback` |
| CD-005 | RPC contract | `tests/e2e/claude-router-default.spec.ts` | `honors or explicitly rejects Claude RPC before reservation` |
| CD-006 | Claude managed child | `tests/e2e/claude-router-default.spec.ts` | `delegates a confined Claude child and retrieves its result` |
| CD-007 | Child restrictions and failure | `tests/e2e/claude-router-default.spec.ts` | `enforces child policy and reaps cancelled or failed work` |
| CD-008 | Claude goal evaluation | `tests/e2e/claude-router-default.spec.ts` | `evaluates Claude goals without tools and completes the loop` |
| CD-009 | Evaluator failure and fencing | `tests/e2e/claude-router-default.spec.ts` | `fences unavailable invalid and superseded goal evaluations` |
| CD-010 | Other consumers and snapshots | `tests/e2e/claude-router-default.spec.ts` | `preserves engine snapshots across default changes and recovery` |
| CD-011 | Safe legacy retirement | `tests/e2e/claude-router-default.spec.ts` | `preserves supported mailbox and worker flows during retirement` |
