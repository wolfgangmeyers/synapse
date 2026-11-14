# Client-Server Contract Audit: Router Backend & React UI

Contract audit across `router-proxy` (Go backend) and `web/router-ui-react` (TypeScript frontend).

**Historical snapshot:** This report records observations and proposals from an earlier audit, not a verified description of the current system. Paths, callers, and line numbers may have changed. Except where explicitly noted as checked against the current checkout, claims and recommendations are hypotheses; an owner must verify current behavior, consumers, and compatibility before acting. No deployment status is implied.

---

## 1. Redundant Endpoints & Overlapping Protocols

### The Dual-Timeline Split: `/v1/conversation` vs `/v1/history?view=complete` vs `/v1/conversations`
Three separate endpoints serve message and activity streams. Two are actively fetched simultaneously in the same UI view.

- **`/v1/conversation?agent_id=...` (Unified Timeline)**: Returns chronological `events: ConversationTimelineEvent[]` with `seq`, `kind` (`message`, `tool`, `context_reset`), `role`, `content`, `metadata`, `created_at` (epoch ms). Queried by `useConversation` and `MessageList`.
- **`/v1/history?agent_id=...&view=complete` (Managed Complete History)**: Returns `entries: ManagedHistoryEntry[]` with `id`, `agent_id`, `at` (ISO timestamp), `level`, `category`, `presentation`, `tool_input`. Queried on mount and scroll by `App.tsx` and `use-router-session.ts`.
- **`/v1/conversations?owner=...&peer=...` (Legacy Peer-to-Peer)**: Returns `messages: ConversationMessage[]` with sequence numbers and attachments. Still called as fallback in `useConversation`.

**Historical hypothesis, not a verified failure**: The audit suspected overlapping timeline and complete-history retrieval and duplicate representations. Verify current call paths, actual overlap, reconciliation, and intended ownership before claiming duplication or a failure.

**Candidate direction (unverified)**: An owner should establish endpoint consumers and timeline ownership before considering consolidation or deprecation, including quiet history and legacy clients.

---

### Duplicate Agent Lists: `/v1/agents` vs `/v1/managed-agents`
- **`/v1/agents`**: Returns tmux windows and connected WebSocket agents. Includes model catalogs, leases, subagent summaries.
- **`/v1/managed-agents`**: Returns router-managed agents from SQLite (`internal/store/managed.go`).
- **Frontend reconciliation**: `agent-projection.ts` (`mergeAgentProjections`) runs an O(N^2) zip of `/v1/agents` and `/v1/managed-agents`, fabricating unified `Agent` objects.
- **WebSocket duplication**: `/v1/ui-live` streams `managed_agents_snapshot` and `managed_agent_delta`, while `/v1/agents` (WebSocket) broadcasts `agent_activity` and `agent_model_catalog`.

**Direction**: Expose a single `/v1/agents` snapshot containing both managed and unmanaged agents with a discriminatory `source: "router-managed" | "tmux"` field. Eliminate dual polling.

---

### Dual WebSocket Channels: `/v1/agents` vs `/v1/ui-live`
The frontend maintains two separate WebSocket connections to the router:
1. **`/v1/agents` (`websocket-client.ts`)**: Mailbox V2 agent socket. UI registers as a client with role `user_interaction` or surrogate peer. Handles lease renewal, heartbeats, arrivals, delivery ACKs.
2. **`/v1/ui-live` (`ui-live-client.ts`)**: Monolithic projection socket. Streams `managed_agents_snapshot`, `managed_children_snapshot`, `history_entry`, `provider_auth_prompt`.

**Problem**: The browser acts as an agent runtime on `/v1/agents` while simultaneously acting as an administrative dashboard on `/v1/ui-live`. Frame losses or reconnect desyncs on one channel leave the other in a conflicting state.

**Direction**: Unify dashboard subscriptions into a single WebSocket connection. If Mailbox V2 wire protocol remains on `/v1/agents` for workers, the Web UI should only consume an administrative live socket (`/v1/ui-live` or `/v1/events`).

---

## 2. Type & Schema Mismatches

### Attachment Data Model Inconsistencies
The attachment schema mutates across every boundary layer:

| Boundary | Shape |
|---|---|
| Frontend Composer Send (`App.tsx` -> `POST /v1/messages`) | `{ attachments: [{ type: "image", data_url: "data:image/png;base64,...", name?: string }] }` |
| Backend HTTP Parse (`http.go:parseMessage`) | Checks `type == "image"`, parses regex `data:(image/[a-zA-Z0-9.+-]+);base64,(.+)` from `data_url` |
| Backend Store Output (`pendingAttachments` / DB) | `{ id: "att-...", url: "/v1/attachments/att-...", mime_type: "image/png", size_bytes: 1234, name: "..." }` |
| Backend Tool Output (`embedded_send_image.go`) | Accepts both `{ path: "/..." }` AND `{ data_url: "data:..." }` |
| Backend SQLite DB (`conversation_messages.attachments_json`) | Stored as JSON string; unmarshaled either as `[]MailAttachment{Path}` or `[]Attachment{URL, ID}` |
| Timeline Metadata (`conversation_timeline.go`) | Packed into `metadata.attachments` or `metadata.images` |
| Frontend Reader (`AttachmentGallery.tsx`, `MessageList.tsx`) | Checks `meta.attachments ?? meta.images`, handles `url`, `id`, `data_url`, and relative `/v1/attachments/` prefixes |

