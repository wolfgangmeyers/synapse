# Spec: Implicit Workflow Return to Previous State

- **ID**: `SPEC-WORKFLOWS-IMPLICIT-RETURN-001`
- **Status**: `Draft` — implementation and tests exist; full acceptance remains unmet
- **Domain**: `Workflows`
- **Target E2E Suite**: `tests/e2e/workflow-implicit-return*.spec.ts` and focused workflow return suites (partial; see matrix)

## 1. Intent & User Outcome

A workflow author can allow an agent to return from a state to the state it just left without drawing a reverse edge. The option is local to the current state and off by default. Existing workflows and explicit transitions continue to work as before.

## 2. Core Invariants

1. `allow_implicit_return` belongs to a state, defaults to false, and requires no separate confirmation. The current state's current setting governs each new attempt; tracking the predecessor does not depend on that setting.
2. A binding has at most one predecessor: the state left by its last successful transition. It is not a stack, arbitrary history, or a message sender. Activation starts with none. A successful return also replaces the predecessor with the state just left.
3. An implicit target must exist, belong to the active workflow, and differ from the current state. Explicit edges and exit authorization remain independent of the toggle and invalid predecessor data; existing cross-workflow explicit edges and legacy runtime self-edges remain usable. This does not relax create/update validation.
4. A successful transition updates the current state and predecessor together atomically; a rejected transition changes neither and runs no transition effects. Concurrent attempts must not overwrite a newer binding with a stale predecessor.
5. A return uses the existing transition lifecycle: history, notifications, instructions, restrictions, variables, goals, reminders, monitors and exit behavior follow the same rules as an explicit transition. This feature does not require atomic lifecycle serialization or admission.
6. The predecessor persists across restart with the active binding, but is cleared on exit, deactivation, reassignment or new activation (even of the same workflow), deletion, and administrative replacement of the binding or state outside a normal transition. Migration never infers it from history.
7. The option is definition data; the predecessor is runtime binding data. Duplication and supported bundle and git-sync round trips preserve true and false; old bundles default to false. Export must not contain a runtime predecessor or invent a graph edge. No forward compatibility with older consumers is assumed.
8. This feature does not add arbitrary graph bypass, cross-agent routing, a history browser, changes to goals or snooze, a retry/idempotency protocol, atomic lifecycle serialization or admission, an RPC protocol, or a broad lifecycle recovery framework.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [IR-024] Understand return paths on the canvas
- **User Goal**: Understand which states permit a return without mistaking a conditional return for a guaranteed route.
- **Given**: A workflow canvas with states with and without implicit return, including dead ends with and without the option.
- **When**: The author views the canvas, changes the option, and reloads it.
- **Then**: Each enabled state displays an auto-return badge, including states with outgoing edges or exit permission; disabled states do not. A concise explanation is always visible beneath the badge, stating that return requires a different previous state in the active workflow; it does not require hover, touch, or keyboard interaction. A dead end without return remains a red danger, while a return-enabled dead end is a warning explaining the predecessor limitation and possible trap. No implicit edge is drawn. Toolbar actions use the dark visual treatment of the application's tabs.

### Scenario: [IR-001] Default-off migration and creation
- **User Goal**: Keep existing and newly created workflows compatible.
- **Given**: A populated database with existing states and an active binding, or a new state or older bundle without the option.
- **When**: The database is migrated, the state is created, or the bundle is imported.
- **Then**: The option is false; migration does not infer a predecessor. After A→B without an explicit B→A edge, B cannot return to A while disabled.

### Scenario: [IR-002] Configure the current state
- **User Goal**: Enable or disable returns per state.
- **Given**: An agent reached B from A, B has no explicit edge to A, and B's option is off.
- **When**: The author enables B using an accessible labeled editor control, saves and reloads, and the agent requests A; later the author disables B.
- **Then**: The editor explains that return requires an existing, different predecessor in the active workflow; the saved setting survives reload and the return succeeds while enabled. A later implicit return from B is unavailable when disabled. Other states' settings are unchanged; new-state controls start off.

