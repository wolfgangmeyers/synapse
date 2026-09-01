# Mecha Messaging diagnostics and history sanitation extraction plan

## Status and source of truth

This is the scoped requirements and historical evidence record for the diagnostics/history slice. The implementation is committed in immutable historical commit `d85c5a8881b2c6d7b72c5e96e93db7c9ee1b14a0`; no services were started.

The current base includes premature scoped E2E commit `0f9b48b7488d8cb23907317a53fd700eb9cf5ea9`, tree `f778050a00582b55fe555188076ac604d96629e8`. The current working tree also contains the approved diagnostics instrumentation. Its scope spans pi-subagents, mecha-messaging, Goal, router, and WebUI diagnostics; the mecha-messaging journal changes are in `diagnostics-history.ts` and the paired `mailbox-client.ts`/generated `mailbox-client.js` output. Historical diagnostics E2E, mutation, coverage, and final review evidence remains tied to its immutable target.

The roadmap identifies this as the next slice after delivery, activity lifecycle, and compaction orchestration. Those completed boundaries stay unchanged. The target is the remaining diagnostics and provider-history code in `pi-extensions/mecha-messaging/index.ts`.

## Review corrections

The implementation and tests must include these eight constraints:

1. `HistoryMessage` accepts Pi message objects whose content may be an array of runtime/provider-specific blocks or a malformed non-array value. Use an `unknown` content value, `ContentBlock = unknown`, and runtime narrowing with `isRecord`; do not use unsafe casts.
2. Only the generated timestamp on the `malformed-tool-call` trace observation may be normalized in the differential comparator. A narrow internal clock seam may be used for direct malformed-diagnostic tests. No other timestamp or arbitrary string may be normalized.
3. Baseline and current differential children use separate roots, environments, homes, diagnostic paths, fake modules, and observation sinks. Inject interval hooks and reject unaccounted startup timers; do not run real intervals or long-lived startup work.
4. The differential fixture makes the hard-compaction threshold deterministic and records its call before the context message read/assignment, including the parent/child gate.
5. Installed-Pi E2E uses a reject-all fetch, explicitly disables automatic compaction, and asserts that the strict fake boundaries received no unexpected calls.
6. Malformed trace payloads retain a maximum of 20 incident entries and slice each string `toolName` to 100 characters. Tests inspect file mode `0600` and trace-directory mode `0700`.
7. The shared JSONL writer owns tail retention for memory and orphan files. The trace writer alone owns one-generation `.1` rotation and rename; they do not share rotation behavior.
8. A raw provider ID of `|suffix` normalizes to the usable empty string `""`. Baseline usability checks are undefined-only, so ownership must use `!== undefined`, not truthiness.

The exact pre-slice baseline is:

- commit: `a2c43e700c59d1fb563a01412d991f2be9fb65c7` (`Close out compaction orchestration validation`);
- tree: `f15b8a2a1ac19e13872d455ce583ab6beff97bfd`;
- `pi-extensions/mecha-messaging/index.ts` blob: `0aca6ef0c52aac1a57819fe06e7e1dd854224e3e`;
- source SHA-256: `c6824f8241652b6d4b8d6225079bb71c0e00d18446300d441cfccb56791ad387`;
- source size: `108015` bytes.

The planning baseline command was run against that checkout state:

```text
./pi-extensions/mecha-messaging/node_modules/.bin/tsx --test \
  tests/mecha_messaging_diagnostics_history.test.ts \
  tests/mecha_messaging_diagnostics_history_differential.test.ts \
  tests/mecha_messaging_package.test.ts \
  tests/memory_capture.test.ts
```

Result: 190 passed, 0 failed, 0 skipped. The command covered the current root context hook, orphan quarantine, memory diagnostics, package exports, native trace filtering, and memory-capture boundaries. No repository files or services were changed by that run.

The working tree already contains unrelated dirty and untracked work, including `docs/wolfgang-agent-operating-policy.md`, `tests/android_qol_parity.test.mjs`, `PLAN.md`, `REPORT.md`, `SPEC.md`, `web/router-ui-react/`, and the directory named `~`. Future implementation must use an isolated clean copy for baseline comparison. It must not stage, reset, rewrite, or include those paths.

The current root package export list is asserted in `tests/mecha_messaging_package.test.ts`. It must remain unchanged. The approved journal instrumentation may modify `diagnostics-history.ts` and the paired `mailbox-client.ts`/generated `mailbox-client.js`; regenerate the JavaScript and require `check:mailbox-client-drift` to pass. Other generated artifacts remain unchanged.

## Requirements and non-goals

The extraction must preserve the current provider input, history ordering, messages, diagnostics, UI notices, trace filtering, paths, bounds, and failure behavior. It must not turn sanitation into a new provider policy.

The implementation may:

- add `pi-extensions/mecha-messaging/diagnostics-history.ts`;
- edit `pi-extensions/mecha-messaging/index.ts` only for imports, type wiring, root context delegation, and re-exports;
- add focused direct, differential, and installed-Pi E2E tests;
- update the package file-list assertion and move or extend existing diagnostics assertions;
- add short comments beside branches whose ordering, redaction, or odd compatibility behavior must stay fixed;
- update this roadmap after implementation with the final commit and evidence.

The implementation must not:

- change `mailbox-client.ts` or generated `mailbox-client.js`, except for the approved journal instrumentation, which must keep the generated file in sync with its TypeScript source;
- change `memory-capture.ts`, compaction, activity lifecycle, delivery, or installed packages as part of this extraction; the approved diagnostics instrumentation may also cover pi-subagents, Goal, router, and WebUI diagnostics;
- add a public root export, event, environment variable, diagnostic file, provider field, or retention policy;
- change malformed-name validation, tool-call ownership, provider-ID normalization, deduplication, notification text, or context-hook ordering;
- repair unrelated history records, change persistence, or add a provider-specific workaround;
- use the live router, shared settings, credentials, browser state, or a running Pi session for validation.

This is a pure behavior-preserving extraction. No standalone `SPEC.md` is created. The plan is the requirements source because the existing root `SPEC.md` is unrelated dirty work and must remain untouched. The implementation owner still follows the canonical `dev-loop` workflow.

## History and decision anchors

