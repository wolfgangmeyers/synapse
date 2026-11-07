# Spec: Claude process admission and recovery

- **ID**: `SPEC-AGENT-CLAUDE-FENCE-001`
- **Status**: Draft
- **Domain**: AgentManagement
- **Target E2E Suite**: `tests/e2e/claude-code-orphan-recovery.spec.ts`

## Intent & User Outcome
A router never starts replacement Claude work while old process cleanup is unsafe, and shutdown retains ownership until admitted work finishes cleanup.

## Core Invariants
1. The manager's admission fence covers workers, children, authentication probes and evaluators.
2. Shutdown closes admission before waiting for all admitted process operations and their cleanup.
3. Orphan records are removed durably only after confirmed absence; failures fence admission.

## User Scenarios

### Normal work and shutdown
Given a running manager and admitted Claude work, when the manager shuts down, then new Claude work is rejected and shutdown waits for admitted work to finish cleanup before returning.

### Unconfirmed cleanup
Given cleanup of any Claude process cannot be confirmed, when another worker, child, authentication probe or evaluator tries to start, then it is rejected until recovery by a new manager.

### Orphan record failure
Given a process has stopped but removal or directory synchronization of its orphan record fails, when cleanup completes, then admission is fenced and the failure is reported rather than silently leaving an unsafe stale record.

### Runtime initialization failure
Given a Claude turn cannot initialize its runtime, when initialization fails before a process starts, then the router reports the error without crashing and releases the admitted execution so later work is not blocked by that failed attempt.

## Acceptance and Coverage
Unit regressions cover admission, shutdown synchronization and cleanup failures. `tests/e2e/claude-code-orphan-recovery.spec.ts` covers cleanup of a verified live orphan, mismatch fencing, and continued Pi availability. The existing worker E2E suite covers the normal managed process lifecycle; other browser-level fault injection remains required before this draft can become active.

### Fence racing an admitted start
Given Claude work was admitted but its executable has not been released, when unsafe cleanup fences admission or shutdown begins, then the waiting executable must not run. Gate release and fencing have one ordering: a release that wins may run and remains owned through cleanup; a fence that wins prevents release. This applies to roots, children, authentication probes and evaluators. Cleanup and durable callbacks must not run while holding the release lock.

### Crash during orphan publication
Given a Claude launch is waiting at its execution gate, when its recovery record is created, then the record and the directory containing it are durable before the gate is released, including on first directory creation. A crash during an incomplete write leaves only a recognizable unpublished temporary file; startup removes such files before admitting work. A crash after publication leaves a complete record for normal identity-verified recovery. Unexpected or untrusted entries and malformed published records still block admission. Any write, sync, publish, or cleanup failure prevents release or admission; concurrent launches cannot overwrite one another's records.

Deterministic process-level crash/failure regressions are required in addition to the worker E2E suite; browser-level fault injection remains a draft acceptance gap.

### Recovery without a pinned process group
Given a recorded group survives router restart, recovery rechecks a matching live leader identity and signals the group, then confirms absence before removing the record. Mismatched, leaderless or uncertain groups are not signaled and fence admission. This check does not pin the process group: PID or group ID reuse between the check and signal can target an unrelated group. Ordinary home-directory permissions do not prevent recovery.

An unsafe orphan or recovery failure does not prevent router startup or Pi work. Claude admission remains fenced for the lifetime of that manager, with a recovery diagnostic; after the operator resolves the orphan, restarting the router retries recovery and permits Claude only if it succeeds. Recovery immediately rechecks a matching live identity before SIGKILL and confirms absence within a bounded time. Mismatched and unknown identities remain untouched.

Uncertain or unconfirmed groups require operator intervention. A different Linux boot may still retire a valid record because processes cannot survive reboot; weak or unavailable boot identity on other platforms must never justify retiring a record as belonging to an earlier boot. Deterministic process-boundary regressions must cover live groups, identity mismatch, and weak markers; browser-level fault injection remains a draft acceptance gap.

### Recovery ledger bookkeeping
Given concurrent Claude launches near the 1024-entry recovery limit, when records are published, then the ledger never exceeds that limit and excess launches remain gated. Recovery rejects unexpected published filenames even if their contents decode. Removing recovery records and temporary prompt or configuration files is followed by syncing their containing directories; sync failures block admission. Creation persists newly created directory entries through their ancestors without imposing private permissions on ordinary home directories.

### Degraded startup diagnostic
Given orphan recovery fails, when the router starts in degraded mode, then Claude admission remains fenced while unrelated runtimes remain available. The recovery diagnostic and shutdown error identify the safe failure category (including an operating-system error when available) without exposing paths, filenames, record contents, or other sensitive data. A filesystem failure must not be reported as an unconfirmed process-group cleanup. Focused regression: `TestClaudeStartupRecoveryReportsSafeFilesystemCause`; browser-level fault injection remains a draft acceptance gap.