**Hacks present**:
- `attachment-urls.ts`: Frontend must rewrite relative `/v1/attachments/...` URLs to absolute URLs using a supplied router URL because server emits origin-less root-relative paths. `attachment-urls.ts` takes `routerUrl`; it does not call `activeRouterUrl()` itself.
- `MessageList.tsx` line 199: `const rawAttachments = meta.attachments ?? (event as any).attachments ?? meta.images;` — catches three conflicting backend naming conventions.

**Direction**: Standardize on `{ id, url, mime_type, size_bytes, name }` everywhere on read, and `{ data_base64, mime_type, name }` on write. Eliminate `data_url` string parsing with regular expressions.

---

### Sequence & Identification Keys: `seq` vs `id` vs `sequence` vs `conversation_sequence`
The sequence tracking fields are completely fragmented:

- `/v1/conversations`: Returns `sequence: number` and `high_water_sequence: number`.
- `/v1/conversation` (timeline): Returns `seq: number` and `next_before_seq: number | null`.
- `/v1/messages`: Returns `enqueue_sequence: number`.
- SQLite schema: Tables use `conversation_sequence INTEGER PRIMARY KEY AUTOINCREMENT`.
- Frontend `HistoryMessage`: Expects `sequence?: number`.
- Frontend `ConversationTimelineEvent`: Expects `seq: number`.
- Frontend ad-hoc mapping in `use-conversation.ts`: Maps `e.seq` into `message.sequence = e.seq`.

**Direction**: Pick `seq` or `sequence` and standardize across all timeline, message, and conversation endpoints.

---

### Timestamps: Unix Milliseconds vs ISO-8601 Strings
- `/v1/conversation`: Emits `created_at: number` (epoch milliseconds: `1740000000000`).
- `/v1/messages`: Emits `created_at: string` (ISO-8601 UTC: `"2025-10-28T05:30:00.000Z"`).
- `/v1/history`: Emits `at: string` (ISO-8601 UTC).
- `/v1/ui-live`: Emits `generated_at: string` (ISO-8601 UTC).
- `/v1/agents`: Emits `connected_at: string`, `lease_expires_at: string`.
- Frontend parser: Requires ugly guards: `typeof item.created_at === "number"` vs `isRouterUtcTimestamp(...)`.

**Direction**: Use ISO-8601 UTC strings (`YYYY-MM-DDTHH:MM:SS.sssZ`) consistently across all JSON payloads, or standardize all event timeline timestamps on integer epoch milliseconds.

---

### MCP Auth Field Naming: `auth_type` vs `auth_mode`
- `router-proxy/internal/transport/mcp_http.go:399`:
  ```go
  authType, _ := rawString(payload, "auth_type", false)
  if authType == "" {
      authType, _ = rawString(payload, "auth_mode", false)
  }
  ```
- `web/router-ui-react/src/transport/http-client.ts:515`:
  ```typescript
  export interface AddMCPServerRequest {
    auth_type?: "none" | "bearer" | "oauth";
    auth_mode?: "none" | "bearer" | "oauth";
  }
  ```
Backend and frontend maintain redundant fields to paper over a naming discrepancy.

**Direction**: Remove `auth_mode`. Standardize on `auth_type`.

---

## 3. Hidden Hacks & Overengineering

### 1. The Idempotency UUID Requirement on Local Calls
Some managed operations require an idempotency key. The original audit claimed every `PUT` / `POST` to managed worker routes (`/v1/managed-agents`, `/v1/settings/default-model`, `/v1/settings/context-thresholds`, etc.) requires an `X-Request-ID` header matching a canonical UUID regex (`[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}`).
- Some backend handlers reject missing or invalid IDs with HTTP 400 `INVALID_REQUEST_ID`.
- `managedRequest()` generates `secureUuid()` for non-GET requests; this does not establish that every button or route uses it.
- The claim that this layer provides no useful safety is unverified. The backend records and replays managed operations by request ID (for example in `managed_http.go`); an owner must assess retry, conflict, and client contracts before changing it.

### 2. Manual URL Path Regex Parsers
Instead of using standard routing (like `net/http` ServeMux patterns in Go 1.22+ or chi/gorilla), `managed_http.go` and `agent_monitors_http.go` contain dozens of handwritten string slicing functions:
- `managedChildrenPath(path)`
- `managedResetPath(path)`
- `managedTerminatePath(path)`
- `managedRestartPath(path)`
- `managedWorkflowStatePath(path)`
- `managedGoalPath(path)`
- `managedInlineToolsPath(path)`
- `managedScratchpadPath(path)`
- `managedAllowInlineToolsPOSTPath(path)`
- `managedAgentPATCHPath(path)`
- `managedInterruptPath(path)`
- `managedSubagentStopPath(path)`

