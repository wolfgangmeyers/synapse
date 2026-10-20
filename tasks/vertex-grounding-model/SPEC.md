# Vertex Google Search grounding

## Outcome
Existing `google_search` returns a public-web summary and source links through configured Vertex credentials. Vertex grounding uses gemini-2.5-flash. Antigravity, Gemini API-key search, provider order, inputs, response formatting, and error reporting stay unchanged.

## Location and design
The search implementation owns its Vertex grounding model. Credential resolution receives that same model, so model selection and endpoint selection agree. Explicit global configuration stays global. Explicit regional configuration stays regional for this model. Missing location keeps the auth resolver's global default. General Vertex model routing stays unchanged.

## Alternatives
Keeping 3.8 does not satisfy the requested verified-model choice. Forcing global independently would preserve an accidental effect of the old model hint but ignore explicit regional configuration. Using 2.5 consistently in auth and requests is the smallest change and respects configured locations.

## Acceptance
- Mocked configured Vertex search sends googleSearch to gemini-2.5-flash, returns summary and sources, and respects global/regional location.
- Existing Antigravity and fallback tests pass unchanged except Vertex model expectations.
- Focused GoogleSearch and Vertex tests pass with instrumented coverage. Automated tests use local mocks only.
- Parent reloads the runtime, then proves existing google_search returns a public-web grounded answer. This worker does not restart it.

## Limits
No refactors, dependencies, private search, other-provider changes, or full managed test suite. Preserve unrelated main.go and codex_models_store files. No commit until validation and review gates pass.

Prior work at a6ee7c8 has no claimed retroactive dev-loop compliance.
