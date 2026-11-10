# Spec: Background bash owner lifetime fencing

- **ID**: `SPEC-BB-FENCE-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/background-bash-owner-isolation.spec.ts`

## Intent & User Outcome

An agent's background processes are cleaned up when its owner lifetime ends; failures are reported rather than treated as successful cleanup. Old work cannot launch more processes after cleanup, and normal owner turnover never exhausts a permanent fence limit.

## Core Invariants

1. Launch authority belongs to one revocable owner lifetime, not a reusable owner string.
2. A launch racing retirement is either included in cleanup or rejected. Once revoked, an owner remains revoked even if cleanup fails. During reset, launch admission is held until cleanup and the epoch outcome are known; a confirmed rollback after successful cleanup does not itself revoke the unchanged owner.
3. Normal owner turnover does not exhaust launch admission, and stale owners remain unable to launch. Uncertain jobs remain retained under the existing cleanup rules.
4. A successor cannot be published before successful predecessor cleanup, and stale authority cannot launch on its behalf.

## User Scenarios

### Scenario: FENCE-001 Owner cleanup races a launch
- **Given**: An owner has a background bash launch in flight.
- **When**: The owner is cleaned up concurrently.
- **Then**: The launch is rejected or its job is included in cleanup; later launches using the old lifetime are rejected.

### Scenario: FENCE-002 Many owner lifetimes retire
- **Given**: More than 4096 owner lifetimes have been cleaned up.
- **When**: A new owner launches a background job.
- **Then**: The launch is not rejected due to retired owner history, and retained stale capabilities remain revoked.

### Scenario: FENCE-003 Cleanup cannot be confirmed
- **Given**: An owner's job cleanup fails or is uncertain.
- **When**: Cleanup is retried or the old owner attempts another launch.
- **Then**: The old owner remains revoked, the cleanup failure is not forgotten, and a successor is not published as if cleanup had succeeded.

### Scenario: FENCE-004 Reset validates and retires the expected owner
- **Given**: A reset carries an old binding, or owned background work cannot be confirmed stopped.
- **When**: The reset runs.
- **Then**: A stale binding does not interrupt or stop the successor. Cleanup failure leaves the current epoch in place and the old capability revoked, so cleanup can be retried. If cleanup succeeds and the epoch transaction is confirmed rolled back, the unchanged owner remains current and can launch only if its capability was not already revoked. If authority cannot be established, the old owner is not allowed to launch as though rollback were confirmed.

### Scenario: FENCE-005 Reset races owner replacement
- **Given**: A reset is requested for an owner while another owner replaces it.
- **When**: The reset drains its runtime and stops its process and background jobs.
- **Then**: Every destructive action applies only to the old owner targeted by the reset. The successor's runtime, process, and jobs are not interrupted or stopped, even if replacement occurs after validation.

### Scenario: FENCE-006 Atomic reset owner handoff and retry
- **Given**: Cleanup for a reset has succeeded.
- **When**: The session epoch changes.
- **Then**: The new epoch and its next owner become effective together. A confirmed rollback after successful cleanup leaves the unchanged owner and its existing authority intact; it does not reverse a prior revocation. If the commit outcome is unknown, the old owner is not treated as authoritative without confirmation. A retry must not claim or overwrite a different successor; a stale reset cannot publish an owner over a successor.

### Scenario: FENCE-007 Reset after a committed tool call
- **Given**: A reset tool call completes while the owner is replaced or another reset is in progress.
- **When**: Its deferred reset runs or resumes after the other coordinator.
- **Then**: It acts only on the owner of the completed tool call. It cannot interrupt, close, stop, or unpublish a successor.

### Scenario: FENCE-008 Blocked launch cleanup and deadline
- **Given**: An admitted launch cannot finish process identification cleanup because signaling is blocked.
- **When**: Owner retirement or manager shutdown reaches its deadline.
- **Then**: It returns a deadline error without admitting another launch or claiming cleanup succeeded. Cleanup must not signal a process whose identity is no longer verified. A later cleanup can retry. A launch waiting for admission also respects cancellation.