| Source | Behavior or decision | Use in this plan |
| --- | --- | --- |
| `509f5681eaf4` | Added malformed tool-name quarantine for provider name limits and punctuation. | Preserve the exact name predicate, block removal, and diagnostic inputs. |
| `9452fb7c806` | Added orphan tool-result quarantine after errored assistant messages can be removed during replay. | Preserve provider-ID normalization, ownership, ordering, and the defensive context hook. |
| `754fbbb8f0` | Added opt-in redacted native-subagent traces with field allowlists and one rotated file. | Move the trace writer without widening its output. |
| `cd30d75ed3` | Renamed the trace to the general extension trace and retained the legacy environment path. | Preserve path precedence and the `createNativeSubagentTrace` alias. |
| `2d21cc1c2` | Migrated this surface to strict TypeScript. | Keep runtime narrowing at the Pi boundary and avoid unsafe assertions. |
| `0cdcaf74f`, `cbbeedcb1`, `a63bb4920` | Established the current extraction pattern and composition-root ownership. | Keep the root as the event-registration and dependency-composition layer. |
| `~/.mecha-developer/kb/pi-orphan-tool-result-crash.md` | Pi can persist tool results after an errored assistant call is removed. | Keep the guard immediately before provider work. |
| `~/.mecha-developer/kb/pi-extension-maintenance.md` | The checkout package is the active extension source and the generated client is the only intentional JavaScript artifact. | Keep package discovery and require the generated client to stay in sync after the approved journal instrumentation. |
| `~/.mecha-developer/kb/pi-subagent-runtime.md` | Installed Pi tests need explicit identity checks and do not prove checkout activation by themselves. | Make the installed-Pi E2E identify the runtime and loaded source directly. |

## Current code ledger

| Current symbol or area | Current location | Extraction treatment |
| --- | --- | --- |
| `appendMemoryCaptureDiagnostic` | `index.ts` | Move the bounded record construction and shared JSONL sink. Re-export it from the root. |
| `appendBoundedJsonl` | `index.ts` | Keep private to the new module. Preserve its append, tail, and catch behavior. |
| `createExtensionTrace` and `createNativeSubagentTrace` | `index.ts` | Move the allowlisted trace writer and alias. Re-export the same root names. |
| `appendOrphanToolResultDiagnostic` | `index.ts` | Move the bounded orphan record writer. Re-export the same root name. |
| `providerToolCallId` | `index.ts` | Move with its first-`|` provider suffix rule. It is shared by both sanitizers. |
| `isValidToolName` | `index.ts` | Move with the same ASCII pattern and 64-character limit. It remains internal. |
| `quarantineMalformedToolCalls` | `index.ts` | Move as a module-level function. It need not become a root export. |
| `assistantToolCallIds`, `messageToolCallIds` | `index.ts` | Move with their different current roles and `toolCall`-only behavior. |
| `quarantineOrphanToolResults` | `index.ts` | Move and re-export from the root. Preserve its result shape. |
| `appendMalformedToolCallDiagnostic` | Inside `createMechaMessagingExtension` | Move its bounded payload assembly behind an injected trace callback and narrow internal test clock. Keep the root dedupe and notice ordering. |
| `reportedMalformedToolCalls`, `reportedOrphanToolResults` | Inside `createMechaMessagingExtension` | Keep in the root because they are context-hook lifetime state. Do not clear them on session start or shutdown. |
| `pi.on("context", ...)` | `index.ts` | Keep registration in the root. Delegate pure sanitation and retain hard-compaction ordering, notices, dedupe, event mutation, and return behavior. |
| `boundedConversationText` | `index.ts` | Leave in the root. It is active-work projection logic, not this history-safety boundary. |

## Selected design

Create one internal module named `diagnostics-history.ts`. It owns pure message sanitation and all bounded diagnostic serialization. It does not register Pi handlers and does not know about Mailbox V2, Goal, the router, memory capture, or the provider.

The root remains responsible for:

- Pi event registration;
- the hard-compaction check before sanitation;
- the two per-extension diagnostic deduplication sets;
- current session and model metadata collection;
- resolving the current agent ID;
- calling the trace and file-writer seams;
- assigning `event.messages`;
- user notices and the exact return value.

The context-event adapter means a pure function that accepts the Pi event's optional message array and returns one complete result. It does not mean moving the `pi.on("context")` registration into the new module.

Three alternatives were considered:

1. **Leave the code in `index.ts`.** This avoids extraction risk. It leaves provider-history rules, file sinks, and privacy filtering mixed into the composition root, so it does not complete the roadmap slice.
2. **Move the entire context handler into a new class that owns Pi, session state, notices, and files.** This creates a second composition root, hides event ordering, and couples the module to Pi lifecycle state. It also makes direct testing depend on unrelated mailbox setup.
3. **Use a pure sanitation result plus stateless bounded sinks.** The module returns the final messages and both incident lists. The root keeps the Pi adapter and its session-scoped dedupe. This gives the extraction a clear contract and preserves the existing owner for every side effect.

The third design is selected. It does not add a repair queue, new incident state, a new diagnostic schema, or a new provider adapter.

## Module boundary and API

`diagnostics-history.ts` may import `summarizeNativeSubagents` and its type from `./activity-lifecycle.js`. It may import only Node `fs` and `path` APIs otherwise. It must not import `index.ts`, the Goal package, the mailbox client, the router, or `memory-capture.ts`.

The exact internal API is:

```ts
export type JsonPrimitive = string | number | boolean | null;
export type JsonValue = JsonPrimitive | JsonObject | JsonValue[] | undefined;
export type JsonObject = { [key: string]: JsonValue };

export type ContentBlock = unknown;
export type HistoryMessage = {
  [key: string]: unknown;
  role?: string;
  stopReason?: string;
  toolCallId?: string;
  toolName?: string;
  content?: unknown;
};

export type MalformedToolCall = {
  messageIndex: number;
  blockIndex: number;
  toolName: string | undefined;
  toolNameLength: number | undefined;
  toolCallId: string | undefined;
};

export type OrphanToolResult = {
  messageIndex: number;
  toolCallId: string | undefined;
  providerToolCallId: string | undefined;
  toolName: string | undefined;
  previousRole: string | undefined;
  nextRole: string | undefined;
  nearestAssistantIndex: number | undefined;
  nearestAssistantStopReason: string | undefined;
  nearestAssistantToolCallIds: string[];
  matchingToolCallLater: boolean;
};

export type ContextHistoryEvent = { messages?: HistoryMessage[] };

export type ContextHistorySanitization = {
  messages: HistoryMessage[];
  messagesBeforeOrphanQuarantine: HistoryMessage[];
  malformed: MalformedToolCall[];
  orphaned: OrphanToolResult[];
};

export type ExtensionTrace = {
  enabled: boolean;
  record: (event: string, fields?: object) => boolean;
};

export function quarantineMalformedToolCalls(
  messages: HistoryMessage[],
): { messages: HistoryMessage[]; malformed: MalformedToolCall[] };

export function quarantineOrphanToolResults(
  messages: HistoryMessage[],
): { messages: HistoryMessage[]; orphaned: OrphanToolResult[] };

export function sanitizeContextHistory(
  event: ContextHistoryEvent,
): ContextHistorySanitization;

export function appendMalformedToolCallDiagnostic(
  input: {
    sessionId: string;
    sessionFile?: string;
    model?: unknown;
    agentId?: string;
    totalMessages?: number;
    malformedCount: number;
    malformed: MalformedToolCall[];
  },
  trace: (event: string, fields: object) => unknown,
  options?: { now?: () => Date },
): void;

export function appendMemoryCaptureDiagnostic(
  event: object,
  options?: { path?: string; maxBytes?: number; keepBytes?: number },
): void;

export function appendOrphanToolResultDiagnostic(
  event: JsonObject,
  options?: { path?: string; maxBytes?: number; keepBytes?: number },
): void;

export function createExtensionTrace(options?: {
  environment?: NodeJS.ProcessEnv;
  path?: string;
  maxBytes?: number;
  now?: () => Date;
}): ExtensionTrace;

export const createNativeSubagentTrace: typeof createExtensionTrace;
```

