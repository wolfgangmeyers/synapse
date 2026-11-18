import { createServer } from "node:http";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { mkdtemp, mkdir, writeFile, readFile, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { randomUUID } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { expect, test, type Page } from "@playwright/test";
import { allocatePort, startRouterProxy, serveFrontendDist } from "./harness";

export const selections = [
  { provider: "google", id: "gemini-2.5-flash" },
  { provider: "google-vertex", id: "gemini-3.8-flash" },
  { provider: "openai-codex", id: "gpt-6.1-terra" },
];
type Reply = { tool: string; args: object; thoughtSignature?: string } | { answer: string };
export async function providerFixture(signedOutGoogle = false) {
  const repo = resolve(import.meta.dirname, "../..");
  const root = await mkdtemp(join(tmpdir(), "agr-e2e-"));
  const history: { path: string; body: Record<string, unknown>; wake: boolean }[] = [];
  let logs = "";
  const violations: string[] = [];
  const replies: Reply[] = [];
  const frames: string[] = [];
  let beforeReply: (() => void) | undefined;
  const sse = (events: object[]) => events.map(e => `data: ${JSON.stringify(e)}\n\n`).join("");
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", chunk => { body += chunk; if (body.length > 2 * 1024 * 1024) req.destroy(); });
    req.on("end", () => {
      if (req.url === "/adc/token") {
        if (!body.includes("refresh_token=agr-refresh")) violations.push("ADC refresh mismatch");
        res.writeHead(200, { "Content-Type": "application/json" }).end(JSON.stringify({ access_token: "agr-adc-token", expires_in: 3600 })); return;
      }
      const codex = req.url === "/codex/responses";
      const google = req.url?.startsWith("/gemini/");
      const vertex = req.url?.startsWith("/vertex/");
      if (req.method !== "POST" || (!codex && !google && !vertex) || history.length >= 80) {
        violations.push(`unexpected route ${req.url}`); res.writeHead(400).end(); return;
      }
      if (codex ? (!req.headers.authorization?.endsWith(".agr-secret") || req.headers["chatgpt-account-id"] !== "agr-account") : google ? (!req.url?.endsWith("key=agr-key") || !!req.headers.authorization) : req.headers.authorization !== "Bearer agr-adc-token") {
        violations.push("provider auth mismatch"); res.writeHead(401).end(); return;
      }
      const parsed = JSON.parse(body);
      const wake = JSON.stringify(parsed).includes("[Subagent Completed]");
      history.push({ path: req.url!, body: parsed, wake });
      const hook = beforeReply; beforeReply = undefined; hook?.();
      const reply = wake ? { answer: "AGR completion acknowledged" } : replies.shift();
      if (!reply) { violations.push("unqueued provider request"); res.writeHead(400).end(); return; }
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      if (!codex) {
        const part: Record<string, any> = "tool" in reply ? { functionCall: { name: reply.tool, args: reply.args } } : { text: reply.answer };
        if ("tool" in reply && reply.thoughtSignature) {
          part.thoughtSignature = reply.thoughtSignature;
          part.thought_signature = reply.thoughtSignature;
        }
        res.end(sse([{ candidates: [{ content: { role: "model", parts: [part] }, finishReason: "STOP" }], usageMetadata: { promptTokenCount: 12, candidatesTokenCount: 8 } }])); return;
      }
      const id = `agr_${history.length}`;
      const events: object[] = [{ type: "response.created", response: { id } }];
      if ("tool" in reply) {
        const item = { type: "function_call", id: `fc_${id}`, call_id: `call_${id}`, name: reply.tool, arguments: JSON.stringify(reply.args) };
        events.push({ type: "response.output_item.added", output_index: 0, item: { ...item, arguments: "" } }, { type: "response.output_item.done", output_index: 0, item });
      } else {
        const item = { type: "message", id: `msg_${id}`, role: "assistant", content: [] };
        events.push({ type: "response.output_item.added", output_index: 0, item }, { type: "response.content_part.added", output_index: 0, content_index: 0, part: { type: "output_text", text: "" } }, { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: reply.answer }, { type: "response.output_item.done", output_index: 0, item: { ...item, status: "completed", content: [{ type: "output_text", text: reply.answer }] } });
      }
      events.push({ type: "response.completed", response: { id, status: "completed" } });
      res.end(sse(events));
    });
  });
  await new Promise<void>(r => server.listen(0, "127.0.0.1", r));
  const addr = server.address();
  if (!addr || typeof addr === "string") throw Error("missing mock address");
  const endpoint = `http://127.0.0.1:${addr.port}`;
  let child: ReturnType<typeof spawn> | undefined;
  let proxy: Awaited<ReturnType<typeof startRouterProxy>> | undefined;
  let frontend: Awaited<ReturnType<typeof serveFrontendDist>> | undefined;
  const close = async () => {
    try {
      const diagnostics = test.info().outputPath(`provider-diagnostics-${signedOutGoogle ? "signed-out" : "authenticated"}.json`);
      let mailbox: object[] = [];
      try { const db = new DatabaseSync(join(root, "router.sqlite")); try { mailbox = db.prepare("SELECT * FROM messages").all(); } finally { db.close(); } } catch {}

      await writeFile(diagnostics, JSON.stringify({ root, pid: child?.pid, exitCode: child?.exitCode, signalCode: child?.signalCode, mailbox, logs, history, violations, queued: replies }, null, 2));
      await test.info().attach("provider-diagnostics", { path: diagnostics, contentType: "application/json" });
    } finally {
      if (child && child.exitCode === null && child.signalCode === null) { const done = once(child, "close"); child.kill("SIGKILL"); await done; }
      if (proxy?.process.pid && proxy.process.exitCode === null) {
        let pids = "";
        try { pids = execFileSync("ps", ["-o", "pid=", "--ppid", String(proxy.process.pid)], { encoding: "utf8" }); } catch (e) { if (e.status !== 1) throw e; }
        for (const pid of pids.trim().split(/\s+/).filter(Boolean)) { try { process.kill(Number(pid), "SIGTERM"); } catch (e) { if (e.code !== "ESRCH") throw e; } }
      }
      await Promise.all([proxy?.close(), frontend?.close()]);
      server.closeAllConnections(); await new Promise<void>(r => server.close(() => r()));
      await rm(root, { recursive: true, force: true });
      history.length = replies.length = frames.length = 0;
    }
  };
  try {
    const home = join(root, "home"), agentDir = join(home, ".pi/agent"), work = join(root, "work");
    for (const p of [agentDir, work, join(home, "sessions"), join(root, "private"), join(root, "config")]) await mkdir(p, { recursive: true, mode: 0o700 });
    const adc = join(root, "private/adc.json");
    await writeFile(adc, JSON.stringify({ type: "authorized_user", client_id: "agr-client", client_secret: "agr-client-secret", refresh_token: "agr-refresh", quota_project_id: "agr-project", token_uri: "https://oauth2.googleapis.com/token" }), { mode: 0o600 });
    await writeFile(join(agentDir, "auth.json"), JSON.stringify({ google: { type: "api_key", key: "agr-key" }, "google-vertex": { type: "env", env: { GOOGLE_APPLICATION_CREDENTIALS: adc, GOOGLE_CLOUD_PROJECT: "agr-project", GOOGLE_CLOUD_LOCATION: "global" } } }), { mode: 0o600 });
    const claims = Buffer.from(JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: "agr-account" } })).toString("base64url");
    await writeFile(join(root, "private/codex.json"), JSON.stringify({ access_token: `fake.${claims}.agr-secret`, refresh_token: "agr-refresh", account_id: "agr-account", expires_at: "2099-01-01T00:00:00Z" }), { mode: 0o600 });
    await writeFile(join(work, "note.txt"), "AGR real tool evidence");
    await writeFile(join(root, "manifest.json"), await readFile(join(repo, "router/contract/managed-tools-v1.json")));
    // Embedded provider tests do not load the retired Pi RPC extension.
    await writeFile(join(root, "mecha-rpc.ts"), "export default function () {}\n");
    // Go resolves the credential consumer beside AuthHelperPath. Exercise the
    // actual private stdin/ModelRuntime projection, not a fixture substitute.
    await writeFile(join(root, "codex-consumer.mjs"), await readFile(join(repo, "auth-helper/codex-consumer.mjs")));
    await symlink(join(repo, "auth-helper/node_modules"), join(root, "node_modules"), "dir");
    const helper = join(root, "auth.mjs");
    await writeFile(helper, `#!${process.execPath}\nimport {createInterface} from 'node:readline';\ncreateInterface({input:process.stdin}).on('line',()=>console.log(JSON.stringify(${JSON.stringify({ providers: [...selections.map(m => ({ provider: m.provider, label: ({ google: "Google", "google-vertex": "Google Vertex AI", "openai-codex": "ChatGPT" })[m.provider], authenticated: !(signedOutGoogle && m.provider !== "openai-codex"), status: signedOutGoogle && m.provider !== "openai-codex" ? "signed_out" : "authenticated" })), { provider: "cursor", label: "Cursor", authenticated: false, status: "signed_out" }], models: selections.filter(m => !signedOutGoogle || m.provider === "openai-codex").map(m => ({ ...m, name: m.id, available: true })) })})));`, { mode: 0o700 });
    // Compile-only overlay of the existing test seam. No production sources are
    // changed: selection/admission/streams/tools stay real. Default HTTP calls
    // are fail-closed and only the Gemini, Vertex and ADC wire endpoints map local.
    const original = join(repo, "router-proxy/cmd/mailbox-router/embedded_restart_fake_enabled.go");
    let source = await readFile(original, "utf8");
    source = source.replace('"github.com/sky-valley/pi/ai"', "");
    source = source.replace(/\tout.EmbeddedModelResolver = func[\s\S]*?\n\taddress :=/, "\taddress :=");
    source = source.replace('return nil, fmt.Errorf("embedded restart fake request denied")', 'fmt.Fprintf(os.Stderr, "AGR denied Codex request: method=%s url=%s host=%s\\n", req.Method, req.URL.String(), req.Host); return nil, fmt.Errorf("embedded restart fake request denied")');
    source = source.replace("out.CodexHTTPTransport =", "http.DefaultTransport = agrTransport{endpoint: endpoint, transport: transport}\n\tout.CodexHTTPTransport =");
    source += `\ntype agrTransport struct { endpoint string; transport http.RoundTripper }\nfunc (d agrTransport) RoundTrip(req *http.Request) (*http.Response,error) {\n prefix := \"\"\n switch req.URL.Host {\n case \"generativelanguage.googleapis.com\": prefix=\"/gemini\"\n case \"aiplatform.googleapis.com\": prefix=\"/vertex\"\n case \"oauth2.googleapis.com\": prefix=\"/adc\"\n case \"chatgpt.com\": if req.URL.Path != \"/backend-api/codex/responses\" { return nil,fmt.Errorf(\"AGR Codex route denied\") }; prefix=\"/codex\"\n default: return nil,fmt.Errorf(\"AGR external dial denied: %s\", req.URL.Host)\n }\n copy:=req.Clone(req.Context()); copy.URL,_=url.Parse(d.endpoint+prefix+req.URL.RequestURI()); if prefix==\"/codex\" { copy.URL,_=url.Parse(d.endpoint+\"/codex/responses\") }; copy.Host=copy.URL.Host\n return d.transport.RoundTrip(copy)\n}\n`;
    await writeFile(join(root, "seam.go"), source);
    await writeFile(join(root, "overlay.json"), JSON.stringify({ Replace: { [original]: join(root, "seam.go") } }));
    const build = spawn("go", ["build", "-tags=embedded_restart_test_only", `-overlay=${join(root, "overlay.json")}`, "-o", join(root, "router"), "./cmd/mailbox-router"], { cwd: join(repo, "router-proxy"), stdio: ["ignore", "pipe", "pipe"] });
    let buildError = ""; build.stderr.on("data", c => buildError += c);
    const [code] = await once(build, "close"); if (code !== 0) throw Error(buildError);
    const pi = join(root, "pi.mjs");
    await writeFile(pi, (await readFile(join(repo, "web/router-ui-react/tests/functional/fixtures/fake-managed-pi.mjs"), "utf8")).replace("#!/usr/bin/env node", `#!${process.execPath}`), { mode: 0o700 });
    const port = await allocatePort();
    const databasePath = join(root, "router.sqlite");
    child = spawn(join(root, "router"), ["--database", databasePath, "--log", join(root, "router.jsonl"), "--host", "127.0.0.1", "--port", String(port), "--managed-pi", pi, "--managed-home", home, "--managed-pi-agent-dir", agentDir, "--managed-session-root", join(home, "sessions"), "--managed-manifest", join(root, "manifest.json"), "--managed-mecha-rpc", join(root, "mecha-rpc.ts"), "--managed-pi-subagents", resolve(repo, "../pi-packages/packages/pi-subagents/src/index.ts"), "--managed-auth-helper", helper, "--managed-runtime-root", root, "--managed-cwd-roots", root, "--managed-quiet-recipient", "functional-supervisor", "--codex-credential-path", join(root, "private/codex.json")], { cwd: repo, env: { PATH: process.env.PATH, HOME: home, XDG_CONFIG_HOME: join(root, "config"), LANG: "C.UTF-8", EMBEDDED_RESTART_FAKE_ENABLE: "1", EMBEDDED_RESTART_FAKE_ENDPOINT: endpoint }, stdio: ["ignore", "pipe", "pipe"] });
    child.stdout!.on("data", c => logs += c); child.stderr!.on("data", c => logs += c);
    try { await expect.poll(() => {
      if (child!.exitCode !== null || child!.signalCode !== null) throw Error(`router exited: ${child!.exitCode}/${child!.signalCode}\n${logs}`);
      return logs.includes("MECHA_V2_ROUTER_READY");
    }, { timeout: 30000, message: "router startup (real credential consumer and helper)" }).toBe(true); } catch (error) { throw Error(`${String(error)}\n${logs}`); }
    proxy = await startRouterProxy({ repoRoot: repo, upstreamPort: port });
    frontend = await serveFrontendDist(join(repo, "web/router-ui-react/dist"));
    const url = proxy.url;
    return { root, work, databasePath, history, violations, replies, frames, close,
      get turns() { return history.filter(call => !call.wake); },
      get wakes() { return history.filter(call => call.wake); },
      beforeReply(hook: () => void) { beforeReply = hook; },
      rows(sql: string, ...params: string[]) { const db = new DatabaseSync(databasePath); try { return db.prepare(sql).all(...params); } finally { db.close(); } },
      exec(sql: string) { const db = new DatabaseSync(databasePath); try { db.exec(sql); } finally { db.close(); } },
      async api(path: string, body?: object, method = "POST") { const res = await fetch(url + path, body ? { method, headers: { "Content-Type": "application/json", "X-Request-ID": randomUUID() }, body: JSON.stringify(body) } : undefined); const text = await res.text(); expect(res.ok, `${path}: ${res.status} ${text}`).toBeTruthy(); return text ? JSON.parse(text) : null; },
      async open(page: Page) {
        page.on("console", message => { logs += `\nbrowser ${message.type()}: ${message.text()}`; });
        page.on("response", async response => { if (response.url().includes("/v1/provider-auth") || response.url().includes("/v1/models")) { try { logs += `\nHTTP ${response.status()} ${response.url()} ${await response.text()}`; } catch {} } });
        page.on("websocket", socket => socket.on("framereceived", frame => { if (frames.length < 2048) frames.push(String(frame.payload)); }));
        await page.route("**/*", route => ["localhost", "127.0.0.1"].includes(new URL(route.request().url()).hostname) ? route.continue() : route.abort());
        await page.addInitScript(url => { localStorage.setItem("mecha.web.activeRouter", url); localStorage.setItem("mecha.web.routers", JSON.stringify([url])); }, url);
        await page.goto(frontend!.url);
        await expect(page.getByRole("button", { name: "Managed actions", exact: true })).toBeVisible();
      },
    };
  } catch (e) { await close(); throw e; }
}
