import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import {
  openMessageRepository,
  PromptTemplateCapacityError,
  PromptTemplateNameConflictError,
  PromptTemplateValidationError,
} from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";

const NOW = new Date("2030-01-01T00:00:00.000Z");
const JSON_HEADERS = { "content-type": "application/json" };

async function fixture(t) {
  const directory = await mkdtemp(join(tmpdir(), "mecha-prompt-templates-"));
  const databasePath = join(directory, "router.sqlite");
  const clock = () => new Date(NOW);
  let repository = openMessageRepository({ databasePath, clock });
  let router = createAgentRouter({ repository, clock, timeoutSweepIntervalMs: 60_000 });
  t.after(async () => {
    await router.close().catch(() => {});
    repository.close();
    await rm(directory, { recursive: true, force: true });
  });
  return {
    databasePath,
    get repository() { return repository; },
    get router() { return router; },
    async restart() {
      await router.close();
      repository.close();
      repository = openMessageRepository({ databasePath, clock });
      router = createAgentRouter({ repository, clock, timeoutSweepIntervalMs: 60_000 });
    },
  };
}

function template(overrides = {}) {
  return { name: "daily check", body: "Keep this body verbatim:\n${goal}", ...overrides };
}

async function httpUrl(router) {
  const url = new URL(await router.listen());
  url.protocol = "http:";
  return url;
}

async function request(base, path, options = {}) {
  return fetch(new URL(path, base), options);
}

test("repository prompt template CRUD preserves body text and generates IDs", async (t) => {
  const { repository } = await fixture(t);
  const created = repository.createPromptTemplate(template());

  assert.equal(created.created, true);
  assert.match(created.prompt_template.id, /^[0-9a-f-]{36}$/);
  assert.equal(created.prompt_template.name, "daily check");
  assert.equal(created.prompt_template.body, "Keep this body verbatim:\n${goal}");
  assert.equal(created.prompt_template.created_at, NOW.toISOString());
  assert.equal(created.prompt_template.updated_at, NOW.toISOString());
  assert.deepEqual(repository.promptTemplateFor(created.prompt_template.id), created.prompt_template);
  assert.deepEqual(repository.promptTemplates(), [created.prompt_template]);

  const updated = repository.updatePromptTemplate(created.prompt_template.id, { name: "updated", body: "  raw\ntext ${value}  " });
  assert.equal(updated.prompt_template.id, created.prompt_template.id);
  assert.equal(updated.prompt_template.body, "  raw\ntext ${value}  ");
  assert.equal(updated.prompt_template.created_at, NOW.toISOString());
  assert.equal(updated.prompt_template.updated_at, NOW.toISOString());

  assert.equal(repository.deletePromptTemplate(created.prompt_template.id), true);
  assert.equal(repository.promptTemplateFor(created.prompt_template.id), undefined);
  assert.equal(repository.deletePromptTemplate(created.prompt_template.id), false);
});

test("prompt template validation rejects NUL characters before SQLite", async (t) => {
  const { repository, router } = await fixture(t);
  const invalidValues = [
    { name: "\u0000daily check", body: "body" },
    { name: "name", body: "\u0000body" },
  ];
  const invalidIds = [
    "\uD800leading", "\uDC00leading",
    "interior\uD800id", "interior\uDC00id",
    "trailing\uD800", "trailing\uDC00",
  ];
  const validSurrogatePairId = "valid-😀-id";

  for (const invalid of invalidValues) {
    assert.throws(() => repository.createPromptTemplate(invalid), PromptTemplateValidationError);
  }
  assert.deepEqual(repository.promptTemplates(), []);
  for (const id of invalidIds) {
    assert.throws(
      () => repository.createPromptTemplate({ id, name: "invalid id", body: "body" }),
      PromptTemplateValidationError,
    );
  }

  const base = await httpUrl(router);
  for (const invalid of invalidValues) {
    const response = await request(base, "/v1/prompt-templates", {
      method: "POST",
      headers: JSON_HEADERS,
      body: JSON.stringify(invalid),
    });
    assert.equal(response.status, 400);
    assert.equal((await response.json()).error.code, "invalid_prompt_template");
  }
  assert.deepEqual(repository.promptTemplates(), []);
  for (const id of invalidIds) {
    const response = await request(base, "/v1/prompt-templates", {
      method: "POST",
      headers: JSON_HEADERS,
      body: JSON.stringify({ id, name: "invalid id", body: "body" }),
    });
    assert.equal(response.status, 400);
    assert.equal((await response.json()).error.code, "invalid_prompt_template");
  }
  const validPairResponse = await request(base, "/v1/prompt-templates", {
    method: "POST",
    headers: JSON_HEADERS,
    body: JSON.stringify({ id: validSurrogatePairId, name: "valid pair", body: "body" }),
  });
  assert.equal(validPairResponse.status, 201);
  assert.equal((await validPairResponse.json()).prompt_template.id, validSurrogatePairId);

  const created = repository.createPromptTemplate(template());
  for (const patch of [{ name: "\u0000updated" }, { body: "\u0000updated body" }]) {
    assert.throws(
      () => repository.updatePromptTemplate(created.prompt_template.id, patch),
      PromptTemplateValidationError,
    );
    const response = await request(base, `/v1/prompt-templates/${created.prompt_template.id}`, {
      method: "PATCH",
      headers: JSON_HEADERS,
      body: JSON.stringify(patch),
    });
    assert.equal(response.status, 400);
    assert.equal((await response.json()).error.code, "invalid_prompt_template");
  }
  assert.deepEqual(repository.promptTemplateFor(created.prompt_template.id), created.prompt_template);
});

