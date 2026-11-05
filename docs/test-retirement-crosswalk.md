# Orphan-test retirement crosswalk

## Scope and current contracts

No production changes. Memory spec preceded test edits. Archives are outside both nonrecursive `tests/*.test.ts` and `tests/*.test.mjs` runners. Archived imports intentionally remain historical. Claude footer tests remain untouched: broken does not prove obsolete.

Discord imports four absent implementation files: bridge.js, discord.js, main.mjs, state.js. Both Pioneer Square helper test bodies are retained unchanged in active `tests/pioneer_square_routes.test.ts`, importing current mailbox-client.ts. This retains canonical parsing, round-trip subject, exact subject/target, all ten invalid strings, malformed subject, 99/100-character name and 17/21-digit ID bounds. Other bridge assertions are historical, not guarantees of the mailbox client or router.

## Protocol assertion audit

`tests/protocol_guards_retirement.test.ts` retains every assertion in the first ten old test bodies against actual shipped UI guards. Throwing adapters contain no validation logic; createEnvelope is only a fixed fixture builder. Coverage includes Sentinel key closure; lifecycle description/status/epoch and ACK; retired Sentinel frames; worker/server activity state/count/lease bounds; full arrival body versus preview/lease/date bounds; incarnation UUID; delivery/injection/ACK/NACK arrays; per-item cardinality; registration sync and attachment-bearing conversation events.

The eleventh test's generated 21-character NanoID assertion is superseded: current Go generates 32 lowercase hex characters. New `TestOrphanProtocolProductionIDsAreUniqueHexIDs` checks 100 actual IDs for current format, uniqueness and envelope validity. UUID/source-kind/lease assertions remain in the final TS guard test. No NanoID guarantee remains.

Existing Go envelope bounds, injected clock/ID, explicit per-item envelopes, client nested/range tests and existing UI protocol-guards tests are also executed. They are not cited as blanket substitutes for unmatched assertions.

## Logger assertion audit

| Old assertion block | Disposition of every assertion |
|---|---|
| Declared mailbox events | Exact message_enqueued/created/deduped duplicate schema is unsupported: absent from Go allowlist. New TestOrphanLoggerCurrentCorrelationAndPrivacy checks exact current delivery_timed_out event/time/correlation, rejected body and nested model, and no writes on rejection. |
| Correlated model controls | All five model_control event names and exact control/source/target/lease/provider/model projections are unsupported historical schemas. None is accepted by current logger. Privacy/correlation assertion class is ported to current delivery_timed_out above, not invented equivalent controls. Managed logging allows bounded redacted text: no blanket no-body promise. |
| Size rotation | Custom 2048-byte threshold, .1/no .2 names and 256-byte slack are historical JS details. Existing Go TestEventLogBoundsEachRetainedFile, TestEventLogRotatesAfterOneDay, TestEventLogRefreshesGenerationTimestampAfterRotation, TestEventLogDropsActiveRecordsOlderThanRetention and TestEventLogRemovesRotationsOlderThanRetention cover actual 16 MiB files, 24-hour rotation and 48-hour expiry. Combined rotated budget is 16 MiB, not a filename-count rule. New below-limit test verifies newest record remains readable. |
| Failed rotation | JS fail-open unbounded growth, .1-directory failure, exactly one in-band log_rotation_failed and exact 41-record count are superseded. Go rotation errors return before append; eventLogger reports stderr nonrecursively. Existing TestEventLoggerReportsWriteFailureWithoutRecursing covers supported failure reporting. No unbounded growth restored. |
| Missing/below-limit | Exported rotateJsonlLog boolean API and custom maxBytes 0/-1 checks are retired: no Go API counterpart. New TestOrphanLoggerBelowLimitKeepsNewestRecord checks creation, no below-limit rotation, both records, newest last and actual 16 MiB constant. |

## Memory and evidence boundaries

All eleven capture TS tests retain duplicate MJS assertions. Deliberately smaller local policy limits remain boundary overrides. The fixture contains complete real Go capture policy and MEM-001 compares the endpoint. Extractor execution is stubbed, no model calls or production evaluator changes.

The 17 filesystem catalog contracts are distinct and unsupported without an installed implementation: hydration/order/path omission; predicates/graph budget; immediate memory/deferred writes; index rename; coalescing; retry; touch/frontmatter; store/CAS; mutation serialization; invalid queue admission; rebuild; drift; batch rollback; index rollback/retry; symlink destination; tags; symlink ancestor. None is claimed by memory E2E.

Exactly two actual E2E cases: MEM-001 browser → real proxy → Go router/SQLite policy and 404 for lookup/expand/touch/apply/rebuild; MEM-002 production lifecycle with real destination rejection prevents extract/apply, emits safe diagnostics, rejects compaction/shutdown, and permits subsequent browser health. Not full Pi continuation, optional persistence, successful CAS, or 100% boundary coverage of eleven supporting TS tests.

