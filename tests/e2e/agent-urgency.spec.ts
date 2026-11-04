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
const marker = (page: Page, id = caller) => row(page, id).locator(".agent-row-urgent-badge");
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
async function open(page: Page, f: WorkflowHarness, pinUrgency = true) {
  await page.route("**/*", route => ["127.0.0.1", "localhost"].includes(new URL(route.request().url()).hostname) ? route.continue() : route.abort());
  await page.addInitScript(({ url, observer, pinUrgency }) => {
    if (pinUrgency && !localStorage.getItem("mecha.toolbar.pinnedControls.v2")) localStorage.setItem("mecha.toolbar.pinnedControls.v2", JSON.stringify(["urgency"]));
    localStorage.setItem("mecha.web.activeRouter", url);
    localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
    localStorage.setItem("mecha.web.agentIds", JSON.stringify({ [url]: observer }));
  }, { url: f.proxy.url, observer: `urgency-observer-${randomUUID()}`, pinUrgency });
  await page.goto(f.frontend.url);
  await expect(row(page)).toBeVisible();
  await expect(row(page, target)).toBeVisible();
}
async function select(page: Page, id = caller) {
  await row(page, id).locator("strong").click({ timeout: 8000 });
  await expect(row(page, id)).toHaveAttribute("aria-selected", "true");
}
async function noControls(page: Page) {
  await expect(page.locator(".agent-row").getByRole("button", { name: /mark .*urgent|clear urgency|retry.*urgency/i })).toHaveCount(0);
  await expect(page.locator(".urgency-rail, .urgency-toggle, .urgency-error, .urgency-pending")).toHaveCount(0);
  await expect(page.locator(".conversation-header [aria-label='Urgent']")).toHaveCount(0);
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
  const selector = row(page, id).locator('.agent-row-select');
  if (urgent) await expect(selector).toHaveAccessibleName(/, urgent(?:, failed)?$/);
  else await expect(selector).not.toHaveAccessibleName(/, urgent(?:, failed)?$/);
  if (urgent) {
    await expect(marker(page, id)).toBeVisible();
    await expect(marker(page, id)).toHaveText("Urgent");
    await expect(row(page, id).locator(".agent-urgency-marker")).toHaveCount(0);
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

  for (const width of [1000, 1280]) test(`pins and unpins urgency in agent controls at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    const f = await fixture();
    try {
      await open(page, f, false); await select(page, target);
      const toggle = page.locator(".urgency-header-button");
      await expect(toggle).toHaveCount(0);
      await page.getByRole("button", { name: "More agent controls" }).click();
      await expect(page.getByRole("button", { name: "Urgency", exact: true })).toHaveAttribute("aria-pressed", "false");
      await page.getByRole("button", { name: "Pin Urgency" }).click();
      await expect(toggle).toBeVisible();
      await saved(page, f, target, false);
      await page.reload(); await select(page, target);
      await expect(toggle).toBeVisible();
      await select(page, caller); await expect(toggle).toBeVisible();
      await select(page, target);
      await page.getByRole("button", { name: "More agent controls" }).click();
      await page.getByRole("button", { name: "Unpin Urgency" }).click();
      await expect(toggle).toHaveCount(0);
      await page.getByRole("button", { name: "Urgency", exact: true }).click();
      await saved(page, f, target, true);
      await page.reload(); await select(page, target);
      await expect(toggle).toHaveCount(0);
      await page.getByRole("button", { name: "More agent controls" }).click();
      await expect(page.getByRole("button", { name: "Urgency", exact: true })).toHaveAttribute("aria-pressed", "true");
      await expect(page.getByRole("button", { name: "Pin Urgency" })).toBeVisible();
      await page.getByRole("button", { name: "Urgency", exact: true }).click();
      await saved(page, f, target, false);
    } finally { await f.close(); }
  });

  test("urgent card exposes its state and keeps a rounded, stripe-free surface", async ({ page }, testInfo) => {
    const f = await fixture();
    try {
      await open(page, f);
      await saved(page, f, target, false);
      expect((await put(f, target, '{"urgent":true}')).status).toBe(200);
      await saved(page, f, target, true);
      const card = row(page, target);
      await expect(card.locator('.fa-triangle-exclamation, .fa-exclamation')).toHaveCount(0);
      const surface = await card.evaluate(e => {
        const style = getComputedStyle(e);
        return { radius: parseFloat(style.borderTopLeftRadius), clip: style.backgroundClip, image: style.backgroundImage };
      });
      expect(surface.radius).toBeGreaterThan(0);
      expect(surface.clip).toBe('padding-box');
      expect(surface.image).not.toMatch(/repeating-linear-gradient/);
      await card.screenshot({ path: testInfo.outputPath('urgent-card.png') });
      expect((await put(f, target, '{"urgent":false}')).status).toBe(200);
      await saved(page, f, target, false);
    } finally { await f.close(); }
  });

  test("urgent appearance retains independent selected and failed cues", async ({ page }) => {
    const f = await fixture();
    try {
      await open(page, f);
      await page.addStyleTag({ content: '.agent-row { transition: none !important; }' });
      expect((await put(f, target, '{"urgent":true}')).status).toBe(200);
      await saved(page, f, target, true);
      const card = row(page, target);
      const styles = () => card.evaluate(e => {
        const s = getComputedStyle(e);
        return { shadow: s.boxShadow, leftWidth: s.borderLeftWidth, leftColor: s.borderLeftColor, border: s.borderTopColor, background: s.backgroundImage };
      });
      const plain = await styles();
      await select(page, target);
      const selected = await styles();
      expect(selected.shadow).not.toBe(plain.shadow);
      expect(selected.shadow).toContain('rgb(129, 140, 248)');
      expect(selected.border).toBe(plain.border);
      expect(selected.background).toBe(plain.background);
      // Exercise the real stylesheet on the live card for both failure states;
      // the fixture does not force a managed agent into failed lifecycle.
      await card.evaluate(e => e.classList.add('failed'));
      const selectedFailed = await styles();
      expect(selectedFailed.shadow).toBe(selected.shadow);
      expect(selectedFailed.leftWidth).toBe('3px');
      expect(selectedFailed.leftColor).toBe('rgb(239, 68, 68)');
      await select(page, caller);
      // Selection re-renders the row, so reapply the simulated failure state.
      await card.evaluate(e => e.classList.add('failed'));
      const failed = await styles();
      expect(failed.shadow).toBe(plain.shadow);
      expect(failed.leftWidth).toBe('3px');
      expect(failed.leftColor).toBe('rgb(239, 68, 68)');
      expect(failed.background).toBe(plain.background);
      await expect(marker(page, target)).toBeVisible();
    } finally { await f.close(); }
  });

  test("toggles urgency from the conversation toolbar", async ({ page, browser }, testInfo) => {
    const f = await fixture();
    const other = await browser.newPage();
    try {
      await open(page, f); await open(other, f); await select(page, target); await select(other, target);
      const toggle = page.locator(".urgency-header-button");
      await expect(toggle).toHaveAttribute("aria-pressed", "false");
      const order = await page.locator(".agent-row").evaluateAll(nodes => nodes.map(n => n.getAttribute("data-agent-id")));
      const inactiveStyle = await row(page, target).evaluate(e => ({ border: getComputedStyle(e).borderColor, background: getComputedStyle(e).backgroundImage, shadow: getComputedStyle(e).boxShadow }));
      await expect(marker(page, target)).toHaveCount(0);
      await toggle.click();
      await saved(page, f, target, true); await saved(other, f, target, true);
      await expect(row(page, target)).toHaveClass(/\burgent\b/);
      await expect(marker(page, caller)).toHaveCount(0);
      await expect.poll(() => row(page, target).evaluate(e => getComputedStyle(e).borderColor)).not.toBe(inactiveStyle.border);
      const urgentStyle = await row(page, target).evaluate(e => ({ border: getComputedStyle(e).borderColor, background: getComputedStyle(e).backgroundImage, shadow: getComputedStyle(e).boxShadow }));
      expect(urgentStyle.border).not.toBe(inactiveStyle.border);
      expect(urgentStyle.background).not.toBe(inactiveStyle.background);
      expect(urgentStyle.shadow).not.toBe(inactiveStyle.shadow);
      await row(page, target).screenshot({ path: testInfo.outputPath("urgent-card.png") });
      await expect(toggle).toHaveAttribute("aria-pressed", "true");
      await expect(other.locator(".urgency-header-button")).toHaveAttribute("aria-pressed", "true");
      await saved(page, f, caller, false);
      await other.reload(); await select(other, target);
      await expect(other.locator(".urgency-header-button")).toHaveAttribute("aria-pressed", "true");
      await toggle.click(); await saved(other, f, target, false); await saved(page, f, target, false);
      await expect(row(page, target)).not.toHaveClass(/\burgent\b/);
      await expect(toggle).toHaveAttribute("aria-pressed", "false");
      expect(await page.locator(".agent-row").evaluateAll(nodes => nodes.map(n => n.getAttribute("data-agent-id")))).toEqual(order);
      await page.route(`**${urgencyPath(target)}`, route => route.request().method() === "PUT" ? route.fulfill({ status: 503, contentType: "application/json", body: '{}' }) : route.continue());
      await toggle.click();
      await expect(page.getByRole("alert").filter({ hasText: "Urgency update outcome is unknown" })).toBeVisible();
      await saved(page, f, target, false); await saved(other, f, target, false);
      await expect(toggle).toHaveAttribute("aria-pressed", "false");
    } finally { await other.close(); await f.close(); }
  });

  test("serializes each toolbar independently and reconciles a lost committed response", async ({ page }) => {
    const f = await fixture();
    try {
      await open(page, f); await select(page, target);
      let release!: () => void;
      const gate = new Promise<void>(resolve => { release = resolve; });
      let entered!: () => void;
      const started = new Promise<void>(resolve => { entered = resolve; });
      let targetWrites = 0;
      await page.route(`**${urgencyPath(target)}`, async route => {
        if (route.request().method() !== "PUT") return route.continue();
        targetWrites++;
        if (targetWrites === 1) { entered(); await gate; }
        await route.continue();
      });
      await page.locator(".urgency-header-button").click();
      await started;
      await expect(page.locator(".urgency-header-button")).toBeDisabled();
      await select(page, caller);
      await expect(page.locator(".urgency-header-button")).toBeEnabled();
      await page.locator(".urgency-header-button").click();
      await saved(page, f, caller, true);
      await select(page, target);
      await expect(page.locator(".urgency-header-button")).toBeDisabled();
      release();
      await saved(page, f, target, true);
      await expect(page.locator(".urgency-header-button")).toHaveAttribute("aria-pressed", "true");
      await page.unroute(`**${urgencyPath(target)}`);
      await page.route(`**${urgencyPath(target)}`, async route => {
        if (route.request().method() !== "PUT") return route.continue();
        const response = await route.fetch();
        expect(response.ok()).toBeTruthy();
        await route.abort("failed");
      });
      await page.locator(".urgency-header-button").click();
      await expect(page.getByRole("alert").filter({ hasText: "Urgency update outcome is unknown" })).toBeVisible();
      await expect(page.locator(".urgency-header-button")).toHaveAttribute("aria-pressed", "false");
      await saved(page, f, target, false);
      await page.unroute(`**${urgencyPath(target)}`);
      await expect(page.locator(".urgency-header-button")).toBeDisabled();
    } finally { await f.close(); }
  });

  test("ignores an older successful response after a newer projection", async ({ page }) => {
    const f = await fixture();
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    let entered!: () => void;
    const committed = new Promise<void>(resolve => { entered = resolve; });
    try {
      await open(page, f); await select(page, target);
      await page.route(`**${urgencyPath(target)}`, async route => {
        if (route.request().method() !== "PUT") return route.continue();
        const response = await route.fetch();
        expect(response.ok()).toBeTruthy();
        entered();
        await gate;
        await route.fulfill({ response });
      });
      const toggle = page.locator(".urgency-header-button");
      await toggle.click();
      await committed;
      await saved(page, f, target, true);
      expect((await put(f, target, '{"urgent":false}')).status).toBe(200);
      await saved(page, f, target, false);
      release();
      await expect(toggle).toBeEnabled();
      await expect(toggle).toHaveAttribute("aria-pressed", "false");
      await saved(page, f, target, false);
    } finally { release(); await f.close(); }
  });

  test("fences a write lost before the server receives it", async ({ page }) => {
    const f = await fixture();
    try {
      await open(page, f); await select(page, target);
      let writes = 0;
      await page.route(`**${urgencyPath(target)}`, async route => {
        if (route.request().method() !== "PUT") return route.continue();
        writes++;
        await route.abort("failed");
      });
      const toggle = page.locator(".urgency-header-button");
      await toggle.click();
      await expect(page.getByRole("alert").filter({ hasText: "Urgency update outcome is unknown" })).toBeVisible();
      await expect(toggle).toBeDisabled();
      await expect(toggle).toHaveAttribute("aria-pressed", "false");
      await saved(page, f, target, false);
      expect(writes).toBe(1);
    } finally { await f.close(); }
  });

  test("allows retry after a pre-write read failure", async ({ page }) => {
    const f = await fixture();
    try {
      await open(page, f); await select(page, target);
      let writes = 0;
      await page.route(`**${urgencyPath(target)}`, async route => {
        if (route.request().method() === "PUT") writes++;
        await route.continue();
      });
      await page.route("**/v1/managed-agents", route => route.fulfill({ status: 503, contentType: "application/json", body: "{}" }));
      const toggle = page.locator(".urgency-header-button");
      await toggle.click();
      await expect(page.getByRole("alert").filter({ hasText: "Could not check urgency" })).toBeVisible();
      await expect(toggle).toBeEnabled();
      expect(writes).toBe(0);
      await page.unroute("**/v1/managed-agents");
      await toggle.click();
      await saved(page, f, target, true);
      expect(writes).toBe(1);
    } finally { await f.close(); }
  });

  test("toggles an offline managed identity absent from connected agents", async ({ page }) => {
    const f = await fixture();
    try {
      // Simulate an offline projection at the transport boundary while retaining
      // the real managed identity, router, store and urgency mutation.
      await page.route("**/v1/agents?models=omit", async route => {
        const response = await route.fetch();
        const body = await response.json();
        body.agents = body.agents.filter((agent: { agent_id: string }) => agent.agent_id !== target);
        await route.fulfill({ response, json: body });
      });
      await open(page, f); await select(page, target);
      const toggle = page.locator(".urgency-header-button");
      await toggle.click();
      await saved(page, f, target, true);
      await expect(toggle).toHaveAttribute("aria-pressed", "true");
      await toggle.click();
      await saved(page, f, target, false);
    } finally { await f.close(); }
  });

  test("prevents a second toggle when the response precedes its projection", async ({ page }) => {
    const f = await fixture();
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    let hold = false, writes = 0;
    try {
      await open(page, f); await select(page, target);
      await page.route("**/v1/managed-agents**", async route => {
        if (hold && route.request().method() === "GET") await gate;
        await route.continue();
      });
      await page.route(`**${urgencyPath(target)}`, async route => {
        if (route.request().method() === "PUT") { writes++; hold = true; }
        await route.continue();
      });
      const toggle = page.locator(".urgency-header-button");
      await toggle.click();
      await expect.poll(() => query(f, "SELECT urgent FROM agents WHERE agent_id=?", target)).toEqual([{ urgent: 1 }]);
      // A stale displayed false must never invite another inverse write.
      if (await toggle.getAttribute("aria-pressed") === "false") await expect(toggle).toBeDisabled();
      release();
      await saved(page, f, target, true);
      await expect(toggle).toHaveAttribute("aria-pressed", "true");
      expect(writes).toBe(1);
    } finally { release(); await f.close(); }
  });

  test("does not resurrect a committed true after a later false and delayed projections", async ({ page }) => {
    const f = await fixture();
    let release!: () => void;
    const gate = new Promise<void>(resolve => { release = resolve; });
    let hold = false;
    try {
      await open(page, f); await select(page, target);
      await page.route("**/v1/managed-agents**", async route => {
        if (hold && route.request().method() === "GET") await gate;
        await route.continue();
      });
      await page.route(`**${urgencyPath(target)}`, async route => {
        if (route.request().method() === "PUT") hold = true;
        await route.continue();
      });
      const toggle = page.locator(".urgency-header-button");
      await toggle.click();
      await expect.poll(() => query(f, "SELECT urgent FROM agents WHERE agent_id=?", target)).toEqual([{ urgent: 1 }]);
      expect((await put(f, target, '{"urgent":false}')).status).toBe(200);
      // Deliberately exceed the old two-second confirmation expiry while
      // projection reads are held, then deliver the current false projection.
      await page.waitForTimeout(2300);
      release();
      await saved(page, f, target, false);
      await expect(toggle).toHaveAttribute("aria-pressed", "false");
      await expect(toggle).toBeEnabled();
    } finally { release(); await f.close(); }
  });

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

  test("keeps active urgency badge independent of execution and workflow state", async ({ page }, testInfo) => {
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
