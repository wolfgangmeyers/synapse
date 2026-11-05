import { mkdtemp, mkdir, writeFile, readFile, rename, rm, stat } from "node:fs/promises";
import { pathToFileURL } from "node:url";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";
import { randomUUID } from "node:crypto";
import { expect, type Page } from "@playwright/test";
import { workflowHarness, assertLocalEvaluatorDefault, userTurn as composeUserTurn, type WorkflowHarnessOptions } from "./workflow-implicit-goals-harness";

// Test-only launcher: workflowHarness intentionally sanitizes environment but
// preserves PATH. No production environment exception or real CLI is required.
export async function claudeWorkersHarness(script: object = {}, options: WorkflowHarnessOptions = {}) {
  const root = await mkdtemp(join(tmpdir(), "claude-browser-"));
  const scriptPath = join(root, "script.json");
  const recordPath = join(root, "record.jsonl");
  const bin = join(root, "bin");
  await mkdir(bin, { mode: 0o700 });
  await writeFile(scriptPath, JSON.stringify(script), { mode: 0o600 });
  const executable = resolve(import.meta.dirname, "../fixtures/fake-claude/claude.mjs");
  // Optional acceptance-only stdout injection after a test observes checked
  // idle. fs.watch provides a release barrier without sleeps or fake edits.
  await writeFile(join(bin, "claude"), `#!${process.execPath}
import { watch, readFileSync, writeFileSync, appendFileSync } from 'node:fs';
import { randomUUID } from 'node:crypto';
import { join } from 'node:path';
process.env.FAKE_CLAUDE_SCRIPT=${JSON.stringify(scriptPath)};
process.env.FAKE_CLAUDE_RECORD=${JSON.stringify(recordPath)};
const argv=process.argv.slice(2);
if(argv.includes('-p')) {
  // Optional reset checkpoint runs before the replacement CLI reads any input.
  let checkpoint;
  try { checkpoint=JSON.parse(readFileSync(join(${JSON.stringify(root)}, 'reset-checkpoint.json'),'utf8')); }
  catch(error) { if(error.code!=='ENOENT') throw error; }
  if(checkpoint) {
    const snapshot=(pid)=>{
      try {
        const raw=readFileSync('/proc/'+pid+'/stat','utf8');
        const fields=raw.slice(raw.lastIndexOf(')')+2).trim().split(/\\s+/);
        return {state:fields[0],group:Number(fields[2]),start:fields[19]};
      } catch(error) { if(error.code==='ENOENT' || error.code==='ESRCH') return null; throw error; }
    };
    const response=await fetch(checkpoint.grant.url,{method:'POST',headers:{...checkpoint.grant.headers,'Content-Type':'application/json'},
      body:JSON.stringify({jsonrpc:'2.0',id:'reset-checkpoint',method:'tools/call',params:{name:'mecha_context',arguments:{action:'status'}}}),signal:AbortSignal.timeout(6500),redirect:'error'});
    const body=await response.json();
    appendFileSync(${JSON.stringify(recordPath)}, JSON.stringify({kind:'reset_checkpoint',pid:process.pid,leader:snapshot(checkpoint.leader),child:snapshot(checkpoint.child),status:response.status,body})+'\\n', {mode:0o600});
  }
  const token=randomUUID();
  const release=join(${JSON.stringify(root)}, 'release-'+token+'.json');
  let revision='';
  const watcher=watch(${JSON.stringify(root)}, (_,name)=>{
    if(name!=='release-'+token+'.json') return;
    let payload;
    try { payload=JSON.parse(readFileSync(release,'utf8')); }
    catch(error) { if(error.code==='ENOENT') return; throw error; }
    if(payload.token!==token || payload.revision===revision) return;
    revision=payload.revision;
    const flag=argv.includes('--resume')?'--resume':'--session-id';
    const session=argv[argv.indexOf(flag)+1];
    // Acknowledge consumption before output: overflow intentionally kills this process.
    writeFileSync(join(${JSON.stringify(root)}, 'ack-'+token+'.json'), JSON.stringify({token,revision}), {mode:0o600});
    for(const event of payload.events) {
      const line=JSON.stringify(event).replaceAll('$SESSION',session);
      process.stdout.write(line+'\\n');
    }
  });
  appendFileSync(${JSON.stringify(recordPath)}, JSON.stringify({kind:'release_target',token,pid:process.pid})+'\\n', {mode:0o600});
  watcher.unref();
}
await import(${JSON.stringify(pathToFileURL(executable).href)});
`, { mode: 0o700 });
  const savedPath = process.env.PATH;
  // An inherited override must never select a real CLI in automated tests.
  const savedBinary = process.env.MECHA_CLAUDE_BIN;
  process.env.PATH = `${bin}:${savedPath ?? ""}`;
  delete process.env.MECHA_CLAUDE_BIN;
  let f: Awaited<ReturnType<typeof workflowHarness>>;
  try { f = await workflowHarness(options); }
  catch (error) { await rm(root, { recursive: true, force: true }); throw error; }
  finally {
    if (savedPath === undefined) delete process.env.PATH; else process.env.PATH = savedPath;
    if (savedBinary === undefined) delete process.env.MECHA_CLAUDE_BIN; else process.env.MECHA_CLAUDE_BIN = savedBinary;
  }
  async function records(): Promise<Record<string, unknown>[]> {
    let raw: string;
    try { raw = await readFile(recordPath, "utf8"); }
    catch (error) { if (error.code === "ENOENT") return []; throw error; }
    // A final incomplete append is not evidence yet.
    return raw.split("\n").slice(0, -1).filter(Boolean).map(line => JSON.parse(line));
  }
  async function starts() {
    return (await records()).filter(row => row.kind === "start" && Array.isArray(row.argv) && row.argv.includes("-p"));
  }
  async function inputs() {
    return (await records()).filter(row => row.kind === "stdin" && typeof row.line === "string")
      .map(row => JSON.parse(String(row.line))).filter(row => row.type === "user");
  }
  async function configure(next: object) {
    await writeFile(scriptPath + ".new", JSON.stringify(next), { mode: 0o600 });
    await rename(scriptPath + ".new", scriptPath);
  }
  async function emitIdleEvents(events: object[], target?: string) {
    const targets = (await records()).filter(row => row.kind === "release_target" && typeof row.token === "string");
    const token = target ?? targets.at(-1)?.token;
    if (typeof token !== "string" || !targets.some(row => row.token === token)) throw Error("no registered fixture release target");
    const revision = randomUUID();
    const releasePath = join(root, `release-${token}.json`);
    await writeFile(releasePath + ".new", JSON.stringify({ token, revision, events }), { mode: 0o600 });
    await rename(releasePath + ".new", releasePath);
    await expect.poll(async () => {
      try {
        const ack = JSON.parse(await readFile(join(root, `ack-${token}.json`), "utf8"));
        return ack.token === token && ack.revision === revision;
      } catch (error) { if (error.code === "ENOENT" || error instanceof SyntaxError) return false; throw error; }
    }, { timeout: 10_000, message: `fixture release acknowledgement for ${token} revision ${revision}` }).toBe(true);
  }
  async function releaseBarrier(receipt: Record<string, unknown>) {
    const { name, invocation, pid, input_id } = receipt;
    if (typeof name !== "string" || typeof invocation !== "string" || !/^[0-9a-f-]{36}$/.test(invocation) ||
        typeof pid !== "number" || typeof input_id !== "string") throw Error("invalid barrier receipt");
    await writeFile(join(root, `barrier-${invocation}`), "release", { mode: 0o600, flag: "wx" });
    await expect.poll(async () => (await records()).some(row => row.kind === "barrier_released" &&
      row.name === name && row.invocation === invocation && row.pid === pid && row.input_id === input_id),
      { timeout: 10_000, message: `barrier release acknowledgement ${invocation}` }).toBe(true);
  }
  async function request(page: Page, path: string, body?: object, method = "POST", direct = false) {
    // Claude defaults are a deliberate admission-refusal test: the real router
    // rejects them before persistence. All potentially usable defaults must use
    // the transport-guarded local Codex fixture.
    const refusedClaude = path === "/v1/settings/default-model" && body !== undefined &&
      "model" in body && typeof body.model === "object" && body.model !== null &&
      "provider" in body.model && body.model.provider === "claude-code";
    if (!refusedClaude) assertLocalEvaluatorDefault(path, body);
    return page.evaluate(async ({ url, body, method }) => {
      const response = await fetch(url, { method: body === undefined && method === "POST" ? "GET" : method,
        credentials: "omit", headers: { "Content-Type": "application/json", "X-Request-ID": crypto.randomUUID() },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
      return { status: response.status, body: await response.json() };
    }, { url: (direct ? f.router.httpUrl : f.proxy.url) + path, body, method });
  }
  async function launch(page: Page, options: object = {}) {
    const name = `claude-${randomUUID()}`;
    const response = await request(page, "/v1/managed-agents", {
      name, cwd: `${f.router.root}/work`, runtime: "embedded", model: { provider: "claude-code", id: "sonnet" }, ...options,
    }, "POST", true);
    expect(response.status, JSON.stringify(response.body)).toBe(202);
    await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
    await page.locator(`[data-agent-id="${name}"]`).click();
    return name;
  }
  const tableRows = (table: string, agent: string) => {
    // Table names are test constants, never browser input.
    if (!/^[a-z_]+$/.test(table)) throw Error("invalid fixture table");
    return f.rows(`SELECT * FROM ${table} WHERE agent_id=?`, agent);
  };
  async function idle(agent: string) {
    await expect.poll(() => tableRows("managed_agents", agent)[0]?.activity_state).toBe("idle");
  }
  async function userTurn(page: Page, text: string) {
    // The router uses a frozen test clock. Keep composer timestamps on that
    // clock too: wall-clock inputs otherwise place checked answers before the
    // loaded timeline window, hiding real responses from browser assertions.
    const { now } = await f.router.advanceClock(1);
    await page.clock.setFixedTime(new Date(now));
    await composeUserTurn(page, text);
  }
  async function final(page: Page, agent: string, text: string, visiblePrefix?: string, checkSharedWs = true) {
    // History display is capped at 65,536 bytes. Large-output callers prove
    // full checked persistence below, but only a short prefix on UI/WS paths.
    await expect(page.getByText(visiblePrefix ?? text, { exact: visiblePrefix === undefined }).first()).toBeVisible();
    await idle(agent);
    // SDK-compatible checked assistant bodies serialize stopReason (camel
    // case), as required by store/embedded_goal_receipt.go's final validator.
    // A partial/error assistant or a previous epoch must not satisfy final().
    await expect.poll(() => f.rows(`SELECT json_extract(part.value, '$.text') AS text
      FROM embedded_entries e
      JOIN embedded_sessions s ON s.agent_id=e.agent_id AND s.session_id=e.session_id
        AND s.epoch=e.epoch AND s.generation=e.generation AND s.current=1
      JOIN json_each(e.body, '$.content') part
      WHERE e.agent_id=? AND e.kind='assistant'
        AND json_extract(e.body, '$.role')='assistant'
        AND json_extract(e.body, '$.stopReason')='stop'
        AND json_extract(part.value, '$.type')='text'`, agent)
      .some(row => typeof row.text === "string" && (visiblePrefix === undefined ? row.text.includes(text) : row.text === text))).toBe(true);
    if (checkSharedWs) await expect.poll(() => f.wsFrames.some(frame => frame.includes(visiblePrefix ?? text))).toBe(true);
  }
  async function grant() {
    const start = (await starts()).at(-1);
    if (!start || !Array.isArray(start.argv)) throw Error("no CLI process");
    const index = start.argv.indexOf("--mcp-config");
    const path = start.argv[index + 1];
    if (typeof path !== "string") throw Error("no MCP config");
    expect((await stat(path)).mode & 0o777).toBe(0o600);
    const config = JSON.parse(await readFile(path, "utf8"));
    return { path, ...config.mcpServers.mecha };
  }
  async function mcp(g: { url: string; headers: Record<string, string> }, name: string, args: object, id?: string) {
    const endpoint = new URL(g.url);
    if (endpoint.protocol !== "http:" || !["127.0.0.1", "[::1]"].includes(endpoint.hostname) || endpoint.username || endpoint.password) {
      throw Error("Fixture MCP endpoint must be literal loopback HTTP");
    }
    const response = await fetch(g.url, { method: "POST", headers: { ...g.headers, "Content-Type": "application/json" },
      body: JSON.stringify({ jsonrpc: "2.0", id: randomUUID(), method: "tools/call", params: {
        name, arguments: args, ...(id === undefined ? {} : { _meta: { "claudecode/toolUseId": id } }),
      } }), signal: AbortSignal.timeout(6500), redirect: "error" });
    return { status: response.status, body: await response.json() };
  }
  // Keep a resolvable local executable which fails without consulting PATH
  // or the host installation. This exercises executable launch failure safely.
  async function breakExecutable() {
    await rename(join(bin, "claude"), join(bin, "claude.saved"));
    await writeFile(join(bin, "claude"), "#!/nonexistent-claude-fixture-interpreter\n", { mode: 0o700 });
  }
  async function restoreExecutable() {
    await rename(join(bin, "claude.saved"), join(bin, "claude"));
  }
  return { ...f, fixtureRoot: root, records, starts, inputs, configure, releaseBarrier, request, launch, tableRows, idle, final, grant, mcp, userTurn, emitIdleEvents, breakExecutable, restoreExecutable,
    close: async () => { try { await f.close(); } finally { await rm(root, { recursive: true, force: true }); } },
  };
}
export type ClaudeWorkersHarness = Awaited<ReturnType<typeof claudeWorkersHarness>>;
export async function exists(path: string) {
  try { await stat(path); return true; } catch (error) { if (error.code === "ENOENT") return false; throw error; }
}
