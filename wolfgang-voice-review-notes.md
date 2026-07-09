# Wolfgang Voice Review Notes

Working notes from reviewing PR review comments against Wolfgang's preferred review voice.

These are calibration notes, not final skill instructions. Distill durable rules into:

- `personas/wolfgang-voice.md`
- `.claude/skills/wolfgang-voice-review/SKILL.md`

Potential downstream updates:

- Consider updating `mecha-review` after collecting more examples. Comment 4 appears to be a review-quality false positive / overclaim, not just a voice issue: it asked for proof of completeness from a layer that likely can only document and trust the `Source.RebuildHistory` contract.

## Identity-Digital/dnsid-go#124

PR: https://github.com/Identity-Digital/dnsid-go/pull/124

### Comment 1: nested module depends on unreleased parent fields

Original comment:

> Blocking: this nested module currently requires `github.com/Identity-Digital/dnsid-go v0.11.0`, but the package uses `LogEvent` fields that are added in this PR. The local `replace ../..` masks that during development; consumers do not inherit dependency `replace` directives, so this package will resolve the older root module and fail to compile. This new module also needs the same nested-module wiring as `key/aws`: run `(cd log/c2sptlog && go test ./...)` in CI and update/tag this module during release.

Wolfgang rewrite:

> `v0.11.0` doesn't have the new `LogEvent` fields, I think we need to do this in 2 stages instead. Cut a new `dnsid-go` release then use that version instead.
>
> Also, this needs the same nested-module CI/release wiring as `key/aws`: run its tests in CI and tag it as part of the release.

Why the original is off:

- It explains too much mechanism up front. The `replace` detail is true, but it is not the first thing the author needs.
- The important point is the release ordering problem: the nested module depends on fields that do not exist in the published parent version.
- Wolfgang's version names the concrete fix directly instead of proving the entire failure mode.
- Keep the `replace` explanation available only if needed; do not lead with it.

Distilled rules:

- Prefer the actionable release/process decision over a full causal proof when the causal proof is mostly tooling mechanics.
- Say the concrete incompatibility first: "`vX` does not have `Y`."
- For dependency/versioning comments, propose the release sequence directly.
- Do not prepend inline comments with "Blocking:". If severity matters, make it clear through the substance of the comment or the review state.
- Avoid robotic review labels and stock AI phrasing. The point of this review pass is to make comments sound like Wolfgang wrote them, not like a generic review agent classified them.
- A good rewrite can be shorter even if it drops technically useful background.

### Comment 2: public write path accepts events rebuild rejects

Original comment:

> Blocking: public verification now requires signed `seq`, `prev_index`, `prev_leaf_hash`, and `prev_state_hash` on events after the stream start, but `WriteEvent` serializes whatever the caller supplied. `payloadFromEvent` only emits those fields when they are already present, and `IdentityManager.SignAndWriteEvent` signs the canonical bytes before this append path runs. A normal public rotation or revocation write can therefore succeed, then fail public rebuild later. Please compute/require the chain metadata before signing, or reject public writes that do not contain it, and add an end-to-end public write/rebuild test.

Wolfgang rewrite:

> Looks like public writes aren't requiring the chain fields (`seq`, `prev_index`, `prev_leaf_hash`, `prev_state_hash`) for events after the initial issuance. Can we make sure those are added before signing and covered by a write + rebuild test?

Why the original is off:

- It starts with the robotic "Blocking:" label.
- It explains too much internal mechanism in the posted comment.
- The key distinction is simple: the first issuance only needs `seq = 0`; later public events need the full chain fields.
- The important fix is that fields must be present before signing, not merely patched in before append.
- The "write succeeds, rebuild fails" explanation is useful for discussion, but it does not need to be fully spelled out in the PR comment.

Distilled rules:

- For validation-path mismatches, lead with the missing requirement and affected scope.
- Prefer "Looks like..." for uncertain but concrete findings.
- Ask for the expected fix and test together: "Can we make sure X and cover it with Y?"
- Avoid exhaustive function-by-function walkthroughs unless the author needs the debugging trail.

### Comment 3: historical issuance accepted after key rotation

Original comment:

> Blocking: this accepts any historical `ISSUANCE` for the requested thumbprint. After a `KEY_ROTATION`, the previous operational key is still present in the issuance event, so a TXT record that keeps advertising the rotated-away key can satisfy `fl=logchk` as long as there is no revocation. This needs to verify the current lifecycle snapshot/active key at the verification time, not just historical issuance existence.

Wolfgang rewrite:

> Shouldn't this check whether the key is still active? If the key was rotated, it looks like the old issuance event can still make `fl=logchk` pass.

Why the original is off:

- It starts with the robotic "Blocking:" label.
- It overexplains the lifecycle mechanics for a small, localized concern.
- The simpler question shape is enough: ask whether the check should account for rotation/current active state.
- The failure mode still needs one concrete sentence so the author sees why the question matters.

