# Vertex grounding plan

Requires parent approval of SPEC.md and independent plan sign-off before code edits.

1. Update existing configured-global test to assert the complete 2.5 model endpoint. Update direct Vertex and configured-regional expectations to 2.5, with the regional endpoint retained. Existing configured-regional test exercises auth availability first, then cached-token execution. Run these deterministic local-mock tests and save their failures before changing production code.
2. Add one Vertex-grounding-only model constant in embedded_google_search.go. Use it in hasGoogleSearchAuth, executeGoogleSearch's Vertex auth hint, and executeVertexGrounding. Leave Antigravity, API-key grounding, shared auth logic, errors and formatting unchanged.
3. Run focused GoogleSearch and Vertex tests only with -count=1 -timeout=60s -coverprofile. Read per-function coverage. Existing mocks cover source formatting, provider failover and errors. Preserve unrelated dirty files.
4. Hand exact diff and logs to parent for runtime rebuild/reload and live google_search proof. Do not restart the runtime here. After live proof, complete independent implementation review and different-persona validation. Commit only after all gates pass.

Precedent: resolveVertexADCInfo uses model-specific global requirements, otherwise configured location, otherwise global. executeVertexGrounding uses vertexHost(location). This plan keeps those owning rules and introduces no routing policy.

Scope: SPEC.md acceptance only. No shared auth changes, dependencies, refactors, new provider behavior, full managed suite or unrelated file edits.
