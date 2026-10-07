import test from "node:test";
import assert from "node:assert/strict";
import { JSDOM } from "jsdom";
import { spawn } from "node:child_process";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import {
  ARRIVAL_DIALOG_SELECTOR,
  browserHelpers,
  controlResultMatchesRequest,
  UI_AGENT,
  DESKTOP_VIEWPORT,
  buildChromeArgs,
  createBrowserTab,
  INITIAL_WINDOW_SIZE,
  MODEL_CURRENT_SELECTOR,
  NARROW_VIEWPORT,
  requireDesktopViewport,
  requireNarrowViewport,
  requireSidebarViewport,
  runRequested,
  setViewport,
  stopChild,
} from "./bcb-runner.mjs";

test("sidebar viewport check rejects truncation, missing scrolling, and incorrect rendered geometry", () => {
  const ids = Array.from({ length: 20 }, (_, index) => `worker-${index}`);
  const proof = { ids, clientHeight: 1550, scrollHeight: 2070, overflowY: "auto", heights: ids.map(() => 94), gap: 10, visibleRows: 15 };
  assert.doesNotThrow(() => requireSidebarViewport(proof, ids));
  assert.throws(() => requireSidebarViewport({ ...proof, ids: ids.slice(0, 15) }, ids), /truncated/);
  assert.throws(() => requireSidebarViewport({ ...proof, scrollHeight: 1550 }, ids), /does not scroll/);
  assert.throws(() => requireSidebarViewport({ ...proof, overflowY: "hidden" }, ids), /does not scroll/);
  assert.throws(() => requireSidebarViewport({ ...proof, heights: ids.map(() => 100) }, ids), /15 normal rows/);
  assert.throws(() => requireSidebarViewport({ ...proof, clientHeight: 1446, visibleRows: 14 }, ids), /15 normal rows/);
  assert.throws(() => requireSidebarViewport({ ...proof, visibleRows: 16 }, ids), /15 normal rows/);
});

test("browser storage audit accepts only bounded source-qualified scoped recency maps", async () => {
  const { isScopedRecentAgentMap } = await import("./bcb-managed-rpc-release-runner.mjs");
  const key = "http://router.test\u001fweb";
  assert.equal(isScopedRecentAgentMap({ [key]: ["tmux:worker", "router-managed:worker"], "http://other.test\u001fweb": ["tmux:worker"] }), true);
  for (const invalid of [null, [], ["tmux:worker"], { unscoped: [] }, { [key]: "tmux:worker" }, { [key]: ["worker"] }, { [key]: ["tmux:worker", "tmux:worker"] }, { [key]: Array.from({ length: 11 }, (_, i) => `tmux:${i}`) }, Object.fromEntries(Array.from({ length: 21 }, (_, i) => [`http://router-${i}.test\u001fweb`, []]))]) {
    assert.equal(isScopedRecentAgentMap(invalid), false);
  }
});

test("release dispatcher requires the complete isolated managed-RPC mode pair", async () => {
  await assert.rejects(runRequested(["--managed-rpc"]), /Usage: test:acceptance:bcb/);
  await assert.rejects(runRequested(["--release"]), /Usage: test:acceptance:bcb/);
});

test("task child cleanup escalates to SIGKILL and succeeds once the exact PID is absent", { timeout: 5_000 }, async () => {
  const child = spawn(process.execPath, ["-e", `
    process.on("SIGTERM", () => process.stdout.write("term\\n"));
    process.stdout.write("ready\\n");
    setInterval(() => {}, 1_000);
  `], { stdio: ["ignore", "pipe", "ignore"] });
  const pid = child.pid;
  let output = "";
  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk) => { output += chunk; });
  await new Promise((resolveReady) => child.stdout.once("data", resolveReady));

  try {
    await stopChild(child, "term-resistant task child", 50);
  } finally {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  }

  assert.equal(child.signalCode, "SIGKILL");
  assert.match(output, /term/);
  assert.throws(() => process.kill(pid, 0), (error) => error?.code === "ESRCH");
});