The optional `appendMalformedToolCallDiagnostic` clock is an internal direct-test seam, not a Messaging root export. Its default is `() => new Date()`, so the root path keeps current behavior. Do not add a clock option to the memory or orphan writers. The implementation may use local aliases, but the argument meaning and result fields are fixed. `HistoryMessage` deliberately does not extend `JsonObject`: its unknown index signature and `unknown` content value accept both Pi arrays and malformed non-array content without an assertion. `Array.isArray` must narrow content before iteration, and each block must be narrowed separately. Narrow every block with a runtime record check before reading `type`, `name`, or `id`; do not cast Pi values, use `as any`, or use `as unknown as`. The new module's exports are implementation-module exports. The Messaging root re-exports only the names it exports today. `quarantineMalformedToolCalls` and `sanitizeContextHistory` must not appear in `Object.keys()` of the root package.

`sanitizeContextHistory` must perform the same two passes as the current handler:

1. run malformed-call filtering;
2. run orphan-result filtering on the malformed-filtered messages;
3. return the final messages, the malformed-filtered event list, and both incident lists.

The `messagesBeforeOrphanQuarantine` result is required by the root adapter. Pi observes that intermediate list through `event.messages`, while the provider receives the final orphan-filtered `messages` list. Keeping both lists in one adapter avoids a second sanitation pass and preserves the baseline event assignment.

It must not mutate the input message objects. It must retain the current cloned-assistant behavior in the malformed pass and the current original-array return when the orphan pass finds no orphans. Direct tests should assert values and ordering. They should not make a new identity guarantee.

## Frozen history behavior

### Malformed tool calls

- Scan only assistant messages whose `content` is an array.
- Treat both `type: "toolCall"` and `type: "tool_use"` as tool-call blocks for name validation.
- A name is valid only when it is a non-empty string, no longer than 64 JavaScript string characters, and matches `/^[a-zA-Z0-9_-]+$/`.
- Remove each invalid block. Keep the assistant message and every other block in its original order, including `null`, strings, ordinary text, and valid tool calls.
- Record the original message index and block index. A non-string block ID becomes `undefined` in the incident record.
- Do not scan tool results, user messages, or non-array content.
- The valid `tool_use` alias remains in history, but the current ownership helpers recognize only `toolCall`. This means a valid `tool_use` block does not establish ownership for a later result. It is an existing compatibility quirk and stays frozen in this refactor.
- A malformed call's result is handled by the second orphan pass. The two removals must remain separate in the result so direct tests can prove the pass order.

### Orphan tool results

- `providerToolCallId` accepts a non-empty raw string and returns the text before the first `|`. It does not trim or otherwise normalize the value. The raw value `|suffix` therefore returns the usable empty string `""`; the baseline check is `!== undefined`, not a truthiness check. A `toolCall` with `|call` owns a result with `|suffix` through that empty provider ID. A raw empty string has no usable ID because only the raw input length check rejects it.
- Scan messages from left to right.
- A `toolResult` with a usable provider ID is retained only after a prior preserved assistant message contributed the same provider ID through a `type: "toolCall"` block.
- Assistant messages with `stopReason: "error"` or `stopReason: "aborted"` contribute no ownership IDs. A result after either message is quarantined.
- Assistant messages with any other stop reason may contribute string IDs from `toolCall` blocks. A missing or non-string block ID contributes nothing.
- A result with no usable ID is preserved. It is not reported as an orphan.
- An orphan result is removed from the provider list and recorded with the current message index, raw and normalized IDs, tool name, neighboring roles, nearest prior assistant metadata, and whether a later `toolCall` block has the same normalized ID.
- A later matching call does not rescue an earlier result. `matchingToolCallLater` is diagnostic context only.
- Diagnostic nearest-assistant IDs use `toolCall` blocks even when that assistant has an error or aborted stop reason. Ownership and diagnostic context intentionally use different helpers.
- A clean orphan pass returns the supplied array and an empty incident list. A dirty pass returns a new list containing every preserved message in original order.

### Context event behavior and ordering

The root keeps one `pi.on("context")` handler. Its exact order remains:

1. for a parent context, call `compactionState().enforceHardCompaction(context)`;
2. call `sanitizeContextHistory(event)` and retain its malformed and orphan lists;
3. report fresh malformed incidents through the current trace and notify with `Quarantined malformed tool call(s): ...`;
4. assign `event.messages` to `messagesBeforeOrphanQuarantine`, the malformed-filtered list;
5. read the assigned event list as the baseline did, then report fresh orphan incidents through the current JSONL writer and notify with `Quarantined orphan tool result(s) before provider request: ...`;
6. return `undefined` when no orphan was found, or `{ messages }` when at least one orphan was removed.

The sanitation portion currently runs for child contexts too because only the hard-compaction check is parent-gated. Preserve that behavior.

Malformed reporting happens before assignment to `event.messages`. Orphan reporting happens after assignment. This is observable in an ordered harness and must not be simplified into one generic notification pass.

The two deduplication sets stay in `index.ts` for the lifetime of one extension instance. Their key remains `${sessionId}:${toolCallId}` using the raw tool-call ID. That preserves these current details:

