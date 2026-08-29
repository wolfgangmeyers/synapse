import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { sendPromptTemplateCrud } from "../tools/router_web_ui.mjs";
import {
  parsePromptTemplateImport,
  parsePromptTemplateTokens,
  promptTemplateExportPayload,
  renderPromptTemplate,
} from "../web/router-ui/prompt-templates.js";

const root = new URL("../", import.meta.url);
const read = (path) => readFile(new URL(path, root), "utf8");

test("prompt tokens preserve first-use order, repetition, case, and malformed text", () => {
  assert.deepEqual(
    parsePromptTemplateTokens("${First} ${second} ${First} ${SECOND} ${} ${incomplete"),
    ["First", "second", "SECOND"],
  );
  assert.equal(
    renderPromptTemplate("Hello ${First}; ${second}; ${First}; ${SECOND}; ${}; ${incomplete}; ${missing}", {
      First: "A$1",
      second: "B$$2",
      SECOND: "$& literal",
    }),
    "Hello A$1; B$$2; A$1; $& literal; ${}; ${incomplete}; ${missing}",
  );
});

test("prompt template import/export format preserves names and bodies", () => {
  const templates = [
    { name: "Review", body: "Check ${File}" },
    { name: "Review lowercase", body: "Check ${file}" },
  ];
  const payload = promptTemplateExportPayload(templates);
  assert.deepEqual(payload, {
    format: "mecha-prompt-templates",
    version: 1,
    templates,
  });
  assert.deepEqual(parsePromptTemplateImport(JSON.stringify(payload)), templates);
  assert.deepEqual(promptTemplateExportPayload(null), {
    format: "mecha-prompt-templates",
    version: 1,
    templates: [],
  });
  assert.deepEqual(promptTemplateExportPayload(undefined), {
    format: "mecha-prompt-templates",
    version: 1,
    templates: [],
  });
  assert.deepEqual(promptTemplateExportPayload({ name: "not an array" }), {
    format: "mecha-prompt-templates",
    version: 1,
    templates: [],
  });
  assert.throws(() => parsePromptTemplateImport("{"), /valid prompt template JSON file/);
  assert.throws(
    () => parsePromptTemplateImport(JSON.stringify({ ...payload, templates: [{ name: "bad\u0000name", body: "body" }] })),
    /valid name and body/,
  );
  assert.throws(
    () => parsePromptTemplateImport(JSON.stringify({ ...payload, templates: [{ name: "n".repeat(129), body: "body" }] })),
    /valid name and body/,
  );
  assert.throws(
    () => parsePromptTemplateImport(JSON.stringify({ ...payload, templates: [{ name: "name", body: "b".repeat(65 * 1024) }] })),
    /valid name and body/,
  );
  assert.throws(
    () => parsePromptTemplateImport(
      JSON.stringify({ ...payload, templates: [{ name: "name", body: "body\u0000" }] }),
    ),
    /valid name and body/,
  );
  assert.throws(
    () => parsePromptTemplateImport(JSON.stringify({ ...payload, templates: [{ name: "Review", body: "one" }, { name: "Review", body: "two" }] })),
    /duplicate template name/,
  );
  assert.throws(
    () => parsePromptTemplateImport(JSON.stringify({ ...payload, templates: [{ name: "", body: "body" }] })),
    /valid name and body/,
  );
  assert.throws(
    () => parsePromptTemplateImport(JSON.stringify({ format: "other", version: 1, templates: [] })),
    /valid prompt template export/,
  );
});

test("prompt template export preserves portable metadata and rejects invalid metadata", () => {
  const updatedAt = "2026-08-25T12:34:56.789Z";
  assert.deepEqual(promptTemplateExportPayload([
    {
      id: "legacy/ID%? # é",
      name: "Review",
      body: "Check ${File}",
      created_at: "server-only",
      updated_at: updatedAt,
    },
    { id: "without-update", name: "No timestamp", body: "Body", updated_at: "" },
    { name: "No metadata", body: "Body", updated_at: "not-a-timestamp" },
  ]), {
    format: "mecha-prompt-templates",
    version: 1,
    templates: [
      { id: "legacy/ID%? # é", name: "Review", body: "Check ${File}", updated_at: updatedAt },
      { id: "without-update", name: "No timestamp", body: "Body" },
      { name: "No metadata", body: "Body" },
    ],
  });
  assert.deepEqual(parsePromptTemplateImport(JSON.stringify({
    format: "mecha-prompt-templates",
    version: 1,
    templates: [{ id: "legacy/ID%? # é", name: "Review", body: "Body", updated_at: updatedAt }],
  })), [{ id: "legacy/ID%? # é", name: "Review", body: "Body", updated_at: updatedAt }]);
  for (const value of [null, "", "2026-08-25T12:34:56Z", "2026-08-25T05:34:56.789-07:00", "2026-02-29T12:34:56.789Z", 123]) {
    assert.throws(() => parsePromptTemplateImport(JSON.stringify({
      format: "mecha-prompt-templates", version: 1,
      templates: [{ name: "Review", body: "Body", updated_at: value }],
    })), /valid metadata/);
  }
  for (const value of [null, "", "a".repeat(1025), `a\u0000b`, "bad\uD800id", "bad\uDC00id"]) {
    assert.throws(() => parsePromptTemplateImport(JSON.stringify({
      format: "mecha-prompt-templates", version: 1,
      templates: [{ id: value, name: "Review", body: "Body" }],
    })), /valid metadata/);
  }
  assert.throws(() => parsePromptTemplateImport(JSON.stringify({
    format: "mecha-prompt-templates", version: 1,
    templates: [{ name: "Review", body: "Body", created_at: null }],
  })), /valid metadata/);
  assert.throws(() => parsePromptTemplateImport(JSON.stringify({
    format: "mecha-prompt-templates", version: 1,
    templates: [
      { id: "same", name: "Review", body: "Body" },
      { id: "same", name: "Other", body: "Body" },
    ],
  })), /duplicate template id/);
  assert.throws(() => parsePromptTemplateImport(JSON.stringify({
    format: "mecha-prompt-templates", version: 1,
    templates: [{ name: "Review", body: "Body", extra: true }],
  })), /valid metadata/);
});