test("managed release runner uses the router-built UI and visible child selection", async () => {
  const runner = await readFile(fileURLToPath(new URL("./bcb-managed-rpc-release-runner.mjs", import.meta.url)), "utf8");
  assert.match(runner, /buildRouterUI/);
  assert.match(runner, /--static-ui-root/);
  assert.match(runner, /selectAndRevealManagedChildren/);
  assert.match(runner, /parent_selected/);
  assert.match(runner, /child_visible/);
  assert.match(runner, /startIsolatedBcbBrowser/);
  assert.match(runner, /agent_role: "agent", client_type: "phase3-acceptance"/);
  assert.doesNotMatch(runner, /startVite|startLoopbackGateway|window\.open|System Events/);
});

test("control result uses the forwarded target identity", () => {
  const request = {
    agent_id: "fixture-peer",
    instance_id: "peer-instance",
    source_agent_id: UI_AGENT,
    control_id: "control-1",
  };
  const result = {
    agent_id: "fixture-peer",
    instance_id: "peer-instance",
    source_agent_id: UI_AGENT,
    control_id: "control-1",
  };
  assert.equal(controlResultMatchesRequest(request, result), true);
  assert.equal(controlResultMatchesRequest({ ...request, agent_id: UI_AGENT }, result), false);
  assert.equal(controlResultMatchesRequest({ ...request, instance_id: "other-instance" }, result), false);
  assert.equal(controlResultMatchesRequest({ ...request, target_agent_id: "fixture-peer" }, result), true);
});

test("setViewport maps a numeric BCB tab id to its CDP target UUID", async () => {
  const bcbTab = { id: 12345, url: "http://127.0.0.1:4173/" };
  const fetchCalls = [];
  const sockets = [];
  class MockWebSocket {
    constructor(url) {
      this.url = url;
      this.listeners = new Map();
      this.commands = [];
      sockets.push(this);
    }

    once(event, callback) {
      if (event === "open") Promise.resolve().then(callback);
      else this.on(event, callback);
    }

    on(event, callback) {
      const callbacks = this.listeners.get(event) ?? [];
      callbacks.push(callback);
      this.listeners.set(event, callbacks);
    }

    off(event, callback) {
      this.listeners.set(event, (this.listeners.get(event) ?? []).filter((candidate) => candidate !== callback));
    }

    send(payload) {
      const command = JSON.parse(payload);
      this.commands.push(command);
      const result = command.method === "Browser.getWindowForTarget"
        ? { windowId: "window-1" }
        : command.method === "Target.attachToTarget" ? { sessionId: "session-1" } : {};
      Promise.resolve().then(() => {
        for (const callback of this.listeners.get("message") ?? []) callback(JSON.stringify({ id: command.id, result }));
      });
    }

    close() {}
  }
  const fetchImpl = async (url) => {
    fetchCalls.push(url);
    if (url.endsWith("/json/list")) return { ok: true, json: async () => [{ id: "target-uuid", type: "page", url: bcbTab.url }] };
    if (url.endsWith("/json/version")) return { ok: true, json: async () => ({ webSocketDebuggerUrl: "ws://127.0.0.1:9222/devtools/browser" }) };
    throw new Error(`unexpected fetch: ${url}`);
  };

  await setViewport(9222, bcbTab, 430, 900, { fetchImpl, WebSocketImpl: MockWebSocket });

  assert.equal(fetchCalls[0], "http://127.0.0.1:9222/json/list");
  assert.deepEqual(sockets[0]?.commands.map(({ method }) => method), [
    "Browser.getWindowForTarget",
    "Browser.setWindowBounds",
    "Target.activateTarget",
    "Target.attachToTarget",
    "Emulation.setDeviceMetricsOverride",
    "Target.detachFromTarget",
  ]);
  assert.equal(sockets[0]?.commands[0]?.params.targetId, "target-uuid");
  assert.equal(sockets[0]?.commands[1]?.params.windowId, "window-1");
  assert.deepEqual(sockets[0]?.commands[1]?.params.bounds, { windowState: "normal" });
  assert.equal(sockets[0]?.commands[2]?.params.targetId, "target-uuid");
  assert.equal(sockets[0]?.commands[3]?.params.targetId, "target-uuid");
  assert.equal(sockets[0]?.commands[4]?.sessionId, "session-1");
  assert.equal(sockets[0]?.commands[5]?.params.sessionId, "session-1");
});

