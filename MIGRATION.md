# Migrating from mecha-wolfgang to mecha-developer

This repo (`mecha-developer`) absorbed the dev-tooling skills that used to live in `mecha-wolfgang`. Code-review skills live in a third repo, `mecha-review`. This document explains the layout and how to migrate a machine from a mecha-wolfgang setup to a mecha-developer one.

## Repo layout

| Repo | Owns |
|------|------|
| `mecha-developer` | Dev-tooling skills: supervisor, worker, watchdog, mailbox, reminders, install, decompose, explain, kb, recall, voice, augment-persona, generate-persona, worktree, workflows, test-coverage, generate-supervisor-persona (deprecated wrapper). |
| `mecha-review` | Code-review skills: review-pr, robot-review, pr-context, pr-summary, pr-validate, generate-reviewer-persona. |
| `mecha-wolfgang` | Remaining skills not yet migrated: browser-console, codex-memory, migrate-legacy-test. Also still hosts `MAILBOX_REQUIREMENTS.md` and the `personas/` directory consumed by the install skill. |

## Data home

Persistent runtime state (mailboxes, supervisor state, logs, kb, recall config, reminders, workflows) lives at:

```
~/.mecha-developer/
```

This used to be `~/.mecha-wolfgang/`. All skill code in this repo now references `.mecha-developer`. The ntfy notification topic is still `mecha-wolfgang-notify` for stability — phone subscribers don't have to resubscribe. If you want to rename the topic, change `NTFY_TOPIC` in `.claude/skills/supervisor/tools/watchdog.py` and resubscribe.

## One-time machine migration

If you are upgrading a machine that previously ran mecha-wolfgang, do these steps in order. **The supervisor process must be stopped before steps 2–4 because they mutate live state.**

### 0. Stop the supervisor and any active workers

The supervisor reads/writes `~/.mecha-wolfgang/supervisor-state.json` continuously and follows `~/.claude/skills/supervisor/tools/*.py`. Renaming the data home or flipping symlinks under a running supervisor produces a broken hybrid state. Bring everything down first:

- Exit the supervisor Claude session
- Stop any active worker windows (or accept that they will be re-launched against the new paths)
- Verify with `ls -la ~/.claude/skills/` and `tmux ls`

### 1. Clone the new repos

```bash
cd ~/code
git clone git@github.com:wolfgangmeyers/mecha-developer.git
git clone git@github.com:wolfgangmeyers/mecha-review.git   # if not already present
```

### 2. Rename the data home

```bash
mv ~/.mecha-wolfgang ~/.mecha-developer
```

If you want zero-downtime for any process that still references the old path, leave a compat symlink instead:

```bash
mv ~/.mecha-wolfgang ~/.mecha-developer
ln -s ~/.mecha-developer ~/.mecha-wolfgang
```

The mecha-developer skills do not require the compat symlink. The three skills still living in mecha-wolfgang (browser-console, codex-memory, migrate-legacy-test) do not reference the data home at all, so they are unaffected by this rename.

### 3. Flip the `~/.claude/skills/` symlinks

The 17 skills migrated to mecha-developer need their per-skill symlinks repointed. For each migrated skill:

```bash
SKILLS="supervisor worker watchdog mailbox reminders install decompose explain kb recall voice augment-persona generate-persona worktree workflows test-coverage generate-supervisor-persona"
for s in $SKILLS; do
  rm -f ~/.claude/skills/$s
  ln -s "$HOME/code/mecha-developer/.claude/skills/$s" ~/.claude/skills/$s
done
```

Verify:

```bash
ls -l ~/.claude/skills/ | grep mecha-developer | wc -l   # should be 17
```

Leave the symlinks for `browser-console`, `codex-memory`, `migrate-legacy-test` pointing at mecha-wolfgang. Review skills (`review-pr`, etc.) should already be pointing at mecha-review.

### 4. Migrate the crontab

The crontab still references `code/mecha-wolfgang/...` paths and `~/.mecha-wolfgang/...` log destinations. Generate a renamed copy, diff, and install:

```bash
crontab -l > /tmp/crontab.before
sed 's|code/mecha-wolfgang|code/mecha-developer|g; s|\.mecha-wolfgang|.mecha-developer|g' /tmp/crontab.before > /tmp/crontab.after
diff -u /tmp/crontab.before /tmp/crontab.after        # review
crontab /tmp/crontab.after                             # install
crontab -l | grep -E "mecha-(wolfgang|developer)"      # verify
```

No legitimate residual `mecha-wolfgang` cron entries should remain, assuming you have no custom cron jobs for browser-console/codex-memory/migrate-legacy-test (none of those skills install cron jobs).

### 5. Re-run install (optional, but recommended)

The install skill is idempotent. From `~/code/mecha-developer`:

```bash
cd ~/code/mecha-developer
# Read .claude/skills/install/SKILL.md and follow the steps.
```

This re-creates symlinks, hooks, and cron entries from the new repo root, catching anything step 3 or 4 missed.

### 6. Restart the supervisor and spot-check

- Start a fresh supervisor session in `~/code/mecha-developer`
- `python3 ~/.claude/skills/supervisor/tools/cluster_status.py` runs cleanly
- `ls ~/.mecha-developer/` shows your mailboxes, supervisor-state.json, logs
- A test mail through `mailbox_send.py` lands and is delivered
- A scheduled reminder fires
- The watchdog log shows new entries after 30 minutes

## What does NOT move

- The `personas/` directory and `MAILBOX_REQUIREMENTS.md` still live in `mecha-wolfgang`. The install skill symlinks personas from there. If/when those move, update install accordingly.
- The ntfy topic `mecha-wolfgang-notify` is unchanged. It is an external identifier, not a path.
- `mecha-review` is not touched by this migration. The lone doc reference to `~/.mecha-wolfgang/kb/` in `mecha-review/.claude/skills/robot-review/SKILL.md` is stale-but-harmless; fix in a follow-up PR against mecha-review if you care.

## Reverting

If you need to roll back to a mecha-wolfgang setup:

```bash
mv ~/.mecha-developer ~/.mecha-wolfgang
# Re-point ~/.claude/skills/<each> symlinks at mecha-wolfgang/.claude/skills/<same>
# Restore the previous crontab from /tmp/crontab.before (if still present)
```

No data lives only in this repo — everything stateful is in the data home, which the rename preserves byte-for-byte.
