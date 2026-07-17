import assert from "node:assert/strict";
import { once } from "node:events";
import { createServer } from "node:http";
import { spawn } from "node:child_process";
import test from "node:test";

function dispatch(args) {
  return new Promise((resolve, reject) => {
    const child = spawn("python3", ["tools/sentinel_dispatch.py", ...args]);
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", reject);
    child.on("exit", (code) => resolve({ code, stdout, stderr }));
  });
}

test("dispatch submits model-only Sentinel requests for immediate and scheduled work", async (t) => {
  const requests = [];
  const server = createServer(async (request, response) => {
    let body = "";
    for await (const chunk of request) body += chunk;
    requests.push({ path: request.url, body: JSON.parse(body) });
    response.writeHead(201, { "content-type": "application/json" });
    response.end(JSON.stringify({ accepted: true }));
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  t.after(() => server.close());
  const router = `http://127.0.0.1:${server.address().port}`;

  assert.equal((await dispatch(["--router", router, "--to", "target", "--prompt", "private", "--model", "chosen", "--id", "immediate"])).code, 0);
  assert.equal((await dispatch(["--router", router, "--to", "target", "--prompt", "private", "--model", "chosen", "--id", "recurring", "--next-due-at", "2030-01-01T00:00:00.000Z", "--interval-ms", "60000"])).code, 0);

  assert.deepEqual(requests.map(({ path, body }) => ({ path, sentinel: body.sentinel, target: body.to ?? body.target })), [
    { path: "/v1/messages", sentinel: { model: "chosen" }, target: "target" },
    { path: "/v1/reminders", sentinel: { model: "chosen" }, target: "target" },
  ]);
  assert.equal(requests.every(({ body }) => !("receiver_policy" in body.sentinel) && !("origin_sender" in body.sentinel)), true);
});