### Scenario: FENCE-009 Blocked process creation or identification
- **Given**: An admitted background launch is blocked starting or identifying its child.
- **When**: Shutdown or retirement reaches its deadline, or the owner capability is revoked while another launch waits.
- **Then**: Shutdown and retirement return a deadline error without losing track of the admitted launch; waiting launches promptly fail as retired. Cleanup can be retried after the launch settles, and cleanup must not signal an unrelated process.

### Scenario: FENCE-010 Retirement times out before an admitted launch settles
- **Given**: Owner termination or replacement fences an admitted background launch that is still starting or identifying its process.
- **When**: Retirement reaches its deadline and its caller does not retry, and the launch subsequently settles.
- **Then**: Retirement reports the deadline, stale launches stay fenced, and cleanup proceeds after the launch settles without requiring the caller to retry, targets only that retired owner, and remains bounded even after repeated retirement timeouts. A cleanup failure remains visible in diagnostics; no general retry service is promised. Shutdown retains its existing deadline.



## Automated E2E Verification Matrix

The owner-isolation Playwright case covers foreign job access, not the lifecycle scenarios below. Each scenario still needs browser-level end-to-end evidence. The supplemental tests named here exist; their presence alone does not establish a passing run or full scenario coverage.

| Scenario | E2E status | Supplemental coverage and remaining gap |
| --- | --- | --- |
| FENCE-001 | Pending: cleanup versus launch. | `TestBackgroundBashStopOwnerFencesConcurrentLaunch` and `TestBackgroundBashCapabilitySerializesLaunchAndCleanup` exercise selected races. |
| FENCE-002 | Pending: managed-boundary lifetime churn and stale invocation. | `TestBackgroundBashCapabilityRetirementChurn` exercises capability churn. |
| FENCE-003 | Pending: uncertain cleanup and successor admission. | `TestBackgroundBashCapabilityCleanupFailureRemainsRevoked` and `TestResetCleanupFailureAndTransactionRetryRealStore` exercise failure and retry. |
| FENCE-004 | Pending: stale reset and cleanup/transaction failure through E2E boundaries. | `TestResetStaleBindingDoesNotInterruptSuccessorRealStore`, `TestResetCleanupFailureAndTransactionRetryRealStore` and `TestResetTransactionFailurePreservesOwnerRealStore` cover selected real-store paths. |
| FENCE-005 | Pending: reset/replacement race across full boundaries. | `TestClaudeStopSnapshotReplacementBarrier` and `TestResetCompareAndClearPreservesReplacement` cover selected handle races; complete race coverage remains pending. |
| FENCE-006 | Pending: reset handoff, rollback and ambiguous outcome. | `TestResetSeedFailureRetainsOldOwner` and `TestResetPostCommitErrorPublishesOwner` cover selected real-store outcomes; complete ambiguous-outcome and successor races remain pending. |
| FENCE-007 | Pending: committed tool reset racing replacement across full boundaries. | `TestCommittedToolResetBrowserHandoff` and `TestResetHandoffTransfersOnlyMatchingDeferredGate` cover selected handoff paths; the successor race remains pending. |
| FENCE-008 | Pending: blocked cleanup and deadlines through E2E boundaries. | `TestBackgroundBashRetirementBlockedSignalDoesNotBlockShutdownOrOtherJob` and `TestManagerCloseDeadlineWithBlockedAdmittedBackgroundLaunch` cover selected blocked paths. |
| FENCE-009 | Pending: blocked start or identification and revocation through E2E boundaries. | `TestManagerCloseDeadlineWithBlockedAdmittedBackgroundLaunch` and `TestBackgroundLaunchTimeoutDuringIdentification` cover selected manager paths. |
| FENCE-010 | Pending: retirement timeout and eventual cleanup through E2E boundaries. | `TestRetirementTimeoutCleansAdmittedLaunchWithoutRetry` and `TestRetirementDeferredCleanupScheduledOnce` cover selected deferred cleanup paths. |

These are draft target scenarios, not a claim of acceptance.
