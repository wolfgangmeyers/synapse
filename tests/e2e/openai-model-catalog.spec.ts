import { test, expect, type Page } from "@playwright/test";
import { createServer } from "node:http";
import { execFileSync } from "node:child_process";
import { existsSync, readFileSync } from "node:fs";
import { mkdtemp, mkdir, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { randomUUID } from "node:crypto";
import { resolve, join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { startIsolatedRouter, startRouterProxy, serveFrontendDist, type IsolatedRouter, type IsolatedProxy, type IsolatedFrontend } from "./harness";

const repoRoot = resolve(import.meta.dirname, "../..");
const terra = "gpt-6.1-terra";
const sol = "gpt-6-sol";
const pair = (id = terra) => ({ provider: "openai-codex", id });
type Captured = { method: string; path: string; body: Record<string, unknown> };

// Only the external HTTP boundary is synthetic. History excludes auth headers,
// is capped at 16 requests / 2 MiB per body, and is cleared even on test failure.
async function fixture(signedOut = false, discoverChildren = (pid: number) =>
  execFileSync("ps", ["-o", "pid=", "--ppid", String(pid)], { encoding: "utf8" }).trim()) {
  const history: Captured[] = [];
  const violations: string[] = [];
  let mode: "answer" | "tool" | "deny" = "answer";
  let toolSent = false;
  const sse = (events: object[]) => events.map(event => `data: ${JSON.stringify(event)}\n\n`).join("");
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", chunk => {
      body += chunk;
      if (Buffer.byteLength(body) > 2 * 1024 * 1024) { violations.push("body limit"); req.destroy(); }
    });
    req.on("end", () => {
      if (history.length >= 16 || req.method !== "POST" || req.url !== "/codex/responses") {
        violations.push("unexpected request"); res.writeHead(400).end(); return;
      }
      if (!req.headers.authorization?.endsWith(".terra-test-secret") || req.headers["chatgpt-account-id"] !== "terra-test-account") {
        violations.push("auth mismatch"); res.writeHead(401).end(); return;
      }
      let parsed: Record<string, unknown>;
      try { parsed = JSON.parse(body); } catch { violations.push("invalid JSON"); res.writeHead(400).end(); return; }
      history.push({ method: req.method, path: req.url, body: parsed });
      if (mode === "deny") {
        res.writeHead(403, { "Content-Type": "application/json" });
        res.end(JSON.stringify({ error: { message: "Terra model access denied", code: "model_not_available" } })); return;
      }
      const created = { type: "response.created", response: { id: "resp_terra" } };
      const completed = { type: "response.completed", response: { id: "resp_terra", status: "completed" } };
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      if (mode === "tool" && !toolSent) {
        toolSent = true;
        // Read the actual REST-published attachment, never a synthetic image.
        const input = parsed.input;
        const texts = Array.isArray(input) ? input.flatMap(item =>
          Array.isArray(item.content) ? item.content.filter((part: { type: string }) => part.type === "input_text").map((part: { text: string }) => part.text) : []) : [];
        const paths = texts.flatMap((text: string) => [...text.matchAll(/Attached image:\s*1\. (\/[^\r\n]+\.jpg)(?=\r?\n|$)/g)].map(match => match[1]));
        const attachmentRoot = router ? router.databasePath + ".attachments/" : "";
        if (paths.length !== 1 || !attachmentRoot || !paths[0].startsWith(attachmentRoot) || paths[0].includes("..")) {
          violations.push("missing or invalid attachment reference"); res.end(); return;
        }
        const calls = [
          { type: "function_call", id: "fc_terra_image", call_id: "call_terra_image", name: "read", arguments: JSON.stringify({ path: paths[0] }) },
          { type: "function_call", id: "fc_terra_note", call_id: "call_terra_note", name: "read", arguments: JSON.stringify({ path: "note.txt" }) },
        ];
        res.end(sse([created, ...calls.flatMap((item, output_index) => [
          { type: "response.output_item.added", output_index, item: { ...item, arguments: "" } },
          { type: "response.output_item.done", output_index, item },
        ]), completed])); return;
      }
      const answer = mode === "tool" ? "Terra image and real tool complete" : `Streamed ${parsed.model} answer`;
      const item = { id: "msg_terra", type: "message", role: "assistant", content: [] };
      res.write(sse([created, { type: "response.output_item.added", output_index: 0, item }, { type: "response.content_part.added", output_index: 0, content_index: 0, part: { type: "output_text", text: "" } }, { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: answer.slice(0, 9) }]));
      // Separate chunks prove the real SDK stream and WS projection, not a
      // Chat Completions JSON response or synthetic browser timeline.
      setTimeout(() => res.end(sse([
        { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: answer.slice(9) },
        { type: "response.output_text.done", output_index: 0, content_index: 0, text: answer },
        { type: "response.output_item.done", output_index: 0, item: { ...item, status: "completed", content: [{ type: "output_text", text: answer }] } }, completed,
      ])), 30);
    });
  });
  await new Promise<void>(r => server.listen(0, "127.0.0.1", r));
  const address = server.address();
  if (!address || typeof address === "string") throw Error("No mock port");
  let router: IsolatedRouter | undefined;
  let proxy: IsolatedProxy | undefined;
  let frontend: IsolatedFrontend | undefined;
  const close = async () => {
    try {
      // `go run` owns an executable child; close it before the launcher.
      if (proxy?.process.pid && proxy.process.exitCode === null && proxy.process.signalCode === null) {
        let children = "";
        try { children = discoverChildren(proxy.process.pid); }
        catch (error) {
          // GNU ps returns 1 for no matching children, not a discovery failure.
          if (!(error.status === 1 && String(error.stdout ?? "").trim() === "")) throw error;
        }
        for (const pid of children.split(/\s+/).filter(Boolean)) {
          try { process.kill(Number(pid), "SIGTERM"); }
          catch (error) { if (error.code !== "ESRCH") throw error; }
        }
      }
    } finally {
      await Promise.allSettled([proxy?.close(), router?.close(), frontend?.close()]);
      server.closeAllConnections();
      try { await new Promise<void>(r => server.close(() => r())); }
      finally { history.length = 0; }
    }
  };
  try {
    router = await startIsolatedRouter({ repoRoot, managed: true, testClock: false, embedded: { endpoint: `http://127.0.0.1:${address.port}`, signedOut } });
    proxy = await startRouterProxy({ repoRoot, upstreamPort: router.port });
    frontend = await serveFrontendDist(join(repoRoot, "web/router-ui-react/dist"));
    return { router, proxy, frontend, mockUrl: `http://127.0.0.1:${address.port}`, history, violations, close, setMode(value: typeof mode) { mode = value; toolSent = false; } };
  } catch (error) { await close().catch(() => {}); throw error; }
}
type Fixture = Awaited<ReturnType<typeof fixture>>;
async function api(f: Fixture, path: string, body?: object, method = "POST") {
  const response = await fetch(f.proxy.url + path, body ? { method, headers: { "Content-Type": "application/json", "X-Request-ID": randomUUID() }, body: JSON.stringify(body) } : undefined);
  return { status: response.status, body: await response.json() };
}
async function open(page: Page, f: Fixture, direct = false) {
  page.setDefaultTimeout(8_000);
  const url = direct ? f.router.httpUrl : f.proxy.url;
  await page.addInitScript(url => {
    // A single initializer; explicit router changes survive reload.
    if (!localStorage.getItem("mecha.web.activeRouter")) {
      localStorage.setItem("mecha.web.activeRouter", url);
      localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
    }
  }, url);
  await page.goto(f.frontend.url);
  await expect(page.getByRole("button", { name: "Managed actions", exact: true })).toBeVisible();
}
async function dialog(page: Page) {
  await page.getByRole("button", { name: "Managed actions", exact: true }).click();
  await page.getByRole("menuitem", { name: "Launch Agent", exact: true }).click();
  return page.getByRole("dialog", { name: "Launch managed agent", exact: true });
}
async function launch(page: Page, f: Fixture, name: string, model: string | null = terra, instruction = "Say hello") {
  const form = await dialog(page);
  await form.getByLabel("Name", { exact: true }).fill(name);
  await form.getByLabel("Working folder", { exact: true }).fill(join(f.router.root, "work"));
  if (model) {
    await form.getByRole("button", { name: "Use router default", exact: true }).click();
    await page.getByRole("searchbox", { name: "Search available models" }).fill(model);
    const option = page.getByRole("option").filter({ hasText: `openai-codex/${model}` });
    await expect(option).toHaveCount(1);
    await option.click();
  }
  if (instruction) await form.getByLabel("Initial instruction (optional)").fill(instruction);
  const response = page.waitForResponse(r => r.url().endsWith("/v1/managed-agents") && r.request().method() === "POST");
  await form.getByRole("button", { name: "Launch", exact: true }).click();
  const admitted = await response;
  expect(admitted.status(), await admitted.text()).toBe(202);
  await expect(form).not.toBeVisible();
  await page.locator(`[data-agent-id="${name}"]`).click();
}
function saved(f: Fixture, name: string) {
  const db = new DatabaseSync(f.router.databasePath);
  try { return db.prepare("SELECT selected_model_provider AS provider, selected_model_id AS id, runtime FROM managed_agents WHERE agent_id = ?").get(name); }
  finally { db.close(); }
}
function cleanPublic(text: string) {
  for (const secret of ["terra-test-secret", "terra-refresh-secret", "other-provider-test-secret"]) expect(text.includes(secret), "fixture credential must not appear on public surfaces").toBe(false);
}

