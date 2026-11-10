# Spec: Memory capture

- **ID**: `SPEC-MEMORY-001`
- **Status**: `Active` for router policy and absent catalog; Pi extension lifecycle retired
- **Domain**: `Mailbox`
- **Target E2E Suite**: `tests/e2e/memory-capture.spec.ts`

## 1. Intent & User Outcome

The Go router publishes memory policy but installs no filesystem catalog. Policy availability is not persistence. The former Pi extension capture lifecycle has been removed; this specification does not claim a replacement or active compaction or shutdown capture.

## 2. Core Invariants

1. The default router publishes a hard-coded memory capture policy.
2. Default catalog operations return `404 not_found`, never fictional write success. Router health remains available.
3. Optional catalog support is an injected interface, not an installed filesystem implementation. Filesystem hydration, flushing, rollback and index regeneration are not current guarantees.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [MEM-001] Policy exists without a catalog
- **User Goal**: Discover settings without assuming storage exists.
- **Given**: An isolated default Go router with SQLite, reached through the real proxy.
- **When**: A browser fetches policy and attempts lookup, expand, touch, apply and rebuild.
- **Then**: The policy's `recall.capture` matches the explicit fixture. All catalog requests return `404 not_found`. None reports persistence. Health remains available.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
|---|---|---|---|
| MEM-001 | Policy and absent catalog | `tests/e2e/memory-capture.spec.ts` | `default router publishes policy without pretending catalog persistence` |

## 5. Retired Pi Extension Lifecycle and Coverage

The removed Pi extension previously attempted best-effort capture during compaction and shutdown. Its destination-before-extraction, validation, serialization, cancellation, deadline and diagnostic behavior are no longer active guarantees. The former MEM-002 lifecycle-boundary E2E case and `tests/memory_capture.test.ts` supporting coverage are retired with that implementation. The remaining E2E test covers router policy and missing catalog behavior only; it does not exercise a capture lifecycle or establish persistence.
