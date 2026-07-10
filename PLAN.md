# Phase 1 Plan

This plan implements the revised `SPEC.md` only after its explicit supervisor go/no-go approval. Until then, source inspection and gate-artifact revision are the only permitted work.

## 1. Isolated live Pi harness

Create a temporary-root external-process harness that invokes the installed Pi 0.80.6 binary with:

- a disposable extension loaded by `--extension`;
- a controllable local fake model/provider so idle and busy agent states can be held deterministically without external credentials;
- temporary `PI_CODING_AGENT_DIR`, `--session-dir`, evidence path, and test-only extension configuration;
- no installed extension/configuration, tmux, LaunchAgent, mailbox, or reminder interaction.

The probe injected a stable `custom_message` with the router ID in content and `details`, recorded `message_end` correlation and `ctx.sessionManager.getEntries()` lookup, then deterministically crashed before the first assistant entry. A replacement process could not find the ID because no session file existed. The candidate acknowledgement rule is rejected; the harness preserves this as the hard no-go regression.

## 2. Live acceptance matrix

The suite now covers idle and busy in real isolated PTY/TUI processes using the documented `ctx.ui.getEditorText()` observation; the busy routed ID is absent from the first provider context and present at the next boundary. A separate real isolated RPC process covers reload, new, resume, fork, and every shutdown reason while proving test-resource cleanup. Source/declaration checks only pin expected documented types and implementation ordering; they are not acceptance substitutes.

## 3. Protocol fixtures and validation

After evaluating the router stack, add `protocol/v1/schema.json`, valid examples, and rejected examples for the exact envelope, frames, errors, and boundary cases listed in `SPEC.md`. Select a maintained JSON Schema validator compatible with the selected stack; do not write a custom validator. Tests run the selected validator against every fixture and assert the stated valid/rejected result.

## 4. Router-stack decision

Inspect repository manifest(s), Node 26 runtime capability, Python runtime packages, and maintained dependency footprints. Compare Node and Python against the criteria in `SPEC.md`, including a maintained WebSocket server implementation and a maintained JSON Schema validator. Document the evidence, selection, rejected alternative, exact dependency versions, and why no handwritten framing is involved. Do not select a stack based merely on the TypeScript probe.

## 5. Documentation and gates

Add the short decision log with source version/hashes, live evidence results, router-stack decision, and unresolved risks. Update `harness_spec.md` only where live evidence requires correction. Run targeted tests, complete relevant tests, static checks, `git diff --check`, coverage analysis, and robot review. Report exact commands/results, changed files, proven and unproven guarantees, and an explicit next-phase recommendation. Do not begin Phase 2, commit, push, or modify active system state.

## Current gate recommendation

**Hard no-go.** The isolated live crash/restart test proves that the candidate event-plus-history check does not survive the required persistence boundary. Phase 2 cannot begin until a different documented acknowledgement boundary is proposed and proven.
