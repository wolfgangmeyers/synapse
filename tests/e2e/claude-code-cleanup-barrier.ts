import { createServer, type ServerResponse } from "node:http";
import { randomBytes } from "node:crypto";

// One bounded, authenticated hold at the real post-reap/pre-delete boundary.
export async function cleanupBarrier() {
  const token = randomBytes(32).toString("hex");
  let pending: ServerResponse | undefined;
  let arrive!: (action: string) => void;
  let reject!: (error: Error) => void;
  const arrived = new Promise<string>((resolve, rejectPromise) => { arrive = resolve; reject = rejectPromise; });
  void arrived.catch(() => {});
  const timer = setTimeout(() => reject(Error("cleanup barrier arrival timeout")), 15000);
  const server = createServer((req, res) => {
    if (req.method !== "POST" || req.url !== "/cleanup" || req.headers.authorization !== `Bearer ${token}` || pending) {
      req.resume(); res.writeHead(403).end(); return;
    }
    let body = "";
    req.on("data", chunk => { body += String(chunk); if (body.length > 4096) req.destroy(); });
    req.on("end", () => {
      let value: unknown;
      try { value = JSON.parse(body); } catch { res.writeHead(400).end(); return; }
      if (!value || typeof value !== "object" || !("action_id" in value) || typeof value.action_id !== "string" || !value.action_id) { res.writeHead(400).end(); return; }
      pending = res; clearTimeout(timer); arrive(value.action_id);
    });
  });
  await new Promise<void>((resolve, rejectPromise) => { server.once("error", rejectPromise); server.listen(0, "127.0.0.1", resolve); });
  const address = server.address();
  if (!address || typeof address === "string") throw Error("missing cleanup listener");
  return {
    token, endpoint: `http://127.0.0.1:${address.port}`, arrived,
    assertHeld() { if (!pending || pending.destroyed || pending.writableEnded) throw Error("cleanup not held"); },
    release() { if (!pending || pending.destroyed || pending.writableEnded) throw Error("cleanup not held"); pending.writeHead(204).end(); pending = undefined; },
    async close() { clearTimeout(timer); if (pending && !pending.destroyed) pending.writeHead(503).end(); pending = undefined; server.closeAllConnections(); await new Promise<void>(resolve => server.close(() => resolve())); },
  };
}
