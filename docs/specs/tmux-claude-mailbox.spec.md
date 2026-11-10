# Spec: tmux Claude worker mailbox

- **ID**: `SPEC-MAILBOX-TMUX-001`
- **Status**: `Draft`
- **Domain**: `Mailbox`
- **Target E2E Suite**: `tests/e2e/tmux-claude-mailbox.spec.ts`

## Intent & User Outcome

A launcher-managed Claude worker in a router-owned tmux window receives mailbox messages in its terminal without depending on the removed Pi extension.

## Core Invariants

1. The sidecar watches the same router-selected tmux inventory used for injection, not a hardcoded local session.
2. Router inventory failures are not interpreted as proof the window is gone. A confirmed missing window stops the sidecar.
3. Delivery is acknowledged only after terminal injection succeeds; failures remain retryable under the mailbox protocol.
4. A delivery is bound to the tmux server lifetime and window incarnation selected at registration, including across tmux server restarts. A replacement with the same name never receives its bytes or Enter.
5. Sidecar logs do not contain message bodies and are size bounded.

## User Scenarios

### Message delivery
- **Given** a Claude worker and its sidecar registered with the router in a router-selected tmux session.
- **When** a mailbox message is sent to the worker.
- **Then** the sidecar injects the message into the worker window via the router and acknowledges successful delivery.

### Startup inventory temporarily unavailable
- **Given** a launched worker and an unavailable or temporarily malformed router inventory.
- **When** the inventory becomes available.
- **Then** the sidecar binds the window and registers; it never guesses an identity while the inventory is unavailable. A confirmed missing window is not treated as a transient inventory error.

### Window removal
- **Given** a registered worker sidecar.
- **When** the router inventory confirms the worker window is missing.
- **Then** the sidecar exits and releases its router connection. An inventory error alone does not cause exit.

### Window replacement during delivery
- **Given** the sidecar is delivering to a window.
- **When** that window is removed and another takes its name before delivery completes.
- **Then** the replacement receives no subsequent bytes and the old delivery is not acknowledged.

### Injection failure
- **Given** a registered worker whose window cannot accept router injection.
- **When** a message is delivered.
- **Then** it is not acknowledged as successfully injected.

## Coverage

The Playwright runner exercises the historical launcher, sidecar CLI and mailbox client through the real router, SQLite and a private tmux server, with a deterministic terminal stand-in for Claude. The private-tmux test replaces the window during a real sidecar delivery, observes the sidecar’s delayed Enter request and the real router’s rejection, checks a raw-input replacement receives no bytes, and verifies the message remains unacknowledged. A test-local proxy holds monitor inventory responses during that window; it forwards the delivery request to the real router. A real-tmux adapter test restarts the server and verifies a recycled window ID rejects the old server token. Monitor failure, startup retry and injection failure are covered by focused tests. Browser interaction and a live Claude CLI remain unverified.
