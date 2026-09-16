# Managed-worker router log viewer

## Status and boundaries

Phase 1 specification only. This document authorizes no production implementation. The existing archive and RPC production changes are frozen, separate, uncommitted, and unchanged.

The viewer is a read-only Web UI for the configured production router debug log. It consumes stored log records and safely emits the required viewer records. It does not implement, inspect, or alter archive or RPC behavior. It does not read archive data, SQLite message/history data, sidecar files, process files, session files, or stderr.

The existing Web UI/router reachability remains the access boundary. This feature adds no permission, login, cookie, session authority, token, export, download, arbitrary path input, worker control, delivery retry, or log mutation.

## Current source gaps

These statements describe the current checkout. The dirty archive and RPC files are out of scope and must stay untouched.

- `router-proxy/internal/transport/router_log.go` currently serves only the active reader as `GET /v1/router-log` with `text/plain`. It does not read retained generations, search, page, or return the V2 JSON contract.
- `router-proxy/cmd/mailbox-router/main.go` creates the production `logWriter`, but does not inject a reader into `transport.Options`.
- `router-proxy/internal/transport/router.go` currently declares `RouterLogReader func(context.Context) ([]byte, error)`, which cannot accept the bounded query, time, or cursor input and cannot return the structured response.
- `router-proxy/cmd/mailbox-router/log_writer.go` has timestamp-only rotation names, no `.1` producer, and open `map[string]any` fields. It writes the generated timestamp alongside the event and caller fields.
- `web/router-ui-react/src/transport/http-client.ts` has no typed router-log client.
- `web/router-ui-react/src/protocol/manifest.ts` and `router/contract/manifest.json` omit the router-log query route.
- `web/router-ui-react/src/App.tsx` has no router log viewer surface or agent-scoped Logs action.

The implementation must close these gaps without changing unrelated transport, archive, or RPC semantics.

## Purpose

Show the actual retained router log used for debugging worker liveness, lifecycle, RPC, delivery, retry, and failure boundaries. The source is the configured `--log` or `MECHA_ROUTER_LOG` JSONL file and eligible retained generations. This is not a synthesized timeline, audit database, archive, or history replacement.

## Raw-byte fidelity

The stored record is one newline-delimited byte sequence. The viewer preserves the selected sequence exactly.

- Split records only on byte `0x0A`.
- Remove exactly that newline delimiter from the transported record. If the source uses CRLF, keep the `0x0D` byte in the record.
- Encode the remaining bytes as base64 in JSON. Do not decode, normalize, trim, validate as UTF-8, parse, reserialize, redact, reorder, deduplicate, enrich, or synthesize them.
- Preserve valid UTF-8, invalid UTF-8, malformed JSON, empty records, leading or trailing whitespace, and every carriage return.
- A final sequence without a newline is a provisional trailing record. It is eligible for selection and is transported the same way. The cursor records that provisional state.
- An empty record has an empty base64 value. It is still a record when it is delimited by a newline.

Search is a byte-substring selection over the raw record bytes. A query is valid UTF-8, so its encoded bytes are matched exactly. Search does not case-fold, parse JSON, or inspect generated fields. An empty query matches every selected record.

The line object has exactly one field:

```json
{"bytes_base64":"<base64 of the original record bytes, excluding only one LF delimiter>"}
```

The base64 value is transport encoding only. The UI must not change the bytes held by the response or client state.

## Eligible generations and deterministic snapshots

The reader receives one fixed configured active path from production startup. The browser never supplies a path.

For each query, enumerate the active file and sibling generations in the active file's directory. A candidate is eligible only when all of these are true:

- It is the active file, a timestamped rotation with the production basename and suffix `YYYYMMDDTHHMMSS.nnnnnnnnnZ` with an optional numeric collision suffix such as `-1`, or the exact legacy basename suffix `.1`.
- `Lstat` identifies a regular, non-symlink file.
- Its size is strictly less than `32 MiB` (`33,554,432` bytes).

