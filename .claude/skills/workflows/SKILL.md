---
name: workflows
description: "Manage and execute plain-English workflow templates. Trigger: ANY mention of a workflow by name or concept (e.g. 'hybrid workflow', 'dev-loop', 'run the X workflow', 'what workflows exist', 'follow the workflow'). Load this skill proactively whenever the user references a workflow — even casually — so you can look up its definition before acting."
---

# Workflows Skill

## Overview

Workflows are plain-English templates that define repeatable procedures. This skill stores them as named workflow records and helps agents read, update, and execute them step by step.

## Trigger Conditions

Use this skill when the user asks to:
- Run a workflow by name (for example, "run workflow X" or "execute the X workflow")
- List or show available workflows
- Create a new workflow
- Update, refine, or edit an existing workflow
- Delete or clean up workflows

## Scripts Reference

All scripts live in `.claude/skills/workflows/tools/`.
Run these commands from the mecha-developer repository root.

- `python3 .claude/skills/workflows/tools/list_workflows.py`
  - Lists all workflows as `<name> <description>` (one per line)
  - Prints `No workflows defined yet.` when none exist

- `python3 .claude/skills/workflows/tools/get_workflow.py <name>`
  - Shows full workflow details (name, description, updated timestamp, body)
  - Exits with code 1 if the workflow is not found

- `python3 .claude/skills/workflows/tools/save_workflow.py --name <name> --description "<text>" --body "<text>"`
  - Creates or updates a workflow by name
  - Preserves `created_at` on updates, always refreshes `updated_at`
- `python3 .claude/skills/workflows/tools/save_workflow.py --name <name> --description "<text>" --body-file <path>`
  - Creates or updates a workflow by name
  - Preserves `created_at` on updates, always refreshes `updated_at`
  - Prefer `--body-file` for multi-line bodies and special characters

- `python3 .claude/skills/workflows/tools/cleanup_workflows.py [--name <name> ... | --all] [--confirmed]`
  - Deletes selected workflows by names, or all workflows with `--all`
  - Without `--confirmed`, performs a dry run and prints what would be deleted

## Executing A Workflow

When asked to run a workflow:
1. Run `python3 .claude/skills/workflows/tools/get_workflow.py <name>` to retrieve it.
2. Read the workflow body carefully before executing.
3. Execute each step in sequence using the appropriate tools and commands.
4. Report exactly what was done, and call out any errors, blockers, or deviations.

## Delegating A Workflow To A Worker

When handing work to a worker that is supposed to follow a workflow, do **not** paste or summarize the workflow body into the worker instructions. Embedded copies drift and are less accurate than the canonical workflow record.

Instead, the worker instruction must explicitly tell the worker to:
1. Load the `workflows` skill.
2. Use the workflows skill to load the named workflow with `get_workflow.py <name>`.
3. Follow that loaded workflow as the source of truth for the task.
4. Report any deviation, missing workflow, or unclear workflow step before proceeding past it.

The supervisor may include the task goal, scope, constraints, worktree, base branch, and non-goals in the handoff, but the procedural workflow itself must be loaded by the worker from the canonical workflows skill.

## Creating And Updating Workflows

When writing or refining workflow bodies:
- Be specific and actionable; every step must be unambiguous.
- Include exact commands, file paths, and decision points where relevant.
- Keep steps atomic (one action per step).
- Write for an agent executor, not for a human reader; precision beats prose.

## Cleanup Note

`cleanup_workflows.py` requires an explicit `--confirmed` flag to actually delete data. User confirmation is required before deletion.
