# Execution evidence

Implementation only on main, base fb45e2e45ec5179d6aedc26aed2e329e4457a6f9. SPEC approval and round-one plan sign-off were supplied by parent (robot-review2ba7befd, pr-validate78d4ad35). Canonical workflow fetched to /tmp/google-search-vertex-dev-loop.txt.

Outcome: Vertex grounding uses one gemini-2.5-flash constant for availability auth, execution auth, and request model. Explicit regional configuration remains regional. No other provider or shared routing changes.

## Tests

Working directory: /Users/wolfgang.meyers/code/mecha-developer/router-proxy.

RED command:

```sh
go test ./internal/managed -run 'TestEmbeddedGoogleSearch_(VertexIntegration|ConfiguredVertexGlobalGrounding|ConfiguredVertexRegionalLocationUsesModelEndpoint)$' -count=1 -timeout=60s -v
```

All three failed on the old model/endpoint, exit 1. Output: /tmp/vertex-grounding-model/red.log. Exit record: /tmp/vertex-grounding-model/red.exit.

GREEN command:

```sh
go test ./internal/managed -run 'GoogleSearch|Vertex' -count=1 -timeout=60s -covermode=atomic -coverprofile=/tmp/vertex-grounding-model/coverage.out -v
```

All 12 selected tests passed, exit 0. Output: /tmp/vertex-grounding-model/green.log. Exit record: /tmp/vertex-grounding-model/green.exit.

Coverage skill loaded. Instrumented report: /tmp/vertex-grounding-model/coverage.out. Full per-function output from go tool cover -func: /tmp/vertex-grounding-model/coverage-functions.txt. The selected tests cover 2.3% of the whole managed package; this is not whole-suite coverage.

Search per-function statement coverage:

| Function | Coverage |
| --- | --- |
| embeddedGoogleSearchSchema | 100.0% |
| hasGoogleSearchAuth | 88.9% |
| embeddedGoogleSearchTool | 0.0% |
| executeGoogleSearch | 69.0% |
| formatGroundingResponse | 82.1% |
| executeAntigravityGrounding | 82.9% |
| executeVertexGrounding | 74.2% |
| executeGeminiAPIKeyGrounding | 0.0% |

Related auth: vertexHost 100.0%, vertexModelRequiresGlobal 100.0%, resolveVertexADCInfo 88.1%, embeddedVertexAuth 60.0%, exchangeAuthorizedUserToken 72.7%. Service-account exchange/JWT are 0.0%; streamEmbeddedVertex is 42.9% in this focused selection.

The focused run does not exercise the tool wrapper, API-key execution, service-account exchange, or every error branch. These are unchanged paths, not proof of full coverage. No tests added outside the approved plan.

Coverage analysis: /tmp/vertex-grounding-model/coverage-review.md. No changed-behavior blockers or required missing tests found. All three changed executable call sites ran. The parent delegated coverage analysis to the analyst. The analyst performed it directly and launched no further subagents. The optional omitted-location search integration test was intentionally skipped: resolver default coverage passes, that behavior is untouched, and this change adds no new branch.

The full managed suite timed out twice earlier and was not repeated. No full-suite pass or coverage is claimed.

## Requirements mapping

- Vertex 2.5 request, googleSearch payload, summary and sources, global endpoint: TestEmbeddedGoogleSearch_ConfiguredVertexGlobalGrounding.
- Explicit regional endpoint and auth availability followed by cached-token execution: TestEmbeddedGoogleSearch_ConfiguredVertexRegionalLocationUsesModelEndpoint.
- Direct Vertex request model, regional host, summary and sources: TestEmbeddedGoogleSearch_VertexIntegration.
- Antigravity request and formatting unchanged: TestEmbeddedGoogleSearch_AntigravityIntegration.
- Existing fallback error, gating and schema: TestEmbeddedGoogleSearch_CredentialFailover, TestEmbeddedGoogleSearch_ToolGating, TestEmbeddedGoogleSearch_ToolSchema. The existing failover test checks an Antigravity error, not successful Vertex fallback.
- Shared routing/defaults unchanged: TestVertexURLHostConstruction, TestVertexAuthLocationDefaulting, and the remaining selected Vertex tests.
- Live public-web google_search answer: parent observed actual functions.google_search success after reload. The answer identified https://go.dev/ as the official Go site and described Go as statically typed, compiled, and designed at Google. Six reported sources included two go.dev entries. /tmp/vertex-grounding-model/live-proof.md records parent-provided evidence, not a call executed or independently inspected by the recorder.

## Target and preservation

Exact two-file diff: /tmp/vertex-grounding-model/implementation.diff (SHA-1 8b53cb127d1aed4588d780384863a970deb53cfc). Production diff: 5 additions, 4 deletions. Test diff: 3 additions, 4 deletions (includes removal of unused strings import). git diff --check passed.

Unrelated main.go and codex_models_store*.go hashes still match /tmp/vertex-grounding-model/preserved.sha. Existing tasks preserved; only this ledger added. Baseline tracked diff: /tmp/vertex-grounding-model/baseline.diff.

## Runtime evidence

Parent reports the runtime was faithfully rebuilt/reloaded and the online backup recovered with integrity verified. Saved verification: /tmp/vertex-grounding-model/runtime-after.txt (2026-10-20T12:48:22.703175-07:00), listener PID 39764, /v1/health HTTP 200 with readiness/status ok. Runtime reports base fb45e2e45ec5179d6aedc26aed2e329e4457a6f9 with a dirty source tree. The saved two-file diff still matches the SHA-1 above.

The live-proof recorder compared /tmp/vertex-router-backup.LIsTeP/process-before.txt with runtime-after.txt. Saved executable commands and flags are byte-identical (1,521 bytes each), excluding process metadata. This establishes equality of the saved captures only.

## Completed review

Two total rounds. Replacement reviewers completed existing rounds, not new rounds.

- Round one, plan: reviewer2ba7befd used simplicity-scope; validator78d4ad35 used requirements-contract. Both clean, with parent-supplied signoff before implementation.
- Round two, implementation: reviewer6f80c8d5 used simplicity-scope. Clean signoff, no blocking or should-address findings. Report: /tmp/vertex-grounding-model/implementation-review.md.
- Round two, different-persona validation: validatorf8bc17ef used requirements-contract. Clean, implementation signoff stands. Report: /tmp/vertex-grounding-model/implementation-validation.md. Prior validator5de96 stopped and supplied no signoff.

Round-two reports target the immutable two-code-file diff above, based on fb45e2e45ec5179d6aedc26aed2e329e4457a6f9. The retained nonblocking clarification was coverage attribution, corrected above. Live proof remains parent-provided, not an independent live test by the reviewer or validator.

Packaging changes only this ledger before staging the two code files and three task records. No source edits, test reruns, builds or restarts during packaging. Prior red/green evidence, focused coverage and full-suite timeout limits remain above.