Every eligible generation is included. Do not apply the logger's configured rotated-size budget, age budget, or a viewer-specific retention budget. A file is not dropped because it is old when it still meets the eligibility rules.

The snapshot records each generation's basename, kind, file identity, length, and the captured read boundary. File identity is the stable operating-system identity from the file stat, such as device and inode where available. It stays inside the server and sealed cursor. The API never returns a path or file identity.

Use this deterministic generation order:

1. Timestamped generations in ascending parsed rotation timestamp.
2. Legacy `.1` in its fixed position after timestamped generations.
3. The active file last.

Ties use basename, then file identity. The fixed `.1` position avoids inventing a timestamp. Physical byte order within each generation is preserved. The combination of generation order and byte offset is the snapshot's source order and is the definition of chronological order when records are malformed or timestamp-less.

Read each generation only through its captured length. A generation that disappears or cannot be opened during the initial snapshot may be omitted and reported outside the stream. A newly created generation is considered on the next refresh. No race may duplicate, invent, or transform a record.

The reader uses cross-read raw-line reassembly. It reads in `16 MiB` chunks and carries bytes across chunk boundaries. It must handle a line boundary crossing a chunk boundary and an unterminated line at the captured end. The maximum eligible file is just under `32 MiB`, so the reader must not use a scanner limit below that. It must not turn a long or malformed line into an error record.

## Search, time windows, and paging

The route is server-backed. It creates one retained snapshot, scans the complete snapshot, and applies the query to that snapshot. It retains only the newest 500 matches from that snapshot. The initial request returns the newest page in chronological display order. One forward cursor moves toward older pages within that fixed snapshot and fixed 500-match result set. The route never returns the 501st older match.

React keeps the prior request-cursor and page results locally. `Older` sends the returned cursor. `Newer` returns through that local stack and does not create a new snapshot. Search and time-window changes, refresh, source replacement, and cursor invalidation clear the stack and create a new snapshot. There are no controls that combine pages from different snapshots.

Newest means the end of the deterministic snapshot source order. This remains defined when timestamps are missing or malformed. A timestamp is used for the optional time window only.

The optional time window uses an inclusive `from` and exclusive `to` RFC3339 timestamp. Both values are UTC timestamps with at most 64 UTF-8 bytes. The request must include both fields as either strings or `null`, and `from` must be earlier than `to` when both are present.

- With no time window, timestamp-less and malformed-timestamp records can match the query and remain in source order.
- With either time bound present, a record is eligible only when its top-level `timestamp` is a valid RFC3339 timestamp and falls within the bound.
- Missing, non-string, malformed, and non-UTC-normalizable timestamps do not match a bounded time window. The raw record remains unchanged when returned by an unrestricted query.
- A malformed JSON record has no usable timestamp for a bounded time window, but it remains searchable without a time window.

The route returns at most 500 retained matches in total, with any response page containing only the records that fit the byte budget. If a page reaches the response budget, the cursor resumes at the next record in that fixed 500-match result set. A maximum-size record can therefore be returned by itself.

## Closed JSON response and query contract

The route is:

```text
GET /v1/router-log
```

The response is closed typed JSON. The request uses one explicit query-string mapping. Its typed Go and TypeScript representation has exactly four fields: `query: string`, `from: string | null`, `to: string | null`, and `cursor: string | null`.

The mapping is:

- `query` appears exactly once. Its value may be empty. A missing `query` is invalid.
- `from`, `to`, and `cursor` are nullable typed fields. A `null` field is omitted from the URL. A non-null field appears exactly once with its encoded value.
- No other parameter is accepted. Duplicate parameters, including duplicate `query`, `from`, `to`, or `cursor`, are invalid.
- Percent decoding must succeed. Invalid percent encoding and decoded values that are not valid UTF-8 are invalid.
- `query` is at most 128 UTF-8 bytes and may not contain a NUL byte. `from` and `to` are at most 64 UTF-8 bytes and must be valid RFC3339 timestamps. `cursor` is at most 4 KiB and must be a valid opaque cursor.
- A present empty `from`, `to`, or `cursor` is not the same as an omitted nullable field. It is an invalid timestamp or cursor value.
- Go and TypeScript use the same missing-versus-empty rules. Go maps an omitted nullable parameter to `nil` and a present parameter to its decoded string. TypeScript serializes `null` by omission and preserves an empty non-null string so the server rejects it.
- Unknown parameters, invalid values, and invalid ranges return the typed `invalid_request` error.

