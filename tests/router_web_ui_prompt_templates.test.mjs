import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";
import { sendPromptTemplateCrud } from "../tools/router_web_ui.mjs";
import { parsePromptTemplateTokens, renderPromptTemplate } from "../web/router-ui/prompt-templates.js";

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
    await sendPromptTemplateCrud({ requestId: "create-1", operation: "create", payload: { name: "Review", body: "Check ${file}" } }, configured, (event) => sent.push(event));
    await sendPromptTemplateCrud({ requestId: "update-1", operation: "update", promptTemplateId: "generated-id", payload: { name: "Review 2", body: "Check ${file}" } }, configured, (event) => sent.push(event));
    await sendPromptTemplateCrud({ requestId: "delete-1", operation: "delete", promptTemplateId: "generated-id" }, configured, (event) => sent.push(event));
  } finally {
    globalThis.fetch = previousFetch;
  }
  assert.deepEqual(calls.map(({ url, options }) => [url, options.method]), [
    ["http://router.test/v1/prompt-templates", "GET"],
    ["http://router.test/v1/prompt-templates", "POST"],
    ["http://router.test/v1/prompt-templates/generated-id", "PATCH"],
    ["http://router.test/v1/prompt-templates/generated-id", "DELETE"],
  ]);
  assert.deepEqual(JSON.parse(calls[1].options.body), { name: "Review", body: "Check ${file}" });
  assert.deepEqual(JSON.parse(calls[2].options.body), { name: "Review 2", body: "Check ${file}" });
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
  assert.ok(app.includes('type: "prompt_template_crud"'));
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
