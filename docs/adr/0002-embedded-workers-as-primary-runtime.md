# ADR 0002: Embedded Workers as Primary Runtime

- **Status**: Accepted
- **Deciders**: Wolfgang
- **Date**: 2025-10-28

---

## Context & Problem Statement

Historically, `mecha-developer` orchestrated multi-agent development by launching external tmux windows running standalone CLI agents (Pi CLI, Claude Code, Cursor, Codex). This model introduced severe friction:

1. **Window Management & Brittle Parsing**: Capturing and scraping terminal panes via `tmux capture-pane` is fragile, non-deterministic, and prone to escape-sequence corruption.
2. **Process Lifecycle Overhead**: Managing individual OS processes across external shells introduces race conditions during startup, termination, and compaction.
3. **Delivery Inconsistencies**: Terminal delivery required sidecars (`mailbox_send.py`), complex terminal injectors, and polling loops that degraded responsiveness.
4. **Token Inefficiency**: Heavy multi-turn supervisor loops over tmux windows consumed excessive tokens and delayed worker coordination.

---

## Decision

Adopt embedded agents running within the router/application process runtime as the default worker mode.

1. **Embedded Agent Default**: Native embedded workers are the primary execution engine for all developer tasks, review workflows, and background subagents.
2. **Default Model**: Supervisors, workers, and background subagents use `gemini-3.8-flash` by default unless explicitly overridden by Wolfgang for a specific task.
3. **Tmux/Pi as Maintenance Backup**: External tmux and standalone CLI workers (Pi CLI, Claude Code, Cursor, Codex) are placed into maintenance mode. They serve strictly as a secondary fallback in case embedded runtime access fails or Wolfgang explicitly requests a specific CLI runtime.
4. **No Spurious CLI Launches**: Agents and supervisors must not spawn external CLI workers unless explicit instructions mandate them.

---

## Invariants & Guarantees

- **Router Process Boundary**: Embedded workers run under direct supervisor/router control via internal message delivery, eliminating terminal scraping and tmux synchronization.
- **Model Invariant**: Default model is `gemini-3.8-flash` across all workers and subagents.
- **Extension & Tool Parity**: Managed workers retain full tool support, goal tracking, and filesystem execution capabilities identical to standalone interactive sessions.
- **Clean Fallback Boundary**: The system retains tmux harness capabilities in maintenance mode, preserving backward compatibility for CLI workflows when explicitly summoned.

---

## Consequences

### Positive
- Sub-second worker invocation and execution without terminal pane initialization overhead.
- Direct structured logging and timeline emission directly into `conversation_timeline` without regex output scrapers.
- Drastic reduction in token consumption and orchestration latency.
- Deterministic testability using in-memory or loopback mock harnesses.

### Negative
- Direct terminal inspection (attaching to a tmux pane to watch live curses UI) is not available for embedded workers; visibility relies on the web UI or timeline feeds.
- Backup tmux launcher code must still be maintained for disaster recovery.

---

## References

- Project Instructions: `/home/wolfgang/AGENTS.md`
- Operating Policy: `docs/wolfgang-agent-operating-policy.md`
- Embedded Architecture: `docs/embedded-pi-go-technical-spec.md`
- Subagent Specs: `docs/router-subagents-technical-spec.md`
