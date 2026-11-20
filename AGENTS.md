# Synapse

This repo holds dev-tooling skills for Wolfgang's AI agent workflow. Skills live in `.claude/skills/<name>/SKILL.md`.

## Mandatory behavioral specifications and E2E testing policy

Any behavioral change, feature implementation, or bugfix in `mecha-developer` must adhere to the behavioral specification and testing standards defined in `docs/behavioral-specification-standard.md`.

- **Specification First**: Before writing or changing code that affects system behavior, a behavioral specification in `docs/specs/` must clearly document the expected behavior from the user's point of view, structured into user scenarios (covering both happy paths and edge/error paths).
- **Mandatory Spec Backfill**: When doing development or bugfixes on areas of the codebase where no behavioral specification exists yet, agents MUST backfill complete behavioral specifications for those affected areas before modifying or adding production code.
- **Mandatory Fast, Frictionless E2E Tests**: Behavioral changes must be backed by fast, reliable, non-flaky Playwright end-to-end tests in `tests/e2e/`. These tests must exercise real system boundaries—headless browser, WebSockets, REST API, router-proxy, and SQLite store—with LLM calls replaced by deterministic local mocks.
- **Retire Redundant Brittle Tests**: Integration tests that merely mock internal JavaScript functions or duplicate E2E test coverage without testing real end-to-end boundaries should be eliminated.
- **Specification Index**: The complete catalog of active behavioral specifications is maintained in `docs/specs/INDEX.md`.

Direct specifications: [permanent managed mode](docs/specs/permanent-managed-mode.spec.md), [embedded tool schema parity and rejection](docs/specs/embedded-tool-schema-parity.spec.md) (Draft; acceptance incomplete), [activity dialog visibility](docs/specs/activity-dialog-visibility.spec.md), [sidebar branding](docs/specs/sidebar-brand.spec.md), [Web UI Oxanium typography](docs/specs/ui-typography-oxanium.spec.md), [router launch recipe migration](docs/specs/router-recipe-migration.spec.md), [background bash](docs/specs/background-bash.spec.md) (Draft; acceptance incomplete), [memory capture](docs/specs/memory-capture.spec.md), [router native tooling](docs/specs/router-native-tooling.spec.md), [conversation send receipt retention](docs/specs/conversation-send-receipt-retention.spec.md), [attachment startup safety](docs/specs/attachment-startup-safety.spec.md), [recurring reminder terminal message recovery](docs/specs/recurring-reminder-terminal-message-recovery.spec.md), [desktop package payload](docs/specs/desktop-package-payload.spec.md), [UI polling and freshness](docs/specs/ui-polling-freshness.spec.md), [managed agent projection resilience](docs/specs/managed-agent-projection-resilience.spec.md), [managed runtime contracts](docs/specs/managed-runtime-contracts.spec.md) (Draft; acceptance incomplete), [empty assistant turn transcript retention](docs/specs/empty-assistant-turn-transcript-retention.spec.md), [subagent completion forward guidance](docs/specs/subagent-completion-forward-guidance.spec.md), [tool presentation size limit resilience](docs/specs/tool-presentation-resilience.spec.md), and [embedded mid-turn steering](docs/specs/embedded-mid-turn-steering.spec.md). Each specification retains its own status. See the [retirement crosswalk](docs/retirement-crosswalk.md) and [CLI archive record](docs/cli-archive-migration.md) for archival disposition.

## How skills work

A skill is a self-contained instruction set. Any agent (Pi, Claude Code, Codex, Cursor) reads `SKILL.md` and follows the instructions. Skills can reference other skills, but the file is always authoritative — never assume what a skill does, look it up.

## Worker Runtime

Managed worker runtime is machine-local configuration. `python3 .claude/skills/supervisor/tools/worker_launch.py` (the compatibility entry point for `tools/tmux-worker/worker_launch.py`) reads `~/.mecha-developer/worker-runtime.json` (or `MECHA_WORKER_RUNTIME_CONFIG`) for `{"default_agent":"pi|claude|codex|cursor"}` and falls back to Pi when absent; explicit `--agent` overrides it. Ordinary managed Pi workers, PR-review workers, and their subagents use `google-vertex/gemini-3.8-flash` by default. Use another model only when the task explicitly requires it. Set `MECHA_ROUTER_URL` to a loopback HTTP(S) base URL such as `http://127.0.0.1:8765` for compatible worker clients. The repository’s former `mecha-messaging` Pi extension has been removed; do not assume it provides mailbox delivery. Managed Pi launches preserve Pi's normal extension configuration. Do not pass flags that disable, replace, or selectively reconstruct extensions: every managed worker needs the same normal tools, including goal support, as an ordinary Pi session. Other runtimes likewise keep their normal extension policy. Claude workers use the launcher-managed Mailbox sidecar for terminal delivery and reply through `mailbox_send.py`.

