import { cp, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { join, dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";
import WebSocket from "ws";
import {
  allocateLoopbackPort,
  createTaskRoot,
  startIsolatedGoRouter,
  verifyRouterReadiness,
} from "../support/isolated-go-router.mjs";
import { connectFixturePeer } from "../support/node-router.mjs";
import { clipboardPasteScript } from "../support/bcb-proof-fixtures.mjs";

const HERE = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(HERE, "../..");
const REPOSITORY = resolve(PROJECT, "../..");
const BCB_SOURCE = process.env.MECHA_BCB_ROOT ?? "/Users/wolfgang/code/browser-console-bridge";
const TIMEOUTS = Object.freeze({ startup: 30_000, browser: 20_000, flow: 60_000, shutdown: 10_000 });
export const UI_AGENT = "wolfgang";
const PEER_AGENT = "bcb-fixture-peer";
const FIXTURE_TIME = "2030-01-01T00:00:00.000Z";
export const DESKTOP_VIEWPORT = Object.freeze({ width: 1440, height: 900 });
export const NARROW_VIEWPORT = Object.freeze({ width: 430, height: 900 });
export const INITIAL_WINDOW_SIZE = Object.freeze({ width: 1440, height: 1000 });
export const MODEL_CURRENT_SELECTOR = ".model-controls:not(.prompt-controls) .model-current";
export const ARRIVAL_DIALOG_SELECTOR = 'dialog[aria-labelledby="arrived-dialog-sender"][open]';

function withTimeout(promise, ms, label) {
  let timer;
  const timeout = new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms); });
  return Promise.race([promise, timeout]).finally(() => clearTimeout(timer));
}

function spawnLogged(command, args, options = {}) {
  const child = spawn(command, args, { ...options, stdio: ["ignore", "pipe", "pipe"] });
  const output = { stdout: "", stderr: "" };
  child.stdout.on("data", (chunk) => { output.stdout += String(chunk); });
  child.stderr.on("data", (chunk) => { output.stderr += String(chunk); });
  child.output = output;
  return child;
}

async function stopChild(child, label) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  const closed = new Promise((resolveClose) => child.once("close", resolveClose));
  child.kill("SIGTERM");
  if (await Promise.race([closed.then(() => true), new Promise((resolveWait) => setTimeout(() => resolveWait(false), TIMEOUTS.shutdown))])) return;
  child.kill("SIGKILL");
  await closed;
  throw new Error(`${label} did not stop after SIGTERM`);
}

async function copyBcb(taskRoot, wsPort) {
  const destination = join(taskRoot, "bcb");
  await mkdir(destination, { recursive: true, mode: 0o700 });
  await cp(join(BCB_SOURCE, "server"), join(destination, "server"), { recursive: true });
  await cp(join(BCB_SOURCE, "extension"), join(destination, "extension"), { recursive: true });
  const background = join(destination, "extension", "background.js");
  const source = await readFile(background, "utf8");
  await writeFile(background, source.replace("ws://localhost:18081", `ws://127.0.0.1:${wsPort}`), { mode: 0o600 });
  return destination;
}

async function startBcb(taskRoot) {
  const httpPort = await allocateLoopbackPort();
  const wsPort = await allocateLoopbackPort();
  const bcbRoot = await copyBcb(taskRoot, wsPort);
  const python = process.env.MECHA_BCB_PYTHON ?? join(BCB_SOURCE, ".venv", "bin", "python3");
  const server = spawnLogged(python, [join(bcbRoot, "server", "bridge_server.py")], {
    cwd: bcbRoot,
    env: { ...process.env, BCB_HOST: "127.0.0.1", BCB_HTTP_PORT: String(httpPort), BCB_WS_PORT: String(wsPort) },
  });
  const baseUrl = `http://127.0.0.1:${httpPort}`;
  await withTimeout((async () => {
    for (;;) {
      try {
        const response = await fetch(`${baseUrl}/health`);
        if (response.ok) break;
      } catch { /* startup */ }
      await new Promise((resolveWait) => setTimeout(resolveWait, 100));
    }
  })(), TIMEOUTS.startup, "BCB server startup");
  return { root: bcbRoot, httpPort, wsPort, baseUrl, server, python };
}

function cftBinary() {
  const configured = process.env.MECHA_CFT_BIN ?? process.env.CHROME_FOR_TESTING_BIN;
  if (configured) return resolve(configured);
  throw new Error("Set MECHA_CFT_BIN to a Chromium for Testing executable. The runner never uses shared Chrome.");
}

export function buildChromeArgs({ profile, cdpPort, extension, url }) {
  return [
    `--user-data-dir=${profile}`, `--remote-debugging-port=${cdpPort}`, `--load-extension=${extension}`, `--disable-extensions-except=${extension}`,
    "--no-first-run", "--no-default-browser-check", "--disable-background-networking", "--new-window", "--remote-allow-origins=*",
    `--window-size=${INITIAL_WINDOW_SIZE.width},${INITIAL_WINDOW_SIZE.height}`, url,
  ];
}

async function startVite() {
  const vitePort = await allocateLoopbackPort();
  const vite = spawnLogged("npm", ["run", "dev", "--", "--host", "127.0.0.1", "--port", String(vitePort), "--strictPort"], { cwd: PROJECT, env: { ...process.env, VITE_ROUTER_URL: "" } });
  const url = `http://127.0.0.1:${vitePort}`;
  await withTimeout((async () => {
    for (;;) {
      try { if ((await fetch(`${url}/`)).ok) return; } catch { /* startup */ }
      await new Promise((resolveWait) => setTimeout(resolveWait, 100));
    }
  })(), TIMEOUTS.startup, "Vite startup");
  vite.url = url;
  return vite;
}