test("prompt template relay maps CRUD commands to the REST routes", async () => {
  const calls = [];
  const sent = [];
  const previousFetch = globalThis.fetch;
  globalThis.fetch = async (url, options) => {
    calls.push({ url: String(url), options });
    return new Response(JSON.stringify({ prompt_template: { id: "generated-id" } }), { status: 200, headers: { "content-type": "application/json" } });
  };
  try {
    const configured = { routerUrl: "http://router.test", agentId: "web-agent" };
    await sendPromptTemplateCrud({ requestId: "list-1", operation: "list" }, configured, (event) => sent.push(event));
    await sendPromptTemplateCrud({ requestId: "create-1", operation: "create", payload: { id: "legacy/ID%? # é", name: "Review", body: "Check ${file}", updated_at: "2026-08-25T12:34:56.789Z" } }, configured, (event) => sent.push(event));
    await sendPromptTemplateCrud({ requestId: "update-1", operation: "update", promptTemplateId: "legacy/ID%? # é", payload: { id: "must-be-stripped", name: "Review 2", body: "Check ${file}", updated_at: "2026-08-25T12:34:56.789Z" } }, configured, (event) => sent.push(event));
    await sendPromptTemplateCrud({ requestId: "delete-1", operation: "delete", promptTemplateId: "legacy/ID%? # é" }, configured, (event) => sent.push(event));
    await assert.rejects(
      () => sendPromptTemplateCrud({ requestId: "bad-id", operation: "delete", promptTemplateId: "bad\uD800id" }, configured, () => {}),
      /Prompt template ID is invalid/,
    );
    await assert.rejects(
      () => sendPromptTemplateCrud({ requestId: "bad-payload", operation: "create", payload: { id: "bad\uDC00id", name: "Review", body: "Body" } }, configured, () => {}),
      /Prompt template ID is invalid/,
    );
  } finally {
    globalThis.fetch = previousFetch;
  }
  assert.deepEqual(calls.map(({ url, options }) => [url, options.method]), [
    ["http://router.test/v1/prompt-templates", "GET"],
    ["http://router.test/v1/prompt-templates", "POST"],
    ["http://router.test/v1/prompt-templates/legacy%2FID%25%3F%20%23%20%C3%A9", "PATCH"],
    ["http://router.test/v1/prompt-templates/legacy%2FID%25%3F%20%23%20%C3%A9", "DELETE"],
  ]);
  assert.deepEqual(JSON.parse(calls[1].options.body), { id: "legacy/ID%? # é", name: "Review", body: "Check ${file}", updated_at: "2026-08-25T12:34:56.789Z" });
  assert.deepEqual(JSON.parse(calls[2].options.body), { name: "Review 2", body: "Check ${file}", updated_at: "2026-08-25T12:34:56.789Z" });
  assert.equal(calls[3].options.body, undefined);
  assert.deepEqual(sent.map((event) => event.type), [
    "prompt_template_crud_result", "prompt_template_crud_result", "prompt_template_crud_result", "prompt_template_crud_result",
  ]);
});

test("prompt template controls and dialogs are wired into the WebUI", async () => {
  const html = await read("web/router-ui/index.html");
  const app = await read("web/router-ui/app.js");
  const css = await read("web/router-ui/styles.css");
  assert.match(html, /class="prompt-picker-group"[\s\S]*<span class="model-label">Prompt<\/span>[\s\S]*id="prompt-picker"/);
  assert.match(html, /id="prompt-current"[^>]*aria-controls="prompt-menu"/);
  assert.match(html, /id="prompt-manage"[^>]*aria-controls="prompt-templates-dialog"/);
  assert.match(html, /id="prompt-templates-dialog"[^>]*class="message-dialog prompt-dialog"/);
  assert.match(html, /id="prompt-use-dialog"[^>]*class="message-dialog prompt-dialog"/);
  assert.match(html, /id="close-prompt-templates-dialog"/);
  assert.ok(app.includes('type: "prompt_template_crud"'));
  assert.ok(app.includes("parsePromptTemplateImport"));
  assert.ok(app.includes("promptTemplateExportPayload"));
  assert.ok(app.includes('dataset.action = "import"'));
  assert.ok(app.includes('dataset.action = "export"'));
  assert.ok(app.includes("function renderPromptTemplateEditor()"));
  assert.ok(app.includes('sendPromptTemplateCrud("delete", template.id)'));
  assert.ok(app.includes('textContent = "Delete this template?"'));
  assert.ok(app.includes('textContent = "Paste"'));
  assert.ok(app.includes("setSelectionRange(caret, caret)"));
  assert.ok(app.includes('add.append(icon("fa-plus"))'));
  assert.ok(app.includes('add.setAttribute("aria-label", "New template")'));
  assert.ok(css.includes(".prompt-picker-group"));
  assert.ok(css.includes(".prompt-dialog-add"));
  assert.ok(css.includes(".prompt-manage-button"));
  assert.ok(css.includes(".prompt-template-editor-form textarea") && css.includes("resize: vertical"));
});
