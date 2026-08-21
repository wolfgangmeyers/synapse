import assert from "node:assert/strict";
import http from "node:http";
import WebSocket from "ws";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { parseArguments } from "../tools/run_router.mjs";

const validHosts = ["127.0.0.1", "::1", "0.0.0.0"];
const tailnetHost = "wolfgang-roger.tailb36da1.ts.net";
const arbitraryAuthorities = [
  tailnetHost,
  `${tailnetHost}:8765`,
  "100.64.0.1",
  "100.64.0.1:8765",
  "anything.example:8765",
  `attacker@${tailnetHost}`,
  `${tailnetHost}/path`,
  `${tailnetHost}?query=1`,
  `${tailnetHost}#fragment`,
  `${tailnetHost}\\path`,
  `${tailnetHost} extra`,
  `${tailnetHost}:bad-port`,
  `${tailnetHost}%2fpath`,
];
const invalidHosts = ["192.168.0.175", "localhost", "0.0.0.1", tailnetHost];

function cliOptions(host) {
  return parseArguments(["--database", "/tmp/router.sqlite", "--host", host]);
}

function requestWithHost(url, host) {
  return new Promise((resolve, reject) => {
    const request = http.request(url, { headers: { Host: host } }, (response) => {
      response.resume();
      response.once("end", () => resolve(response.statusCode));
    });
    request.once("error", reject);
    request.end();
  });
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

test("router HTTP requests accept arbitrary Host authorities while rejecting browser origins", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "router-host-request-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  t.after(() => router.close());
  const websocketUrl = await router.listen();
  const httpUrl = websocketUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/health");

  for (const host of arbitraryAuthorities) {
    assert.equal(await requestWithHost(httpUrl, host), 200, `expected ${host} to be accepted`);
  }

  const originRejected = await fetch(httpUrl.replace("/v1/health", "/v1/agents"), {
    headers: { host: arbitraryAuthorities.at(-1), origin: "https://untrusted.example" },
  });
  assert.equal(originRejected.status, 403);
});

test("router WebSocket upgrades accept arbitrary Host authorities while rejecting browser origins", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "router-host-websocket-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  t.after(() => router.close());
  const websocketUrl = await router.listen();

  for (const host of arbitraryAuthorities) {
    const accepted = new WebSocket(websocketUrl, { headers: { host } });
    t.after(() => accepted.close());
    await new Promise((resolve, reject) => {
      accepted.once("open", resolve);
      accepted.once("error", reject);
    });
  }

  const rejected = (options) => new Promise((resolve, reject) => {
    const socket = new WebSocket(websocketUrl, options);
    socket.once("unexpected-response", (_request, response) => resolve(response.statusCode));
    socket.once("open", () => reject(new Error("rejected WebSocket unexpectedly opened")));
    socket.once("error", () => {});
  });

  assert.equal(await rejected({
    headers: { host: arbitraryAuthorities.at(-1) },
    origin: "https://untrusted.example",
  }), 403);
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
