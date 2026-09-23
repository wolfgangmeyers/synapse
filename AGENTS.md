# mecha-developer

This repo holds dev-tooling skills for Wolfgang's AI agent workflow. Skills live in `.claude/skills/<name>/SKILL.md`.

## Mandatory portable policy

All Wolfgang-managed supervisors and workers must read `docs/wolfgang-agent-operating-policy.md` before substantive work. A machine-level `~/AGENTS.md` must include a mandatory reference to that file, or to the installed copy of that file on that machine, so the policy is loaded before role-specific supervisor or worker instructions.

When adding that reference on a machine, do a one-time extraction pass: move any broadly portable Wolfgang agent policies discovered in that machine's `~/AGENTS.md` into `docs/wolfgang-agent-operating-policy.md` and push them upstream. Going forward, new portable policy decisions should be added directly to the policy file rather than accumulating only in machine-local instructions.

Keep this policy portable. Put role mechanics in the supervisor/worker skills and put machine, project, credential, and incident facts in the KB or repo-specific instructions.

## How skills work

A skill is a self-contained instruction set. Any agent (Pi, Claude Code, Codex, Cursor) reads `SKILL.md` and follows the instructions. Skills can reference other skills, but the file is always authoritative — never assume what a skill does, look it up.

## Worker Runtime

Managed worker runtime is machine-local configuration. `worker_launch.py` reads `~/.mecha-developer/worker-runtime.json` (or `MECHA_WORKER_RUNTIME_CONFIG`) for `{"default_agent":"pi|claude|codex|cursor"}` and falls back to Pi when absent; explicit `--agent` overrides it. Ordinary managed Pi workers and subagents use provider `openai-codex` and model `gpt-5.6-sol` by default. Use `gpt-6-astra` for larger or more difficult tasks when warranted. PR-review workers and their subagents use `gpt-5.6-luna`. Set `MECHA_ROUTER_URL` to a loopback HTTP(S) base URL such as `http://127.0.0.1:8765`; the Pi extension derives its WebSocket endpoint internally. Managed Pi launches preserve Pi's normal extension configuration. Do not pass flags that disable, replace, or selectively reconstruct extensions: every managed worker needs the same normal tools, including goal support, as an ordinary Pi session. Other runtimes likewise keep their normal extension policy. Claude workers use the launcher-managed Mailbox sidecar for terminal delivery and reply through `mailbox_send.py`.

The Wolfgang-maintained Pi Subagents fork at `/Users/wolfgang/code/pi-packages` is a required dependency of the mecha-developer system whenever native Pi subagent lifecycle behavior is used.

## Installing Wolfgang's Pi fork

```sh
npm install -g 'git+https://github.com/wolfgangmeyers/pi.git#wolfgang/glm5-bedrock-converse-fix'
```

Prefix the command with `sudo` only when the global npm prefix is root-owned. It is not a macOS requirement. Omitting `#wolfgang/glm5-bedrock-converse-fix` follows the fork default `main`, which now includes the repair.

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

## Browser transport CORS

The backend must accept browser calls from arbitrary frontend URLs. Cross-origin browser routes need permissive, non-credentialed CORS transport headers, including the request headers used for idempotency and diagnostics. This is compatibility, not access control: never parse, allowlist, log as identity, or authorize by `Origin` or CORS headers, and never enable credentialed CORS, cookies, or session-based browser authority. Browser clients must omit credentials for direct managed-control calls. Test both a direct cross-origin browser launch and an arbitrary `Origin`; same-origin proxies are useful but never the only supported route.

## Maintenance

When adding or updating a skill, keep this `CLAUDE.md`'s skill table current.

### Subagent visibility checker

`.claude/skills/supervisor/tools/subagent_visibility_checker.py` independently captures visible active subagent rows from one or more tmux windows and compares them with router lifecycle state. Its comparison findings are limited to no description overlap and children visible only in tmux. See `python3 .claude/skills/supervisor/tools/subagent_visibility_checker.py --help` for usage.

## Supervisor Mailbox And Compaction

In a Pi supervisor session with the Mailbox V2 extension, use the native top-level `mecha_mailbox_send` tool for supervisor replies. It returns the accepted message ID and state through the existing router client. Use the Mailbox V2 router sender below as the fallback for runtimes without the native tool or launcher-side sends:
`/home/ubuntu/code/mecha-developer/.claude/skills/supervisor/tools/mailbox_send.py`.
Successful fallback sends print the accepted message ID and actual router state. Do not use stale
`mecha-wolfgang` mailbox sender copies; they write legacy disk mailbox files that
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

## Production Code Workflow

For any production-code implementation or change, the canonical `dev-loop` workflow is mandatory. The worker or implementing agent must load the `workflows` skill and fetch the source-of-truth workflow with `get_workflow.py dev-loop` before substantive implementation. Do not authorize production code, mark it complete, or open/advance a PR without dev-loop evidence for specification/plan, implementation, validation, coverage, and review.

## Automated testing policy

Prioritize behavioral functional tests that put as many system-owned components and boundaries together as practical, with dependencies mocked as necessary, and prove the software works. Automated tests must not call LLMs or external services; replace those dependencies with deterministic mocks at the external boundary. Tests must satisfy and map every applicable requirement. Isolated unit tests are supporting evidence, not sufficient when an owned cross-component path can be exercised.

## TypeScript Type Safety

In `pi-extensions/mecha-messaging`, TypeScript code must preserve type safety. Do not use `as any`, `as unknown as`, or assertion/casting escape hatches to silence or evade a type error. Use runtime validation and narrowing at external or untyped boundaries. An assertion is allowed only when a documented, narrow boundary-validation case is absolutely necessary: keep it at that boundary, validate the value first, and add a nearby comment naming the boundary and the validation that makes the assertion sound. Do not weaken the compiler or linter to make an unsafe assertion pass.

Run `npm run lint:mecha-messaging` (or `npm --prefix pi-extensions/mecha-messaging run lint`) for the enforced checks. The package linter enables `@typescript-eslint/no-explicit-any` and `@typescript-eslint/no-unsafe-type-assertion`; this rule does not enable global TypeScript strict mode or change migration, runtime, settings, installer, or test behavior.

## Delegation and Review Discipline

**MUST HAVE: Never request review from someone on a PR while CI is not green. Getting CI green is the highest priority for a PR.**

Before normal delegation, state the smallest observable outcome and explicit non-goals. At final review, map every changed file to an acceptance criterion or validated blocker; remove unsupported changes. Do not blame Pi or a model for a failure without controlled evidence separating it from the harness, prompt, extension, configuration, and process control.

When PR review feedback reveals a reusable lesson, capture the tribal knowledge instead of treating it as a one-off fix. Update the appropriate `mecha-review` robot-review heuristic, review skill, or agent-facing guidance so future reviews catch the same class of issue, then route or implement the product-code fix separately.

## Related repos

- `mecha-review` — owns self-contained code-review and review-adjacent skills (`review-pr`, `robot-review`, `pr-context`, `pr-summary`, `pr-validate`, `explain`). Not duplicated here.
- `mecha-developer` — original monorepo; non-review skills were migrated from there into this repo.
