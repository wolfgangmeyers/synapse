import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { RECALL_POLICY, openMemoryCatalog } from "../harness/memory-catalog.js";
import { createMemoryCaptureLifecycle, runMemoryExtractor } from "../pi-extensions/memory-capture.js";

const policy = { capture: { ...RECALL_POLICY.capture, source_max_bytes: 4096, source_message_max_bytes: 1024 } };
const source = [{ id: "verified-tool", role: "toolResult", details: { memory_validation: "verified" }, content: "Use the signed production workflow. Verify /health before announcing completion." }];
const create = (id, body, related = []) => ({ mode: "create", id, title: id, body, tags: ["release"], related, evidence: { source_ids: ["verified-tool"] } });

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "memory-capture-e2e-")); await writeFile(join(root, "INDEX.md"), "# Index\n"); const catalog = openMemoryCatalog({ kbRoot: root });
  t.after(async () => { try { await catalog.close(); } catch {} await rm(root, { recursive: true, force: true }); });
  return { root, catalog };
}

test("configured extractor captures, retains, and recalls tagged linked chunks", async (t) => {
  const { root, catalog } = await fixture(t); const records = [create("release-process", "Use the signed production workflow."), create("release-health", "Verify /health before announcing completion.", ["release-process"])]; let model;
  const extract = (input) => runMemoryExtractor({ ...input, spawn: (file, args, options, done) => { model = { file, args, options }; done(null, JSON.stringify({ decision: "apply", records })); return { kill() {} }; } });
  const capture = createMemoryCaptureLifecycle({ policy, extract, apply: (input) => catalog.apply(input) });
  assert.deepEqual(await capture.compact(source), { status: "applied", records: 2 });
  assert.equal(model.file, "pi"); assert.equal(model.args[model.args.indexOf("--model") + 1], "openai-codex/gpt-5.6-sol"); assert.equal(model.options.timeout, 60_000); assert.match(model.args.at(-1), /\{"decision":"noop","records":\[\]\}/);
  const stored = await catalog.lookup({ stores: ["default"], terms: ["signed"] }); assert.deepEqual(stored.map(({ id }) => id), ["release-process"]);
  await catalog.close(); const original = await readFile(join(root, "release-process.md"), "utf8"); assert.match(original, /tags:\n  - release\nrelated:/); assert.doesNotMatch(original, /evidence|verified-tool/i); assert.doesNotMatch(JSON.stringify(stored), /evidence|verified-tool/i);
  const replay = createMemoryCaptureLifecycle({ policy, extract, apply: (input) => catalog.apply(input) }); assert.deepEqual(await replay.compact(source), { status: "noop", records: 0 }); await catalog.close(); assert.equal(await readFile(join(root, "release-process.md"), "utf8"), original);
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
