# Spec: Claude process admission and recovery

- **ID**: `SPEC-AGENT-CLAUDE-FENCE-001`
- **Status**: Draft
- **Domain**: AgentManagement
- **Target E2E Suite**: `tests/e2e/claude-code-orphan-recovery.spec.ts`

## Intent & User Outcome
A router never starts replacement work for a Claude worker while its old process cleanup is unsafe, without unnecessarily blocking unrelated work. Shutdown retains ownership until admitted work finishes cleanup.

## Core Invariants
1. The manager's admission fence covers workers, children, authentication probes and evaluators.
2. Shutdown closes admission before waiting for all admitted process operations and their cleanup.
3. Orphan records are removed durably only after confirmed absence. Unconfirmed cleanup blocks unsafe replacement work and retains recovery evidence. When the affected worker is known, fence only that worker; unrelated workers continue. Retain the global fence for unattributable or shared startup failures and shutdown.

## User Scenarios

### Normal work and shutdown
Given a running manager and admitted Claude work, when the manager shuts down, then new Claude work is rejected and shutdown waits for admitted work to finish cleanup before returning.

### Slow and unconfirmed cleanup
Given a Claude process group has been stopped but has not yet exited, when cleanup checks for its absence, then it briefly retries within a bounded interval before treating the cleanup as failed. If the group exits within that interval, cleanup succeeds and the worker can run again without a spurious failure.

Given cleanup remains unconfirmed after that interval, replacement work for the known affected worker is blocked and orphan evidence is retained; unrelated Claude workers continue. The caller sees the actual process cleanup error, not advice to install the Claude CLI; a preflight or other preceding error must not hide it. Do not claim cleanup succeeded or signal an unverified group. Unattributable or shared startup failures retain the global fence; shutdown retains its global admission fence.

### Orphan record failure
Given a process has stopped but removal or directory synchronization of its orphan record fails, when cleanup completes, then the affected worker is fenced and the failure is reported rather than silently leaving an unsafe stale record. Unattributable or shared startup bookkeeping failures retain the global fence.

### Runtime initialization failure
Given a Claude turn cannot initialize its runtime, when initialization fails before a process starts, then the router reports the error without crashing and releases the admitted execution so later work is not blocked by that failed attempt.

## Acceptance and Coverage
Unit regressions cover admission, shutdown synchronization and cleanup failures. `tests/e2e/claude-code-orphan-recovery.spec.ts` covers cleanup of a verified live orphan, mismatch fencing, and continued Pi availability. The existing worker E2E suite covers the normal managed process lifecycle; other browser-level fault injection remains required before this draft can become active.

### Fence racing an admitted start
Given Claude work was admitted but its executable has not been released, when a relevant cleanup failure fences that start or shutdown begins, then the waiting executable must not run. Gate release and fencing have one ordering: a release that wins may run and remains owned through cleanup; a fence that wins prevents release. This applies to roots, children, authentication probes and evaluators. Cleanup and durable callbacks must not run while holding the release lock.

### Crash during orphan publication
Given a Claude launch is waiting at its execution gate, when its recovery record is created, then the record and the directory containing it are durable before the gate is released, including on first directory creation. A crash during an incomplete write leaves only a recognizable unpublished temporary file; startup removes such files before admitting work. A crash after publication leaves a complete record for normal identity-verified recovery. Unexpected or untrusted entries and malformed published records still block admission. Any write, sync, publish, or cleanup failure prevents the affected release; failures in shared recovery bookkeeping may also fence admission. Concurrent launches cannot overwrite one another's records.

Deterministic process-level crash/failure regressions are required in addition to the worker E2E suite; browser-level fault injection remains a draft acceptance gap.

### Recovery without a pinned process group
Given a recorded group survives router restart, recovery rechecks a matching live leader identity and signals the group, then confirms absence before removing the record. Mismatched, leaderless or uncertain groups are not signaled; their evidence is retained and the affected worker is fenced when identifiable. This check does not pin the process group: PID or group ID reuse between the check and signal can target an unrelated group. Ordinary home-directory permissions do not prevent recovery.

An unsafe orphan or recovery failure does not prevent router startup or Pi work. A known affected Claude worker is fenced; unrelated Claude workers continue. Unattributable or shared startup failures retain the global fence and a truthful recovery diagnostic. Existing orphan records contain no worker owner; an unsafe or failed recovery of such a record is unattributable and globally fences Claude admission, not just one worker. Recovery immediately rechecks a matching live identity before SIGKILL and confirms absence within a bounded time. Mismatched and unknown identities remain untouched and may require operator intervention.

Uncertain or unconfirmed groups may require operator intervention when safety cannot be established. A different Linux boot may still retire a valid record because processes cannot survive reboot; weak or unavailable boot identity on other platforms must never justify retiring a record as belonging to an earlier boot. Deterministic process-boundary regressions must cover live groups, identity mismatch, and weak markers; browser-level fault injection remains a draft acceptance gap.

### Recovery ledger bookkeeping
Given concurrent Claude launches near the 1024-entry recovery limit, when records are published, then the ledger never exceeds that limit and excess launches remain gated. Recovery rejects unexpected published filenames even if their contents decode. Removing recovery records and temporary prompt or configuration files is followed by syncing their containing directories; sync failures fence the affected worker when attributable; shared or unattributable startup failures retain the global fence. Creation persists newly created directory entries through their ancestors without imposing private permissions on ordinary home directories.

### Degraded startup diagnostic
Given orphan recovery fails, when the router starts in degraded mode, then the known affected Claude worker is fenced while unrelated workers continue; unattributable or shared startup failures retain the global fence. Unrelated runtimes remain available. Admission errors, the recovery diagnostic and shutdown error identify the safe failure category (including an operating-system error when available) without exposing paths, filenames, record contents, or other sensitive data. A filesystem failure must not be reported as an unconfirmed process-group cleanup. Focused regression: `TestClaudeStartupRecoveryReportsSafeFilesystemCause`; browser-level fault injection remains a draft acceptance gap.

### Required issue #48 verification

The draft acceptance gap includes deterministic process-boundary and browser E2E coverage for a group that exits during the bounded retry, and an unconfirmed group that fences its known worker while an unrelated Claude worker continues. Verify retained orphan evidence, a truthful cleanup diagnostic even after a preceding error, unresolved identity and bookkeeping failures, and the global fence for unattributable or shared startup failures and shutdown. These are required tests, not claims of existing coverage.
