import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { parseArguments } from "../tools/run_router.mjs";

const validHosts = ["127.0.0.1", "::1", "0.0.0.0"];
const invalidHosts = ["192.168.0.175", "localhost", "0.0.0.1"];

function cliOptions(host) {
  return parseArguments(["--database", "/tmp/router.sqlite", "--host", host]);
}

test("router CLI accepts loopback and LAN wildcard hosts", () => {
  for (const host of validHosts) {
    assert.equal(cliOptions(host).host, host);
  }
});

test("router CLI rejects hosts outside the explicit allowlist", () => {
  for (const host of invalidHosts) {
    assert.throws(() => cliOptions(host), /--host must be 127\.0\.0\.1, ::1, or 0\.0\.0\.0/);
  }
});

test("router constructor accepts loopback and LAN wildcard hosts", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "router-host-validation-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());

  for (const host of validHosts) {
    const router = createAgentRouter({ repository, host, timeoutSweepIntervalMs: 60_000 });
    assert.equal(typeof router.listen, "function");
  }
});

test("router constructor rejects hosts outside the explicit allowlist", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "router-host-validation-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());

  for (const host of invalidHosts) {
    assert.throws(
      () => createAgentRouter({ repository, host }),
      /host must be 127\.0\.0\.1, ::1, or 0\.0\.0\.0/,
    );
  }
});

test("router listens on the LAN wildcard without changing the listen path", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "router-host-listen-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  const router = createAgentRouter({ repository, host: "0.0.0.0", port: 0, timeoutSweepIntervalMs: 60_000 });
  t.after(() => router.close());

  const websocketUrl = await router.listen();

  assert.match(websocketUrl, /^ws:\/\/0\.0\.0\.0:\d+\/v1\/agents$/);
});
