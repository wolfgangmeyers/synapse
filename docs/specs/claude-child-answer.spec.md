# Spec: Claude child answer settlement

- **ID**: `SPEC-CLAUDE-CHILD-ANSWER-001`
- **Status**: `Draft` — acceptance incomplete
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/claude-child-answer-settlement-supplement.spec.ts`

## Intent & user outcome

A parent receives the child's current complete answer only when the child's tool results are properly acknowledged; otherwise it receives a failure rather than a stale or unsupported answer.

## Core invariants

1. Text blocks belonging to the current assistant message are delivered in order; a later assistant message replaces the previous candidate.
2. A tool call or user message invalidates an earlier candidate answer.
3. A successful answer requires matching results for checked tool executions; unacknowledged, mismatched or unchecked results cannot settle successfully.

## User scenarios

### Scenario: [CCA-001] Current multi-block answer
- **User Goal**: Receive the complete current answer, not an obsolete candidate.
- **Given**: A Claude child emits assistant text after its last user or tool result.
- **When**: It emits multiple blocks with the same message ID, a later assistant message, a tool call, or a user message and then finishes.
- **Then**: Blocks of the current message are delivered in order; a later assistant message replaces the candidate, while a tool call or user message invalidates it. Without a current final answer the child fails.

### Scenario: [CCA-002] Tool result acknowledgement
- **User Goal**: Receive an answer only after checked tool results are acknowledged.
- **Given**: A child executes a checked tool, or emits a tool result without a checked producer.
- **When**: Claude finishes with a matching, missing, mismatched, or unchecked tool result.
- **Then**: A matching result followed by a final answer may complete; missing, mismatched, or unchecked results fail rather than reporting an answer.

## Automated E2E verification matrix

| Scenario ID | Scenario description | E2E suite | Test case name |
| --- | --- | --- | --- |
| `CCA-001` | Blocks, replacement and invalidation | `tests/e2e/claude-child-answer-settlement-supplement.spec.ts` | `CCA settlement: multi-block`; `CCA settlement: later assistant replaces candidate`; `CCA settlement: tool call invalidates candidate`; `CCA settlement: user message invalidates candidate` |
| `CCA-002` | Checked result acknowledgement | `tests/e2e/claude-child-answer-settlement-supplement.spec.ts` | `CCA settlement: matching`; `CCA settlement: unacknowledged`; `CCA settlement: mismatched`; `CCA settlement: unchecked` |

All eight cases were reported passed in `/tmp/mecha-batch25.s4gOVH/browser.log` (exit 0); the suite matches that evidence checkout. They check persisted child outcomes and parent notices using a fake Claude process. They cover the listed single-tool examples, not multiple concurrent or repeated tool calls, duplicate or reordered acknowledgements, or every mixed content ordering; those changed-behavior combinations remain unverified by this matrix. `TestClaudeChildAnswerBlockAndAcknowledgement` is supporting Go coverage. This batch is not full acceptance of the wider Claude lifecycle; this spec remains Draft.
