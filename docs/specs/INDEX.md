# Behavioral Specifications Catalog

This index catalogs behavioral specifications and their test targets. Some specifications or targets are missing, planned, or only partially covered; entries do not imply complete Playwright E2E coverage.

For formatting guidelines and authoring conventions, refer to the [Behavioral Specification Standard](../behavioral-specification-standard.md).

---

## Conversation Subsystem Specifications

The Conversation subsystem governs the core interaction interface between Wolfgang and managed AI agents, including timeline feed rendering, scroll anchoring, media attachments, input isolation, and inter-agent mailbox visualizations.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-CONV-001` | Feed Rendering & Real-Time Event Streaming | `Conversation` | [`conversation-timeline.md`](./conversation-timeline.md) | `tests/e2e/conversation-feed.spec.ts`, `tests/e2e/conversation-architecture-simplification.spec.ts` (partial coverage) | `Active` |
| `SPEC-CONV-002` | Attachments & Thumbnail Gallery | `Conversation` | [`conv-attachments.spec.md`](./conv-attachments.spec.md) | `tests/e2e/conversation-attachments.spec.ts` (planned) | Spec missing |
| `SPEC-CONV-003` | Scroll Anchoring & Upward Pagination | `Conversation` | [`conv-scroll-and-pagination.spec.md`](./conv-scroll-and-pagination.spec.md) | `tests/e2e/conversation-scroll-and-pagination.spec.ts`, `tests/e2e/conversation-scroll-regressions.spec.ts` | `Active` |
| `SPEC-CONV-004` | Composer Input Isolation & Drafts | `Composer` | [`conv-composer-isolation.spec.md`](./conv-composer-isolation.spec.md) | `tests/e2e/conversation-composer-isolation.spec.ts` (planned) | Spec missing |
| `SPEC-CONV-005` | Mailbox Envelope Cards | `Mailbox` | [`conv-envelope-cards.spec.md`](./conv-envelope-cards.spec.md) | `tests/e2e/conversation-envelope-cards.spec.ts` (planned) | Spec missing |
| `SPEC-CONV-006` | Web Agent Switch History | `Conversation` | [`web-agent-switch-history.spec.md`](./web-agent-switch-history.spec.md) | `tests/e2e/web-agent-switch-history.spec.ts`, `tests/e2e/web-history-isolation-edge.spec.ts`, `tests/e2e/web-pending-send-boundary.spec.ts` (partial) | `Draft`; acceptance incomplete |
| `SPEC-CONV-SELECTED-MANAGED-001` | Selected managed activity pagination | `Conversation` | [`managed-selected-recent-activity.spec.md`](./managed-selected-recent-activity.spec.md) | `tests/e2e/selected-managed-activity-pagination.spec.ts`, `tests/e2e/combined-history-window.spec.ts`, `tests/e2e/combined-history-live-window.spec.ts` (partial) | `Draft`; acceptance incomplete |
| `SPEC-CONV-RESIDUAL-001` | Conversation Residual Reliability | `Conversation` | [`web-residual-reliability.spec.md`](./web-residual-reliability.spec.md) | `tests/e2e/web-residual-reliability.spec.ts` (planned) | `Draft` |

---

## Provider Removal Specifications

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-PROVIDERS-ANTIGRAVITY-REMOVAL-001` | Remove Antigravity; preserve Gemini API-key, Vertex and Codex | `Settings` | [`antigravity-removal.spec.md`](./antigravity-removal.spec.md) | `tests/e2e/antigravity-removal.spec.ts` (planned) | `Draft` |
| `SPEC-PROVIDERS-GOOGLE-GEMINI-4-001` | Add Gemini 4 models (argon, carbon, hydrogen) to Google & Vertex providers | `Settings` | [`google-gemini-4-models.spec.md`](./google-gemini-4-models.spec.md) | `router-proxy/internal/managed/embedded_vertex_test.go`, `auth-helper/auth-helper.test.mjs` | `Active` |
| `SPEC-MANAGED-RESTART-ALL-REMOVAL-001` | Remove managed Restart All; preserve individual restart | `Managed Agents` | [`restart-all-removal.spec.md`](./restart-all-removal.spec.md) | `tests/e2e/individual-restart.spec.ts` | `Draft` |