Distilled rules:

- Use question-shaped review comments when the issue is a likely missing condition or edge case.
- Keep the question concrete enough that it implies the needed fix.
- Add one short failure-mode sentence when a bare question would be too easy to dismiss.

### Comment 4: non-revocation depends on complete source history

Original comment:

> Blocking: `VerifyNonRevocation` accepts whatever `Source.RebuildHistory` returns. Inclusion proofs verify the entries that came back; they do not prove that the source returned every entry for this stream. An incomplete or untrusted indexer can omit a revocation/retirement and this returns success. Current-state non-revocation needs a verifiable completeness/freshness signal, or it should fail closed unless the source mode is a scan/trusted index/monitored index/committed index/signed bundle that proves completeness.

Wolfgang rewrite:

> I think this depends on `RebuildHistory` returning a complete history, but that isn't documented on the `Source` contract. Can we make that expectation explicit, especially because `VerifyNonRevocation` trusts it to prove no revocation/retirement happened?

Why the original is off:

- It starts with the robotic "Blocking:" label.
- It frames the fix as "prove completeness/freshness" or fail closed, but this layer may not have a realistic way to prove that.
- The actual design assumption is trust in the `Source` implementation. The SDK defines the interface but does not appear to own a production source that can cryptographically prove all matching events were returned.
- Implementing a stronger interface would not prove anything by itself; the implementation would still need to scan a complete log, use a trusted indexer, or rely on some external cryptographic summary.
- The better review point is the API contract: `RebuildHistory` sounds complete, and `VerifyNonRevocation` relies on that completeness, so the expectation should be documented clearly.
- This is a meaningful change in point of view, not just a voice rewrite. The original asks for a stronger guarantee; Wolfgang's version asks to make the necessary trust boundary explicit.

Distilled rules:

- Do not demand a stronger guarantee than the reviewed layer can actually provide.
- When an abstraction necessarily trusts an implementation, ask to document the contract/trust boundary instead of pretending the abstraction can prove it.
- If discussion changes the technical diagnosis, capture the changed point of view explicitly instead of treating the rewrite as a shorter version of the original.
- Prefer "I think this depends on..." for design-contract assumptions that may be true but are not documented.

### Comment 5: key age uses self-declared event timestamp

Original comment:

> Should address: this returns the event’s signed `ts`, not an accepted log timestamp. When `Policy` has no `CheckpointTime` and `WitnessQuorum <= 0`, `proof.LogTime` is zero, `RebuildHistory` skips the `event.Timestamp <= proof.LogTime` guard, and key-age enforcement consumes a self-declared timestamp. Timestamp-dependent checks should fail when no accepted checkpoint/witness time exists, or carry the accepted proof time through and use that for `KeyTimestamp`.

Wolfgang rewrite:

> I think `KeyTimestamp` needs to require a trusted checkpoint/witness time instead of returning the event's self-declared `ts`. Since any TXT record can set `ka=`, we probably shouldn't allow a C2SP log reader without timestamp policy configuration.

Why the original is off:

- It uses a robotic "Should address:" label.
- It buries the product/API concern under implementation details about `proof.LogTime` and `RebuildHistory`.
- The issue is real: `ka=` enforcement calls `KeyTimestamp`, and at the reviewed commit `KeyTimestamp` returned the event author's timestamp directly.
- The risky behavior is not "backdating" for max-age enforcement; it is relying on a self-declared event timestamp for trusted key-age policy.
- The witness path is one way to derive trusted checkpoint time from witness cosignatures. The important review point does not need to teach that mechanism.
- Wolfgang's version turns the finding into the higher-level requirement: C2SP log readers that may enforce `ka=` need timestamp policy configuration.

Distilled rules:

- Lead with the product/API requirement when the implementation details are only supporting evidence.
- For time/security-sensitive findings, name the trust boundary directly: self-declared time vs trusted time.
- Avoid teaching the whole mechanism in the PR comment when the requested change is a clearer configuration/API requirement.

### Comment 6: ReadEvent trusts returned event for final ref

Original comment:

> Should address: `ReadEvent` delegates the requested `ref` to the source, but the client never parses the final event reference or checks that the returned proof index matches the `@index`. It also returns the parsed event without lifecycle signature validation. For migration final-entry verification, a source could return a different included event and this method would still hand it back. Please parse `ParseFinalEventRef`, require the parsed reference and proof index to match, and either verify the event signatures/lifecycle context or expose this as a raw inclusion-proof read.

Wolfgang rewrite:

> This should parse the ref and ensure that the reference and proof indexes match, and validate that the event is part of the verified lifecycle. Unless this is supposed to be a raw-inclusion read?

Why the original is off:

- It uses the robotic "Should address:" label.
- It spends too much time proving the possible bad source behavior.
- The core contract is clear enough: if `ReadEvent` returns a lifecycle event for a final ref, it should verify both the requested index and lifecycle membership.
- The alternative design is useful, but it should be a short question rather than a long "either/or" solution list.
- "Raw inclusion read" means a weaker API that only proves an entry is included in the log, not that it is valid lifecycle history.