Claude hook payloads have no current repository consumer (search found none). All payload bytes preserved; README alone now labels historical evidence, not replay oracle. Archive is finite repository-owned evidence, not runtime accumulation.

## Parent handoff

CI removes only dead explicit bridge step and both bridge-only path filters. Root scripts untouched. Dependency owner: retain `tsx --test tests/*.test.ts` in root runner; extension already uses this glob. No additional script changes needed if parent retains that glob.

Requested parent-owned INDEX row (not edited here):

`| SPEC-MEMORY-001 | [Memory capture](memory-capture.spec.md) | Mailbox | Active | tests/e2e/memory-capture.spec.ts |`

Harness dependency: memory suite calls startIsolatedRouter(managed:false), startRouterProxy only. Dirty harness hunks concern optional codexCredentialPath/embedded branches and setupE2EHarness, none used here. Committed HEAD harness replay from /tmp/orphan-committed-harness also passed both E2E cases (20.6s); only snapshot imports were adjusted to resolve checkout modules. No harness edits made.

## Complete old-test inventory

Each original line range includes all assertions, including loops/callbacks and multiline expected values. Historical dispositions are explicit unsupported gaps, not replacement coverage. Files remain byte-identical and auditable.

### discord_gateway_bridge.test.mjs

| # | Original test and lines | All-assertion disposition |
|---|---|---|
| 1 | inbound messages go directly to supervisor with an UTF-8 bounded untrusted frame (L38–55) | Historical absent bridge implementation; every assertion unsupported. |
| 2 | inbound outbox survives router failure without retaining a reply route (L56–76) | Historical absent bridge implementation; every assertion unsupported. |
| 3 | every normal supervisor message for this identity posts to its one channel (L77–91) | Historical absent bridge implementation; every assertion unsupported. |
| 4 | configured bridge identities do not share sender authorization or channels (L92–107) | Historical absent bridge implementation; every assertion unsupported. |
| 5 | concurrent flush requests serialize Discord sends (L108–119) | Historical absent bridge implementation; every assertion unsupported. |
| 6 | Gateway closes and reconnects after a missed heartbeat acknowledgement (L120–151) | Historical absent bridge implementation; every assertion unsupported. |
| 7 | close waits for an active outbound send before state is released (L152–164) | Historical absent bridge implementation; every assertion unsupported. |
| 8 | pending capacity, retention, retry, and terminal cleanup are bounded (L165–175) | Historical absent bridge implementation; every assertion unsupported. |
| 9 | legacy pending records migrate without reply routing (L176–196) | Historical absent bridge implementation; every assertion unsupported. |
| 10 | Pioneer Square route helpers round-trip canonical routes and targets (L197–205) | Active route TS suite, unchanged body. |
| 11 | Pioneer Square route helpers reject missing hash, whitespace, uppercase, delimiters, and wrong prefixes (L206–230) | Active route TS suite, unchanged body. |
| 12 | outbound supervisor messages require a route and never admit invalid messages (L231–248) | Historical absent bridge implementation; every assertion unsupported. |
| 13 | valid supervisor route is the outbound Discord destination (L249–259) | Historical absent bridge implementation; every assertion unsupported. |
| 14 | a pending route incident retries on a later rejected route (L260–288) | Historical absent bridge implementation; every assertion unsupported. |
| 15 | schema v2 migration preserves destinations and terminal-fails legacy pending rows (L289–319) | Historical absent bridge implementation; every assertion unsupported. |
| 16 | pending outbound destination survives close, reopen, and retry (L320–333) | Historical absent bridge implementation; every assertion unsupported. |
| 17 | runtime config requires an explicit unique state path and validates agentId (L334–353) | Historical absent bridge implementation; every assertion unsupported. |

### jsonl_logger.test.mjs

| # | Original test and lines | All-assertion disposition |
|---|---|---|
| 1 | JSONL logger persists declared mailbox events and rejects body-bearing fields (L11–39) | Logger audit row 1 above. |
| 2 | JSONL logger persists correlated model-control events without payload content (L40–141) | Logger audit row 2 above. |
| 3 | JSONL logger rotates at the size limit and keeps on-disk usage bounded (L142–178) | Logger audit row 3 above. |
| 4 | a rotation that cannot complete still records the event (L179–215) | Logger audit row 4 above. |
| 5 | JSONL log rotation is a no-op below the limit and for a missing log (L216–232) | Logger audit row 5 above. |

### protocol.test.mjs

