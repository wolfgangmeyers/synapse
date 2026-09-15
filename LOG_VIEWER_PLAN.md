# Managed-worker router log viewer plan

## Status and guardrails

Planning only. Do not edit production code until `LOG_VIEWER_SPEC.md` has independent review and approval.

Edit scope for a later implementation is limited to the viewer contract, production logger safety, the production reader wiring, the read-only route, the typed React client, the viewer UI, manifests, and their tests. Keep the current archive and RPC production changes frozen. Do not reset, stash, commit, push, deploy, or mix those dirty files into this work.

The later implementation must load the canonical `dev-loop` workflow before production-code work. It must use the preview-first path for browser and service checks. This phase makes no runtime or deployment change.

## Source-gap ledger

Confirm these gaps at the start of implementation and close only the listed viewer gaps:

| Current file | Current state | Later change |
| --- | --- | --- |
| `router-proxy/internal/transport/router_log.go` | Active-only `GET /v1/router-log`, `text/plain` response | Replace with the closed JSON query route and retained-generation reader contract. |
| `router-proxy/cmd/mailbox-router/main.go` | Production writer is created, but no reader is passed to `transport.Options` | Construct the reader from the same resolved `options.logPath` and inject it at the existing `transport.New` call. |
| `router-proxy/internal/transport/router.go` | `RouterLogReader func(context.Context) ([]byte, error)` cannot accept validated query, time, or cursor input or return a structured page | Replace it with a typed query-capable interface or function and cover it in transport tests. |
| `router-proxy/cmd/mailbox-router/log_writer.go` | Timestamp-only names, no `.1` producer, open `map[string]any` fields | Add the closed typed event/field safety contract. Keep the viewer reader independent of the writer's retention budget. |
| `web/router-ui-react/src/transport/http-client.ts` | No typed router-log client | Add the typed client and boundary validation. |
| `web/router-ui-react/src/protocol/manifest.ts` | No router-log route entry | Add exactly `{methods:["GET"],path:"/v1/router-log"}`. |
| `router/contract/manifest.json` | No router-log route entry | Add the same ordinary method/path entry. Keep request and response types outside the manifest. |
| `web/router-ui-react/src/App.tsx` | No log page or navigation | Add the `/logs` page and existing-shell navigation. |

Do not describe the current active-only route as an implemented V2 feature. Do not treat the dirty `router_log.go` file as proof that the contract is complete.

## Contract decisions to preserve

The implementation must use the contract in the specification without adding archive or RPC behavior.

### Source and fidelity

- Read only the configured production log path and its sibling eligible generations. The browser never provides a path.
- Enumerate the active file, every timestamped rotation using `YYYYMMDDTHHMMSS.nnnnnnnnnZ` plus an optional numeric collision suffix, exact legacy `.1`, and only regular non-symlink files below `32 MiB` strictly. Include every eligible generation regardless of the logger's age or rotated-size budget.
- Sort timestamped generations by timestamp, use a fixed `.1` position, and put the active file last. Break ties by basename and file identity. Keep byte order within each file.
- Snapshot each generation's name, kind, identity, length, and boundary. Read only to the captured length. A disappeared or unreadable generation may be omitted on the initial scan with a bounded warning.
- Split only on LF. Base64 the bytes before that delimiter. Preserve CR, invalid UTF-8, malformed JSON, whitespace, empty records, and a final unterminated record. Never redact or rewrite a selected record.
- Reassemble raw lines across `16 MiB` reads. Support files just under `32 MiB` and a line crossing a read boundary. Do not use a smaller scanner limit.

### Query and pages