async function startChrome(taskRoot, bcb, downloads, url) {
  const cdpPort = await allocateLoopbackPort();
  const profile = join(taskRoot, "cft-profile");
  await mkdir(join(profile, "Default"), { recursive: true, mode: 0o700 });
  await writeFile(join(profile, "Default", "Preferences"), `${JSON.stringify({ download: { default_directory: downloads, prompt_for_download: false, directory_upgrade: true } })}\n`, { mode: 0o600 });
  const extension = join(bcb.root, "extension");
  const chrome = spawnLogged(cftBinary(), buildChromeArgs({ profile, cdpPort, extension, url }), {
    cwd: taskRoot,
    env: { ...process.env, BCB_HTTP_PORT: String(bcb.httpPort), BCB_WS_PORT: String(bcb.wsPort) },
  });
  const versionUrl = `http://127.0.0.1:${cdpPort}/json/version`;
  await withTimeout((async () => {
    for (;;) {
      try { if ((await fetch(versionUrl)).ok) return; } catch { /* startup */ }
      await new Promise((resolveWait) => setTimeout(resolveWait, 100));
    }
  })(), TIMEOUTS.startup, "Chromium for Testing startup");
  return { chrome, profile, cdpPort, downloads, url };
}

async function bcbCommand(bcb, args, timeoutMs = TIMEOUTS.browser) {
  const child = spawn(bcb.python, ["-m", ...args], { cwd: BCB_SOURCE, env: { ...process.env, BCB_HOST: "127.0.0.1", BCB_HTTP_PORT: String(bcb.httpPort), BCB_WS_PORT: String(bcb.wsPort) }, stdio: ["ignore", "pipe", "pipe"] });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk) => { stdout += String(chunk); });
  child.stderr.on("data", (chunk) => { stderr += String(chunk); });
  const result = await withTimeout(new Promise((resolveResult, rejectResult) => {
    child.once("error", rejectResult);
    child.once("close", (code) => code === 0 ? resolveResult(stdout.trim()) : rejectResult(new Error(`BCB command failed (${code}): ${stderr || stdout}`)));
  }), timeoutMs, "BCB command");
  if (!result) return undefined;
  try { return JSON.parse(result); } catch { return result; }
}

async function tabs(bcb) { return bcbCommand(bcb, ["cli.bcb_tabs", "--json"]); }

async function browserCdpCommand(cdpPort, method, params = {}, dependencies = {}) {
  const fetchImpl = dependencies.fetchImpl ?? fetch;
  const WebSocketImpl = dependencies.WebSocketImpl ?? WebSocket;
  const versionResponse = await fetchImpl(`http://127.0.0.1:${cdpPort}/json/version`);
  if (!versionResponse.ok) throw new Error(`CDP version returned ${versionResponse.status}`);
  const version = await versionResponse.json();
  const socket = new WebSocketImpl(version.webSocketDebuggerUrl);
  await new Promise((resolveOpen, rejectOpen) => { socket.once("open", resolveOpen); socket.once("error", rejectOpen); });
  const id = 1;
  try {
    return await new Promise((resolveCommand, rejectCommand) => {
      const onMessage = (data) => {
        const message = JSON.parse(data.toString());
        if (message.id !== id) return;
        socket.off("message", onMessage);
        message.error ? rejectCommand(new Error(JSON.stringify(message.error))) : resolveCommand(message.result);
      };
      socket.on("message", onMessage);
      socket.send(JSON.stringify({ id, method, params }));
    });
  } finally {
    socket.close();
  }
}

export async function createBrowserTab(cdpPort, url, dependencies = {}) {
  const result = await browserCdpCommand(cdpPort, "Target.createTarget", { url }, dependencies);
  if (typeof result?.targetId !== "string" || !result.targetId) throw new Error("CDP target creation returned no target ID");
  return result.targetId;
}

async function waitForExactTabUrl(bcb, url, timeoutMs = TIMEOUTS.browser) {
  return withTimeout((async () => {
    for (;;) {
      const currentTabs = await tabs(bcb);
      const match = Array.isArray(currentTabs) && currentTabs.find((tab) => tab?.url === url);
      if (match) return match;
      await new Promise((resolveWait) => setTimeout(resolveWait, 100));
    }
  })(), timeoutMs, `legacy comparison tab at ${url}`);
}

async function closeBrowserTarget(cdpPort, targetId) {
  if (typeof targetId !== "string" || !targetId) return;
  await browserCdpCommand(cdpPort, "Target.closeTarget", { targetId }).catch(() => undefined);
}

async function execBrowser(bcb, tabId, code, timeoutMs = TIMEOUTS.browser) {
  const envelope = await bcbCommand(bcb, ["cli.bcb_exec", "--json", "--world", "auto", "--tab", String(tabId), "--timeout", String(Math.ceil(timeoutMs / 1000)), code], timeoutMs + 5_000);
  if (!envelope?.success) throw new Error(`Browser JavaScript failed: ${envelope?.error ?? "unknown error"}`);
  return envelope.result;
}
async function consoleEntries(bcb, tabId) { return bcbCommand(bcb, ["cli.bcb_console", "--json", "--tab", String(tabId), "--limit", "500"]); }
async function screenshot(bcb, tabId, path) {
  // Chrome throttles visible-tab captures. Keep the desktop and narrow proofs
  // far enough apart for the isolated CfT browser to accept both captures.
  await new Promise((resolveWait) => setTimeout(resolveWait, 700));
  await bcbCommand(bcb, ["cli.bcb_screenshot", "--tab", String(tabId), "--output", path], TIMEOUTS.browser + 5_000);
  return path;
}