test("CDP creates comparison tabs without a user gesture", async () => {
  const fetchCalls = [];
  const commands = [];
  class MockWebSocket {
    constructor() { this.listeners = new Map(); }
    once(event, callback) { if (event === "open") Promise.resolve().then(callback); else this.on(event, callback); }
    on(event, callback) { this.listeners.set(event, [...(this.listeners.get(event) ?? []), callback]); }
    off(event, callback) { this.listeners.set(event, (this.listeners.get(event) ?? []).filter((candidate) => candidate !== callback)); }
    send(payload) {
      const command = JSON.parse(payload);
      commands.push(command);
      Promise.resolve().then(() => {
        for (const callback of this.listeners.get("message") ?? []) callback(JSON.stringify({ id: command.id, result: { targetId: "legacy-target" } }));
      });
    }
    close() {}
  }
  const fetchImpl = async (url) => { fetchCalls.push(url); return { ok: true, json: async () => ({ webSocketDebuggerUrl: "ws://127.0.0.1:9222/devtools/browser" }) }; };
  assert.equal(await createBrowserTab(9222, "http://127.0.0.1:41000/index.html", { fetchImpl, WebSocketImpl: MockWebSocket }), "legacy-target");
  assert.deepEqual(fetchCalls, ["http://127.0.0.1:9222/json/version"]);
  assert.deepEqual(commands, [{ id: 1, method: "Target.createTarget", params: { url: "http://127.0.0.1:41000/index.html" } }]);
});

test("CfT launch arguments use a fresh headed window at the Vite URL", () => {
  const url = "http://127.0.0.1:4173";
  const args = buildChromeArgs({ profile: "/tmp/cft-profile", cdpPort: 9222, extension: "/tmp/bcb-extension", url });
  assert.deepEqual(args.slice(-2), [`--window-size=${INITIAL_WINDOW_SIZE.width},${INITIAL_WINDOW_SIZE.height}`, url]);
  assert.ok(args.includes("--no-sandbox"));
  assert.ok(args.includes("--new-window"));
  assert.ok(args.includes("--remote-allow-origins=*"));
  assert.equal(args.some((arg) => arg.startsWith("--headless")), false);
  assert.equal(args.includes("about:blank"), false);
});

test("setViewport reports zero or multiple CDP target matches", async () => {
  const bcbTab = { id: 12345, url: "http://127.0.0.1:4173/" };
  const fetchImpl = (targets) => async () => ({ ok: true, json: async () => targets });
  await assert.rejects(
    setViewport(9222, bcbTab, 430, 900, { fetchImpl: fetchImpl([]), WebSocketImpl: class {} }),
    /matched 0 CDP page targets; expected exactly one/,
  );
  await assert.rejects(
    setViewport(9222, bcbTab, 430, 900, { fetchImpl: fetchImpl([
      { id: "target-a", type: "page", url: bcbTab.url },
      { id: "target-b", type: "page", url: bcbTab.url },
    ]), WebSocketImpl: class {} }),
    /matched 2 CDP page targets; expected exactly one/,
  );
});

test("visual viewport guards enforce the BCB widths", () => {
  assert.equal(DESKTOP_VIEWPORT.height, 900);
  assert.equal(NARROW_VIEWPORT.width, 430);
  assert.doesNotThrow(() => requireDesktopViewport({ innerWidth: 821 }));
  assert.throws(() => requireDesktopViewport({ innerWidth: 820 }), /desktop visual check got 820px/);
  assert.doesNotThrow(() => requireNarrowViewport({ innerWidth: 430 }));
  assert.throws(() => requireNarrowViewport({ innerWidth: 431 }), /required 430px viewport/);
});

test("model control selector skips the prompt control", () => {
  const dom = new JSDOM(`
    <div class="prompt-controls"><div class="model-picker"><button class="model-current">Select prompt</button></div></div>
    <div class="model-controls"><div class="model-picker"><button class="model-current">fixture/fixture-model</button></div></div>
  `);
  const button = dom.window.document.querySelector(MODEL_CURRENT_SELECTOR);
  assert.equal(button?.textContent, "fixture/fixture-model");
});

