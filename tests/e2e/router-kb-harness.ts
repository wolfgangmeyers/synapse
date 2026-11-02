import { createServer } from "node:http";
import { execFileSync } from "node:child_process";
import { once } from "node:events";
import { rm } from "node:fs/promises";
import { randomUUID } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { join, resolve } from "node:path";
import { expect, type Page } from "@playwright/test";
import { startIsolatedRouter, startRouterProxy, serveFrontendDist, type IsolatedRouter, type IsolatedProxy, type IsolatedFrontend } from "./harness";

type Reply = { tool: "kb_list" | "kb_read"; args: object } | { answer: string };
export const model = { provider: "openai-codex", id: "gpt-6.1-terra" };

// go run owns a separate executable: close that child before closing its parent.
async function closeProxyChild(proxy: IsolatedProxy | undefined) {
  if (!proxy?.process.pid || proxy.process.exitCode !== null) return;
  let children = "";
  try { children = execFileSync("ps", ["-o", "pid=", "--ppid", String(proxy.process.pid)], { encoding: "utf8" }).trim(); }
  catch (e) { if (e.status !== 1) throw e; }
  for (const pid of children.split(/\s+/).filter(Boolean)) {
    try { process.kill(Number(pid), "SIGTERM"); } catch (e) { if (e.code !== "ESRCH") throw e; }
  }
}

// Only the provider boundary is mocked. Every response must be explicitly queued;
// unexpected requests fail closed and remain observable. No external calls.
export async function kbHarness(kbDir?: (home: string) => string) {
  const requests: Record<string, unknown>[] = [];
  const violations: string[] = [];
  const replies: Reply[] = [];
  const wsFrames: string[] = [];
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", chunk => {
      body += chunk;
      if (Buffer.byteLength(body) > 2 * 1024 * 1024) { violations.push("body limit"); req.destroy(); }
    });
    req.on("end", async () => {
      if (req.method !== "POST" || req.url !== "/codex/responses" || requests.length >= 128) {
        violations.push(`unexpected route ${req.method} ${req.url}`); res.writeHead(400).end(); return;
      }
      if (!req.headers.authorization?.endsWith(".terra-test-secret") || req.headers["chatgpt-account-id"] !== "terra-test-account") {
        violations.push("synthetic auth mismatch"); res.writeHead(401).end(); return;
      }
      const parsed = JSON.parse(body);
      requests.push(parsed);
      const reply = replies.shift();
      if (!reply || parsed.model !== model.id) {
        violations.push(`unexpected provider request ${requests.length}: ${parsed.model}`); res.writeHead(400).end(); return;
      }
      const id = `response_${requests.length}`;
      const events: object[] = [{ type: "response.created", response: { id } }];
      if ("tool" in reply) {
        const item = { type: "function_call", id: `fc_${id}`, call_id: `call_${id}`, name: reply.tool, arguments: JSON.stringify(reply.args) };
        events.push({ type: "response.output_item.added", output_index: 0, item: { ...item, arguments: "" } }, { type: "response.output_item.done", output_index: 0, item });
      } else {
        const item = { type: "message", id: `msg_${id}`, role: "assistant", content: [] };
        events.push({ type: "response.output_item.added", output_index: 0, item },
          { type: "response.content_part.added", output_index: 0, content_index: 0, part: { type: "output_text", text: "" } },
          { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: reply.answer },
          { type: "response.output_text.done", output_index: 0, content_index: 0, text: reply.answer },
          { type: "response.output_item.done", output_index: 0, item: { ...item, status: "completed", content: [{ type: "output_text", text: reply.answer }] } });
      }
      events.push({ type: "response.completed", response: { id, status: "completed" } });
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.end(events.map(e => `data: ${JSON.stringify(e)}\n\n`).join(""));
    });
  });
  await new Promise<void>(r => server.listen(0, "127.0.0.1", r));
  const address = server.address();
  if (!address || typeof address === "string") throw Error("missing mock address");
  let router: IsolatedRouter | undefined;
  let proxy: IsolatedProxy | undefined;
  let frontend: IsolatedFrontend | undefined;
  const close = async () => {
    try {
      await closeProxyChild(proxy);
    } finally {
      // Wait for the actual router exit before its shared cleanup removes the
      // disposable home. Killing and immediately removing races shutdown writes.
      if (router && router.process.exitCode === null && router.process.signalCode === null) {
        const exited = once(router.process, "close");
        router.process.kill("SIGKILL");
        await exited;
      }
      await Promise.all([proxy?.close(), router?.close(), frontend?.close()]);
      if (router) await rm(router.root, { recursive: true, force: true });
      server.closeAllConnections();
      await new Promise<void>(r => server.close(() => r()));
      requests.length = replies.length = 0;
    }
  };
  try {
    const repoRoot = resolve(import.meta.dirname, "../..");
    router = await startIsolatedRouter({ repoRoot, managed: true, testClock: true, embedded: { endpoint: `http://127.0.0.1:${address.port}`, kbDir } });
    proxy = await startRouterProxy({ repoRoot, upstreamPort: router.port });
    await expect.poll(async () => {
      try { return (await fetch(proxy!.url + "/v1/health")).status; } catch { return 0; }
    }, { timeout: 5000 }).toBe(200);
    frontend = await serveFrontendDist(join(repoRoot, "web/router-ui-react/dist"));
    const f = { router, proxy, frontend, requests, violations, replies, wsFrames, close,
      async api(path: string, body?: object, method = "POST") {
        const res = await fetch(proxy!.url + path, body === undefined ? undefined : {
          method, headers: { "Content-Type": "application/json", "X-Request-ID": randomUUID() }, body: JSON.stringify(body),
        });
        const text = await res.text();
        expect(res.ok, `${method} ${path}: ${res.status} ${text}`).toBeTruthy();
        return text ? JSON.parse(text) : null;
      },
      rows(sql: string, agent: string) {
        const db = new DatabaseSync(router!.databasePath, { readOnly: true });
        try { return db.prepare(sql).all(agent); } finally { db.close(); }
      },
      async open(page: Page) {
        page.setDefaultTimeout(8000);
        page.on("websocket", socket => {
          expect(socket.url()).toContain(`127.0.0.1:${proxy!.port}`);
          socket.on("framereceived", frame => {
            if (wsFrames.length < 2048) wsFrames.push(String(frame.payload));
          });
        });
        await page.addInitScript(url => {
          localStorage.setItem("mecha.web.activeRouter", url);
          localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
        }, proxy!.url);
        await page.goto(frontend!.url);
        await expect(page.getByRole("button", { name: "Managed actions", exact: true })).toBeVisible();
      },
    };
    await f.api("/v1/settings/default-model", { model }, "PUT");
    return f;
  } catch (error) { await close(); throw error; }
}

export async function userTurn(page: Page, text: string) {
  const composer = page.getByRole("textbox", { name: "Message", exact: true });
  await expect(composer).toBeVisible();
  await composer.fill(text);
  await composer.press("Enter");
}