Any variation in path structure (trailing slashes, `/v1/` prefix vs root) requires manual boilerplate and caused bugs (e.g. `managedInterruptPath` manually stripping `/v1/`).

### 3. Raw `fetch()` Invocations Bypassing HTTP Client
In the current `web/router-ui-react/src/transport/http-client.ts` (line numbers may change):
```typescript
async interruptManagedAgent(agentId: string): Promise<void> {
  const doFetch = fetchImpl ?? fetch;
  await doFetch(`${this.baseUrl ?? base}/v1/managed-agents/${encodeURIComponent(agentId)}/interrupt`, { method: "POST" });
},
async stopManagedSubagent(agentId: string, childId: string): Promise<void> {
  const doFetch = fetchImpl ?? fetch;
  await doFetch(`${this.baseUrl ?? base}/v1/managed-agents/${encodeURIComponent(agentId)}/subagents/${encodeURIComponent(childId)}/stop`, { method: "POST" });
}
```
These managed calls use the configured base (`this.baseUrl ?? base`), but bypass `request()` and `managedRequest()`. They do not check `res.ok`, so HTTP error responses resolve without an exception; they also bypass the wrapper's structured error handling and managed request options. Assess the endpoint-specific contract before changing headers or credentials.

`ProgressHistoryModal.tsx` uses `client.progressHistory(agentId)` when a client is supplied; that method uses `request()` and checks the response shape. Its fallback fetch uses `${activeRouterUrl()}/v1/agents/.../progress/history` and checks `res.ok`. It is not a root-relative fetch.

### 4. Overzealous Protocol Validation Guards
The current `web/router-ui-react/src/transport/http-client.ts` defines `validSource` (not `protocol/guards.ts`):
```typescript
function validSource(value: unknown): boolean {
  const source = recordValue(value);
  return !!source && Object.keys(source).length === 1 && ["agent", "web_ui", "android", "discord", "reminder", "system", "unknown"].includes(String(source.kind));
}
```
If the backend adds an optional diagnostics or timestamp field to `source`, `Object.keys(source).length === 1` fails and causes the frontend to drop the entire message or throw `invalid_response`.

---

## 4. Potentially Unused Endpoints & Legacy Routes

Historical hypotheses only: UI usage is not proof an endpoint has no external, native, test, or operational consumers. The relevant owners must verify callers, route ownership, and compatibility before removal or consolidation.

| Route | Backend Location | Frontend Status | Recommendation |
|---|---|---|---|
| `/v1/memory/*` (`rebuild`, `lookup`, `expand`, `apply`, `touch`) | `http.go:616` | Exposed in `http-client.ts`, never called anywhere in UI components | Remove from UI client or mark as internal tool-only |
| `/v1/tmux/windows*` | `live.go:662` | Exposed in `http-client.ts:tmux()`, never invoked by React UI | UI is managed-first; remove dead tmux HTTP wrapper from client |
| `/v1/tools/catalog` & `/v1/tools/catalog/` | `http.go:255` / `tools_http.go` | Not declared in `httpRoutes` manifest, never consumed by frontend | Delete or document internal-only status |
| `/v1/messages/{id}` (GET) | `http.go:routes` | Exposed in `http-client.ts:message()`, never used in UI | Remove from client |
| `/v1/messages/{id}/retry` (POST) | `http.go:routes` | Exposed in `http-client.ts:retryMessage()`, never used in UI | Remove from client |
| `/v1/messages/{id}` (DELETE) | `http.go:routes` | Exposed in `http-client.ts:deleteMessage()`, never used in UI | Remove from client |
| `/v1/managed-agents/{id}/router-log` | `router_log.go` | Both agent-scoped and global `/v1/router-log?query=...` exist | Keep `/v1/router-log` with query parameter; agent-specific route is redundant |
| `/v1/acceptance/` | `router.go` | Test harness hook | Ensure disabled in production builds |

---

## 5. Historical Candidate Actions

These are proposals, not validated instructions. Assign owners and verify current behavior and all consumers before changing contracts or removing routes.

1. **Unify timeline retrieval**: Promote `/v1/conversation?agent_id=...` as canonical. Remove UI dependency on `/v1/history?view=complete` and `/v1/conversations`.
2. **Standardize attachments**: Make attachment payload shapes identical across message sending, database persistence, timeline events, and image rendering (`{ id, url, mime_type, size_bytes, name }`).
3. **Harmonize casing and types**:
   - Change `seq` in `conversation_timeline` to `sequence` (or vice-versa).
   - Use ISO-8601 UTC strings everywhere.
   - Clean up `auth_type` vs `auth_mode`.
4. **Clean frontend transports**:
   - Assess the managed interrupt and subagent-stop bare fetches and their error handling; assess whether the progress modal fallback is still needed.
   - Assess strict source validation in `http-client.ts` (e.g. `Object.keys(x).length === 1`) against the intended contract.
5. **Investigate potentially unused endpoints**: Before considering removal, verify ownership and consumers of the UI client wrappers for `/v1/memory/*`, `/v1/tmux/windows/*`, and `/v1/messages/{id}/retry`.