- the same raw ID in one session reports once across repeated context events;
- the same raw ID in two sessions reports once per session;
- two suffix variants with the same provider ID remain different diagnostic identities;
- missing IDs share the current `sessionId:undefined` bucket;
- session start and shutdown do not clear either set.

The sanitation result itself is always applied on every context event. Deduplication suppresses only repeat diagnostics and notices. It never restores a quarantined message.

## Frozen diagnostics behavior

### Shared bounded JSONL writer

Move `appendBoundedJsonl` without changing its operational behavior. This shared helper is used only by memory-capture and orphan-result JSONL files:

- no path means no write and no error;
- create the containing directory recursively;
- append one JSON object followed by a newline with file mode `0600`;
- when the active file exceeds the supplied maximum, retain the current tail using the supplied keep size;
- catch directory, append, stat, read, and write errors;
- never allow a diagnostic failure to escape into Pi or provider flow.

The shared helper retains a tail in the active file. It does not remove or rename a `.1` file. Trace rotation is separate: `createExtensionTrace` alone removes the old `.1` and renames the active trace to `.1` before appending. The trace writer does not call `appendBoundedJsonl`, and the bounded JSONL writers do not rotate.

Keep these current limits:

| Diagnostic surface | Active limit | Retention/rotation | Default path |
| --- | ---: | --- | --- |
| Memory capture (shared JSONL writer) | 128 KiB | 64 KiB tail in the active file; no rename | `$HOME/.mecha-developer/logs/memory-capture.jsonl` or `MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH` |
| Orphan results (shared JSONL writer) | 256 KiB | 128 KiB tail in the active file; no rename | `$HOME/.mecha-developer/logs/orphan-tool-results.jsonl` or `MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH` |
| Extension trace (trace writer) | 512 KiB before rotation | one `.1` generation; no tail rewrite | `MECHA_PI_EXTENSION_TRACE_PATH`, then legacy `MECHA_PI_NATIVE_SUBAGENT_TRACE_PATH` |

Do not add an unbounded archive or a second rotated generation.

### Memory capture records

`appendMemoryCaptureDiagnostic` keeps exactly these output keys:

- `timestamp` from the event when it is a string, otherwise the current ISO timestamp;
- `trigger`, limited to `compaction`, `shutdown`, or `unknown`;
- `status`, limited to `noop`, `applied`, or `failed`;
- `reason`, limited to the existing lowercase slug pattern, with `capture-failed` fallback;
- finite numeric `records` when present.

Unknown source fields, error text, source bodies, prompts, and capture evidence do not enter the record. The current omission of `undefined` keys after JSON serialization remains. The existing memory-capture tests stay in place.

### Orphan records

`appendOrphanToolResultDiagnostic` keeps its current snake_case record and nested orphan fields. It includes only operational metadata:

- timestamp, session ID, session file, string model, string agent ID, total message count, quarantined count;
- message index, raw tool-call ID, provider ID, tool name, neighboring roles, nearest assistant index, nearest assistant stop reason, nearest assistant provider IDs, and the later-match boolean.

Non-string metadata becomes absent. Source message bodies, tool output, prompts, error objects, and arbitrary extra fields never enter the record. The writer remains best effort and bounded.

### Native trace

`createExtensionTrace` keeps these rules:

- no configured path returns `{ enabled: false, record: () => false }` and does not create a file;
- an explicit non-empty path, positive safe `maxBytes`, and callable `now` are required when enabled;
- invalid event names, fields, clocks, file operations, and serialization paths return `false` from `record` and do not throw;
- event names match `/^[a-z][a-z0-9_.-]{0,80}$/`;
- allowed strings are sliced to 128 characters without trimming;
- allowed numbers are safe non-negative integers;
- allowed booleans remain booleans;
- unknown fields are silently dropped;
- `subagents` is summarized through the existing bounded `summarizeNativeSubagents` helper, with non-arrays becoming an empty list;
- create the parent directory with mode `0700` and the file with mode `0600`;
- when the active file is at or above its limit, remove `.1` and rename the active file to `.1` before appending;
- keep the current event and field allowlists exactly.

The malformed diagnostic trace keeps the full fresh-incident count in `malformedCount`, caps the `malformed` payload at exactly 20 entries, and slices each string `toolName` in that payload to exactly 100 characters. The UI notice still uses its existing 50-character display slice. Direct tests must pass 21 incidents and a name longer than 100 characters and assert all three values.

In particular, do not add `messageIndex`, `blockIndex`, `sessionId`, `sessionFile`, `model`, `agentId`, `totalMessages`, or `malformed` to the native trace allowlist in this slice. The current malformed diagnostic builds camelCase fields, and the default trace filter drops those fields. The default trace file therefore records the `malformed-tool-call` event and its generated timestamp without those payload fields. A custom injected trace callback can still observe the payload. Changing this output is a diagnostics behavior change and needs a separate decision.

The public aliases remain:

```text
createExtensionTrace === createNativeSubagentTrace
```

The extension path wins over the legacy native-subagent path when both are configured. A blank environment value does not enable tracing.

## State and dependency ownership

| State or dependency | Owner | Lifetime and rules |
| --- | --- | --- |
| Sanitized messages and incident arrays | New module call | Stateless per context event. No cross-event cache. |
| Shared JSONL append/tail behavior | New module | Used only by memory and orphan writers. One active file with tail retention; no `.1` rename. |
| Trace path, limit, and file rotation | Trace closure in new module | One closure per trace instance. The trace writer alone owns one active file plus `.1` rotation/rename; it does not use the shared tail writer. |
| Diagnostic file paths | New module | Resolve explicit options first, then current environment at call time, then current HOME fallback. |
| Memory/orphan record limits | New module constants | Preserve 128/64 KiB and 256/128 KiB values. |
| `reportedMalformedToolCalls` | Root | One extension-instance set keyed by raw session and raw call ID. No reset added. |
| `reportedOrphanToolResults` | Root | One extension-instance set keyed by raw session and raw call ID. No reset added. |
| Current agent ID | Root | Resolve only while building a fresh incident record. Resolver failure remains contained. |
| UI notice | Root | Use the current context UI, exact text, and `error` level. |
| Hard compaction state | Compaction module | Root checks it before calling the new sanitation adapter. No ordering change. |
| Native subagent summarizer | Activity module | Reuse the existing bounded helper. Do not move activity state into this slice. |
| Memory capture lifecycle | Root and `memory-capture.ts` | Keep the existing diagnostic callback reference. Do not change capture construction or source sanitization. |
| Pi, session manager, router, and provider | Root or test harness | New module sees structural values only. It does not call any external boundary directly. |

