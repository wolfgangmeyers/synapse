# Managed-agent projection compatibility fix

## Scope

Fix the browser-facing managed-agent projection and its UI-live compatibility handling. The change covers the router list projection, UI-live transition frames, Vite HTTP and WebSocket guards, and the client-side managed-row update path.

Archive persistence and archive detail routes keep their current behavior. This fix does not change archive storage, cleanup, history, child projections, authentication, or router lifecycle behavior.

## Observable behavior

### Live-agent list

`GET /v1/managed-agents` returns only current managed-agent rows in `agents`. Every returned row is the live `ManagedAgent` shape. Archive-shaped records do not appear in this browser-facing list, including when the underlying projection snapshot contains both live and archived records.

A valid live row remains usable when it is adjacent to archive-shaped records. The response remains invalid when the live row itself is malformed or carries an unknown field. The existing live-agent detail route continues to return its current live detail contract, and archive persistence/detail behavior remains unchanged.

### UI-live snapshots

A `managed_agents_snapshot` frame contains live managed-agent rows only. A mixed stored snapshot cannot cause an archive row to invalidate the frame or replace a valid live row.

The UI displays every valid live row from the snapshot. An archive-shaped row is excluded from the live managed-agent projection. Other UI-live frame types keep their existing behavior.

### UI-live delta and removal frames

The documented wire contract is:

- `managed_agent_delta`: `type`, `protocol`, `projection_version`, `base_version`, `agent_id`, and `changes`.
- `managed_agent_removed`: `type`, `protocol`, `projection_version`, `base_version`, and `agent_id`.

Neither new frame includes `record_key`. The router never emits that undocumented field in these frames. `changes` is one complete live managed-agent projection, and its `agent_id` matches the frame `agent_id`.

A single live-agent update produces one delta when the projection versions are adjacent. A single live-agent removal produces one removal frame. Version gaps or batched changes continue to use an authoritative snapshot, so the browser never guesses a partial state.

Removing one live agent removes only that agent's managed row and its associated live child state. Other managed rows remain visible, including a row with a different agent ID. Reusing an agent ID across an old archive and a current live row cannot make termination of the live row remove or overwrite the archive detail state because archives are outside the live projection.

### Vite compatibility window

The Vite protocol guards accept both forms during the compatibility window:

1. The new documented delta and removal frames without `record_key`.
2. A known legacy delta or removal frame with an optional `record_key` when that value is a valid bounded string.

The guards reject unknown fields in both forms. They also reject an invalid `record_key`, invalid versions, invalid agent identity, malformed live changes, and other existing contract violations. The TypeScript frame types and clients expose the documented new contract while accepting the legacy wire form only at the validation boundary.

HTTP and UI-live snapshot compatibility filters accept valid live rows from mixed legacy responses. They discard recognized archive-shaped records before live-agent validation. They do not discard valid live records merely because another record in the same response is archive-shaped. Unknown fields on a live record still fail validation.

## High-level design

1. **Managed projection boundary**
   - Use the existing `ProjectionListEnvelope` and `PublicAgent`/`PublicArchive` separation as the boundary between durable storage and browser-facing data.
   - Make the list projection explicitly live-only while leaving archive persistence and archive/detail routes on their existing paths.
   - Keep the live projection identity based on the live agent ID for the browser-managed surface.

2. **UI-live transport**
   - Use `uiLiveProjectionFromStored` to build a live-only browser projection.
   - Use `uiLiveTransition` to emit the documented delta/removal keys and to fall back to a full snapshot for gaps or ambiguous transitions.
   - Make the transition/removal behavior operate on the same live row set that the client applies, so one termination cannot clear unrelated rows.

3. **Vite HTTP and protocol boundaries**
   - Keep strict exact-key validation in the existing HTTP response validators and `isUILiveFrame` guards.
   - Normalize only the known archive-shaped records at the snapshot compatibility boundary.
   - Permit the optional legacy `record_key` only on the two known legacy frame forms, with its existing safe-string validation. Do not generalize the exception to other frame types or fields.

