import { test, expect } from "@playwright/test";
import { closeAfterWorkflowStartupFailure } from "./workflow-implicit-goals-harness";

test("startup failure survives successful cleanup", async () => {
  const startup = new Error("startup failed");
  let closed = false;
  await expect(closeAfterWorkflowStartupFailure(startup, async () => { closed = true; })).rejects.toBe(startup);
  expect(closed).toBe(true);
});

test("startup and cleanup failures are both reported", async () => {
  const startup = new Error("startup failed");
  const cleanup = new Error("cleanup failed");
  let result: unknown;
  try { await closeAfterWorkflowStartupFailure(startup, async () => { throw cleanup; }); }
  catch (error) { result = error; }
  expect(result).toBeInstanceOf(AggregateError);
  if (!(result instanceof AggregateError)) throw new Error("expected AggregateError");
  expect(result.cause).toBe(startup);
  expect(result.errors).toEqual([startup, cleanup]);
});

test("workflow startup closes acquired resources and preserves the boundary error", async () => {
  const { workflowHarness } = await import("./workflow-implicit-goals-harness");
  const { Server } = await import("node:http");
  const { execFileSync } = await import("node:child_process");
  const { existsSync } = await import("node:fs");
  const { dirname } = await import("node:path");
  const startup = new Error("controlled default-model transport failure");
  const originalFetch = globalThis.fetch;
  let root = "";
  let proxyPort = "";
  let servers: InstanceType<typeof Server>[] = [];
  let pids: number[] = [];
  globalThis.fetch = async (input, init) => {
    const url = String(input);
    if (url.endsWith("/v1/settings/default-model")) {
      proxyPort = new URL(url).port;
      // Capture the actual listening fixture servers and processes while startup owns them.
      const handles: unknown[] = Reflect.apply(Reflect.get(process, "_getActiveHandles"), process, []);
      servers = handles.filter((handle): handle is InstanceType<typeof Server> => handle instanceof Server && handle.listening);
      const rows = execFileSync("ps", ["-eo", "pid=,args="], { encoding: "utf8" }).split("\n");
      pids = rows.filter(row => row.includes(`${root}/mailbox-router`) || (row.includes("/router-proxy") && row.includes(`--listen-port ${proxyPort}`)))
        .map(row => Number(row.trim().split(/\s+/)[0]));
      throw startup;
    }
    return originalFetch(input, init);
  };
  try {
    await expect(workflowHarness({ kbDir: home => { root = dirname(home); return `${home}/kb`; } })).rejects.toBe(startup);
    expect(root).not.toBe("");
    expect(proxyPort).not.toBe("");
    expect(servers.length).toBeGreaterThanOrEqual(2); // provider and frontend
    expect(pids).toHaveLength(2); // router and proxy
    expect(existsSync(root)).toBe(false);
    for (const server of servers) expect(server.listening).toBe(false);
    for (const pid of pids) expect(() => process.kill(pid, 0)).toThrow();
    await expect(originalFetch(`http://127.0.0.1:${proxyPort}/v1/health`)).rejects.toThrow();
  } finally {
    globalThis.fetch = originalFetch;
  }
});
