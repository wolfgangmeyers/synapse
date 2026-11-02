# mecha-developer

This repo holds dev-tooling skills for Wolfgang's AI agent workflow. Skills live in `.claude/skills/<name>/SKILL.md`.

## Mandatory behavioral specifications and E2E testing policy

Any behavioral change, feature implementation, or bugfix in `mecha-developer` must adhere to the behavioral specification and testing standards defined in `docs/behavioral-specification-standard.md`.

- **Specification First**: Before writing or changing code that affects system behavior, a behavioral specification in `docs/specs/` must clearly document the expected behavior from the user's point of view, structured into user scenarios (covering both happy paths and edge/error paths).
- **Mandatory Spec Backfill**: When doing development or bugfixes on areas of the codebase where no behavioral specification exists yet, agents MUST backfill complete behavioral specifications for those affected areas before modifying or adding production code.
- **Mandatory Fast, Frictionless E2E Tests**: Behavioral changes must be backed by fast, reliable, non-flaky Playwright end-to-end tests in `tests/e2e/`. These tests must exercise real system boundaries—headless browser, WebSockets, REST API, router-proxy, and SQLite store—with LLM calls replaced by deterministic local mocks.
- **Retire Redundant Brittle Tests**: Integration tests that merely mock internal JavaScript functions or duplicate E2E test coverage without testing real end-to-end boundaries should be eliminated.
- **Specification Index**: The complete catalog of active behavioral specifications is maintained in `docs/specs/INDEX.md`.

## How skills work

A skill is a self-contained instruction set. Any agent (Pi, Claude Code, Codex, Cursor) reads `SKILL.md` and follows the instructions. Skills can reference other skills, but the file is always authoritative — never assume what a skill does, look it up.

## Worker Runtime

Managed worker runtime is machine-local configuration. `worker_launch.py` reads `~/.mecha-developer/worker-runtime.json` (or `MECHA_WORKER_RUNTIME_CONFIG`) for `{"default_agent":"pi|claude|codex|cursor"}` and falls back to Pi when absent; explicit `--agent` overrides it. Ordinary managed Pi workers, PR-review workers, and their subagents use `google-vertex/gemini-3.8-flash` by default. Use another model only when the task explicitly requires it. Set `MECHA_ROUTER_URL` to a loopback HTTP(S) base URL such as `http://127.0.0.1:8765`; the Pi extension derives its WebSocket endpoint internally. Managed Pi launches preserve Pi's normal extension configuration. Do not pass flags that disable, replace, or selectively reconstruct extensions: every managed worker needs the same normal tools, including goal support, as an ordinary Pi session. Other runtimes likewise keep their normal extension policy. Claude workers use the launcher-managed Mailbox sidecar for terminal delivery and reply through `mailbox_send.py`.

The Wolfgang-maintained Pi Subagents fork at `$HOME/code/pi-packages` is a required dependency of the mecha-developer system whenever native Pi subagent lifecycle behavior is used.

## Installing upstream Pi

```sh
npm install -g @earendil-works/pi-coding-agent@latest
```

This installs the regular upstream Pi package. Prefix the command with `sudo` only when the global npm prefix is root-owned. It is not a macOS requirement.

## Router Proxy

`router-proxy/` contains the official Go Mailbox V2 router and its optional Go reverse proxy. Start the router from the repository root with `tools/run_router.sh`; router-managed Pi is enabled by default, and `MECHA_ROUTER_MANAGED=false` preserves legacy-only routing when necessary. It binds loopback by default (`127.0.0.1:8765`) and multiplexes HTTP REST plus WebSocket upgrades on `/v1/agents` over one port. The proxy does not reimplement any router routes; it transparently forwards every HTTP request and WebSocket frame to an upstream router over plain HTTP.

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

The proxy preserves upstream response headers and status codes, supports request bodies up to 6 MiB to match the router limit, and forwards WebSocket upgrades without injecting an `Origin` header. Pi extension clients still require a loopback hostname in `MECHA_ROUTER_URL`; use `127.0.0.1` when testing through a different local port. More detail and a parity test recipe live in `router-proxy/README.md`.

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

When adding or updating a skill, keep this `CLAUDE.md`'s skill table current.

### Subagent visibility checker

`.claude/skills/supervisor/tools/subagent_visibility_checker.py` independently captures visible active subagent rows from one or more tmux windows and compares them with router lifecycle state. Its comparison findings are limited to no description overlap and children visible only in tmux. See `python3 .claude/skills/supervisor/tools/subagent_visibility_checker.py --help` for usage.

## Supervisor Mailbox And Compaction

In a Pi supervisor session with the Mailbox V2 extension, use the native top-level `mecha_mailbox_send` tool for supervisor replies. It returns the accepted message ID and state through the existing router client. Use the Mailbox V2 router sender below as the fallback for runtimes without the native tool or launcher-side sends:
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

Prioritize behavioral functional tests that put as many system-owned components and boundaries together as practical, with dependencies mocked as necessary, and prove the software works. Automated tests must not call LLMs or external services; replace those dependencies with deterministic mocks at the external boundary. Tests must satisfy and map every applicable requirement. Isolated unit tests are supporting evidence, not sufficient when an owned cross-component path can be exercised.

## Behavioral Functional Testing Standard

Behavioral functional tests should exercise things hooked together — ideally a full router running workers with mock LLMs in those tests, verifying all expected behavior end-to-end across the full lifecycle (inbound messages, turns, tool execution, answer synthesis, context reset, scratchpad, goal loops, outbox delivery, and WebSocket notifications).

## TypeScript Type Safety

In `pi-extensions/mecha-messaging`, TypeScript code must preserve type safety. Do not use `as any`, `as unknown as`, or assertion/casting escape hatches to silence or evade a type error. Use runtime validation and narrowing at external or untyped boundaries. An assertion is allowed only when a documented, narrow boundary-validation case is absolutely necessary: keep it at that boundary, validate the value first, and add a nearby comment naming the boundary and the validation that makes the assertion sound. Do not weaken the compiler or linter to make an unsafe assertion pass.

Run `npm run lint:mecha-messaging` (or `npm --prefix pi-extensions/mecha-messaging run lint`) for the enforced checks. The package linter enables `@typescript-eslint/no-explicit-any` and `@typescript-eslint/no-unsafe-type-assertion`; this rule does not enable global TypeScript strict mode or change migration, runtime, settings, installer, or test behavior.

## Single-User Workstation Assumption and Directory Permissions

These are single-user developer workstations, not shared multi-user or hostile multi-tenant mainframes. Do not invent unrequested multi-user containment restrictions or hostile same-user permission gates.

Specifically, user home directories and ancestor paths on macOS and Linux standard configurations often use 0750 or 0755 permissions by default. When verifying path safety for local state or credential stores, never require 0700 on ancestor directories. Only verify that ancestors are not group- or world-writable (mode & 0o022 == 0) and that the private leaf directory itself is owner-only (0700).

## Delegation and Review Discipline

**MUST HAVE: Never request review from someone on a PR while CI is not green. Getting CI green is the highest priority for a PR.**

Before normal delegation, state the smallest observable outcome and explicit non-goals. At final review, map every changed file to an acceptance criterion or validated blocker; remove unsupported changes. Do not blame Pi or a model for a failure without controlled evidence separating it from the harness, prompt, extension, configuration, and process control.

When PR review feedback reveals a reusable lesson, capture the tribal knowledge instead of treating it as a one-off fix. Update the appropriate robot-review heuristic, review skill, or agent-facing guidance so future reviews catch the same class of issue, then route or implement the product-code fix separately.