test("prompt template repository validates fields, exact names, and collection capacity", async (t) => {
  const { repository } = await fixture(t);
  for (const invalid of [
    {},
    { name: "", body: "body" },
    { name: "   ", body: "body" },
    { name: "name", body: "" },
    { name: "name", body: "   " },
    { name: "x".repeat(129), body: "body" },
    { name: "name", body: "x".repeat(65_537) },
    { id: "", name: "name", body: "body" },
  ]) {
    assert.throws(() => repository.createPromptTemplate(invalid), PromptTemplateValidationError);
  }

  repository.createPromptTemplate(template({ name: "same", body: "one" }));
  assert.throws(
    () => repository.createPromptTemplate(template({ name: "same", body: "two" })),
    PromptTemplateNameConflictError,
  );
  assert.equal(repository.createPromptTemplate(template({ name: "SAME", body: "two" })).created, true);

  for (let index = repository.promptTemplates().length; index < 100; index += 1) {
    repository.createPromptTemplate(template({ name: `template-${index}` }));
  }
  assert.equal(repository.promptTemplates().length, 100);
  assert.throws(
    () => repository.createPromptTemplate(template({ name: "first-over-capacity" })),
    PromptTemplateCapacityError,
  );
  assert.throws(
    () => repository.updatePromptTemplate(repository.promptTemplates()[0].id, { name: "   " }),
    PromptTemplateValidationError,
  );
  assert.throws(
    () => repository.updatePromptTemplate(repository.promptTemplates()[0].id, { surprise: true }),
    PromptTemplateValidationError,
  );
});

test("prompt template REST routes support CRUD and typed errors", async (t) => {
  const { repository, router } = await fixture(t);
  const base = await httpUrl(router);

  const empty = await request(base, "/v1/prompt-templates");
  assert.equal(empty.status, 200);
  assert.deepEqual(await empty.json(), { prompt_templates: [] });

  const createdResponse = await request(base, "/v1/prompt-templates", {
    method: "POST",
    headers: JSON_HEADERS,
    body: JSON.stringify(template()),
  });
  assert.equal(createdResponse.status, 201);
  assert.equal(createdResponse.headers.get("cache-control"), "no-store");
  const created = (await createdResponse.json()).prompt_template;
  assert.deepEqual(created, repository.promptTemplates()[0]);

  const conflict = await request(base, "/v1/prompt-templates", {
    method: "POST",
    headers: JSON_HEADERS,
    body: JSON.stringify(template({ body: "different" })),
  });
  assert.equal(conflict.status, 409);
  assert.equal((await conflict.json()).error.code, "prompt_template_name_conflict");

  const patchedResponse = await request(base, `/v1/prompt-templates/${created.id}`, {
    method: "PATCH",
    headers: JSON_HEADERS,
    body: JSON.stringify({ body: "patched\n${still-freeform}" }),
  });
  assert.equal(patchedResponse.status, 200);
  assert.equal((await patchedResponse.json()).prompt_template.body, "patched\n${still-freeform}");

  const fetched = await request(base, `/v1/prompt-templates/${created.id}`);
  assert.equal(fetched.status, 200);
  assert.equal((await fetched.json()).prompt_template.body, "patched\n${still-freeform}");

  const deleted = await request(base, `/v1/prompt-templates/${created.id}`, { method: "DELETE" });
  assert.equal(deleted.status, 204);
  assert.equal(deleted.headers.get("cache-control"), "no-store");
  assert.equal((await request(base, `/v1/prompt-templates/${created.id}`)).status, 404);

  for (const [method, path, body] of [
    ["POST", "/v1/prompt-templates", { name: "", body: "body" }],
    ["POST", "/v1/prompt-templates", { name: "name", body: "" }],
    ["PATCH", "/v1/prompt-templates/missing", { body: "body" }],
  ]) {
    const response = await request(base, path, {
      method,
      headers: JSON_HEADERS,
      body: JSON.stringify(body),
    });
    assert.equal(response.status, method === "PATCH" ? 404 : 400);
    if (method === "POST") assert.equal((await response.json()).error.code, "invalid_prompt_template");
  }

  const nonJson = await request(base, "/v1/prompt-templates", { method: "POST", body: "plain" });
  assert.equal(nonJson.status, 400);
  assert.equal((await nonJson.json()).error.code, "invalid_request");
  assert.equal(typeof repository.promptTemplates, "function");
});