The module has no timers, network calls, child processes, session files, global symbols, mutable history, or Pi event listeners.

## Root wiring

The root should replace local definitions with imports from `diagnostics-history.ts` and retain the existing root exports. The package's public export comparison must pass without adding a name.

The `createMechaMessagingExtension` setup keeps its current order. Trace validation and the `traceNativeSubagent` adapter are created before the context hook. The current `appendDiagnostic` wrapper remains the injected trace seam for root event code.

The context hook uses the new pure result. It must not call the module's writers through a hidden global or let the module access `context.ui`. The root supplies the same metadata and invokes the same side effects in the sequence above.

The module-level `appendMalformedToolCallDiagnostic` receives the existing payload values and the root trace callback. It maps the same fields, clips the same display and diagnostic names, catches its own errors, and does not return a status that could alter the context result.

No handler registration order changes. `context` remains after tool completion handlers and before `message_end`. The hard-compaction call stays ahead of all history sanitation. The module must not publish a new Pi event, call the provider, or write to session history.

## Direct tests

Add `tests/mecha_messaging_diagnostics_history.test.ts` for the module's direct contract. Keep current root tests in `tests/mecha_messaging_package.test.ts` and `tests/memory_capture.test.ts` as regression coverage. Move a test only when its import path changes and keep a root integration assertion for every root behavior.

The direct matrix must cover:

- valid, empty, overlong, punctuated, whitespace, underscore, hyphen, and non-string tool names;
- `toolCall` and `tool_use` validation aliases;
- null, string, ordinary text, malformed, and valid blocks in one assistant message;
- assistant and non-assistant messages with missing or non-array content;
- exact malformed indexes, name lengths, and string-only IDs;
- valid tool-call ownership with no suffix, one suffix, multiple suffixes, `|suffix` producing the usable normalized ID `""` through the baseline's undefined-only check, and raw empty IDs;
- orphan results before a call, after a call, after an errored assistant, after an aborted assistant, after a valid `tool_use`, and after a later matching call;
- missing-ID results, duplicate results, nearest-assistant metadata, and preservation of every valid message order;
- malformed-call removal followed by orphan-result removal through `sanitizeContextHistory`;
- clean-input result shape and the current cloned-assistant value behavior;
- memory diagnostic allowlists, fallback trigger/status/reason values, undefined output keys, tail bounds, secret-text exclusion, and a fresh output file whose mode bits are exactly `0600`;
- orphan diagnostic field narrowing, nested metadata, bound, secret-text exclusion, and a fresh output file whose mode bits are exactly `0600`;
- disabled trace, path precedence, legacy path fallback, invalid factory inputs, invalid record inputs, field allowlists, string clipping, safe-number checks, boolean checks, subagent summaries, one-generation rotation, trace parent-directory mode bits exactly `0700`, trace file mode bits exactly `0600`, and write-failure containment;
- malformed trace payload behavior, including exactly 20 retained incident entries, 100-character `toolName` clipping, the full `malformedCount`, the injected clock, and the fact that the default trace filter drops its camelCase payload fields;
- diagnostic writers pointed at an unusable path returning without throwing;
- the orphan writer's outer catch containing an event-access failure without throwing.

The root integration matrix must cover:

- hard compaction is checked before sanitation;
- a malformed block and its following result produce two sanitation effects in the correct order;
- fresh malformed and orphan incidents produce the exact existing notice text and level;
- repeated context events report one incident per raw session/call key while still returning sanitized messages every time;
- same raw call ID in different sessions reports independently;
- same provider ID with different raw suffixes keeps separate report identities;
- the raw `|suffix` ID is retained as normalized `""` for ownership because checks are `!== undefined`, while a raw empty ID follows the no-ID path;
- a missing call ID keeps the current dedup bucket behavior;
- no-orphan contexts return `undefined` after assigning the sanitized event messages;
- orphan contexts return exactly `{ messages }`;
- context sanitation remains active for a child context;
- trace or file-write failure does not change provider-facing messages or handler completion;
- the root export list and package file-discovery contract remain unchanged except for the new internal module appearing in the expected file list.

Tests must assert exact message arrays and exact notification strings. They must not only assert that the provider eventually returned a response.

## Differential baseline harness

Add `tests/mecha_messaging_diagnostics_history_differential.test.ts`. It compares the current root behavior with the immutable pre-slice TypeScript root at `a2c43e700c59d1fb563a01412d991f2be9fb65c7`.

The harness must:

1. create a temporary run directory containing two separate child-local roots: one baseline root and one current root;
2. run `git archive --format=tar a2c43e700c59d1fb563a01412d991f2be9fb65c7 pi-extensions/mecha-messaging pi-extensions/mecha-goal` from the repository root;
3. unpack only the archived package trees into the baseline child root and materialize the exact current implementation package trees into the separate current child root;
4. verify the archived `mecha-messaging/index.ts` has the expected baseline blob or SHA-256 and still contains the pre-extraction sanitizer and trace symbols;
5. launch one child rooted in each child-local root for every scenario. Each child gets its own HOME, working directory, module cache/fake modules, diagnostic paths, session files, and observation sink. Neither child may import the other root or the shared checkout directly;
6. install each child's fakes before importing that child's baseline or current root;
7. give both sides the same fixed Pi runtime, contexts, session IDs, model, agent ID, trace recorder, environment paths, diagnostic events, and input histories;
8. record one ordered observation for every operation, returned value, event-message assignment, trace call, UI call, file record, and writer error outcome;
9. compare ordered arrays and fail with scenario, operation, first index, and field path;
10. fail when a child exits nonzero or produces no final JSON observation. A missing child result is not a passing comparison.

The baseline source identity is fixed to the full commit above. It must not be replaced with `HEAD`, a JavaScript artifact, or a moving branch name.

The child fixture must use:

- a separate temporary HOME and explicit `MECHA_MEMORY_CAPTURE_DIAGNOSTICS_PATH`, `MECHA_ORPHAN_TOOL_RESULT_DIAGNOSTICS_PATH`, and `MECHA_PI_EXTENSION_TRACE_PATH` values under each child root;
- a fake client and coordinator that only record calls and never connect to a router;
- a fake `resolveCurrentAgentId` returning a fixed valid ID;
- an injected trace recorder for root context observations, plus direct file-writer calls for actual JSONL output;
- child-local `setTimeoutImpl`, `clearTimeoutImpl`, `setIntervalImpl`, and `clearIntervalImpl` hooks. Each hook records its delay and handle, never runs a real long-lived callback, and fails on an unexpected call. The fixture must clear every handle before emitting its final observation;
- no permissive startup shim: do not emit session-start, message-start, agent-start, or other lifecycle startup events in a differential scenario unless that event is required by the scenario and its expected calls are asserted. The context fixture must not start a router, reader, network client, or interval as incidental setup;
- a fixed model object and parent/child session contexts;
- a fake fetch that rejects every URL, records the URL in an unexpected-call list, and is asserted empty. A root path that initializes an external reader therefore fails immediately, even though this slice should not call one.

