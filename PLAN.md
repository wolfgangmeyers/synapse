# Goal Extension Loading Plan

Base: `wolfgang/managed-worker-goals`, approved `SPEC.md` SHA-256 `7029885278a4509157e9c30db3d5038fa4e5046ded0b69c8649c9db7a1c9350c`.

## 1. TDD: write the failing contract first

Update `.claude/skills/supervisor/tools/test_worker_launch.py` only:

- Parse the new-session and resumed Pi command argv with `shlex.split`, and make the first failing assertions cover the exact Goal path expression `os.path.realpath(os.path.join(REPOSITORY_ROOT, "pi-extensions", "mecha-goal", "index.ts"))`, its absolute/realpath result, and the Goal package-discovery entry.
- For both commands, assert exactly one `--no-extensions`, exactly three unique explicit extensions, and this exact order: Goal, pi-subagents, mecha-messaging.
- For both commands, assert the exact nonsecret router/Mailbox exports (`MECHA_ROUTER_URL` equals the configured loopback URL and `MECHA_PI_MODEL_KEY` equals the selected provider/model), `--approve`, and mutually exclusive selectors: new has `--session-id` and no `--session`; resume has `--session` and no `--session-id`.
- Include Goal in the package-discovery duplicate/entrypoint coverage, including the resolved path rather than only a basename.
- Add a missing-Goal pre-tmux test that patches the Goal path absent and proves all launch side effects remain untouched: `window_exists`, `ensure_session`, `new_window`, `send_text`, and `send_enter`. Keep the existing missing-entrypoint coverage.

Run the focused launcher test once at this point and record the expected failure. Do not change production code before that failure is observed.

## 2. Smallest implementation

Update `.claude/skills/supervisor/tools/worker_launch.py` only:

- Derive the Goal path once from the existing canonical `REPOSITORY_ROOT` using `os.path.realpath`.
- Make `resolve_pi_extensions()` return one ordered tuple containing Goal, pi-subagents, and mecha-messaging exactly once, with the existing preflight existence check.
- Leave new/resume command construction, provider/model, router/Mailbox, permission handling, and delegation paths unchanged.

## 3. Focused and full validation

- Rerun `.claude/skills/supervisor/tools/test_worker_launch.py` and the directly affected relaunch, recovery, and lifecycle test modules.
- Run the repository’s full supervisor-tool Python test suite with its existing test discovery command.
- Run `test-coverage` against the changed launcher/test seam and close any contract gap that maps to the approved SPEC.
- Prove the missing-Goal test fails if its pre-tmux assertions are removed, then retain the minimal assertion set.

No external services, tmux provisioning, or deployed runtime changes are needed.

## 4. Review

Run implementation `robot-review` from the full focus set: backend/runtime behavior (applicable because this is launcher/runtime code), correctness, security, data, operations, tests, documentation/comments, maintainability, and visual UI. Record evidence-backed N/A reasons for data persistence and visual UI only: this change neither stores/transforms data nor changes a UI. Run `pr-validate` with a different persona, then perform a fresh follow-up review until reviewers explicitly sign off. Remove any change that does not map to the approved SPEC.

## 5. Delivery and proof

Commit the scoped change with `Wolfgang Meyers <wolfgang.meyers@identity.digital>`, push `wolfgang/managed-worker-goals`, and report the commit plus validation evidence to the supervisor. Before and after delivery, prove `/home/ubuntu/code/mecha-developer` has the same pre-existing status and no mutation from this task. Leave no unrelated files in the worker commit.