A successful response has exactly these keys:

```json
{
  "status": "complete",
  "lines": [{"bytes_base64":"eyJldmVudCI6InJvdXRlcl9zdGFydGVkIn0="}],
  "next_cursor": null,
  "diagnostics": []
}
```

Response rules:

- `status` is one of `complete`, `partial`, or `source_unavailable`.
- `lines` is an array of zero through 500 line objects. Each line object has exactly `bytes_base64`.
- `next_cursor` is an opaque cursor string or `null`.
- `diagnostics` is an array of zero through 8 closed diagnostic objects. Each object has exactly `category` and `count`.
- The encoded response body is at most `48 MiB`. This covers one raw line just under `32 MiB`, its maximum base64 representation of `44,739,244` bytes, and the JSON envelope. Page packing stops before the limit and leaves the cursor for remaining matches.
- An empty `lines` array with `status: "complete"` is the no-match result. It does not prove that an event never existed outside the retained snapshot.
- `partial` means readable records were returned with a bounded source warning. `source_unavailable` means no readable eligible generation was available. Neither status adds a log line.

The diagnostic object is closed:

```json
{"category":"generation_unreadable","count":1}
```

Allowed diagnostic categories are `source_unavailable`, `generation_unreadable`, `generation_disappeared`, `snapshot_changed`, `tail_unterminated`, and `cursor_invalidated`. `count` is an integer from 0 through 999 and saturates at 999. The array has at most eight objects, with at most one object for each category. The server does not include a path, raw error, stack, or arbitrary nested value.

The typed error body is also closed:

```json
{"error":{"code":"invalid_request","message":"The router-log request is invalid."}}
```

Allowed error codes are `invalid_request`, `method_not_allowed`, `source_unavailable`, and `invalid_cursor`. Each code has a fixed message. The handler never copies an operating-system error, path, raw error text, stack, or arbitrary details into an error response.

## Cursor contract

The cursor is opaque, authenticated, and bounded. Its sealed claims bind all of the following:

- protocol version;
- exact query bytes and both time-window bounds;
- generation name and generation kind;
- file identity;
- byte offset within that generation;
- captured generation length;
- snapshot boundary, including the complete ordered eligible-generation inventory and captured lengths;
- provisional trailing-line state; and
- the page direction and expiry policy.

The cursor never contains a filesystem path or raw log bytes. The server re-enumerates and re-stats the configured source before using it. It rejects the cursor with `invalid_cursor` when a bound source generation is replaced, removed, changes length, or changes provisional trailing-line state. Tail completion is a length/state change and invalidates the cursor. A changed eligible-generation inventory changes the snapshot boundary and invalidates the cursor. The client clears the page stack and starts a fresh query after this error.

A cursor is also invalid when its query or time window differs from the request, its claims fail authentication, its offset is outside the captured generation, or its bounded lifetime has expired. A refresh always starts a new snapshot.

## Logger privacy precondition

Exact-line delivery makes logger emission the privacy boundary. The viewer cannot safely redact a stored line. Before viewer work, replace open event maps with a closed typed event and field contract, or typed safe event builders that enforce the same contract.

The logger must reject unknown event names and fields before serialization. Every allowed field has an event-specific type and bound. Strings, numbers, arrays, and records have fixed limits. Diagnostics use fixed categories. The logger must not persist message subjects or bodies, prompts, instructions, model input or output, tool arguments or results, credentials, authorization codes, cookies, headers, bearer values, raw request or response payloads, raw errors, stacks, filesystem paths, working directories, manifests, process IDs, command lines, runtime fences, session identities, instance identities, lease identities, producer epochs, frame identities, parent-session identities, or arbitrary nested caller objects.