The fake external boundaries must be strict. The fake client records all arguments and rejects unexpected method names. The fake UI records exact message and level. The trace recorder records fields before any normalization so the comparison can distinguish a root adapter change from file filtering. A diagnostic path pointing to a directory exercises best-effort write failure without touching a real shared path.

The `context-order` fixture must force the hard-compaction seam. Set a child-local deterministic compaction policy with a mandatory threshold, give the parent context a fixed usage at or above that threshold, and provide `abort` and `compact` fakes that record calls without invoking a provider or completion callback. Mark the child session through the same child-session event path the root uses before exercising the child context. Record the usage check, hard-compaction call, abort, compact call, message read, malformed trace, event assignment, and orphan writer/UI operations in order. Assert hard-compaction operations precede the first message read and that a child context with the same usage performs sanitation without the parent hard-compaction call. Use a fresh extension child for each scenario so a pending fake compaction cannot leak across cases.

Use a narrow normalization allowlist. The preferred fixture makes all calls deterministic. The only comparator normalization permitted for time is the exact `timestamp` field of the `malformed-tool-call` trace observation; identify it by its trace operation and event name. Direct module tests use the optional malformed-diagnostic clock seam with a fixed `Date`. Do not normalize memory-capture or orphan-writer timestamps, arbitrary `.timestamp` fields, temporary paths beyond the explicitly supplied runner paths, arbitrary strings, message bodies, IDs, counts, field names, or arrays. Add no allowlist entry without a baseline citation and plan-review signoff.

The differential scenario table is:

| Scenario | Required observations and assertions |
| --- | --- |
| `clean-history` | Empty and clean histories preserve every message and order. The context handler assigns the current sanitized array and returns `undefined`. |
| `malformed-matrix` | Mixed `toolCall` and `tool_use` blocks remove only invalid names. Valid blocks, nulls, strings, and ordinary messages remain ordered. Record exact incident indexes. |
| `malformed-causes-orphan` | A malformed call is removed first. Its result is then removed as an orphan. Malformed reporting precedes event assignment. The orphan file is absent when the assignment is observed, and exists in the post-context file snapshot. Orphan reporting follows assignment. |
| `orphan-ownership` | Valid provider-suffixed calls retain matching results, including raw `|suffix` IDs whose normalized provider ID is `""`. Results before calls, after error/aborted assistants, after valid `tool_use`, and with no usable ID follow the frozen rules. |
| `later-match` | A result with a later matching call is still removed and reports `matchingToolCallLater: true`. |
| `repeat-and-scope` | Repeated context events deduplicate by raw session/call key. Two sessions report independently. Suffix variants remain distinct. |
| `trace-and-writers` | Direct root exports produce identical filtered records, file paths, bounds, rotation, and disabled behavior. Unknown fields and source text stay absent. |
| `diagnostic-failure` | Unusable diagnostic paths do not change the returned sanitized messages or throw into the context handler. |
| `context-order` | A parent usage at the forced mandatory threshold records hard-compaction operations before the first message read and sanitation operation. A child with the same usage skips hard compaction but still sanitizes. Clean and orphan return values match the baseline exactly. |

Add comparator self-tests. Insert one unlisted retained-message mutation, one extra notice, one changed trace field, and one reordered observation into copies of a baseline result. Each mutation must make the comparator fail. The comparator must reject an unauthorized normalization path, including an orphan or memory `.timestamp`, and a mismatch hidden inside an array or nested field. A malformed trace timestamp at any other event or field path must also fail.

The differential command is:

```text
./pi-extensions/mecha-messaging/node_modules/.bin/tsx --test \
  tests/mecha_messaging_diagnostics_history_differential.test.ts
```

## Isolated installed-Pi E2E

Add `tests/mecha_messaging_diagnostics_history_installed_pi_e2e.test.ts` for one real installed Pi runtime. This test proves that the root context hook is reached on the actual Pi event path and that the sanitized list reaches the provider. It does not claim router, Mailbox, native-subagent, or production-provider behavior.

The current machine has an installed `@earendil-works/pi-coding-agent` `0.84.3` with matching installed `@earendil-works/pi-ai` `0.84.3`. The test must resolve the installed package from an explicit `MECHA_PI_PACKAGE_ROOT` for the acceptance run, verify both manifest names and versions, reject a source checkout, and verify the required `dist` entrypoints. A configured missing, malformed, wrong-identity, or mismatched package root must fail. An absent ambient package may be skipped only in the default CI compatibility run. A skipped run is reported as not-run and is not evidence for implementation acceptance. The local acceptance handoff needs one successful configured run.

Keep this runtime isolated:

- temporary HOME, project, Pi agent directory, session directory, and diagnostic paths;
- `SettingsManager.inMemory({ compaction: { enabled: false, reserveTokens: 1, keepRecentTokens: 1 }, retry: { enabled: false } })`, with an assertion that automatic compaction is disabled before the prompt starts;
- a temporary `SessionManager` transcript;
- `DefaultResourceLoader` with the current checkout Messaging factory supplied inline exactly once;
- `session.bindExtensions({ mode: "json" })` and an assertion that the context handler is installed;
- installed Pi's `ai.fauxProvider` with a deterministic model and response queue;
- a fake Mailbox client with `connect`, `close`, activity methods, lifecycle methods, reply/receipt methods, fixed instance and lease values. Every method is allowlisted and records arguments;
- a fake coordinator that records calls and never opens a socket;
- child-local timeout and interval hooks passed to the Messaging factory. They record handles, never run long-lived callbacks, and are all cleared in teardown;
- an injected fixed trace recorder or a temporary trace file;
- a child-local `fetch` that rejects every URL, records each unexpected URL, and has an explicit empty-call assertion after the prompt and cleanup;
- an `unexpectedCalls` list shared by the fake client, coordinator, fetch, and any startup hook. Each boundary throws on an unlisted call and the test asserts the list is empty;
- no live router, network, credentials, native-subagent package, browser, or shared settings.

