# Spec: Embedded Tool Schema Parity and Rejection

- **ID**: `SPEC-EMBEDDED-TOOL-SCHEMA-001`
- **Status**: `Draft` (implementation and acceptance coverage pending)
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/embedded-tool-schema-parity.spec.ts`, `tests/e2e/embedded-tool-schema-rejections.spec.ts`, `tests/e2e/embedded-tool-schema-google.spec.ts`, and `tests/e2e/embedded-tool-schema-gap-boundaries.spec.ts` (partial coverage)

## Intent & User Outcome

A managed agent can use the tools and argument forms advertised to its provider. In particular, a read with nullable pagination works when null is advertised. When a call cannot be used, the agent gets a useful, safe explanation without weakening tool permissions or executing a rejected call.

## Core Invariants

1. For an authorized, available tool, calls valid under the schema actually advertised to the provider must not be rejected because of a mismatch in schema conversion, dispatch, or host validation. Normal tool execution failures remain possible.
2. The reported read contract includes nullable `offset` and `limit`: null must work when advertised, not be removed from the schema to make a mismatch disappear. Other provider schemas must be inspected rather than silently narrowed or broadened.
3. Workflow filtering, current host catalog lookup, owner checks and argument validation remain in force before durable execution intent and execution. A rejected call creates neither an execution intent nor an execution effect. Existing pre-validation running history and stored inputs may still exist; their absence is not promised.
4. New rejection diagnostics distinguish unavailable host registrations from invalid arguments, and account separately for SDK-level unknown tools. They identify the tool and actionable field/type or missing-field problem where available. Prefer trusted schema names; bound and sanitize untrusted identifiers and use a safe fallback. Do not echo raw argument values, secrets, or file contents in new diagnostics.
5. The reported `multi_tool_use.parallel` / `functions.read` wrapper is not established as owned by this repository. Provider multiple-tool calls, concurrent direct calls, or a mock wrapper do not prove actual wrapper behavior. Mixed-batch sibling behavior is not specified until its existing contract is established.
6. Replay aliasing uses persisted provenance for each historical call, not the current declaration set as evidence of what was advertised earlier. Provenance distinguishes advertised, unadvertised, and unknown/ambiguous. It survives transcript persistence and restart. Unknown includes legacy records and calls whose identity cannot be matched conservatively after hooks mutate messages.
7. Replay is transcript serialization, not execution. Isolation prevents an unadvertised or ambiguous historical name from colliding with an advertised wire name; it does not claim the historical call executed. Preserve call/result correlation, call ID, and result content through isolation.

## User Scenarios

### Scenario: [ETS-001] Direct reads honor the advertised pagination contract
- **User Goal**: Read a file using valid pagination without an opaque schema rejection.
- **Given**: An authorized managed session advertises `read`, a readable fixture file, and the provider schema captured at the external boundary; in the reported contract both `offset` and `limit` allow null.
- **When**: The provider makes direct reads with both fields omitted, both null, and valid integers, holding the path and other inputs constant.
- **Then**: Each form valid in the captured schema executes and returns the expected file content for its pagination. The reported nullable contract accepts null; integer and omitted cases are verified against their advertised contract. Any provider discrepancy is recorded, not hidden by narrowing the reported contract.

### Scenario: [ETS-001a] Fractional read pagination is rejected
- **Given**: An authorized managed session advertises read pagination as integer or null for both `offset` and `limit`.
- **When**: A provider supplies a fractional number for either pagination field.
- **Then**: The call is rejected before reading the file or creating execution intent. Null, omitted fields, and integer values remain valid; the advertised and validated schemas agree.

### Scenario: [ETS-002] Five reads through the actual reported wrapper
- **User Goal**: Read five independent files through the reported parallel route using valid pagination.
- **Given**: The actual `multi_tool_use.parallel` / `functions.read` route and its owner are available, five readable fixtures exist, and the schema and arguments at wrapper dispatch are captured; the reported schema allows null pagination.
- **When**: Five independent reads are dispatched through that route with omitted pagination, then null pagination, then valid integer pagination, keeping each path and all other inputs constant across variants.
- **Then**: Every call valid under its advertised contract executes and returns its expected content, including all five null reads in the reported nullable contract. If the route is unavailable or external, this scenario remains blocked and its owner and exact boundary are reported; direct calls or provider multi-call coverage cannot stand in for it.

### Scenario: [ETS-003] Multiple provider tool calls are not confused with the wrapper
- **User Goal**: Have multiple authorized reads in one provider response processed according to their advertised schemas.
- **Given**: A managed session advertises read and has five readable fixtures.
- **When**: A deterministic provider response contains five independent valid read tool calls, including null pagination when advertised.
- **Then**: All five execute and return expected content. This establishes provider multiple-call behavior only, not the actual wrapper contract.

### Scenario: [ETS-004] Invalid arguments are actionable and do not execute
- **User Goal**: Correct an invalid call without exposing its contents.
- **Given**: An authorized session advertises a registered tool with a required field and typed fields.
- **When**: The provider calls it with a missing required field or a wrong argument type.
- **Then**: Each call is rejected with a safe diagnostic identifying the tool and the missing field or expected type where available, rather than an opaque generic mismatch. No execution intent or effect is created; pre-validation history and stored inputs are assessed separately.

### Scenario: [ETS-004a] Coerced inputs are not blamed for another failure
- **Given**: An argument is valid after SDK coercion, but another argument fails validation.
- **When**: The host produces a rejection diagnostic.
- **Then**: It does not identify the coercible argument as invalid; if the actual failure cannot be identified safely, it gives generic schema guidance without exposing values.

### Scenario: [ETS-004b] Nullable pagination alongside an invalid field
- **Given**: Read advertises nullable pagination and a required string path.
- **When**: A host call supplies null pagination and an invalid path type.
- **Then**: The host rejects the path type, not the valid null pagination, without echoing values or creating an execution intent or effect.

### Scenario: [ETS-005] SDK rejects an unknown tool
- **User Goal**: Understand why a nonexistent tool cannot be used.
- **Given**: A tool name is unknown to the SDK's dispatched tool set.
- **When**: The provider requests that tool.
- **Then**: The call does not execute or create a durable execution intent. Its safe error indicates the tool is unavailable, without claiming that the host catalog lookup was reached. Untrusted names are bounded and sanitized with a safe fallback.

### Scenario: [ETS-005a] Codex replay and alias isolation
- **Given**: Codex requests an undeclared tool, including a tool filtered by policy.
- **When**: The rejected call and its correlated result are sent back in a later provider request, including after the declaration set changes to include that canonical tool name.
- **Then**: The conversation continues without executing the historical call. A call originally made when its name was undeclared remains isolated on replay; it must not be remapped into a currently authorized tool. Provider wire aliases resolve only when actually declared for that request. If `mecha_worker.launch` is not declared, that canonical spelling receives a correlated safe unavailable result and can be replayed. If its wire alias is declared, a provider response using the unadvertised canonical spelling must not dispatch it; the advertised alias still dispatches normally.
- **Status**: Unmet for historical canonical unknown-to-declared replay isolation. The current Codex outbound mapping uses the current declaration set: when `mecha_worker.launch` is now declared, a historical call with that canonical name is rewritten to its current wire alias even if it was originally unknown. This is declaration-based remapping of replayed history, not evidence that the historical call is re-executed. Legacy history may lack provenance distinguishing an originally undeclared call from a formerly valid canonical call; the name alone cannot resolve that ambiguity.

### Scenario: [ETS-005b] Persisted per-call provenance across declaration changes and restart
- **Given**: A provider request advertises a wire alias for one tool and does not advertise another spelling; calls and correlated results are persisted with the advertised/unadvertised classification for each call at that request boundary.
- **When**: The declaration set changes and the transcript is replayed, including after a process restart and reload from durable storage.
- **Then**: A proven advertised call may be mapped according to its original advertised identity; a proven unadvertised call is not promoted into a declared alias. Both retain their call IDs and correlated result content. The current request's declarations alone never establish historical provenance. Replay does not execute historical calls.

### Scenario: [ETS-005c] Hooks mutate historical messages
- **Given**: A hook can reorder, remove, duplicate, insert, or modify calls or their surrounding messages before replay.
- **When**: The system associates persisted provenance with the hook output.
- **Then**: It transfers provenance only for an unambiguous one-to-one identity match to the original call. Matching must not reuse one original for multiple outputs or infer provenance from name or position alone. Changed identity, duplicate candidates, missing identity, or otherwise ambiguous matches become unknown; unmatched removed calls are not attached to another call. Reordering alone preserves provenance when unique identity is established. Results remain correlated with their own calls and are not silently reassigned.

### Scenario: [ETS-005d] Ambiguous and legacy collision isolation
- **Given**: A legacy call has no persisted provenance, or a hook-mutated call has unknown provenance, and its historical name would collide with a currently advertised wire name after mapping.
- **When**: The history is serialized for replay.
- **Then**: The ambiguous call is isolated from that wire name, with its original ID and correlated result content preserved, and without executing it. A legacy name that does not collide need not be rewritten solely because provenance is absent. This deliberately favors isolation over compatibility: a formerly valid legacy call may no longer alias when its history is ambiguous and collides; its name alone cannot prove it was advertised.

### Scenario: [ETS-005e] Bounded, deterministic isolation names
- **Given**: Replay includes colliding unknown calls, including repeated names, distinct call IDs, hostile or long names, and names that resemble generated isolation names or declared aliases.
- **When**: Isolation names are generated for the same transcript and declarations, including after restart.
- **Then**: Names satisfy the provider's applicable length and character constraints, are deterministic for the same inputs, and cannot collide with declared wire names or with another distinct isolated identity in that replay. Collision handling is bounded and deterministic; exhaustion or an unrepresentable transcript fails safely rather than aliasing a call or dropping its result. No raw secret argument or result content is included in generated names or diagnostics.

### Scenario: [ETS-006] A known tool is missing from the current host catalog
- **User Goal**: Distinguish a stale or unavailable registration from malformed arguments.
- **Given**: A reachable test setup has a tool known to SDK dispatch but absent from the current host catalog at the host check.
- **When**: The provider requests that tool with otherwise valid arguments.
- **Then**: The host rejects it with a safe, actionable unavailable-registration diagnostic, distinct from an invalid-arguments error; it creates no execution intent and does not execute. If this state cannot be reached through the real boundary, document the blocker rather than equating it with an SDK-unknown tool.

### Scenario: [ETS-007] Permissions and ownership still gate calls
- **User Goal**: Prevent a valid-looking tool call from bypassing session policy.
- **Given**: A tool is filtered by workflow policy or a call fails the current owner check.
- **When**: The provider attempts the call, including with otherwise schema-valid arguments.
- **Then**: The call is denied without durable execution intent or execution; schema parity does not grant authority or restore a filtered tool.

### Scenario: [ETS-008] Rejection diagnostics handle hostile identifiers safely
- **User Goal**: Diagnose a rejection without leaking secrets or allowing untrusted text to corrupt output.
- **Given**: Rejected calls or registrations include long identifiers, control characters, secret-like identifiers, and secret argument values.
- **When**: The rejection is presented to the agent or user.
- **Then**: New diagnostics use trusted names where possible, otherwise bounded sanitized identifiers or a safe fallback; they do not contain raw argument values, secrets, or file contents. Existing input/history persistence is not represented as new diagnostic leakage or as absent.

## Automated Verification Matrix

For rejection tests, inspect durable intent by correlated call ID as well as execution effects; an empty effects table is not a positive control. A valid intent-bearing call and an invalid call of the same tool in the same session must demonstrate that the observed store path records only the admitted intent. Read-only or ordinary calls may use a different admission path and cannot alone establish that control.

The entries below describe tests present in the checkout, not accepted scenarios. Except for the focused host lifecycle pass noted in ETS-005a, execution remains unverified here. E2E uses a deterministic provider mock; supporting Go tests do not substitute for the required real-boundary E2E. Gaps listed are not exhaustive claims about unrelated suites. Acceptance remains unverified.

| Scenario ID | Current test coverage | Unverified acceptance gaps |
| :--- | :--- | :--- |
| `ETS-001` | `tests/e2e/embedded-tool-schema-parity.spec.ts`: `direct and five provider reads honor advertised pagination` exercises direct omitted, null and integer calls and checks null in the captured schema and returned content. `embedded_tool_schema_regression_test.go`: `TestIssue45ReadPaginationHostContract` supports host coverage. `tests/e2e/embedded-tool-schema-google.spec.ts` covers Google and Vertex advertised read pagination at the provider boundary. | Test execution and full advertised-contract acceptance remain unverified. |
| `ETS-001a` | Rejections E2E: `provider rejection: fractional-offset` and `fractional-limit` check that both advertised fields are integer or null, each fractional call produces one correlated persisted error and provider output, the diagnostic indicates invalid/schema/integer, and `embedded_effects` is empty. `embedded_tool_schema_regression_test.go`: `TestIssue45HostDenialsAreActionableAndSafe` directly invokes host admission for both fields and checks an error, field name, secret exclusion and no recoverable effect for the call ID. `embedded_issue45_independent_test.go`: `TestIssue45ReadNullableSchema` checks both fractional fields against the configured schema. `ETS-001` covers valid pagination separately. | E2E may reject in the SDK before host admission; it does not independently observe file access, prove absence of every intent store, or establish host dispatch. The direct host admission test does not run the real dispatcher or independently observe a file read. Execution of these tests and full acceptance remain unverified. |
| `ETS-002` | No actual-wrapper test identified. | Actual `multi_tool_use.parallel` / `functions.read` owner, boundary, captured dispatch and all three five-read variants remain unverified. Provider multi-call coverage is not a substitute. |
| `ETS-003` | The same parity E2E test sends five provider calls for each pagination variant and checks correlated outputs. | Test execution and full acceptance remain unverified; this does not exercise the actual wrapper. |
| `ETS-004` | `tests/e2e/embedded-tool-schema-rejections.spec.ts`: `provider rejection: wrong-type`, `missing-path`, `non-read-wrong-type`, and `non-read-missing` check diagnostics and no stored effects. `embedded_tool_schema_regression_test.go`: `TestIssue45HostDenialsAreActionableAndSafe` supports host rejection. `embedded-tool-schema-gap-boundaries.spec.ts`: `invalid reminder records no intent while a valid reminder records an observed intent` checks that a rejected call leaves no effect, reminder, or reminder receipt, and that a same-session positive control records an observed effect and receipt linked to a scheduled reminder in SQLite. | E2E does not separately assert execution effects for every tool or establish the host rejection boundary for SDK-invalid arguments; complete acceptance remains unverified. |
| `ETS-004a` | `embedded_coercion_diagnostic_test.go`: `TestEmbeddedDiagnosticDoesNotBlameCoercibleField` checks generic guidance for a coercible integer and invalid enum. | Host/provider E2E and broader coercion cases are not established; test execution and acceptance remain unverified. |
| `ETS-004b` | `embedded_nullable_rejection_qa_test.go`: `TestHostNullablePaginationStructuredRejection`; `tests/e2e/embedded-tool-schema-gap-boundaries.spec.ts`: `nullable pagination with invalid path reports a safe path error` checks advertised nulls, a correlated provider error mentioning the path and string type but not pagination or the fixture value, a persisted error result, and no rows in `embedded_effects`. | The diagnostic does not prove null pagination was independently validated: the SDK may reject the path first, before host validation. This E2E does not establish that the host callback was reached, independently observe file reads, or prove absence of intent outside `embedded_effects`. Full acceptance remains unverified. |
| `ETS-005` | Rejections E2E: `provider rejection: unknown` checks an unavailable diagnostic, correlated result and no stored effects. `embedded_unknown_codex_regression_test.go`: `TestCodexUnknownToolCanReachDispatch` checks adapter dispatchability. | E2E does not separately prove absence of durable intent or establish the exact rejection boundary; execution and complete acceptance remain unverified. |
| `ETS-005a` | Rejections E2E includes `provider rejection: filtered` and `provider rejection: canonical-unknown`: they check correlated rejection and no rows in `embedded_effects`; the latter checks neither worker spelling is advertised and that the conversation continues. `embedded_codex_alias_host_lifecycle_test.go`: `TestCodexUnknownAliasNamesHostLifecycle` passed against the published SDK pinned at `45705f2546ba`, exercising unknown alias-like names, replay, persisted errors and a subsequent read through the host. SDK provider tests cover the declared wire alias and rejection of its unadvertised canonical spelling, plus replay while the tool remains undeclared. | E2E execution against the current pin is not established here; an empty `embedded_effects` table alone does not prove absence of every possible intent or effect. The focused host test does not exercise selection of a declared worker alias; declared-alias host lifecycle and real-boundary acceptance remain unmet. Historical canonical unknown-to-declared replay isolation is unmet: current declaration-based mapping rewrites that historical name to the declared wire alias; no re-execution is established. Legacy provenance ambiguity remains unresolved. No cited test establishes isolation across that declaration change. |
| `ETS-005b`–`ETS-005e` | No acceptance test identified for durable per-call provenance, hook mutation, legacy collision isolation, or bounded isolation names. The current-session wrapper cannot establish historical provenance. | Unmet/unverified: deterministic real-boundary coverage must capture request-time declarations and persisted classifications, change declarations, restart and replay; exercise advertised and unadvertised calls, hook reorder/removal/insertion/duplication/identity mutation, legacy collision and non-collision, repeated and hostile names, declared/generated-name collisions and bounded failure. Assert wire names, unchanged IDs and correlated result content, and no historical re-execution. Supporting SDK tests alone do not establish host persistence or restart. |
| `ETS-006` | `embedded_tool_schema_regression_test.go`: `TestIssue45HostCurrentCatalogMiss` changes the host catalog after per-turn tool setup and checks host denial and no stored effect. | No provider-to-host E2E reaching this state identified; real-boundary reachability, no durable intent and full acceptance remain unverified. |
| `ETS-007` | Rejections E2E: `provider rejection: filtered` checks workflow filtering, rejection and no stored effects. | Owner-check denial and absence of durable intent are not established by that E2E; full policy and ownership acceptance remains unverified. |
| `ETS-008` | Rejections E2E: `provider rejection: hostile-name` checks an unavailable diagnostic, absence of fixture secrets and control characters, a 256-character diagnostic limit, and absence of a 65-character run from the hostile name. `embedded_schema_diagnostics_review_test.go`: `TestSchemaDiagnosticsUntrustedAndNonTypeFailures` checks fallback for hostile registration names and absence of unsafe field-name text. | E2E does not cover all diagnostic paths and identifier forms; test execution and full acceptance remain unverified. |

### Boundary blockers found during gap review

- **Current catalog miss (ETS-006):** `newEmbeddedTurnToolAuthority` snapshots the per-turn tool set and populates the lane's in-memory base catalog. The host test produces the mismatch by directly calling `lane.setTools` after that snapshot. No router REST or provider boundary was identified that removes a base registration between advertisement and host lookup; the tool-permission API filters active tools instead and is not a catalog removal. A real-boundary test needs a supported catalog mutation/reload trigger or an explicitly scoped test seam; an SDK-unknown call is not equivalent.
- **Owner denial (ETS-007):** `BeforeToolExecute` binds the execution user and later calls `RenewEmbeddedOwner` against the saved binding/identity. The provider mock can hold a response, but the router exposes no identified API to replace the live owner token at that point. Mutating SQLite behind the live router would bypass its ownership lifecycle and may prevent transcript persistence rather than prove a checked host denial. A deterministic real-boundary ownership handoff/fence trigger and an observable synchronization point before admission are needed; this is not demonstrated by workflow filtering.
- **Fractional read execution (ETS-001a):** Neither a safe diagnostic without fixture contents nor an empty effects table proves that the file was not opened or read. A FIFO without a writer can indefinitely block a regressed read; a nonblocking writer probe can miss a short-lived reader, and a FIFO may not follow the same path as an ordinary file. A reliable real-boundary proof needs a platform-supported filesystem access observer armed before the call, with a same-session valid-read positive control and bounded cleanup, or instrumentation around the real dispatcher that preserves its behavior. No such portable, controlled observer is established here; do not infer no read from these tests.
- **Durable intent scope:** SQLite `embedded_effects` is a durable intent store for the reminder control; the control also verifies the dedicated receipt and scheduled reminder. Other managed operations can use specialized invocation paths. The positive control proves this store is live, not that every tool's execution or every possible intent store was independently observed. The invalid read can be stopped by SDK validation before host admission.

The current wrapper's knowledge is limited to the current session; it does not establish whether a historical call was advertised. The host and SDK must agree on a durable provenance representation, hook matching identity, provider-specific name bounds, and a bounded collision/failure strategy before SDK production implementation. Those design details and their acceptance evidence remain unresolved; this spec does not assert that they are implemented.

Go test files above are under `router-proxy/internal/managed/`. Full issue acceptance requires the actual wrapper scenario; its unresolved ownership is not a passing result. Investigate and document any established mixed-batch contract before adding a corresponding scenario or changing behavior.