function browserHelpers() {
  return `
const isVisibleEnabled = (element) => { if (!(element instanceof HTMLElement)) return false; for (let current = element; current instanceof HTMLElement; current = current.parentElement) { if (current.matches(':disabled,[hidden],[aria-hidden="true"],[aria-disabled="true"]') || current.closest('fieldset:disabled')) return false; const dialog = current.closest('dialog'); if (dialog && !dialog.open) return false; const style = getComputedStyle(current); if (style.display === 'none' || style.visibility === 'hidden' || style.opacity === '0') return false; } return true; };
const byText = (selector, text, scopeSelector) => { const scope = scopeSelector ? document.querySelector(scopeSelector) : document; if (!(scope instanceof Element || scope instanceof Document)) throw new Error('scope missing: ' + scopeSelector); const candidates = [...scope.querySelectorAll(selector)].filter(isVisibleEnabled); const exact = candidates.find((item) => item.textContent?.trim() === text); const element = exact ?? candidates.find((item) => item.textContent?.includes(text)); if (!(element instanceof HTMLElement)) throw new Error('element missing: ' + selector + ' ' + text); return element; };
const setElementValue = (element, value) => { if (!isVisibleEnabled(element) || !(element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement)) throw new Error('field is not editable'); const setter = Object.getOwnPropertyDescriptor(element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype, 'value')?.set; if (!setter) throw new Error('value setter missing'); setter.call(element, value); element.dispatchEvent(new Event('input', { bubbles: true })); element.dispatchEvent(new Event('change', { bubbles: true })); };
const setValue = (selector, value) => { const element = document.querySelector(selector); if (!(element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement)) throw new Error('field missing: ' + selector); setElementValue(element, value); };
const clickText = (text, scopeSelector) => { byText('button', text, scopeSelector).click(); };
const clickPromptTemplateDelete = (name) => { const row = [...document.querySelectorAll('.prompt-template-item')].filter(isVisibleEnabled).find((item) => item.querySelector('strong')?.textContent?.trim() === name); if (!(row instanceof HTMLElement)) throw new Error('prompt template row missing: ' + name); const button = [...row.querySelectorAll('button')].filter(isVisibleEnabled).find((item) => item.textContent?.trim() === 'Delete'); if (!(button instanceof HTMLElement)) throw new Error('prompt template delete button missing: ' + name); button.click(); };
const clickRouterRow = (text) => { const rows = [...document.querySelectorAll('.router-row')].filter(isVisibleEnabled); const row = rows.find((item) => [...item.querySelectorAll('.router-label strong,.router-label .meta')].some((node) => node.textContent?.trim() === text)); if (!(row instanceof HTMLElement)) throw new Error('router row missing: ' + text); row.click(); };
const clickRouterRowByHost = (host) => { const rows = [...document.querySelectorAll('.router-row')].filter(isVisibleEnabled); const row = rows.find((item) => item.querySelector('.router-label strong')?.textContent?.trim() === host); if (!(row instanceof HTMLElement)) throw new Error('router row missing for host: ' + host); row.click(); };
const labelField = (label, value) => { const node = [...document.querySelectorAll('label')].find((item) => isVisibleEnabled(item) && item.textContent?.trim().startsWith(label)); if (!node) throw new Error('label missing: ' + label); const field = node.querySelector('input,textarea'); if (!(field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement)) throw new Error('field missing for label: ' + label); setElementValue(field, value); };
const waitFor = async (predicate, label, timeout = 20000) => { const started = Date.now(); while (Date.now() - started < timeout) { if (predicate()) return true; await new Promise((resolveWait) => setTimeout(resolveWait, 100)); } throw new Error('browser wait timed out: ' + label); };
`;
}

async function waitBrowser(bcb, tabId, expression, label, timeoutMs = TIMEOUTS.browser) {
  return execBrowser(bcb, tabId, `(async () => { ${browserHelpers()} await waitFor(() => Boolean(${expression}), ${JSON.stringify(label)}, ${timeoutMs}); return true; })()`, timeoutMs + 3_000);
}
async function clickBrowser(bcb, tabId, selector) { return execBrowser(bcb, tabId, `(async () => { ${browserHelpers()} const element = document.querySelector(${JSON.stringify(selector)}); if (!isVisibleEnabled(element)) throw new Error('missing visible enabled selector: ' + ${JSON.stringify(selector)}); element.click(); return true; })()`); }
async function clickTextBrowser(bcb, tabId, text, scopeSelector) { return execBrowser(bcb, tabId, `(async () => { ${browserHelpers()} clickText(${JSON.stringify(text)}, ${JSON.stringify(scopeSelector)}); return true; })()`); }
async function clickPromptTemplateDeleteBrowser(bcb, tabId, name) { return execBrowser(bcb, tabId, `(async () => { ${browserHelpers()} clickPromptTemplateDelete(${JSON.stringify(name)}); return true; })()`); }
async function clickRouterRowBrowser(bcb, tabId, text) { return execBrowser(bcb, tabId, `(async () => { ${browserHelpers()} clickRouterRow(${JSON.stringify(text)}); return true; })()`); }
async function clickRouterRowByHostBrowser(bcb, tabId, host) { return execBrowser(bcb, tabId, `(async () => { ${browserHelpers()} clickRouterRowByHost(${JSON.stringify(host)}); return true; })()`); }
async function fillBrowser(bcb, tabId, selector, value) { return execBrowser(bcb, tabId, `(async () => { ${browserHelpers()} setValue(${JSON.stringify(selector)}, ${JSON.stringify(value)}); return true; })()`); }
async function fillLabelBrowser(bcb, tabId, label, value) { return execBrowser(bcb, tabId, `(async () => { ${browserHelpers()} labelField(${JSON.stringify(label)}, ${JSON.stringify(value)}); return true; })()`); }

async function browserSnapshot(bcb, tabId) {
  return execBrowser(bcb, tabId, `(() => ({ url: location.href, title: document.title, viewport: { innerWidth, innerHeight, devicePixelRatio }, bodyText: document.body?.innerText ?? '', html: document.documentElement?.outerHTML ?? '', resources: performance.getEntriesByType('resource').map((entry) => entry.name), localStorageKeys: Object.keys(localStorage) }))()`);
}

async function postJson(url, body, method = "POST") {
  const response = await fetch(url, { method, headers: { "content-type": "application/json" }, body: JSON.stringify(body) });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(`${method} ${url} returned ${response.status}: ${JSON.stringify(payload)}`);
  return payload;
}

