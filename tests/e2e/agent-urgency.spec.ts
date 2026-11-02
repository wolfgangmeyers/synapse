import { test, expect, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { join } from "node:path";
import { workflowHarness, model, lastToolOutput, type WorkflowHarness, type Reply } from "./workflow-implicit-goals-harness";

// SPEC-AGENT-URGENCY-001. Only the external provider is deterministic. Calls
// execute through the embedded SDK, proxy/router, SQLite and browser sockets.
// Root registration, replay/fences and controlled store/result-loss faults also
// require supporting Go tests: the public launch route creates worker roles.
const caller = "urgency-caller", target = "urgency-target";
const tool = "mecha_agent_urgency";
const row = (page: Page, id = caller) => page.locator(`.agent-row[data-agent-id="${id}"]`);
const marker = (page: Page, id = caller) => row(page, id).getByRole("img", { name: "Urgent", exact: true });
const urgencyPath = (id: string) => `/v1/agents/${encodeURIComponent(id)}/urgency`;

function query(f: WorkflowHarness, sql: string, ...args: (string | number)[]) {
  const db = new DatabaseSync(f.router.databasePath, { readOnly: true });
  try { return db.prepare(sql).all(...args); } finally { db.close(); }
}
async function create(f: WorkflowHarness, name: string) {
  await f.api("/v1/managed-agents", { name, cwd: join(f.router.root, "work"), runtime: "embedded", model });
  await expect.poll(() => query(f, "SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", name)[0]?.lifecycle_state).toBe("running");
}
async function fixture() {
  const f = await workflowHarness();
  try { await create(f, caller); await create(f, target); return f; }
  catch (error) { await f.close(); throw error; }
}
async function open(page: Page, f: WorkflowHarness) {
  await page.route("**/*", route => ["127.0.0.1", "localhost"].includes(new URL(route.request().url()).hostname) ? route.continue() : route.abort());
  await page.addInitScript(({ url, observer }) => {
    localStorage.setItem("mecha.web.activeRouter", url);
    localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
    localStorage.setItem("mecha.web.agentIds", JSON.stringify({ [url]: observer }));
  }, { url: f.proxy.url, observer: `urgency-observer-${randomUUID()}` });
  await page.goto(f.frontend.url);
  await expect(row(page)).toBeVisible();
  await expect(row(page, target)).toBeVisible();
}
async function select(page: Page, id = caller) {
  await row(page, id).locator("strong").click({ timeout: 8000 });
  await expect(row(page, id)).toHaveAttribute("aria-selected", "true");
}
async function noControls(page: Page) {
  await expect(page.getByRole("button", { name: /mark .*urgent|clear urgency|retry.*urgency/i })).toHaveCount(0);
  await expect(page.locator(".urgency-rail, .urgency-toggle, .urgency-error, .urgency-pending")).toHaveCount(0);
  await expect(page.locator(".conversation-header [aria-label='Urgent'], .conversation-header [aria-label*='urgency' i]")).toHaveCount(0);
}
async function fontAwesomeGlyph(page: Page, selector: string, glyph: string) {
  const icon = page.locator(selector);
  await expect(icon).toBeVisible();
  await expect.poll(() => icon.evaluate(async (element) => {
    await document.fonts.ready;
    const pseudo = getComputedStyle(element, "::before");
    return {
      content: pseudo.content.replace(/^["']|["']$/g, ""),
      family: pseudo.fontFamily.replace(/["']/g, ""),
      weight: pseudo.fontWeight,
      ready: document.fonts.check('900 12px "Font Awesome 6 Free"'),
      loaded: [...document.fonts].some(face => face.family.replace(/["']/g, "") === "Font Awesome 6 Free" && face.weight === "900" && face.status === "loaded"),
      width: element.getBoundingClientRect().width > 0,
      svg: element.querySelectorAll("svg").length,
    };
  })).toEqual({ content: glyph, family: "Font Awesome 6 Free", weight: "900", ready: true, loaded: true, width: true, svg: 0 });
}
async function saved(page: Page, f: WorkflowHarness, id: string, urgent: boolean) {
  await expect.poll(() => query(f, "SELECT urgent FROM agents WHERE agent_id=?", id)).toEqual([{ urgent: Number(urgent) }]);
  await expect(marker(page, id)).toHaveCount(urgent ? 1 : 0);
  if (urgent) {
    await expect(marker(page, id)).toBeVisible();
    await expect(marker(page, id)).toHaveClass(/fa-triangle-exclamation/);
    await fontAwesomeGlyph(page, `.agent-row[data-agent-id="${id}"] .agent-urgency-marker`, "\uf071");
    expect(await marker(page, id).evaluate(e => {
      const bounds = e.getBoundingClientRect();
      return { tag: e.tagName, width: bounds.width, height: bounds.height, size: getComputedStyle(e).fontSize, text: e.textContent, svg: e.querySelectorAll("svg").length };
    })).toEqual({ tag: "I", width: 12, height: 12, size: "12px", text: "", svg: 0 });
    expect(await marker(page, id).evaluate(e => ({ tab: e.tabIndex, button: e.closest("button") !== null, pressed: e.hasAttribute("aria-pressed"), animation: getComputedStyle(e).animationName })))
      .toEqual({ tab: -1, button: false, pressed: false, animation: "none" });
    await expect(marker(page, id)).toHaveCSS("color", "rgb(251, 191, 36)");
  }
  await noControls(page);
}

// Freeze-clock fixtures must not stamp a final before their user envelope.
// Read the router's real epoch (f.clock starts at zero), align BEFORE enqueue,
// and preserve the protocol's mandatory three fractional digits. No reload fix.
async function timestamp(f: WorkflowHarness) {
  const initial = await f.router.advanceClock(1);
  const delta = Math.max(1, Date.now() + 2000 - Date.parse(initial.now));
  const clock = await f.router.advanceClock(delta);
  const value = new Date(clock.now).toISOString();
  expect(value).toMatch(/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z$/);
  return value;
}
async function turn(f: WorkflowHarness, page: Page, replies: Reply[], agent = caller) {
  await select(page, agent);
  const start = f.requests.length;
  const answer = `Urgency fixture final ${randomUUID()}`;
  f.replies.push(...replies, { answer });
  await f.api("/v1/messages", { id: randomUUID(), from: "user", to: agent, body: "Execute the explicitly supplied fixture call", created_at: await timestamp(f), source: { kind: "web_ui" } });
  await expect.poll(() => f.requests.length).toBe(start + replies.length + 1);
  await expect(page.getByText(answer, { exact: true })).toBeVisible();
  expect(f.violations).toEqual([]);
  return { request: f.requests[start], output: replies.some(reply => "tool" in reply) ? lastToolOutput(f) : "" };
}
function schema(request: Record<string, unknown>) {
  expect(Array.isArray(request.tools)).toBe(true);
  const definition = (request.tools as Record<string, unknown>[]).find(t => t.name === tool);
  expect(definition, "actual provider-visible native registration").toBeTruthy();
  expect(definition!.parameters).toMatchObject({ type: "object", additionalProperties: false, required: ["urgent"], properties: { urgent: { type: "boolean" }, agent_id: { type: "string", maxLength: 128 } } });
  expect(Object.keys((definition!.parameters as { properties: object }).properties).sort()).toEqual(["agent_id", "urgent"]);
}
async function native(f: WorkflowHarness, page: Page, urgent: boolean, agent_id?: string, agent = caller) {
  const result = await turn(f, page, [{ tool, args: { urgent, ...(agent_id === undefined ? {} : { agent_id }) } }], agent);
  schema(result.request);
  expect(JSON.parse(result.output)).toEqual({ agent_id: agent_id ?? agent, urgent });
}
async function put(f: WorkflowHarness, id: string, body: string) {
  return fetch(f.proxy.url + urgencyPath(id), { method: "PUT", headers: { "Content-Type": "application/json", Origin: "https://arbitrary.invalid" }, body });
}

test.describe("Agent urgency native contract", () => {
  test.setTimeout(120_000);

  test("sets and clears urgency through embedded native tools without off controls", async ({ page }) => {
    const f = await fixture();
    try {
      await open(page, f); await select(page);
      await saved(page, f, caller, false); await saved(page, f, target, false);
      await row(page, target).hover(); await row(page, target).locator(".agent-row-select").focus();
      await noControls(page);
      expect(query(f, "SELECT count(*) AS n FROM embedded_entries WHERE agent_id=? AND kind IN ('user','assistant','toolResult')", target)).toEqual([{ n: 0 }]);
      const title = await row(page, target).locator("strong").innerText();
      const height = await row(page, target).evaluate(e => e.getBoundingClientRect().height);
      const order = await page.locator(".agent-row").evaluateAll(nodes => nodes.map(n => n.getAttribute("data-agent-id")));
      await native(f, page, true); await saved(page, f, caller, true);
      await native(f, page, true, target); await saved(page, f, target, true);
      expect(await row(page, target).locator("strong").innerText()).toBe(title);
      expect(await row(page, target).evaluate(e => e.getBoundingClientRect().height)).toBe(height);
      expect(await page.locator(".agent-row").evaluateAll(nodes => nodes.map(n => n.getAttribute("data-agent-id")))).toEqual(order);
      await expect(row(page)).toHaveAttribute("aria-selected", "true");
      await native(f, page, false, target); await saved(page, f, target, false);
      await native(f, page, false); await saved(page, f, caller, false);
      // A second actual worker uses omitted-target self resolution too.
      await native(f, page, true, undefined, target); await saved(page, f, target, true);
      await native(f, page, false, undefined, target); await saved(page, f, target, false);
    } finally { await f.close(); }
  });

  test("synchronizes native urgency across clients reload and restart", async ({ page, browser }) => {
    const f = await fixture(), context = await browser.newContext(), other = await context.newPage();
    const frames: string[] = [];
    other.on("websocket", ws => ws.on("framereceived", frame => { if (frames.length < 2048) frames.push(String(frame.payload)); }));
    try {
      await open(page, f); await open(other, f);
      for (const urgent of [true, false]) {
        const start = frames.length;
        await native(f, page, urgent, target);
        await saved(other, f, target, urgent); // no reload: actual live observer
        await expect.poll(() => frames.slice(start).some(raw => { const frame = JSON.parse(raw); return (frame.type === "agent_urgency_changed" && frame.agent_id === target && frame.urgent === urgent) || JSON.stringify(frame).includes(`\"urgent\":${urgent}`); })).toBe(true);
        await other.reload(); await saved(other, f, target, urgent);
        // The ACKed caller is neutral; the idle target owns restart awareness.
        const beforeRestart = f.requests.length;
        f.replies.push({ answer: `Restart awareness ${urgent}` });
        await f.restart();
        await expect.poll(() => query(f, "SELECT count(*) AS n FROM embedded_restart_wake_slots WHERE agent_id=? AND slot='current' AND kind='worker-idle' AND state='checked-complete'", target)[0]?.n).toBe(1);
        expect(f.requests).toHaveLength(beforeRestart + 1);
        await expect.poll(() => query(f, "SELECT count(*) AS n FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND CAST(body AS TEXT) LIKE ?", target, `%Restart awareness ${urgent}%`)[0]?.n).toBe(1);
        expect(f.violations).toEqual([]);
        for (const client of [page, other]) await saved(client, f, target, urgent);
        await other.reload(); await saved(other, f, target, urgent);
      }
    } finally { await context.close(); await f.close(); }
  });

  test("keeps active urgency triangle independent of execution and workflow state", async ({ page }, testInfo) => {
    const f = await fixture();
    try {
      await open(page, f);
      await turn(f, page, [{ tool: "mecha_progress", args: { action: "set", percent: 42, note: "Fixture milestone" } }, { tool: "mecha_blocked", args: { action: "set", reason: "Fixture needs credentials" } }]);
      await expect(row(page).getByRole("button", { name: /Blocked:/ })).toBeVisible();
      await expect(row(page).locator(".agent-row-progress")).toContainText("42%");
      const workflow = await f.api("/v1/workflows", { name: "Urgency coexistence" });
      const { workflow_state: state } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "Review", entry_text: "Keep reviewing", allow_exit: true });
      // Activation admits a real turn; explicitly snooze its implicit goal so
      // subsequent urgency turns do not race an unqueued workflow continuation.
      const activationStart = f.requests.length;
      f.replies.push({ tool: "workflow_snooze", args: { duration_minutes: 30 } }, { answer: "Workflow activation settled" });
      await f.api(`/v1/managed-agents/${caller}/workflow-state`, { action: "activate", workflow_state_id: state.id }, "PUT");
      await expect.poll(() => f.requests.length).toBe(activationStart + 2);
      await expect.poll(() => query(f, "SELECT count(*) AS n FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND CAST(body AS TEXT) LIKE '%Workflow activation settled%'", caller)[0]?.n).toBe(1);
      expect(f.violations).toEqual([]);
      await expect(row(page).locator(".agent-row-workflow-badge")).toContainText("Review");
      const targetMail = query(f, "SELECT count(*) AS n FROM messages WHERE recipient=?", target);
      await native(f, page, true);
      await saved(page, f, caller, true);
      await expect(row(page)).toHaveAttribute("aria-selected", "true");
      await expect(row(page).getByRole("button", { name: /Blocked:/ })).toBeVisible();
      await expect(row(page).locator(".agent-row-progress")).toContainText("42%");
      await saved(page, f, target, false);
      await fontAwesomeGlyph(page, `.agent-row[data-agent-id="${caller}"] .agent-row-workflow-badge .fa-bolt`, "\uf0e7");
      await page.mouse.move(1100, 700); // Keep hover tooltips out of evidence.
      await page.screenshot({ path: testInfo.outputPath("urgent-selected-blocked-progress.png") });
      const titleBeforeNarrow = await row(page).locator("strong").innerText();
      const wide = page.viewportSize()!;
      await page.setViewportSize({ width: 900, height: 800 });
      await expect(row(page).locator("strong")).toHaveText(titleBeforeNarrow);
      await saved(page, f, caller, true);
      expect(await row(page).locator("strong").evaluate(e => e.scrollWidth <= e.clientWidth)).toBe(true);
      await page.screenshot({ path: testInfo.outputPath("urgent-narrow-title.png") });
      await page.setViewportSize(wide);
      // Both genuine FontAwesome glyphs use production bundled fonts; all
      // non-loopback requests remain blocked, including the old CDN path.
      await expect(row(page).locator(".agent-row-workflow-badge")).toBeVisible();
      await expect(row(page).locator(".agent-row-workflow-badge")).toContainText("Review");
      await select(page, target); await select(page);
      const resetStart = f.requests.length;
      f.replies.push({ answer: "Context reset acknowledged" });
      const resetResponse = await fetch(f.proxy.url + `/v1/managed-agents/${caller}/reset`, { method: "POST", headers: { "Content-Type": "application/json", "X-Request-ID": randomUUID() }, body: "{}", signal: AbortSignal.timeout(10_000) });
      expect(resetResponse.ok, await resetResponse.text()).toBe(true);
      await expect.poll(() => f.requests.length).toBe(resetStart + 1);
      await expect.poll(() => query(f, "SELECT count(*) AS n FROM embedded_entries WHERE agent_id=? AND kind='assistant' AND CAST(body AS TEXT) LIKE '%Context reset acknowledged%'", caller)[0]?.n).toBe(1);
      expect(f.violations).toEqual([]);
      await saved(page, f, caller, true);
      expect(query(f, "SELECT count(*) AS n FROM messages WHERE recipient=?", target)).toEqual(targetMail);
      await native(f, page, false); await saved(page, f, caller, false);
      await page.screenshot({ path: testInfo.outputPath("no-urgency-off-control.png") });
    } finally { await f.close(); }
  });

  test("rejects invalid urgency calls and reconciles uncertain outcomes without row errors", async ({ page, browser }) => {
    const f = await fixture(), context = await browser.newContext(), other = await context.newPage();
    try {
      await open(page, f); await open(other, f);
      for (const args of [{}, { urgent: null }, { urgent: "true" }, { urgent: true, agent_id: "" }, { urgent: true, agent_id: "   " }, { urgent: true, agent_id: null }, { urgent: true, extra: false }, { urgent: true, agent_id: "missing-urgency" }]) {
        const result = await turn(f, page, [{ tool, args }]);
        schema(result.request);
        const checked = query(f, "SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='toolResult' ORDER BY seq DESC LIMIT 1", caller)[0];
        expect(checked, "invalid native call has a durable checked tool error").toBeTruthy();
        const responseID = `response_${f.requests.length - 1}`;
        expect(JSON.parse(String(checked.body))).toMatchObject({ toolCallId: `call_${responseID}|fc_${responseID}`, toolName: tool, isError: true, content: [{ type: "text", text: result.output }] });
        expect(result.output).toMatch(/error|invalid|unknown|not found|does not exist|must|schema|required|expected urgent boolean/i);
        await saved(other, f, caller, false);
        await saved(other, f, target, false);
        await noControls(page);
        expect(query(f, "SELECT agent_id FROM agents WHERE agent_id=?", "missing-urgency")).toEqual([]);
      }
      for (const body of ['{}', '{"urgent":null}', '{"urgent":1}', '{"urgent":"true"}', '[]', 'null', 'true', '{', '{"urgent":true} {}', '{"urgent":true,"extra":false}', '{"urgent":true,"urgent":false}', '{"Urgent":true}']) expect((await put(f, caller, body)).status, body).toBe(400);
      expect((await put(f, "missing-urgency", '{"urgent":true}')).status).toBe(404);
      // Native success and same-value fresh retry do not invert the mark.
      await native(f, page, true, target); await saved(other, f, target, true);
      await native(f, page, true, target); await saved(other, f, target, true);
      const response = await put(f, target, '{"urgent":false}');
      expect(response.status).toBe(200);
      expect(response.headers.get("access-control-allow-origin")).toBe("*");
      expect(response.headers.get("access-control-allow-credentials")).not.toBe("true");
      await saved(other, f, target, false);
      await native(f, page, false, target); await saved(page, f, target, false);
      // Disconnect only the observer; a committed native write still survives
      // reconnection. True lost-result/fence/replay injection is Go coverage,
      // not a claim that aborting browser REST simulates native result loss.
      await other.goto("about:blank");
      await native(f, page, true, target);
      await other.goto(f.frontend.url); await saved(other, f, target, true);
      await native(f, page, false, target); await saved(other, f, target, false);
    } finally { await context.close(); await f.close(); }
  });

  test("bounds native urgency to logical identity lifetime", async ({ page }) => {
    const f = await fixture();
    try {
      await open(page, f); await saved(page, f, target, false);
      for (const urgent of [true, true, false, true]) await native(f, page, urgent, target);
      expect(query(f, "SELECT count(*) AS n FROM agents WHERE agent_id=?", target)).toEqual([{ n: 1 }]);
      expect(query(f, "SELECT name FROM sqlite_master WHERE type='table' AND name LIKE '%urgency%'")).toEqual([]);
      await f.advance(60 * 60 * 1000 + 1);
      await saved(page, f, target, true);
      // Embedded restart requires the explicit model-selection boundary;
      // an empty body is the separate process-runtime restart contract.
      await f.api(`/v1/managed-agents/${target}/restart`, { model });
      await saved(page, f, target, true);
      // DELETE archives target-owned mail through its cutoff. Retained mail
      // must be admitted after final deletion, not held in the serial lane.
      // Clock advancement runs pruning. Align before deletion while the
      // managed identity still protects its shared row; enqueue without a new
      // sweep so this tests retained retirement, not pruned-ID recreation.
      const retainedAt = await timestamp(f);
      const response = await fetch(f.proxy.url + `/v1/managed-agents/${target}`, { method: "DELETE", headers: { "X-Request-ID": randomUUID() }, signal: AbortSignal.timeout(10_000) });
      expect(response.ok, await response.text()).toBe(true);
      await expect.poll(() => query(f, "SELECT count(*) AS n FROM managed_agents WHERE agent_id=?", target)[0]?.n).toBe(0);
      await f.api("/v1/messages", { id: randomUUID(), from: "user", to: target, body: "Retained unrelated mail after deletion", created_at: retainedAt, source: { kind: "agent" } });
      await expect.poll(() => query(f, "SELECT urgent,identity_active FROM agents WHERE agent_id=?", target)).toEqual([{ urgent: 0, identity_active: 0 }]);
      expect(query(f, "SELECT count(*) AS n FROM messages WHERE recipient=?", target)[0]?.n).toBeGreaterThan(0);
      expect((await put(f, target, '{"urgent":true}')).status).toBe(404);
      const rejected = await turn(f, page, [{ tool, args: { urgent: true, agent_id: target } }]);
      expect(rejected.output).toMatch(/error|unknown|retired|not found|active|does not exist/i);
      expect(query(f, "SELECT urgent,identity_active FROM agents WHERE agent_id=?", target)).toEqual([{ urgent: 0, identity_active: 0 }]);
      await create(f, target);
      await saved(page, f, target, false);
      await native(f, page, true, target); await saved(page, f, target, true);
      expect(f.violations).toEqual([]);
    } finally { await f.close(); }
  });
});