The Wolfgang-maintained Pi Subagents fork at `$HOME/code/pi-packages` is a required dependency of the mecha-developer system whenever native Pi subagent lifecycle behavior is used.

## Installing upstream Pi

```sh
npm install -g @earendil-works/pi-coding-agent@latest
```

This installs the regular upstream Pi package. Prefix the command with `sudo` only when the global npm prefix is root-owned. It is not a macOS requirement.

## Router Proxy

`router-proxy/` contains the official Go Mailbox V2 router and its optional Go reverse proxy. Start the router from the repository root with `tools/run_router.sh`; managed mode is permanent and non-optional across all router operations. Managed launches use the embedded runtime; the former Pi RPC integration is retired. It binds loopback by default (`127.0.0.1:8765`) and multiplexes HTTP REST plus WebSocket upgrades on `/v1/agents` over one port. The proxy does not reimplement any router routes; it transparently forwards every HTTP request and WebSocket frame to an upstream router over plain HTTP.

Use it when another machine on the LAN, or a local process on a different port, needs the full router API surface without changing the upstream router bind address. Typical local test: upstream `127.0.0.1:8765`, listen `0.0.0.0:9876`, then point clients at `http://127.0.0.1:9876`.

Build:

```bash
go build -o router-proxy ./router-proxy
```

Launch:

```bash
go run ./router-proxy \
  --upstream-host 127.0.0.1 \
  --upstream-port 8765 \
  --listen-port 9876
```

Required flags: `--upstream-host`, `--upstream-port`, `--listen-port`. Optional `--listen-host` defaults to `0.0.0.0`. On success it prints:

```
MECHA_ROUTER_PROXY_READY listen=http://0.0.0.0:9876 upstream=http://127.0.0.1:8765
```

The proxy preserves upstream response headers and status codes, supports request bodies up to 6 MiB to match the router limit, and forwards WebSocket upgrades without injecting an `Origin` header. For compatible local clients, use `127.0.0.1` in `MECHA_ROUTER_URL` when testing through a different local port. More detail and a parity test recipe live in `router-proxy/README.md`.

### Safe Router Upgrades and Restarts

For zero-risk upgrades, database migration verification, worker quiescence, and crash-loop-safe restarts when operators lack direct keyboard access, follow the `router-safe-upgrade` skill (`.claude/skills/router-safe-upgrade/SKILL.md`).

## Isolated Router & Web UI Development Launches

Subagent background shells or standard ephemeral shell commands terminate child processes when their session or harness exits. Never launch long-running test routers directly in ephemeral tool shells without detaching them. Use a persistent session manager such as tmux, or launch detached background processes via `nohup` / `setsid`.

### Serving Web UI Directly via Router

Building the Web UI and pointing the router at its dist directory serves the frontend directly at `/` on the router's HTTP port. This eliminates separate static file servers and avoids cross-origin CORS overhead:

1. Build the UI:
   ```bash
   npm --prefix web/router-ui-react run build
   ```
2. Pass `--managed-ui-dist` (or export `MECHA_ROUTER_MANAGED_UI_DIST=/path/to/web/router-ui-react/dist`). When `tools/run_router.sh` runs and finds `web/router-ui-react/dist/index.html`, it automatically serves that built UI unless overridden.

### Option 1: tmux (Recommended for interactive dev & inspection)

Use detached tmux sessions to keep isolated routers running independently while retaining log capture and console attachment:

```bash
# Pick isolated ports and temporary state paths
ROUTER_PORT=18765
DB_PATH="/tmp/test-router.sqlite"
LOG_PATH="/tmp/test-router.jsonl"
UI_DIST="$(pwd)/web/router-ui-react/dist"

tmux new-session -d -s test-router -n router \
  "MECHA_ROUTER_PORT=${ROUTER_PORT} \
   MECHA_ROUTER_DATABASE=${DB_PATH} \
   MECHA_ROUTER_LOG=${LOG_PATH} \
   MECHA_ROUTER_MANAGED_UI_DIST=${UI_DIST} \
   ./tools/run_router.sh"

# Inspect output or verify health
tmux capture-pane -pt test-router:router -S -50
curl -fsS "http://127.0.0.1:${ROUTER_PORT}/v1/health"

# Stop session when finished
tmux kill-session -t test-router
```

