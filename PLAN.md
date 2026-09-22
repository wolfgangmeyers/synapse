# Plan

Implement `SPEC.md` with tests first. Scope is the managed-agent browser projection and its UI-live compatibility boundary. Do not touch archive storage, archive routes, router lifecycle, router runtime, or the dirty avatar/UI work.

## Current precedents

- `managed.PublicAgent` and `managed.PublicArchive` in `router-proxy/internal/managed/projection.go` are the existing public-shape boundaries.
- `ProjectionListEnvelope` currently appends both shapes.
- `uiLiveProjectionFromStored`, `uiLiveTransition`, and `uiLiveRecordKey` in `router-proxy/internal/transport/ui_live.go` currently retain both shapes and emit `record_key`.
- `isUILiveFrame` in `web/router-ui-react/src/protocol/guards.ts` is the strict frame guard.
- `createHttpClient().managedAgents()` in `web/router-ui-react/src/transport/http-client.ts` and `createUILiveClient` in `web/router-ui-react/src/transport/ui-live-client.ts` are the compatibility boundaries.
- `useRouterSession` dispatches managed frames; `reducer` currently fences versions but identifies rows by `agent_id`.
- Existing supervisor-authored edits in `http-client.ts`, `guards.ts`, and `ui-live-client.ts` are in scope. Keep the narrow archive filtering and optional bounded legacy `record_key` acceptance if the tests below prove them; adjust only where required by the live-only contract.

## Tests first

Add or update failing tests before production changes.

### Go projection and UI-live tests

Files:

- `router-proxy/internal/managed/projection_test.go`
- `router-proxy/internal/transport/ui_live_test.go`
- `router-proxy/internal/transport/managed_http_test.go` (list response and archive/detail preservation)

Cover:

- mixed, live-only, archive-only, and empty stored snapshots;
- `ProjectionListEnvelope` returning every live `PublicAgent` and no archive-shaped row;
- archive detail and persistence behavior remaining available through existing routes;
- UI-live snapshots containing every live row and no archive row;
- one adjacent live row surviving an archive-shaped stored row;
- one live delta and one live removal using `agent_id` identity, with exact documented key sets and no `record_key`;
- `changes.agent_id` matching the frame `agent_id`;
- version gaps, batched changes, stale versions, and ambiguous transitions falling back to snapshots;
- removing an agent absent from the current live list under an allowed adjacent version fence, asserting that the version advances while every other agent and child remains; stale, gapped, and regressed absent-agent removals leave the state unchanged;
- removing one of two live rows preserving the other row and its children;
- a reused ID across a live row and an archive affecting only the live projection.

### Web transport and state tests

Files:

- `web/router-ui-react/tests/transport/http-contract.test.ts`
- `web/router-ui-react/tests/transport/ui-live.test.ts`
- `web/router-ui-react/tests/unit/protocol-guards.test.ts`
- `web/router-ui-react/tests/unit/managed-archive.test.tsx`
- `web/router-ui-react/tests/unit/projections.test.ts`
- `web/router-ui-react/tests/component/app-fencing.test.tsx` if the mixed HTTP/UI-live path needs an end-to-end reducer assertion

Cover:

- mixed HTTP responses retaining all valid live rows while filtering recognized archive rows;
- empty HTTP lists preserving `generated_at` and `projection_version`;
- mixed UI-live snapshots retaining all valid live rows while filtering recognized archive rows;
- empty UI-live snapshots preserving `projection_version`;
- new delta/removal frames without `record_key`;
- known legacy delta/removal frames with a valid bounded string `record_key`;
- rejection of unknown keys, invalid `record_key`, invalid versions, invalid agent IDs, malformed changes, archive-shaped delta changes, and other existing schema violations;
- reducer version fencing and removal of exactly the target live row, including preservation of another live row and its child projection;
- absent-agent removal under an allowed adjacent version fence advancing the version while preserving all other agents and children, with stale, gapped, and regressed variants leaving state unchanged;
- live/archive ID reuse without archive state being overwritten or removed.

Existing regression coverage: `TestManagedProjectionIsDirectAndKeepsItsVersion` already covers the live-detail projection staying direct and preserving its version; cite it instead of adding another live-detail test.

