import assert from "node:assert/strict";
import { once } from "node:events";
import { spawn } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { startRouterWebUi } from "../tools/router_web_ui.mjs";

const agentId = "prompt-template-peer";
const webAgentId = "wolfgang-web";
const templateName = "Shared review prompt";
const updatedTemplateName = "Shared review prompt updated";
const templateBody = "Review ${File} at ${line}; repeat ${File}; compare ${LINE}; again ${line}.";
const updatedTemplateBody = "Updated ${File} and ${line}; compare ${LINE}; repeat ${File}.";
const browserScriptTimeoutMs = 15_000;

// This is intentionally one serialized browser test. The prompt-template state is
// shared by the manager, picker, draft, and router-backed persistence checks.
test("router WebUI manages and uses shared prompt templates", { concurrency: false }, async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "router-prompt-templates-web-ui-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  let router = createAgentRouter({
    repository,
    host: "127.0.0.1",
    port: 0,
    timeoutSweepIntervalMs: 60_000,
  });
  const sockets = [];
  let ui;
  let browser;
  let webSocketSendSpyInstalled = false;

  try {
    const routerWsUrl = await router.listen();
    const routerHttpUrl = routerWsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    const peerSocket = await connectRouterAgent(routerWsUrl, agentId, sockets);
    ui = await startRouterWebUi({ router: routerHttpUrl, host: "127.0.0.1", port: 0, output: () => {} });

    try {
      browser = await startElectronDomBrowser(ui.httpUrl);
    } catch (error) {
      if (error.code === "ELECTRON_UNAVAILABLE") {
        t.skip(`Electron cannot launch in this test environment: ${error.message}`);
        return;
      }
      throw error;
    }

    await waitForDom(browser, () => Boolean(document.querySelector('.agent-row[data-agent-id="prompt-template-peer"]')));
    await browser.evaluate(`(() => document.querySelector('.agent-row[data-agent-id="${agentId}"]')?.click())()`);
    await waitForDom(browser, () => {
      const controls = document.querySelector("#prompt-controls");
      return Boolean(controls && !controls.hidden && !document.querySelector("#draft")?.disabled);
    });

    await browser.evaluate("(() => document.querySelector('#prompt-manage')?.click())()");
    await waitForDom(browser, () => {
      const dialog = document.querySelector("#prompt-templates-dialog");
      return Boolean(dialog?.open && dialog.querySelector(".prompt-dialog-empty")?.textContent.trim() === "No saved prompt templates.");
    });

    await browser.evaluate("(() => document.querySelector('#prompt-templates-dialog-body > button')?.click())()");
    await waitForDom(browser, () => Boolean(document.querySelector(".prompt-template-editor-form")));
    const unsavedTemplateName = "Draft template";
    const unsavedTemplateBody = "Draft body ${File}";
    await browser.evaluate(`(() => {
      const form = document.querySelector('.prompt-template-editor-form');
      const values = ${JSON.stringify({ name: unsavedTemplateName, body: unsavedTemplateBody })};
      for (const [name, value] of Object.entries(values)) {
        const field = form.elements.namedItem(name);
        field.value = value;
        field.dispatchEvent(new Event('input', { bubbles: true }));
      }
    })()`);
    await browser.evaluate(`(() => {
      const body = document.querySelector('.prompt-template-editor-form')?.elements.namedItem('body');
      body?.focus();
      body?.setSelectionRange(6, 10);
    })()`);
    const routerPort = Number(new URL(routerHttpUrl).port);
    await router.close();
    await waitForDom(browser, () => document.querySelector("#status-title")?.textContent === "Reconnecting");
    router = createAgentRouter({
      repository,
      host: "127.0.0.1",
      port: routerPort,
      timeoutSweepIntervalMs: 60_000,
    });
    const restartedRouterWsUrl = await router.listen();
    await connectRouterAgent(restartedRouterWsUrl, agentId, sockets);
    await waitForDom(browser, () => document.querySelector("#status-title")?.textContent.startsWith("Connected as"));
    await new Promise((resolve) => setTimeout(resolve, 1_000));
    const unsavedValuesAfterReconnect = await browser.evaluate(`(() => {
      const form = document.querySelector('.prompt-template-editor-form');
      const active = document.activeElement;
      return {
        name: form?.elements.namedItem('name')?.value,
        body: form?.elements.namedItem('body')?.value,
        focusedField: active?.name,
        selectionStart: active?.selectionStart,
        selectionEnd: active?.selectionEnd,
      };
    })()`);
    assert.deepEqual(unsavedValuesAfterReconnect, {
      name: unsavedTemplateName,
      body: unsavedTemplateBody,
      focusedField: "body",
      selectionStart: 6,
      selectionEnd: 10,
    });
    await fillTemplateEditor(browser, templateName, templateBody);
    await waitForPromptTemplate(browser, templateName, templateBody);

    await browser.evaluate("(() => document.querySelector('#prompt-templates-dialog')?.close())()");
    await browser.evaluate("(() => { location.reload(); return true; })()");
    await waitForDom(browser, () => Boolean(document.querySelector('.agent-row[data-agent-id="prompt-template-peer"]')), 10_000);
    await browser.evaluate(`(() => document.querySelector('.agent-row[data-agent-id="${agentId}"]')?.click())()`);
    await waitForDom(browser, () => {
      const controls = document.querySelector("#prompt-controls");
      return Boolean(controls && !controls.hidden && !document.querySelector("#prompt-manage")?.disabled);
    });
    await browser.evaluate("(() => document.querySelector('#prompt-manage')?.click())()");
    await waitForPromptTemplate(browser, templateName, templateBody);

    await browser.evaluate(`(() => {
      const item = [...document.querySelectorAll('.prompt-template-item')]
        .find((candidate) => candidate.querySelector('strong')?.textContent === ${JSON.stringify(templateName)});
      [...(item?.querySelectorAll('button') ?? [])].find((button) => button.textContent.trim() === 'Edit')?.click();
    })()`);
    await waitForDom(browser, () => Boolean(document.querySelector(".prompt-template-editor-form")));
    await fillTemplateEditor(browser, updatedTemplateName, updatedTemplateBody, "Save changes");
    await waitForPromptTemplate(browser, updatedTemplateName, updatedTemplateBody);

    await browser.evaluate(`(() => {
      const item = [...document.querySelectorAll('.prompt-template-item')]
        .find((candidate) => candidate.querySelector('strong')?.textContent === ${JSON.stringify(updatedTemplateName)});
      [...(item?.querySelectorAll('button') ?? [])].find((button) => button.textContent.trim() === 'Edit')?.click();
    })()`);
    await waitForDom(browser, () => Boolean(document.querySelector(".prompt-template-editor-form")));
    await browser.evaluate(`(() => {
      const form = document.querySelector('.prompt-template-editor-form');
      form.elements.namedItem('name').value = 'Discarded name';
      form.elements.namedItem('body').value = 'Discarded ${"${not-saved}"}';
      [...form.querySelectorAll('button')].find((button) => button.textContent.trim() === 'Cancel')?.click();
    })()`);
    await waitForPromptTemplate(browser, updatedTemplateName, updatedTemplateBody);
    const afterEditCancel = await browser.evaluate(`(() => {
      const item = [...document.querySelectorAll('.prompt-template-item')]
        .find((candidate) => candidate.querySelector('strong')?.textContent === ${JSON.stringify(updatedTemplateName)});
      return { name: item?.querySelector('strong')?.textContent, body: item?.querySelector('pre')?.textContent };
    })()`);
    assert.deepEqual(afterEditCancel, { name: updatedTemplateName, body: updatedTemplateBody });

    const draftBeforeCancel = "Keep this prefix [replace this selected text] and this suffix.";
    const firstSelection = await preparePromptPicker(browser, draftBeforeCancel);
    await waitForPromptOption(browser, updatedTemplateName);
    await browser.evaluate(`(() => [...document.querySelectorAll('#prompt-options .model-option')]
      .find((option) => option.textContent.includes(${JSON.stringify(updatedTemplateName)}))?.click())()`);
    await waitForDom(browser, () => document.querySelector("#prompt-use-dialog")?.open === true);
    const useDialog = await browser.evaluate(`(() => {
      const labels = [...document.querySelectorAll('#prompt-use-dialog-body .prompt-use-form > label')];
      const inputs = labels.map((label) => label.querySelector('input'));
      return {
        inputCount: inputs.length,
        names: inputs.map((input) => input.name),
        labels: labels.map((label) => label.textContent.trim()),
      };
    })()`);
    assert.deepEqual(useDialog, {
      inputCount: 3,
      names: ["File", "line", "LINE"],
      labels: ["File", "line", "LINE"],
    });

    await browser.evaluate(`(() => [...document.querySelectorAll('#prompt-use-dialog button')]
      .find((button) => button.textContent.trim() === 'Cancel')?.click())()`);
    await waitForDom(browser, () => document.querySelector("#prompt-use-dialog")?.open === false);
    assert.equal(await browser.evaluate("(() => document.querySelector('#draft')?.value)()"), draftBeforeCancel);
    assert.equal(firstSelection.start < firstSelection.end, true);

    const values = {
      File: "src/$1.js",
      line: "literal $ value",
      LINE: "CASE-$&",
    };
    const secondSelection = await preparePromptPicker(browser, draftBeforeCancel);
    await waitForPromptOption(browser, updatedTemplateName);
    await browser.evaluate(`(() => [...document.querySelectorAll('#prompt-options .model-option')]
      .find((option) => option.textContent.includes(${JSON.stringify(updatedTemplateName)}))?.click())()`);
    await waitForDom(browser, () => document.querySelector("#prompt-use-dialog")?.open === true);
    await browser.evaluate(`(() => {
      const values = ${JSON.stringify(values)};
      for (const input of document.querySelectorAll('#prompt-use-dialog-body .prompt-use-form input')) {
        input.value = values[input.name];
        input.dispatchEvent(new Event('input', { bubbles: true }));
      }
    })()`);
    const renderedBody = updatedTemplateBody.replace(/\$\{([^{}]+)\}/g, (_token, name) => values[name]);
    const expectedDraft = `${draftBeforeCancel.slice(0, secondSelection.start)}${renderedBody}${draftBeforeCancel.slice(secondSelection.end)}`;
    await installWebSocketSendSpy(browser);
    webSocketSendSpyInstalled = true;
    const pasteCommandStart = await webSocketSendSpyLength(browser);
    await browser.evaluate(`(() => [...document.querySelectorAll('#prompt-use-dialog button')]
      .find((button) => button.textContent.trim() === 'Paste')?.click())()`);
    await waitForDom(browser, () => document.querySelector("#prompt-use-dialog")?.open === false);
    await waitForDomValue(browser, "#draft", expectedDraft);
    assert.equal(await browser.evaluate("(() => document.querySelector('#draft')?.value)()"), expectedDraft);
    assert.doesNotMatch(expectedDraft, /\$\{[^{}]+\}/);
    assert.equal((await webSocketSendTypes(browser, pasteCommandStart)).includes("send_message"), false);

    await browser.evaluate("(() => document.querySelector('#prompt-manage')?.click())()");
    await waitForPromptTemplate(browser, updatedTemplateName, updatedTemplateBody);
    await browser.evaluate(`(() => {
      const item = [...document.querySelectorAll('.prompt-template-item')]
        .find((candidate) => candidate.querySelector('strong')?.textContent === ${JSON.stringify(updatedTemplateName)});
      [...(item?.querySelectorAll('button') ?? [])].find((button) => button.textContent.trim() === 'Delete')?.click();
    })()`);
    await waitForDom(browser, () => document.querySelector(".prompt-delete-confirmation")?.textContent.trim() === "Delete this template?");
    await browser.evaluate(`(() => [...document.querySelectorAll('.prompt-template-actions button')]
      .find((button) => button.textContent.trim() === 'Cancel')?.click())()`);
    await waitForPromptTemplate(browser, updatedTemplateName, updatedTemplateBody);
    await waitForDom(browser, () => !document.querySelector(".prompt-delete-confirmation"));

    await browser.evaluate(`(() => {
      const item = [...document.querySelectorAll('.prompt-template-item')]
        .find((candidate) => candidate.querySelector('strong')?.textContent === ${JSON.stringify(updatedTemplateName)});
      [...(item?.querySelectorAll('button') ?? [])].find((button) => button.textContent.trim() === 'Delete')?.click();
    })()`);
    await waitForDom(browser, () => document.querySelector(".prompt-delete-confirmation")?.textContent.trim() === "Delete this template?");
    await browser.evaluate(`(() => [...document.querySelectorAll('.prompt-template-actions button')]
      .find((button) => button.classList.contains('danger-button') && button.textContent.trim() === 'Delete')?.click())()`);
    await waitForPromptTemplate(browser, updatedTemplateName, updatedTemplateBody, false);

    await browser.evaluate("(() => document.querySelector('#prompt-templates-dialog')?.close())()");
    await browser.evaluate("(() => document.querySelector('#prompt-manage')?.click())()");
    await waitForDom(browser, () => {
      const dialog = document.querySelector("#prompt-templates-dialog");
      return Boolean(dialog?.open
        && dialog.querySelector(".prompt-dialog-empty")?.textContent.trim() === "No saved prompt templates.");
    });
    await waitForPromptTemplate(browser, updatedTemplateName, updatedTemplateBody, false);
    assert.equal((await webSocketSendTypes(browser, pasteCommandStart)).includes("send_message"), false);
  } finally {
    if (browser && webSocketSendSpyInstalled) {
      try {
        await restoreWebSocketSendSpy(browser);
      } catch {
        // The browser may already have exited after a test failure.
      }
    }
    await browser?.close();
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

async function fillTemplateEditor(browser, name, body, submitLabel = "Create template") {
  await browser.evaluate(`(() => {
    const form = document.querySelector('.prompt-template-editor-form');
    form.elements.namedItem('name').value = ${JSON.stringify(name)};
    form.elements.namedItem('body').value = ${JSON.stringify(body)};
    [...form.querySelectorAll('button')].find((button) => button.textContent.trim() === ${JSON.stringify(submitLabel)})?.click();
  })()`);
}

async function preparePromptPicker(browser, draftValue) {
  const serializedDraftValue = JSON.stringify(draftValue);
  return browser.evaluate(`(() => {
    const draft = document.querySelector('#draft');
    const draftValue = ${serializedDraftValue};
    draft.value = draftValue;
    draft.dispatchEvent(new Event('input', { bubbles: true }));
    const start = draftValue.indexOf('[replace this selected text]') + 1;
    const end = start + 'replace this selected text'.length;
    draft.focus();
    draft.setSelectionRange(start, end);
    const picker = document.querySelector('#prompt-current');
    picker.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }));
    picker.click();
    return { value: draft.value, start, end };
  })()`);
}

async function connectRouterAgent(routerWsUrl, id, sockets) {
  const socket = new WebSocket(routerWsUrl);
  sockets.push(socket);
  await onceOpen(socket);
  socket.send(JSON.stringify({
    version: "v1",
    frame_id: `${id}-register`,
    type: "register",
    agent_id: id,
    instance_id: `${id}-instance`,
  }));
  await waitForFrame(socket, (frame) => frame.type === "registered", 2_000);
  return socket;
}

async function startElectronDomBrowser(url) {
  const electronPath = join(process.cwd(), "node_modules/.bin/electron");
  const temporaryDirectory = await mkdtemp(join(tmpdir(), "router-prompt-templates-electron-"));
  const scriptPath = join(temporaryDirectory, "browser.cjs");
  const script = `const { app, BrowserWindow } = require("electron");
const readline = require("node:readline");
let window;
app.whenReady().then(async () => {
  const newline = String.fromCharCode(10);
  process.stderr.write("APP_READY" + newline);
  window = new BrowserWindow({
    show: false,
    webPreferences: {
      contextIsolation: true,
      sandbox: true,
      partition: "router-prompt-templates-test-" + Date.now() + "-" + Math.random(),
    },
  });
  window.webContents.session.webRequest.onBeforeRequest({ urls: ["https://cdnjs.cloudflare.com/*"] }, (_details, callback) => callback({ cancel: true }));
  process.stderr.write("LOADING " + process.env.MECHA_TEST_UI_URL + newline);
  await window.loadURL(process.env.MECHA_TEST_UI_URL);
  process.stderr.write("LOADED" + newline);
  process.stdout.write("READY" + newline);
  const input = readline.createInterface({ input: process.stdin });
  input.on("line", async (line) => {
    try {
      const request = JSON.parse(line);
      const result = await window.webContents.executeJavaScript(request.script);
      process.stdout.write(JSON.stringify({ ok: true, result }) + newline);
    } catch (error) {
      process.stdout.write(JSON.stringify({ ok: false, error: error.message }) + newline);
    }
  });
}).catch((error) => { process.stderr.write(error.stack + String.fromCharCode(10)); app.quit(); });`;
  await writeFile(scriptPath, script);
  const child = spawn(electronPath, ["--no-sandbox", scriptPath], {
    env: { ...process.env, MECHA_TEST_UI_URL: url },
    stdio: ["pipe", "pipe", "pipe"],
    detached: true,
  });
  let output = "";
  let stderr = "";
  let settled = false;
  const lines = [];
  child.stdout.setEncoding("utf8");
  child.stderr.setEncoding("utf8");
  child.stderr.on("data", (chunk) => { stderr += chunk; });
  child.stdout.on("data", (chunk) => {
    output += chunk;
    const newline = String.fromCharCode(10);
    let newlineIndex;
    while ((newlineIndex = output.indexOf(newline)) !== -1) {
      lines.push(output.slice(0, newlineIndex));
      output = output.slice(newlineIndex + newline.length);
    }
  });
  const readLine = (timeoutMs = browserScriptTimeoutMs) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => { cleanup(); reject(new Error(`timed out waiting for Electron${stderr.trim() ? `: ${stderr.trim()}` : ""}`)); }, timeoutMs);
    const check = () => {
      if (lines.length > 0) { const line = lines.shift(); cleanup(); resolve(line); }
      else if (child.exitCode !== null) { cleanup(); reject(new Error(`Electron exited with code ${child.exitCode}${stderr.trim() ? `: ${stderr.trim()}` : ""}`)); }
      else setTimeout(check, 10);
    };
    const cleanup = () => clearTimeout(timer);
    check();
  });
  const cleanupTemporaryDirectory = () => rm(temporaryDirectory, { recursive: true, force: true });
  try {
    await readLine(browserScriptTimeoutMs);
  } catch (error) {
    terminateElectron(child);
    await cleanupTemporaryDirectory();
    const unavailable = new Error(error.message);
    unavailable.code = "ELECTRON_UNAVAILABLE";
    throw unavailable;
  }
  settled = true;
  return {
    async evaluate(scriptToEvaluate) {
      child.stdin.write(JSON.stringify({ script: scriptToEvaluate }) + String.fromCharCode(10));
      const response = JSON.parse(await readLine());
      if (!response.ok) throw new Error(response.error);
      return response.result;
    },
    async close() {
      if (!settled) return;
      settled = false;
      child.stdin.end();
      await new Promise((resolve) => {
        const timer = setTimeout(() => { terminateElectron(child); resolve(); }, 1_000);
        child.once("exit", () => { clearTimeout(timer); resolve(); });
      });
      await cleanupTemporaryDirectory();
    },
  };
}

