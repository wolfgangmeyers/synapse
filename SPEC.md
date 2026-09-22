# Goal Extension Loading Specification

## Behavior

Every managed Pi worker launch and resume command must start Pi with the existing `--no-extensions` flag and one canonical ordered extension list, containing each of these exactly once:

1. `mecha-goal`
2. the canonical `pi-subagents` entrypoint
3. the canonical `mecha-messaging` entrypoint

The Goal entrypoint is resolved in `.claude/skills/supervisor/tools/worker_launch.py` from its existing `REPOSITORY_ROOT` constant with `os.path.realpath(os.path.join(REPOSITORY_ROOT, "pi-extensions", "mecha-goal", "index.ts"))`; it must not use a machine-specific absolute checkout path. When the canonical launcher runs from the primary checkout, this resolves to `/home/ubuntu/code/mecha-developer/pi-extensions/mecha-goal/index.ts`; when tests run from this isolated worktree, the same expression resolves to its checked-in `pi-extensions/mecha-goal/index.ts`. The same argv contract applies to a new session and to a resumed session. The command must preserve the existing provider, model, session, router, Mailbox, and permission behavior, and must fail before tmux side effects when any required extension is missing.

## High-level design

The existing Python launcher owns the managed Pi argv. Define the ordered extension list exactly once in `resolve_pi_extensions()`, deriving Goal from `REPOSITORY_ROOT` through `realpath` and retaining the existing canonical pi-subagents and mecha-messaging paths. Keep validation in that function so new and resumed commands share the same list construction and no second command builder is introduced. Preserve worktree portability by deriving the Goal path from the executing repository root rather than embedding a checkout path.

The focused launcher tests must parse both the new-session and resumed-session argv and assert, for each: `--no-extensions`; exactly three explicit extensions in the required order with no duplicates; the existing provider and model; configured router/Mailbox environment; the permission flag; and the correct new-session (`--session-id`) versus resume (`--session`) flag. The package-discovery duplicate test must include Goal. Add a missing-Goal pre-tmux test proving an absent Goal entrypoint fails before `window_exists`, provisioning, or command submission, while retaining coverage for missing existing entrypoints.

## Alternatives considered

- **Derive Goal from `REPOSITORY_ROOT` and add it to the existing extension list.** Chosen. It avoids machine-specific paths, changes one source of truth, and automatically covers both new and resumed commands.
- **Build separate extension lists for new and resumed workers.** Rejected. The launcher already builds both forms through one Pi branch, so a second list could drift and violate parity.
- **Enable extension discovery or modify Pi settings.** Rejected. Discovery would weaken the explicit allowlist and add behavior outside this ticket. The accepted contract requires `--no-extensions` plus explicit paths.

## Scope fuse

Observable outcome: the canonical launcher explicitly loads Goal for every managed Pi launch/resume command, before pi-subagents and mecha-messaging, with focused tests proving the complete argv contract and pre-tmux missing-Goal failure.

Non-goals: extension discovery, runtime-config schema, hot reload, supervisor restart, task2298 files, router/UI changes, package or dependency changes, global deployed skill copies, unrelated refactors, and unrelated documentation.