Distilled rules:

- Lead with the preferred API contract when a function name implies stronger semantics.
- If there is a plausible weaker contract, add it as a short "unless this is supposed to be X?" question.
- Avoid long alternative-solution lists when one preferred fix is clear.

### Comment 7: migration-in fields without verifier support

Original comment:

> Blocking: migration events are parsed/serialized, but the lifecycle verifier can never accept migration as the first event in a C2SP stream. That means `prev_lr`/`prev_ref` migration-in histories are representable on the wire but impossible to verify. If migration-in is intended for C2SP, this needs a genesis path that verifies the referenced prior final entry and then accepts the migrated key state, or the migration fields should be rejected/left unsupported until that is designed.

Wolfgang rewrite:

> Hmm aren't `prev_lr` and `prev_ref` used for migration-in? Are we adding support for that in this PR? If so, we should update the verifier to handle migration as the first event.

Why the original is off:

- It starts with the robotic "Blocking:" label.
- It assumes the solution framing before asking the product-scope question.
- The core issue is that the PR appears to half-support migration-in: fields are parsed/serialized, but the verifier cannot accept migration as the first event.
- Wolfgang's rewrite asks whether migration-in is actually in scope before requiring the full verifier path.
- The alternative "reject/leave unsupported" path is implied by the scope question and does not need to be spelled out unless the author asks.

Distilled rules:

- When code appears to half-support a feature, ask the scope question first.
- Use "If so..." to connect the missing implementation work to the author's answer.
- Do not force a design branch in the first comment when a short scope question will clarify intent.

### Comment 9: RebuildHistory ordering contract is unclear

Original comment:

> Should address: `SnapshotAt` expects events in lifecycle order, but `RebuildHistory` sorts C2SP entries by log index before verification and returns them in that order. For public C2SP logs, consensus/log order and lifecycle timestamp order are not guaranteed to match, especially after migration or delayed publication. A caller using `SnapshotAt` on this history can get the wrong active/revoked state unless the API documents log-order semantics or returns lifecycle-sorted events.

Wolfgang rewrite:

> Can we document the ordering contract for `RebuildHistory` / `DomainLog.Events()`? `SnapshotAt` sorts by lifecycle timestamp internally, but other consumers may not know whether these events are in log order or lifecycle order.

Why the original is off:

- It uses the robotic "Should address:" label.
- It appears to overclaim that `SnapshotAt` can consume the `RebuildHistory` order incorrectly. At the reviewed commit, `SnapshotAt` sorts events by lifecycle timestamp internally.
- There is still a real documentation issue: `RebuildHistory`, `DomainLog`, `Events()`, and `SnapshotAt` do not clearly document event ordering expectations.
- Wolfgang's rewrite changes the finding from a behavioral bug to an API/documentation ambiguity.

Distilled rules:

- If investigation weakens the bug claim, rewrite the comment to the smaller true issue instead of preserving the original severity.
- Do not claim a caller is broken when the code already normalizes internally.
- For API ambiguity, ask to document the contract rather than implying an implementation bug.

### Comment 10: witness quorum timestamp uses earliest signer

Original comment:

> Should address: when a witness quorum is required, `acceptedCheckpointTime` returns the earliest accepted witness timestamp plus skew. That can treat the checkpoint as accepted before quorum actually existed. If key-age or freshness policy uses this value, a checkpoint with one old witness signature and the remaining quorum signatures much later inherits the old timestamp. The accepted time should be the quorum threshold timestamp, not the earliest signer.

Wolfgang rewrite:

> Shouldn't `acceptedCheckpointTime` return the final accepted witness timestamp? Seems like the timestamp should reflect when quorum was established.

Why the original is off:

- It uses the robotic "Should address:" label.
- The core point is simple: accepted time should reflect when quorum was established.
- The long example about one old witness and later quorum signatures is useful background, but it does not need to be in the PR comment unless the author asks.

Distilled rules:

- For simple logic questions, ask the direct question first and keep the rationale to one short sentence.
- Prefer "Seems like..." for a concise explanation of expected behavior.
- Do not include a long hypothetical when the invariant is easy to state directly.

### Comment 11: JSON canonicalization is not full JCS

Original comment:

> Should address: this is not a full RFC 8785/JCS canonicalizer. The known fields are mostly in a safe subset, but unknown signed fields are explicitly included in canonicalization, and `json.Number` preserves lexical forms such as `1.0` where JCS would emit a different canonical number. Unicode escaping can also diverge from JCS. Please use a JCS canonicalizer, or reject unknown/non-safe values so the implementation cannot sign or accept payloads outside the subset where Go’s JSON output matches JCS.

Wolfgang rewrite:

> I don't think this is actually JCS canonicalization. Can we either use a real JCS canonicalizer or reject values outside the subset we know Go serializes the same way?

Why the original is off:

- It uses the robotic "Should address:" label.
- It includes too many examples of how canonicalization can diverge.
- The important point is simpler: the implementation is not actually the named standard unless it uses a real implementation or restricts inputs to a safe subset.
- Wolfgang's rewrite keeps the two viable fixes but drops the proof trail.

Distilled rules:

- When implementation claims a standard but only supports a subset, say that directly.
- Keep one concise fix-choice question when there are two legitimate paths.
- Do not list every technical divergence when the category of divergence is enough.

### Comment 12: proof verifier skipped in rebuild tests

Original comment:

> Should address: the client tests all use `withProofVerifySkipped`, so the new proof verifier is not exercised on the path that rebuilds history. `Policy.VerifyProof` is security-sensitive code; it needs at least one real proof/checkpoint success path plus negatives for wrong origin, wrong entry index, wrong leaf hash, insufficient witness quorum, stale checkpoint, and future witness timestamp.

Wolfgang rewrite:

> Looks like the rebuild tests are all using `withProofVerifySkipped`, so we aren't actually covering `Policy.VerifyProof` here. Can we add at least one real proof/checkpoint test and a few negative cases for the verifier?

Why the original is off:

- It uses the robotic "Should address:" label.
- It turns the test request into a long checklist.
- The important point is the untested security-sensitive path.
- Wolfgang's rewrite asks for representative coverage without enumerating every negative case in the first comment.

Distilled rules:

- For missing-test findings, name the path that is not actually covered.
- Ask for representative positive and negative coverage instead of dumping a full test matrix.
- Use "we aren't actually covering X" when tests exist but bypass the important path.

### Comment 13: public chain fields checked before signature validity

Original comment:

> Blocking: this rejects bad public chain fields before the event's signature/lifecycle validity is known. In a public append-only log, an attacker can add a same-domain candidate with an invalid signature and bad chain metadata; `RebuildHistory` returns `invalid c2sp-tlog public lifecycle chain` instead of ignoring the unsigned entry. Please verify that the event would otherwise be accepted before treating bad chain metadata as a history error.

Wolfgang rewrite:

> Should we check the event signature before validating the public chain fields? That would cause unsigned events to be ignored rather than failing the history rebuild.

Why the original is off:

- It uses the robotic "Blocking:" label.
- It over-explains the attacker scenario before asking the simpler ordering question.
- The important point is the validation order: signature validity should be known before public chain metadata is treated as fatal.
- Wolfgang's rewrite states the practical outcome directly.

Distilled rules:

- For ordering bugs, ask whether the checks should happen in the opposite order.
- State the observable behavior change in one sentence.
- Avoid leading with attacker framing when a simple validation-order explanation is enough.

### Comment 14: nested module still points at old parent version

Original comment:

> Blocking: this nested module requires `dnsid-go v0.11.0`, but the new C2SP code references root `log.LogEvent` fields added after that tag, such as `EntityPublicKey`, `AccountableSig`, and `OperationalCountersig`. The local `replace => ../..` makes repo CI pass, but consumers do not inherit dependency replaces from imported modules, so a normal consumer build resolves the older parent API and fails. Please require a parent version/pseudo-version that contains the needed API and add a consumer-mode check without the local replace.

Wolfgang rewrite:

> `v0.11.0` still doesn't have these `LogEvent` fields. I think this needs to point at a parent version that includes them, and we should have a no-replace consumer check so CI catches this.

Why the original is off:

- It uses the robotic "Blocking:" label.
- It repeats the full `replace` explanation from an earlier version of the same finding.
- The important update is that the old parent version still lacks the fields, and CI should catch that in consumer mode.
- Wolfgang's rewrite keeps the release/versioning point concrete without re-proving the whole Go modules failure mode.

Distilled rules:

- For repeated findings, do not re-explain the whole mechanism.
- Use "still" when the issue survived an attempted fix or later revision.
- Keep the added ask focused on what would catch the regression next time.

### Comment 15: SnapshotAt became caller-order-sensitive

Original comment:

> Should address: removing the timestamp sort changes the exported `DomainLog.SnapshotAt` contract from order-insensitive to caller-order-sensitive. Valid but unsorted histories can now produce false terminal-state errors or the wrong active key. Please either restore timestamp ordering here, or explicitly document and enforce ordered input at construction and all callers.

Wolfgang rewrite:

> Looks like removing the timestamp sort makes `SnapshotAt` caller-order-sensitive. Can we either keep the sort here or document/enforce that `DomainLog` events must already be in lifecycle order?

Why the original is off:

- It uses the robotic "Should address:" label.
- It overstates the failure modes before stating the contract change.
- The core issue is enough: a previously order-insensitive API now depends on caller-provided ordering.
- Wolfgang's rewrite keeps the two viable paths but removes the extra proof.

Distilled rules:

- For contract changes, name the before/after behavior directly.
- Offer the two obvious paths when both are legitimate: preserve the old behavior or document/enforce the new contract.
- Avoid piling on failure examples when the contract change itself explains the risk.

### Comment 16: MaxCheckpointAge skips when timestamp is unavailable

Original comment:

> Should address: `MaxCheckpointAge` is skipped when no accepted checkpoint time is produced. With `MaxCheckpointAge > 0` and no `CheckpointTime` hook or witness quorum timestamp, `logTime` is zero and stale checkpoints still verify. If freshness is configured, missing timestamp evidence should fail closed.

Wolfgang rewrite:

> If `MaxCheckpointAge` is set, shouldn't we require a checkpoint/witness timestamp? Otherwise it looks like freshness is configured but silently skipped when `logTime` is zero.

Why the original is off:

- It uses the robotic "Should address:" label.
- It states the implementation mechanics before the policy expectation.
- The important point is that a configured freshness policy should not silently do nothing.
- Wolfgang's rewrite asks the policy question first and keeps the implementation detail as the consequence.

Distilled rules:

- When a configured policy can silently do nothing, ask whether the prerequisite should be required.
- Lead with the configuration/behavior mismatch, then name the implementation state that causes it.
- Use "silently skipped" for policy paths that appear enabled but are bypassed.

### Comment 17: quorum timestamp should reflect quorum threshold

Original comment:

> Should address: quorum acceptance time should be the timestamp at which the configured quorum is reached, not the earliest witness timestamp. With quorum 2 and witness times 10:00 and 11:00, the checkpoint is accepted at 11:00. Sort accepted witness times and use `times[WitnessQuorum-1]`, with future/skew checks applied to that threshold.

Wolfgang rewrite:

> Shouldn't quorum time be when the configured quorum is reached? With quorum 2, that seems like the second accepted witness timestamp, not the first.

Why the original is off:

- It uses the robotic "Should address:" label.
- It gives an implementation recipe when the conceptual invariant is enough.
- This is a cleaner version of the earlier quorum-time finding: the accepted time should be the quorum threshold time.

Distilled rules:

- For repeated concepts, prefer the cleaner formulation and do not add new rules unless the feedback changes.
- Avoid implementation recipes when the expected invariant is clear.

### Comment 18: generic log interface cannot pass C2SP chain fields

Original comment:

> Should address: a public `c2sptlog.Client` satisfies the generic `dnsidlog.Log` interface, but generic manager flows only call `Canonical` and `WriteEvent`. For public non-issuance events this path always errors because chain fields must come from `WriteEventWithChain`, so manager-driven flows like revocation cannot write through a registered public C2SP log. Please either provide a chain-field path through the root abstraction or avoid exposing public clients as generic write-capable logs.

Wolfgang rewrite:

> I think we need to update the `dnsidlog.Log` interface to handle the chain fields. I believe the C2SP log requires these for all writes after issuance.

Why the original is off:

- It uses the robotic "Should address:" label.
- It uses abstract phrasing like "root abstraction" instead of naming the concrete interface.
- It over-explains the manager flow before stating the required API change.
- Wolfgang's rewrite names the interface and the reason directly.

Distilled rules:

- Name the concrete interface/API instead of using broad abstraction language.
- If the preferred fix is clear, state it directly instead of offering multiple design branches.
- Keep the consequence implicit when the API mismatch already explains the problem.

### Comment 19: historical issuance still passes after rotation

Original comment:

> Blocking: this still verifies historical issuance rather than the current lifecycle binding. After a valid `KEY_ROTATION`, the original operational key remains in the `ISSUANCE` event, so a TXT record that keeps advertising the rotated-away key can satisfy `fl=logchk` as long as there is no revocation. Please verify against the materialized active key at the verification time, covering both initial issuance and rotation-derived bindings.

Wolfgang rewrite:

> This still looks like it's checking historical issuance rather than whether the key is active now. Shouldn't `fl=logchk` fail if the TXT record keeps advertising a key that was already rotated out?

Why the original is off:

- It uses the robotic "Blocking:" label.
- It repeats the full mechanism for a repeated issue.
- The important point is that the attempted fix still checks historical existence instead of current active state.
- Wolfgang's rewrite keeps the concrete failure mode as a question.

Distilled rules:

- For repeated findings, use "still" and focus on what remains wrong.
- Convert remaining edge-case failures into a direct "shouldn't X fail if Y?" question.
- Do not repeat full background for an issue already raised earlier in the review.

### Comment 20: migration-in still cannot start rebuild

Original comment:

> Blocking: lifecycle rebuild still can only start from `ISSUANCE`. The package parses and serializes `MIGRATION`, but this pre-start branch skips every non-issuance event, so a migration-in stream can never become verified history. Please implement the migration start path with prior-log/final-ref verification, or reject `MIGRATION` until it is actually supported and tested.

Wolfgang rewrite:

> This still looks like partial migration-in support. If `MIGRATION` is supposed to be supported here, shouldn't rebuild be able to start from it? Otherwise I think we should reject it until it's actually supported.

Why the original is off:

- It uses the robotic "Blocking:" label.
- It repeats the mechanism from the earlier migration-in finding.
- The core question is still product/API scope: is migration-in supported in this PR or not?
- Wolfgang's rewrite keeps both paths, but phrases them as the natural consequence of the scope decision.

Distilled rules:

- For repeated partial-feature findings, name the partial support directly.
- Use "If X is supposed to be supported..." to tie the required implementation to scope.
- Use "Otherwise I think we should reject it..." when unsupported input should not be accepted yet.

### Comment 21: no-replace check fails on release PRs

Original comment:

> Blocking: this no-replace consumer check will fail on the generated release PR. `release-pr.yml` rewrites `log/c2sptlog/go.mod` to require the next root tag before `release.yml` creates that tag; then this step drops the local replace and tries to download the not-yet-existing version. I reproduced the shape by requiring `github.com/Identity-Digital/dnsid-go@v0.12.0` without the replace, which fails with `unknown revision v0.12.0`. Please make this check release-PR-aware or test against a reachable pseudo-version/current checkout until the tag exists.

Wolfgang rewrite:

> I think this no-replace check needs to skip or change behavior on `release/next`. The release PR points `log/c2sptlog` at the next root tag before that tag exists, which will cause CI to fail.

Why the original is off:

- It uses the robotic "Blocking:" label.
- It explains the reproduction in more detail than the PR comment needs.
- "Release-PR-aware" is vague by itself; naming `release/next` makes the condition concrete.
- Extra caveats like "even if the module is otherwise releasable" sound like generic review padding and are not Wolfgang's phrasing.

Distilled rules:

- Replace vague workflow terms with the concrete branch, job, or condition when known.
- Stop after the practical failure mode when it is already clear.
- Avoid trailing caveats that do not change the requested fix.

### Comment 22: non-revocation ignores retirement

Original comment:

> Should address: this only fails on `REVOCATION`, while `RETIREMENT` is also terminal in the lifecycle verifier and `DomainLog.SnapshotAt`. A retired lifecycle with no revocation can still pass the `fl=logchk` non-revocation step. Please either treat terminal non-active states at or before the verification time as policy failures, or add an explicit active-state check alongside non-revocation.

Wolfgang rewrite:

> Should this also fail for retired identities? `RETIREMENT` is terminal too, but this looks like it only checks revocation.

Why the original is off:

- It uses the robotic "Should address:" label.
- It over-explains the lifecycle policy path before asking the simple missing-state question.
- Wolfgang's rewrite names the omitted sibling state and why it matters.

Distilled rules:

- For missing sibling conditions, ask whether the omitted case should be handled too.
- Name the parallel condition directly instead of describing every downstream policy effect.

### Comment 23: generic log path still lacks chain metadata

Original comment:

> Should address: public `Client` still satisfies the generic `dnsidlog.Log` write interface, but generic manager flows only call `Canonical` and `WriteEvent`. For public non-issuance events this path errors because chain metadata can only be supplied through C2SP-specific methods, so `IdentityManager`-driven public revocation/rotation writes are not usable. Please add a chain-aware root abstraction/provider hook, or avoid exposing public C2SP clients as generic write-capable logs until the chain can be supplied before signing.

Wolfgang rewrite:

> I think this still needs a path for `dnsidlog.Log` to handle C2SP chain metadata. Public C2SP revocation/rotation writes don't seem usable through the generic manager flow otherwise.

Why the original is off:

- It uses the robotic "Should address:" label.
- It repeats the full interface explanation from an earlier comment.
- It uses abstract wording like "root abstraction/provider hook" instead of naming the concrete interface/path.
- Wolfgang's rewrite states the remaining API gap and practical consequence.

Distilled rules:

- For repeated API-gap findings, name what still needs a path and the concrete flow that remains unusable.
- Avoid introducing new abstract nouns when the concrete interface name is enough.

### Comment 24: migration-in does not verify prior ref

Original comment:

> Blocking: migration-in genesis now starts a verified C2SP stream when `prev_lr` and `prev_ref` are non-empty, but this path never parses the final reference or verifies the referenced prior-log event. A self-signed migration can claim continuity from any prior log without proving that the previous lifecycle actually ended in a matching migration-out/final entry. Please verify the referenced prior log/final entry before accepting migration-in genesis, including the expected domain/key/terminal migration context, or keep migration-in unsupported until that continuity check exists.
>
> Socratic question: Does this path verify the event referenced by `prev_ref`, or only check that the field is non-empty?

Wolfgang rewrite:

> Looks like migration-in now checks that `prev_lr` and `prev_ref` are present, but not that the referenced prior event is valid. Should we verify `prev_ref` before allowing migration to start the new stream?