### Option 2: nohup / Background Detachment (Scriptable across macOS and Linux)

For headless or script-driven automation without tmux:

```bash
ROUTER_PORT=18765
DB_PATH="/tmp/test-router.sqlite"
LOG_PATH="/tmp/test-router.jsonl"
OUT_LOG="/tmp/test-router.out"
PID_FILE="/tmp/test-router.pid"
UI_DIST="$(pwd)/web/router-ui-react/dist"

nohup env \
  MECHA_ROUTER_PORT="${ROUTER_PORT}" \
  MECHA_ROUTER_DATABASE="${DB_PATH}" \
  MECHA_ROUTER_LOG="${LOG_PATH}" \
  MECHA_ROUTER_MANAGED_UI_DIST="${UI_DIST}" \
  ./tools/run_router.sh > "${OUT_LOG}" 2>&1 &

echo $! > "${PID_FILE}"

# Poll health check until ready
curl --retry 10 --retry-delay 1 --retry-connrefused -fsS "http://127.0.0.1:${ROUTER_PORT}/v1/health"

# Stop when done
kill "$(cat "${PID_FILE}")" && rm -f "${PID_FILE}"
```

## System 1 Decision Model Integration

For high-speed, structured evaluation and classification (e.g. security checks, triage, policy verification), agents can leverage the System 1 decision model (Cloudflare Clef) hosted via Ollama. See `docs/system-one.md` for complete API schema, routing, and examples.

Rules for System 1 integrations:
- **Never hardcode hostnames or IPs**: Do not hardcode remote hostnames or specific Tailscale/LAN IPs. Call the local router proxy at `http://127.0.0.1:8765/v1/systemone` or dynamically discover the configured backend via `GET /v1/settings/systemone`.
- **Mandatory graceful fallback**: System 1 is an optional accelerator, not a hard blocker. If the endpoint is unconfigured or disabled (HTTP 503), times out (HTTP 504), or encounters a backend failure (HTTP 502/connection error), agents must gracefully degrade to standard LLM reasoning or heuristic defaults without crashing or halting the workflow.

## Browser transport CORS

The backend must accept browser calls from arbitrary frontend URLs. Cross-origin browser routes need permissive, non-credentialed CORS transport headers, including the request headers used for idempotency and diagnostics. This is compatibility, not access control: never parse, allowlist, log as identity, or authorize by `Origin` or CORS headers, and never enable credentialed CORS, cookies, or session-based browser authority. Browser clients must omit credentials for direct managed-control calls. Test both a direct cross-origin browser launch and an arbitrary `Origin`; same-origin proxies are useful but never the only supported route.

## Web UI Design System & Architecture

Any work on the Web UI (`web/router-ui-react`) must strictly follow the comprehensive design system, component patterns, and architectural standards in `docs/web-ui-design-system.md`. This includes the dark glassmorphic styling, neon accent system, sidebar architecture, partitioned conversation header, unified `.icon-btn-round` action buttons, continuous timeline across context resets, and strict separation between list and form views in modal dialogs.

## UI Icon Button Styling

Small icon-only action buttons across cards, headers, toolbars, and list items must use the compact circular icon button styling (`.icon-btn-round`: 24px x 24px, `border-radius: 999px`, subtle border and translucent background with hover states, as seen in the agent conversation header and agent list) rather than large blocky/rectangular buttons. See `docs/web-ui-design-system.md` for complete token and variant specifications.

## Config Dialog Layout Convention

Configuration and management dialogs (such as MCP Servers, Routers, Providers, etc.) must follow the convention of cleanly separating the "add new item / edit item" form from the list view. The main view should be purely a list view showing configured items, empty state if none exist, and an "+ Add <Item>" action button in the header/toolbar. It must not clutter the list view with permanently visible inline input form fields. Clicking "+ Add" or "Edit" transitions to a dedicated form view (with Back/Cancel and Save/Submit actions) that returns to the list view upon completion or cancellation. See `docs/web-ui-design-system.md` for full modal shell and view transition requirements.

## Maintenance

When adding or updating a skill, keep this skill table current.

## Skills

