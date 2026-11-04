import { createServer, type ServerResponse } from "node:http";
import { DatabaseSync } from "node:sqlite";
import { randomBytes } from "node:crypto";
import type { IsolatedRouter } from "./harness";
import { expect, type Page, type WebSocket } from "@playwright/test";

// Install before navigation: HTTP history can settle while ui-live is still
// connecting. Its initial snapshot is the real subscription boundary, not DOM
// visibility or the independent /v1/agents registration. Retain only two bits
// for the current socket; dispose removes listeners at fixture teardown.
export function recoveryLiveEvidence(page: Page, proxyURL: string, agent: string, answer: string) {
  const endpoint = new URL("/v1/ui-live", proxyURL);
  endpoint.protocol = "ws:";
  let current: WebSocket | undefined;
  let snapshot = false;
  let final = false;
  function received(frame: { payload: string | Buffer }) {
    const value: unknown = JSON.parse(String(frame.payload));
    if (!value || typeof value !== "object" || !("type" in value)) return;
    if (value.type === "managed_agents_snapshot") snapshot = true;
    if (snapshot && value.type === "history_entry" && "view" in value && value.view === "complete" &&
        "entry" in value && value.entry && typeof value.entry === "object" &&
        "agent_id" in value.entry && value.entry.agent_id === agent &&
        "message" in value.entry && value.entry.message === "Assistant response" &&
        "message_content" in value.entry && value.entry.message_content === answer) final = true;
  }
  function closed() { snapshot = false; }
  function detach() {
    current?.off("framereceived", received);
    current?.off("close", closed);
  }
  function opened(socket: WebSocket) {
    if (socket.url() !== endpoint.toString()) return;
    detach(); current = socket; snapshot = false; final = false;
    socket.on("framereceived", received); socket.on("close", closed);
  }
  page.on("websocket", opened);
  return {
    ready: () => expect.poll(() => snapshot, { timeout: 8000, message: "recovered ui-live snapshot before foreground input" }).toBe(true),
    final: () => expect.poll(() => final, { message: "exact foreground assistant history_entry on recovered ui-live socket" }).toBe(true),
    close() { page.off("websocket", opened); detach(); current = undefined; snapshot = final = false; },
  };
}

export interface EffectReceipt {
  phase: "intent" | "domain" | "checked";
  agent_id: string; session_id: string; epoch: number; generation: string;
  owner_token: number; effect_id: string;
}
function validReceipt(value: unknown): value is EffectReceipt {
  if (!value || typeof value !== "object") return false;
  return "phase" in value && ["intent", "domain", "checked"].includes(String(value.phase)) &&
    ["agent_id", "session_id", "generation", "effect_id"].every(key =>
      key in value && typeof Reflect.get(value, key) === "string" && Reflect.get(value, key).length > 0) &&
    "epoch" in value && Number.isSafeInteger(value.epoch) && Number(value.epoch) > 0 &&
    "owner_token" in value && Number.isSafeInteger(value.owner_token) && Number(value.owner_token) > 0;
}

