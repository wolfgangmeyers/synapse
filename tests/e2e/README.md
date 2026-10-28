# Playwright End-to-End (E2E) Test Suite

This directory contains the headless Playwright end-to-end tests for `mecha-developer`, complying with the [Behavioral Specification Standard](../../docs/behavioral-specification-standard.md).

## Architecture

The test harness (`tests/e2e/harness.ts`) runs an isolated, real environment:
1. **Isolated SQLite Database**: Created in a private temporary directory (`/tmp/mecha-e2e-XXXXXX/`) with 0700 permissions.
2. **Ephemeral Mailbox Router**: Compiles and launches an isolated instance of `mailbox-router` on a free ephemeral loopback port (`127.0.0.1:<port>`).
3. **Transparent Router Proxy**: Optionally forwards traffic to test network parity and CORS/proxy forwarding (`router-proxy`).
4. **Deterministic Mock LLM Server**: Fast local HTTP server simulating OpenAI-compatible / model completion endpoints without reaching external networks.
5. **Static Frontend Server**: Serves built React UI assets from `web/router-ui-react/dist`.
6. **Headless Chromium**: Controlled via `@playwright/test`.

## Running Tests

To run the complete Playwright E2E suite:

```bash
npm run test:e2e
```

### Pre-requisites

Ensure the frontend has been built at least once:

```bash
npm --prefix web/router-ui-react run build
```

The E2E harness will verify `web/router-ui-react/dist` exists before starting tests.

### Running with UI or Tracing

To run Playwright in headed or interactive mode:

```bash
npx playwright test --ui
```

To run a specific test:

```bash
npx playwright test tests/e2e/conversation-feed.spec.ts
```

## Adding New Tests

1. Create or reference the behavioral specification in `docs/specs/<feature>.spec.md`.
2. Map your test cases to the specification scenarios.
3. Import `setupE2EHarness` from `./harness` to initialize an isolated stack per test suite or test case.
4. Verify tests complete quickly and deterministically without external network calls.
