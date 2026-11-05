import assert from "node:assert/strict";
import test from "node:test";

import { readFileSync } from "node:fs";
const capturePolicy = JSON.parse(readFileSync(new URL("./fixtures/memory-capture-policy.json", import.meta.url), "utf8"));
import { createMemoryCaptureLifecycle, runMemoryExtractor, sanitizeCaptureSource, validateCaptureResult } from "../pi-extensions/mecha-messaging/memory-capture.ts";

const policy = { capture: { compaction_deadline_ms: 30_000, shutdown_deadline_ms: 10_000, source_max_bytes: 4096, source_message_max_bytes: 1024, maximum_records: 6, maximum_title_bytes: 256, maximum_body_bytes_per_record: 16384, maximum_total_body_bytes: 32768, maximum_related_ids_per_record: 24 } };
const source = [{ id: "tool-1", role: "toolResult", content: "Use the signed production workflow. Verify /health before announcing completion." }];
const record = (id: string, body: string, related: string[] = []) => ({ mode: "create", id, title: id, body, tags: ["release"], related, evidence: { source_ids: ["tool-1"] } });

test("capture lifecycle serializes a valid tagged linked chunk batch before apply", async () => {
  const calls: Array<{ operations: Array<{ id: string }> }> = []; const capture = createMemoryCaptureLifecycle({ policy, extract: async () => ({ decision: "apply", records: [record("release-process", "Use the signed production workflow."), record("release-health", "Verify /health before announcing completion.", ["release-process"]) ] }), apply: async (input) => { calls.push(input); return { status: "applied", records: 2 }; } });
  assert.deepEqual(await capture.compact(source), { status: "applied", records: 2 });
  assert.equal(calls.length, 1); assert.deepEqual(calls[0].operations.map(({ id }) => id), ["release-process", "release-health"]);
});

test("ordinary successful Pi tool results support capture evidence without custom details", () => {
  const wrapped = [{ id: "entry-1", type: "message", message: { role: "toolResult", content: [{ type: "text", text: "Use the signed production workflow." }], isError: false } }];
  const snapshot = sanitizeCaptureSource(wrapped, policy.capture); assert.deepEqual(snapshot.messages, [{ id: "entry-1", role: "toolResult", text: "Use the signed production workflow." }]); assert.deepEqual(validateCaptureResult({ decision: "apply", records: [{ ...record("release-process", "Use the signed production workflow."), evidence: { source_ids: ["entry-1"] } }] }, snapshot, policy.capture).operations.map(({ id }) => id), ["release-process"]);
  const failed = sanitizeCaptureSource([{ id: "entry-2", type: "message", message: { role: "toolResult", content: [{ type: "text", text: "Use the signed production workflow." }], isError: true } }], policy.capture); assert.deepEqual(failed.messages, []);
});

test("sanitizer excludes custom, compaction, thinking, images, and credential payloads", () => {
  const snapshot = sanitizeCaptureSource([{ id: "user", role: "user", content: "keep this" }, { id: "thinking", role: "thinking", content: "secret thought" }, { id: "custom", type: "custom_message", content: "mailbox body" }, { id: "compact", type: "compaction", summary: "summary" }, { id: "image", role: "user", content: [{ type: "image", data: "base64" }] }, { id: "secret", role: "tool", content: "token=abc123" }, { id: "env", role: "tool", content: "AWS_SECRET_KEY=long-secret-value" }, { id: "auth", role: "tool", content: "Authorization: Bearer long-private-token" }, { id: "mail", role: "tool", content: "From: person@example.test\nprivate correspondence" }], policy.capture);
  assert.deepEqual(snapshot.entryIds, ["user"]); assert.doesNotMatch(JSON.stringify(snapshot), /mailbox body|secret thought|base64|abc123|AWS_SECRET|Bearer|person@example/);
});

