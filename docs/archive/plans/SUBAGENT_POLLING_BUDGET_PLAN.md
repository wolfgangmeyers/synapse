> **Archival status: historical implementation plan with acceptance gaps, not completed.** Current reference: [replacement specification](../../specs/subagent-read-tools-budget.spec.md). See the [plan-specific completion evidence, remaining gaps, and status mismatch](../../retirement-crosswalk.md#five-plan-dispositions), including [specific unmapped acceptance](../../retirement-crosswalk.md#specific-plan-acceptance-still-unmapped). Existing implementation/test files are not a passing acceptance run. No tests were run for this docs-only move. Original content follows, with relative Markdown links repaired. Implementation evidence: `router-proxy/internal/managed/embedded_subagent_budget.go` and deterministic Go budget tests. The named browser suite only injects lifecycle snapshots and inspects the viewer. Actual shared three-credit tool consumption, one/minute refill, invalid-ID no debit, terminal-free retrieval and old warning/throttle removal still need complete E2E mapping. Replacement spec says **Active**, which does not close that gap.

# Plan: Subagent Read Tools Budget & Polling Redirection

## Overview
Replace aggressive shouting warnings (`SubagentSnapshotWarning`) and child-specific throttles (`SubagentThrottle`) on subagents with a unified, transparent subagent read credit budget modeled after the inline tools budget.
This covers both subagent read tools: `get_subagent_result` and `list_subagents`.

## Requirements & Scope
1. **Remove Old Deterrent & Throttles**:
   - Remove `SubagentSnapshotWarning` ("**MANDATORY**!!! This is a snapshot only. Do NOT repeatedly pull a running result!!! Rest and wait for completion; a completion notification will arrive automatically.") from all locations:
     - `FormatSubagentResultReportWithOptions` (where it was prepended to running and queued snapshots).
     - Tool description of `get_subagent_result` (replace with a clean, professional description).
     - Error message when `wait` argument is passed (replace with clean rejection).
   - Remove `SubagentThrottle`, `SubagentThrottleKey`, `SubagentThrottleOptions`, and `ErrSubagentPollThrottled`.
   - Clean up or update obsolete unit tests that asserted on the old throttle or warning.

2. **Implement Subagent Read Budget**:
   - Modeled directly on the inline tool replenishment budget in `managedAgentLane`:
     - Constants:
       - `SubagentReadPoolCapacity = 3` (3 credits maximum)
       - `SubagentReadReplenishmentIntervalMS = 60_000` (1 credit per minute, 60,000 ms)
     - Fields on `managedAgentLane`:
       - `subagentReadPoolRemainingMs int64`
       - `subagentReadPoolUpdatedAt time.Time`
     - Lane methods:
       - Place in dedicated file `router-proxy/internal/managed/embedded_subagent_budget.go`.
       - Guard with `l.activeMu.Lock()` / `defer l.activeMu.Unlock()`.
       - `replenishSubagentReadPool(now time.Time)`:
         Initializes to full capacity (3 credits = 180,000 ms) on first check (`isZero()`).
         Guards negative elapsed time (`if elapsedMs < 0 { elapsedMs = 0 }`).
         Adds elapsed ms capped at `SubagentReadPoolCapacity * SubagentReadReplenishmentIntervalMS`.
       - `consumeSubagentReadCredit(now time.Time) error`:
         Replenishes pool.
         If `subagentReadPoolRemainingMs < SubagentReadReplenishmentIntervalMS`, returns the redirection error:
         `"Subagent read budget exhausted: you have used all available subagent read credits. Subagent read tools (get_subagent_result and list_subagents) are intended only for occasional checks; repeated polling of subagent status is disallowed. You must rest, continue unblocked work, or wait for the automatic completion notification instead of polling. (Credits replenish at 1 credit per minute up to a maximum of 3.)"`
         Otherwise deducts `SubagentReadReplenishmentIntervalMS` (1 credit) and returns `nil`.
   - Tool integration:
     - Use `m.now()` (`m.clock.Now()`) to ensure deterministic clock testing support.
     - In `m.embeddedListSubagentsTool(parent)`:
       Calls `lane.consumeSubagentReadCredit(m.now())`. If budget exhausted, returns the redirection error.
     - In `m.embeddedGetSubagentResultTool(parent)`:
       Fetches record for `agentID` via store first (so invalid IDs return 404/not-found without debiting credits).
       Checks status with `IsQueuedOrRunning(record.Status)`.
       If `record.Status` is `running` or `queued`: calls `lane.consumeSubagentReadCredit(m.now())`. If budget exhausted, returns the redirection error.
       If `record.Status` is terminal (`completed`, `failed`, `stopped`, `timed_out`): consumes NO credits (free retrieval of final deliverables).

3. **Tool Description & Argument Updates**:
   - `get_subagent_result` tool description:
     `"Retrieve execution results, status, metrics, or detailed transcripts for a background subagent."`
   - `wait` argument error:
     `"unsupported argument \"wait\": result retrieval is snapshot-only"`

4. **Testing Strategy**:
   - Unit & Functional tests in `router-proxy/internal/managed/`:
     - Test clean descriptions and removal of warning string.
     - Test `list_subagents` budget consumption (3 calls consume 3 credits, 4th call is rejected with redirection message).
     - Test `get_subagent_result` on running/queued subagent consumes credit and rejects on exhaustion.
     - Test `get_subagent_result` on completed subagent succeeds even when budget is 0.
     - Test replenishment over time (1 credit added after 60s).
     - Test `wait` argument returns clean error.
   - E2E Playwright test in `tests/e2e/subagent-read-tools-budget.spec.ts`:
     - Test that an agent calling subagent read tools is subject to the budget and receives the redirection error upon exhaustion without UI regressions.

## Proposed Changes by File

### 1. `router-proxy/internal/managed/embedded_subagent_results.go`
- Remove `SubagentSnapshotWarning` constant.
- In `FormatSubagentResultReportWithOptions`, remove `SubagentSnapshotWarning + "\n\n"` prepended to running and queued snapshots.
- Remove `SubagentThrottle`, `SubagentThrottleKey`, `SubagentThrottleOptions`, `ErrSubagentPollThrottled`, `NewSubagentThrottle`, `Allow`, etc.

### 2. `router-proxy/internal/managed/embedded_subagent_public_tools.go`
- Remove `m.subagentThrottle()` and `m.subagentThrottleInstance`.
- Update `embeddedGetSubagentResultTool`:
  - Set description to `"Retrieve execution results, status, metrics, or detailed transcripts for a background subagent."`
  - Set `wait` error to `"unsupported argument \"wait\": result retrieval is snapshot-only"`
  - Replace throttle check with `lane.consumeSubagentReadCredit(now)` when `record.Status` is `running` or `queued`.
- Update `embeddedListSubagentsTool`:
  - Enforce `lane.consumeSubagentReadCredit(now)`. If exhausted, return redirection error.

### 3. `router-proxy/internal/managed/embedded_subagent_budget.go`
- Dedicated file for Subagent Read Budget logic:
  - `SubagentReadPoolCapacity = 3`
  - `SubagentReadReplenishmentIntervalMS = 60_000`
  - `SubagentReadExhaustionError = "Subagent read budget exhausted: you have used all available subagent read credits. Subagent read tools (get_subagent_result and list_subagents) are intended only for occasional checks; repeated polling of subagent status is disallowed. You must rest, continue unblocked work, or wait for the automatic completion notification instead of polling. (Credits replenish at 1 credit per minute up to a maximum of 3.)"`
  - Methods on `*managedAgentLane`:
    - `replenishSubagentReadPool(now time.Time)`
    - `consumeSubagentReadCredit(now time.Time) error`
    - `subagentReadPoolRemaining(now time.Time) (float64, int)` for inspection/testing.

### 4. `router-proxy/internal/managed/supervisor.go`
- Add `subagentReadPoolRemainingMs int64` and `subagentReadPoolUpdatedAt time.Time` to `managedAgentLane` struct.
- Remove `subagentThrottleInstance *SubagentThrottle` from `Manager` struct if present.

### 5. `router-proxy/internal/managed/embedded_subagent_results_test.go` and `embedded_subagent_public_tools_test.go`
- Update existing tests to remove references to `SubagentSnapshotWarning` and `SubagentThrottle`.
- Add test coverage for subagent read budget consumption, replenishment, exhaustion redirection, and terminal status exemption.

### 6. `tests/e2e/subagent-read-tools-budget.spec.ts`
- E2E test verifying behavior in real browser / router environment.

## Verification Checklist
- [ ] No occurrences of `SubagentSnapshotWarning` or shouting text remaining in codebase.
- [ ] Unit tests pass: `go test ./router-proxy/internal/managed/...`
- [ ] Router builds cleanly: `go build -o router-proxy/mecha-mailbox-router ./router-proxy`
- [ ] Behavioral tests and Playwright E2E suite pass.
- [ ] Robot review pass.