Seed the temporary session with a valid user turn, an assistant message containing ordinary text, a valid `toolCall`, and an invalid `toolCall` or `tool_use` block. Seed a valid result, an orphan result, and an errored or aborted assistant/result pair using the message shapes accepted by the installed SessionManager. Automatic compaction stays disabled for every scenario; an unexpected compaction or lifecycle call is a test failure. The faux provider response function must capture the exact `context.messages` it receives before returning a deterministic assistant text.

Assert that:

- the provider receives no invalid tool-call block;
- the provider receives no orphan result;
- the valid assistant text, valid call, and valid result retain their relative order and fields;
- a valid provider-suffixed ID still matches its result;
- a valid `tool_use` alias follows the current ownership behavior;
- the session prompt completes and reaches idle;
- the context hook's notice and diagnostic behavior matches the direct contract;
- diagnostic output contains no seeded source text, prompts, tool output, error detail, or secret marker;
- the diagnostic files remain within the configured fixture bounds and use the expected temporary paths;
- replacing the diagnostic path with an unusable temporary target still lets the same provider request complete;
- the `unexpectedCalls` list is empty after each provider request and again after cleanup, including no fetch, client, coordinator, interval, or automatic-compaction call outside the fixture contract.

Use a second installed-Pi scenario for a clean history and assert that no sanitation notice or orphan return is produced. Keep automatic compaction disabled in this scenario too. Do not add an installed-Pi test for native lifecycle behavior. That belongs to the completed activity slice.

The test must restore the parent process environment, current working directory, and global `fetch` in `finally`, dispose the session, close fake resources, and remove its temporary root. It must not mutate global settings or install packages as part of the test.

## Coverage gate

Load the `test-coverage` skill and run it with:

```text
lang=node
package=pi-extensions/mecha-messaging
focus=diagnostics-history.ts
```

The skill's report must identify the new module and every uncovered meaningful branch. Address Blocking Gaps and the default Should-Add gaps before review.

The extracted module has a 100 percent pass bar for lines, functions, statements, and branches. Root wiring is covered behaviorally by root tests, differential observations, and installed-Pi E2E. Do not report 100 percent coverage for the full 2,000-line composition root.

Run this focused instrumentation after the direct tests exist:

```text
cd /Users/wolfgang/code/mecha-developer
rm -rf /tmp/mecha-diagnostics-history-coverage
npx --yes c8@10.1.3 \
  --all \
  --extension=.ts \
  --include='pi-extensions/mecha-messaging/diagnostics-history.ts' \
  --exclude='**/*.d.ts' \
  --reporter=text \
  --reporter=json-summary \
  --report-dir=/tmp/mecha-diagnostics-history-coverage \
  --check-coverage \
  --lines=100 \
  --functions=100 \
  --statements=100 \
  --branches=100 \
  ./pi-extensions/mecha-messaging/node_modules/.bin/tsx --test \
    tests/mecha_messaging_diagnostics_history.test.ts
```

The output must include `diagnostics-history.ts` and `/tmp/mecha-diagnostics-history-coverage/coverage-summary.json`. File-write catches, invalid trace input, disabled trace, rotation, path fallback, malformed alias handling, orphan ownership, and clean-input paths all need actual execution. Do not exclude a branch because it is defensive or inconvenient. If a branch is unreachable, document the source invariant and test that boundary.

## Mutation proof

Mutation testing is required in addition to coverage. Use temporary copies and restore only the specific mutation. Do not use `git checkout` or `git restore` on a file that could contain unrelated dirty work.

The direct module mutation set must include at least:

| Mutation | Test that must fail |
| --- | --- |
| Accept a space or punctuation in a tool name | malformed-name matrix |
| Raise the name limit above 64 | overlong-name case |
| Stop recognizing `tool_use` for malformed validation | alias case |
| Keep an invalid block in assistant content | valid-block/order case |
| Run orphan filtering before malformed filtering | malformed-causes-orphan order case |
| Treat `tool_use` as an ownership call | frozen alias ownership case |
| Let error or aborted assistant calls own results | error/aborted ownership case |
| Stop splitting provider IDs at `|` | suffix ownership case |
| Reject the normalized empty provider ID from raw `|suffix`, or change the undefined-only check to truthiness | `|suffix` ownership case |
| Keep an orphan result | orphan result case |
| Let a later matching call rescue an earlier result | later-match case |
| Drop a valid no-ID result | missing-ID preservation case |
| Reorder preserved messages | history order case |
| Include unknown diagnostic fields or source text | writer redaction case |
| Raise the malformed payload cap above 20 or the payload `toolName` cap above 100 | malformed trace cap case |
| Remove trace field allowlisting or string clipping | native trace redaction case |
| Disable trace rotation or JSONL tailing | bounded-writer case |
| Change diagnostic file mode from `0600` or trace directory mode from `0700` | permission case |
| Rethrow a writer failure | diagnostic-failure case |
| Change memory/orphan limits | exact bound case |

The root mutation set must include at least:

| Mutation | Test that must fail |
| --- | --- |
| Move sanitation before hard-compaction enforcement | context-order case |
| Assign `event.messages` before malformed reporting | context-order case |
| Report orphan incidents before assignment | context-order case |
| Clear or bypass the deduplication key | repeat-and-scope case |
| Return `{ messages }` for a clean context | clean-history case |
| Return `undefined` for an orphan context | orphan ownership case |
| Skip sanitation for child contexts | child context case |
| Change malformed or orphan notice text | exact root notice case |

The differential comparator must be mutation-checked separately. Every inserted observation, changed retained message, changed field, and unauthorized normalization rule must fail. After each mutation, restore the temporary file and verify the real source and tests are unchanged by path hash and `git diff --check`.

## Validation commands

Run these from the repository root after implementation:

```text
npm --prefix pi-extensions/mecha-messaging run typecheck
npm run lint:mecha-messaging
npm --prefix pi-extensions/mecha-messaging run check:mailbox-client-drift
git diff --check
```

Run the focused package tests:

```text
./pi-extensions/mecha-messaging/node_modules/.bin/tsx --test \
  tests/mecha_messaging_diagnostics_history.test.ts \
  tests/mecha_messaging_diagnostics_history_differential.test.ts \
  tests/mecha_messaging_diagnostics_history_installed_pi_e2e.test.ts \
  tests/mecha_messaging_package.test.ts \
  tests/memory_capture.test.ts
```

Run the full package command:

```text
npm run test:mecha-messaging
```

Run the repository command separately:

```text
npm test
```

