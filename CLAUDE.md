# mecha-developer

This repo holds dev-tooling skills for Wolfgang's AI agent workflow. Skills live in `.claude/skills/<name>/SKILL.md`.

## How skills work

A skill is a self-contained instruction set. Any agent (Claude Code, Codex, Cursor) reads `SKILL.md` and follows the instructions. Skills can reference other skills, but the file is always authoritative — never assume what a skill does, look it up.

## Maintenance

When adding or updating a skill, keep this CLAUDE.md's skill table (below) current.

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
| `pr-watch` | Run and maintain the read-only polling GitHub PR watcher used as a lightweight substitute for the webhook PR review bot. |
| `recall` | Proactive memory surfacing system — detects when stored knowledge is relevant and delivers it via mailbox before the agent needs to be told. Runs automatically via hook + cron. |
| `reminders` | Set one-shot reminders that fire via mailbox after a delay. Set, list, or delete reminders for any tmux window. |
| `supervisor` | Wolfgang's operations hub for routing tasks, managing workers, and tracking persistent state. Top-level session mode — do not invoke as a sub-skill. |
| `test-coverage` | Run language-agnostic code coverage analysis and identify gaps with concrete test suggestions across supported project toolchains. |
| `voice` | Generate a voice profile from writing samples so an LLM can reproduce how someone speaks and writes. Source-agnostic: works with Facebook exports, chat logs, email archives, plain text, or any writing samples. |
| `watchdog` | Watchdog momentum-nudge guidance for the Codex agent wrapper and legacy `watchdog.py` fallback. Not for direct invocation. |
| `worker` | Worker agent instructions — injected by the supervisor at launch. Provides rules, mailbox communication patterns, and the standard plan/implement/review workflow. Loaded automatically; do not invoke manually. |
| `workflows` | Manage and execute plain-English workflow templates. Load whenever a workflow is mentioned by name — even casually — before acting on it. |
| `worktree` | Create, list, or remove git worktrees for isolated workstreams as sibling directories of the main repo. |
