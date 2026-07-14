# mecha-developer

Personal dev-tooling skills for AI coding agents — supervisor, worker, watchdog, mailbox, reminders, install, decompose, kb, voice, augment-persona, generate-persona, worktree, workflows, test-coverage.

Companion repos:
- `wolfgangmeyers/mecha-review` — self-contained PR review and review-adjacent skills (review-pr, robot-review, pr-summary, pr-validate, pr-context, explain)

## Structure

Skills live under `.claude/skills/<name>/SKILL.md` with optional `tools/` and supporting files. Each skill is a self-contained instruction set any agent (Claude Code, Codex, Cursor) can invoke by reading the file.

## Status

This repo is the home for non-review developer workflow skills.