Mutation-check the new assertions by removing each key assertion and confirming its test fails, then restore only that assertion.

## Production changes after tests are red

1. `router-proxy/internal/managed/projection.go`
   - Make `ProjectionListEnvelope` live-only by projecting `snapshot.Agents` through `PublicAgent`.
   - Leave `PublicArchive`, archive persistence, and archive/detail projection functions unchanged.

2. `router-proxy/internal/transport/ui_live.go`
   - Make `uiLiveProjectionFromStored` consume only `stored.Agents`.
   - Keep live identity keyed by live agent ID.
   - Update `uiLiveTransition` to emit only `type`, `protocol`, `projection_version`, `base_version`, `agent_id`, and (for deltas) complete `changes`.
   - Preserve the adjacent-version single-change optimization and snapshot recovery for gaps, batches, stale versions, and ambiguity.

3. `web/router-ui-react/src/protocol/types.ts`
   - Keep the public TypeScript frame types on the documented contract without `record_key`.
   - Accept the legacy field only through the runtime guard boundary, not in the exposed new frame types.

4. `web/router-ui-react/src/protocol/guards.ts`
   - Retain the supervisor-authored narrow optional legacy `record_key` allowance for the two lifecycle frame types only.
   - Keep exact-key rejection and all existing value validation.

5. `web/router-ui-react/src/transport/http-client.ts`
   - Retain the supervisor-authored boundary filter for recognized `record_kind: "archived"` records.
   - Validate every remaining row as `ManagedAgent`; preserve rejection of malformed or unknown-field live rows.

6. `web/router-ui-react/src/transport/ui-live-client.ts`
   - Retain the supervisor-authored snapshot-only archive filter before `isUILiveFrame`.
   - Keep all other frame validation and recovery behavior unchanged.

7. `web/router-ui-react/src/state/reducer.ts` and, only if tests require it, `web/router-ui-react/src/hooks/use-router-session.ts` / `web/router-ui-react/src/state/agent-projection.ts`
   - Apply the documented live-only frames by live `agent_id`.
   - Preserve projection-version fencing, recovery, goal state, and child-state handling.
   - Do not add archive identity to the live reducer; archives are excluded at the projection boundary.

## Acceptance mapping

| Acceptance criterion | Production files and symbols | Tests | Commands |
|---|---|---|---|
| Browser lists and UI-live projections contain live agents only | `projection.go: ProjectionListEnvelope`; `ui_live.go: uiLiveProjectionFromStored`, `uiLiveProjection.frame`; `http-client.ts: managedAgents`; `ui-live-client.ts: withoutArchivedSnapshotAgents` | Go projection/UI-live tests; `http-contract.test.ts`; `ui-live.test.ts`; `managed-archive.test.tsx` | `cd router-proxy && go test ./internal/managed ./internal/transport`; `npm --prefix web/router-ui-react run test -- tests/transport/http-contract.test.ts tests/transport/ui-live.test.ts tests/unit/managed-archive.test.tsx` |
| Archive persistence and archive/detail routes are unchanged | `projection.go: PublicArchive`; existing archive handlers in `managed_http.go` | `managed_http_test.go`; existing archive persistence tests | `cd router-proxy && go test ./internal/managed ./internal/transport -run 'Archive|ManagedHttp'` |
| Delta/removal frames omit `record_key` and use documented keys | `ui_live.go: uiLiveTransition` | `ui_live_test.go`; `ui-live.test.ts`; protocol guard tests | `cd router-proxy && go test ./internal/transport -run 'UILive'`; `npm --prefix web/router-ui-react run test -- tests/transport/ui-live.test.ts tests/unit/protocol-guards.test.ts` |
| Vite accepts new and known legacy lifecycle frames | `guards.ts: isUILiveFrame`; `types.ts` | `protocol-guards.test.ts`; `ui-live.test.ts`; `managed-archive.test.tsx` | `npm --prefix web/router-ui-react run test -- tests/unit/protocol-guards.test.ts tests/transport/ui-live.test.ts tests/unit/managed-archive.test.tsx` |
| Vite rejects unknown fields and invalid legacy values | `guards.ts: isUILiveFrame` | `protocol-guards.test.ts` | same focused guard command above |
| Mixed snapshots retain every valid live row and exclude archives | `projection.go`; `ui_live.go`; `http-client.ts`; `ui-live-client.ts` | Go mixed snapshot tests; HTTP/UI-live/archive tests | Go and web focused commands above |
| Removing one agent cannot clear or replace unrelated rows | `ui_live.go: uiLiveProjectionFromStored`, `uiLiveTransition`; `reducer.ts` managed actions | `ui_live_test.go`; `managed-archive.test.tsx`; `projections.test.ts`; app-fencing test if changed | `cd router-proxy && go test ./internal/transport -run 'UILive'`; `npm --prefix web/router-ui-react run test -- tests/unit/managed-archive.test.tsx tests/unit/projections.test.ts tests/component/app-fencing.test.tsx` |
| No restart, push, staging, or commit in this phase | This plan only; no production/test edits now | Git status readback | No router command; no push, stage, or commit |
| This phase creates only the planning artifact and preserves the existing `SPEC.md` and unrelated dirty files | `PLAN.md` only; no production or test file changes | `git status --short` with the pre-existing dirty paths preserved | No test or router command; read-only status check only |

