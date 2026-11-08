import { isDeepStrictEqual } from "node:util";
import { readFileSync } from "node:fs";
import { writeFile, rm } from "node:fs/promises";
import { test, expect } from "@playwright/test";
import { cleanupBarrier } from "./claude-code-cleanup-barrier";
import { claudeWorkersHarness, exists, type ClaudeWorkersHarness } from "./claude-workers-harness";
import { settled } from "./workflow-implicit-goals-harness";

// SPEC-LIFECYCLE-001 CC-003/006/009/012/015. Only the external CLI/evaluator is fake:
// browser -> proxy/WS -> real router -> CLI HTTP MCP -> checked SQLite.
// Authored acceptance coverage, not evidence of an executed/passing suite.
const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input } });
const text = (value: string) => ({ text: value });

// A killed orphan can remain a zombie until its adopter reaps it. kill(pid, 0)
// alone counts that non-executable process (or a reused PID) as a live leak.
// Capture both fixture identities before release; never signal a replacement.
function processSnapshot(pid: number): { identity: string; state: string } | undefined {
  if (process.platform !== "linux") return undefined;
  try {
    const raw = readFileSync(`/proc/${pid}/stat`, "utf8");
    const fields = raw.slice(raw.lastIndexOf(")") + 2).trim().split(/\s+/);
    if (!/^\d+$/.test(fields[19] ?? "") || !/^[A-Za-z]$/.test(fields[0] ?? "")) {
      throw Error("invalid fixture process stat");
    }
    return { identity: `${pid}:${fields[19]}`, state: fields[0] };
  } catch (error) { if (error.code === "ENOENT" || error.code === "ESRCH") return undefined; throw error; }
}
function identity(pid: number): string | undefined {
  return processSnapshot(pid)?.identity;
}
function alive(pid: number, captured?: string) {
  if (process.platform === "linux") {
    if (!captured) throw Error("missing captured fixture process identity");
    const current = processSnapshot(pid);
    return current !== undefined && current.identity === captured && !["Z", "X", "x"].includes(current.state);
  }
  try { process.kill(pid, 0); return true; }
  catch (error) { if (error.code === "ESRCH") return false; throw error; }
}


// Select the actual canonical assistant producer, never derive an ID from the
// fixture's raw CLI ID or manufacture an effect witness. Join the result on the
// complete binding and require a later checked sequence and identical tool name.
function checkedTool(f: ClaudeWorkersHarness, agent: string, name: string, args: object, decodeJSON = true) {
  const rows = f.rows(`SELECT e.seq AS producer_seq, e.digest AS producer_digest,
      json_extract(part.value, '$.id') AS call_id,
      json_extract(part.value, '$.arguments') AS arguments_json,
      r.seq AS result_seq, r.digest AS result_digest, CAST(r.body AS TEXT) AS result_json
    FROM embedded_entries e
    JOIN embedded_sessions s ON s.agent_id=e.agent_id AND s.session_id=e.session_id
      AND s.epoch=e.epoch AND s.generation=e.generation AND s.current=1
    JOIN json_each(e.body, '$.content') part
    JOIN embedded_entries r ON r.agent_id=e.agent_id AND r.session_id=e.session_id
      AND r.epoch=e.epoch AND r.generation=e.generation AND r.seq>e.seq
      AND r.kind IN ('toolResult','tool_result')
      AND json_extract(r.body, '$.toolCallId')=json_extract(part.value, '$.id')
      AND json_extract(r.body, '$.toolName')=json_extract(part.value, '$.name')
    WHERE e.agent_id=? AND e.kind='assistant'
      AND json_extract(part.value, '$.type')='toolCall'`, agent)
    .filter(row => JSON.parse(String(row.result_json)).toolName === name &&
      isDeepStrictEqual(JSON.parse(String(row.arguments_json)), args));
  expect(rows).toHaveLength(1);
  const row = rows[0];
  expect(row.call_id).toMatch(/^claude-[0-9a-f]{64}$/);
  expect(row.producer_digest).toEqual(expect.any(String));
  expect(row.result_digest).toEqual(expect.any(String));
  expect(Number(row.result_seq)).toBeGreaterThan(Number(row.producer_seq));
  expect(JSON.parse(String(row.arguments_json))).toEqual(args);
  const result = JSON.parse(String(row.result_json));
  expect(result.toolCallId).toBe(row.call_id);
  expect(result.isError ?? false).toBe(false);
  expect(result.content).toHaveLength(1);
  expect(result.content[0].type).toBe("text");
  return { id: String(row.call_id), result, payload: decodeJSON ? JSON.parse(result.content[0].text) : result.content[0].text };
}

