# mecha-developer

This repo holds dev-tooling skills for Wolfgang's AI agent workflow. Skills live in `.claude/skills/<name>/SKILL.md`.

## How skills work

A skill is a self-contained instruction set. Any agent (Pi, Claude Code, Codex, Cursor) reads `SKILL.md` and follows the instructions. Skills can reference other skills, but the file is always authoritative — never assume what a skill does, look it up.

## Worker Runtime

Managed worker runtime is machine-local configuration. `worker_launch.py` reads `~/.mecha-developer/worker-runtime.json` (or `MECHA_WORKER_RUNTIME_CONFIG`) for `{"default_agent":"pi|claude|codex|cursor"}` and falls back to Pi when absent; explicit `--agent` overrides it. The file has one runtime reader. Ordinary managed Pi workers and subagents use provider `openai-codex` and model `gpt-5.6-terra`; PR-review workers and their subagents use `gpt-5.6-luna`. Use another model only when the task explicitly requires it. Set `MECHA_ROUTER_URL` to a loopback HTTP(S) base URL such as `http://127.0.0.1:8765`; the Pi extension derives its WebSocket endpoint internally. Normal Pi launches use Pi's default configured extensions and pass neither `--extension` nor `--no-extensions`/`-ne`; those flags are reserved for deliberate extension-test tools. Claude workers use the launcher-managed Mailbox sidecar for terminal delivery and reply through `mailbox_send.py`.

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
| `anxiety` | Monitor a target worker against a validated requirement packet from the same worktree, using read-only evidence and concrete escalation. |
| `decompose` | Break large tasks into phased dev-loop iterations with clear boundaries, ordering, and inter-phase contracts. |
| `generate-persona` | Generate a behavioral persona from any data source (chat logs, PR reviews, text files, git history), guided by intended use case. Replaces `generate-reviewer-persona` and `generate-supervisor-persona`. |
| `generate-supervisor-persona` | **Deprecated** — thin wrapper that redirects to `generate-persona` with `--use supervision --source chat-logs`. |
| `install` | Set up or reinstall mecha-developer by symlinking personas, skills, and hooks to the home directory. One-time setup utility. |
| `intake` | Turn an idea into a validated requirement packet and hand it to the supervisor for target-worker dispatch and read-only anxiety monitoring. |
| `kb` | Manage the persistent markdown knowledge base at `~/.mecha-developer/kb/`. For persistent, reusable knowledge worth keeping across sessions. |
| `mailbox` | Mecha-wolfgang async mailbox — Mailbox V2 router delivery formats and mandatory pre-read state-update rules. **Load with supervisor** so runtime-delivered mail is handled correctly. |
| `pi-extension-maintenance` | Safely inspect, install, update, uninstall, and diagnose the registered checkout-source Mailbox V2 Pi extension without relocating, copying, symlinking, or restoring an installed artifact. |
| `pr-watch` | Watch assigned GitHub pull requests and run the required review workflow from the dedicated PR-review agent. |
| `reminders` | Set one-shot reminders that fire via mailbox after a delay. Set, list, or delete reminders for any tmux window. |
| `remote-cloud-desktop` | Provision a generic Ubuntu KDE Plasma X11 desktop over xrdp with access restricted to a private overlay network. |
| `roger-advanced-report-implementation` | Implement RogerEMR advanced/self-serve reports on the governed dataset, Parquet, DuckDB-WASM, and Report Builder architecture, distinct from fixed server-side reports. |
| `roger-report-implementation` | Create RogerEMR fixed reports with evidence-backed source parity, Terra planning, correct permissions/stacking, screen-export agreement, isolated synthetic demos, and report-specific review gates. |
| `subagent-orchestration` | Delegate bounded work with explicit contracts, evidence gates, context limits, safe handoffs, and stop rules while the parent keeps orchestration and final validation. |
| `supervisor` | Wolfgang's operations hub for routing tasks, managing workers, and tracking persistent state. Top-level session mode — do not invoke as a sub-skill. |
| `supervisor-review` | Supervisor readiness interrogation and quality gate before accepting worker done, blocked, parked, commit/push, draft PR, or review-ready state transitions. |
| `supervisor-sweep` | Craft and maintain the supervisor general sweep reminder so active work, open-PR feedback, CI, and merge-conflict checks are captured every time. |
| `test-coverage` | Run language-agnostic code coverage analysis and identify gaps with concrete test suggestions across supported project toolchains. |
| `unramble` | Compress verbose Claude output into concise Wolfgang voice. Use when Claude sends wall-of-text output that needs condensation before forwarding to a human. |
| `voice` | Generate a voice profile from writing samples so an LLM can reproduce how someone speaks and writes. Source-agnostic: works with Facebook exports, chat logs, email archives, plain text, or any writing samples. |
| `voice-review-builder` | Collect and distill Wolfgang's feedback on PR review comments to improve review voice and review quality. |
| `watchdog` | LLM-based Watchdog momentum-nudge guidance. Not for direct invocation. |
| `worker` | Worker agent instructions — provided by the supervisor when launching or resuming a managed worker. Provides rules, mailbox communication patterns, and the standard plan/implement/review workflow. Supervisors instruct workers to read it through the runtime's verified delivery path; do not invoke manually. |
| `worker-launch` | Managed worker launch, resume, follow-up handoff, parking, shutdown, and Mailbox V2 coordination guidance for supervisors. |
| `wolfgang-voice-review` | Private final gate for rewriting validated PR review findings into Wolfgang's review voice before posting. |
| `workflows` | Manage and execute plain-English workflow templates. Load whenever a workflow is mentioned by name — even casually — before acting on it. |
| `worktree` | Create, list, or remove git worktrees for isolated workstreams as sibling directories of the main repo. |
