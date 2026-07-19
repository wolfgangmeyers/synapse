import assert from "node:assert/strict";
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { MemoryCatalogError, openMemoryCatalog, RECALL_POLICY } from "../harness/memory-catalog.js";

async function request(url, path, body) {
  return fetch(`${url}${path}`, { method: body === undefined ? "GET" : "POST", headers: body === undefined ? {} : { "content-type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
}

test("memory HTTP routes expose shared policy and forward only explicit selected stores", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-memory-router-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const calls = [];
  const catalog = {
    rebuild: () => 2,
    lookup: async (input) => { calls.push(["lookup", input]); if (!Array.isArray(input.stores) || input.stores.includes("disabled")) throw new MemoryCatalogError("invalid selected stores"); return [{ id: "default" }]; },
    expand: async (input) => { calls.push(["expand", input]); return []; },
    touch: (input) => { calls.push(["touch", input]); return "2026-07-13T00:00:00.000Z"; },
    apply: (input) => { calls.push(["apply", input]); if (input === null || typeof input !== "object" || input.invalid || input.operations?.some((operation) => !Object.hasOwn(operation, "expected_body") || !Array.isArray(operation.expected_related) || operation.expected_related.some((id) => typeof id !== "string" || !/^[a-z0-9][a-z0-9-]*$/.test(id)) || typeof operation.expected_body !== "string" || typeof operation.id !== "string" || typeof operation.body !== "string" || operation.body.length === 0)) throw new MemoryCatalogError("invalid capture operation"); return { status: "noop", records: 0 }; },
  };
  const router = createAgentRouter({ repository, memoryCatalog: catalog, port: 0 });
  const wsUrl = await router.listen(); const url = wsUrl.replace(/^ws:/, "http:").replace(/\/v1\/agents$/, "");
  t.after(async () => { await router.close(); repository.close(); await rm(directory, { recursive: true, force: true }); });

  const policy = await request(url, "/v1/memory/policy");
  assert.equal(policy.status, 200); assert.equal((await policy.json()).recall.maximum_messages, 3);
  const lookup = await request(url, "/v1/memory/lookup", { stores: ["default"], terms: ["literal"] });
  assert.equal(lookup.status, 200); assert.deepEqual(await lookup.json(), { records: [{ id: "default" }] });
  assert.deepEqual(calls, [["lookup", { stores: ["default"], terms: ["literal"] }]]);
  const expanded = await request(url, "/v1/memory/expand", { stores: ["default"], ids: ["default"], depth: 0 });
  assert.equal(expanded.status, 200); assert.deepEqual(await expanded.json(), { records: [] });
  const touched = await request(url, "/v1/memory/touch", { stores: ["default"], ids: ["default"] });
  assert.equal(touched.status, 200); assert.equal((await touched.json()).relevant_at, "2026-07-13T00:00:00.000Z");
  for (const payload of [{ invalid: true }, null, { operations: [{ mode: "update", id: 7, expected_body: "body", expected_related: [] }] }, { operations: [{ mode: "update", id: "id", expected_body: "body" }] }, { operations: [{ mode: "update", id: "id", expected_body: "body", expected_related: "id" }] }, { operations: [{ mode: "update", id: "id", expected_body: "body", expected_related: [7] }] }, { operations: [{ mode: "update", id: "id", expected_body: "body", expected_related: ["bad id"] }] }, { operations: [{ mode: "update", id: "id", expected_related: [] }] }, { operations: [{ mode: "update", id: "id", expected_body: 7, expected_related: [] }] }, { operations: [{ mode: "update", id: "id", body: "", expected_body: "body", expected_related: [] }] }]) { const malformed = await request(url, "/v1/memory/apply", payload); assert.equal(malformed.status, 400); }
  const rebuilt = await request(url, "/v1/memory/rebuild", {});
  assert.equal(rebuilt.status, 200); assert.equal((await rebuilt.json()).records, 2);
  const disabled = await request(url, "/v1/memory/lookup", { stores: ["disabled"] });
  assert.equal(disabled.status, 400);
  const missing = await request(url, "/v1/memory/lookup", { terms: ["literal"] });
  assert.equal(missing.status, 400);
});

test("memory policy returns built-in defaults without a catalog", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-memory-policy-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, port: 0 });
  const wsUrl = await router.listen(); const url = wsUrl.replace(/^ws:/, "http:").replace(/\/v1\/agents$/, "");
  t.after(async () => { await router.close(); repository.close(); await rm(directory, { recursive: true, force: true }); });

  const response = await request(url, "/v1/memory/policy");
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { recall: RECALL_POLICY });
  const lookup = await request(url, "/v1/memory/lookup", { stores: ["default"] });
  assert.equal(lookup.status, 404);
});

