import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { join } from "node:path";
import { workflowHarness, model, lastToolOutput, type Reply } from "./workflow-implicit-goals-harness";

// RNT-006: external provider only is mocked. Browser/WS, proxy, SDK and SQLite are real.
test("removes worker tool and preserves mailbox and admin lifecycle", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await workflowHarness();
  let stderr = ""; f.router.process.stderr?.on("data", b => { stderr += String(b); });
  const caller = "worker-tool-caller", child = "admin-spawned-child";
  try {
    await f.api("/v1/managed-agents", { name: caller, cwd: join(f.router.root, "work"), runtime: "embedded", model });
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", caller)[0]?.lifecycle_state).toBe("running");
    await f.open(page);
    const row = page.locator(`.agent-row[data-agent-id="${caller}"]`);
    await expect(row).toBeVisible();
    await row.locator(".agent-row-select").click();
    async function turn(reply: Reply, final: string, childTurn = false, stale = false) {
      const start = f.requests.length;
      const baseline = Number(f.rows("SELECT COALESCE(MAX(seq), -1) AS seq FROM embedded_entries WHERE agent_id=?", caller)[0].seq);
      f.replies.push(reply);
      if (!stale) f.replies.push({ answer: final });
      if (childTurn) f.replies.push({ answer: final });
      const initial = await f.router.advanceClock(1);
      const clock = await f.router.advanceClock(Math.max(1, Date.now() + 2000 - Date.parse(initial.now)));
      await f.api("/v1/messages", { id: randomUUID(), from: "user", to: caller, body: "Execute the fixture call", created_at: new Date(clock.now).toISOString(), source: { kind: "web_ui" } });
      await expect.poll(() => f.requests.length).toBe(start + (stale ? 1 : childTurn ? 3 : 2));
      if (stale) {
        // Codex rejects undeclared tool names before SDK dispatch, clears the
        // unresolved call and saves an assistant error, not a tool result.
        const entries = () => f.rows("SELECT seq, kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY seq", caller)
          .filter(entry => Number(entry.seq) > baseline);
        await expect.poll(() => entries().filter(entry => entry.kind === "assistant")
          .map(entry => JSON.parse(String(entry.body)))).toContainEqual(expect.objectContaining({
            role: "assistant", stopReason: "error",
            errorMessage: "Codex stream failed; turn outcome unknown", content: [],
          }));
        expect(entries().filter(entry => entry.kind === "toolResult")).toEqual([]);
      } else {
        // Mailbox turns project durable sent mail, not necessarily the model's
        // final text. Verify that final at the SDK persistence boundary.
        await expect.poll(() => f.rows("SELECT seq, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant' ORDER BY seq", caller)
          .filter(entry => Number(entry.seq) > baseline)
          .map(entry => JSON.parse(String(entry.body)).content)).toContainEqual([expect.objectContaining({ type: "text", text: final })]);
      }
      return f.requests[start];
    }
    const operationsBefore = f.rows("SELECT request_id FROM managed_http_operations WHERE ? IS NOT NULL ORDER BY request_id", caller);
    const stale = { action: "launch", name: "forbidden-child", cwd: join(f.router.root, "work"), instruction: "must not start" };
    const request = await turn({ tool: "mecha_worker", args: stale }, "Rejected stale launch", false, true);
    const definitions = request.tools as { name: string; parameters: { properties: Record<string, { enum?: string[] }> } }[];
    expect(definitions.some(t => t.name === "mecha_worker")).toBe(false);
    expect(definitions.some(t => t.name === "worker_spawn")).toBe(false);
    for (const action of ["list", "send", "cleanup"]) {
      await turn({ tool: "mecha_worker", args: { action, name: "forbidden-child", body: "must not send" } }, `Rejected stale ${action}`, false, true);
    }
    expect(f.rows("SELECT body FROM messages WHERE sender=?", caller)).toEqual([]);
    expect(f.rows("SELECT agent_id FROM managed_agents WHERE agent_id=?", "forbidden-child")).toEqual([]);
    expect(f.rows("SELECT effect_id FROM embedded_effects WHERE agent_id=?", caller)).toEqual([]);
    expect(f.rows("SELECT request_id FROM managed_http_operations WHERE ? IS NOT NULL ORDER BY request_id", caller)).toEqual(operationsBefore);
    const mailboxRequest = await turn({ tool: "mecha_mailbox_send", args: { recipient: "user", body: "Preserved durable mailbox" } }, "Mailbox preserved");
    const history = mailboxRequest.input as { type: string; output?: string }[];
    expect(history.filter(i => i.type === "function_call_output")).toHaveLength(0);
    expect(lastToolOutput(f)).toContain("accepted");
    expect(f.rows("SELECT body FROM messages WHERE sender=?", caller).some(r => r.body === "Preserved durable mailbox")).toBe(true);
    await expect(page.getByText("Preserved durable mailbox", { exact: true })).toBeVisible();

    // Enable only the existing admin capability through its real HTTP permission API.
    await f.api(`/v1/managed-agents/${caller}/tools`, { tool_name: "worker_spawn", enabled: true }, "PUT");
    const spawnRequest = await turn({ tool: "worker_spawn", args: { name: child, cwd: join(f.router.root, "work"), instruction: "Run deterministic child turn", model } }, "Admin spawn preserved", true);
    expect((spawnRequest.tools as { name: string }[]).some(t => t.name === "worker_spawn")).toBe(true);
    const outputs = f.requests.slice(-2).flatMap(r => Array.isArray(r.input) ? r.input : []).filter(i => i.type === "function_call_output");
    expect(outputs.some(i => { try { return JSON.parse(i.output).worker_id === child; } catch { return false; } })).toBe(true);
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", child)[0]?.lifecycle_state).toBe("running");
    await f.api(`/v1/managed-agents/${caller}/tools`, { tool_name: "worker_terminate", enabled: true }, "PUT");
    await turn({ tool: "worker_terminate", args: { worker_id: child } }, "Admin terminate preserved");
    expect(lastToolOutput(f)).toContain("accepted");
    // Termination cleanup removes the managed row; do not race a transient state.
    await expect.poll(() => f.rows("SELECT agent_id FROM managed_agents WHERE agent_id=?", child)).toEqual([]);
    expect(f.wsFrames.length).toBeGreaterThan(0);
    expect(f.violations).toEqual([]);
  } catch (err) { console.error("ROUTER STDERR", stderr); console.error("ROUTER LOG", await (await import("node:fs/promises")).readFile(f.router.logPath, "utf8")); throw err; } finally { await f.close(); }
});