## Agent Lifecycle & Execution Specifications

Specifications covering agent process supervisors, tmux bridges, embedded runtimes, context threshold handling, and background task orchestration.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-ACTIVITY-DIALOG-001` | Activity dialog visibility | `AgentManagement` | [`activity-dialog-visibility.spec.md`](./activity-dialog-visibility.spec.md) | `tests/e2e/activity-dialog-visibility.spec.ts` | `Active` |
| `SPEC-BB-WIDGET-001` | Background shell widget and retained output | `AgentManagement` | [`background-bash-widget.spec.md`](./background-bash-widget.spec.md) | `tests/e2e/background-bash-widget.spec.ts` (pending) | `Draft` |
| `SPEC-BB-PLATFORM` | Background bash unsupported platforms | `AgentManagement` | [`background-bash-platform-limitation.spec.md`](./background-bash-platform-limitation.spec.md) | Build-tagged Go tests; cross-platform E2E pending | `Draft` |
| `SPEC-RESET-OWNER-HANDLES` | Reset owner-bound handles and transaction outcomes | `AgentManagement` | [`reset-owner-handles.spec.md`](./reset-owner-handles.spec.md) | Go regression tests; complete E2E mapping pending | `Draft` |
| `SPEC-BB-FENCE-001` | Background bash owner lifetime fencing | `AgentManagement` | [`background-bash-owner-capability.spec.md`](./background-bash-owner-capability.spec.md) | `tests/e2e/background-bash-owner-isolation.spec.ts` (partial) | `Draft` |
| `SPEC-MANAGED-PROJECTION-001` | Managed agent views after a worker failure | `AgentManagement` | [`managed-agent-projection-resilience.spec.md`](./managed-agent-projection-resilience.spec.md) | `tests/e2e/managed-agent-projection-resilience.spec.ts` | `Active` |
| `SPEC-AGENT-CLAUDE-LATE-STEER` | Claude late steering echo | `AgentManagement` | [`claude-late-steer.spec.md`](./claude-late-steer.spec.md) | `tests/e2e/claude-late-echo.spec.ts` | `Draft` |
| `SPEC-AGENT-LEGACY-RPC-001` | Legacy RPC preservation | `AgentManagement` | [`legacy-rpc-preservation.spec.md`](./legacy-rpc-preservation.spec.md) | `tests/e2e/legacy-rpc-preservation.spec.ts` (planned) | `Draft` |
| `SPEC-CLAUDE-CHILD-CAPACITY-001` | Claude child resource capacity and cancellation | `AgentManagement` | [`claude-child-capacity.spec.md`](./claude-child-capacity.spec.md) | `tests/e2e/claude-child-reservations.spec.ts` and `tests/e2e/claude-child-capacity-fixture.spec.ts` (partial); Go admission regressions | `Draft` |
| `SPEC-CLAUDE-CHILD-ANSWER-001` | Claude child answer settlement | `AgentManagement` | [`claude-child-answer.spec.md`](./claude-child-answer.spec.md) | `tests/e2e/claude-child-answer-settlement-supplement.spec.ts` (eight settlement cases; Draft) | `Draft` |
| `SPEC-CLAUDE-CHILD-RESUME-001` | Claude child single attempt | `AgentManagement` | [`claude-child-effect-resume.spec.md`](./claude-child-effect-resume.spec.md) | `tests/e2e/claude-child-effect-resume.spec.ts` (partial) | `Draft` |
| `SPEC-AGENT-POLLING-001` | UI polling and freshness | `AgentManagement` | [`ui-polling-freshness.spec.md`](./ui-polling-freshness.spec.md) | `tests/e2e/ui-polling-freshness.spec.ts` | `Draft` |
| `SPEC-MAILBOX-ATTACHMENT-STARTUP-001` | Attachment Safety During Router Startup | `Mailbox` | [`attachment-startup-safety.spec.md`](./attachment-startup-safety.spec.md) | `tests/e2e/attachment-startup-safety.spec.ts` | `Active` |
| `SPEC-MAILBOX-TMUX-001` | tmux Claude worker mailbox | `Mailbox` | [`tmux-claude-mailbox.spec.md`](./tmux-claude-mailbox.spec.md) | `tests/e2e/tmux-claude-mailbox.spec.ts` | `Draft` |
| `SPEC-MAILBOX-RECEIPTS-001` | Bounded Conversation Send Receipts | `Mailbox` | [`conversation-send-receipt-retention.spec.md`](./conversation-send-receipt-retention.spec.md) | `tests/e2e/conversation-send-receipt-retention.spec.ts` | `Active` |
| `SPEC-MAILBOX-001` | Mailbox Progress Across Session Replacement | `Mailbox` | [`mailbox-session-recovery.spec.md`](./mailbox-session-recovery.spec.md) | `tests/e2e/mailbox-session-recovery.spec.ts` (one combined ordinary/reset TC; remaining matrix planned) | `Draft` |
| `SPEC-GOAL-001` | Bounded Automatic Goal Wakeups | `AgentManagement` | [`goal-wake-safety.spec.md`](./goal-wake-safety.spec.md) | `tests/e2e/goal-wake-safety.spec.ts` (one partial GWS-002/003 TC; remaining matrix planned) | `Draft` |
| `SPEC-ROUTER-INIT-001` | Fresh-machine Router Initialization | `Settings` | [`router-initialization.spec.md`](./router-initialization.spec.md) | `tests/e2e/router-initialization.spec.ts` | `Active` |
| `SPEC-ROUTER-RECIPE-MIGRATION-001` | Router Launch Recipe Migration Recovery | `Settings` | [`router-recipe-migration.spec.md`](./router-recipe-migration.spec.md) | `tests/e2e/router-recipe-migration.spec.ts` | `Active` |
| `SPEC-ROUTER-MIGRATION-001` | Router Retention Migration Recovery | `Settings` | [`router-retention-migration.spec.md`](./router-retention-migration.spec.md) | `tests/e2e/router-retention-migration.spec.ts` | `Active` |
| `SPEC-SQLITE-CONNECTION-LIFECYCLE` | SQLite connection lifecycle safety | `AgentManagement` | [`sqlite-connection-lifecycle.spec.md`](./sqlite-connection-lifecycle.spec.md) | `tests/e2e/embedded-stale-relaunch.spec.ts` (partial); replacement connection regression in store tests | `Draft` |
| `SPEC-AGENT-STALE-RELAUNCH` | Embedded relaunch safety | `AgentManagement` | [`embedded-stale-relaunch.spec.md`](./embedded-stale-relaunch.spec.md) | `tests/e2e/embedded-stale-relaunch.spec.ts` | `Draft` |
| `SPEC-AGENT-URGENCY-001` | Explicit Agent Urgency Tool | `AgentManagement` | [`agent-urgency.spec.md`](./agent-urgency.spec.md) | `tests/e2e/agent-urgency.spec.ts` | `Active` |
| `SPEC-LAUNCH-MODEL-VISIBILITY-001` | Launch Model Picker Visibility | `AgentManagement` | [`launch-model-picker-visibility.spec.md`](./launch-model-picker-visibility.spec.md) | `tests/e2e/launch-model-discovery.spec.ts` | `Active` |
| `SPEC-OPENAI-CATALOG-001` | OpenAI GPT-6.1 Terra Catalog | `AgentManagement` | [`openai-model-catalog.spec.md`](./openai-model-catalog.spec.md) | `tests/e2e/openai-model-catalog.spec.ts` | `Active` |
| `SPEC-RUNTIME-ENGINE-001` | Pi/Claude Runtime Adapter Boundary | `AgentManagement` | [`runtime-engine-boundary.spec.md`](./runtime-engine-boundary.spec.md) | `tests/e2e/runtime-engine-boundary.spec.ts` (planned) | `Draft` |
| `SPEC-CLAUDE-DEFAULT-001` | Claude router defaults, roots, children, goals and legacy retirement | `AgentManagement` | [`claude-router-default.spec.md`](./claude-router-default.spec.md) | `tests/e2e/claude-router-default.spec.ts` and other suites (partial; see spec matrix) | `Draft`; acceptance incomplete |
| `SPEC-CLAUDE-SUBAGENT-001` | Claude managed subagent lifecycle | `AgentManagement` | [`claude-managed-subagent-lifecycle.spec.md`](./claude-managed-subagent-lifecycle.spec.md) | `tests/e2e/claude-managed-subagent-lifecycle.spec.ts` | `Draft` |
| `SPEC-CLAUDE-TOOLS-001` | Claude Mecha tools and policy | `AgentManagement` | [`claude-mecha-tool-parity.spec.md`](./claude-mecha-tool-parity.spec.md) | `tests/e2e/claude-code-tool-parity.spec.ts` | `Draft` |
| `SPEC-CLAUDE-PRIVATE-MCP-001` | Claude private MCP transport | `AgentManagement` | [`claude-private-mcp.spec.md`](./claude-private-mcp.spec.md) | `tests/e2e/claude-code-tools.spec.ts` (pending) | `Draft` |
| `SPEC-AGENT-CLAUDE-FENCE-001` | Claude process admission and recovery | `AgentManagement` | [`claude-process-fence.spec.md`](./claude-process-fence.spec.md) | `tests/e2e/claude-code-workers.spec.ts` (partial; fault injection planned) | `Draft` |
| `SPEC-CLAUDE-DIAGNOSTICS-001` | Claude Protocol Diagnostics | `AgentManagement` | [`claude-protocol-diagnostics.spec.md`](./claude-protocol-diagnostics.spec.md) | `tests/e2e/claude-protocol-diagnostics.spec.ts` | `Draft`; acceptance pending |
| `SPEC-CLAUDE-STARTUP-TIMEOUTS-001` | Claude Code Worker Startup Timeouts and Milestone Diagnostics | `AgentManagement` | [`claude-worker-startup-timeouts.spec.md`](./claude-worker-startup-timeouts.spec.md) | `tests/e2e/claude-worker-startup-timeouts.spec.ts` | `Draft`; acceptance pending |
| `SPEC-LIFECYCLE-ANTIGRAVITY-001` | Antigravity CLI Managed Workers | `AgentManagement` | [`antigravity-cli-workers.spec.md`](./antigravity-cli-workers.spec.md) | `tests/e2e/antigravity-cli-workers.spec.ts` | `Draft` |
| `SPEC-LIFECYCLE-001` | Claude Code Managed Workers | `AgentManagement` | [`claude-code-workers-spec.md`](./claude-code-workers-spec.md) | `tests/e2e/claude-code-workers.spec.ts` (planned) | `Draft` |
| `SPEC-LIFECYCLE-CODEX-CATALOG-001` | Codex Catalog and Embedded Launch Parity | `AgentManagement` | [`codex-model-catalog.spec.md`](./codex-model-catalog.spec.md) | `tests/e2e/codex-model-catalog.spec.ts` | `Active` |
| `SPEC-LIFECYCLE-002` | Context Reset Window Config | `AgentManagement` | [`context-reset-window-spec.md`](./context-reset-window-spec.md) | `tests/e2e/context-reset-window.spec.ts` (planned) | `Active` |
| `SPEC-LIFECYCLE-003` | Tmux Worker Registry & List | `AgentManagement` | [`tmux-worker-list-spec.md`](./tmux-worker-list-spec.md) | `tests/e2e/tmux-worker-list.spec.ts` (planned) | `Draft` |
| `SPEC-LIFECYCLE-004` | Router MCP & OAuth Tool Routing | `Settings` | [`router-mcp-oauth-tools-spec.md`](./router-mcp-oauth-tools-spec.md) | `tests/e2e/router-mcp-oauth.spec.ts` (planned) | `Draft` |
| `SPEC-BACKGROUND-BASH-NO-WAIT-001` | Background bash without a wait tool | `AgentManagement` | [`background-bash-no-wait.spec.md`](./background-bash-no-wait.spec.md) | `tests/e2e/background-bash.spec.ts` and related suites (partial) | `Draft`; acceptance incomplete |
| `SPEC-BACKGROUND-BASH-001` | Background Bash Jobs and same-process promotion | `AgentManagement` | [`background-bash.spec.md`](./background-bash.spec.md) | `tests/e2e/background-bash.spec.ts` and related suites, including `tests/e2e/background-bash-promotion.spec.ts` and `tests/e2e/background-bash-claude-child-wait.spec.ts` (partial) | `Draft`; acceptance incomplete |
| `SPEC-LIFECYCLE-005` | Subagent Read Tools Budget & Redirection | `AgentManagement` | [`subagent-read-tools-budget.spec.md`](./subagent-read-tools-budget.spec.md) | `tests/e2e/subagent-read-tools-budget.spec.ts` | `Active` |
| `SPEC-SUBAGENT-COMPLETION-GUIDANCE-001` | Subagent Completion Forward Guidance | `AgentManagement` | [`subagent-completion-forward-guidance.spec.md`](./subagent-completion-forward-guidance.spec.md) | `router-proxy/internal/managed/embedded_subagent_results_test.go`, `router-proxy/internal/managed/embedded_subagent_runner_test.go`, `router-proxy/internal/domain/message_test.go` | `Active` |
| `SPEC-BACKGROUND-WORKFLOWS-001` | Background Workflows & Scheduled Execution | `Workflows` | [`SPEC-BACKGROUND-WORKFLOWS.md`](./SPEC-BACKGROUND-WORKFLOWS.md) | `tests/e2e/background-workflows.spec.ts` | `Active` |
| `SPEC-BACKGROUND-CLEANUP-001` | Background Worker Cleanup (24-hour age sweep; acceptance pending) | `Workflows` | [`background-worker-cleanup.spec.md`](./background-worker-cleanup.spec.md) | `tests/e2e/background-expiry.spec.ts`, `tests/e2e/background-cleanup.spec.ts`, `tests/e2e/background-scheduled-expiry.spec.ts`, `tests/e2e/background-active-retry.spec.ts` (partial) | `Draft` |
| `SPEC-LIFECYCLE-007` | Deterministic Embedded Own Reminder Delays & Idempotency | `AgentManagement` | [`embedded-own-reminder-delay.spec.md`](./embedded-own-reminder-delay.spec.md) | `router-proxy/internal/store/embedded_own_reminder_test.go` | `Active` |
| `SPEC-WORKFLOW-BACKGROUND-CATALOG-001` | Workflow background tool catalog | `Workflows` | [`workflow-background-tool-catalog.spec.md`](./workflow-background-tool-catalog.spec.md) | `tests/e2e/workflow-background-tool-catalog.spec.ts` | `Draft` |
| `SPEC-WORKFLOWS-JOURNAL-001` | Workflow run lifecycle journal | `Workflows` | [`workflow-run-journal.spec.md`](./workflow-run-journal.spec.md) | `tests/e2e/workflow-run-journal.spec.ts` (12 tests exist) | `Draft`; acceptance incomplete |
| `SPEC-WORKFLOWS-002` | Implicit Workflow Goals & Workflow-Level Snooze | `Workflows` | [`workflow-implicit-goals.spec.md`](./workflow-implicit-goals.spec.md) | `tests/e2e/workflow-implicit-goals.spec.ts` | `Active` |
| `SPEC-WORKFLOWS-SNOOZE-ABUSE-001` | Workflow Snooze Abuse Prevention & Mandatory Justification | `Workflows` | [`workflow-snooze-abuse.spec.md`](./workflow-snooze-abuse.spec.md) | `tests/e2e/workflow-implicit-goals.spec.ts` | `Active` |
| `SPEC-WORKFLOWS-IMPLICIT-RETURN-001` | Implicit Workflow Return to Previous State | `Workflows` | [`workflow-implicit-return.spec.md`](./workflow-implicit-return.spec.md) | `tests/e2e/workflow-implicit-return*.spec.ts` and focused return suites (partial; see spec matrix) | `Draft`; acceptance incomplete |
| `SPEC-TRANSCRIPT-EMPTY-TURN-001` | Empty assistant turn transcript retention | `AgentManagement` | [`empty-assistant-turn-transcript-retention.spec.md`](./empty-assistant-turn-transcript-retention.spec.md) | `router-proxy/internal/managed/embedded_google_auth_test.go` | `Active` |
| `SPEC-TOOL-PRESENTATION-RESILIENCE-001` | Tool Presentation Size Limit Resilience | `AgentManagement` | [`tool-presentation-resilience.spec.md`](./tool-presentation-resilience.spec.md) | `tests/e2e/tool-presentation-resilience.spec.ts` | `Active` |
| `SPEC-WORKFLOWS-003` | Structured Workflow Schedules & Test Clock | `Workflows` | [`SPEC-STRUCTURED-WORKFLOW-SCHEDULES.md`](./SPEC-STRUCTURED-WORKFLOW-SCHEDULES.md) | `tests/e2e/background-workflows.spec.ts` | `Active` |
| `SPEC-WORKFLOW-COLLECTIONS-001` | Multiple Workflow Collections with Per-Collection Git Sync, Move, and Copy | `Workflows` | [`workflow-collections.spec.md`](./workflow-collections.spec.md) | `tests/e2e/workflow-collections.spec.ts` | `Active` |

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
| `SPEC-EMBEDDED-TOOL-SCHEMA-001` | Embedded tool schema parity and rejection | `AgentManagement` | [`embedded-tool-schema-parity.spec.md`](./embedded-tool-schema-parity.spec.md) | `tests/e2e/embedded-tool-schema-parity.spec.ts`, `tests/e2e/embedded-tool-schema-rejections.spec.ts`, and `tests/e2e/embedded-tool-schema-google.spec.ts` (partial; acceptance unverified); actual wrapper boundary unverified | `Draft`; acceptance incomplete |
| `SPEC-SEARCH-001` | Codex Search (`codex_search`) | `Tooling` | [`codex-search.md`](./codex-search.md) | `tests/e2e/codex-search.spec.ts` (planned) | `Active / Proposed` |

---

## Settings & Configuration Specifications

Specifications governing provider settings, router MCP configurations, and server management menus.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-ROUTER-CONFIG-001` | Router JSON Config File & Custom KB Directory | `Settings` | [`router-config-file-and-kb.spec.md`](./router-config-file-and-kb.spec.md) | `tests/e2e/router-kb-root-selection.spec.ts` (partial coverage: CFG-009/010), `tests/e2e/router-config-file-and-kb.spec.ts` (planned) | `Draft` |
| `SPEC-SETTINGS-DEFAULT-MODEL-001` | Default Model Save Feedback & Concise Copy | `Settings` | [`default-model-save-feedback.spec.md`](./default-model-save-feedback.spec.md) | `tests/e2e/claude-sonnet-default-provider-settings.spec.ts` (DM-001); `tests/e2e/default-model-save-errors.spec.ts` (DM-003; DM-004 for 503 only); `tests/e2e/claude-selector-supplement.spec.ts` (DM-001; its DM-005 help-copy assertion is stale); DM-002 has no executable coverage | `Draft`; DM-002 gap, DM-005 stale |
| `SPEC-SETTINGS-DYNAMIC-PROVIDERS-001` | Dynamic Model Providers & Custom Endpoints | `Settings` | [`dynamic-model-providers.spec.md`](./dynamic-model-providers.spec.md) | `tests/e2e/dynamic-model-providers.spec.ts` | `Draft` |
| `SPEC-SETTINGS-002` | Server Management Dropdown Menu | `Settings` | [`SPEC-SERVER-MANAGEMENT-MENU.md`](./SPEC-SERVER-MANAGEMENT-MENU.md) | `tests/e2e/server-management-menu.spec.ts` | `Draft` |