function terminateElectron(child) {
  try { process.kill(-child.pid); } catch { child.kill(); }
}

async function waitForDom(browser, predicate, timeoutMs = 8_000) {
  const source = predicate.toString();
  return browser.evaluate(`(async () => {
    const deadline = Date.now() + ${timeoutMs};
    let lastError;
    while (Date.now() < deadline) {
      try {
        const result = await (${source})();
        if (result) return result;
      } catch (error) {
        lastError = error.message;
      }
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    throw new Error(lastError || "timed out waiting for DOM condition");
  })()`);
}

async function waitForPromptTemplate(browser, name, body, present = true) {
  const serializedName = JSON.stringify(name);
  const serializedBody = JSON.stringify(body);
  return browser.evaluate(`(async () => {
    const expectedName = ${serializedName};
    const expectedBody = ${serializedBody};
    const deadline = Date.now() + 8_000;
    while (Date.now() < deadline) {
      const item = [...document.querySelectorAll('.prompt-template-item')]
        .find((candidate) => candidate.querySelector('strong')?.textContent === expectedName);
      const found = item?.querySelector('pre')?.textContent === expectedBody;
      if (found === ${present}) return found;
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    throw new Error(${JSON.stringify(`timed out waiting for prompt template ${name}`)});
  })()`);
}

