# Behavioral Specifications Catalog

This index serves as the master catalog and discovery portal for all behavioral specifications in `mecha-developer`. Every behavioral change, user-facing feature, or subsystem contract must be documented here and mapped to authoritative Playwright end-to-end (E2E) suites in `tests/e2e/`.

For formatting guidelines and authoring conventions, refer to the [Behavioral Specification Standard](../behavioral-specification-standard.md).

---

## Conversation Subsystem Specifications

The Conversation subsystem governs the core interaction interface between Wolfgang and managed AI agents, including timeline feed rendering, scroll anchoring, media attachments, input isolation, and inter-agent mailbox visualizations.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-CONV-001` | Feed Rendering & Real-Time Event Streaming | `Conversation` | [`conversation-timeline.md`](./conversation-timeline.md) | `tests/e2e/conversation-feed-rendering.spec.ts` | `Active` |
| `SPEC-CONV-002` | Attachments & Thumbnail Gallery | `Conversation` | [`conv-attachments.spec.md`](./conv-attachments.spec.md) | `tests/e2e/conversation-attachments.spec.ts` | `Active` |
| `SPEC-CONV-003` | Scroll Anchoring & Upward Pagination | `Conversation` | [`conv-scroll-and-pagination.spec.md`](./conv-scroll-and-pagination.spec.md) | `tests/e2e/conversation-scroll-and-pagination.spec.ts` | `Active` |
| `SPEC-CONV-004` | Composer Input Isolation & Drafts | `Composer` | [`conv-composer-isolation.spec.md`](./conv-composer-isolation.spec.md) | `tests/e2e/conversation-composer-isolation.spec.ts` | `Active` |
| `SPEC-CONV-005` | Mailbox Envelope Cards | `Mailbox` | [`conv-envelope-cards.spec.md`](./conv-envelope-cards.spec.md) | `tests/e2e/conversation-envelope-cards.spec.ts` | `Active` |

---

## Agent Lifecycle & Execution Specifications

Specifications covering agent process supervisors, tmux bridges, embedded runtimes, context threshold handling, and background task orchestration.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-LIFECYCLE-001` | Claude Code Managed Workers | `AgentManagement` | [`claude-code-workers-spec.md`](./claude-code-workers-spec.md) | `tests/e2e/claude-code-workers.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-002` | Context Reset Window Config | `AgentManagement` | [`context-reset-window-spec.md`](./context-reset-window-spec.md) | `tests/e2e/context-reset-window.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-003` | Tmux Worker Registry & List | `AgentManagement` | [`tmux-worker-list-spec.md`](./tmux-worker-list-spec.md) | `tests/e2e/tmux-worker-list.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-004` | Router MCP & OAuth Tool Routing | `Settings` | [`router-mcp-oauth-tools-spec.md`](./router-mcp-oauth-tools-spec.md) | `tests/e2e/router-mcp-oauth.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-005` | Subagent Read Tools Budget & Redirection | `AgentManagement` | [`subagent-read-tools-budget.spec.md`](./subagent-read-tools-budget.spec.md) | `tests/e2e/subagent-read-tools-budget.spec.ts` | `Active` |
| `SPEC-BACKGROUND-WORKFLOWS-001` | Background Workflows & Scheduled Execution | `Workflows` | [`SPEC-BACKGROUND-WORKFLOWS.md`](./SPEC-BACKGROUND-WORKFLOWS.md) | `tests/e2e/background-workflows.spec.ts` | `Active` |

---

## Knowledge Base Specifications

Specifications governing persistent organizational knowledge storage, Markdown documentation browsing, and Git synchronization.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-KB-001` | Knowledge Base Git Sync, kb_delete, & Markdown Browser | `KnowledgeBase` | [`knowledge-base-git-sync.spec.md`](./knowledge-base-git-sync.spec.md) | `tests/e2e/knowledge-base-git-sync.spec.ts` | `Active` |

---

## Spec Authoring & Maintenance Workflow

1. **Pre-Implementation Requirement**: No pull request or implementation commit modifying user behavior may land without an accompanying or pre-existing specification in `docs/specs/`.
2. **Scenario Completeness**: Every specification must detail both Happy Paths and Non-Happy/Edge/Error paths using strict `Given / When / Then` contracts.
3. **Verification Coupling**: Every scenario must map 1:1 to an automated Playwright E2E test case in Section 4 of the specification.
4. **Deprecation**: Deprecated specifications must have their status changed to `Deprecated` with a pointer to the superseding specification.