The audit must cover these current producers:

| Producer | Required change |
| --- | --- |
| `log_writer.go` | Replace open maps and key-based redaction with a closed event and field contract. Bound all values and use fixed logger failure categories. |
| `internal/transport/ws_lifecycle.go` | Keep reviewed state, count, version, and fixed outcomes. Remove session and transport identity. |
| `internal/transport/live.go` | Remove `lease_id`. Keep reviewed delivery state, attempt, and fixed reason. |
| `internal/transport/http.go` | Review every reminder and operational field. Keep fixed safe fields and no body or raw error. |
| `internal/transport/router.go` | Decide each startup field explicitly. Keep no process or filesystem identity. |
| All callers | Use safe event builders or receive deterministic rejection. |

Logger write failures go to stderr as bounded local diagnostics. They never create another stored event. Stderr is not a viewer source.

Phase 2 evidence must show safe stored records for router startup and shutdown, startup failure categories, sweep heartbeat and bounded counts, managed launch/restart/reset/termination outcomes, RPC operation categories and fixed outcomes, and delivery offer, receipt, retry, timeout, and dead-letter states. The viewer shows only records that were emitted and remain eligible.

## Production wiring and manifests

The production launcher must construct `newRouterLogReader(options.logPath)` from the same resolved `options.logPath` passed to `newLogWriter`. At the existing `transport.New` call in `router-proxy/cmd/mailbox-router/main.go`, it must set `RouterLogReader: newRouterLogReader(options.logPath)`. There is no second path, browser-supplied path, archive fallback, or acceptance-router fallback.

Replace the current `RouterLogReader func(context.Context) ([]byte, error)` in `router-proxy/internal/transport/router.go` with a typed query-capable interface or function. It accepts the already validated query, time bounds, and cursor and returns the structured snapshot/page response. The transport handler owns request validation. The reader owns snapshot, cursor, and source-state validation.

The server route and client route inventory must be added to both current manifests:

- `web/router-ui-react/src/protocol/manifest.ts`
- `router/contract/manifest.json`

The method and path must match exactly: `{methods:["GET"],path:"/v1/router-log"}`. Keep the ordinary manifest entry shape used by the current manifests. Do not add route metadata, a new manifest schema, or request/response details to the manifest. The closed request and response types live in Go, TypeScript, and their tests.

Add a typed React transport client. It validates the closed request and response at the boundary, checks base64 syntax and the response byte budget, rejects unknown response keys, and exposes `RouterLogQuery`, `RouterLogLine`, `RouterLogResponse`, and fixed error categories. It must not use unsafe TypeScript casts. It must not parse or rewrite line bytes.

## React viewer behavior

Add an in-context Logs action to the selected agent's existing conversation header. Clicking it opens an agent-scoped modal on the mailbox page; do not add a standalone Logs page or global navigation entry. API calls always use the configured router URL and the typed client.

The modal has:

- a dark code-editor-like surface that matches the existing application colors;
- a heading naming the selected agent, a short scope note, and the shared modal close control;
- a plain-text search field, `From` and `To` time inputs, `Search`, `Refresh`, and `Clear` controls;
- loading and error status outside the log text;
- a no-match message outside the log text;
- a scrollable log panel with a readable structured view that preserves every logged field, including message, body, and error text, plus an obvious `Raw` view for the original line; credentials and access tokens are redacted in both views;
- `Older` and `Newer` paging controls with disabled states at the ends; and
- a read-only label and no controls for workers, delivery, retries, log deletion, export, or download.

Initial modal load queries the selected agent and displays the newest 500 matching records oldest-to-newest within that page. `Older` requests the next forward cursor within the fixed newest-500 result set. React stores the request cursor and page for each response. `Newer` returns to the prior stored page without a server request. Search, time-window changes, and refresh clear the stack and start one new snapshot. Disable `Older` when `next_cursor` is null. Disable `Newer` at the initial page. A cursor error clears all pages, shows a fixed message, and starts a fresh query.