test.describe("OpenAI Terra real embedded catalog", () => {
  test.setTimeout(120_000);
  let f: Fixture;
  test.afterEach(async () => { if (f) { const violations = [...f.violations]; const retained = JSON.stringify(f.history.map(r => r.body)); await f.close(); cleanPublic(retained); expect(violations).toEqual([]); } });

  test("selects Terra and persists a streamed answer", async ({ page }) => {
    // Fake controlled markers only; never inspect or print workstation credentials.
    const sentinels = {
      GOOGLE_APPLICATION_CREDENTIALS: "/fake-terra-host/adc.json",
      CLOUDSDK_CONFIG: "/fake-terra-host/gcloud",
      MECHA_HOST_HOME: "/fake-terra-host/home",
      OPENAI_API_KEY: "fake-terra-ambient-token",
      AWS_SESSION_TOKEN: "fake-terra-ambient-session",
    };
    const previous = Object.fromEntries(Object.keys(sentinels).map(key => [key, process.env[key]]));
    try {
      Object.assign(process.env, sentinels);
      f = await fixture();
    } finally {
      for (const [key, value] of Object.entries(previous)) {
        if (value === undefined) delete process.env[key];
        else process.env[key] = value;
      }
    }
    // Actual owned subprocess boundary, not an environment-builder mock.
    const environment = Object.fromEntries(readFileSync(`/proc/${f.router.process.pid}/environ`, "utf8")
      .split("\0").filter(Boolean).map(entry => {
        const separator = entry.indexOf("=");
        return [entry.slice(0, separator), entry.slice(separator + 1)];
      }));
    expect(Object.keys(environment).sort()).toEqual([
      "PATH", "LANG", "HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "TMPDIR",
      "EMBEDDED_RESTART_FAKE_ENABLE", "EMBEDDED_RESTART_FAKE_ENDPOINT",
    ].sort());
    expect(environment.PATH === process.env.PATH).toBe(true);
    for (const [key, path] of Object.entries({
      HOME: join(f.router.root, "managed-home"), XDG_CONFIG_HOME: join(f.router.root, "config"),
      XDG_CACHE_HOME: join(f.router.root, "cache"), TMPDIR: join(f.router.root, "tmp"),
    })) {
      expect(environment[key] === path, `${key} must be fixture-owned`).toBe(true);
      expect(existsSync(path)).toBe(true);
    }
    const catalog = await api(f, "/v1/models");
    expect(catalog.body.models.filter((m: { provider: string; id: string }) => m.provider === "openai-codex" && m.id === terra)).toHaveLength(1);
    cleanPublic(JSON.stringify(catalog));
    const restDestinations: string[] = [];
    const sockets: string[] = [];
    const answers: { url: string; agent: string }[] = [];
    let leakedFrame = false;
    page.on("request", request => {
      if (request.method() === "POST" && new URL(request.url()).pathname === "/v1/managed-agents") restDestinations.push(request.url());
    });
    page.on("websocket", ws => {
      if (new URL(ws.url()).pathname === "/v1/ui-live") sockets.push(ws.url());
      ws.on("framereceived", ({ payload }) => {
        const text = payload.toString();
        leakedFrame ||= ["terra-test-secret", "terra-refresh-secret", "other-provider-test-secret"].some(secret => text.includes(secret));
        const frame = JSON.parse(text);
        if (frame.type === "history_entry" && frame.entry?.presentation?.kind === "message" && frame.entry.presentation.to === "user" &&
            frame.entry.message_content === `Streamed ${terra} answer`) {
          answers.push({ url: ws.url(), agent: frame.entry.agent_id });
        }
      });
    });
    // Observe real browser transports, not merely configured storage intent.
    await open(page, f, true);
    await launch(page, f, "terra-direct");
    await expect(page.getByText(`Streamed ${terra} answer`, { exact: true })).toBeVisible();
    expect(f.history).toHaveLength(1);
    expect(f.history[0]).toMatchObject({ method: "POST", path: "/codex/responses", body: { model: terra, stream: true } });
    expect(JSON.stringify(f.history[0].body)).toContain("Say hello");
    expect(saved(f, "terra-direct")).toMatchObject({ ...pair(), runtime: "embedded" });
    await page.reload();
    await page.locator('[data-agent-id="terra-direct"]').click();
    await expect(page.getByText(`Streamed ${terra} answer`, { exact: true })).toBeVisible();
    const directWS = f.router.httpUrl.replace("http:", "ws:") + "/v1/ui-live";
    await expect.poll(() => answers.some(a => a.url === directWS && a.agent === "terra-direct")).toBe(true);
    await page.evaluate(url => {
      localStorage.setItem("mecha.web.activeRouter", url);
      localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
    }, f.proxy.url);
    await page.reload();
    await launch(page, f, "terra-proxy");
    await expect(page.getByText(`Streamed ${terra} answer`, { exact: true })).toBeVisible();
    expect(f.history.map(r => r.body.model)).toEqual([terra, terra]);
    const proxyWS = f.proxy.url.replace("http:", "ws:") + "/v1/ui-live";
    await expect.poll(() => answers.some(a => a.url === proxyWS && a.agent === "terra-proxy")).toBe(true);
    expect(restDestinations).toEqual([f.router.httpUrl + "/v1/managed-agents", f.proxy.url + "/v1/managed-agents"]);
    expect(new Set(sockets)).toEqual(new Set([directWS, proxyWS]));
    expect(leakedFrame, "public WS frames must not contain fixture credentials").toBe(false);
    cleanPublic(JSON.stringify(f.history.map(r => r.body)));
    cleanPublic(await page.locator("body").innerText());
  });

  test("executes Terra image and Responses tool round trip with exact metadata", async ({ page }) => {
    f = await fixture();
    const catalog = await api(f, "/v1/models");
    expect(catalog.body.models.find((m: { provider: string; id: string }) => m.provider === "openai-codex" && m.id === terra)).toMatchObject({ available: true });
    // Remaining exact SDK metadata is checked by the tagged resolver test.
    await open(page, f);
    await launch(page, f, "terra-image", terra, "");
    f.setMode("tool");
    const pixel = await page.evaluate(() => {
      const canvas = document.createElement("canvas"); canvas.width = 2; canvas.height = 2;
      const ctx = canvas.getContext("2d"); if (!ctx) throw Error("Canvas unavailable");
      ctx.fillStyle = "red"; ctx.fillRect(0, 0, 2, 2);
      return canvas.toDataURL("image/png").split(",")[1];
    });
    await page.locator('input[type="file"][accept="image/*"]').setInputFiles({ name: "pixel.png", mimeType: "image/png", buffer: Buffer.from(pixel, "base64") });
    await expect(page.getByRole("img", { name: "pixel.png", exact: true })).toBeVisible();
    await page.getByRole("textbox", { name: "Message", exact: true }).fill("Inspect this pixel and read note.txt");
    await page.getByRole("textbox", { name: "Message", exact: true }).press("Enter");
    await expect(page.getByText("Terra image and real tool complete", { exact: true })).toBeVisible();
    expect(f.history).toHaveLength(2);
    const first = JSON.stringify(f.history[0].body);
    expect(first).toContain("Inspect this pixel");
    expect(first).toContain("Attached image:");
    expect(first).toContain(f.router.databasePath + ".attachments/");
    expect(first).toContain('"name":"read"');
    const continuation = JSON.stringify(f.history[1].body);
    const outputs = f.history[1].body.input as { type: string; call_id: string; output: unknown }[];
    const assertOutputs = (items: typeof outputs) => {
      const imageResults = items.filter(item => item.type === "function_call_output" && item.call_id === "call_terra_image");
      const noteResults = items.filter(item => item.type === "function_call_output" && item.call_id === "call_terra_note");
      expect(imageResults).toHaveLength(1);
      expect(noteResults).toHaveLength(1);
      expect(noteResults[0].output).toEqual(expect.stringContaining("terra harmless tool evidence"));
      const imageParts = imageResults[0].output;
      expect(Array.isArray(imageParts)).toBe(true);
      if (!Array.isArray(imageParts)) throw Error("Image output must contain content parts");
      expect(imageParts).toEqual(expect.arrayContaining([
        expect.objectContaining({ type: "input_text", text: expect.stringContaining("Read image file [image/jpeg]") }),
      ]));
      const images = imageParts.filter(part => part.type === "input_image");
      expect(images).toHaveLength(1);
      expect(images[0].image_url).toMatch(/^data:image\/jpeg;base64,[A-Za-z0-9+/=]+$/);
      return images[0].image_url;
    };
    const imageURL = assertOutputs(outputs);
    const swapped = structuredClone(outputs);
    const image = swapped.find(item => item.type === "function_call_output" && item.call_id === "call_terra_image")!;
    const note = swapped.find(item => item.type === "function_call_output" && item.call_id === "call_terra_note")!;
    [image.output, note.output] = [note.output, image.output];
    expect(() => assertOutputs(swapped), "swapped call outputs must be rejected").toThrow();
    const decoded = await page.evaluate(async url => {
      const image = new Image(); image.src = url; await image.decode();
      const canvas = document.createElement("canvas"); canvas.width = image.width; canvas.height = image.height;
      const ctx = canvas.getContext("2d"); if (!ctx) throw Error("Canvas unavailable");
      ctx.drawImage(image, 0, 0);
      return { width: image.width, height: image.height, pixel: [...ctx.getImageData(0, 0, 1, 1).data] };
    }, imageURL);
    expect(decoded).toMatchObject({ width: 2, height: 2 });
    expect(decoded.pixel[0]).toBeGreaterThan(240);
    expect(decoded.pixel[1]).toBeLessThan(15);
    expect(decoded.pixel[2]).toBeLessThan(15);
    expect(decoded.pixel[3]).toBe(255);
    expect(continuation).toContain("terra harmless tool evidence");
    expect(f.history.map(r => r.body.model)).toEqual([terra, terra]);
    cleanPublic(continuation);
  });

  test("hides signed-out Terra and rejects stale launch", async ({ page }) => {
    f = await fixture(true);
    await open(page, f);
    const form = await dialog(page);
    await form.getByRole("button", { name: "Use router default", exact: true }).click();
    await page.getByRole("searchbox", { name: "Search available models" }).fill(terra);
    await expect(page.getByRole("option").filter({ hasText: `openai-codex/${terra}` })).toHaveCount(0);
    const catalog = await api(f, "/v1/models");
    expect(catalog.body.models.some((m: { provider: string; available: boolean }) => m.provider === "google" && m.available)).toBe(true);
    expect(catalog.body.models.some((m: { provider: string; id: string; available: boolean }) => m.provider === "openai-codex" && m.id === terra && m.available)).toBe(false);
    const stale = await api(f, "/v1/managed-agents", { name: "terra-stale", cwd: join(f.router.root, "work"), runtime: "embedded", model: pair(), initial_instruction: "must not run" });
    expect(stale.status).toBeGreaterThanOrEqual(400);
    expect(stale.status).toBeLessThan(500);
    expect(saved(f, "terra-stale")).toBeUndefined();
    expect(f.history).toHaveLength(0);
    cleanPublic(JSON.stringify([catalog, stale]));
  });

  test("preserves existing selections metadata and defaults", async ({ page }) => {
    f = await fixture();
    const configured = await api(f, "/v1/settings/default-model", { model: pair(sol) }, "PUT");
    expect(configured.status, JSON.stringify(configured.body)).toBe(200);
    const before = (await api(f, "/v1/models")).body;
    const existing = before.models.find((m: { provider: string; id: string }) => m.provider === "openai-codex" && m.id === sol);
    await open(page, f);
    await launch(page, f, "existing-explicit", sol);
    await expect(page.getByText(`Streamed ${sol} answer`, { exact: true })).toBeVisible();
    await page.reload();
    expect(saved(f, "existing-explicit")).toMatchObject(pair(sol));
    const after = (await api(f, "/v1/models")).body;
    expect(after.default_model).toEqual(before.default_model);
    expect(after.models.find((m: { provider: string; id: string }) => m.provider === "openai-codex" && m.id === sol)).toEqual(existing);
    await launch(page, f, "existing-default", null);
    await expect(page.getByText(`Streamed ${sol} answer`, { exact: true })).toBeVisible();
    expect(saved(f, "existing-default")).toMatchObject(pair(sol));
    expect(f.history.map(r => r.body.model)).toEqual([sol, sol]);
  });

  test("surfaces Terra access rejection without substitution", async ({ page }) => {
    f = await fixture(); f.setMode("deny");
    await open(page, f);
    await launch(page, f, "terra-denied");
    await expect(page.getByText("Turn failed: Codex Responses HTTP request failed; turn outcome unknown", { exact: true })).toBeVisible();
    await expect(page.getByText(`Streamed ${terra} answer`, { exact: true })).toHaveCount(0);
    expect(saved(f, "terra-denied")).toMatchObject(pair());
    expect(f.history.map(r => r.body.model)).toEqual([terra]);
    cleanPublic(await page.locator("body").innerText());
    cleanPublic(JSON.stringify((await api(f, "/v1/managed-agents")).body));
  });
});