| Skill | Description |
|-------|-------------|
| `augment-persona` | Analyze a persona file for gaps, generate targeted scenario-based questions, and integrate answers back into the persona. Use for improving existing personas, not generating new ones. |
| `anxiety` | Monitor a target worker against a validated requirement packet from the same worktree, using read-only evidence and concrete escalation. |
| `decompose` | Break large tasks into phased dev-loop iterations with clear boundaries, ordering, and inter-phase contracts. |
| `generate-persona` | Generate a behavioral persona from any data source (chat logs, PR reviews, text files, git history), guided by intended use case. Replaces `generate-reviewer-persona` and `generate-supervisor-persona`. |
| `generate-supervisor-persona` | **Deprecated** — thin wrapper that redirects to `generate-persona` with `--use supervision --source chat-logs`. |
| `kb` | Manage the persistent markdown knowledge base at `~/.mecha-developer/kb/`. For persistent, reusable knowledge worth keeping across sessions. |
| `mailbox` | Mailbox V2 async mailbox — Mailbox V2 router delivery formats and mandatory pre-read state-update rules. **Load with supervisor** so runtime-delivered mail is handled correctly. |
| `pi-extension-maintenance` | Safely inspect, install, update, uninstall, and diagnose the registered checkout-source Mailbox V2 Pi extension without relocating, copying, symlinking, or restoring an installed artifact. |
| `pr-watch` | Watch assigned GitHub pull requests and run the required review workflow from the dedicated PR-review agent. |
| `remote-cloud-desktop` | Provision a generic Ubuntu KDE Plasma X11 desktop over xrdp with access restricted to a private overlay network. |
| `router-safe-upgrade` | Perform zero-risk, crash-loop-safe upgrades of the mecha mailbox router with pre-flight diff analysis, isolated dry-runs, worker quiescence, and post-restart health checks. |
| `roger-advanced-report-implementation` | Implement RogerEMR advanced/self-serve reports on the governed dataset, Parquet, DuckDB-WASM, and Report Builder architecture, distinct from fixed server-side reports. |
| `roger-report-implementation` | Create RogerEMR fixed reports with evidence-backed source parity, Terra planning, correct permissions/stacking, screen-export agreement, isolated synthetic demos, and report-specific review gates. |
| `slice-avatar-grid` | Slice AI-generated avatar grid sheets (like 4x4 robot helmet galleries) into individually centered, transparent PNG avatars for web/router-ui-react/avatars. Handles AI grid jitter, circle center detection, and antialiased circular background removal. |
| `subagent-orchestration` | Delegate bounded work with explicit contracts, evidence gates, context limits, safe handoffs, and stop rules while the parent keeps orchestration and final validation. |
| `supervisor` | Wolfgang's operations hub for routing tasks, managing workers, and tracking persistent state. Top-level session mode — do not invoke as a sub-skill. |
| `supervisor-review` | Supervisor readiness interrogation and quality gate before accepting worker done, blocked, parked, commit/push, draft PR, or review-ready state transitions. |
| `supervisor-sweep` | Craft and maintain the supervisor general sweep reminder so active work, open-PR feedback, CI, and merge-conflict checks are captured every time. |
| `test-coverage` | Run language-agnostic code coverage analysis and identify gaps with concrete test suggestions across supported project toolchains. |
| `voice` | Generate a voice profile from writing samples so an LLM can reproduce how someone speaks and writes. Source-agnostic: works with Facebook exports, chat logs, email archives, plain text, or any writing samples. |
| `watchdog` | LLM-based Watchdog momentum-nudge guidance. Not for direct invocation. |
| `worker` | Worker agent instructions — provided by the supervisor when launching or resuming a managed worker. Provides rules, mailbox communication patterns, and the standard plan/implement/review workflow. Supervisors instruct workers to read it through the runtime's verified delivery path; do not invoke manually. |
| `worker-launch` | Managed worker launch, resume, follow-up handoff, parking, shutdown, and Mailbox V2 coordination guidance for supervisors. |
| `wolfgang-voice-review` | Private final gate for rewriting validated PR review findings into Wolfgang's review voice before posting. |
| `workflow-management` | Manage and inspect router-owned durable workflow state graphs, transitions, and bundle schemas. Trigger: user asks about workflows, workflow definitions, bundle schemas, or states. |
| `worktree` | Create, list, or remove git worktrees for isolated workstreams as sibling directories of the main repo. |

### Subagent visibility checker

`.claude/skills/supervisor/tools/subagent_visibility_checker.py` independently captures visible active subagent rows from one or more tmux windows and compares them with router lifecycle state. Its comparison findings are limited to no description overlap and children visible only in tmux. See `python3 .claude/skills/supervisor/tools/subagent_visibility_checker.py --help` for usage.

## Supervisor Mailbox And Compaction

