# mecha-developer

This repo holds dev-tooling skills for Wolfgang's AI agent workflow. Skills live in `.claude/skills/<name>/SKILL.md`.

## Mandatory portable policy

All Wolfgang-managed supervisors and workers must read `docs/wolfgang-agent-operating-policy.md` before substantive work. A machine-level `~/AGENTS.md` must include a mandatory reference to that file, or to the installed copy of that file on that machine, so the policy is loaded before role-specific supervisor or worker instructions.

When adding that reference on a machine, do a one-time extraction pass: move any broadly portable Wolfgang agent policies discovered in that machine's `~/AGENTS.md` into `docs/wolfgang-agent-operating-policy.md` and push them upstream. Going forward, new portable policy decisions should be added directly to the policy file rather than accumulating only in machine-local instructions.

Keep this policy portable. Put role mechanics in the supervisor/worker skills and put machine, project, credential, and incident facts in the KB or repo-specific instructions.

## How skills work

A skill is a self-contained instruction set. Any agent (Pi, Claude Code, Codex, Cursor) reads `SKILL.md` and follows the instructions. Skills can reference other skills, but the file is always authoritative — never assume what a skill does, look it up.

## Worker Runtime

Managed worker runtime is machine-local configuration. `worker_launch.py` reads `~/.mecha-developer/worker-runtime.json` (or `MECHA_WORKER_RUNTIME_CONFIG`) for `{"default_agent":"pi|claude|codex|cursor"}` and falls back to Pi when absent; explicit `--agent` overrides it. Ordinary managed Pi workers and subagents use provider `openai-codex` and model `gpt-5.6-terra`; PR-review workers and their subagents use `gpt-5.6-luna`. Use another model only when the task explicitly requires it. Set `MECHA_PI_MAILBOX_EXTENSION` to a validated local Mailbox V2 extension file and `MECHA_ROUTER_URL` to a loopback HTTP(S) base URL such as `http://127.0.0.1:8765`; the Pi extension derives its WebSocket endpoint internally. Managed Pi workers use Pi's default configured extensions and add the Mailbox V2 extension explicitly; do not add `--no-extensions` unless Wolfgang asks for a bare-extension diagnostic session. Claude workers use the launcher-managed Mailbox sidecar for terminal delivery and reply through `mailbox_send.py`.

The Wolfgang-maintained Pi Subagents fork at `/Users/wolfgang/code/pi-packages` is a required dependency of the mecha-developer system whenever native Pi subagent lifecycle behavior is used.

## Installing Wolfgang's Pi fork

```sh
npm install -g 'git+https://github.com/wolfgangmeyers/pi.git#wolfgang/glm5-bedrock-converse-fix'
```

Prefix the command with `sudo` only when the global npm prefix is root-owned. It is not a macOS requirement. Omitting `#wolfgang/glm5-bedrock-converse-fix` follows the fork default `main`, which now includes the repair.

## Router Proxy

`router-proxy/` is a Go reverse proxy for the Mailbox V2 router. The router itself binds loopback only (`127.0.0.1:8765` by default) and multiplexes HTTP REST plus WebSocket upgrades on `/v1/agents` over one port. The proxy does not reimplement any router routes; it transparently forwards every HTTP request and WebSocket frame to an upstream router over plain HTTP.

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

## Maintenance

When adding or updating a skill, keep this `CLAUDE.md`'s skill table current.

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

This repository does not use pull requests or feature branches by default. Work directly on `main`, keep the primary checkout clean between tasks, and integrate completed work into `main` and push it. Do not leave completed work, commits, or dirty files stranded on a branch or worktree.

In the primary mecha-developer checkout, dirty tracked changes are not acceptable as resting state. If the work is done, commit it and push it. If it is not done, tie every dirty file to an active task with an owner and next action; never leave unexplained or dangling dirty files.

Use a worktree only for larger or riskier mecha-developer changes or when isolation is genuinely useful. Dirty state is expected there only while that task is active. When the change is done, merge it into `main`, push it, and clean up the temporary branch/worktree. Use a PR or long-lived branch only when Wolfgang explicitly asks for one.

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

## Production Code Workflow

For any production-code implementation or change, the canonical `dev-loop` workflow is mandatory. The worker or implementing agent must load the `workflows` skill and fetch the source-of-truth workflow with `get_workflow.py dev-loop` before substantive implementation. Do not authorize production code, mark it complete, or open/advance a PR without dev-loop evidence for specification/plan, implementation, validation, coverage, and review.

## Delegation and Review Discipline

Before normal delegation, state the smallest observable outcome and explicit non-goals. At final review, map every changed file to an acceptance criterion or validated blocker; remove unsupported changes. Do not blame Pi or a model for a failure without controlled evidence separating it from the harness, prompt, extension, configuration, and process control.

When PR review feedback reveals a reusable lesson, capture the tribal knowledge instead of treating it as a one-off fix. Update the appropriate `mecha-review` robot-review heuristic, review skill, or agent-facing guidance so future reviews catch the same class of issue, then route or implement the product-code fix separately.

## Related repos

- `mecha-review` — owns self-contained code-review and review-adjacent skills (`review-pr`, `robot-review`, `pr-context`, `pr-summary`, `pr-validate`, `explain`). Not duplicated here.
- `mecha-developer` — original monorepo; non-review skills were migrated from there into this repo.