test("malformed memory apply requests leave a real catalog untouched before its queue", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-memory-router-real-"));
  const recordPath = join(directory, "default.md"); const indexPath = join(directory, "INDEX.md");
  await writeFile(recordPath, "---\nid: default\ntitle: Default\nrelevant_at: 2026-07-13T00:00:00.000Z\nrelated:\n---\n\ndefault body\n");
  await writeFile(indexPath, "# index\n");
  let queueRuns = 0; let writes = 0;
  const catalog = openMemoryCatalog({ kbRoot: directory, mutationHook: () => { queueRuns += 1; }, writeHook: () => { writes += 1; } });
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, memoryCatalog: catalog, port: 0 });
  const wsUrl = await router.listen(); const url = wsUrl.replace(/^ws:/, "http:").replace(/\/v1\/agents$/, "");
  t.after(async () => { await router.close(); repository.close(); await catalog.close(); await rm(directory, { recursive: true, force: true }); });

  const snapshot = { queueRuns, writes, record: await readFile(recordPath, "utf8"), index: await readFile(indexPath, "utf8") };
  for (const payload of [null, { store: "default", request_id: "shape", operations: [] }, { store: "default", request_id: "canonical", operations: [{ mode: "create", id: "new", title: "New\nTitle", body: "body", related: [] }] }, { store: "default", request_id: "date-only", operations: [{ mode: "update", id: "default", expected_relevant_at: "2026-07-13", expected_title: "Default", expected_body: "default body", expected_related: [], title: "Changed", body: "changed", related: [] }] }, { store: "default", request_id: "batch", operations: [{ mode: "create", id: "new", title: "New", body: "body", related: [] }, { mode: "create", id: "new", title: "Again", body: "body", related: [] }] }]) {
    const response = await request(url, "/v1/memory/apply", payload);
    assert.ok(response.status >= 400 && response.status < 500);
    assert.ok((await response.text()).length < 256);
  }
  assert.equal(queueRuns, snapshot.queueRuns);
  assert.equal(writes, snapshot.writes);
  assert.equal(await readFile(recordPath, "utf8"), snapshot.record);
  assert.equal(await readFile(indexPath, "utf8"), snapshot.index);
});

test("router close flushes deferred memory mutations", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-memory-router-close-"));
  const recordPath = join(directory, "default.md");
  await writeFile(recordPath, "---\nid: default\ntitle: Default\nrelevant_at: 2026-07-13T00:00:00.000Z\nrelated:\n---\n\ndefault body\n");
  await writeFile(join(directory, "INDEX.md"), "# Index\n");
  const catalog = openMemoryCatalog({ kbRoot: directory }); const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") }); const router = createAgentRouter({ repository, memoryCatalog: catalog, port: 0 });
  try {
    const wsUrl = await router.listen(); const url = wsUrl.replace(/^ws:/, "http:").replace(/\/v1\/agents$/, "");
    const response = await request(url, "/v1/memory/apply", { store: "default", request_id: "close", operations: [{ mode: "update", id: "default", expected_relevant_at: "2026-07-13T00:00:00.000Z", expected_title: "Default", expected_body: "default body", expected_related: [], title: "Changed", body: "changed", related: [] }] });
    assert.equal(response.status, 200); assert.match(await response.text(), /"applied"/);
    assert.match(await readFile(recordPath, "utf8"), /title: Default/);
    await router.close();
    assert.match(await readFile(recordPath, "utf8"), /title: Changed/);
  } finally { repository.close(); await catalog.close(); await rm(directory, { recursive: true, force: true }); }
});
