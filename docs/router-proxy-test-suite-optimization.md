# Tracking Ticket: Optimize Long-Running Tests in `router-proxy`

## 1. Problem Statement

Running the full Go test suite (`go test ./router-proxy/...` or testing key packages such as `cmd/mailbox-router`, `internal/managed`, `internal/managedbridge`, `internal/store`, and `internal/transport`) is excessively slow. The complete suite takes several minutes to execute or exceeds subagent execution timeouts (>10 minutes).

Key pain points:
- Subagents and automated dev-loops fail or stall while running full package test suites.
- Developers and agents lack a fast feedback loop (<30 seconds) for iterative local verification.
- Test suites currently conflate lightweight unit tests with heavy end-to-end integration flows and real-time waiting logic.

---

## 2. Root Cause Analysis

Investigation reveals several root causes contributing to the test suite latency:

1. **Wall-Clock Sleeps**:
   - Multiple unit and integration tests across `router-proxy/internal/managed`, `router-proxy/internal/store`, and `router-proxy/internal/managedbridge` invoke literal `time.Sleep(...)` calls (ranging from 10ms to several seconds) to wait for asynchronous state changes, goroutines, or debounced updates.
   - Accumulation across hundreds of test functions compounds into minutes of dead idle time.

2. **High Default Watchdog, Lease, and Connection Timeouts**:
   - Tests exercising timeout handling, crash recovery, watchdog nudges, and connection retry loops rely on production timeout constants (e.g. 10s, 30s, or 60s windows) rather than parameterized test configurations.
   - Long-lived context deadlines cause stalled or slow-failing tests to wait up to the full deadline.

3. **Absence of Clock Abstractions and Mock Tickers**:
   - Asynchronous loops and schedulers directly instantiate `time.After`, `time.NewTicker`, and `time.Now` rather than taking an injectable clock interface or deterministic ticker mock.
   - Tests waiting on periodic events (e.g. log rotation, retention pruning, reminder delivery) wait for real elapsed wall-clock time instead of advancing synthetic time.

4. **Lack of Fast vs. Heavy Test Segmentation**:
   - Few or no tests check `testing.Short()`.
   - Running `go test -short ./...` still executes long-running integration flows because tests have not been gated with `if testing.Short() { t.Skip(...) }` or separated into dedicated integration test tags/targets.

---

## 3. Goals & Planned Optimizations

### Goal 1: Profile and Categorize Test Execution Times
- Measure per-package and per-test runtimes across `router-proxy` to identify the top 20 slowest tests and packages.
- Establish baseline metrics to track runtime reductions.

### Goal 2: Replace Wall-Clock Sleeps with Deterministic Wait Patterns & Mock Clocks
- **Event-Driven Synchronization**: Replace blind `time.Sleep` calls with channels, sync primitives, or condition polling with short backoffs and immediate completion triggers.
- **Clock Abstraction**: Introduce an injectable clock interface (or leverage deterministic clock implementations) for schedulers, log rotators, watchdog timers, and lease managers so time can be advanced instantaneously in unit tests.
- **Sub-Second and Millisecond Thresholds**: Parameterize timeouts and retry backoffs in tested subsystems so unit tests can configure millisecond-level deadlines instead of multi-second production intervals.

### Goal 3: Test Suite Segmentation & `-short` Support
- **Support `go test -short`**: Gate heavy multi-step integration tests, browser/tmux launch fixtures, and process-spawning tests with:
  ```go
  if testing.Short() {
      t.Skip("skipping slow integration test in short mode")
  }
  ```
- **Target Sub-30s Full Short Suite**: Ensure `go test -short ./...` runs in under 30 seconds across the entire repository.
- **Dedicated Integration Targets**: Provide clear Makefile/script targets for running fast unit suites vs. heavy acceptance/end-to-end suites.

---

## 4. Work Breakdown & Action Items

- [ ] **Phase 1: Instrumentation & Triage**
  - Profile slowest tests and catalog test files with `time.Sleep` or long timeouts.
  - Tag initial set of heavy end-to-end integration tests with `testing.Short()` skips.
- [ ] **Phase 2: Clock & Timeout Injection**
  - Injectable clock / ticker abstractions for background schedulers and retention loops.
  - Millisecond timeout parameterization for retry/recovery test fixtures.
- [ ] **Phase 3: Synchronization Refactoring**
  - Refactor polling and sleep loops to channel/condition synchronization.
- [ ] **Phase 4: CI & Agent Test Target Integration**
  - Verify `go test -short ./...` completes reliably in <30s.
  - Document recommended fast test commands for agent dev-loops and pre-commit checks.