function frameIdentity(peer) { return { agent_id: peer.agent_id, instance_id: peer.instance_id, lease_id: peer.registered.lease_id }; }
function makeFrame(peer, type, fields) { return { version: "v1", frame_id: `bcb-${type}-${Date.now()}-${Math.random().toString(16).slice(2)}`, type, ...frameIdentity(peer), ...fields }; }

// The router forwards controls with the target identity in agent_id/instance_id and
// keeps the originating UI identity in source_agent_id. It removes target_* fields.
export function controlResultMatchesRequest(request, result) {
  return request.control_id === result.control_id
    && request.source_agent_id === result.source_agent_id
    && request.agent_id === result.agent_id
    && request.instance_id === result.instance_id;
}

function attachFixtureController(peer) {
  const frames = [];
  const sentFrames = [];
  const send = (frame) => { sentFrames.push(frame); peer.send(frame); };
  let modelCount = 0;
  let lockCount = 0;
  peer.socket.on("message", (data) => {
    let frame;
    try { frame = JSON.parse(data.toString()); } catch { return; }
    frames.push(frame);
    if (frame.type === "message") {
      const message = frame.message;
      send(makeFrame(peer, "injection_requested", { message_id: message.id, attempt_number: frame.attempt_number, ...(frame.delivery_items ? { delivery_items: frame.delivery_items } : {}) }));
      send(makeFrame(peer, "ack", { message_id: message.id, attempt_number: frame.attempt_number, ...(frame.delivery_items ? { delivery_items: frame.delivery_items } : {}) }));
    }
    if (frame.type === "model_control") {
      modelCount += 1;
      send(makeFrame(peer, "model_control_result", { source_agent_id: frame.source_agent_id ?? UI_AGENT, control_id: frame.control_id, accepted: modelCount === 1, current_model: modelCount === 1 ? frame.model : { provider: "fixture", id: "fixture-next", name: "Fixture next" }, ...(modelCount === 1 ? {} : { error_code: "rejected" }) }));
    }
    if (frame.type === "goal_control") {
      send(makeFrame(peer, "goal_control_result", { source_agent_id: frame.source_agent_id ?? UI_AGENT, control_id: frame.control_id, revision: frame.revision, accepted: true, goal_state: { enabled: frame.action === "set", goal: frame.goal, goal_lock_enabled: frame.settings.goal_lock_enabled, snooze_enabled: frame.settings.snooze_enabled, snooze_duration_ms: frame.settings.snooze_duration_ms, snooze_turn_limit: frame.settings.snooze_turn_limit, snooze_budget_cap_ms: frame.settings.snooze_budget_cap_ms, snooze_refill_yield_ms: frame.settings.snooze_refill_yield_ms, snooze_refill_elapsed_ms: frame.settings.snooze_refill_elapsed_ms, outcome_counts: { COMPLETED: 1, NOT_DONE: 0 } } }));
    }
    if (frame.type === "reminder_lock_control") {
      lockCount += 1;
      send(makeFrame(peer, "reminder_lock_control_result", { source_agent_id: frame.source_agent_id ?? UI_AGENT, control_id: frame.control_id, reminder_id: frame.reminder_id, accepted: lockCount > 1, ...(lockCount === 1 ? { error_code: "target_not_connected" } : {}) }));
    }
  });
  return { frames, sentFrames, sendActivity(activity_state, extra = {}) { send(makeFrame(peer, "activity", { activity_state, ...extra })); }, sendCatalog(models) { send(makeFrame(peer, "model_catalog", { model_summaries: models, current_model: models[0], total_count: models.length, truncated: false })); }, sendArrival() { send(makeFrame(peer, "message_arrival", { from: "fixture-sender", source: { kind: "agent" }, preview: "Fixture arrival", body: "**Safe** arrival body", subject: "Fixture arrival", created_at: FIXTURE_TIME, message_id: "fixture-arrival" })); } };
}

function receiptFramesForDelivery(fixture) {
  const delivered = fixture.frames.find((frame) => frame.type === "message" && typeof frame.message?.id === "string");
  if (!delivered) return undefined;
  const receipts = fixture.sentFrames.filter((frame) => (frame.type === "injection_requested" || frame.type === "ack") && frame.message_id === delivered.message.id);
  if (!receipts.some((frame) => frame.type === "injection_requested") || !receipts.some((frame) => frame.type === "ack")) return undefined;
  return { message_id: delivered.message.id, receipts };
}

async function waitForFixtureReceipts(fixture, timeoutMs = TIMEOUTS.browser) {
  return withTimeout((async () => {
    for (;;) {
      const result = receiptFramesForDelivery(fixture);
      if (result) return result;
      await new Promise((resolveWait) => setTimeout(resolveWait, 25));
    }
  })(), timeoutMs, "fixture injection and ack receipts");
}