Decode each record from base64 using a fatal UTF-8 decoder, parse the JSON record, and render every selected-agent field as escaped text. The structured view may format fields for readability; the Raw view preserves the original line except for credential and access-token redactions. Do not use `innerHTML` or Markdown. Malformed or non-UTF-8 records remain available in Raw view when their agent scope can be established; otherwise they are not assigned to an agent.

## Required validation

No implementation is complete without these checks:

1. Unit and integration tests cover exact bytes with CRLF, bare CR, invalid UTF-8, malformed JSON, whitespace, empty records, an unterminated trailing record, and a line crossing the `16 MiB` read boundary.
2. Generation tests cover the active file, every timestamped generation, legacy `.1`, regular-file filtering, strict size below `32 MiB`, all eligible files beyond the logger retention budget, deterministic mixed ordering, tie-breaking, and file identity changes.
3. Query tests cover exact byte search, no time window, inclusive `from`, exclusive `to`, timestamp-less and malformed timestamps, newest 500 selection, chronological page order, response packing, and the maximum base64 line plus JSON envelope under `48 MiB`.
4. Cursor tests cover query/time-window binding, generation name, identity, offset, length, snapshot boundary, provisional trailing state, expiry, replacement, removal, length change, new generation, and tail completion invalidation.
5. Go route tests cover the explicit query-string mapping, missing-versus-empty behavior in Go and TypeScript, duplicate and unknown parameters, invalid percent encoding, invalid UTF-8, invalid timestamps and cursors, fixed errors, the closed JSON response, no path/raw-error leakage, source-unavailable and partial states, CORS, no-store headers, and read-only methods.
6. React tests cover manifest parity, typed query serialization and response validation, missing-versus-empty behavior, the agent-scoped Logs action and modal, search/loading/error/no-match/paging states, full-field structured rendering, raw-line rendering, credential/access-token redaction, and the absence of mutation/export controls.
7. One mandatory test must build the actual React bundle, start the actual Go router with `--managed-ui-dist` pointing at that built bundle, open the router-served mailbox page, select an agent, open its Logs modal, and exercise the bundled viewer against `GET /v1/router-log`. A component test or a separately running Vite server is not enough.
8. Regression checks must preserve the current bind host and port rules, direct router reachability, wildcard non-credentialed CORS and preflight behavior, WebSocket and UI-live behavior, existing authentication/provider behavior, existing permissions and managed controls, message and reminder behavior, export/static asset behavior, archive behavior, and RPC behavior. Archive and RPC production files remain frozen.

## Isolated delivery reproduction

Use existing retained safe evidence first. If it is insufficient, use an isolated router on a non-8765 loopback port with a temporary private database, log path, managed home, session root, and deterministic provider. Use a synthetic recipient and harmless opaque message marker. The marker must not appear in stored records.

Exercise a receiver that accepts an offer and does not settle it. Let existing timeout, retry, and dead-letter behavior run. Query safe event names and fixed codes through the proposed route. Compare every returned base64 value with source bytes after removing only the source LF delimiter. Check that synthetic body, prompt, credential, token, process, session, and path markers occur in neither the stored log nor the response. Remove only temporary resources.

Do not touch production or shared customer traffic. Do not deploy or modify archive or RPC production files for this viewer.

## Phase 1 acceptance bar

- The actual configured retained router log is the only evidence source.
- Selected records are byte-preserving base64 values, with only the LF delimiter excluded.
- Active, timestamped, `.1`, and all other eligible regular generations are read under the strict size rule, independent of logger retention budget.
- Search, newest-500 selection, chronological paging, time-window behavior, and cursor invalidation are deterministic and bounded.
- The route and client use closed typed JSON schemas with bounded redacted diagnostics.
- The UI is dark, navigable, searchable, readable for invalid bytes and CR, and read-only.
- Logger safety is established before viewer work.
- The actual router-served built bundle test passes, and existing bind, CORS, direct reachability, authentication, permission, export, static, archive, and RPC behavior remains unchanged.
- No implementation, archive, or RPC production file is changed in Phase 1.