| # | Original test and lines | All-assertion disposition |
|---|---|---|
| 1 | Sentinel descriptor accepts only caller-selected model metadata (L8–14) | Every assertion ported to current UI guard suite. |
| 2 | enforces lifecycle metadata limits without logging source text (L15–34) | Every assertion ported to current UI guard suite. |
| 3 | the shared protocol rejects retired Sentinel lifecycle frames (L35–41) | Every assertion ported to current UI guard suite. |
| 4 | activity frames carry lease-scoped worker state (L42–80) | Every assertion ported to current UI guard suite. |
| 5 | message arrival frames carry the full body separately from the capped preview (L81–119) | Every assertion ported to current UI guard suite. |
| 6 | correlated delivery frames require a valid router-generated incarnation (L120–163) | Every assertion ported to current UI guard suite. |
| 7 | delivery_items validate on delivery, injection, ACK, and NACK frames (L164–180) | Every assertion ported to current UI guard suite. |
| 8 | multi-item delivery frames require matching explicit per-item envelopes (L181–196) | Every assertion ported to current UI guard suite. |
| 9 | malformed delivery_items are rejected on delivery receipts and messages (L197–220) | Every assertion ported to current UI guard suite. |
| 10 | conversation sync is additive to registration and has a canonical server event (L221–262) | Every assertion ported to current UI guard suite. |
| 11 | new envelopes use URL-safe NanoIDs while existing UUID IDs remain valid (L263–283) | Actual Go hex IDs replace NanoID assertion; all remaining assertions ported. |

### memory_capture.test.mjs

| # | Original test and lines | All-assertion disposition |
|---|---|---|
| 1 | capture lifecycle serializes a valid tagged linked chunk batch before apply (L11–16) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 2 | ordinary successful Pi tool results support capture evidence without custom details (L17–22) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 3 | sanitizer excludes custom, compaction, thinking, images, and credential payloads (L23–27) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 4 | validator rejects stale, unverified, noisy, malformed, and unlinked capture output before apply (L28–41) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 5 | bounded destination context rejects duplicate creates and mismatched update CAS bases before apply (L42–49) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 6 | noisy source produces no extractor or write (L50–55) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 7 | capture lifecycle emits bounded outcome diagnostics without source text (L56–70) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 8 | configured extractor permits sixty seconds and passes cancellation to its model process (L71–79) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 9 | destination lookup failure clears lifecycle timers (L80–92) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 10 | canceled compaction cannot apply a completed extraction (L93–100) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |
| 11 | final shutdown deadline includes time queued behind compaction (L101–104) | Every assertion retained in same-named active TS test; policy fixture replaces dead import. |

### memory_catalog.test.mjs

| # | Original test and lines | All-assertion disposition |
|---|---|---|
| 1 | startup hydration is ordered, excludes noncanonical files, and does not disclose paths (L26–37) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 2 | lookup and expansion use hydrated records with bounded predicates and graph traversal (L38–48) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 3 | apply changes memory immediately and defers canonical record and index writes until close (L49–61) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 4 | captured index labels follow a record title update after each flush (L62–74) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 5 | separate mutations coalesce into one deferred index flush (L75–87) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 6 | a failed deferred flush gets one safe retry and preserves pending state for shutdown (L88–97) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 7 | touch updates recency in memory immediately and preserves legacy frontmatter on shutdown flush (L98–109) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 8 | CAS and selected-store validation use current in-memory state (L110–118) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 9 | conflicting applies serialize through the mutation gate against current in-memory state (L119–130) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 10 | invalid apply input never enters the mutation queue (L131–139) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 11 | explicit rebuild is the only path that observes an out-of-band canonical edit (L140–149) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 12 | flush rejects out-of-band drift without overwriting it (L150–159) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 13 | record write failures roll back the complete batch before reporting failure (L160–170) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 14 | index write failures restore records and preserve the pending batch for a later flush (L171–181) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 15 | flush rejects symlinked destinations before mutating memory or disk (L182–190) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 16 | tagged records retain canonical tags through deferred capture (L191–199) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |
| 17 | configured KB roots reached through symlinked ancestors reject flushes (L200–207) | Distinct historical optional filesystem contract; every assertion unsupported without installed catalog. |

## Pre-move byte manifest

SHA-256 verified after move. README is sole intentional change.

