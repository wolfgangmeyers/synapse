import assert from "node:assert/strict";
import http from "node:http";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { createTmuxAdapter } from "../harness/tmux-adapter.js";

function call(url, method, body, headers = {}) {
  return new Promise((resolve, reject) => {
    const request = http.request(url, { method, headers: { ...(body === undefined ? {} : { "content-type": "application/json" }), ...headers } }, (response) => {
      let text = ""; response.setEncoding("utf8"); response.on("data", (chunk) => { text += chunk; });
      response.on("end", () => resolve({ status: response.statusCode, headers: response.headers, body: text === "" ? undefined : JSON.parse(text) }));
    });
    request.on("error", reject); if (body !== undefined) request.end(JSON.stringify(body)); else request.end();
  });
}

async function fixture(t, adapter, options = {}) {
  const directory = await mkdtemp(join(tmpdir(), "router-tmux-")); t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") }); t.after(() => repository.close());
  const router = createAgentRouter({ repository, tmuxAdapter: adapter, timeoutSweepIntervalMs: 60_000, ...options });
  const ws = await router.listen(); t.after(() => router.close());
  return `http://${ws.slice(5).replace(/\/v1\/agents$/, "")}`;
}

test("tmux REST lifecycle accepts arbitrary origins and preserves the directive", async (t) => {
  const base = await fixture(t, { enumerate: async () => ["old"] }, { startupSession: "old" });
  let result = await call(`${base}/v1/health`, "GET", undefined, { origin: "https://random-origin.example", "x-correlation-id": "corr-1" });
  assert.equal(result.status, 200); assert.equal(result.headers["x-correlation-id"], "corr-1");
  result = await call(`${base}/v1/health`, "OPTIONS", undefined, { origin: "https://random-origin.example" }); assert.equal(result.status, 204);
});

test("tmux REST rejects invalid path names before adapter invocation", async (t) => {
  let invoked = false;
  const base = await fixture(t, {
    enumerate: async () => [],
    remove: async () => { invoked = true; },
  });
  const result = await call(`${base}/v1/tmux/windows/%2F`, "DELETE");
  assert.equal(result.status, 400);
  assert.equal(result.body.error.code, "tmux_invalid_name");
  assert.equal(invoked, false);
});