// Disposable test transport, not an execution/replay API. Caller arms with the
// exact canonical identity supplied by core, never a raw CLI ID or latest row.
// Retention: one receipt/held response, <=4KiB body, <=10s hold, close clears all.
export async function effectBarrier(options: { arrivalMS?: number; bodyMS?: number; holdMS?: number } = {}) {
  for (const [key, value] of Object.entries(options)) {
    const maximum = key === "bodyMS" ? 2000 : 10_000;
    if (!Number.isSafeInteger(value) || value < 1 || value > maximum) throw Error(`invalid barrier deadline: ${key}`);
  }
  const token = randomBytes(32).toString("hex");
  let scope: EffectReceipt | undefined;
  let receipt: EffectReceipt | undefined;
  let bindingScope: Omit<EffectReceipt, "effect_id"> | undefined;
  let pending: ServerResponse | undefined;
  let holdTimer: ReturnType<typeof setTimeout> | undefined;
  let arrivalTimer: ReturnType<typeof setTimeout> | undefined;
  let terminal = false;
  let arrive!: (receipt: EffectReceipt) => void;
  let rejectArrival!: (error: Error) => void;
  let finish!: (reason: string) => void;
  const arrived = new Promise<EffectReceipt>((resolve, reject) => { arrive = resolve; rejectArrival = reject; });
  // Consumers may arm before installing their await; retain rejection, not an
  // unhandled promise. Awaiting the original still rejects normally.
  void arrived.catch(() => {});
  const finished = new Promise<string>(resolve => { finish = resolve; });
  function fail(error: Error) {
    terminal = true; clearTimeout(arrivalTimer); rejectArrival(error);
  }
  function endHold(reason: string, status?: number) {
    const response = pending; pending = undefined; clearTimeout(holdTimer);
    terminal = true;
    if (response && status !== undefined && !response.destroyed) response.writeHead(status).end();
    finish(reason);
  }
  const server = createServer((req, res) => {
    if (req.method !== "POST" || req.url !== "/barrier" || req.headers.authorization !== `Bearer ${token}`) {
      res.writeHead(403).end(); req.resume(); return;
    }
    let body = "";
    req.setTimeout(options.bodyMS ?? 2000, () => { fail(Error("barrier body timeout")); req.destroy(); });
    req.on("error", () => { if (!req.complete) fail(Error("barrier transport failed")); });
    req.on("aborted", () => fail(Error("barrier transport aborted")));
    req.on("data", chunk => {
      body += String(chunk);
      if (Buffer.byteLength(body) > 4096) { fail(Error("barrier oversize body")); req.destroy(); }
    });
    req.on("end", () => {
      // IncomingMessage timeout configures the socket too. Neither may govern
      // the held response: its own deadline below is the only hold timer.
      req.setTimeout(0); req.socket.setTimeout(0);
      let value: unknown;
      try { value = JSON.parse(body); } catch { res.writeHead(400).end(); return; }
      if (!validReceipt(value)) { res.writeHead(400).end(); return; }
      if (terminal || receipt || !(scope ?? bindingScope) || Object.keys(scope ?? bindingScope!).some(key => Reflect.get(scope ?? bindingScope!, key) !== Reflect.get(value, key))) {
        res.writeHead(409).end(); return;
      }
      receipt = value; pending = res; clearTimeout(arrivalTimer);
      res.once("close", () => { if (pending === res) endHold("disconnect"); });
      holdTimer = setTimeout(() => endHold("timeout", 504), options.holdMS ?? 10_000);
      arrive(value);
    });
  });
  server.on("error", error => { fail(error); endHold("transport", 503); });
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject); server.listen(0, "127.0.0.1", () => { server.removeListener("error", reject); resolve(); });
  });
  const address = server.address();
  if (!address || typeof address === "string") throw Error("missing barrier listener");
  let closing: Promise<void> | undefined;
  return {
    endpoint: `http://127.0.0.1:${address.port}`, token, arrived, finished,
    assertHeld() {
      if (terminal || !pending || pending.destroyed || pending.writableEnded) throw Error("barrier is not held pending at kill");
    },
    arm(identity: EffectReceipt) {
      if (terminal || scope || !validReceipt(identity)) throw Error("barrier requires one valid exact scope");
      scope = { ...identity };
      arrivalTimer = setTimeout(() => fail(Error("barrier arrival deadline")), options.arrivalMS ?? 10_000);
    },
    armBinding(identity: Omit<EffectReceipt, "effect_id">) {
      if (terminal || scope || bindingScope || !validReceipt({ ...identity, effect_id: "validation-only" })) throw Error("invalid binding scope");
      bindingScope = { ...identity };
      arrivalTimer = setTimeout(() => fail(Error("barrier arrival deadline")), options.arrivalMS ?? 10_000);
    },
    release() {
      if (!pending || pending.destroyed || pending.writableEnded) throw Error("no held barrier");
      endHold("released", 204);
    },
    close() {
      closing ??= (async () => {
        fail(Error("barrier closed")); endHold("closed", 503);
        scope = receipt = undefined; bindingScope = undefined;
        const closed = new Promise<void>((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
        server.closeAllConnections(); await closed;
      })();
      return closing;
    },
  };
}

// ChildProcess handle belongs to the fixture, not a numeric PID lookup. Join
// exit before existing restart() reopens the same SQLite database. No SIGTERM.
export async function crashAndRestart(router: IsolatedRouter, barrier?: Pick<Awaited<ReturnType<typeof effectBarrier>>, "assertHeld" | "finished">) {
  const child = router.process;
  if (child.exitCode !== null || child.signalCode !== null) throw Error("router already exited before crash");
  await new Promise<void>((resolve, reject) => {
    const timer = setTimeout(() => { child.removeListener("close", closed); reject(Error("router crash did not join")); }, 5000);
    function closed(_code: number | null, signal: NodeJS.Signals | null) {
      clearTimeout(timer);
      if (signal !== "SIGKILL") reject(Error(`unexpected router exit: ${signal}`)); else resolve();
    }
    child.once("close", closed);
    // No await between this assertion and SIGKILL.
    barrier?.assertHeld();
    if (!child.kill("SIGKILL")) {
      clearTimeout(timer); child.removeListener("close", closed); reject(Error("router SIGKILL failed"));
    }
  });
  if (barrier && await barrier.finished !== "disconnect") throw Error("crash barrier ended without crash disconnect");
  await router.restart();
}

// Read-only original-effect inspector: full saved binding and canonical ID, no
// owner renewal, no grant, no Execute. This is evidence lookup, NOT MCP replay.
export function inspectOriginalEffect(databasePath: string, receipt: EffectReceipt) {
  const db = new DatabaseSync(databasePath, { readOnly: true });
  try {
    return db.prepare(`SELECT state, CAST(result AS TEXT) AS result FROM embedded_effects
      WHERE agent_id=? AND session_id=? AND epoch=? AND generation=? AND effect_id=?`)
      .get(receipt.agent_id, receipt.session_id, receipt.epoch, receipt.generation, receipt.effect_id);
  } finally { db.close(); }
}