async function waitForPromptOption(browser, name) {
  const serializedName = JSON.stringify(name);
  return browser.evaluate(`(async () => {
    const expectedName = ${serializedName};
    const deadline = Date.now() + 8_000;
    while (Date.now() < deadline) {
      const option = [...document.querySelectorAll('#prompt-options .model-option')]
        .find((candidate) => candidate.textContent.includes(expectedName));
      if (option) return true;
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    throw new Error(${JSON.stringify(`timed out waiting for prompt option ${name}`)});
  })()`);
}

async function waitForDomValue(browser, selector, expected) {
  const serializedSelector = JSON.stringify(selector);
  const serializedExpected = JSON.stringify(expected);
  return browser.evaluate(`(async () => {
    const deadline = Date.now() + 8_000;
    while (Date.now() < deadline) {
      if (document.querySelector(${serializedSelector})?.value === ${serializedExpected}) return true;
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    throw new Error('timed out waiting for expected DOM value');
  })()`);
}

async function installWebSocketSendSpy(browser) {
  await browser.evaluate(`(() => {
    const key = "__routerPromptTemplatesWebSocketSendSpy";
    const prototype = WebSocket.prototype;
    const originalSend = prototype.send;
    const types = [];
    function spySend(data) {
      let type = null;
      if (typeof data === "string") {
        try { type = JSON.parse(data)?.type ?? null; } catch {}
      }
      types.push(type);
      return originalSend.call(this, data);
    }
    prototype.send = spySend;
    globalThis[key] = {
      types,
      restore() {
        if (prototype.send === spySend) prototype.send = originalSend;
        delete globalThis[key];
      },
    };
  })()`);
}