// Same external fixture, local HTTP only; no extra provider lifecycle.
test("fixture retention and close remove owned resources", async () => {
  test.setTimeout(120_000);
  const f = await fixture();
  try {
    const post = (body: string) => fetch(f.mockUrl + "/codex/responses", {
      method: "POST", headers: { authorization: "Bearer local.terra-test-secret", "chatgpt-account-id": "terra-test-account" },
      body, signal: AbortSignal.timeout(5_000),
    });
    await expect(post(JSON.stringify({ padding: "x".repeat(2 * 1024 * 1024) }))).rejects.toThrow();
    expect(f.history).toHaveLength(0);
    expect(f.violations).toEqual(["body limit"]);
    const body = JSON.stringify({ model: terra, stream: true, input: "retention probe" });
    for (let i = 0; i < 16; i++) {
      const response = await post(body); expect(response.status).toBe(200); await response.text();
    }
    const retained = JSON.stringify(f.history);
    expect(f.history).toHaveLength(16);
    expect((await post(body)).status).toBe(400);
    expect(JSON.stringify(f.history)).toBe(retained);
    expect(f.violations).toEqual(["body limit", "unexpected request"]);
    cleanPublic(retained);
    for (const path of [f.router.databasePath, join(f.router.root, "private/codex.json"), join(f.router.root, "managed-home"), join(f.router.root, "work")]) expect(existsSync(path)).toBe(true);
  } finally { await f.close(); }
  expect(f.history).toEqual([]);
  expect(existsSync(f.router.root)).toBe(false);
  await expect.poll(() => f.router.process.exitCode !== null || f.router.process.signalCode !== null).toBe(true);
  await expect.poll(() => f.proxy.process.exitCode !== null || f.proxy.process.signalCode !== null).toBe(true);
  expect(f.frontend.server.listening).toBe(false);
  for (const url of [f.router.httpUrl, f.proxy.url, f.frontend.url, f.mockUrl]) {
    await expect(fetch(url, { signal: AbortSignal.timeout(1_000) })).rejects.toThrow();
  }
});