### Scenario: [IR-003] API and administration configuration
- **User Goal**: Manage the same option through supported interfaces.
- **Given**: States with both option values.
- **When**: A user creates, reads, lists and updates them through REST and managed workflow administration tools, including an update omitting the option and one explicitly setting false.
- **Then**: Representations and tool schemas expose the value; omission on update preserves it, explicit false disables it, and omission on creation defaults to false. Subsequent attempts use the updated value.

### Scenario: [IR-004] Only the immediately previous state
- **User Goal**: Return one step rather than bypass the workflow graph.
- **Given**: An agent transitions A→B→C and C enables implicit return, with no explicit C→A or C→B edge.
- **When**: It requests A and then B.
- **Then**: A is rejected and B succeeds. The new predecessor is C. Enabling the setting after arrival also permits return to the actual last state, even if tracking began while disabled.

### Scenario: [IR-005] Returning updates the predecessor
- **User Goal**: Understand repeated returns.
- **Given**: A→B succeeds and B can return to A.
- **When**: B→A succeeds, followed by a request for B.
- **Then**: B is now A's predecessor; the request succeeds only if A enables implicit return or has an explicit edge to B. A fresh activation has no implicit target.

### Scenario: [IR-006] Existing explicit transition compatibility
- **User Goal**: Continue using defined transitions and exit.
- **Given**: The option is off, an explicit reverse or cross-workflow edge exists, or a legacy explicit self-edge is present at runtime.
- **When**: The authorized agent uses that edge or an authorized exit.
- **Then**: Existing explicit and exit behavior remains available. A predecessor resulting from a cross-workflow edge is eligible for implicit return only if it belongs to the active workflow. Existing creation and update validation is unchanged.

### Scenario: [IR-007] Guidance and target resolution
- **User Goal**: See and select only currently available targets.
- **Given**: A binding has a valid predecessor and the current state enables return, or it has no eligible predecessor, or the target is also an explicit edge.
- **When**: Agent transition, background, assignment or HTTP guidance is generated and an authorized agent uses the exposed `mecha_workflow_transition` tool with an ID or case-insensitive name.
- **Then**: Guidance includes an eligible return target once, never implies the toggle alone creates a target, and preserves normal explicit targets. Existing ID/name resolution works. The transition tool factory also supports the direct `workflow_transition` alias with the same return behavior; this does not require exposing that alias in the managed tool catalog. Explicit edges are checked first in configured order, so an explicit target name collision takes precedence over the implicit predecessor, including when the input is the predecessor ID matching an explicit target name. The canvas does not draw a phantom edge; dead-end messaging describes the conditional return accurately.

### Scenario: [IR-008] Rejection and stale predecessors
- **User Goal**: Prevent unauthorized or invalid transitions without disrupting valid edges.
- **Given**: No binding or predecessor exists; a requested target is missing, neither an edge nor predecessor, or the current state itself without an explicit self-edge; the caller is an unauthorized subagent; or the predecessor was deleted, moved or otherwise became stale.
- **When**: The agent attempts the invalid transition.
- **Then**: It is rejected without changing state or running transition effects. The rejection lists the currently eligible explicit and implicit targets, or uses the existing no-outgoing-transitions message when none are available. Target input retains existing whitespace trimming and case-insensitive name matching. A stale predecessor cannot authorize return or block a valid explicit edge. Supported state and workflow deletion clean up affected bindings.

### Scenario: [IR-009] Normal state-entry behavior
- **User Goal**: Make an implicit return behave like an ordinary successful transition.
- **Given**: An eligible return target with state-entry configuration.
- **When**: The agent returns using the actual transition tool.
- **Then**: Normal transition history and WebSocket notifications occur, and instructions, tool restrictions, reminders, monitors, variables, goals and exit behavior follow the same rules as an explicit transition, including existing authorization and reason handling.

### Scenario: [IR-010] Restart and binding lifecycle
- **User Goal**: Keep a live binding across restart without leaking its predecessor into a new one.
- **Given**: A binding has an eligible predecessor and configured states.
- **When**: The router restarts and reconnects; subsequently the workflow exits, deactivates, is reassigned (including to the same workflow), is administratively replaced, or a fresh background run starts.
- **Then**: The original active binding and configuration survive restart; each replacement or fresh activation has no inherited predecessor. Deletion clears affected runtime binding data.