async function webSocketSendSpyLength(browser) {
  return browser.evaluate(`(() => globalThis.__routerPromptTemplatesWebSocketSendSpy?.types.length ?? 0)()`);
}

async function webSocketSendTypes(browser, start) {
  return browser.evaluate(`(() => globalThis.__routerPromptTemplatesWebSocketSendSpy?.types.slice(${start}) ?? [])()`);
}

async function restoreWebSocketSendSpy(browser) {
  await browser.evaluate(`(() => globalThis.__routerPromptTemplatesWebSocketSendSpy?.restore())()`);
}

function onceOpen(socket) {
  return new Promise((resolve, reject) => {
    socket.once("open", resolve);
    socket.once("error", reject);
  });
}

function waitForFrame(socket, predicate, timeoutMs = 1_000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => cleanup(reject, new Error("timed out waiting for router frame")), timeoutMs);
    const onMessage = (payload) => {
      const frame = JSON.parse(payload.toString("utf8"));
      if (predicate(frame)) cleanup(resolve, frame);
    };
    const onError = (error) => cleanup(reject, error);
    const cleanup = (done, value) => {
      clearTimeout(timer);
      socket.off("message", onMessage);
      socket.off("error", onError);
      done(value);
    };
    socket.on("message", onMessage);
    socket.once("error", onError);
  });
}
