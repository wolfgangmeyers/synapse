# mecha-developer

This repo holds dev-tooling skills for Wolfgang's AI agent workflow. Skills live in `.claude/skills/<name>/SKILL.md`.

## How skills work

A skill is a self-contained instruction set. Any agent (Pi, Claude Code, Codex, Cursor) reads `SKILL.md` and follows the instructions. Skills can reference other skills, but the file is always authoritative — never assume what a skill does, look it up.

## Worker Runtime

Managed worker runtime is machine-local configuration. `worker_launch.py` reads `~/.mecha-developer/worker-runtime.json` (or `MECHA_WORKER_RUNTIME_CONFIG`) for `{"default_agent":"pi|claude|codex|cursor"}` and falls back to Pi when absent; explicit `--agent` overrides it. When runtime is Pi, preserve provider `openai-codex` and model `gpt-5.5`. Set `MECHA_PI_MAILBOX_EXTENSION` to a validated local Mailbox V2 extension file and `MECHA_ROUTER_URL` to a loopback HTTP(S) base URL such as `http://127.0.0.1:8765`; the Pi extension derives its WebSocket endpoint internally. Managed Pi workers use Pi's default configured extensions and add the Mailbox V2 extension explicitly; do not add `--no-extensions` unless Wolfgang asks for a bare-extension diagnostic session. Claude workers use the launcher-managed Mailbox sidecar for terminal delivery and reply through `mailbox_send.py`.

## Maintenance

When adding or updating a skill, keep this `CLAUDE.md`'s skill table current.

## Repository workflow

For this repository, make changes on `main` by default. Do not create or use a worktree for mecha-developer changes unless Wolfgang explicitly asks for one.

When a change is done, merge it into `main` and push it up. Do not leave completed work parked only on a side branch or worktree unless Wolfgang explicitly asks for that.

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

## Delegation and Review Discipline

Before normal delegation, state the smallest observable outcome and explicit non-goals. At final review, map every changed file to an acceptance criterion or validated blocker; remove unsupported changes. Do not blame Pi or a model for a failure without controlled evidence separating it from the harness, prompt, extension, configuration, and process control.

When PR review feedback reveals a reusable lesson, capture the tribal knowledge instead of treating it as a one-off fix. Update the appropriate `mecha-review` robot-review heuristic, review skill, or agent-facing guidance so future reviews catch the same class of issue, then route or implement the product-code fix separately.

## Related repos

- `mecha-review` — owns self-contained code-review and review-adjacent skills (`review-pr`, `robot-review`, `pr-context`, `pr-summary`, `pr-validate`, `explain`). Not duplicated here.
- `mecha-developer` — original monorepo; non-review skills were migrated from there into this repo.