test("lists managed workers with status filter and metadata", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await workflowHarness();
  let stderr = ""; f.router.process.stderr?.on("data", b => { stderr += String(b); });
  const caller = "worker-list-caller";
  try {
    await f.api("/v1/managed-agents", { name: caller, cwd: join(f.router.root, "work"), runtime: "embedded", model });
    await expect.poll(() => f.rows("SELECT lifecycle_state FROM managed_agents WHERE agent_id=?", caller)[0]?.lifecycle_state).toBe("running");
    await f.open(page);
    const row = page.locator(`.agent-row[data-agent-id="${caller}"]`);
    await expect(row).toBeVisible();
    await row.locator(".agent-row-select").click();

    async function turn(reply: Reply, final: string) {
      const start = f.requests.length;
      const baseline = Number(f.rows("SELECT COALESCE(MAX(seq), -1) AS seq FROM embedded_entries WHERE agent_id=?", caller)[0].seq);
      f.replies.push(reply, { answer: final });
      const initial = await f.router.advanceClock(1);
      const clock = await f.router.advanceClock(Math.max(1, Date.now() + 2000 - Date.parse(initial.now)));
      await f.api("/v1/messages", { id: randomUUID(), from: "user", to: caller, body: "Execute the fixture call", created_at: new Date(clock.now).toISOString(), source: { kind: "web_ui" } });
      await expect.poll(() => f.requests.length).toBe(start + 2);
      await expect.poll(() => f.rows("SELECT seq, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant' ORDER BY seq", caller)
        .filter(entry => Number(entry.seq) > baseline)
        .map(entry => JSON.parse(String(entry.body)).content)).toContainEqual([expect.objectContaining({ type: "text", text: final })]);
      return f.requests[start];
    }

    // Step 1: disabled rejection
    const disabledRequest = await turn({ tool: "worker_list", args: { status: "all" } }, "Rejected disabled worker_list");
    const disabledDefs = disabledRequest.tools as { name: string }[];
    expect(disabledDefs.some(t => t.name === "worker_list")).toBe(false);
    expect(lastToolOutput(f)).toMatch(/unavailable|rejected|not found/i);

    // Step 2: Enable worker_list via PUT /v1/managed-agents/${caller}/tools
    await f.api(`/v1/managed-agents/${caller}/tools`, { tool_name: "worker_list", enabled: true }, "PUT");

    // Step 3: Call turn({ tool: "worker_list", args: { status: "all" } }, "Listed workers all")
    const listAllRequest = await turn({ tool: "worker_list", args: { status: "all" } }, "Listed workers all");
    expect((listAllRequest.tools as { name: string }[]).some(t => t.name === "worker_list")).toBe(true);

    const listAllOutput = JSON.parse(lastToolOutput(f));
    expect(listAllOutput.status).toBe("ok");
    expect(listAllOutput.filter).toBe("all");
    expect(listAllOutput.total).toBeGreaterThanOrEqual(1);
    const callerEntry = listAllOutput.workers.find((w: { agent_id: string }) => w.agent_id === caller);
    expect(callerEntry).toBeDefined();
    expect(callerEntry.agent_id).toBe(caller);
    expect(callerEntry.runtime).toBe("embedded");
    expect(callerEntry.updated_at).toBeDefined();
    expect(callerEntry.lifecycle_state).toBe("running");
    expect(callerEntry.cwd).toBe(join(f.router.root, "work"));
    expect(callerEntry.model).toEqual({ provider: model.provider, id: model.id });
    expect(callerEntry.uptime_seconds).toBeGreaterThanOrEqual(0);

    // Step 3b: Call turn with omitted status default ({})
    await turn({ tool: "worker_list", args: {} }, "Listed workers default omitted");
    const listOmittedOutput = JSON.parse(lastToolOutput(f));
    expect(listOmittedOutput.status).toBe("ok");
    expect(listOmittedOutput.filter).toBe("all");
    expect(listOmittedOutput.workers.some((w: { agent_id: string }) => w.agent_id === caller)).toBe(true);

    // Step 4: Call turn({ tool: "worker_list", args: { status: "running" } }, "Listed workers running")
    await turn({ tool: "worker_list", args: { status: "running" } }, "Listed workers running");
    const listRunningOutput = JSON.parse(lastToolOutput(f));
    expect(listRunningOutput.status).toBe("ok");
    expect(listRunningOutput.filter).toBe("running");
    expect(listRunningOutput.workers.some((w: { agent_id: string }) => w.agent_id === caller)).toBe(true);

    // Step 4b: Call turn({ tool: "worker_list", args: { status: "active" } }, "Listed workers active")
    await turn({ tool: "worker_list", args: { status: "active" } }, "Listed workers active");
    const listActiveOutput = JSON.parse(lastToolOutput(f));
    expect(listActiveOutput.status).toBe("ok");
    expect(listActiveOutput.filter).toBe("active");
    expect(listActiveOutput.workers.some((w: { agent_id: string }) => w.agent_id === caller)).toBe(true);

    // Step 5: Call turn({ tool: "worker_list", args: { status: "stopped" } }, "Listed workers stopped")
    await turn({ tool: "worker_list", args: { status: "stopped" } }, "Listed workers stopped");
    const listStoppedOutput = JSON.parse(lastToolOutput(f));
    expect(listStoppedOutput.status).toBe("ok");
    expect(listStoppedOutput.filter).toBe("stopped");

    // Step 6: Call turn with invalid status rejection ({ status: "invalid_status" })
    await turn({ tool: "worker_list", args: { status: "invalid_status" } }, "Rejected invalid status");
    expect(lastToolOutput(f)).toMatch(/invalid arguments for worker_list|invalid status/i);

    expect(f.wsFrames.length).toBeGreaterThan(0);
    expect(f.violations).toEqual([]);
  } catch (err) {
    console.error("ROUTER STDERR", stderr);
    console.error("ROUTER LOG", await (await import("node:fs/promises")).readFile(f.router.logPath, "utf8"));
    throw err;
  } finally {
    await f.close();
  }
});