---

## Agent Scratchpad Specifications

Specifications governing persistent agent scratchpad memory, notes retention, pinning, and storage pruning.

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-SCRATCHPAD-001` | Scratchpad Entry Truncation Tool & Storage | `AgentManagement` | [`scratchpad-truncation.spec.md`](./scratchpad-truncation.spec.md) | `tests/e2e/scratchpad-truncation.spec.ts` | `Draft` |

---

## Cleanup Contract Specifications

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-MEMORY-001` | Memory policy & absent catalog; Pi capture lifecycle retired | `Mailbox` | [`memory-capture.spec.md`](./memory-capture.spec.md) | `tests/e2e/memory-capture.spec.ts` (router policy and absent catalog only) | `Active` (router scope); lifecycle retired |
| `SPEC-ROUTER-TOOLS-001` | Router Native Tooling | `Mailbox` | [`router-native-tooling.spec.md`](./router-native-tooling.spec.md) | `tests/e2e/router-native-tooling.spec.ts` | `Active` |
| `SPEC-DESKTOP-001` | Desktop Package Payload | `AgentManagement` | [`desktop-package-payload.spec.md`](./desktop-package-payload.spec.md) | `tests/e2e/desktop-package-payload.spec.ts` | `Active` |
| `SPEC-MANAGED-RUNTIME-001` | Managed Runtime Contracts | `AgentManagement` | [`managed-runtime-contracts.spec.md`](./managed-runtime-contracts.spec.md) | Complete Playwright mapping pending | `Draft` |