- Use the current `GET /v1/router-log` route with one explicit query-string mapping. The typed Go and TypeScript request has exactly `query: string`, `from: string | null`, `to: string | null`, and `cursor: string | null`.
- Serialize `query` exactly once, including when it is empty. Omit nullable `from`, `to`, and `cursor` when null. Serialize each non-null value exactly once. Reject missing `query`, duplicate or unknown parameters, invalid percent encoding, decoded invalid UTF-8, invalid timestamps, invalid cursors, and invalid ranges. A present empty nullable value is invalid, not null.
- Limit the search term to 128 UTF-8 bytes and the cursor to 4 KiB. Match query bytes exactly against each raw record.
- Use an inclusive `from` and exclusive `to` RFC3339 window. Without a window, include timestamp-less and malformed-timestamp records. With either bound, include only records with a valid timestamp in range. Malformed JSON has no usable timestamp for a bounded window.
- Scan one retained snapshot and keep only the newest 500 matches in deterministic snapshot source order. The initial request returns the newest page in chronological display order. One forward cursor moves toward older pages within that fixed snapshot and fixed 500-match result set.
- React keeps the prior request-cursor and page stack locally. `Older` sends the returned cursor. `Newer` returns through already fetched pages in the same snapshot. Search, time-window changes, refresh, source replacement, or cursor invalidation clear the stack and start a new snapshot. Never mix pages from different snapshots.
- Cap the retained match set at 500 records and responses at `48 MiB`. The cap covers a single maximum raw record under `32 MiB`, whose base64 is at most `44,739,244` bytes, plus JSON. Leave a cursor for records in that fixed set that do not fit.
- Bind the authenticated opaque cursor to query, time window, generation name, file identity, byte offset, generation length, snapshot boundary, provisional trailing-line state, direction, and expiry. Invalidate it on replacement, removal, length change, snapshot change, or tail completion.

### Closed response and diagnostics

The GET request returns the closed JSON response in the specification. Use exactly this response shape:

```json
{
  "status": "complete",
  "lines": [{"bytes_base64":"..."}],
  "next_cursor": null,
  "diagnostics": []
}
```

Allow only `complete`, `partial`, and `source_unavailable` statuses. A no-match result is `complete` with an empty `lines` array. Each line object contains only `bytes_base64`.

Keep diagnostics closed and bounded: at most eight objects, each with exactly a fixed `category` and a saturated `count` from 0 through 999. Allow only `source_unavailable`, `generation_unreadable`, `generation_disappeared`, `snapshot_changed`, `tail_unterminated`, and `cursor_invalidated`, with at most one object per category. Do not return paths, raw operating-system errors, stack traces, request bodies, arbitrary details, or nested caller objects. Use fixed messages in the typed error envelope for invalid request, method not allowed, source unavailable, and invalid cursor.

## Phase sequence

### 1. Independent review and source audit

Review the specification and this plan against the current files. Verify the source-gap ledger, current bind and CORS behavior, static UI serving, manifest ownership, and existing tests. Resolve only approved document feedback. No production implementation in this phase.

### 2. Logger safety design

Inventory every production `EventLogger` call. Define the closed event names, field names, types, and bounds. Replace open maps and key-based redaction with safe typed builders or deterministic rejection. Remove session, transport, lease, process, path, prompt, body, credential, raw-error, stack, and arbitrary nested values.

Cover `log_writer.go`, `ws_lifecycle.go`, `live.go`, `http.go`, `router.go`, and all callers. Keep logger failures on stderr without recursive logging. Add tests that reject every unknown or unsafe field and verify bounds.

Do not use the viewer as a logger privacy fix. The viewer must receive only records already safe to store.

### 3. Reader and cursor implementation

Create the production reader behind a narrow injected interface. It must take the configured path at construction and never accept a browser path. Implement eligibility, stat identity, deterministic mixed ordering, a single retained snapshot, 16 MiB cross-read reassembly, provisional trailing-line handling, byte search, timestamp filtering, newest-500 selection, response packing, and authenticated cursor claims.

Keep the reader independent from the writer's configured rotated-size retention budget. Test source replacement, removal, append, tail completion, rotation races, malformed records, and partially unreadable generations.

### 4. Production wiring and route

In `router-proxy/cmd/mailbox-router/main.go`, construct `newRouterLogReader(options.logPath)` from the same `options.logPath` used by `newLogWriter`. Set `RouterLogReader: newRouterLogReader(options.logPath)` in the existing `transport.Options` literal at `transport.New`.

In `router-proxy/internal/transport/router.go`, replace `RouterLogReader func(context.Context) ([]byte, error)` with the typed query-capable interface or function. It accepts validated query, time bounds, and cursor input and returns the structured snapshot/page response.

Replace the active-only text response in `router-proxy/internal/transport/router_log.go` with the closed typed JSON response on the existing `GET /v1/router-log` route. Parse only the explicit bounded query-string mapping. Reject other methods and invalid parameters with fixed typed errors. Preserve existing no-store, no-sniff, wildcard non-credentialed CORS, correlation header, direct reachability, bind rules, and API error behavior for all other routes.