test("validator rejects stale, unverified, noisy, malformed, and unlinked capture output before apply", () => {
  const snapshot = sanitizeCaptureSource(source, policy.capture);
  const invalid = [
    { decision: "apply", records: [{ ...record("bad", "# heading") }] },
    { decision: "apply", records: [{ ...record("bad", "durable", []), evidence: { source_ids: ["missing"] } }] },
    { decision: "apply", records: [{ ...record("bad", "durable", []), tags: ["Release"] }] },
    { decision: "apply", records: [{ ...record("bad", "durable", ["unknown"]) }] },
    { decision: "apply", records: [{ ...record("bad", "durable", []), stale: true }] },
    "```json\n{\"decision\":\"noop\",\"records\":[]}\n```",
  ];
  for (const value of invalid) assert.throws(() => validateCaptureResult(value, snapshot, policy.capture));
  assert.deepEqual(validateCaptureResult({ decision: "noop", records: [] }, snapshot, policy.capture), { decision: "noop", records: [] });
});

test("bounded destination context rejects duplicate creates and mismatched update CAS bases before apply", async () => {
  const destination = [{ id: "release-process", title: "release-process", memory: "Use the signed production workflow.", relevant_at: "2026-01-01T00:00:00.000Z", tags: ["release"], related: [] }]; let applied = 0;
  const duplicate = createMemoryCaptureLifecycle({ policy, context: async () => destination, extract: async () => ({ decision: "apply", records: [record("release-process", "Use the signed production workflow.")] }), apply: async () => { applied += 1; } });
  await assert.rejects(() => duplicate.compact(source), /invalid durable/); assert.equal(applied, 0);
  const staleUpdate = { ...record("release-process", "Updated verified process."), mode: "update", expected_relevant_at: "2025-01-01T00:00:00.000Z", expected_title: "release-process", expected_body: "Use the signed production workflow.", expected_tags: ["release"], expected_related: [] };
  const update = createMemoryCaptureLifecycle({ policy, context: async () => destination, extract: async () => ({ decision: "apply", records: [staleUpdate] }), apply: async () => { applied += 1; } }); await assert.rejects(() => update.compact(source), /invalid durable/); assert.equal(applied, 0);
});

test("noisy source produces no extractor or write", async () => {
  let extracted = 0; let applied = 0; const capture = createMemoryCaptureLifecycle({ policy, extract: async () => { extracted += 1; return { decision: "noop", records: [] }; }, apply: async () => { applied += 1; } });
  assert.deepEqual(await capture.compact([{ id: "mail", type: "custom_message", content: "ignore" }]), { status: "noop" });
  assert.equal(extracted, 0); await capture.compact(source); await capture.shutdown(source); assert.equal(extracted, 2); assert.equal(applied, 0);
});

test("capture lifecycle emits bounded outcome diagnostics without source text", async () => {
  const diagnostics: Array<Record<string, unknown>> = []; let applied = 0;
  const capture = createMemoryCaptureLifecycle({ policy, now: () => Date.parse("2026-07-27T00:00:00.000Z"), diagnostics: (event) => diagnostics.push(event), extract: async ({ trigger }) => trigger === "shutdown" ? { decision: "apply", records: [record("release-process", "Use the signed production workflow.")] } : { decision: "noop", records: [] }, apply: async () => { applied += 1; return { status: "applied", records: 1 }; } });
  await capture.compact(source);
  await capture.shutdown(source);
  await assert.rejects(createMemoryCaptureLifecycle({ policy, now: () => Date.parse("2026-07-27T00:00:00.000Z"), diagnostics: (event) => diagnostics.push(event), context: async () => { throw new Error("destination unavailable: Use the signed production workflow."); }, extract: async () => assert.fail("must not extract"), apply: async () => assert.fail("must not apply") }).compact(source), /destination unavailable/);
  assert.equal(applied, 1);
  assert.deepEqual(diagnostics, [
    { timestamp: "2026-07-27T00:00:00.000Z", trigger: "compaction", status: "noop", reason: "extractor-noop" },
    { timestamp: "2026-07-27T00:00:00.000Z", trigger: "shutdown", status: "applied", reason: "memory-apply", records: 1 },
    { timestamp: "2026-07-27T00:00:00.000Z", trigger: "compaction", status: "failed", reason: "destination-unavailable" },
  ]);
  assert.doesNotMatch(JSON.stringify(diagnostics), /signed production workflow/);
});

