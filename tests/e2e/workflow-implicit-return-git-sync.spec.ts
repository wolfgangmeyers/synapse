import { test, expect } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createRunningWorkflow, userTurn, workflowHarness } from "./workflow-implicit-goals-harness";

test("IR-011: git sync retains definition flags, not runtime predecessor or return edge", async ({ page }) => {
  test.setTimeout(120_000);
  const root = await mkdtemp(join(tmpdir(), "workflow-git-e2e-"));
  const git = (...args: string[]) => execFileSync("git", args, { encoding: "utf8" });
  let f: Awaited<ReturnType<typeof workflowHarness>> | undefined;
  const server = createServer((req, res) => {
    const url = new URL(req.url!, "http://127.0.0.1");
    const child = spawn("git", ["http-backend"], { env: { ...process.env, GIT_PROJECT_ROOT: root, GIT_HTTP_EXPORT_ALL: "1", PATH_INFO: url.pathname, QUERY_STRING: url.search.slice(1), REQUEST_METHOD: req.method, CONTENT_TYPE: req.headers["content-type"] ?? "", CONTENT_LENGTH: req.headers["content-length"] ?? "" } });
    req.pipe(child.stdin);
    let headers = Buffer.alloc(0);
    let started = false;
    child.stdout.on("data", (chunk: Buffer) => {
      if (started) { res.write(chunk); return; }
      headers = Buffer.concat([headers, chunk]);
      const end = headers.indexOf("\r\n\r\n");
      if (end < 0) return;
      for (const line of headers.subarray(0, end).toString().split("\r\n")) {
        const colon = line.indexOf(":");
        if (colon < 0) continue;
        const name = line.slice(0, colon), value = line.slice(colon + 1).trim();
        if (name.toLowerCase() === "status") res.statusCode = Number.parseInt(value, 10);
        else res.setHeader(name, value);
      }
      started = true;
      res.write(headers.subarray(end + 4));
    });
    child.on("close", code => { if (!started) res.statusCode = 500; res.end(); });
  });
  try {
    git("init", "--bare", "--initial-branch=main", join(root, "remote.git"));
    git("--git-dir", join(root, "remote.git"), "config", "http.receivepack", "true");
    await new Promise<void>(resolve => server.listen(0, "127.0.0.1", resolve));
    const address = server.address();
    if (!address || typeof address === "string") throw Error("missing git server address");
    f = await workflowHarness();
    await writeFile(join(f.router.root, "managed-home", ".gitconfig"), '[user]\n name = E2E Test\n email = e2e@example.invalid\n');
    f.replies.push({ tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 } }, { answer: "Ready" });
    const { agent, workflow, a, b } = await createRunningWorkflow(f, page, 600000, false);
    await expect(page.getByText("Ready", { exact: true }).first()).toBeVisible();
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", allow_exit: true, allow_implicit_return: true }, "PUT");
    f.replies.push({ tool: "mecha_workflow_transition", args: { action: "transition", target_state: b.id } },
      { tool: "workflow_snooze", args: { justification: "Waiting for CI process", duration_minutes: 5 } }, { answer: "At B" });
    await userTurn(page, "Move to B");
    await expect(page.getByText("At B", { exact: true }).first()).toBeVisible();
    expect(f.rows("SELECT active_workflow_state_id, workflow_predecessor_state_id FROM managed_agents WHERE agent_id=?", agent)[0]).toMatchObject({ active_workflow_state_id: b.id, workflow_predecessor_state_id: a.id });
    const browserApi = async (path: string, method: string, body?: object) => page.evaluate(async ({ url, path, method, body }) => {
      const response = await fetch(url + path, { method, credentials: "omit", headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined });
      const text = await response.text();
      if (!response.ok) throw Error(`${response.status}: ${text}`);
      return JSON.parse(text);
    }, { url: f!.proxy.url, path, method, body });
    await browserApi("/v1/git-sync/workflows", "PUT", { repo_url: `http://127.0.0.1:${address.port}/remote.git` });
    expect(await browserApi("/v1/git-sync/workflows/push", "POST")).toMatchObject({ success: true });
    const raw = git("--git-dir", join(root, "remote.git"), "show", "main:workflows.json");
    const payload = JSON.parse(raw);
    const bundle = payload.workflows.find((item: { workflow: { name: string } }) => item.workflow.name === workflow.name);
    expect(bundle).toBeDefined();
    expect(raw).not.toMatch(/predecessor/i);
    const exportedA = bundle.states.find((s: { name: string }) => s.name === "A");
    const exportedB = bundle.states.find((s: { name: string }) => s.name === "B");
    expect(exportedA.allow_implicit_return).toBe(false);
    expect(exportedB.allow_implicit_return).toBe(true);
    expect(exportedA.transitions).toEqual([{ target_state_key: exportedB.key, description: "Advance" }]);
    expect(exportedB.transitions ?? []).toEqual([]);
    await f.api(`/v1/workflow-states/${b.id}`, { name: "B", allow_implicit_return: false }, "PUT");
    expect(await browserApi("/v1/git-sync/workflows/pull", "POST")).toMatchObject({ exists: true, count: 1 });
    const list = await f.api("/v1/workflows");
    const restored = list.find((w: { name: string }) => w.name === workflow.name);
    const detail = await f.api(`/v1/workflows/${restored.id}`);
    const restoredA = detail.states.find((s: { name: string }) => s.name === "A");
    const restoredB = detail.states.find((s: { name: string }) => s.name === "B");
    expect(restoredA.allow_implicit_return).toBe(false);
    expect(restoredB.allow_implicit_return).toBe(true);
    expect(restoredA.transitions).toEqual([{ target_state_id: restoredB.id, description: "Advance" }]);
    expect(restoredB.transitions ?? []).toEqual([]);
    expect(f.violations).toEqual([]);
    expect(f.replies).toEqual([]);
  } finally {
    await f?.close();
    await new Promise<void>(resolve => server.close(() => resolve()));
    await rm(root, { recursive: true, force: true });
  }
});