### Scenario: [IR-011] Duplication and portability
- **User Goal**: Move or duplicate workflow definitions without moving runtime history.
- **Given**: A workflow contains states with true and false settings, and another bundle predates the setting.
- **When**: The user duplicates, exports and imports, or git-syncs the workflow, and imports the older bundle.
- **Then**: Both explicit values survive supported round trips, missing old values become false, and no runtime predecessor or implicit graph edge is exported. Compatibility documentation does not promise that older consumers understand the new field.

### Scenario: [IR-012] Competing binding updates
- **User Goal**: Avoid a state and predecessor that describe different transitions.
- **Given**: Two transitions compete from the same binding, or a transition competes with replacement or deactivation of that binding.
- **When**: The operations complete.
- **Then**: Each successful transition records the state it left as predecessor in the same atomic binding update. A stale attempt cannot overwrite a newer binding; a rejected attempt has no transition effects.

## 4. Automated E2E Verification Matrix

The mappings below identify existing candidate coverage, not a claim of complete or passing acceptance. All scenarios remain **partial** until their full contracts pass the required browser, WebSocket, REST, router-proxy and SQLite boundaries with deterministic LLM mocks. Focused Go and component tests are supporting evidence only.

| Scenario | E2E suite | Test case or coverage |
| :--- | :--- | :--- |
| IR-001 | `tests/e2e/workflow-implicit-return.spec.ts`; `tests/e2e/workflow-pre130-migration.spec.ts` | Defaults and omitted values; pre-129 state configuration and an active workflow binding survive migrations 129/130 and restart with return off and no predecessor inferred from history; pre-130 upgrade likewise does not infer a predecessor. The managed functional test `router-proxy/internal/managed/workflow_migration_return_test.go` invokes the real tool on an upgraded pre-129 binding with history and checks rejection and retained effects; this is not a browser or running-agent lifecycle test. |
| IR-002 | `tests/e2e/workflow-implicit-return.spec.ts` | Accessible editor setting and return after enabling. |
| IR-003 | `tests/e2e/workflow-implicit-return.spec.ts`; `tests/e2e/workflow-implicit-return-admin.spec.ts`; `tests/e2e/workflow-return-missing-lifecycle.spec.ts` | REST and managed administration configuration. |
| IR-004 | `tests/e2e/workflow-implicit-return.spec.ts`; `tests/e2e/workflow-return-late-enable-effects.spec.ts` | Immediate predecessor and late enable. |
| IR-005 | `tests/e2e/workflow-implicit-return.spec.ts`; `tests/e2e/workflow-return-edge-acceptance.spec.ts` | Repeated returns and disabled predecessor with explicit edge. |
| IR-006 | `tests/e2e/workflow-return-edge-acceptance.spec.ts` | Legacy self and cross-workflow explicit edges. |
| IR-007 | `tests/e2e/workflow-return-target-collision-review.spec.ts`; `tests/e2e/workflow-return-missing-lifecycle.spec.ts`; `router-proxy/internal/managed/embedded_workflow_implicit_return_test.go` (`TestWorkflowImplicitReturnTool`) | Target collision, deduplicated explicit reverse guidance, and direct factory alias return. |
| IR-008 | `tests/e2e/workflow-implicit-return.spec.ts`; `tests/e2e/workflow-return-edge-acceptance.spec.ts` | Invalid and deleted predecessor rejection. |
| IR-009 | `tests/e2e/workflow-return-entry-contract.spec.ts` | Return entry effects compared with an explicit transition. |
| IR-010 | `tests/e2e/workflow-implicit-return.spec.ts`; `tests/e2e/workflow-return-missing-lifecycle.spec.ts` | Restart, exit and fresh activation. |
| IR-011 | `tests/e2e/workflow-implicit-return.spec.ts`; `tests/e2e/workflow-implicit-return-git-sync.spec.ts` | Definition round trips, older import and git sync. |
| IR-024 | `tests/e2e/workflow-implicit-return.spec.ts` | Canvas badges, editor toggle persistence, dead-end warnings, explicit edges and toolbar styling. |
| IR-012 | `tests/e2e/workflow-implicit-return-concurrency.spec.ts` | Partial concurrency coverage; competing binding updates need full E2E evidence. |

Acceptance requires passing tests for every scenario and the required system boundaries. No new test run is claimed here.