The handler may report partial source warnings outside the log stream. It must never send a synthetic line for a warning or pass through a file path or underlying error.

### 5. Manifest and typed client

Add the ordinary manifest entry `{methods:["GET"],path:"/v1/router-log"}` to both `web/router-ui-react/src/protocol/manifest.ts` and `router/contract/manifest.json`. Do not expand either manifest schema with route metadata or request/response details.

Add a typed client in the existing React transport layer. Validate every request before fetch and every response after fetch. Require exact object keys, closed status and diagnostic enums, bounded arrays, valid base64, and the `48 MiB` response cap. Keep transport bytes as base64 or decoded byte arrays. Do not parse or reserialize records. Avoid unsafe TypeScript assertions and `any`.

Add contract tests for query serialization, missing-versus-empty behavior in Go and TypeScript, duplicate and unknown parameters, invalid percent encoding, invalid UTF-8, invalid timestamps and cursors, response limits, invalid base64, fixed errors, CORS-origin calls, and manifest parity.

### 6. Dark React page

Add `/logs` navigation to the existing shell and a read-only viewer surface. Keep the route same-origin when served by the built bundle. Use the typed client with the configured router URL.

Implement:

- unrestricted initial load of the newest 500 matches;
- exact byte search and bounded `From` and `To` fields;
- `Search`, `Refresh`, and `Clear` actions;
- loading, no-match, partial-window, source-unavailable, invalid-cursor, and request-error states outside the log panel;
- `Older` and `Newer` page controls within the fixed newest-500 result set, with correct disabled states;
- a dark monospace panel with `white-space: pre`, horizontal scrolling, and no wrapping that changes line shape;
- plain text nodes only, with no Markdown, HTML, JSON table, export, download, or worker controls; and
- deterministic display for valid UTF-8, CR, and invalid byte sequences.

Use a fatal UTF-8 decode for valid records. Keep CR and whitespace in the text node. Render invalid bytes with one documented deterministic `\\xNN` fallback for display. The fallback must not alter the base64 response, search input, cursor, or source comparison.

### 7. Regression and isolated evidence

Run the focused reader, route, logger, client, and UI tests. Add focused coverage for the changed source and test the mutation-sensitive branches: line splitting, timestamp filtering, newest-match ring, cursor invalidation, diagnostic bounds, response packing, and invalid-byte rendering.

Run the isolated delivery reproduction from the specification. Use a non-8765 private router, private database, private log/home/session roots, deterministic provider, and synthetic recipient. Exercise offer acceptance without settlement through timeout, retry, and dead-letter. Compare returned base64 bytes to source bytes after removing only LF. Verify body, prompt, credential, token, process, session, and path markers are absent from logs and responses.

Then run the mandatory built-bundle test:

1. Build the actual React bundle.
2. Start the actual Go router with `--managed-ui-dist` set to that build directory and the production reader injected.
3. Request the same-origin `/logs` page from the router.
4. Exercise the bundled page's search against `GET /v1/router-log` with bounded query parameters.
5. Verify the page loads without a Vite overlay and renders the returned records.

A component test or a separately running Vite server does not satisfy this check.

Regression checks must preserve router bind and direct reachability, wildcard CORS and preflight, WebSocket and UI-live, existing authentication/provider behavior, permissions and managed controls, message/reminder behavior, static assets and export behavior, archive behavior, and RPC behavior. Archive and RPC production files remain frozen.

### 8. Review and handoff

Run Robot Review after the primary behavior and tests are stable. Validate reviewer findings independently. Run report-independent coverage checks for the raw-line and cursor branches. Re-review any material contract or behavior change on the exact changed target.

A later implementation is ready only when the source-gap ledger is closed, the typed route and manifests agree, the actual built bundle is served by the router, the isolated evidence is complete, and all preserved behavior checks pass.

## Explicit non-goals

- No archive implementation, archive route, archive retention change, or archive UI.
- No RPC protocol, RPC payload, RPC logger, or RPC behavior change.
- No new permission, role, login, cookie, token, session authority, or access-control category.
- No arbitrary file browser or path parameter.
- No client-side source selection, redaction, JSON projection, timeline synthesis, or line suppression.
- No log deletion, mutation, export, download, worker operation, delivery retry, or control action.
- No change to logger retention policy. Viewer eligibility is intentionally independent from that budget.
