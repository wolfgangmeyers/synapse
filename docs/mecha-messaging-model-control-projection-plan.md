# Mecha Messaging model and control projection extraction plan

## Status and baseline

This is an implementation plan only. The current task writes this document. It does not edit source or tests, start services, commit, or push.

The exact implementation baseline is commit `3c9bfb53a3112d0fd4a7a42ba835dc484021de91`. The target is `pi-extensions/mecha-messaging/index.ts`. The baseline source, existing tests, and established seam map are authoritative. Use a clean isolated copy of this commit for differential work. Preserve unrelated dirty and untracked files in the checkout.

## Scope and ownership

Extract the model types, pure projection helpers, model state, model-control path, and adjacent Goal bridge adapter into `pi-extensions/mecha-messaging/model-control-projection.ts`.

The root remains the composition point. It keeps parent context and lifetime, Pi event and tool registrations, session reset and startup/shutdown ordering, activity-lifecycle construction and calls, mailbox callbacks and client ownership, and the sole EventBus subscriptions for Goal state events. It also keeps the cached Goal control state, its normalization, and its publication. The extracted module must not add duplicate handlers or a second cache.

The module owns the extracted model projections, model-control state, and model-control wiring. Its Goal bridge receives an injected EventBus emitter and narrow callbacks for the root-owned Goal state and publication behavior. It may emit a Goal control request through that injected bus. It must not subscribe to the EventBus, import `index.ts`, own Pi registrations, own the Goal state machine, or serialize mailbox wire frames.

`mecha-goal` validates Goal requests and transitions. The mailbox/router path fences wire correlation. The Messaging bridge normalizes its untyped EventBus result and uses the root-owned cached Goal state as its timeout or unavailable-response fallback. Messaging does not validate Goal target, revision, or control ID.

Keep mailbox serialization and protocol details in `mailbox-client.ts`. Do not edit the client or generated client artifact.

## Proposed typed boundary

Use structural types for the external Pi and Goal edges. Names may change if the boundary and behavior stay the same.

```ts
type ModelIdentity = { provider: string; id: string };
type ModelProjection = ModelIdentity & {
  name?: string;
  contextWindow?: number;
  maxTokens?: number;
};
type ModelControlProjection = {
  catalog(): ModelProjection[] | null;
  current(): ModelProjection | null;
  select(input: unknown): Promise<ModelControlResult>;
  onContext(model: unknown): void;
  reset(): void;
};
```

The client callback remains array-returning: the root must continue to pass `getModelCatalog: () => unknown[]` to `createMailboxClient`, with the extracted adapter supplying the full valid source array. Do not replace it with a `{ models, totalCount, truncated }` object or cap it at the display limit. Preserve the existing source bound and normalization. `mailbox-client.ts` must derive `total_count` and `truncated` from the pre-cap normalized source, then apply its existing `MAX_MODELS` and frame-byte limits. A frame reduced by fitting is also truncated against that same pre-cap source count. Tests must cover the one-thousand-entry source and exact boundary behavior.

Provide pure helpers for runtime narrowing and bounded normalization, catalog projection, current-model projection, identity comparison, control-result normalization, and post-switch override handling. Re-export only existing root APIs if the baseline exports one; add no public export.

The module owns the short-lived successful-switch override and control request state. The root supplies `getParentContext()`, `getCurrentModel()`, activity refresh, and notice/trace callbacks. A successful selection refreshes the current projection before its result is returned.

The Goal bridge receives the injected EventBus emitter, a callback to read the cached Goal state, and a callback for root-owned result/state handling. It emits the existing request shape, waits one second, normalizes the untyped event result, and returns the cached state on timeout or unavailable/malformed response. It does not implement Goal transitions or maintain a second revision source.

## Frozen behavior

These rules are acceptance criteria for a behavior-preserving move:

- Model identity is always provider plus model ID. Never match on ID alone.
- A registry entry is usable only when provider and ID are valid. Malformed entries are ignored. Optional names and numeric limits survive only when valid and within existing bounds.
- An unprojectable current model is `null`. Do not substitute a registry item or retain stale optimistic data.
- Validate the requested provider and ID against the available registry before calling `pi.setModel`. Preserve the existing invalid-input, unavailable-registry, unavailable-model, rejected-switch, and failed-switch error codes.
- Capture the native current identity immediately before `setModel`. If `setModel` throws, accept the switch only when that pre-call identity differs from the requested identity and the post-call native provider and ID both match the request. Otherwise retain the failure result.
- A successful switch may use the short-lived override until a fresh Pi context arrives. Clear it when the context changes, when the native projection reflects the requested model, or when the model changes locally. It must not become permanent optimistic state.
- For an accepted switch, the order is activity refresh, forced catalog publication, then model-control wire-result serialization and send. Preserve this order even when the returned result is normalized at the client boundary.
- Goal request and transition validation remains in `mecha-goal`; wire correlation fencing remains in mailbox/router; the Messaging bridge only performs its stated untyped-result normalization and cached-state fallback.

## File ledger and exclusions

Later implementation work may:

- add `model-control-projection.ts`;
- edit `index.ts` for imports, typed construction, root callbacks, re-exports, and delegation only;
- add focused direct, differential, and installed-Pi E2E tests;
- update the package file-list assertion and existing model/Goal assertions when coverage moves across the seam;
- update the quality-refactor roadmap with final evidence.

Do not change `mailbox-client.ts`, generated JavaScript, `mecha-goal`, protocol/client behavior, compaction or lifecycle behavior, diagnostics/history sanitation, router or router proxy, Web UI, Android, settings, package versions, services, or unrelated files. Explicitly exclude compaction, lifecycle, diagnostics, router, Web UI, and Android work from this slice.

## Validation plan

1. **Direct and compatibility tests.** Add module tests for malformed registry records, provider-plus-ID identity, null current projection, optional-field bounds, pre-cap source counts, catalog truncation at and above the limit, validation-before-`setModel`, rejected switches, throw-after-mutation confirmation, override clearing, refresh-before-result ordering, normalized errors, and Goal bridge timeout/fallback behavior. Explicitly run `tests/pi_extension_standalone.test.ts` and `tests/mecha_messaging_goal_bridge.test.ts`, plus the existing transport gates in `tests/model_catalog_transport.test.mjs`, `tests/mailbox_client_goal_control.test.mjs`, `tests/router_web_ui.test.mjs`, and `tests/goal_transport.test.ts`.

2. **Executable differential.** Run baseline and extracted implementations in separate clean temporary roots with separate homes, fake Pi registries, contexts, EventBuses, clients, and observation sinks. Drive startup/reset, valid and malformed catalogs, pre-cap and boundary truncation, every switch outcome including throw-after-mutation with both pre-call identity cases, stale context, override clearing, reconnect, cross-target response, timeout, stale revision, and full-state fallback. Record ordered projections, callback calls, errors, control responses, refresh, forced publication, and wire-result observations. Compare sequences and final state. Normalize only injected paths, timestamps, and generated IDs. Any difference outside a written allowlist fails with the first scenario and operation.

3. **Temporary installed-Pi E2E.** Use a temporary router, WebSocket clients, temporary `HOME` and session paths, a fake registry, and a fake `setModel` with an isolated installed-Pi runtime. Prove the loaded Pi and Messaging extension identities. Verify catalog/current-model frames, successful/rejected/throw-after-mutation switches, reconnect behavior, and a Goal control request through the injected EventBus. Assert exact provider/ID projections, pre-cap `total_count`/`truncated`, error codes, and success ordering. Do not use the live router, browser settings, credentials, or a shared session.

4. **Quality gates.** Run the explicit standalone and Goal-bridge tests, focused tests, changed-source coverage, mutation checks, package typecheck and lint, the complete Mecha Messaging test command, export/package-file checks, and the repository's full validation command. Mutation checks must invert representative assertions and prove the focused suite fails before restoration. Run Robot Review plus supervisor review, validate findings, and resolve every real finding before readiness.

5. **Completion record.** Update the roadmap with the final commit, exact validation results, differential allowlist, installed-Pi runtime identity, and rejected review findings. Push only the scoped implementation after required review and validation gates. The present task does none of those implementation actions.