function observedEffect(f: ClaudeWorkersHarness, agent: string, checked: ReturnType<typeof checkedTool>) {
  // SQLite stores the checked result as a BLOB; cast at the boundary so the
  // Node SQLite driver returns UTF-8 JSON rather than a Uint8Array.
  const effect = f.rows("SELECT effect_id, state, CAST(result AS TEXT) AS result FROM embedded_effects WHERE agent_id=?", agent)
    .find(row => row.effect_id === checked.id);
  expect(effect?.state).toBe("observed");
  expect(JSON.parse(String(effect?.result))).toEqual(checked.result);
}

test.describe("Claude successful router tools", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  test.afterEach(async () => { try { await f?.close(); } finally { f = undefined; } });

  test("MCP discovery follows live worker tool policy", async ({ page }) => {
    f = await claudeWorkersHarness({ executions_by_input: [
      { contains: "Inspect policy", turns: [[{ barrier: "policy-discovery" }, text("Policy ready")]] },
    ] });
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page, "Inspect policy");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "policy-discovery")).toBe(true);
    const receipt = (await f.records()).find(row => row.kind === "barrier" && row.name === "policy-discovery")!;
    const grant = await f.grant();
    const list = async () => {
      const response = await fetch(grant.url, { method: "POST", headers: { ...grant.headers, "Content-Type": "application/json" },
        body: JSON.stringify({ jsonrpc: "2.0", id: "list", method: "tools/list" }), signal: AbortSignal.timeout(6500) });
      expect(response.status).toBe(200);
      const data = await response.json();
      expect(data.error).toBeUndefined();
      return data.result.tools.map((tool: { name: string }) => tool.name);
    };
    expect(await list()).toContain("mecha_agent_urgency");
    expect((await f.request(page, `/v1/managed-agents/${agent}/tools`, { tool_name: "mecha_agent_urgency", enabled: false }, "PUT")).status).toBe(200);
    expect(await list()).not.toContain("mecha_agent_urgency");
    expect((await f.request(page, `/v1/managed-agents/${agent}/tools`, { tool_name: "mecha_agent_urgency", enabled: true }, "PUT")).status).toBe(200);
    expect(await list()).toContain("mecha_agent_urgency");
    expect(await f.grant()).toEqual(grant);
    await f.releaseBarrier(receipt); await f.final(page, agent, "Policy ready");
  });

  test("cached catalog refreshes between turns without losing session", async ({ page }) => {
    const cached = (id: string, urgent: boolean) => ({ mcp_call: { id, name: "mecha_agent_urgency", input: { urgent }, require_catalog: true } });
    f = await claudeWorkersHarness({ executions_by_input: [
      { contains: "Catalog one", turns: [[call("c1", "scratchpad", { action: "read" }), { barrier: "first-cached" }, text("Catalog first")]] },
      { contains: "Catalog two", turns: [[cached("c2", true), { barrier: "catalog-stale" }, cached("stale", false), text("Catalog second")]] },
      { contains: "Catalog three", turns: [[cached("c3", false), text("Catalog third")]] },
      { contains: "Catalog four", turns: [[call("c4", "scratchpad", { action: "read" }), text("Catalog fourth")]] },
    ] });
    await f.open(page); const agent = await f.launch(page);
    const policy = async (enabled: boolean) => expect((await f!.request(page, `/v1/managed-agents/${agent}/tools`, { tool_name: "mecha_agent_urgency", enabled }, "PUT")).status).toBe(200);
    await policy(false);
    await f.userTurn(page, "Catalog one");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "first-cached")).toBe(true);
    const firstRecords = await f.records();
    const firstPID = Number(firstRecords.find(row => row.kind === "catalog")!.pid); const firstIdentity = identity(firstPID);
    expect(alive(firstPID, firstIdentity)).toBe(true);
    await f.releaseBarrier(firstRecords.find(row => row.kind === "barrier" && row.name === "first-cached")!);
    await f.final(page, agent, "Catalog first");
    await expect.poll(() => alive(firstPID, firstIdentity)).toBe(false);
    await policy(true);
    await f.userTurn(page, "Catalog two");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "catalog-stale")).toBe(true);
    const barrier = (await f.records()).find(row => row.kind === "barrier" && row.name === "catalog-stale")!;
    const oldGrant = await f.grant();
    expect(checkedTool(f, agent, "mecha_agent_urgency", { urgent: true }).payload.urgent).toBe(true);
    await policy(false);
    await f.releaseBarrier(barrier); await f.final(page, agent, "Catalog second");
    const results = () => f!.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).map(row => JSON.parse(String(row.body)));
    expect(results().some(row => row.toolName === "mecha_agent_urgency" && row.isError)).toBe(true);
    expect(f.rows("SELECT urgent FROM agents WHERE agent_id=?", agent)).toEqual([{ urgent: 1 }]);
    await expect.poll(async () => (await fetch(oldGrant.url, { method: "POST", headers: { ...oldGrant.headers, "Content-Type": "application/json" }, body: JSON.stringify({jsonrpc:"2.0",id:1,method:"tools/list"}) })).status).not.toBe(200);
    await policy(true);
    await f.userTurn(page, "Catalog three"); await f.final(page, agent, "Catalog third");
    expect(results().some(row => row.toolName === "mecha_agent_urgency" && !row.isError && JSON.parse(row.content[0].text).urgent === false)).toBe(true);
    expect(f.rows("SELECT urgent FROM agents WHERE agent_id=?", agent)).toEqual([{ urgent: 0 }]);
    await policy(false);
    await f.userTurn(page, "Catalog four"); await f.final(page, agent, "Catalog fourth");
    const records = await f.records();
    expect(records.filter(row => row.kind === "catalog").map(row => (row.names as string[]).includes("mecha_agent_urgency"))).toEqual([false, true, true, false]);
    expect(records.filter(row => row.kind === "cached_call" && row.name === "mecha_agent_urgency")).toHaveLength(3);
    const starts = await f.starts(); expect(starts).toHaveLength(4);
    expect(starts.slice(1).every(row => (row.argv as string[]).includes("--resume"))).toBe(true);
    const sessions = starts.map(row => { const argv = row.argv as string[]; return argv[argv.indexOf(argv.includes("--resume") ? "--resume" : "--session-id") + 1]; });
    expect(new Set(sessions).size).toBe(1);
  });

  test("Claude MCP discovers and checks urgency set and clear", async ({ page }) => {
    f = await claudeWorkersHarness({ executions_by_input: [
      { contains: "Prepare urgency", turns: [[{ barrier: "urgency-discovery" }, text("Urgency ready")]] },
      { contains: "Set urgency", turns: [[call("urgency-set", "mecha_agent_urgency", { urgent: true }), text("Urgency set")]] },
      { contains: "Clear urgency", turns: [[call("urgency-clear", "mecha_agent_urgency", { urgent: false }), text("Urgency cleared")]] },
    ] });
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page, "Prepare urgency");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "urgency-discovery")).toBe(true);
    const receipt = (await f.records()).find(row => row.kind === "barrier" && row.name === "urgency-discovery")!;
    const grant = await f.grant();
    const endpoint = new URL(grant.url);
    expect(endpoint.protocol).toBe("http:");
    expect(["127.0.0.1", "[::1]"]).toContain(endpoint.hostname);
    const response = await fetch(grant.url, { method: "POST", headers: { ...grant.headers, "Content-Type": "application/json" },
      body: JSON.stringify({ jsonrpc: "2.0", id: "discovery", method: "tools/list" }),
      signal: AbortSignal.timeout(6500), redirect: "error" });
    expect(response.status).toBe(200);
    const discovery = await response.json();
    expect(discovery.id).toBe("discovery");
    const names = discovery.result.tools.map((tool: { name: string }) => tool.name);
    expect(names).toContain("mecha_agent_urgency");
    expect(names).not.toContain("mecha_worker");
    await f.releaseBarrier(receipt);
    await f.final(page, agent, "Urgency ready");
    for (const [urgent, prompt, final] of [[true, "Set urgency", "Urgency set"], [false, "Clear urgency", "Urgency cleared"]] as const) {
      await f.userTurn(page, prompt); await f.final(page, agent, final);
      const checked = checkedTool(f, agent, "mecha_agent_urgency", { urgent });
      observedEffect(f, agent, checked);
      expect(checked.payload).toMatchObject({ agent_id: agent, urgent });
      expect(f.rows("SELECT urgent FROM agents WHERE agent_id=?", agent)).toEqual([{ urgent: urgent ? 1 : 0 }]);
    }
    expect(f.violations).toEqual([]);
  });

  test("CC-003/006 mecha_send_image delivers exact pixels with canonical checked result", async ({ page }) => {
    const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==", "base64");
    const args = { data_url: `data:image/png;base64,${png.toString("base64")}`, recipient: "user", message: "Claude MCP pixel delivery" };
    f = await claudeWorkersHarness({ turns: [[call("send-pixel", "mecha_send_image", args), text("Image delivered through MCP")]] });
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page, "Send the deterministic image to me");
    await f.final(page, agent, "Image delivered through MCP");
    const checked = checkedTool(f, agent, "mecha_send_image", args);
    observedEffect(f, agent, checked);
    expect(checked.payload.status).toBe("accepted");
    const messages = f.rows("SELECT id,recipient,body FROM messages WHERE sender=?", agent)
      .filter(row => row.id === checked.payload.message_id);
    expect(messages).toHaveLength(1);
    expect(messages[0].recipient).toBe("user");
    expect(messages[0].body).toContain(args.message);
    const history = f.rows("SELECT message_id,attachments_json FROM conversation_messages WHERE sender=?", agent)
      .filter(row => row.message_id === checked.payload.message_id);
    expect(history).toHaveLength(1);
    const attachments = JSON.parse(String(history[0].attachments_json));
    expect(attachments).toHaveLength(1);
    expect(attachments[0]).toMatchObject({ id: checked.payload.attachment_id, url: checked.payload.url,
      mime_type: "image/png", size_bytes: png.length });
    // Fetch the actual persisted attachment through browser/proxy, not a data
    // URL supplied by the test. Also require a successfully decoded UI image.
    const bytes = await page.evaluate(async url => {
      const response = await fetch(url, { credentials: "omit" });
      if (!response.ok) throw Error(`attachment HTTP ${response.status}`);
      return Array.from(new Uint8Array(await response.arrayBuffer()));
    }, new URL(attachments[0].url, f.proxy.url).href);
    expect(Buffer.from(bytes)).toEqual(png);
    const image = page.getByRole("img", { name: "image.png", exact: true }).first();
    await expect(image).toBeVisible();
    await expect.poll(() => image.evaluate((element: HTMLImageElement) => element.complete && element.naturalWidth > 0)).toBe(true);
    expect(f.violations).toEqual([]);
  });

  test("CC-006 mecha_progress persists and projects a successful MCP milestone", async ({ page }) => {
    const args = { action: "set", percent: 42, note: "Checked Claude milestone" };
    f = await claudeWorkersHarness({ turns: [[call("progress-42", "mecha_progress", args), text("Milestone saved")]] });
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page, "Record measurable progress"); await f.final(page, agent, "Milestone saved");
    const checked = checkedTool(f, agent, "mecha_progress", args);
    expect(checked.payload).toMatchObject({ ok: true, percent: 42, note: args.note });
    expect(f.tableRows("managed_agents", agent)[0]).toMatchObject({ progress_percent: 42, progress_note: args.note,
      progress_updated_at: checked.payload.updated_at });
    expect(f.tableRows("agent_progress_history", agent)).toEqual([expect.objectContaining({ percent: 42, note: args.note })]);
    await expect(page.locator('[role="progressbar"][aria-valuenow="42"]')).toBeVisible();
    expect(f.violations).toEqual([]);
  });

  test("CC-006 mecha_reminder creates one bounded one-shot and consumes its delivery", async ({ page }) => {
    const args = { action: "create", body: "One-shot Claude MCP reminder", delay_seconds: 60 };
    f = await claudeWorkersHarness({ executions_by_input: [
      { contains: "Remind yourself once in one minute", turns: [[call("reminder-once", "mecha_reminder", args), text("One-shot reminder scheduled")]] },
      { contains: args.body, turns: [[text("One-shot reminder consumed")]] },
    ] });
    await f.open(page); const agent = await f.launch(page);
    await f.userTurn(page, "Remind yourself once in one minute");
    await f.final(page, agent, "One-shot reminder scheduled");
    const checked = checkedTool(f, agent, "mecha_reminder", args);
    observedEffect(f, agent, checked);
    const reminders = () => f!.rows("SELECT * FROM reminders WHERE creator=?", agent);
    expect(reminders()).toHaveLength(1);
    const reminder = reminders()[0];
    expect(reminder).toMatchObject({ id: checked.payload.reminder_id, target: agent, body: args.body,
      state: "scheduled", interval_ms: null, linked_message_id: null });
    const calls = f.tableRows("embedded_own_reminder_calls", agent);
    expect(calls).toHaveLength(1);
    expect(calls[0]).toMatchObject({ call_id: checked.id, reminder_id: reminder.id, state: "observed" });
    expect(JSON.parse(String(calls[0].result_json))).toEqual(checked.payload);
    const delay = Date.parse(String(reminder.next_due_at)) - Date.parse(String(reminder.created_at));
    expect(delay).toBe(60_000);
    // The test clock is frozen since creation; the advance endpoint requires a positive delta.
    await f.advance(delay + 1);
    await f.final(page, agent, "One-shot reminder consumed");
    const messages = f.rows("SELECT * FROM messages WHERE recipient=? AND source_kind='reminder'", agent);
    expect(messages).toHaveLength(1);
    expect(messages[0]).toMatchObject({ body: args.body, state: "acknowledged" });
    expect(f.tableRows("embedded_acks", agent).filter(row => row.message_id === messages[0].id)).toHaveLength(1);
    expect(JSON.stringify(await f.inputs())).toContain(args.body);
    // ACK atomically retires a one-shot reminder. The immutable call receipt
    // survives; cleanup is not deferred to a later test or manual operation.
    expect(reminders()).toEqual([]);
    expect(f.tableRows("embedded_own_reminder_calls", agent)).toEqual(calls);
    expect(f.violations).toEqual([]);
  });

  test("CC-006/009/015 mecha_worker_terminate checks its result before stopping and revoking its process", async ({ page }) => {
    const args = { reason: "Checked MCP self-termination", cleanup_worktree: false };
    const cleanup = await cleanupBarrier();
    const forbidden = ["FORBIDDEN after self-termination one", "FORBIDDEN after self-termination two"];
    let cleanupPath: string | undefined;
    try {
      f = await claudeWorkersHarness({ interrupt_output: forbidden, executions_by_input: [
        { contains: "Establish the process before self-termination", turns: [[text("Self-termination binding ready")]] },
        { contains: "Finish by terminating yourself through MCP", turns: [[{ spawn_child: {} }, { barrier: "before-self-termination" }, call("terminate-self", "mecha_worker_terminate", args), { wait: "interrupt" }]] },
      ] });
      await f.open(page); const agent = await f.launch(page);
      await f.userTurn(page, "Establish the process before self-termination");
      await f.final(page, agent, "Self-termination binding ready");
      const binding = f.tableRows("embedded_sessions", agent).find(row => row.current === 1)!;
      expect(binding.role).toBe("worker");
      cleanupPath = f.router.databasePath + ".cleanup-barrier.json";
      await writeFile(cleanupPath, JSON.stringify({ enabled: "1", endpoint: cleanup.endpoint, token: cleanup.token }), { mode: 0o600 });
      await f.userTurn(page, "Finish by terminating yourself through MCP");
      await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "before-self-termination")).toBe(true);
      const fixtureReceipt = (await f.records()).find(row => row.kind === "barrier" && row.name === "before-self-termination")!;
      const grant = await f.grant();
      const child = (await f.records()).find(row => row.kind === "child");
      expect(child).toBeDefined();
      const pids = [Number(child!.parent_pid), Number(child!.pid)];
      const identities = pids.map(pid => {
        expect(Number.isSafeInteger(pid) && pid > 1).toBe(true);
        const captured = identity(pid);
        expect(alive(pid, captured)).toBe(true);
        return { pid, captured };
      });
      expect(await exists(grant.path)).toBe(true);
      expect(f.tableRows("managed_agents", agent)[0].desired_state).toBe("running");
      await f.releaseBarrier(fixtureReceipt);
      // The cleanup barrier holds the real lifecycle after interruption but before
      // cascading checked history; inspect the checked result at that boundary.
      const actionID = await cleanup.arrived;
      cleanup.assertHeld();
      const checked = checkedTool(f, agent, "mecha_worker_terminate", args, false);
      expect(checked.payload).toContain("Worker self-termination scheduled");
      expect(checked.payload).toContain(args.reason);
      expect(checked.payload).toContain("cleanup_worktree: false");
      expect((await f.records()).filter(row => row.kind === "interrupt_output")).toEqual([
        expect.objectContaining({ values: forbidden, pid: pids[0] }),
      ]);
      expect(f.rows("SELECT action_id,kind,state,stage FROM managed_lifecycle_actions WHERE agent_id=?", agent).filter(row => row.action_id === actionID))
        .toEqual([expect.objectContaining({ kind: "terminate", state: "cleanup", stage: "agent_cleanup" })]);
      checkedTool(f, agent, "mecha_worker_terminate", args, false);
      const resultRows = f.rows("SELECT seq,json_extract(body, '$.toolCallId') AS call_id FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).filter(row => row.call_id === checked.id);
      expect(resultRows).toHaveLength(1);
      const afterResult = f.rows("SELECT seq,kind,CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY seq", agent)
        .filter(row => Number(row.seq) > Number(resultRows[0].seq));
      expect(afterResult.filter(row => row.kind === "assistant")).toEqual([]);
      const persisted = JSON.stringify({ entries: f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=?", agent),
        history: f.rows("SELECT * FROM managed_complete_history WHERE agent_id=?", agent),
        conversation: f.rows("SELECT * FROM conversation_messages WHERE sender=?", agent) });
      for (const marker of forbidden) {
        expect(persisted).not.toContain(marker);
        expect(f.wsFrames.join("\n")).not.toContain(marker);
        await expect(page.getByText(marker, { exact: false })).toHaveCount(0);
      }
      await rm(cleanupPath); cleanupPath = undefined;
      cleanup.release();
      // No browser DELETE: the checked MCP request owns this termination.
      await expect.poll(() => f!.tableRows("managed_agent_archives", agent)
        .map(row => row.terminal_outcome)).toEqual(["terminated"]);
      expect(f.tableRows("managed_agents", agent)).toEqual([]);
      expect(f.tableRows("embedded_sessions", agent)).toEqual([]);
      for (const { pid, captured } of identities) {
        await expect.poll(() => alive(pid, captured), { timeout: 5000 }).toBe(false);
      }
      await expect.poll(() => exists(grant.path)).toBe(false);
      expect((await f.mcp(grant, "mecha_context", { action: "status" }, "revoked-after-self-termination")).status).not.toBe(200);
      await expect(page.locator(`[data-agent-id="${agent}"]`)).toHaveCount(0);
      expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM conversation_messages WHERE sender=?", agent)
        .some(row => String(row.body).includes("FORBIDDEN after self-termination"))).toBe(false);
      expect(f.violations).toEqual([]);
    } finally {
      if (cleanupPath) await rm(cleanupPath, { force: true });
      await cleanup.close();
    }
  });

  test("CC-006/012 goal_snooze delays an active eligible goal then admits its continuation", async ({ page }) => {
    const args = { duration_ms: 300_000 };
    f = await claudeWorkersHarness({ executions_by_input: [
      { contains: "REASON: Take the next actionable step to make progress on the goal.", turns: [[call("snooze-active", "goal_snooze", args), text("Active goal snoozed")]] },
      { contains: "REASON: Wait for the saved snooze", turns: [[text("Snoozed goal finished")]] },
    ] });
    await f.open(page); const agent = await f.launch(page);
    f.replies.push({ answer: "VERDICT: NOT_DONE\nREASON: Wait for the saved snooze", evaluation: true },
      { answer: "VERDICT: COMPLETED\nREASON: Goal finished", evaluation: true });
    // Browser sets the goal before its real initiating turn; MCP performs the
    // snooze itself. No fabricated receipt or inactive/idle denial substitutes.
    expect((await f.request(page, `/v1/managed-agents/${agent}/goal`,
      { action: "set", goal: "Finish after one bounded snooze", settings: {
        snooze_enabled: true, snooze_duration_ms: args.duration_ms,
        snooze_turn_limit: null, snooze_budget_cap_ms: null, snooze_refill_yield_ms: 30_000,
        snooze_refill_elapsed_ms: 60_000,
      } }, "PUT")).status).toBe(200);
    await f.final(page, agent, "Active goal snoozed");
    await expect.poll(() => settled(f!, agent).map(row => row.state)).toEqual(["NOT_DONE"]);
    const checked = checkedTool(f, agent, "goal_snooze", args);
    observedEffect(f, agent, checked);
    expect(checked.payload).toMatchObject({ CallID: checked.id, DurationMS: args.duration_ms, Active: true });
    const calls = f.tableRows("embedded_goal_snooze_calls", agent);
    expect(calls).toHaveLength(1);
    expect(calls[0].call_id).toBe(checked.id);
    expect(JSON.parse(String(calls[0].result_json))).toEqual(checked.payload);
    const preferences = f.tableRows("embedded_goal_snooze_preferences", agent);
    expect(preferences).toHaveLength(1);
    expect(preferences[0]).toMatchObject({ call_id: checked.id, due_at: checked.payload.DueAt, active: 1 });
    const initial = settled(f, agent)[0];
    expect(initial).toMatchObject({ continuation_state: "reserved", due_at: checked.payload.DueAt,
      user_seq: calls[0].user_seq, user_digest: calls[0].user_digest, goal_revision: calls[0].goal_revision });
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(true);
    // The endpoint requires a positive delta and reports only whole seconds.
    // Its truncated timestamp may overestimate the remaining time by 999 ms.
    const { now } = await f.router.advanceClock(1);
    const remaining = Date.parse(checked.payload.DueAt) - Date.parse(now);
    expect(remaining).toBeGreaterThan(2000); expect(remaining).toBeLessThanOrEqual(args.duration_ms + 999);
    await f.advance(remaining - 2000);
    expect(await f.inputs()).toHaveLength(1);
    expect(settled(f, agent)[0].started_at).toBeNull();
    await f.advance(2001);
    await f.final(page, agent, "Snoozed goal finished");
    await expect.poll(() => settled(f!, agent).map(row => row.state)).toEqual(["NOT_DONE", "DONE"]);
    expect(await f.inputs()).toHaveLength(2);
    expect(settled(f, agent)[0].started_at).not.toBeNull();
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(false);
    expect(f.requests).toHaveLength(2); expect(f.violations).toEqual([]);
  });
});