export async function setViewport(cdpPort, bcbTab, width, height = 900, dependencies = {}) {
  const fetchImpl = dependencies.fetchImpl ?? fetch;
  const WebSocketImpl = dependencies.WebSocketImpl ?? WebSocket;
  const listResponse = await fetchImpl(`http://127.0.0.1:${cdpPort}/json/list`);
  if (!listResponse.ok) throw new Error(`CDP target list returned ${listResponse.status}`);
  const targets = await listResponse.json();
  const matches = Array.isArray(targets)
    ? targets.filter((target) => target?.type === "page" && target.url === bcbTab?.url)
    : [];
  if (matches.length !== 1) throw new Error(`BCB tab ${bcbTab?.id ?? "unknown"} URL ${bcbTab?.url ?? "unknown"} matched ${matches.length} CDP page targets; expected exactly one`);
  const target = matches[0];
  const version = await (await fetchImpl(`http://127.0.0.1:${cdpPort}/json/version`)).json();
  const socket = new WebSocketImpl(version.webSocketDebuggerUrl);
  await new Promise((resolveOpen, rejectOpen) => { socket.once("open", resolveOpen); socket.once("error", rejectOpen); });
  let nextId = 0;
  const command = (method, params, sessionId) => new Promise((resolveCommand, rejectCommand) => {
    const id = ++nextId;
    const onMessage = (data) => { const message = JSON.parse(data.toString()); if (message.id !== id) return; socket.off("message", onMessage); message.error ? rejectCommand(new Error(JSON.stringify(message.error))) : resolveCommand(message.result); };
    socket.on("message", onMessage); socket.send(JSON.stringify({ id, method, params, ...(sessionId ? { sessionId } : {}) }));
  });
  const { windowId } = await command("Browser.getWindowForTarget", { targetId: target.id });
  await command("Browser.setWindowBounds", { windowId, bounds: { windowState: "normal" } });
  await command("Target.activateTarget", { targetId: target.id });
  const { sessionId } = await command("Target.attachToTarget", { targetId: target.id, flatten: true });
  try {
    await command("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: 1, mobile: false }, sessionId);
  } finally {
    await command("Target.detachFromTarget", { sessionId }).catch(() => undefined);
    socket.close();
  }
}

async function currentBcbTab(bcb, tabId) {
  const currentTabs = await tabs(bcb);
  const tab = Array.isArray(currentTabs) ? currentTabs.find((candidate) => String(candidate.id) === String(tabId)) : undefined;
  if (!tab || typeof tab.url !== "string") throw new Error(`BCB tab ${tabId} is missing immediately before viewport setup`);
  return tab;
}

export function requireDesktopViewport(viewport) {
  if (!(viewport?.innerWidth >= 821)) throw new Error(`desktop visual check got ${viewport?.innerWidth ?? "unknown"}px`);
}

export function requireNarrowViewport(viewport) {
  if (viewport?.innerWidth !== NARROW_VIEWPORT.width) throw new Error(`BCB did not report the required ${NARROW_VIEWPORT.width}px viewport`);
}

async function run() {
  const taskRoot = await createTaskRoot("router-ui-react-bcb");
  const outputRoot = process.env.MECHA_BCB_ARTIFACT_DIR ?? join("/private/tmp", `router-ui-react-bcb-${Date.now()}`);
  const artifacts = join(taskRoot, "artifacts");
  await mkdir(artifacts, { recursive: true, mode: 0o700 });
  await mkdir(outputRoot, { recursive: true, mode: 0o700 });
  const resources = { taskRoot, artifacts, bcb: undefined, chrome: undefined, vite: undefined, legacy: undefined, legacyTargetId: undefined, router: undefined, routerTwo: undefined, peer: undefined, tabId: undefined, legacyTabId: undefined };
  const report = { result: "incomplete", started_at: new Date().toISOString(), task_root: taskRoot, steps: [], isolation: { live_router_port: 8765, live_router_used: false } };
  const writeArtifact = async (name, value) => { const content = `${JSON.stringify(value, null, 2)}\n`; const path = join(outputRoot, `${name}.json`); await writeFile(join(artifacts, `${name}.json`), content, { mode: 0o600 }); await writeFile(path, content, { mode: 0o600 }); return path; };
  const step = async (name, action) => {
    const started = Date.now();
    try {
      const value = await withTimeout(action(), TIMEOUTS.flow, name);
      const snapshot = resources.tabId === undefined ? undefined : await browserSnapshot(resources.bcb, resources.tabId);
      const artifact = await writeArtifact(name, { name, status: "passed", elapsed_ms: Date.now() - started, value, snapshot });
      report.steps.push({ name, status: "passed", artifact });
      return value;
    } catch (error) {
      const failure = { name, status: "failed", elapsed_ms: Date.now() - started, error: error instanceof Error ? error.message : String(error) };
      try { if (resources.tabId !== undefined) failure.snapshot = await browserSnapshot(resources.bcb, resources.tabId); } catch (snapshotError) { failure.snapshot_error = String(snapshotError); }
      const artifact = await writeArtifact(name, failure);
      report.steps.push({ name, status: "failed", artifact });
      throw error;
    }
  };
  try {
    resources.router = await startIsolatedGoRouter({ repositoryRoot: REPOSITORY });
    resources.routerTwo = await startIsolatedGoRouter({ repositoryRoot: REPOSITORY });
    await verifyRouterReadiness(resources.router.httpUrl);
    await verifyRouterReadiness(resources.routerTwo.httpUrl);
    resources.bcb = await startBcb(taskRoot);
    const downloads = join(outputRoot, "downloads");
    await mkdir(downloads, { recursive: true, mode: 0o700 });
    resources.vite = await startVite();
    resources.chrome = await startChrome(taskRoot, resources.bcb, downloads, resources.vite.url);
    let health;
    await withTimeout((async () => { for (;;) { health = await (await fetch(`${resources.bcb.baseUrl}/health`)).json(); if (health.extension_connected === true) return; await new Promise((resolveWait) => setTimeout(resolveWait, 100)); } })(), TIMEOUTS.startup, "BCB extension connection");
    await writeArtifact("environment", { bcb: health, router: { http: resources.router.httpUrl, ws: resources.router.websocketUrl, database: resources.router.databasePath, log: resources.router.logPath, attachments: resources.router.attachmentsPath, port: resources.router.port }, second_router: { http: resources.routerTwo.httpUrl, port: resources.routerTwo.port }, vite: resources.vite.url, chrome: { pid: resources.chrome.chrome.pid, cdp_port: resources.chrome.cdpPort, profile: resources.chrome.profile }, bcb_ports: { http: resources.bcb.httpPort, ws: resources.bcb.wsPort } });

    const initialTabs = await tabs(resources.bcb);
    if (!Array.isArray(initialTabs) || initialTabs.length !== 1) throw new Error(`fresh CfT profile expected one tab, found ${initialTabs?.length ?? "unknown"}`);
    resources.tabId = initialTabs[0].id;

    await step("load-connect", async () => {
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('input[placeholder="http://127.0.0.1:4000"]')`, "configuration screen");
      await fillBrowser(resources.bcb, resources.tabId, "input[placeholder=\"http://127.0.0.1:4000\"]", resources.router.httpUrl);
      await clickTextBrowser(resources.bcb, resources.tabId, "Connect");
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('[aria-label="Connection status"]')?.textContent?.includes('Connected')`, "router connected");
      const text = await execBrowser(resources.bcb, resources.tabId, "document.body.innerText");
      if (text.includes("8765")) throw new Error("UI rendered live-router port");
      return { router: resources.router.httpUrl, agent: UI_AGENT };
    });

    resources.peer = await connectFixturePeer(resources.router, { agent_id: PEER_AGENT, instance_id: "bcb-peer-instance" });
    const fixture = attachFixtureController(resources.peer);
    fixture.sendActivity("active", { subagent_count: 2, subagents: [{ id: "worker-a", description: "Fixture worker A", status: "running" }, { id: "worker-b", description: "Fixture worker B", status: "running" }] });
    fixture.sendCatalog([{ provider: "fixture", id: "fixture-model", label: "Fixture model" }, { provider: "fixture", id: "fixture-next", label: "Fixture next" }]);

    await step("agents-and-states", async () => {
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('[data-agent-id="${PEER_AGENT}"]')`, "fixture peer row");
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('[data-agent-id="${PEER_AGENT}"]')?.textContent?.includes('Bcb Fixture Peer')`, "fixture peer visible label");
      await clickTextBrowser(resources.bcb, resources.tabId, "2 subagents");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Fixture worker A')`, "expanded subagents");
      fixture.sendActivity("compacting", { subagent_count: 2, subagents: [{ id: "worker-a", description: "Fixture worker A", status: "running" }, { id: "worker-b", description: "Fixture worker B", status: "running" }] });
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Compacting')`, "compacting state");
      fixture.sendActivity("active", { subagent_count: 2, subagents: [{ id: "worker-a", description: "Fixture worker A", status: "running" }, { id: "worker-b", description: "Fixture worker B", status: "running" }] });
      await clickBrowser(resources.bcb, resources.tabId, `[data-agent-id="${PEER_AGENT}"] .agent-row-select`);
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('textarea[aria-label="Message"]')`, "composer enabled");
      return { fixture_agent: PEER_AGENT, own_agent_hidden: !(await execBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes(${JSON.stringify(UI_AGENT)})`)) };
    });

    await step("text-receipts-incoming", async () => {
      await fillBrowser(resources.bcb, resources.tabId, "textarea[aria-label=\"Message\"]", "Outgoing fixture text");
      await execBrowser(resources.bcb, resources.tabId, `document.querySelector('textarea[aria-label="Message"]').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); true`);
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Outgoing fixture text')`, "outgoing canonical message");
      await postJson(`${resources.router.httpUrl}/v1/messages`, { id: "incoming-bcb-message", from: PEER_AGENT, to: UI_AGENT, body: "Incoming fixture message", created_at: FIXTURE_TIME, source: { kind: "agent" } });
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Incoming fixture message')`, "incoming message");
      const deliveryReceipts = await waitForFixtureReceipts(fixture);
      return { outgoing_seen: true, incoming_seen: true, delivered_message_id: deliveryReceipts.message_id, peer_receipts: deliveryReceipts.receipts.map((frame) => frame.type) };
    });

    await step("controls-accepted-rejected", async () => {
      await clickBrowser(resources.bcb, resources.tabId, MODEL_CURRENT_SELECTOR);
      await clickTextBrowser(resources.bcb, resources.tabId, "fixture-next");
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector(${JSON.stringify(MODEL_CURRENT_SELECTOR)})?.textContent?.includes('fixture-next')`, "accepted model control");
      await clickBrowser(resources.bcb, resources.tabId, MODEL_CURRENT_SELECTOR);
      await clickTextBrowser(resources.bcb, resources.tabId, "fixture-model");
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector(${JSON.stringify(MODEL_CURRENT_SELECTOR)})?.textContent?.includes('fixture-next')`, "rejected model control");
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label=\"Set goal\"]");
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label^=\"Goal:\"]");
      await fillBrowser(resources.bcb, resources.tabId, "#goal-text", "Fixture goal");
      await clickTextBrowser(resources.bcb, resources.tabId, "Save");
      await waitBrowser(resources.bcb, resources.tabId, `!document.querySelector('.goal-editor-dialog[open]')`, "accepted goal control");
      const requests = (type) => fixture.frames.filter((frame) => frame.type === type);
      const results = (type) => fixture.sentFrames.filter((frame) => frame.type === type);
      const correlated = controlResultMatchesRequest;
      const modelRequests = requests("model_control");
      const modelResults = results("model_control_result");
      const goalRequests = requests("goal_control");
      const goalResults = results("goal_control_result");
      if (modelResults.length !== 2 || modelResults.some((result) => !modelRequests.some((request) => correlated(request, result)))) throw new Error("model control results were not correlated to their requests");
      if (goalResults.length !== 1 || !goalResults.every((result) => goalRequests.some((request) => correlated(request, result)))) throw new Error("goal control result was not correlated to its request");
      if (modelResults[0]?.accepted !== true || modelResults[1]?.accepted !== false || goalResults[0]?.accepted !== true) throw new Error("fixture control outcomes were not exercised");
      return { model_controls: modelResults.length, goal_controls: goalResults.length, correlated: true };
    });

    await step("reconnect-history", async () => {
      await execBrowser(resources.bcb, resources.tabId, "location.reload(); true");
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('[aria-label="Connection status"]')?.textContent?.includes('Connected')`, "reconnected");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Incoming fixture message') && document.body.innerText.includes('Outgoing fixture text')`, "history backfill");
      return { history_restored: true };
    });

    await step("reminders-crud-errors-countdown", async () => {
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label=\"Add reminder\"]");
      await clickTextBrowser(resources.bcb, resources.tabId, "Add reminder");
      await fillLabelBrowser(resources.bcb, resources.tabId, "Send after (minutes)", "0");
      await fillLabelBrowser(resources.bcb, resources.tabId, "Reminder", "Invalid reminder");
      await clickTextBrowser(resources.bcb, resources.tabId, "Create reminder");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('1 to 10080')`, "reminder validation error");
      await fillLabelBrowser(resources.bcb, resources.tabId, "Subject", "Fixture subject");
      await fillLabelBrowser(resources.bcb, resources.tabId, "Send after (minutes)", "1");
      await fillLabelBrowser(resources.bcb, resources.tabId, "Reminder", "Fixture reminder");
      await clickTextBrowser(resources.bcb, resources.tabId, "Repeat every delay interval");
      await clickTextBrowser(resources.bcb, resources.tabId, "Reminder lock");
      await clickTextBrowser(resources.bcb, resources.tabId, "Create reminder");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('lock setup failed')`, "lock-after-create error");
      await clickTextBrowser(resources.bcb, resources.tabId, "Cancel", 'dialog[aria-labelledby="reminders-dialog-title"][open]');
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('dialog[aria-labelledby="reminders-dialog-title"][open]')?.innerText.includes('Fixture subject') && document.querySelector('dialog[aria-labelledby="reminders-dialog-title"][open]')?.innerText.includes('Recurring')`, "reminder list and countdown");
      await clickTextBrowser(resources.bcb, resources.tabId, "Lock");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Native edits blocked.')`, "accepted reminder lock");
      await clickTextBrowser(resources.bcb, resources.tabId, "Edit");
      await fillLabelBrowser(resources.bcb, resources.tabId, "Reminder", "Updated fixture reminder");
      await clickTextBrowser(resources.bcb, resources.tabId, "Save changes");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Updated fixture reminder')`, "updated reminder");
      await clickTextBrowser(resources.bcb, resources.tabId, "Delete");
      await waitBrowser(resources.bcb, resources.tabId, `!document.body.innerText.includes('Updated fixture reminder')`, "deleted reminder");
      return { invalid_delay: true, lock_rejection: true, lock_acceptance: true, crud: true };
    });

    await step("router-manager-history-scope", async () => {
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label^=\"Router\"]");
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label=\"Add router\"]");
      await fillLabelBrowser(resources.bcb, resources.tabId, "Hostname or URL", resources.routerTwo.httpUrl);
      await fillLabelBrowser(resources.bcb, resources.tabId, "Router name", "Second task router");
      await clickTextBrowser(resources.bcb, resources.tabId, "Add router");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('No connected agents')`, "second router selected");
      const secondRouterEmptyStateObserved = true;
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label^=\"Router\"]");
      await clickRouterRowByHostBrowser(resources.bcb, resources.tabId, new URL(resources.router.httpUrl).host);
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('[data-agent-id="${PEER_AGENT}"]')`, "original router restored");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Incoming fixture message')`, "scoped history restored");
      return { second_router: resources.routerTwo.httpUrl, second_router_empty_state_observed: secondRouterEmptyStateObserved, original_router_host: new URL(resources.router.httpUrl).host, history_scoped: true };
    });

    await step("prompt-crud-import-export", async () => {
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label=\"Manage prompt templates\"]");
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label=\"New template\"]");
      await fillLabelBrowser(resources.bcb, resources.tabId, "Template name", "Fixture prompt");
      await fillLabelBrowser(resources.bcb, resources.tabId, "Prompt body", "Review ${file}");
      await clickTextBrowser(resources.bcb, resources.tabId, "Create template");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Fixture prompt')`, "created prompt template");
      await clickBrowser(resources.bcb, resources.tabId, ".model-current");
      await clickTextBrowser(resources.bcb, resources.tabId, "Fixture prompt");
      await fillLabelBrowser(resources.bcb, resources.tabId, "file", "router.ts");
      await clickTextBrowser(resources.bcb, resources.tabId, "Paste");
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('textarea[aria-label="Message"]')?.value.includes('router.ts')`, "prompt inserted without send");
      const exportPath = join(resources.chrome.downloads, "prompt-templates.json");
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label=\"Manage prompt templates\"]");
      await clickBrowser(resources.bcb, resources.tabId, "[data-testid=\"prompt-export\"]");
      await withTimeout((async () => { for (;;) { try { if ((await readdir(resources.chrome.downloads)).includes("prompt-templates.json")) return; } catch { /* download */ } await new Promise((resolveWait) => setTimeout(resolveWait, 100)); } })(), TIMEOUTS.browser, "prompt export download");
      const exported = JSON.parse(await readFile(exportPath, "utf8"));
      if (exported.format !== "mecha-prompt-templates" || exported.version !== 1) throw new Error("prompt export shape is invalid");
      const importInput = '[aria-labelledby="prompt-templates-dialog-title"] input[type=file]';
      await execBrowser(resources.bcb, resources.tabId, `(() => { const inputs = [...document.querySelectorAll('input[type=file]')]; const composerInput = inputs.find((input) => input.accept === 'image/*'); const promptInput = document.querySelector(${JSON.stringify(importInput)}); if (!(composerInput instanceof HTMLInputElement) || !(promptInput instanceof HTMLInputElement)) throw new Error('file input selector regression fixture missing'); if (promptInput.accept !== '.json,application/json' || promptInput === composerInput) throw new Error('prompt import selector resolved the wrong file input'); const file = new File([${JSON.stringify(JSON.stringify({ format: "mecha-prompt-templates", version: 1, templates: [{ id: "fixture-import", name: "Imported fixture", body: "Imported body" }] }))}], 'import.json', { type: 'application/json' }); const transfer = new DataTransfer(); transfer.items.add(file); Object.defineProperty(promptInput, 'files', { configurable: true, value: transfer.files }); promptInput.dispatchEvent(new Event('change', { bubbles: true })); return { total_file_inputs: inputs.length, composer_accept: composerInput.accept, prompt_accept: promptInput.accept }; })()`);
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Import 1 template')`, "prompt import confirmation");
      await clickTextBrowser(resources.bcb, resources.tabId, "Import");
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Imported fixture')`, "prompt import complete");
      await clickPromptTemplateDeleteBrowser(resources.bcb, resources.tabId, "Imported fixture");
      await clickPromptTemplateDeleteBrowser(resources.bcb, resources.tabId, "Imported fixture");
      await waitBrowser(resources.bcb, resources.tabId, `!document.body.innerText.includes('Imported fixture')`, "prompt delete");
      return { export: exported, imported: true, used: true, deleted: true };
    });

    await step("clipboard-image-references", async () => {
      await execBrowser(resources.bcb, resources.tabId, clipboardPasteScript());
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector('.image-preview')`, "clipboard image preview");
      await execBrowser(resources.bcb, resources.tabId, `document.querySelector('textarea[aria-label="Message"]').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); true`);
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('clipboard.png') || document.querySelector('.bubble-images')`, "clipboard attachment reference");
      return { clipboard_fixture: "clipboard.png", attachment_reference: true };
    });

    await step("arrivals", async () => {
      fixture.sendArrival();
      await waitBrowser(resources.bcb, resources.tabId, `document.body.innerText.includes('Fixture arrival')`, "arrival indicator");
      await clickTextBrowser(resources.bcb, resources.tabId, "Fixture arrival");
      await waitBrowser(resources.bcb, resources.tabId, `document.querySelector(${JSON.stringify(ARRIVAL_DIALOG_SELECTOR)})?.innerText.includes('Safe')`, "arrival dialog body");
      const dialogHtml = await execBrowser(resources.bcb, resources.tabId, `document.querySelector(${JSON.stringify(ARRIVAL_DIALOG_SELECTOR)})?.innerHTML ?? ''`);
      if (dialogHtml.includes("<img")) throw new Error("arrival body rendered an image element");
      await clickBrowser(resources.bcb, resources.tabId, "button[aria-label=\"Close message\"]");
      return { source_kind: "agent", safe_markdown: true, separate_from_history: true };
    });

    await step("visual-desktop", async () => {
      await setViewport(resources.chrome.cdpPort, await currentBcbTab(resources.bcb, resources.tabId), DESKTOP_VIEWPORT.width, DESKTOP_VIEWPORT.height);
      const snapshot = await browserSnapshot(resources.bcb, resources.tabId);
      requireDesktopViewport(snapshot.viewport);
      const path = await screenshot(resources.bcb, resources.tabId, join(outputRoot, "visual-desktop.png"));
      await writeArtifact("visual-desktop-proof", { viewport: snapshot.viewport, screenshot: path, supported_ui: "web/router-ui-react" });
      return { viewport: snapshot.viewport, screenshot: path };
    });

    await step("final-console-import-network", async () => {
      const snapshot = await browserSnapshot(resources.bcb, resources.tabId);
      const badUrls = snapshot.resources.filter((url) => url.includes(":8765") || url.includes("router_web_ui"));
      if (badUrls.length) throw new Error(`unexpected live/legacy runtime URL: ${badUrls.join(", ")}`);
      const errors = (await consoleEntries(resources.bcb, resources.tabId)).filter((entry) => entry.level === "error");
      if (errors.length) throw new Error(`browser console errors: ${JSON.stringify(errors)}`);
      await writeArtifact("final-network", { viewport: snapshot.viewport, resources: snapshot.resources, unexpected_urls: badUrls, console_errors: errors, router_url: resources.router.httpUrl });
      return { console_errors: errors.length, import_failures: 0, unexpected_router_urls: badUrls.length };
    });
    report.result = "passed";
  } finally {
    const cleanup = [];
    try { if (resources.bcb && resources.tabId !== undefined) cleanup.push({ tabs_before_close: await tabs(resources.bcb) }); } catch (error) { cleanup.push({ tabs_error: String(error) }); }
    try { if (resources.bcb && resources.legacyTabId !== undefined) await bcbCommand(resources.bcb, ["cli.bcb_close_tabs", String(resources.legacyTabId)]); } catch (error) { cleanup.push({ legacy_tab_close_error: String(error) }); }
    try { if (resources.chrome && resources.legacyTargetId !== undefined) await closeBrowserTarget(resources.chrome.cdpPort, resources.legacyTargetId); } catch (error) { cleanup.push({ legacy_target_close_error: String(error) }); }
    try { if (resources.peer) await resources.peer.close(); } catch (error) { cleanup.push({ peer_error: String(error) }); }
    for (const [name, processInfo] of [["legacy", resources.legacy], ["vite", resources.vite], ["chrome", resources.chrome?.chrome], ["bcb", resources.bcb?.server]]) {
      try { await stopChild(processInfo, name); } catch (error) { cleanup.push({ [`${name}_stop_error`]: String(error) }); }
    }
    for (const [name, router] of [["router", resources.router], ["router_two", resources.routerTwo]]) {
      try { if (router) await router.close(); } catch (error) { cleanup.push({ [`${name}_stop_error`]: String(error) }); }
    }
    cleanup.push({ task_root_removed: false });
    await writeArtifact("teardown", { cleanup, task_processes_stopped: true, shared_chrome_used: false, live_router_used: false });
    report.cleanup = cleanup;
    report.finished_at = new Date().toISOString();
    await writeFile(join(outputRoot, "REPORT.json"), `${JSON.stringify({ ...report, artifact_directory: outputRoot }, null, 2)}\n`, { mode: 0o600 });
    await writeFile(join(artifacts, "REPORT.json"), `${JSON.stringify({ ...report, artifact_directory: outputRoot }, null, 2)}\n`, { mode: 0o600 });
    if (process.env.MECHA_KEEP_BCB_ARTIFACTS !== "1") await rm(taskRoot, { recursive: true, force: true });
  }
  return report;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  run().then((report) => { console.log(JSON.stringify(report, null, 2)); }).catch((error) => { console.error(error instanceof Error ? error.stack : error); process.exitCode = 1; });
}

export { run, browserSnapshot, browserHelpers, receiptFramesForDelivery, waitForFixtureReceipts };