---

## UI-live Readiness

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-AGENT-UI-LIVE-READINESS-001` | UI-live readiness and recovery | `AgentManagement` | [`ui-live-readiness.spec.md`](./ui-live-readiness.spec.md) | Pending | `Draft` |

---

## Spec Authoring & Maintenance Workflow

1. **Pre-Implementation Requirement**: No pull request or implementation commit modifying user behavior may land without an accompanying or pre-existing specification in `docs/specs/`.
2. **Scenario Completeness**: Every specification must detail both Happy Paths and Non-Happy/Edge/Error paths using strict `Given / When / Then` contracts.
3. **Verification Coupling**: Every scenario must map 1:1 to an automated Playwright E2E test case in Section 4 of the specification.
4. **Deprecation**: Deprecated specifications must have their status changed to `Deprecated` with a pointer to the superseding specification.

- [Goal evaluation activity publication](goal-activity-publication.spec.md) (Draft; acceptance testing pending).

- [Tool permissions popups](tool-permissions-popup.spec.md)

- [Web UI Oxanium typography](ui-typography-oxanium.spec.md)

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-UI-TYPE-001` | Local Oxanium typography and readable fallbacks | `AgentManagement` | [`ui-typography-oxanium.spec.md`](./ui-typography-oxanium.spec.md) | `tests/e2e/ui-typography-oxanium.spec.ts` | `Active` |

## Sidebar Branding

| Spec ID | Subsystem / Feature | Domain | Specification File | Target E2E Suite | Status |
| :--- | :--- | :--- | :--- | :--- | :--- |
| `SPEC-UI-BRAND-001` | Synapse sidebar wordmark | `AgentManagement` | [`sidebar-brand.spec.md`](./sidebar-brand.spec.md) | `tests/e2e/sidebar-brand.spec.ts` | `Active` |

- [Embedded restart concurrent replay](embedded-restart-replay-race.spec.md) — regression spec; unit coverage, E2E not added for this scoped fix.
