import assert from "node:assert/strict";
import { writeFileSync } from "node:fs";
import { mkdtemp, mkdir, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { MemoryCatalogError, openMemoryCatalog } from "../harness/memory-catalog.js";

async function fixture({ edgeBudget, mutationHook, writeHook } = {}) {
  const root = await mkdtemp(join(tmpdir(), "mecha-memory-"));
  await mkdir(join(root, "stores", "work"), { recursive: true });
  await mkdir(join(root, "stores", "empty"));
  await writeFile(join(root, "default.md"), record("default", "Default", "default body", ["work"]));
  await writeFile(join(root, "stores", "work", "work.md"), record("work", "Work", "work body", ["default"]));
  await writeFile(join(root, "INDEX.md"), "# Index\n");
  await writeFile(join(root, "legacy.md"), "# legacy\n");
  return { root, catalog: openMemoryCatalog({ kbRoot: root, edgeBudget, mutationHook, writeHook }) };
}
function record(id, title, body, related = [], relevantAt = "2026-07-13T00:00:00.000Z") { return `---\nid: ${id}\ntitle: ${title}\nrelevant_at: ${relevantAt}\nrelated:\n${related.map((item) => `  - ${item}\n`).join("")}---\n\n${body}\n`; }
function taggedRecord(id, title, body, tags, related = []) { return `---\nid: ${id}\ntitle: ${title}\nrelevant_at: 2026-07-13T00:00:00.000Z\ntags:\n${tags.map((item) => `  - ${item}\n`).join("")}related:\n${related.map((item) => `  - ${item}\n`).join("")}---\n\n${body}\n`; }
async function dispose(catalog, root) { await catalog.close(); await rm(root, { recursive: true, force: true }); }
const updateDefault = (body = "changed") => ({ mode: "update", id: "default", expected_relevant_at: "2026-07-13T00:00:00.000Z", expected_title: "Default", expected_body: "default body", expected_related: ["work"], title: "Changed", body, related: ["work"] });
const create = (id, title = id, body = "body", related = []) => ({ mode: "create", id, title, body, related });

test("startup hydration is ordered, excludes noncanonical files, and does not disclose paths", async () => {
  const { root, catalog } = await fixture();
  try {
    await writeFile(join(root, "newer.md"), record("newer", "Newer", "new", [], "2027-07-13T00:00:00.000Z"));
    assert.deepEqual((await catalog.lookup({ stores: ["default", "work"] })).map(({ id }) => id), ["default", "work"]);
    assert.deepEqual((await catalog.rebuild()).valueOf(), 3);
    const records = await catalog.lookup({ stores: ["default", "work"] });
    assert.deepEqual(records.map(({ id }) => id), ["newer", "default", "work"]);
    assert.equal(records.some((item) => Object.hasOwn(item, "path")), false);
  } finally { await dispose(catalog, root); }
});

test("lookup and expansion use hydrated records with bounded predicates and graph traversal", async () => {
  const { root, catalog } = await fixture({ edgeBudget: 1 });
  try {
    await writeFile(join(root, "match.md"), taggedRecord("match", "Release", "needle", ["release"], ["default"]));
    await catalog.rebuild();
    assert.deepEqual((await catalog.lookup({ stores: ["default"], terms: ["needle"] })).map(({ id }) => id), ["match"]);
    assert.deepEqual((await catalog.expand({ stores: ["default", "work"], ids: ["default"], depth: 2 })).map(({ id }) => id), ["default", "work"]);
    assert.deepEqual((await catalog.expand({ stores: ["default"], ids: [], tags: ["release"], terms: ["needle"], depth: 1 })).map(({ id }) => id), ["default", "match"]);
  } finally { await dispose(catalog, root); }
});

test("apply changes memory immediately and defers canonical record and index writes until close", async () => {
  const { root, catalog } = await fixture();
  try {
    const before = await readFile(join(root, "default.md"), "utf8");
    assert.deepEqual(await catalog.apply({ store: "default", request_id: "immediate", operations: [updateDefault()] }), { status: "applied", records: 1 });
    assert.equal((await catalog.lookup({ stores: ["default"] }))[0].title, "Changed");
    assert.equal(await readFile(join(root, "default.md"), "utf8"), before);
    await catalog.close();
    assert.match(await readFile(join(root, "default.md"), "utf8"), /title: Changed/);
    assert.match(await readFile(join(root, "INDEX.md"), "utf8"), /\[Changed\]\(default\.md\)/);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("captured index labels follow a record title update after each flush", async () => {
  const { root, catalog } = await fixture();
  try {
    await catalog.apply({ store: "default", request_id: "before", operations: [create("renamed", "Before")] });
    await catalog.close();
    assert.match(await readFile(join(root, "INDEX.md"), "utf8"), /\[Before\]\(renamed\.md\)/);
    const current = (await catalog.lookup({ stores: ["default"] })).find(({ id }) => id === "renamed");
    await catalog.apply({ store: "default", request_id: "after", operations: [{ mode: "update", id: "renamed", expected_relevant_at: current.relevant_at, expected_title: "Before", expected_body: "body", expected_related: [], title: "After", body: "body", related: [] }] });
    await catalog.close();
    const index = await readFile(join(root, "INDEX.md"), "utf8"); assert.match(index, /\[After\]\(renamed\.md\)/); assert.doesNotMatch(index, /\[Before\]\(renamed\.md\)/);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("separate mutations coalesce into one deferred index flush", async () => {
  let indexWrites = 0; const { root, catalog } = await fixture({ writeHook: ({ index }) => { if (index) indexWrites += 1; } });
  try {
    await catalog.apply({ store: "default", request_id: "one", operations: [create("one", "One")] });
    await catalog.apply({ store: "default", request_id: "two", operations: [create("two", "Two")] });
    assert.equal(indexWrites, 0);
    await new Promise((resolve) => setTimeout(resolve, 80));
    assert.equal(indexWrites, 1);
    assert.match(await readFile(join(root, "one.md"), "utf8"), /title: One/);
    assert.match(await readFile(join(root, "INDEX.md"), "utf8"), /\[Two\]\(two\.md\)/);
  } finally { await dispose(catalog, root); }
});

test("a failed deferred flush gets one safe retry and preserves pending state for shutdown", async () => {
  let fail = true; const { root, catalog } = await fixture({ writeHook: () => { if (fail) { fail = false; throw new Error("temporary flush failure"); } } });
  try {
    await catalog.apply({ store: "default", request_id: "retry", operations: [create("retry", "Retry")] });
    await new Promise((resolve) => setTimeout(resolve, 120));
    assert.match(await readFile(join(root, "retry.md"), "utf8"), /title: Retry/);
    await catalog.close();
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("touch updates recency in memory immediately and preserves legacy frontmatter on shutdown flush", async () => {
  const { root, catalog } = await fixture();
  try {
    const timestamp = await catalog.touch({ stores: ["default"], ids: ["default"] });
    assert.equal((await catalog.lookup({ stores: ["default"] }))[0].relevant_at, timestamp);
    await catalog.close();
    const saved = await readFile(join(root, "default.md"), "utf8");
    assert.match(saved, new RegExp(`relevant_at: ${timestamp}`));
    assert.doesNotMatch(saved, /\ntags:/);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("CAS and selected-store validation use current in-memory state", async () => {
  const { root, catalog } = await fixture();
  try {
    await assert.rejects(() => catalog.touch({ stores: ["default"], ids: ["work"] }), MemoryCatalogError);
    await catalog.apply({ store: "default", request_id: "first", operations: [updateDefault()] });
    await assert.rejects(() => catalog.apply({ store: "default", request_id: "stale", operations: [updateDefault("again")] }), /capture conflict/);
  } finally { await dispose(catalog, root); }
});

test("conflicting applies serialize through the mutation gate against current in-memory state", async () => {
  let release; const blocked = new Promise((resolve) => { release = resolve; }); let runs = 0;
  const { root, catalog } = await fixture({ mutationHook: async () => { if (++runs === 1) await blocked; } });
  try {
    const first = catalog.apply({ store: "default", request_id: "first", operations: [updateDefault()] });
    const second = catalog.apply({ store: "default", request_id: "second", operations: [updateDefault("again")] });
    await Promise.resolve(); assert.equal(runs, 1); release();
    await first; await assert.rejects(() => second, /capture conflict/);
    assert.equal((await catalog.lookup({ stores: ["default"] }))[0].title, "Changed");
  } finally { await dispose(catalog, root); }
});

test("invalid apply input never enters the mutation queue", async () => {
  let runs = 0; const { root, catalog } = await fixture({ mutationHook: () => { runs += 1; } });
  try {
    const before = runs;
    await assert.rejects(() => catalog.apply({ store: "default", request_id: "bad", operations: [{ ...create("new"), title: "New\nTitle" }] }), /invalid capture apply/);
    assert.equal(runs, before);
  } finally { await dispose(catalog, root); }
});

test("explicit rebuild is the only path that observes an out-of-band canonical edit", async () => {
  const { root, catalog } = await fixture();
  try {
    await writeFile(join(root, "default.md"), record("default", "External", "external", ["work"]));
    assert.equal((await catalog.lookup({ stores: ["default"] }))[0].title, "Default");
    await catalog.rebuild();
    assert.equal((await catalog.lookup({ stores: ["default"] }))[0].title, "External");
  } finally { await dispose(catalog, root); }
});

test("flush rejects out-of-band drift without overwriting it", async () => {
  const { root, catalog } = await fixture();
  try {
    await catalog.apply({ store: "default", request_id: "drift", operations: [updateDefault()] });
    await writeFile(join(root, "default.md"), record("default", "External", "external", ["work"]));
    await assert.rejects(() => catalog.close(), /capture conflict/);
    assert.match(await readFile(join(root, "default.md"), "utf8"), /title: External/);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("record write failures roll back the complete batch before reporting failure", async () => {
  let writes = 0; const { root, catalog } = await fixture({ writeHook: () => { if (++writes === 2) throw new Error("later failure"); } });
  try {
    await catalog.apply({ store: "default", request_id: "batch", operations: [create("one", "One"), create("two", "Two")] });
    await assert.rejects(() => catalog.close(), /later failure/);
    assert.equal((await readFile(join(root, "INDEX.md"), "utf8")), "# Index\n");
    await assert.rejects(() => readFile(join(root, "one.md"), "utf8"));
    await assert.rejects(() => readFile(join(root, "two.md"), "utf8"));
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("index write failures restore records and preserve the pending batch for a later flush", async () => {
  let fail = true; const { root, catalog } = await fixture({ writeHook: ({ index }) => { if (index && fail) throw new Error("index failure"); } });
  try {
    await catalog.apply({ store: "default", request_id: "index", operations: [create("new", "New")] });
    await assert.rejects(() => catalog.close(), /index failure/);
    await assert.rejects(() => readFile(join(root, "new.md"), "utf8"));
    fail = false; await catalog.close();
    assert.match(await readFile(join(root, "new.md"), "utf8"), /title: New/);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("flush rejects symlinked destinations before mutating memory or disk", async () => {
  const { root, catalog } = await fixture();
  try {
    await symlink(join(root, "default.md"), join(root, "new.md"));
    await assert.rejects(() => catalog.apply({ store: "default", request_id: "link", operations: [create("new", "New")] }), /unsafe capture path/);
    assert.equal((await catalog.lookup({ stores: ["default"] })).some(({ id }) => id === "new"), false);
  } finally { await dispose(catalog, root); }
});

test("tagged records retain canonical tags through deferred capture", async () => {
  const { root, catalog } = await fixture();
  try {
    await catalog.apply({ store: "default", request_id: "tagged", operations: [{ ...create("tagged", "Tagged", "A durable fact."), tags: ["release", "alpha"] }] });
    await catalog.close();
    assert.match(await readFile(join(root, "tagged.md"), "utf8"), /tags:\n  - alpha\n  - release\nrelated:/);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test("configured KB roots reached through symlinked ancestors reject flushes", async () => {
  const { root, catalog } = await fixture(); const linked = `${root}-link`;
  try {
    await catalog.close(); await symlink(root, linked);
    const linkedCatalog = openMemoryCatalog({ kbRoot: linked });
    await assert.rejects(() => linkedCatalog.apply({ store: "default", request_id: "ancestor", operations: [create("new", "New")] }), /unsafe capture path/);
  } finally { await rm(linked, { force: true }); await rm(root, { recursive: true, force: true }); }
});
