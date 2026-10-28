# ADR 0006: Deterministic Mock LLM End-to-End Testing

- **Status**: Accepted
- **Deciders**: Wolfgang
- **Date**: 2025-10-28

---

## Context & Problem Statement

Agentic workflows and real-time developer tools are notoriously difficult to test reliably:
1. **Flaky External LLM Dependencies**: Tests that call live LLM APIs suffer from rate limits, unpredictable network latency, changing model outputs, and API billing costs.
2. **Brittle JavaScript Unit Mocks**: Unit tests that mock internal JS functions or shallow React components fail to catch real integration failures at system boundaries (e.g., SQLite schema migrations, WebSocket protocol upgrades, HTTP header handling, or browser DOM rendering).
3. **Slow Feedback Loops**: End-to-end tests that launch full heavyweight browser suites or external CLI processes can take minutes, discouraging frequent testing during the `dev-loop`.

We need automated verification that exercises the real system across all boundaries while remaining 100% deterministic, fast, and free of external dependencies.

---

## Decision

Adopt Playwright end-to-end testing backed by deterministic local mock LLM backends and ephemeral SQLite databases as the mandatory verification standard for all behavioral changes in `mecha-developer`.

1. **Exercise Real Boundaries**:
   - Tests spin up real system components: headless browser, WebSocket upgrades, REST APIs, router-proxy reverse proxy, and real SQLite database stores on ephemeral loopback ports.
2. **Deterministic Mock LLM Server**:
   - Replace all external LLM provider calls (Gemini, Claude, OpenAI) with a fast, in-process HTTP mock server (`tests/e2e/harness.ts`).
   - The mock LLM returns configurable, instantaneous, deterministic JSON and streaming text responses.
   - External network calls to real LLMs or outside services in automated test suites are strictly forbidden.
3. **Isolated Ephemeral Environments**:
   - Every test run operates in an isolated temporary directory with a clean SQLite database executing active migrations (`112_conversation_timeline.sql`, etc.).
   - Ports are dynamically allocated (`allocatePort`) on `127.0.0.1` to prevent collisions during parallel test runs.
4. **Retire Brittle In-Memory Unit Mocks**:
   - Eliminate redundant integration tests that merely mock internal JavaScript functions or duplicate E2E test coverage without testing real end-to-end boundaries.

---

## Invariants & Guarantees

- **Zero External Network Calls**: Tests must pass completely offline without internet connectivity or API keys.
- **Hermetic State**: No test run leaves artifacts or database locks in user home directories; all resources are torn down cleanly in `test.afterEach` / `harness.close()`.
- **System Boundary Coverage**: Tests verify the full chain: Browser UI -> HTTP/WS Transport -> Go Router/Proxy -> SQLite Store -> Mock LLM Engine.
- **Fast Execution**: Test suites complete in seconds, providing rapid feedback in pre-commit and dev-loop workflows.

---

## Consequences

### Positive
- Completely eliminates test flakiness caused by third-party LLM rate-limiting, downtime, or nondeterministic responses.
- Catches real serialization, protocol mismatch, and database migration regressions before production deployment.
- High developer confidence during major architectural refactors.

### Negative
- Requires maintaining the E2E test harness (`tests/e2e/harness.ts`) when backend API protocols evolve.
- Engineers must construct mock LLM response fixtures for complex multi-turn tool call scenarios.

---

## References

- Behavioral Specification Standard: `docs/behavioral-specification-standard.md`
- Operating Policy: `docs/wolfgang-agent-operating-policy.md` (Section: Behavioral Functional Testing Standard)
- Test Harness: `tests/e2e/harness.ts`
- E2E Test Suite: `tests/e2e/conversation-feed.spec.ts`
