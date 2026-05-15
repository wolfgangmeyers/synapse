# mecha-developer

Personal dev-tooling skills for AI coding agents — supervisor, worker, watchdog, mailbox, reminders, install, decompose, explain, kb, recall, voice, augment-persona, generate-persona, worktree, workflows, test-coverage.

Companion repos:
- `wolfgangmeyers/mecha-review` — PR review skills (review-pr, robot-review, pr-summary, pr-validate, pr-context, generate-reviewer-persona)
- `wolfgangmeyers/mecha-wolfgang` — Wolfgang's primary supervisor checkout and persona files (legacy source-of-truth for some skills, being migrated)

## Structure

Skills live under `.claude/skills/<name>/SKILL.md` with optional `tools/` and supporting files. Each skill is a self-contained instruction set any agent (Claude Code, Codex, Cursor) can invoke by reading the file.

## Status

This repo is being populated by migrating non-review skills out of `mecha-wolfgang`. No content has been migrated yet — only the skeleton.