## Validation and coverage

After implementation, run:

```sh
cd router-proxy
gofmt -w internal/managed/projection.go internal/transport/ui_live.go
go test ./internal/managed ./internal/transport
go test ./internal/managed ./internal/transport -coverprofile=/tmp/managed-projection.cover
go tool cover -func=/tmp/managed-projection.cover

cd ../web/router-ui-react
npm run typecheck
npm run lint
npm run test -- tests/transport/http-contract.test.ts tests/transport/ui-live.test.ts tests/unit/protocol-guards.test.ts tests/unit/managed-archive.test.tsx tests/unit/projections.test.ts tests/component/app-fencing.test.tsx
npm run test:coverage -- tests/transport/http-contract.test.ts tests/transport/ui-live.test.ts tests/unit/protocol-guards.test.ts tests/unit/managed-archive.test.tsx tests/unit/projections.test.ts
```

Run the canonical `dev-loop` phases after this plan: plan review, TDD implementation, focused integration proof, coverage, and implementation review. Do not restart the router, push, stage, or commit during this specification/plan phase. Preserve all pre-existing dirty files, including avatar/UI changes and unrelated managed files.

# Goal Extension Loading Plan

Base: `origin/main` at `5d7559deb023a61155940c5e57409588b276e9e2`. Target: `wolfgang/managed-worker-goals-v2`.

## TDD

Update `.claude/skills/supervisor/tools/test_worker_launch.py` first. Assert the canonical Goal path, the ordered Goal/pi-subagents/mecha-messaging list, `--no-extensions`, provider/model, router/Mailbox exports, permission flag, and new/resumed session selectors. Include Goal in package-discovery duplicate coverage and prove a missing Goal fails before `window_exists`, provisioning, or command submission. The focused test must fail before the launcher change.

## Implementation

Update `.claude/skills/supervisor/tools/worker_launch.py` only for the launcher behavior. Derive Goal from `REPOSITORY_ROOT` with `os.path.realpath`, return one ordered extension tuple from `resolve_pi_extensions()`, validate all entrypoints before tmux activity, and use the tuple for both new and resumed Pi commands. Preserve existing provider, model, session, router, Mailbox, and permission behavior.

## Validation

Run the focused launcher test, affected supervisor-tool tests, and full local supervisor-tool discovery. Run changed-line trace coverage, `py_compile`, `git diff --check`, and the new-base installer inherited comparison. Use deterministic local tests only. Record artifacts outside the repository.

## Review

Review the full focus set: backend/runtime, correctness, security, data, operations, tests, documentation/comments, maintainability, and visual UI. Data persistence and visual UI are N/A because this seam stores no data and changes no UI. Run independent validation and a fresh follow-up review against the final immutable target. Record every finding and disposition in the owner-only target ledger.

## Delivery

Commit exactly once with author and committer `Wolfgang Meyers <wolfgangmeyers@gmail.com>`. Push `wolfgang/managed-worker-goals-v2` normally. Do not rebase, amend, force-push, alter the old branch, push `main`, open a PR, restart workers, or modify the primary checkout.