# Native operational tools specification

## Goal

Pi exposes the common supervisor operations as grouped native tools. Each call names an approved action and supplies only the fields needed by that action.

## Tools

- `mecha_state`: query, create, and update task state.
- `mecha_priority`: create or update a priority and change its status.
- `mecha_verification_handoff`: record a verification handoff.
- `mecha_reminder`: list, create, and delete reminders.
- `mecha_worker`: list workers and send a worker a message.
- `mecha_pr`: read status for numeric pull request identifiers.
- `mecha_git`: read git status for supervisor tasks.
- `mecha_router`: read router health and retry or dismiss a known message.

## Safety requirements

Every tool uses action-specific closed JSON Schema variants. The extension also validates calls before invoking an adapter. Unknown actions, missing required fields, and extra properties fail without running a script.

Scripts come from a fixed filename allowlist. Callers cannot provide a command, script, repository, working directory, or filesystem path. Argument builders produce argument arrays and never invoke a shell. Pull request operations accept numeric identifiers. Git operations accept supervisor task identifiers.

Worker shutdown is not exposed. `worker_shutdown.py --verified` accepts a caller assertion, not independently established review evidence, so a native caller could self-certify the destructive action.

The runner and router adapters are injected for testing. The default runner resolves allowlisted installed scripts, calls Python with `execFile`, applies a timeout and buffer limit, and returns bounded output. Successful and failed stdout and stderr are normalized and redacted. Credential fields and values are removed without changing ordinary identifiers. Router health goes through the injected router adapter. Every result has text content and object details, and responses are capped at 16,384 characters plus a short truncation marker.

The installer copies the extension, approved Python scripts, their local Python modules, and the JavaScript files needed by sidecar cleanup into one fixed target topology. Installation fails before mutation when a declared source is missing or a relative import is outside that explicit bundle. Any partial copy or settings failure restores every previous file and registration.

## Acceptance criteria

`createMechaMessagingExtension` registers all eight tools once and accepts injected adapters through one options object. Tests prove closed schemas, runtime rejection, fixed script selection, safe argument arrays, adapter isolation, typed bounded results, credential redaction, ordinary identifier preservation, explicit bundle closure, rollback, clean-target imports, and installed execution through all eight tools.
