# Spec: Claude child capacity and cancellation

- **ID**: `SPEC-CLAUDE-CHILD-CAPACITY-001`
- **Status**: `Draft` — acceptance incomplete
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/claude-child-reservations.spec.ts`, `tests/e2e/claude-child-capacity-fixture.spec.ts`

Claude and Pi children use the same manager scheduler and configured global and per-parent limits. Waiting work remains queued without consuming execution time. No separate process-wide Claude capacity ledger coordinates manager lifetimes.

## Scenarios

### [CCC-001] Healthy concurrency and queueing
Eligible children run up to configured manager and per-parent limits. Additional children wait, without execution timeout, until a slot is available.

### [CCC-002] Cancellation and late results
Stopping or timing out a child promptly settles its outcome and closes its process. Its manager scheduler slot remains held until tool cleanup completes; an unconfirmed process stop retains the slot and fences further Claude process admission. This manager-local accounting does not coordinate replacement managers. Late tool results cannot restore authority, change the terminal outcome, enter history or model inputs, or authorize a retry of an uncertain Claude execution. Existing unconfirmed-process fencing remains in force.

### [CCC-003] Stop a queued attempt
Stopping a queued child removes that attempt from the scheduler and registry; stale completion or release from a previous attempt cannot start it or remove a replacement.

## Verification

`claude-child-reservations.spec.ts` covers per-parent saturation and queue wake. The tagged `claude-child-capacity-fixture.spec.ts` holds a cancellation-ignoring mock tool, verifies a queued child stays queued until cleanup is released, then verifies the stopped outcome, late-result authority and retry protection. Coverage is partial; other timing and manager replacement cases remain unaccepted.
