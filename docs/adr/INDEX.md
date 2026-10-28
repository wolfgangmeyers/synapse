# Architectural Decision Records (ADRs)

Master index of architectural decisions for `mecha-developer`.

## Standards & Invariants

All architectural changes, refactors, and feature implementations in `mecha-developer` must adhere to these records and the behavioral specifications in [`docs/specs/`](../specs/INDEX.md).

| ADR | Title | Status | Summary |
| --- | ----- | ------ | ------- |
| [0001](0001-sqlite-monotonic-timeline-authority.md) | SQLite Monotonic Sequence Timeline Authority | **Accepted** | Establish `conversation_timeline` table with strictly increasing per-agent `seq` as single source of truth; deprecate fragmented legacy history and peer chat tables. |
| [0002](0002-embedded-workers-as-primary-runtime.md) | Embedded Workers as Primary Runtime | **Accepted** | Default to native embedded agent workers (`gemini-3.8-flash`), relegating external tmux/pi workers to maintenance-only fallback. |
| [0003](0003-isolated-loopback-router-proxy-networking.md) | Isolated Loopback & Router-Proxy Networking | **Accepted** | Router binds strictly to `127.0.0.1:8765`; TCP proxy bridges LAN/Tailscale at `127.0.0.1:19876` and `9876` with permissive non-credentialed CORS. |
| [0004](0004-single-user-workstation-security-model.md) | Single-User Workstation Security Model | **Accepted** | Assume single-user developer workstation; forbid requiring `0700` on ancestor paths; enforce only non-world/group writable and private leaf permissions. |
| [0005](0005-dark-glassmorphic-cyberpunk-design-system.md) | Dark Glassmorphic Design System & View Hierarchy | **Accepted** | Enforce dark glassmorphic styling, neon accent system, circular `.icon-btn-round` action buttons, and separation of list vs form views in modal dialogs. |
| [0006](0006-deterministic-mock-llm-e2e-testing.md) | Deterministic Mock LLM End-to-End Testing | **Accepted** | Fast, non-flaky Playwright E2E tests exercising real boundaries (SQLite, WebSocket, REST, browser DOM) with local deterministic mock LLMs. |
| [0007](0007-composer-draft-state-isolation.md) | Composer Draft State Isolation | **Accepted** | Complete isolation of composer input draft state from the root React application tree to guarantee 60fps typing without feed re-renders. |

## Lifecycle States

- **Proposed**: Under review, pending prototype validation or consensus.
- **Accepted**: Active architectural policy. All production code and specs must adhere to it.
- **Superseded**: Replaced by a newer decision (links to replacing ADR).
- **Deprecated**: Retained for historical context; no longer actively enforced.