If a Pi supervisor session has a compatible Mailbox V2 tool installed, use the native top-level `mecha_mailbox_send` tool for supervisor replies. The deleted repository `mecha-messaging` extension is not an installation source. It returns the accepted message ID and state through the existing router client. Use the Mailbox V2 router sender below as the fallback for runtimes without the native tool or launcher-side sends:
`/home/ubuntu/code/mecha-developer/.claude/skills/supervisor/tools/mailbox_send.py`.
Successful fallback sends print the accepted message ID and actual router state. Do not use stale
legacy mailbox sender copies; they write legacy disk mailbox files that
the active router does not deliver.

After requesting Pi context compaction, stop the supervisor loop and let the
session go idle. Do not keep replying to mailbox pings or running checks while a
compaction is pending; Pi will not compact until the session reaches the idle
settled boundary.

## Repository workflow

Do not create a separate branch or worktree for `mecha-developer` unless Wolfgang explicitly instructs it. Work directly on `main` and push completed scoped changes to `origin/main`.

Before changing files, inspect the checkout and preserve unrelated dirty or untracked files. Do not reset, stash, overwrite, or include unrelated work. Commit only the requested changes on `main`; leave unrelated work untouched and report it if it prevents a safe commit or push.

If Wolfgang explicitly authorizes a separate worktree or branch, use it only for that task and integrate the completed change back into `main`, push it, and clean up the temporary checkout. Use a PR or long-lived branch only when Wolfgang explicitly asks for one.

## Data retention

Any feature that accumulates data over time—ledgers, logs, histories, queues,
caches, reports, or state records—must define and implement bounded retention
before it ships. Prefer built-in expiry, rotation, or pruning. If history must
outlive the active store, define a bounded archive plus its owner and trigger.
Do not leave cleanup as a manual future task. Test the retention path and
document the size or age limit.

## User input

Wolfgang often uses voice transcription, so requests may include phonetic spellings
or near-miss names. Infer the likely intended repo, skill, tool, or command from
context when the meaning is clear; ask or verify before acting when ambiguity
could affect files, external communication, or destructive operations.

## Completion Autonomy

When Wolfgang directs an outcome be carried to completion or gives broad authorization, proceed through ordinary reversible implementation, validation, review, and local integration without asking again; stop only for an explicit hold or a genuine new external, irreversible, security, legal, or credential decision.

### Authorization and handoff

Wolfgang has authorized all ordinary scoped work in the current task. A concrete goal or `/goal set` is execution authorization. Do not ask for a second authorization or leave workers waiting. Immediately assign an owner, exact next action, base and branch, done conditions, and push limits. Only merge or undraft operations, deployment, IAM or ruleset changes, and genuinely irreversible, external, security, or legal actions remain separately gated.

## Automated testing policy

Do not add tests that verify removed functionality is absent. Remove such tests when found, while preserving positive coverage for functionality that remains.

Prioritize behavioral functional tests that put as many system-owned components and boundaries together as practical, with dependencies mocked as necessary, and prove the software works. Automated tests must not call LLMs or external services; replace those dependencies with deterministic mocks at the external boundary. Tests must satisfy and map every applicable requirement. Isolated unit tests are supporting evidence, not sufficient when an owned cross-component path can be exercised.

## Behavioral Functional Testing Standard

Behavioral functional tests should exercise things hooked together — ideally a full router running workers with mock LLMs in those tests, verifying all expected behavior end-to-end across the full lifecycle (inbound messages, turns, tool execution, answer synthesis, context reset, scratchpad, goal loops, outbox delivery, and WebSocket notifications).

## Single-User Workstation Assumption and Directory Permissions

These are single-user developer workstations, not shared multi-user or hostile multi-tenant mainframes. Do not invent unrequested multi-user containment restrictions or hostile same-user permission gates.

Specifically, user home directories and ancestor paths on macOS and Linux standard configurations often use 0750 or 0755 permissions by default. When verifying path safety for local state or credential stores, never require 0700 on ancestor directories. Only verify that ancestors are not group- or world-writable (mode & 0o022 == 0) and that the private leaf directory itself is owner-only (0700).

## Delegation and Review Discipline

**MUST HAVE: Never request review from someone on a PR while CI is not green. Getting CI green is the highest priority for a PR.**

Before normal delegation, state the smallest observable outcome and explicit non-goals. At final review, map every changed file to an acceptance criterion or validated blocker; remove unsupported changes. Do not blame Pi or a model for a failure without controlled evidence separating it from the harness, prompt, extension, configuration, and process control.

When PR review feedback reveals a reusable lesson, capture the tribal knowledge instead of treating it as a one-off fix. Update the appropriate robot-review heuristic, review skill, or agent-facing guidance so future reviews catch the same class of issue, then route or implement the product-code fix separately.