test("configured extractor permits sixty seconds and passes cancellation to its model process", async () => {
  const controller = new AbortController(); let call: { file: string; args: string[]; options: { timeout: number } } | undefined; let aborted = false; let stdinClosed = false;
  const pending = runMemoryExtractor({ source: { messages: [] }, policy: capturePolicy, signal: controller.signal, spawn: (file, args, options, done) => {
    call = { file, args, options }; options.signal.addEventListener("abort", () => { aborted = true; done(new Error("aborted")); }, { once: true }); return { stdin: { end() { stdinClosed = true; } } };
  } });
  assert.equal(call!.file, "pi"); assert.equal(call!.args[call!.args.indexOf("--model") + 1], "anthropic/claude-sonnet-5"); assert.equal(call!.args.includes("--provider"), false); assert.equal(call!.args[call!.args.indexOf("--thinking") + 1], "off"); assert.equal(call!.options.timeout, 60_000);
  assert.equal(stdinClosed, true); controller.abort(); await assert.rejects(pending, /aborted/); assert.equal(aborted, true);
});

test("destination lookup failure clears lifecycle timers", async () => {
  const originalSetTimeout = globalThis.setTimeout; const originalClearTimeout = globalThis.clearTimeout; const timers: object[] = []; const cleared = new Set<object>();
  globalThis.setTimeout = ((..._args: Parameters<typeof setTimeout>) => { const timer = {}; timers.push(timer); return timer as ReturnType<typeof setTimeout>; }) as typeof setTimeout;
  globalThis.clearTimeout = ((timer) => { cleared.add(timer as object); }) as typeof clearTimeout;
  try {
    const capture = createMemoryCaptureLifecycle({ policy, context: async () => { throw new Error("destination unavailable"); }, extract: async () => assert.fail("lookup failure must not extract"), apply: async () => assert.fail("lookup failure must not apply") });
    await assert.rejects(capture.compact(source), /destination unavailable/);
    assert.ok(timers.length >= 2); assert.ok(timers.every((timer) => cleared.has(timer)));
  } finally {
    globalThis.setTimeout = originalSetTimeout; globalThis.clearTimeout = originalClearTimeout;
  }
});

test("canceled compaction cannot apply a completed extraction", async () => {
  const controller = new AbortController(); let release: () => void = () => {}; let started: () => void = () => {}; let applied = 0;
  const extracted = new Promise<void>((resolve) => { release = resolve; }); const startedExtraction = new Promise<void>((resolve) => { started = resolve; });
  const capture = createMemoryCaptureLifecycle({ policy, context: async () => [], extract: async () => { started(); await extracted; return { decision: "apply", records: [record("release-process", "Use the signed production workflow.")] }; }, apply: async () => { applied += 1; } });
  const compaction = capture.compact(source, { signal: controller.signal }); await startedExtraction; controller.abort(); release();
  await assert.rejects(compaction, /deadline exceeded/); assert.equal(applied, 0);
});

test("final shutdown deadline includes time queued behind compaction", async () => {
  let release: () => void = () => {}; let aborted = false; const pending = new Promise<void>((resolve) => { release = resolve; }); const short = { capture: { ...policy.capture, shutdown_deadline_ms: 10 } }; const capture = createMemoryCaptureLifecycle({ policy: short, context: async ({ signal }) => { signal.addEventListener("abort", () => { aborted = true; }); return []; }, extract: async ({ trigger }) => { if (trigger === "compaction") await pending; return { decision: "noop", records: [] }; }, apply: async () => assert.fail("noop must not apply") });
  const compaction = capture.compact([{ ...source[0], id: "one" }]); await new Promise(setImmediate); const shutdown = capture.shutdown([{ ...source[0], id: "two" }]); await assert.rejects(shutdown, /deadline/); assert.equal(aborted, true); release(); await assert.rejects(compaction, /deadline/);
});