4. **Vite state application**
   - Apply deltas and removals against the live managed-row identity used by the current reducer and UI-live client.
   - Preserve projection-version fencing and recovery behavior.
   - Ensure a removal is scoped to its target live agent and cannot match unrelated managed rows.

## Edge and failure cases

- Empty live list: return an empty `agents` array and preserve the response version metadata.
- Mixed live and archive records: retain all valid live rows and omit archive rows in both HTTP and UI-live snapshots.
- Archive-only stored snapshot: expose no live managed rows; archive persistence/detail routes remain available through their existing contracts.
- Live row with an archive-only field or unknown field: reject the affected response/frame; do not silently broaden the live schema.
- New delta/removal without `record_key`: accept and apply.
- Legacy delta/removal with a valid `record_key`: accept and apply during the compatibility window.
- Legacy delta/removal with an invalid or non-string `record_key`: reject and recover through the existing UI-live protocol path.
- Any other undocumented field on either frame: reject.
- Delta or removal with a version gap, stale base, or regressed version: preserve existing recovery behavior and do not mutate the managed rows.
- Removal for an agent absent from the current live list: advance only when the existing version fence permits it; leave all other rows unchanged.
- Two live rows with different IDs: removing one leaves the other and its child projection intact.
- A live row and an archive with the same `agent_id`: live updates/removals affect only the live row; archive detail behavior is unchanged.
- Router unavailable or projection read failure: preserve the current error/retry behavior. No router restart is part of this fix.

## Alternatives considered

### 1. Change only the Vite filters and guards

This would let the browser ignore archive rows and accept both frame shapes, but the router would continue emitting archive-shaped list/snapshot data and undocumented `record_key` fields. Other browser clients would still receive the wrong contract, and the source of the mixed-record identity problem would remain.

### 2. Remove archive persistence or change archive routes to look live-only

This would simplify the projection, but it would destroy or hide retained archive detail that existing workflows depend on. It exceeds the bug scope and changes durable behavior that the request explicitly preserves.

### 3. Make the router projections live-only, define the wire contract at the router, and keep narrow Vite compatibility at the boundary

This is the chosen approach. The router owns the browser-facing shape, so list and UI-live snapshots agree. The Vite guards remain strict while accepting the two known legacy frame variants for the compatibility window. Keeping archive storage and detail routes separate preserves retention and avoids making archive identity part of the live-agent reducer.

## Acceptance criteria

- Browser-facing managed-agent lists and UI-live managed projections contain live agents only.
- Archive persistence and archive/detail routes behave as they do today.
- Router delta/removal frames omit `record_key` and match the documented key sets.
- Vite accepts new frames without `record_key` and known legacy frames with an optional valid `record_key`.
- Vite rejects unknown fields and invalid legacy values.
- Mixed HTTP/UI-live snapshots retain every valid live row while excluding archive-shaped records.
- Terminating/removing one agent cannot clear or replace unrelated managed rows.
- No router restart, push, staging, or commit is required for this specification phase.
- Only this `SPEC.md` is created for the scoped fix; existing dirty avatar/UI work remains untouched.

# Goal Extension Loading Specification

## Behavior

Every managed Pi worker launch and resume command must start Pi with `--no-extensions` and one ordered list containing the Goal, canonical pi-subagents, and canonical mecha-messaging entrypoints exactly once. Goal is derived from `REPOSITORY_ROOT` with `os.path.realpath`; provider, model, session, router, Mailbox, and permission behavior remain unchanged. Missing extensions fail before tmux side effects.

## Acceptance

- New and resumed Pi argv use the same three-entrypoint order and preserve existing session selectors.
- Goal remains portable across the primary checkout and isolated worktrees.
- Focused tests prove argv parity, package-discovery duplicate avoidance, and pre-tmux missing-Goal failure.

## Non-goals

Extension discovery, runtime configuration, router/UI changes, global copies, dependency changes, and unrelated refactors are out of scope.