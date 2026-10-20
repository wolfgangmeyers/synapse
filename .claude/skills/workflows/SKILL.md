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

## Git Sync & Remote Backing (GitHub Sync)

The router provides Git-backed persistence and synchronization for workflows and prompt templates via the `/v1/git-sync/{target}` API (`workflows` and `prompt_templates`).

### Linking a Repository URL
- Workflows can be linked to a remote Git repository (e.g. GitHub) via `PUT /v1/git-sync/workflows` with JSON payload `{"repo_url": "<url>"}` or configured directly through the Web UI Workflows page.
- Supported URL schemes: HTTPS (`https://github.com/owner/repo.git`), SSH (`ssh://git@github.com/owner/repo.git`), and SCP-style SSH (`git@github.com:owner/repo.git`). Unsafe git protocol helpers (such as `ext::`) or URLs beginning with `-` are rejected.
- To check the linked repository target, issue `GET /v1/git-sync/workflows`. To unlink, send `DELETE /v1/git-sync/workflows`.

### Pushing and Pulling Behavior
- **Target Files**:
  - `workflows`: Synchronizes to/from `workflows.json` at the root of the remote repository.
  - `prompt_templates`: Synchronizes to/from `prompt-templates.json` at the root of the remote repository.
- **Push (`POST /v1/git-sync/workflows/push`)**:
  - Serializes all workflows and states from the router database into portable JSON bundle format (`mecha-workflows`, version 1).
  - Writes to `workflows.json` in the cached local git clone, commits ("Update workflows.json from app"), and pushes to `origin/main`.
- **Pull (`POST /v1/git-sync/workflows/pull`)**:
  - Fetches the latest commits from `origin/main` and resets to `origin/main`.
  - Reads `workflows.json`, validates schema, format, and individual workflow bundles.
  - Atomically replaces all workflows and states in the router database.

### Conflict-Free Overwrite Policy on `main`
- The Git backing engine operates strictly on the `main` branch.
- Before reading or writing, the syncer performs `git fetch origin main`, `git checkout main`, and `git reset --hard origin/main`.
- This ensures a conflict-free overwrite model where the latest remote state on `main` is authoritative during pull, and local changes cleanly apply on top of the tip of `main` during push.

### Host Shell Git Auth & Identity
- Synchronization uses the host machine's system `git` executable (`exec.CommandContext("git", ...)`).
- All network authentication and credentials rely on the host environment:
  - SSH keys configured in `~/.ssh` or running `ssh-agent` for `git@` and `ssh://` URLs.
  - Credential helpers (`git-credential-manager`, `gh auth`, `.git-credentials`, etc.) for `https://` URLs.
  - User identity (`user.name` and `user.email`) configured in the host's global or local `gitconfig`.
- Clones are maintained in an isolated directory at `~/.mecha/git-sync/<hash>` with mode `0700`.

### Missing-File Safety Behavior
- When pulling: If `workflows.json` (or `prompt-templates.json`) does not exist in the remote repository or branch, the operation fails safely without corrupting or deleting existing local router database records.
- Pull returns `{ "exists": false, "message": "No workflows.json found in repository" }` (or `"File not found"` for prompt templates) with HTTP status 200, leaving existing stored records intact.
- If the file exists but has invalid JSON or invalid schema format/version, validation errors are returned without altering the database.