Why the original is off:

- It uses the robotic "Blocking:" label.
- "Genesis" is obscure here. It means "first event" or "start the new stream," and the plainer wording is better.
- It includes an explicit "Socratic question" label, which sounds like an AI review artifact.
- The important point is that presence checks are not validation of the referenced prior event.

Distilled rules:

- Avoid obscure domain-ish terms when plain words say the same thing.
- Do not label questions as "Socratic question."
- Contrast presence checks with actual validation when that is the bug.

### Comment 25: migration-in governance binding not checked

Original comment:

> Should address: the new migration-in genesis path materializes `GovernanceID` into `SnapshotAt`, but `VerifyGovernanceRelationship` still only accepts `ISSUANCE` events. A migrated stream with matching `gi` can pass key-state verification while failing record-policy governance checks for non-domain or delegated governance identifiers. Please check the materialized snapshot governance binding, or treat a validated migration-in genesis with matching `GovernanceID` the same way as issuance for this relationship proof.
>
> Socratic question: Can a migration-in genesis with matching `gi` satisfy this loop?

Wolfgang rewrite:

> If migration can start the stream with a `GovernanceID`, shouldn't `VerifyGovernanceRelationship` handle that too? It looks like this only accepts `ISSUANCE`.

Why the original is off:

- It uses the robotic "Should address:" label.
- It uses the obscure "genesis" wording again.
- It includes a labeled "Socratic question" artifact.
- The core issue is a simpler missing-case question: if migration can establish the governance binding, the verifier should consider it.
- Incidental temporal filler like "today" is unnecessary unless the timing matters.

Distilled rules:

- Remove incidental time words when they do not add meaning.
- For missing-case findings, phrase the expected inclusion as "shouldn't X handle that too?"
- Strip review-framework labels before posting.

### Comment 26: migration signing helper omits operational countersignature

Original comment:

> Should address: C2SP serialization now treats key-bearing `MIGRATION` like `ISSUANCE` and emits both `ae` and `op` signatures, but the SDK signing helper only has that countersignature path for `ISSUANCE`. A caller using `SignAndWriteEvent` for a migration-in genesis with a prefilled accountable signature will not get the required operational countersignature, so the event it writes cannot pass `verifyLifecycle`. Please extend the countersign path to key-bearing migration-in genesis events, while keeping migration-out records accountable-signature-only.
>
> Socratic question: Does the SDK signer add an operational countersignature for key-bearing `MIGRATION` events?

Wolfgang rewrite:

> If key-bearing `MIGRATION` events need the operational countersignature, shouldn't `SignAndWriteEvent` add it there too? It looks like that path only handles `ISSUANCE`.

Why the original is off:

- It uses the robotic "Should address:" label.
- It includes a labeled "Socratic question" artifact.
- It uses "genesis" again where the simpler migration wording is enough.
- The core issue is a missing sibling path in the signing helper.
- The separate Socratic-question behavior has now been removed from `mecha-review`'s `review-pr` skill.

Distilled rules:

- For sibling-path bugs, ask whether the same helper behavior should apply to the new supported case.
- Keep signing/serialization mismatch comments focused on the missing path and the expected helper behavior.

### Review summary 1: first C2SP request-changes summary

Original comment:

> Requesting changes. The prior Greptile lifecycle findings are fixed at this head, and the root plus nested C2SP test suites pass locally.
>
> The remaining issues are in the public C2SP lifecycle contract and module integration: the new nested module is not wired into CI/release, public writes can emit events that public rebuild rejects, current verification can accept a rotated-away key or an incomplete stream, migration-in cannot start a stream, and the proof/canonicalization paths need tighter verification before this method is reliable.

Wolfgang rewrite:

> I think this still needs changes around the public C2SP lifecycle contract and nested module setup. The main issues are CI/release wiring for the nested module, public writes that rebuild can't verify, rotated-key verification, migration-in support, and proof/canonicalization coverage.

Why the original is off:

- It starts with the stock review-state sentence "Requesting changes."
- It includes local test status in the summary even though the useful part is the grouped reason for the review state.
- Wolfgang's rewrite leads with the review judgment and groups the issue categories without narrating the whole review process.

Distilled rules:

- Review summaries should lead with the actual judgment, not the review UI state.
- Group related issues into named categories instead of listing every inline finding mechanically.
- Omit local validation status from the summary unless it changes the decision or is important context.

### Review summary 2: current-head request-changes summary

Original comment:

> Requesting changes on current head. The latest revision fixes several earlier lifecycle gaps, including proof index binding, invalid duplicate shadowing, and an explicit public-chain write API, but there are still correctness issues in the C2SP lifecycle contract.
>
> Blocking:
> - `log/c2sptlog/client.go:158`: `VerifyIssuance` still treats any historical ISSUANCE as a valid key binding. Since `fl=logchk` calls this method for the TXT signing key, a key that was rotated out of active use can still pass log checking as long as it has not been revoked.
> - `log/c2sptlog/signatures.go:52`: migration entries are parsed/serialized but can never become the first accepted event in a C2SP stream, so migration-in histories are not verifiable.
> - `log/c2sptlog/signatures.go:90`: public chain metadata is enforced before post-start signature validation, so unauthenticated junk can poison a public lifecycle rebuild.
> - `log/c2sptlog/go.mod:6`: the nested module requires parent `dnsid-go v0.11.0` while using root `log.LogEvent` fields added after that tag; the local replace hides this from CI but consumers do not inherit it.
>
> Also worth fixing before merge: the shared `DomainLog.SnapshotAt` API is now caller-order-sensitive, checkpoint freshness can be silently disabled when no timestamp source exists, witness quorum time uses the earliest signature rather than the quorum threshold, and public non-issuance writes are not usable through the generic `log.Log` path.
>
> Local validation passed: `go test ./...`, `(cd log/c2sptlog && go test ./...)`, focused root log tests, `go vet ./...` in both modules, `gofmt -l .`, `git diff --check origin/main...HEAD`, and the reported GitHub checks are green.

Wolfgang rewrite:

> I think this still needs changes around the C2SP lifecycle contract. The remaining blockers are rotated-away keys passing `fl=logchk`, migration-in not being verifiable, unauthenticated events breaking rebuild, and `log/c2sptlog` using parent fields that aren't in `v0.11.0`.
>
> There are a few other things worth fixing before merge: `SnapshotAt` is now caller-order-sensitive, checkpoint freshness can be silently skipped without a trusted timestamp, quorum time uses the first witness timestamp instead of when quorum is reached, and public non-issuance writes still don't work through the generic `log.Log` path.

Why the original is off:

- It starts with the review UI state instead of the actual judgment.
- The `Blocking:` label makes the summary sound like a generated report.
- The issue bullets are too close to copied inline findings for a summary.
- The local validation paragraph is process narration and does not change the requested changes decision.

Distilled rules:

- In review summaries, distinguish blockers from other useful fixes in natural prose, not with report labels.
- Summaries can name concrete examples, but should compress inline findings into shorter issue phrases.
- Keep validation details out of the summary unless they explain why the review decision changed.

### Review summary 3: guarded-head request-changes summary

Original comment:

> Requesting changes on the guarded head `a75ff276716a79f7f1ada1e4017b6c4de839cc3c`. The prior duplicate-shadowing, public bad-chain-before-signature, accepted timestamp, proof-test, and nested-module version/CI wiring issues are improved, and the root plus nested validation passes locally.
>
> There are still blocking correctness issues: `fl=logchk` can accept a rotated-away historical issuance key, migration-in streams cannot become verified lifecycle history, and the new no-replace consumer check will fail on generated release PRs because the release tag does not exist yet. I also left two should-address items for terminal retirement handling and the public write path through the generic SDK log interface.

Wolfgang rewrite:

> I think this still needs changes. The remaining blockers are `fl=logchk` accepting rotated-away keys, migration-in not being verifiable, and the no-replace check failing on generated release PRs before the new tag exists.
>
> I also left comments on retirement handling and the generic SDK write path.

Why the original is off:

- It leads with the review UI state and guarded-head/process detail.
- It spends too much space listing what was fixed and what passed locally.
- "There are still blocking correctness issues" is heavier than Wolfgang's usual "remaining blockers are..." phrasing.
- "Should-address items" is review-framework wording; the rewrite just says there are comments on those areas.

Distilled rules:

- For follow-up summaries, lead with what still needs changes.
- Mention fixed/passing context only when it changes the decision.
- Prefer "remaining blockers are..." over heavier report-style severity phrasing.
- Avoid "should-address" as a visible category label.

### Review summary 4: final migration-in request-changes summary

Original comment:

> Requesting changes on the guarded head `fcae78f9abe8ff037fe3dc046d9332b9d042650a`. The latest revision fixes the rotated-away key check, terminal retirement handling, public generic write exposure, proof-timestamp handling, and nested-module CI/release wiring; root and nested validation passes locally.
>
> The remaining blocker is that migration-in genesis is accepted without verifying the referenced prior log/final entry, so the continuity claim is not proven. I also left two migration-in integration issues around governance checks and SDK signing.

Wolfgang rewrite:

> I think this still needs one change around migration-in. It looks like we accept `prev_ref` without verifying the referenced prior log entry, so the continuity claim isn't proven.
>
> I also left comments on the related governance check and SDK signing paths.

Why the original is off:

- It leads with guarded-head and validation-process details instead of the remaining decision.
- It uses "genesis" again where the simpler migration-in wording is clearer.
- The rewritten summary focuses on the one blocker and keeps the related comments separate.

Distilled rules:

- When only one blocker remains, say that directly instead of repeating full review-state context.
- Keep related non-blocking comments in a separate sentence after the main blocker.