test("signal-exit startup rejects and removes its root", async () => {
  test.setTimeout(15_000);
  const root = await mkdtemp(join(tmpdir(), "terra-signal-"));
  try {
    await mkdir(join(root, "bin"));
    await mkdir(join(root, "repo/router-proxy"), { recursive: true });
    await mkdir(join(root, "tmp"));
    // Build shim writes an executable that really self-signals before readiness.
    await writeFile(join(root, "bin/go"), `#!/bin/sh
while [ "$1" != "-o" ]; do shift; done
shift
printf '%s' "$1" > '${root}/binary-path'
printf '#!/bin/sh\nkill -TERM $$\n' > "$1"
chmod 700 "$1"
`, { mode: 0o700 });
    const script = join(root, "run.mts");
    await writeFile(script, `import assert from 'node:assert/strict';
import { existsSync, readFileSync } from 'node:fs';
import { dirname } from 'node:path';
import { startIsolatedRouter } from '${repoRoot}/tests/e2e/harness.ts';
await assert.rejects(startIsolatedRouter({ repoRoot: '${root}/repo', managed: false }), /exited prematurely/);
assert.equal(existsSync(dirname(readFileSync('${root}/binary-path', 'utf8'))), false);
`);
    execFileSync(process.execPath, ["--import", join(repoRoot, "pi-extensions/mecha-rpc/node_modules/tsx/dist/loader.mjs"), script], {
      env: { ...process.env, PATH: `${root}/bin:${process.env.PATH}`, TMPDIR: join(root, "tmp") },
      timeout: 6_000, killSignal: "SIGKILL", stdio: "pipe",
    });
  } finally { await rm(root, { recursive: true, force: true }); }
});

