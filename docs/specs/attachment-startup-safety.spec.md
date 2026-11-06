# Spec: Attachment Safety During Router Startup

- **ID**: `SPEC-MAILBOX-ATTACHMENT-STARTUP-001`
- **Status**: `Active`
- **Domain**: `Mailbox`
- **Target E2E Suite**: `tests/e2e/attachment-startup-safety.spec.ts`

## 1. Intent & User Outcome

A failed router startup must not destroy a saved conversation image. The router may clean up old unreferenced image files only when it can reliably establish that no saved conversation references them.

## 2. Core Invariants

1. If database opening, migration, or startup receipt pruning fails, startup fails visibly without binding a usable router and without deleting attachment files. A database handle that is unavailable, closed, or unable to answer a reference query must never be interpreted as proof that a file is unreferenced.
2. Once the database is healthy, a referenced attachment remains readable after its normal 24-hour file-age limit. Old, positively identified unreferenced files remain eligible for ordinary retention cleanup. Startup and cleanup do not mutate saved conversation metadata to manufacture an unreferenced file.
3. Recovery after a startup error preserves the original image bytes and conversation attachment metadata. The user can access the saved image again through the browser and router proxy without resending it.

## 3. User Scenarios (Behavioral Contract)

### Scenario: [AST-001] Failed startup preserves an old referenced image
- **User Goal**: Keep a saved image when a database upgrade or cleanup encounters a problem.
- **Given**: A conversation references an image older than 24 hours, an equally old orphan image exists, and an owned database is configured to fail either its receipt migration or startup receipt pruning.
- **When**: The upgraded router attempts startup, the failure is removed, and the router starts again.
- **Then**: The failed startup deletes neither image and leaves conversation metadata intact. Once healthy, the referenced image is still readable through browser REST via the proxy; ordinary cleanup can remove the orphan. Browser WebSocket registration also works. No external model is called.

## 4. Automated E2E Verification Matrix

| Scenario ID | Scenario Description | E2E Test Suite | Test Case Name |
| :--- | :--- | :--- | :--- |
| AST-001 | Failed migration or pruning preserves images and recovers | `tests/e2e/attachment-startup-safety.spec.ts` | `preserves referenced images through failed startup and recovery` |

Supporting Go tests cover unavailable/closed database handles and a failed reference query, including fallback during later startup failures.
