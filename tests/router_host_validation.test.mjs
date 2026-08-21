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
const tailnetHost = "donuts-cloud.tailcdfd89.ts.net";
const tailnetAuthority = `${tailnetHost}:8765`;
const validNumericAuthorities = [
  "100.110.123.108:8765",
  "192.168.0.175",
  "203.0.113.9:8080",
  "[2001:db8::1]",
  "[2001:db8::2]:8765",
];
const invalidBindHosts = ["192.168.0.175", "localhost", "0.0.0.1", tailnetHost, "100.110.123.108"];
const hostileAuthorities = [
  `attacker@${tailnetHost}`,
  `${tailnetHost}/path`,
  `${tailnetHost}?query=1`,
  `${tailnetHost}#fragment`,
  `${tailnetHost}\\path`,
  `${tailnetHost} extra`,
  `${tailnetHost}:bad-port`,
  `${tailnetHost}%2fpath`,
  `attacker@${validNumericAuthorities[0]}`,
  `${validNumericAuthorities[0]}/path`,
  `${validNumericAuthorities[0]}?query=1`,
  `${validNumericAuthorities[0]}#fragment`,
  `${validNumericAuthorities[0]}\\path`,
  `${validNumericAuthorities[0]} extra`,
  `${validNumericAuthorities[0]}%2fpath`,
];
const malformedNumericAuthorities = [
  "256.1.1.1",
  "1.2.3",
  "1.2.3.4.5",
  "0x7f000001",
  "2130706433",
  "01.2.3.4",
  "2001:db8::1",
  "[2001:db8:::1]",
  "[2001:db8::1",
  "2001:db8::1]",
  "[2001:db8::1]extra",
  "[2001:db8::1]:bad-port",
  "[2001:db8::1]:65536",
  "[fe80::1%25eth0]",
  "[127.0.0.1]",
  "100.110.123.108:",
  "100.110.123.108:65536",
];
const rejectedRequestAuthorities = [
  ...malformedNumericAuthorities,
  "localhost:8765",
  "example.com:8765",
  ...hostileAuthorities,
];

function cliOptions(host) {
  return parseArguments(["--database", "/tmp/router.sqlite", "--host", host]);
}

function requestWithHeaders(url, headers) {
  return new Promise((resolve, reject) => {
    const request = http.request(url, { headers }, (response) => {
      response.resume();
      response.once("end", () => resolve(response.statusCode));
    });
    request.once("error", reject);
    request.end();
  });
}

function requestWithHost(url, host) {
  return requestWithHeaders(url, { Host: host });
}

test("router CLI accepts loopback and LAN wildcard hosts", () => {
  for (const host of validHosts) {
    assert.equal(cliOptions(host).host, host);
  }
});

test("router CLI rejects hosts outside the explicit allowlist", () => {
  for (const host of invalidBindHosts) {
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

  for (const host of invalidBindHosts) {
    assert.throws(
      () => createAgentRouter({ repository, host }),
      /host must be 127\.0\.0\.1, ::1, or 0\.0\.0\.0/,
    );
  }
});

test("router HTTP accepts valid numeric authorities and exact hostnames", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "router-host-request-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  t.after(() => router.close());
  const websocketUrl = await router.listen();
  const httpUrl = websocketUrl.replace("ws://", "http://").replace("/v1/agents", "/v1/health");

  for (const host of ["127.0.0.1", tailnetHost, tailnetAuthority, ...validNumericAuthorities]) {
    assert.equal(await requestWithHost(httpUrl, host), 200, `expected ${host} to be accepted`);
  }

  for (const authority of rejectedRequestAuthorities) {
    assert.equal(await requestWithHost(httpUrl, authority), 403, `expected ${authority} to be rejected`);
  }

  const corsResponse = await fetch(httpUrl, {
    headers: { host: validNumericAuthorities[0], origin: "https://trusted.example" },
  });
  assert.equal(corsResponse.status, 200);
  assert.equal(corsResponse.headers.get("access-control-allow-origin"), "*");

  const corsPreflight = await fetch(httpUrl.replace("/v1/health", "/v1/messages"), {
    method: "OPTIONS",
    headers: {
      Host: validNumericAuthorities[0],
      Origin: "https://trusted.example",
      "Access-Control-Request-Method": "POST",
      "Access-Control-Request-Headers": "content-type",
    },
  });
  assert.equal(corsPreflight.status, 204);
  assert.equal(corsPreflight.headers.get("access-control-allow-origin"), "*");

  const healthPreflight = await fetch(httpUrl, {
    method: "OPTIONS",
    headers: {
      Host: validNumericAuthorities[0],
      Origin: "https://trusted.example",
      "Access-Control-Request-Method": "GET",
    },
  });
  assert.equal(healthPreflight.status, 204);
  assert.equal(healthPreflight.headers.get("access-control-allow-origin"), "*");

  const corsMessage = await fetch(httpUrl.replace("/v1/health", "/v1/messages"), {
    method: "POST",
    headers: {
      Host: validNumericAuthorities[0],
      Origin: "https://trusted.example",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      id: "task2188-cors-message",
      from: "task2188-cors-web",
      to: "task2188-cors-peer",
      body: "hello",
      created_at: "2026-08-15T00:00:00.000Z",
    }),
  });
  assert.equal(corsMessage.status, 201);
  assert.equal(corsMessage.headers.get("access-control-allow-origin"), "*");

  for (const host of [tailnetHost, validNumericAuthorities[0], validNumericAuthorities[3]]) {
    assert.equal(
      await requestWithHeaders(httpUrl.replace("/v1/health", "/v1/agents"), {
        Host: host,
        Origin: "https://untrusted.example",
      }),
      403,
      `expected Origin on ${host} to be rejected`,
    );
  }
});

test("router WebSocket upgrades accept valid numeric authorities and reject the numeric matrix", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "router-host-websocket-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  t.after(() => repository.close());
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  t.after(() => router.close());
  const websocketUrl = await router.listen();

  for (const host of [tailnetHost, tailnetAuthority, ...validNumericAuthorities]) {
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

  for (const authority of rejectedRequestAuthorities) {
    assert.equal(await rejected({ headers: { host: authority } }), 403, `expected ${authority} to be rejected`);
  }
  for (const host of [tailnetHost, validNumericAuthorities[0], validNumericAuthorities[3]]) {
    assert.equal(await rejected({ headers: { host }, origin: "https://untrusted.example" }), 403, `expected Origin on ${host} to be rejected`);
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