for (const failure of ["empty children", "reaped child", "unexpected discovery error"] as const) {
  test(`fixture cleanup handles ${failure}`, async () => {
    test.setTimeout(30_000);
    // Narrow discovery seam only. The router/proxy/frontend and close are real.
    // ps targets a real, live childless subprocess; the ESRCH PID is already reaped.
    const childless = failure === "empty children"
      ? (await import("node:child_process")).spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], { stdio: "ignore" }) : undefined;
    const reaped = failure === "reaped child"
      ? execFileSync(process.execPath, ["-e", "process.stdout.write(String(process.pid))"], { encoding: "utf8" }) : "";
    let inject = true;
    let f: Fixture | undefined;
    const originalDiscovery = (pid: number) => execFileSync("ps", ["-o", "pid=", "--ppid", String(pid)], { encoding: "utf8" }).trim();
    try {
      f = await fixture(false, pid => {
        const actual = originalDiscovery(pid);
        if (!inject) return actual;
        // Stop the real executable even when the injected observation fails.
        for (const child of actual.split(/\s+/).filter(Boolean)) process.kill(Number(child), "SIGTERM");
        if (failure === "empty children") return originalDiscovery(childless!.pid!);
        if (failure === "reaped child") return reaped;
        throw Error("controlled discovery failure");
      });
      f.history.push({ method: "POST", path: "/codex/responses", body: {} });
      if (failure === "unexpected discovery error") await expect(f.close()).rejects.toThrow("controlled discovery failure");
      else await f.close();
      expect(f.history).toHaveLength(0);
      expect(existsSync(f.router.root)).toBe(false);
      expect(f.frontend.server.listening).toBe(false);
      for (const url of [f.mockUrl, f.router.httpUrl, f.proxy.url, f.frontend.url]) {
        await expect(fetch(url, { signal: AbortSignal.timeout(1_000) })).rejects.toThrow();
      }
    } finally {
      inject = false;
      if (childless) {
        const closed = new Promise<void>(resolve => childless.once("close", () => resolve()));
        childless.kill("SIGTERM");
        await closed;
      }
      if (f) {
        // Independent resource release even against the unfixed early throw.
        await Promise.allSettled([f.proxy.close(), f.router.close(), f.frontend.close()]);
        await f.close();
      }
    }
  });
}
