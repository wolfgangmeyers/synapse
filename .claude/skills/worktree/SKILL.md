---
name: worktree
description: "Create, list, or remove git worktrees for isolated workstreams as sibling directories of the main repo. Trigger: user asks to create a worktree, work on a feature in isolation, or clean up a worktree. Do NOT use for standard git operations that don't need worktree isolation."
---

# Worktree Skill

Manage git worktrees for isolated workstreams. Worktrees are created as **sibling directories** of the main repo, not subdirectories.

## Usage

```
/worktree create <repo-path> <branch-description> [alias]
/worktree remove <repo-path> <alias>
/worktree list <repo-path>
```

## Conventions

- **Branch naming**: `wolfgang/<description>` (e.g., `wolfgang/fix-login-timeout`)
- **Directory placement**: Sibling of the main repo. If the repo is at `~/code/<repo>`, a worktree with alias `login` lives at `~/code/<repo>-login`.
- **Alias**: Short name for the worktree. Used as both the directory suffix and the `wt` navigation target. If not provided, derive from the branch description (last segment, abbreviated).

## Create

```bash
# From the main repo directory:
cd <repo-path>

# Determine sibling path
REPO_DIR=$(basename "$(pwd)")
PARENT_DIR=$(dirname "$(pwd)")
WORKTREE_PATH="$PARENT_DIR/$REPO_DIR-<alias>"

# Create worktree with new branch
git worktree add -b wolfgang/<branch-description> "$WORKTREE_PATH"
```

The worktree directory is now at `$WORKTREE_PATH`. Workers or humans can `cd` into it or use `wt <alias>` to navigate there.

## Copy gitignored dev secrets into the worktree

`git worktree add` does NOT copy gitignored files (`.env.development.local`, `google-credentials.json`, similar). Without these, the dev env in the worktree boots with a bootstrap script's freshly-generated secrets — which silently drift from sibling worktrees and the base repo. Cross-service paths that rely on paired secrets (webhook HMAC, shared API keys) fail with no log line.

**Always run this immediately after `git worktree add`**, before launching any worker or dev env:

```bash
# Copy every gitignored dev secret + config from the base repo into the worktree
cd "$WORKTREE_PATH"
for f in .env.development.local google-credentials.json .vscode; do
  [ -e "../$REPO_DIR/$f" ] && cp -r "../$REPO_DIR/$f" .
done
```

This is the generic replacement for `~/bin/winit` (which only handled `glue-api`). It works for any repo because it copies whichever of the listed files exist in the base. Add new filenames here as new repos surface them.

**Verification**: after copying, `ls .env.development.local` (or whichever files the repo needs) must succeed. If a required file doesn't exist in the base repo either, that's an upstream setup gap — escalate before launching the worker.

**Paired-secret integrity**: for cross-service features (glue-api ↔ glue-agent, glue-api ↔ glue-web), confirm the paired secret values match across worktrees of both repos. Mismatches are the #1 silent-failure source in dev environments.

## Remove

```bash
cd <repo-path>
REPO_DIR=$(basename "$(pwd)")
PARENT_DIR=$(dirname "$(pwd)")

git worktree remove "$PARENT_DIR/$REPO_DIR-<alias>"
```

Use `--force` only if the worktree has uncommitted changes and the user explicitly confirms.

Before removing any dirty worktree, classify every tracked and untracked source/artifact change:

- **committed/merged** into the destination branch,
- **intentionally abandoned** because it is obsolete or superseded, or
- **still needed** and therefore must be committed, copied into the canonical checkout, or tracked as a follow-up task before cleanup.

Do not use `git stash` as a substitute for this classification. A stash is only a temporary recovery archive; keep a state entry/reminder until the stash has been applied, committed, or explicitly abandoned.

## List

```bash
cd <repo-path>
git worktree list
```

## Supervisor Integration

When the supervisor needs to launch a worker for a task on a specific repo:

1. **Create a worktree** for the task so the worker has an isolated checkout:
   ```bash
   cd <repo-path>
   REPO_DIR=$(basename "$(pwd)")
   PARENT_DIR=$(dirname "$(pwd)")
   ALIAS="<short-task-alias>"
   WORKTREE_PATH="$PARENT_DIR/$REPO_DIR-$ALIAS"

   git worktree add -b "wolfgang/$ALIAS" "$WORKTREE_PATH"
   ```

2. **Copy gitignored dev secrets** into the new worktree before launching anything (see "Copy gitignored dev secrets into the worktree" above):
   ```bash
   cd "$WORKTREE_PATH"
   for f in .env.development.local google-credentials.json .vscode; do
     [ -e "../$REPO_DIR/$f" ] && cp -r "../$REPO_DIR/$f" .
   done
   ```

3. **Launch the worker** in the worktree directory (not the main repo):
   ```bash
   tmux new-window -t mecha -n "worker-N"
   tmux send-keys -t mecha:worker-N "cd $WORKTREE_PATH && claude" Enter
   ```

3. **Clean up** after the worker finishes and changes are merged, and only after the dirty worktree cleanup gate above is satisfied:
   ```bash
   cd <repo-path>
   git worktree remove "$WORKTREE_PATH"
   ```

This keeps the main repo checkout clean and lets multiple workers operate on the same repo simultaneously without conflicts.

## Why Sibling Directories

- Subfolders pollute the repo's directory listing and can accidentally get staged
- Sibling directories are invisible to the main repo's git
- `wt` navigation works the same either way
- Multiple worktrees for the same repo are visually grouped in the parent directory (e.g., `<repo>/`, `<repo>-login/`, `<repo>-dark-mode/`)
