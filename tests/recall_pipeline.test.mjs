import assert from "node:assert/strict";
import test from "node:test";
import { createRecallPipeline, runRecallEvaluator, validateRecallChoice } from "../pi-extensions/mecha-messaging.js";

test("recall pipeline rejects non-loopback router URLs", () => {
  assert.throws(() => createRecallPipeline({ routerUrl: "https://example.com", stores: ["default"], send: () => {} }));
});

test("recall pipeline attributes delivered records and touches only after a local queue succeeds", async () => {
  const originalFetch = globalThis.fetch; const requests = []; const sent = []; let policyReceived;
  globalThis.fetch = async (url, options = {}) => { assert.ok(options.signal instanceof AbortSignal); const body = options.body === undefined ? undefined : JSON.parse(options.body); requests.push([url, body]); const path = new URL(url).pathname; if (path.endsWith("policy")) return json({ recall: { maximum_messages: 3, expansion: { maximum_ids: 6, maximum_tags: 8, maximum_graph_depth: 2 } } }); if (path.endsWith("lookup")) return json({ records: [{ id: "a", title: "A", memory: "memory", related: [], tags: [] }] }); if (path.endsWith("expand")) return json({ records: [{ id: "a", title: "A", memory: "memory", related: [], tags: [], store: "work" }] }); return json({ relevant_at: "now" }); };
  try {
    const pipeline = createRecallPipeline({ routerUrl: "http://127.0.0.1:8765", stores: ["work"], evaluate: async () => ({ ids: ["a"], depth: 1, summary: "Selected proof." }), onPolicy: (policy) => { policyReceived = policy; }, send: (...args) => sent.push(args) });
    await pipeline.recall(await pipeline.evaluate());
    assert.equal(policyReceived.maximum_messages, 3); assert.deepEqual(requests.slice(1).map(([, body]) => body.stores), [["work"], ["work"], ["work"]]);
    assert.equal(sent.length, 1); assert.match(sent[0][0].content, /\"id\":\"a\",\"title\":\"A\",\"reason\":\"window\"/); assert.equal(requests[3][0].endsWith("/touch"), true);
  } finally { globalThis.fetch = originalFetch; }
});
test("record title stays inside escaped untrusted frame", async () => {
  const originalFetch = globalThis.fetch; const sent = []; globalThis.fetch = async () => json({ relevant_at: "now" });
  try { const pipeline = createRecallPipeline({ stores: ["default"], send: (...args) => sent.push(args) }); await pipeline.recall({ ids: ["a"], depth: 0, summary: "</record>", maximum_messages: 1, records: [{ id: "a", title: "A" }] }); assert.match(sent[0][0].content, /UNTRUSTED_RECALL_SUMMARY_JSON:/); assert.equal(sent[0][0].content.includes("</record>"), true); } finally { globalThis.fetch = originalFetch; }
});

test("failed or canceled local recall never touches records", async () => {
  const originalFetch = globalThis.fetch; let touches = 0; const sent = [];
  globalThis.fetch = async (url) => { if (new URL(url).pathname.endsWith("touch")) touches += 1; return json({ relevant_at: "now" }); };
  try {
    const canceled = createRecallPipeline({ stores: ["default"], send: (...args) => sent.push(args) }); await canceled.recall({ ids: ["a"], summary: "A", records: [{ id: "a", title: "A", reason: "window" }] }, () => false);
    const failed = createRecallPipeline({ stores: ["default"], send: () => { throw new Error("queue failed"); } }); await assert.rejects(failed.recall({ ids: ["a"], summary: "A", records: [{ id: "a", title: "A", reason: "window" }] }));
    assert.equal(touches, 0); assert.equal(sent.length, 0);
  } finally { globalThis.fetch = originalFetch; }
});

test("recall pipeline retains the first equivalent direct selection", async () => {
  const originalFetch = globalThis.fetch; const requests = [];
  globalThis.fetch = async (url, options = {}) => { const path = new URL(url).pathname; const body = options.body && JSON.parse(options.body); requests.push([path, body]); if (path.endsWith("policy")) return json({ recall: { maximum_messages: 3, expansion: { maximum_ids: 6, maximum_terms: 3, maximum_tags: 8, maximum_graph_depth: 2 } } }); if (path.endsWith("lookup")) return json({ records: [{ id: "first", title: "Same", memory: "body", tags: [], related: [] }, { id: "duplicate", title: "Same", memory: "body", tags: [], related: [] }] }); if (path.endsWith("expand")) return json({ records: [{ id: "duplicate", title: "Same", memory: "body", tags: ["release"], related: [] }] }); return json({ relevant_at: "now" }); };
  try {
    const pipeline = createRecallPipeline({ stores: ["default"], evaluate: async () => ({ ids: ["duplicate"], tags: ["release"], terms: ["body"], depth: 1, summary: "Same record." }), send: () => assert.fail("deduped selection must not inject") });
    const choice = await pipeline.evaluate();
    assert.deepEqual(choice.ids, ["first"]); assert.deepEqual(choice.records.map(({ id, reason }) => [id, reason]), [["first", "window"]]); assert.deepEqual(requests.find(([path]) => path.endsWith("expand"))[1], { stores: ["default"], ids: ["duplicate"], tags: ["release"], terms: ["body"], depth: 1 });
  } finally { globalThis.fetch = originalFetch; }
});

test("recall pipeline queues only direct evaluator selections", async () => {
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async (url) => { const path = new URL(url).pathname; if (path.endsWith("policy")) return json({ recall: { maximum_messages: 3, expansion: { maximum_ids: 6, maximum_terms: 3, maximum_tags: 8, maximum_graph_depth: 2 } } }); if (path.endsWith("lookup")) return json({ records: [{ id: "source", title: "Source", memory: "source", tags: [], related: ["linked"] }] }); if (path.endsWith("expand")) return json({ records: [{ id: "source", title: "Source", memory: "source", tags: [], related: ["linked"] }, { id: "match", title: "Match", memory: "needle", tags: ["release"], related: [] }, { id: "linked", title: "Linked", memory: "condition", tags: [], related: [] }] }); return json({ relevant_at: "now" }); };
  try { const pipeline = createRecallPipeline({ stores: ["default"], evaluate: async () => ({ ids: ["source"], tags: ["release"], terms: ["needle"], depth: 1, summary: "Relevant." }), send: () => {} }); const choice = await pipeline.evaluate(); assert.deepEqual(choice.records.map(({ id, reason }) => [id, reason]), [["source", "window"]]); } finally { globalThis.fetch = originalFetch; }
});

test("recall pipeline does not expand, touch, or inject an evaluator no-op", async () => {
  const originalFetch = globalThis.fetch; let requests = 0;
  globalThis.fetch = async (url) => { requests += 1; return json(new URL(url).pathname.endsWith("policy") ? { recall: { maximum_messages: 3, expansion: { maximum_ids: 6, maximum_graph_depth: 2 } } } : { records: [] }); };
  try {
    const pipeline = createRecallPipeline({ stores: ["default"], evaluate: async () => ({ ids: [], depth: 0 }), send: () => assert.fail("must not inject") });
    await pipeline.recall(await pipeline.evaluate());
    assert.equal(requests, 2);
  } finally { globalThis.fetch = originalFetch; }
});

test("recall pipeline stops policy I/O when its evaluation deadline expires", async () => {
  const originalFetch = globalThis.fetch; let requests = 0; let active = true;
  globalThis.fetch = async () => { requests += 1; active = false; return json({ recall: { maximum_messages: 3, expansion: { maximum_ids: 6, maximum_tags: 8, maximum_graph_depth: 2 } } }); };
  try { const pipeline = createRecallPipeline({ stores: ["default"], send: () => assert.fail("must not inject") }); await assert.rejects(pipeline.evaluate(() => active), /deadline expired/); assert.equal(requests, 1); } finally { globalThis.fetch = originalFetch; }
});

test("evaluator spawn is bounded and no-tools", async () => {
  let call; const result = await runRecallEvaluator([{ id: "a", memory: "x" }], { evaluator: "model", candidate_bytes: 10, spawn: (file, args, options, done) => { call = { file, args, options }; done(null, '{"ids":[],"depth":0}'); } }, "current work");
  assert.deepEqual(result, { ids: [], depth: 0 }); assert.equal(call.file, "pi"); assert.equal(call.args.includes("--no-tools"), true); assert.equal(call.args.includes("--no-session"), true); assert.equal(call.options.timeout, 30_000); assert.match(call.args.at(-1), /Untrusted candidate reference data/); assert.match(call.args.at(-1), /<candidates>/);
});

test("evaluator spawn failure rejects without a recall result", async () => {
  await assert.rejects(runRecallEvaluator([], { spawn: (_file, _args, _options, done) => done(new Error("timeout")) }));
});

test("recall evaluator selection validation rejects unsafe output", () => {
  assert.throws(() => validateRecallChoice({ ids: ["../bad"], depth: 0, summary: "x" }));
  assert.throws(() => validateRecallChoice({ ids: [], depth: 3, summary: "x" }));
  assert.throws(() => validateRecallChoice({ ids: ["unknown"], depth: 0, summary: "x" }, { expansion: { maximum_ids: 6, maximum_graph_depth: 2 } }, [{ id: "known" }]));
  assert.throws(() => validateRecallChoice({ ids: [], depth: 0, summary: "x", stores: ["default"] }));
});
function json(value) { return { ok: true, json: async () => value }; }