test("arrival selector targets only the open native arrival dialog", () => {
  const dom = new JSDOM(`
    <div role="dialog"><p>Other dialog</p></div>
    <dialog aria-labelledby="arrived-dialog-sender"><p>Closed arrival</p></dialog>
    <dialog aria-labelledby="arrived-dialog-sender" open><p>Open arrival</p></dialog>
  `);
  const dialog = dom.window.document.querySelector(ARRIVAL_DIALOG_SELECTOR);
  assert.equal(dialog?.textContent?.trim(), "Open arrival");
  assert.equal(dom.window.document.querySelector('[role="dialog"]')?.textContent?.trim(), "Other dialog");
});

test("router selector clicks the semantic row from the rendered router markup", () => {
  const dom = new JSDOM(`
    <div class="saved-routers">
      <div class="router-row" tabindex="0" role="button">
        <div class="router-label"><strong>127.0.0.1:41001</strong><div class="meta">First router</div></div>
        <div class="router-row-actions"><button type="button" aria-label="Edit First router">Edit</button></div>
      </div>
      <div class="router-row" tabindex="0" role="button">
        <div class="router-label"><strong>127.0.0.1:41002</strong><div class="meta">Second task router</div></div>
        <div class="router-row-actions"><button type="button" aria-label="Edit Second task router">Edit</button></div>
      </div>
    </div>
  `, { runScripts: "outside-only" });
  const selected = [];
  dom.window.document.querySelectorAll(".router-row").forEach((row) => row.addEventListener("click", () => selected.push(row.querySelector(".router-label strong")?.textContent?.trim())));
  const secondHost = new URL("http://127.0.0.1:41002").host;
  const originalHost = new URL("http://127.0.0.1:41001").host;
  dom.window.eval(`${browserHelpers()}; clickRouterRow(${JSON.stringify("Second task router")}); clickRouterRowByHost(${JSON.stringify(secondHost)}); clickRouterRowByHost(${JSON.stringify(originalHost)});`);
  assert.deepEqual(selected, ["127.0.0.1:41002", "127.0.0.1:41002", "127.0.0.1:41001"]);
});

test("prompt template delete selector selects and removes only the named row", () => {
  const dom = new JSDOM(`
    <div class="prompt-template-item"><strong>Other fixture</strong><pre>Other body</pre><div class="prompt-template-actions"><button type="button">Delete</button></div></div>
    <div class="prompt-template-item"><strong>Imported fixture</strong><pre>Imported body</pre><div class="prompt-template-actions"><button type="button">Delete</button></div></div>
  `, { runScripts: "outside-only" });
  const selected = [];
  dom.window.document.querySelectorAll(".prompt-template-item").forEach((row) => {
    row.querySelector("button")?.addEventListener("click", () => {
      selected.push(row.querySelector("strong")?.textContent?.trim());
      const button = row.querySelector("button");
      if (button?.parentElement?.dataset.confirmed === "true") row.remove();
      else if (button?.parentElement) { button.parentElement.dataset.confirmed = "true"; }
    });
  });
  dom.window.eval(`${browserHelpers()}; clickPromptTemplateDelete("Imported fixture"); clickPromptTemplateDelete("Imported fixture");`);
  assert.deepEqual(selected, ["Imported fixture", "Imported fixture"]);
  assert.deepEqual([...dom.window.document.querySelectorAll(".prompt-template-item strong")].map((node) => node.textContent?.trim()), ["Other fixture"]);
});

test("Cancel resolves the open reminder control when the goal dialog is closed", () => {
  const dom = new JSDOM(`
    <dialog class="message-dialog goal-editor-dialog" aria-labelledby="goal-dialog-title">
      <div class="dialog-shell"><h2 id="goal-dialog-title">Goal</h2><button type="button">Cancel</button></div>
    </dialog>
    <dialog class="message-dialog" aria-labelledby="reminders-dialog-title" open>
      <div class="dialog-shell"><h2 id="reminders-dialog-title">Reminders</h2><button type="button" id="reminder-cancel">Cancel</button></div>
    </dialog>
  `, { runScripts: "outside-only" });
  const clicked = [];
  dom.window.document.querySelectorAll("button").forEach((button) => button.addEventListener("click", () => clicked.push(button.id)));
  dom.window.eval(`${browserHelpers()}; clickText("Cancel", 'dialog[aria-labelledby="reminders-dialog-title"][open]');`);
  assert.deepEqual(clicked, ["reminder-cancel"]);
});
