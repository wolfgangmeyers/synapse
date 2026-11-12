# Spec: Background Bash Without a Wait Tool

- **ID**: `SPEC-BACKGROUND-BASH-NO-WAIT-001`
- **Status**: `Draft`
- **Domain**: `AgentManagement`
- **Target E2E Suite**: `tests/e2e/background-bash.spec.ts`

## Intent & User Outcome

Background shell jobs remain background work. Agents receive completion notices and can inspect jobs without blocking their turn.

## Core Invariants

The managed runtime does not expose `wait_background_bash_job` to roots, workers, or subagents. Launch, list, get, kill, promotion, and completion notifications retain their existing contracts. The shared background bash spec reflects this no-wait contract.

## User Scenarios

### Scenario: [BB-NO-WAIT-001] Background work stays in the background
- **Given**: A root, worker, or subagent owns a running explicit or promoted background job.
- **When**: The agent inspects its available tools and continues working.
- **Then**: No wait tool is offered. List and get remain nonblocking snapshots, and the ordinary completion notice is attempted when the job finishes. Existing ownership and polling rules apply. If a subagent finishes while its job is still running, existing owner retirement cleans up the job; completion does not resume an already finished child.

## Automated E2E Verification Matrix

| Scenario | Test |
| --- | --- |
| BB-NO-WAIT-001 | Partial: `Claude child promotes the same shell and inspects its completion without a wait tool` and `finishing a Claude child retires its still-running background job` passed in a focused run. Runtime catalog and completion notification across all managed roles remain pending acceptance. |
