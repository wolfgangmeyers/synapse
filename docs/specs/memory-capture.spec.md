# Spec: Memory capture

- **ID**: `SPEC-MEMORY-001`
- **Status**: `Active`
- **Domain**: `Mailbox`
- **Target E2E Suite**: `tests/e2e/memory-capture.spec.ts`

## 1. Intent & User Outcome

Capture is best effort during Pi compaction and shutdown. Ordinary work remains usable when durable memory is unavailable. The shipped Go router publishes policy but installs no filesystem catalog. Policy availability is not persistence.

## 2. Core Invariants

1. Default catalog operations return `404 not_found`, never fictional write success.
2. Destination lookup precedes extraction. Failure rejects capture, emits a content-free diagnostic, and prevents extraction and apply. Callers can continue ordinary work.
3. Optional catalog support is an injected interface, not an installed filesystem implementation. Filesystem hydration, flushing, rollback and index regeneration are not current guarantees.
4. Validation precedes apply. Only bounded prose evidenced by successful tool results is accepted. Credentials, correspondence, thinking, images, custom and compaction messages are excluded. Links and update CAS bases must match supplied context.
5. Compaction and shutdown serialize. Cancellation prevents apply. Shutdown deadlines include queue time. Failure clears timers. Diagnostics exclude source text.
6. Extractor configuration follows router policy: `anthropic/claude-sonnet-5`, thinking off, 60-second timeout, cancellation forwarded. Tests stub execution, never call models.
7. This consolidation adds no runtime history. Temporary servers and stores are removed. The checked-in historical archive is finite, repository-owned evidence, not a growing runtime store.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [MEM-001] Policy exists without a catalog
- **User Goal**: Discover settings without assuming storage exists.
- **Given**: An isolated default Go router with SQLite, reached through the real proxy.
- **When**: A browser fetches policy and attempts lookup, expand, touch, apply and rebuild.
- **Then**: Policy matches the explicit fixture. All catalog requests return `404 not_found`. None reports persistence. Health remains available.

### Scenario: [MEM-002] Optional memory failure permits continued work
- **User Goal**: Continue after capture is unavailable.
- **Given**: The production capture lifecycle uses policy and destination lookup from the isolated router.
- **When**: Compaction and shutdown attempt capture of a successful tool result.
- **Then**: Both reject with destination unavailable and bounded diagnostics. Neither extraction nor apply runs. Browser health requests still succeed. This is lifecycle-boundary coverage, not a full managed Pi session or successful optional catalog implementation.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
|---|---|---|---|
| MEM-001 | Policy and absent catalog | `tests/e2e/memory-capture.spec.ts` | `default router publishes policy without pretending catalog persistence` |
| MEM-002 | Graceful capture failure | `tests/e2e/memory-capture.spec.ts` | `unavailable destination prevents extraction and apply without breaking router work` |

Supporting deterministic coverage: all eleven scenarios in `tests/memory_capture.test.ts` retain sanitizer, validation, serialization, diagnostics, subprocess cancellation and deadline assertions. Injected apply callbacks do not establish persistence.
