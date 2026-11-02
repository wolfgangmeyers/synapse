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

## Provider Removal Specifications

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-PROVIDERS-ANTIGRAVITY-REMOVAL-001` | Remove Antigravity; preserve Gemini API-key, Vertex and Codex | `Settings` | [`antigravity-removal.spec.md`](./antigravity-removal.spec.md) | `tests/e2e/antigravity-removal.spec.ts` (planned) | `Draft` |

## Agent Lifecycle & Execution Specifications

Specifications covering agent process supervisors, tmux bridges, embedded runtimes, context threshold handling, and background task orchestration.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-ROUTER-INIT-001` | Fresh-machine Router Initialization | `Settings` | [`router-initialization.spec.md`](./router-initialization.spec.md) | `tests/e2e/router-initialization.spec.ts` | `Active` |
| `SPEC-ROUTER-MIGRATION-001` | Router Retention Migration Recovery | `Settings` | [`router-retention-migration.spec.md`](./router-retention-migration.spec.md) | `tests/e2e/router-retention-migration.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-CODEX-CATALOG-001` | Codex Catalog and Embedded Launch Parity | `AgentManagement` | [`codex-model-catalog.spec.md`](./codex-model-catalog.spec.md) | `tests/e2e/codex-model-catalog.spec.ts` | `Active` |
| `SPEC-OPENAI-CATALOG-001` | OpenAI GPT-6.1 Terra Catalog | `AgentManagement` | [`openai-model-catalog.spec.md`](./openai-model-catalog.spec.md) | `tests/e2e/openai-model-catalog.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-001` | Claude Code Managed Workers | `AgentManagement` | [`claude-code-workers-spec.md`](./claude-code-workers-spec.md) | `tests/e2e/claude-code-workers.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-002` | Context Reset Window Config | `AgentManagement` | [`context-reset-window-spec.md`](./context-reset-window-spec.md) | `tests/e2e/context-reset-window.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-003` | Tmux Worker Registry & List | `AgentManagement` | [`tmux-worker-list-spec.md`](./tmux-worker-list-spec.md) | `tests/e2e/tmux-worker-list.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-004` | Router MCP & OAuth Tool Routing | `Settings` | [`router-mcp-oauth-tools-spec.md`](./router-mcp-oauth-tools-spec.md) | `tests/e2e/router-mcp-oauth.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-005` | Subagent Read Tools Budget & Redirection | `AgentManagement` | [`subagent-read-tools-budget.spec.md`](./subagent-read-tools-budget.spec.md) | `tests/e2e/subagent-read-tools-budget.spec.ts` | `Active` |
| `SPEC-BACKGROUND-WORKFLOWS-001` | Background Workflows & Scheduled Execution | `Workflows` | [`SPEC-BACKGROUND-WORKFLOWS.md`](./SPEC-BACKGROUND-WORKFLOWS.md) | `tests/e2e/background-workflows.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-007` | Deterministic Embedded Own Reminder Delays & Idempotency | `AgentManagement` | [`embedded-own-reminder-delay.spec.md`](./embedded-own-reminder-delay.spec.md) | `router-proxy/internal/store/embedded_own_reminder_test.go` | `Active` |
| `SPEC-WORKFLOWS-002` | Implicit Workflow Goals & Workflow-Level Snooze | `Workflows` | [`workflow-implicit-goals.spec.md`](./workflow-implicit-goals.spec.md) | `tests/e2e/workflow-implicit-goals.spec.ts` | `Active` |
| `SPEC-WORKFLOWS-003` | Structured Workflow Schedules & Test Clock | `Workflows` | [`SPEC-STRUCTURED-WORKFLOW-SCHEDULES.md`](./SPEC-STRUCTURED-WORKFLOW-SCHEDULES.md) | `tests/e2e/background-workflows.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-007` | Deterministic Embedded Own Reminder Delays & Idempotency | `AgentManagement` | [`embedded-own-reminder-delay.spec.md`](./embedded-own-reminder-delay.spec.md) | `router-proxy/internal/store/embedded_own_reminder_test.go` | `Active` |

---

## Knowledge Base Specifications

Specifications governing persistent organizational knowledge storage, Markdown documentation browsing, and Git synchronization.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-KB-001` | Knowledge Base Git Sync, kb_delete, & Markdown Browser | `KnowledgeBase` | [`knowledge-base-git-sync.spec.md`](./knowledge-base-git-sync.spec.md) | `tests/e2e/knowledge-base-git-sync.spec.ts` | `Active` |

---

## Search & Tooling Specifications

Specifications governing agent search capabilities, tool integrations, and external research grounding.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-SEARCH-001` | Codex Search (`codex_search`) | `Tooling` | [`codex-search.md`](./codex-search.md) | `tests/e2e/codex-search.spec.ts` | `Active` |

---

## Settings & Configuration Specifications

Specifications governing provider settings, router MCP configurations, and server management menus.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-ROUTER-CONFIG-001` | Router JSON Config File & Custom KB Directory | `Settings` | [`router-config-file-and-kb.spec.md`](./router-config-file-and-kb.spec.md) | `tests/e2e/router-config-file-and-kb.spec.ts` | `Draft` |
| `SPEC-SETTINGS-002` | Server Management Dropdown Menu | `Settings` | [`SPEC-SERVER-MANAGEMENT-MENU.md`](./SPEC-SERVER-MANAGEMENT-MENU.md) | `tests/e2e/server-management-menu.spec.ts` | `Draft` |

---

## Agent Scratchpad Specifications

Specifications governing persistent agent scratchpad memory, notes retention, pinning, and storage pruning.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-SCRATCHPAD-001` | Scratchpad Entry Truncation Tool & Storage | `AgentManagement` | [`scratchpad-truncation.spec.md`](./scratchpad-truncation.spec.md) | `tests/e2e/scratchpad-truncation.spec.ts` | `Draft` |

---

## Spec Authoring & Maintenance Workflow

1. **Pre-Implementation Requirement**: No pull request or implementation commit modifying user behavior may land without an accompanying or pre-existing specification in `docs/specs/`.
2. **Scenario Completeness**: Every specification must detail both Happy Paths and Non-Happy/Edge/Error paths using strict `Given / When / Then` contracts.
3. **Verification Coupling**: Every scenario must map 1:1 to an automated Playwright E2E test case in Section 4 of the specification.
4. **Deprecation**: Deprecated specifications must have their status changed to `Deprecated` with a pointer to the superseding specification.