The installed-Pi test's skipped ambient mode must be reported separately from the passing direct and differential tests. A configured installed-Pi failure is a failed E2E, not a skip.

No live router or other service is needed. Any temporary process, fake executable, local socket, or temporary file belongs to its test and must be closed before teardown.

## Dev-loop and review gates

The implementation owner must load the `workflows` skill and retrieve the canonical workflow before production changes:

```text
python3 .claude/skills/workflows/tools/get_workflow.py dev-loop
```

The retrieved workflow is the source of truth. Its required order applies:

1. Search the persistent KB before implementation and record the relevant files and decisions.
2. Treat this plan as the requirements source for this pure refactor. Review behavior, boundaries, alternatives, and exclusions before code.
3. Run Robot Review on this plan with the applicable architecture, API, state, privacy, error handling, test, differential, E2E, coverage, and scope focus areas.
4. Run `pr-validate` with a different persona from the reviewer. Address valid findings and repeat review after every material plan revision until reviewers sign off.
5. Write failing direct tests before moving implementation code. Make the primary sanitation path pass before implementation Robot Review.
6. Run `test-coverage` with concrete output. Address Blocking Gaps and Should-Add gaps.
7. Run Robot Review on the complete implementation with one reviewer per applicable focus area. Validate every finding with a different persona.
8. Apply valid fixes, then run a fresh full Robot Review on the changed target. Do not carry signoff from an older tree across a behavior, contract, or test-evidence change.
9. Record the exact final commit/tree, test counts, differential result, installed-Pi identity, E2E result, coverage output, mutation results, and review signoffs.

The review must pay special attention to:

- malformed `toolCall` and `tool_use` validation versus ownership;
- provider suffix normalization and error/aborted ownership exclusion;
- valid message and array ordering after two sanitation passes;
- context handler side-effect ordering and root return shapes;
- per-session raw-ID deduplication and its missing-ID behavior;
- privacy boundaries in malformed payloads, orphan records, and native traces;
- JSONL bounds, rotation, permissions, and best-effort catches;
- no new root exports, event names, paths, fields, timers, or provider behavior;
- strict TypeScript narrowing without `as any`, `as unknown as`, or unsafe assertions;
- installed-Pi package identity and proof that the current checkout factory was loaded;
- exact baseline materialization and closed differential normalization.

## Completed mutation proof

A temporary Node script outside the repository applied 40 source mutations one at a time and ran the relevant direct or differential test command. It restored the exact original bytes after every mutation, checked the restored module and root hashes after each iteration, and restored both files again in a finalizer. No mutation runner or dependency was added to the repository. The proof completed with all 40 mutants killed:

- Direct module set (24): malformed-name punctuation; 65-character name limit; `tool_use` validation alias; retained malformed block; sanitation pass order; `tool_use` ownership; errored-assistant ownership; provider-ID suffix splitting; normalized empty-ID rejection; truthiness ownership check; retained orphan result; later-call rescue; missing-ID result removal; preserved-message reorder; orphan source-field logging; malformed trace count cap; malformed trace name cap; trace unknown-field allowlist; trace string clipping; trace rotation; JSONL tail retention; diagnostic file mode; trace directory mode; orphan outer-catch rethrow.
- Root set (16): sanitation before hard compaction; assignment before malformed reporting; skipped event assignment; malformed deduplication session scope; clean return shape; orphan return shape; child sanitation; malformed notice text; orphan notice text; final-list event assignment; bypassed adapter input; orphan count; malformed count; malformed deduplication bypass; orphan deduplication bypass; removed post-assignment event read.

The direct command was `tsx --test tests/mecha_messaging_diagnostics_history.test.ts`. The root command was `tsx --test tests/mecha_messaging_diagnostics_history_differential.test.ts`. The final restored source hashes were `diagnostics-history.ts` `d7ad8e1398217b97cecf8cb646822c8a857eaa5e4989612e7c5a09ecf7ec4d79` and `index.ts` `b370e684928001625bcaf641f4fab2bb050defb3886835ccf1249f43a874dc00`. The proof did not use or claim a permanent mutation runner.

## Documentation gate

Keep this plan as the durable record of the selected boundary, frozen odd behaviors, rejected alternatives, baseline identity, and validation contract. Do not delete it without first moving any rejected alternative or newly discovered system fact into the appropriate KB entry.

After implementation, update `docs/mecha-messaging-quality-refactor-roadmap.md` under **Diagnostics and history sanitation** with the actual commit or working-tree identity, tests, and remaining gaps. Do not claim an installed-Pi run when the test skipped. Do not claim diagnostic fields that the default trace filter drops.

Add comments only where a future reader could safely change behavior by accident. The useful comments explain why sanitation runs in two passes, why `tool_use` ownership stays separate, why error/aborted assistants do not establish ownership, why the root keeps context side effects, and why trace fields are dropped. Do not add comments that restate the next line of code.

Read the completed plan, diff, and roadmap back as separate artifacts before accepting the implementation. Human-facing documentation and any commit or review text must go through the Wolfgang voice review gate.

## Completion criteria

This slice is complete only when:

- `diagnostics-history.ts` owns the agreed pure sanitation and bounded diagnostic code;
- `index.ts` remains the Pi composition root and preserves context ordering;
- root exports, messages, statuses, paths, bounds, and trace allowlists remain unchanged; the approved diagnostics instrumentation spans pi-subagents, mecha-messaging, Goal, router, and WebUI diagnostics, with the paired `mailbox-client.ts`/generated `mailbox-client.js` output passing the generated-client drift check;
- valid history messages and their order survive every direct, differential, and installed-Pi scenario;
- malformed blocks and orphan results are removed before provider work under the frozen rules;
- malformed and orphan diagnostics remain bounded, redacted, deduplicated, and best effort;
- the differential harness compares the exact archived baseline with no unlisted ordered mismatch;
- the installed-Pi E2E passes with an explicitly identified matching runtime, or its absence is reported as not-run and does not count as evidence;
- direct module coverage reaches 100 percent for lines, functions, statements, and branches;
- every listed mutation fails a relevant assertion and the real checkout is restored;
- typecheck, lint, generated-client drift, focused tests, full tests, and `git diff --check` pass;
- Robot Review and independent validation sign off on the immutable final target;
- the roadmap records the final evidence without adding scope.

The extraction implementation and tests were committed in the historical slice. The approved diagnostics instrumentation spans pi-subagents, mecha-messaging, Goal, router, and WebUI diagnostics; the mecha-messaging journal changes include the diagnostics/history and paired mailbox-client source/generated files, with generated-client drift checked. No services were started.