test("prompt template capacity rejects the first item above 100 through HTTP", async (t) => {
  const { repository, router } = await fixture(t);
  for (let index = 0; index < 100; index += 1) {
    repository.createPromptTemplate(template({ name: `template-${index}` }));
  }
  const base = await httpUrl(router);
  const response = await request(base, "/v1/prompt-templates", {
    method: "POST",
    headers: JSON_HEADERS,
    body: JSON.stringify(template({ name: "template-100" })),
  });
  assert.equal(response.status, 409);
  assert.equal((await response.json()).error.code, "prompt_template_capacity_exceeded");
});

test("prompt templates persist after repository and router restart", async (t) => {
  const fixtureState = await fixture(t);
  const { repository, router, restart } = fixtureState;
  const base = await httpUrl(router);
  const response = await request(base, "/v1/prompt-templates", {
    method: "POST",
    headers: JSON_HEADERS,
    body: JSON.stringify(template({ body: "persisted\n${value}" })),
  });
  const saved = (await response.json()).prompt_template;

  await restart();
  const restartedBase = await httpUrl(fixtureState.router);
  const list = await request(restartedBase, "/v1/prompt-templates");
  assert.equal(list.status, 200);
  assert.deepEqual((await list.json()).prompt_templates, [saved]);
});

test("prompt templates accept portable imported identity and timestamp", async (t) => {
  const { repository, router } = await fixture(t);
  const id = "legacy/Punctuation?%# \u00e9";
  const updatedAt = "2029-12-31T23:59:59.999Z";
  const base = await httpUrl(router);
  const createdResponse = await request(base, "/v1/prompt-templates", {
    method: "POST",
    headers: JSON_HEADERS,
    body: JSON.stringify(template({ id, updated_at: updatedAt })),
  });
  assert.equal(createdResponse.status, 201);
  assert.deepEqual((await createdResponse.json()).prompt_template, {
    id,
    name: "daily check",
    body: template().body,
    created_at: NOW.toISOString(),
    updated_at: updatedAt,
  });

  const fetched = await request(base, `/v1/prompt-templates/${encodeURIComponent(id)}`);
  assert.equal(fetched.status, 200);
  assert.equal((await fetched.json()).prompt_template.id, id);
  assert.deepEqual(repository.promptTemplates()[0].updated_at, updatedAt);
});

test("prompt template timestamp clock consumption distinguishes normal and imported writes", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-prompt-template-clock-"));
  const clockValues = [
    "2030-01-01T00:00:01.000Z",
    "2030-01-01T00:00:02.000Z",
    "2030-01-01T00:00:03.000Z",
  ];
  let measuring = false;
  let clockCalls = 0;
  const clock = () => new Date(measuring ? clockValues[clockCalls++] : NOW);
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), clock });
  measuring = true;
  t.after(async () => {
    repository.close();
    await rm(directory, { recursive: true, force: true });
  });

  const normal = repository.createPromptTemplate(template({ name: "normal" })).prompt_template;
  const imported = repository.createPromptTemplate(template({
    id: "Imported ID / mixed-case",
    name: "imported",
    updated_at: "2028-01-01T00:00:00.123Z",
  })).prompt_template;
  const normalUpdated = repository.updatePromptTemplate(normal.id, { body: "normal update" }).prompt_template;
  const importedUpdated = repository.updatePromptTemplate(imported.id, {
    body: "imported update",
    updated_at: "2027-01-01T00:00:00.456Z",
  }).prompt_template;
  assert.throws(
    () => repository.createPromptTemplate(template({ name: "normal" })),
    PromptTemplateNameConflictError,
  );

  assert.equal(clockCalls, 3);
  assert.equal(normal.created_at, "2030-01-01T00:00:01.000Z");
  assert.equal(normal.updated_at, normal.created_at);
  assert.equal(imported.created_at, "2030-01-01T00:00:02.000Z");
  assert.equal(imported.updated_at, "2028-01-01T00:00:00.123Z");
  assert.equal(normalUpdated.updated_at, "2030-01-01T00:00:03.000Z");
  assert.equal(importedUpdated.updated_at, "2027-01-01T00:00:00.456Z");
});
