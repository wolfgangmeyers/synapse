# Spec: Conversation residual reliability

- **ID**: `SPEC-CONV-RESIDUAL-001`
- **Status**: Draft
- **Domain**: Conversation
- **Target E2E Suite**: `tests/e2e/web-residual-reliability.spec.ts`

## Intent & User Outcome
Conversation history remains responsive and live messages and drafts are not silently lost. An accepted send is not reported as failed because a local thumbnail cache is slow.

## Core Invariants
1. History requests have a bounded lifetime, abort on timeout, and expose a retryable error.
2. Reconnect preserves live entries not yet represented in durable history, including entries received before disconnection.
3. A confirmed send settles independently of best-effort thumbnail caching.
4. Capacity admission happens before network send; unsent text is retained or an actionable notice is provided.

## User Scenarios
### SCN-001: History stalls
- **Given** a history request stalls, **when** its deadline expires, **then** it is aborted within 15 seconds and the user sees an explicit retry button that starts a fresh request. Existing live and pending content remains visible after a failed or empty retry.
### SCN-002: Live entry precedes reconnect
- **Given** an undurable live entry is visible, **when** reconnect returns a non-overlapping durable page, **then** the entry remains visible.
### SCN-003: Thumbnail cache stalls after acceptance
- **Given** the router accepts an image send, **when** local thumbnail caching stalls or fails, **then** the message remains accepted and is not marked unconfirmed.
### SCN-004: Outbox is full
- **Given** the outbox is at capacity, **when** another message is submitted, **then** no network send occurs and the user can recover the unsent text or receives a clear actionable notice.

## Automated E2E Verification Matrix

All cases below are planned and not yet implemented; this specification remains Draft.
| Scenario | E2E suite | Test case |
| --- | --- | --- |
| SCN-001 | `tests/e2e/web-residual-reliability.spec.ts` | history timeout aborts and reports error |
| SCN-002 | `tests/e2e/web-residual-reliability.spec.ts` | reconnect preserves preexisting live entry |
| SCN-003 | `tests/e2e/web-residual-reliability.spec.ts` | accepted send survives stalled thumbnail cache |
| SCN-004 | `tests/e2e/web-residual-reliability.spec.ts` | full outbox rejects before send |
