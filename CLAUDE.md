# mecha-developer

This repo holds dev-tooling skills for Wolfgang's AI agent workflow. Skills live in `.claude/skills/<name>/SKILL.md`.

## How skills work

A skill is a self-contained instruction set. Any agent (Pi, Claude Code, Codex, Cursor) reads `SKILL.md` and follows the instructions. Skills can reference other skills, but the file is always authoritative — never assume what a skill does, look it up.

## Worker Runtime

Managed worker runtime is machine-local configuration. `worker_launch.py` reads `~/.mecha-developer/worker-runtime.json` (or `MECHA_WORKER_RUNTIME_CONFIG`) for `{"default_agent":"pi|claude|codex|cursor"}` and falls back to Pi when absent; explicit `--agent` overrides it. The file has a second reader
with a deliberately different contract: `supervisor_start.py` refuses to start and exits 2 when the
file is missing, rather than falling back, because a supervisor launched on a runtime this machine
cannot run fails silently on its first model call. When runtime is Pi, preserve provider `anthropic` and model `claude-opus-5`. Set `MECHA_PI_MAILBOX_EXTENSION` to a validated local Mailbox V2 extension file and `MECHA_ROUTER_URL` to a loopback HTTP(S) base URL such as `http://127.0.0.1:8765`; the Pi extension derives its WebSocket endpoint internally. Managed Pi workers use Pi's default configured extensions and add the Mailbox V2 extension explicitly; do not add `--no-extensions` unless Wolfgang asks for a bare-extension diagnostic session. Claude workers use the launcher-managed Mailbox sidecar for terminal delivery and reply through `mailbox_send.py`.

## Maintenance

When adding or updating a skill, keep this CLAUDE.md's skill table (below) current.

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

## Related repos

- `mecha-review` — owns self-contained code-review and review-adjacent skills (`review-pr`, `robot-review`, `pr-context`, `pr-summary`, `pr-validate`, `explain`). Not duplicated here.
- `mecha-developer` — original monorepo; non-review skills were migrated from there into this repo.

## Skills

| Skill | Description |
|-------|-------------|
| `augment-persona` | Analyze a persona file for gaps, generate targeted scenario-based questions, and integrate answers back into the persona. Use for improving existing personas, not generating new ones. |
| `decompose` | Break large tasks into phased dev-loop iterations with clear boundaries, ordering, and inter-phase contracts. |
| `generate-persona` | Generate a behavioral persona from any data source (chat logs, PR reviews, text files, git history), guided by intended use case. Replaces `generate-reviewer-persona` and `generate-supervisor-persona`. |
| `generate-supervisor-persona` | **Deprecated** — thin wrapper that redirects to `generate-persona` with `--use supervision --source chat-logs`. |
| `install` | Set up or reinstall mecha-developer by symlinking personas, skills, and hooks to the home directory. One-time setup utility. |
| `kb` | Manage the persistent markdown knowledge base at `~/.mecha-developer/kb/`. For persistent, reusable knowledge worth keeping across sessions. |
| `mailbox` | Mecha-wolfgang async mailbox — delivery format, mandatory pre-read `state_update` blocks, hooks/cron. **Load with supervisor** so `[MAILBOX]` injections are handled correctly. Spec: `MAILBOX_REQUIREMENTS.md` at repo root. |
| `pi-extension-maintenance` | Safely inspect, install, update, uninstall, and diagnose the copied Mailbox V2 Pi extension lifecycle without touching unrelated Pi configuration. |
| `pr-watch` | Run and maintain the read-only polling GitHub PR watcher used as a lightweight substitute for the webhook PR review bot. |
| `reminders` | Set one-shot reminders that fire via mailbox after a delay. Set, list, or delete reminders for any tmux window. |
| `remote-cloud-desktop` | Provision a generic Ubuntu KDE Plasma X11 desktop over xrdp with access restricted to a private overlay network. |
| `roger-advanced-report-implementation` | Implement RogerEMR advanced/self-serve reports on the governed dataset, Parquet, DuckDB-WASM, and Report Builder architecture, distinct from fixed server-side reports. |
| `roger-report-implementation` | Create RogerEMR fixed reports with evidence-backed source parity, Sol planning, correct permissions/stacking, screen-export agreement, isolated synthetic demos, and report-specific review gates. |
| `supervisor` | Wolfgang's operations hub for routing tasks, managing workers, and tracking persistent state. Top-level session mode — do not invoke as a sub-skill. |
| `supervisor-review` | Supervisor readiness interrogation and quality gate before accepting worker done, blocked, parked, commit/push, draft PR, or review-ready state transitions. |
| `test-coverage` | Run language-agnostic code coverage analysis and identify gaps with concrete test suggestions across supported project toolchains. |
| `unramble` | Compress verbose Claude output into concise Wolfgang voice. Use when Claude sends wall-of-text output that needs condensation before forwarding to a human. |
| `voice` | Generate a voice profile from writing samples so an LLM can reproduce how someone speaks and writes. Source-agnostic: works with Facebook exports, chat logs, email archives, plain text, or any writing samples. |
| `voice-review-builder` | Collect and distill Wolfgang's feedback on PR review comments to improve review voice and review quality. |
| `watchdog` | LLM-based Watchdog momentum-nudge guidance. The separate Priority Handoff Monitor owns deterministic five-minute checks. Not for direct invocation. |
| `worker` | Worker agent instructions — injected by the supervisor at launch. Provides rules, mailbox communication patterns, and the standard plan/implement/review workflow. Loaded automatically; do not invoke manually. |
| `wolfgang-voice-review` | Private final gate for rewriting validated PR review findings into Wolfgang's review voice before posting. |
| `workflows` | Manage and execute plain-English workflow templates. Load whenever a workflow is mentioned by name — even casually — before acting on it. |
| `worktree` | Create, list, or remove git worktrees for isolated workstreams as sibling directories of the main repo. |