| Original path | SHA-256 |
|---|---|
| `tests/discord_gateway_bridge.test.mjs` | `27d18faaf8bdba9da58a599ddd32897e755465374660a418ecc65ab63a252471` |
| `tests/jsonl_logger.test.mjs` | `1963777aace8fd8eea8d1d1377ff978278321f3d934dcbd4a2ddfc60986386d3` |
| `tests/protocol.test.mjs` | `2861ba11d18e66fda9c29e7fb6d11a021da8fc965cb1c81cba060abf209d5be3` |
| `tests/memory_capture.test.mjs` | `bb014f70ea6ff3335543eb58391736276db4e0f0b43510af5e27895e0d348bc8` |
| `tests/memory_catalog.test.mjs` | `b2c67d2159c755bdd368f486acc406445a2400219499bf2bdcdf16b4e419a100` |
| `tests/fixtures/claude-hooks/probe2.jsonl` | `e7cd155d6a7178e1cab73ff514f5d4e72664b10e2d9823529aa7c74308d13c6a` |
| `tests/fixtures/claude-hooks/README.md` | `89d0401d8c20f405a70e3ee0ebfee4498eb7a5585bf3270d9004eff01f6d3978` |
| `tests/fixtures/claude-hooks/probe1.jsonl` | `5c8aafdd9dfad4efee54901df91320567c4fee080d59e060b66a65e7f932dcef` |
| `tests/fixtures/claude-hooks/probe9-multihook.jsonl` | `cd4fd349a5053857f28ce23ec26a5686bde01141e148f2bf8a78f391966ae7ba` |
| `tests/fixtures/claude-hooks/probe12-blocked-ask.jsonl` | `29e4c38c8aca6374cb9d0c44f3911a687ec6d2168209d952b4187b0f1d2e9686` |
| `tests/fixtures/claude-hooks/probe13-fast-interrupt.jsonl` | `7e0dbc8c5a141ce8b72f25d48bfe6fa07b8b90223d868069d7267f4c8be5304a` |
| `tests/fixtures/claude-hooks/probe8-postcompact.jsonl` | `e4affd6a302589c8dd122e6b61ad9171e14a418ce1e4a68b0f93bdc37369d268` |
| `tests/fixtures/claude-hooks/probe12-blocked-g6.jsonl` | `b7683b6f195adf75e66819adf0b5237cce3370263d6ecf7afedc39935491eda5` |
| `tests/fixtures/claude-hooks/probe6-interrupt.jsonl` | `742b45230502293a6dff06c7fd3ca8b7aee301f7ee1151c94828ecd6ab1ff938` |
| `tests/fixtures/claude-hooks/probe10-transcript-evidence.jsonl` | `f53463c5a10ed78cec13e8a9855be4b307916b79e151f842c99fe4da9d50e795` |
| `tests/fixtures/claude-hooks/probe11-interrupt-evidence.jsonl` | `3d0769474ce270fd90753ebcaa4cb037206b73f13f1f98ff67241d23243f7ee4` |
| `tests/fixtures/claude-hooks/probe7-compact.jsonl` | `28e7b22bf9a24208dae9dcc99d0efd84c6a7cfc1c06f7f0c3ff6750cc0519979` |
| `tests/fixtures/claude-hooks/probe3-blocking.jsonl` | `bf0d9258a04722ef4728924d4601cc2d24d2df601002fc828820f52f0e4680c2` |
| `tests/fixtures/claude-hooks/probe5-transcript-inflight.jsonl` | `731f573c904dc7e8e6e7c95f4c2fba16b460542f92960f875f1eeb8dea6bba08` |

## Final executed verification

- Active TS suites: `tsx --test tests/pioneer_square_routes.test.ts tests/memory_capture.test.ts tests/protocol_guards_retirement.test.ts`: **24 passed**, zero failed/skipped (11 capture, 2 routes, 11 protocol).
- Playwright discovery: **2 tests** in memory-capture.spec.ts.
- Actual memory E2E: **2 passed (16.8s)** with repository harness; committed-HEAD harness snapshot control: **2 passed (20.6s)**. The initial run found a test-only incorrect health expectation (`ready` versus actual `ok`); fixed expectation, no timeout weakening. JSON fixture import was changed to fs loading for both TS and Playwright loaders.
- Existing UI `tests/unit/protocol-guards.test.ts`: **18 passed**, 1 file passed (5.54s), no UI build.
- Final bounded Go run: `GOMAXPROCS=2 GOFLAGS=-p=2 go -C router-proxy test ./internal/protocol ./internal/transport ./cmd/mailbox-router -run 'TestOrphan|TestValidateClientFrame|TestValidateEnvelope|TestEnvelopeUses|TestMultiMessageFrame|TestMessageFrameUses|TestValidateEvent|TestLogWriter|TestTypedEventLogger|TestEventLog' -count=1`: **all 3 packages passed**, exit 0 (0.031s / 0.149s / 0.265s).
- Post-move SHA verification: **18 identical files** (5 tests + 13 payloads); README alone changed. Diff whitespace check clean.
- Servers: both successful E2E runs completed afterAll proxy/router cleanup; no memory-suite worker or mailbox-router remained. Other owners' scroll workers and longstanding orphan proxy processes were not killed. Shared harness proxy cleanup joins its launcher, not an independently verified downstream `go run` child; no claim that all workstation proxy processes are gone.
- No dependency installs, node_modules mutations, full UI build, external/model calls, index/ref/stage/commit actions. No production, harness, scroll or provider edits.
