import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { RECALL_POLICY, openMemoryCatalog } from "../harness/memory-catalog.js";
import { createAgentRouter } from "../harness/router.js";
import { createMemoryCaptureLifecycle, runMemoryExtractor } from "../pi-extensions/memory-capture.js";

const policy = { capture: { ...RECALL_POLICY.capture, source_max_bytes: 4096, source_message_max_bytes: 1024 } };
const source = [{ id: "compact-tool", type: "message", message: { role: "toolResult", content: [{ type: "text", text: "Use the signed production workflow." }], isError: false } }];
const create = (id, body, related = []) => ({ mode: "create", id, title: id, body, tags: ["release"], related, evidence: { source_ids: ["compact-tool"] } });

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "memory-capture-e2e-")); await writeFile(join(root, "INDEX.md"), "# Index\n"); const catalog = openMemoryCatalog({ kbRoot: root }); const repository = openMessageRepository({ databasePath: join(root, "router.sqlite") }); const router = createAgentRouter({ repository, memoryCatalog: catalog, port: 0 }); const wsUrl = await router.listen(); const url = wsUrl.replace(/^ws:/, "http:").replace(/\/v1\/agents$/, ""); let closed = false;
  const close = async () => { if (closed) return; closed = true; await router.close(); repository.close(); };
  t.after(async () => { await close(); await rm(root, { recursive: true, force: true }); });
  return { root, close, url, catalog };
}

async function memoryRequest(url, path, body) {
  const response = await fetch(`${url}${path}`, { method: body === undefined ? "GET" : "POST", headers: body === undefined ? {} : { "content-type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
  assert.equal(response.status, 200); return response.json();
}

test("ordinary successful tool results capture through compaction and shutdown into a disposable KB router", async (t) => {
  const { root, close, url } = await fixture(t); const shutdownSource = [{ id: "shutdown-tool", type: "message", message: { role: "toolResult", content: [{ type: "text", text: "Verify /health before announcing completion." }], isError: false } }]; const compactRecord = create("release-process", "Use the signed production workflow."); const shutdownRecord = { ...create("release-health", "Verify /health before announcing completion."), evidence: { source_ids: ["shutdown-tool"] } }; let model;
  const extract = (input) => runMemoryExtractor({ ...input, spawn: (file, args, options, done) => { model = { file, args, options }; done(null, JSON.stringify({ decision: "apply", records: input.trigger === "compaction" ? [compactRecord] : [shutdownRecord] })); return { kill() {} }; } });
  const capture = createMemoryCaptureLifecycle({ policy, extract, context: async () => (await memoryRequest(url, "/v1/memory/lookup", { stores: ["default"] })).records, apply: (input) => memoryRequest(url, "/v1/memory/apply", input) });
  assert.deepEqual(await capture.compact(source), { status: "applied", records: 1 });
  assert.deepEqual(await capture.shutdown(shutdownSource), { status: "applied", records: 1 });
  assert.equal(model.file, "pi"); assert.equal(model.args[model.args.indexOf("--model") + 1], "openai-codex/gpt-5.6-sol"); assert.equal(model.options.timeout, 60_000); assert.match(model.args.at(-1), /successful Pi toolResult/);
  const stored = await memoryRequest(url, "/v1/memory/lookup", { stores: ["default"] }); assert.deepEqual(stored.records.map(({ id }) => id), ["release-health", "release-process"]); assert.ok(Date.parse(stored.records[0].relevant_at) >= Date.parse(stored.records[1].relevant_at)); assert.doesNotMatch(JSON.stringify(stored), /evidence|compact-tool|shutdown-tool/i);
  await close(); const compactText = await readFile(join(root, "release-process.md"), "utf8"); const shutdownText = await readFile(join(root, "release-health.md"), "utf8"); assert.match(compactText, /tags:\n  - release\nrelated:/); assert.match(shutdownText, /Verify \/health before announcing completion\./); assert.doesNotMatch(`${compactText}\n${shutdownText}`, /evidence|compact-tool|shutdown-tool/i);
});

test("adversarial fake extractor stale, unverified, noisy, and one-invalid-record batches never write", async (t) => {
  const { root, catalog } = await fixture(t); const invalid = [
    { decision: "noop", records: [] },
    { decision: "apply", records: [create("stale-status", "Current task status only.", [])] },
    { decision: "apply", records: [{ ...create("raw-summary", "# copied raw summary", []) }] },
    { decision: "apply", records: [create("good", "Verified durable fact."), { ...create("bad", "Verified durable fact."), tags: ["Bad Tag"] }] },
  ];
  for (const output of invalid) { const capture = createMemoryCaptureLifecycle({ policy, extract: async () => output, apply: (input) => catalog.apply(input) }); if (output.decision === "noop") assert.deepEqual(await capture.compact(source), { status: "noop" }); else await assert.rejects(() => capture.compact(source)); assert.equal((await catalog.lookup({ stores: ["default"] })).length, 0); }
  const customOnly = createMemoryCaptureLifecycle({ policy, extract: async () => assert.fail("noise must not reach extractor"), apply: () => assert.fail("noise must not write") }); assert.deepEqual(await customOnly.compact([{ id: "mail", type: "custom_message", content: "unverified assertion" }]), { status: "noop" }); assert.equal((await catalog.lookup({ stores: ["default"] })).length, 0); assert.equal(await (await import("node:fs/promises")).readdir(root).then((files) => files.some((file) => file.endsWith(".md") && file !== "INDEX.md")), false);
});
