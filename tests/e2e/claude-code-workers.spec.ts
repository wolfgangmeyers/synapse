import { readFile, writeFile } from "node:fs/promises";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import { test, expect, type Page } from "@playwright/test";
import { claudeWorkersHarness, exists, type ClaudeWorkersHarness } from "./claude-workers-harness";
import { settled } from "./workflow-implicit-goals-harness";

// SPEC-LIFECYCLE-001, CC-001–018. Auth/default refusal also reuses the existing
// claude-catalog-admission.spec.ts; Pi regression stays in runtime-engine-boundary.
// Authored acceptance tests, NOT execution evidence. Full-scenario gaps listed
// in integrationGaps below must not be inferred covered from a scenario tag.
export const integrationGaps = {
  "CC-001": "worktree launch/config inheritance beyond argv/cwd",
  "CC-003": "agent/reminder inbound images; send_image outgoing pixels",
  "CC-004": "startup/recovery delayed echo and same-text result reordering",
  "CC-005": "rollback/expiry/late callback and epoch-commit crash barriers; Pi owned separately",
  "CC-006": "all tool schemas; concurrent HTTP-before-producer; attachment pre-admission",
  "CC-007": "crash at each domain/intent/checked-result barrier; durable replay after process restart",
  "CC-008": "idle status without a foreground grant",
  "CC-009": "router SIGKILL recovery and interrupted-tool marking",
  "CC-010": "set_model refusal fallback with child lifetime",
  "CC-011": "threshold override/warning/loop edge, seed 200k truncation, compaction",
  "CC-012": "snooze/cap20/empty breaker/aborted-unprompted exclusions",
  "CC-013": "zero-window and unusable-stream default injection",
  "CC-014": "missing reset fallback, restart hold rediscovery and interrupted-work-first",
  "CC-015": "startup orphan sweep, missing transcript reseed, stderr 64KiB",
  "CC-016": "fresh process cleanup and resumption across executions",
  "CC-017": "64/6MiB input and 128/16MiB producer-wait limits",
  "CC-018": "pre-submit denied input and exact echo/fence barrier; unsolicited scope",
};

const text = (value: string) => ({ text: value });
const call = (id: string, name: string, input: object) => ({ mcp_call: { id, name, input } });

function processStat(pid: number) {
  try {
    const raw = readFileSync(`/proc/${pid}/stat`, "utf8");
    const fields = raw.slice(raw.lastIndexOf(")") + 2).trim().split(/\s+/);
    return { state: fields[0], group: Number(fields[2]), start: fields[19] };
  } catch (error) { if (error.code === "ENOENT" || error.code === "ESRCH") return undefined; throw error; }
}

async function resetFromControls(page: Page, agent: string) {
  await page.getByRole("button", { name: "More agent controls", exact: true }).click();
  const response = page.waitForResponse(r => new URL(r.url()).pathname === `/v1/managed-agents/${encodeURIComponent(agent)}/reset` && r.request().method() === "POST");
  await page.getByRole("menu", { name: "Agent controls menu" }).getByRole("button", { name: "Reset", exact: true }).click();
  const accepted = await response;
  expect(accepted.status()).toBe(202);
  const { operation_id: operationId } = await accepted.json();
  expect(operationId).toEqual(expect.any(String));
  expect(operationId.length).toBeGreaterThan(0);
  return operationId;
}

test.describe("Claude workers deterministic CLI acceptance", () => {
  test.setTimeout(90_000);
  let f: ClaudeWorkersHarness | undefined;
  let agent = "";
  test.afterEach(async ({}, info) => {
    try {
      if (f && info.status !== info.expectedStatus) await info.attach("claude-boundaries", {
        contentType: "application/json", body: Buffer.from(JSON.stringify({ records: await f.records(),
          requests: f.requests, violations: f.violations, ws: f.wsFrames,
          entries: agent ? f.rows("SELECT kind,epoch,seq,CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY epoch,seq", agent) : [],
        }, null, 2)),
      });
    } finally { await f?.close(); f = undefined; agent = ""; }
  });

  test("CC-001 launches local Claude without provider settings via direct cross-origin control", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[text("Initial instruction answered")]] });
    await f.open(page);
    const before = await f.api("/v1/provider-auth");
    agent = await f.launch(page, { initial_instruction: "Inspect only the local fixture" });
    await f.final(page, agent, "Initial instruction answered");
    const starts = await f.starts(); expect(starts).toHaveLength(1);
    expect(starts[0].cwd).toBe(`${f.router.root}/work`);
    expect(starts[0].argv).toEqual(expect.arrayContaining(["--model", "sonnet", "--permission-mode", "bypassPermissions", "--replay-user-messages"]));
    expect(JSON.stringify(await f.inputs())).toContain("Inspect only the local fixture");
    expect((await f.api("/v1/provider-auth")).providers).toEqual(before.providers);
    expect(JSON.stringify(before)).not.toContain('"provider":"claude-code"');
    expect((await f.api("/v1/models")).default_model.provider).not.toBe("claude-code");
    expect(f.violations).toEqual([]);
  });

  for (const [label, auth] of [
    ["signed out", { loggedIn: false }], ["malformed", { raw: "not-json" }], ["hung", { hang: true }],
  ] as const) test(`CC-002 rejects ${label} on demand and retries after local correction`, async ({ page }) => {
    f = await claudeWorkersHarness({ auth }); await f.open(page);
    const name = `refused-${label.replaceAll(" ", "-")}`;
    const result = await f.request(page, "/v1/managed-agents", { name, cwd: `${f.router.root}/work`, runtime: "embedded", model: { provider: "claude-code", id: "sonnet" } });
    expect(result.status).toBe(422); expect(result.body.error.code).toBe("CLAUDE_UNAVAILABLE");
    expect(result.body.error.message).toMatch(/host|install|sign.in/i);
    expect(f.tableRows("managed_agents", name)).toEqual([]); expect(await f.starts()).toEqual([]);
    await f.configure({ turns: [[text("Corrected local auth")]] });
    agent = await f.launch(page); await f.userTurn(page, "Retry locally");
    await f.final(page, agent, "Corrected local auth");
  });

  test("CC-002 missing local interpreter fails without falling back to host Claude", async ({ page }) => {
    f = await claudeWorkersHarness(); await f.open(page); await f.breakExecutable();
    const result = await f.request(page, "/v1/managed-agents", { name: "missing-cli-fixture", cwd: `${f.router.root}/work`, runtime: "embedded", model: { provider: "claude-code", id: "sonnet" } });
    expect(result.status).toBe(422); expect(result.body.error.code).toBe("CLAUDE_UNAVAILABLE");
    expect(f.tableRows("managed_agents", "missing-cli-fixture")).toEqual([]);
    expect(await f.records()).toEqual([]);
    await f.restoreExecutable(); await f.configure({ turns: [[text("Executable restored")]] });
    agent = await f.launch(page); await f.userTurn(page, "Retry restored executable");
    await f.final(page, agent, "Executable restored");
  });

  test("CC-003 renders images tool steps and final without automatic mail", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[{ thinking: "Inspecting deterministic image" },
      { tool: { id: "native-read", name: "Read", input: { file_path: "note.txt" }, result: "local fixture contents" } },
      call("scratch-create", "scratchpad", { action: "append", content: "Image observation saved" }), text("Claude image complete")]] });
    await f.open(page); agent = await f.launch(page);
    const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==", "base64");
    await page.locator('input[type="file"][accept="image/*"]').setInputFiles({ name: "pixel.png", mimeType: "image/png", buffer: png });
    const sent = page.waitForRequest(request => request.method() === "POST" &&
      request.postDataJSON()?.attachments?.some(attachment => attachment.type === "image"));
    await f.userTurn(page, "Inspect this pixel"); await f.final(page, agent, "Claude image complete");
    // The real composer flattens/resizes to JPEG. Its upload, not the source
    // PNG encoding, is the exact image the runtime must deliver unchanged.
    const uploaded = (await sent).postDataJSON().attachments[0].data_url;
    const uploadedBytes = uploaded.split(",")[1];
    const input = JSON.stringify(await f.inputs());
    expect(input).toContain("Inspect this pixel"); expect(input).toContain('"type":"image"'); expect(input).toContain(uploadedBytes);
    const inputs = await f.inputs();
    const images = inputs.flatMap(item => item.message.content).filter(block => block.type === "image");
    expect(images).toEqual([{ type: "image", source: { type: "base64", media_type: "image/jpeg", data: uploadedBytes } }]);
    const attachments = f.rows("SELECT attachments_json FROM conversation_messages WHERE body LIKE ?", "%Inspect this pixel%");
    expect(attachments.length).toBeGreaterThan(0);
    const attachment = JSON.parse(String(attachments[0].attachments_json))[0];
    const stored = await readFile(`${f.router.databasePath}.attachments/${attachment.id}`);
    expect(stored.toString("base64")).toBe(uploadedBytes);
    // Decode both boundary images independently and compare every RGBA pixel.
    const pixels = await page.evaluate(async ({ uploaded, delivered }) => {
      async function decode(url: string) {
        const bitmap = await createImageBitmap(await (await fetch(url)).blob());
        const canvas = document.createElement("canvas"); canvas.width = bitmap.width; canvas.height = bitmap.height;
        const context = canvas.getContext("2d"); if (!context) throw Error("No image decoder");
        context.drawImage(bitmap, 0, 0); bitmap.close();
        return { width: canvas.width, height: canvas.height, rgba: Array.from(context.getImageData(0, 0, canvas.width, canvas.height).data) };
      }
      return { uploaded: await decode(uploaded), delivered: await decode(delivered) };
    }, { uploaded, delivered: `data:${images[0].source.media_type};base64,${images[0].source.data}` });
    expect(pixels.uploaded.width).toBe(1); expect(pixels.uploaded.height).toBe(1);
    expect(pixels.delivered).toEqual(pixels.uploaded);
    await expect(page.getByText("Inspecting deterministic image", { exact: true }).first()).toBeVisible();
    // Native tool display translation must not create a router-effect witness.
    const checked = f.rows("SELECT kind, CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? ORDER BY seq", agent);
    expect(checked.some(row => String(row.body).includes("native-read"))).toBe(true);
    expect(checked.some(row => String(row.body).includes("local fixture contents"))).toBe(true);
    expect(f.tableRows("embedded_effects", agent).some(row => String(row.effect_id).includes("native-read"))).toBe(false);
    expect(f.tableRows("embedded_acks", agent)).toHaveLength(1);
    expect(f.tableRows("scratchpad_entries", agent).map(row => row.content)).toEqual(["Image observation saved"]);
    const record = f.tableRows("managed_agents", agent)[0]; expect(record.context_tokens).toBe(150); expect(record.context_window).toBe(1000000);
    expect(f.rows("SELECT o.* FROM embedded_reply_outbox o JOIN embedded_attempts a ON a.attempt_id=o.attempt_id WHERE a.agent_id=?", agent)).toEqual([]);
  });

  test("CC-004 ACKs exact echoes, not accepted equal-text queued input", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[text("Partial first turn"), { wait: "interrupt" }]] });
    await f.open(page); agent = await f.launch(page);
    await f.userTurn(page, "same text");
    await expect.poll(() => f!.tableRows("embedded_acks", agent).length).toBe(1);
    const firstAck = f.tableRows("embedded_acks", agent)[0];
    const firstStart = (await f.starts())[0];
    const messageId = randomUUID();
    const { now } = await f.router.advanceClock(1);
    const sent = await f.request(page, "/v1/messages", { id: messageId, from: "equal-text-fixture", to: agent, body: "same text", created_at: new Date(now).toISOString() });
    expect(sent.status).toBe(201); expect(sent.body.message.id).toBe(messageId);
    await expect.poll(() => f!.rows("SELECT state FROM messages WHERE id=?", messageId)[0]?.state).toBe("queued");
    expect(f.tableRows("embedded_attempts", agent).filter(row => row.message_id === messageId)).toEqual([]);
    expect(f.tableRows("embedded_acks", agent)).toEqual([firstAck]);
    expect(await f.inputs()).toHaveLength(1);
    await f.configure({ turns: [[text("Queued distinct turn")]] });
    await page.getByRole("button", { name: "Interrupt worker", exact: true }).click();
    await f.final(page, agent, "Queued distinct turn");
    const acks = f.tableRows("embedded_acks", agent); expect(acks).toHaveLength(2);
    expect(acks.filter(row => row.message_id === firstAck.message_id)).toEqual([firstAck]);
    const attempt = f.tableRows("embedded_attempts", agent).find(row => row.message_id === messageId);
    expect(attempt).toBeTruthy();
    expect(acks.find(row => row.message_id === messageId)?.attempt_id).toBe(attempt!.attempt_id);
    expect(firstAck.message_id).not.toBe(messageId);
    expect(new Set(acks.map(row => row.entry_seq)).size).toBe(2);
    const starts = await f.starts(); expect(starts).toHaveLength(2);
    expect(starts[1].pid).not.toBe(firstStart.pid);
    expect(await f.inputs()).toHaveLength(2);
  });

  test("CC-005 browser reset preserves queued unconsumed mail", async ({ page }) => {
    test.skip(process.platform !== "linux", "process identity checkpoint requires /proc");
    f = await claudeWorkersHarness({ turns: [[{ spawn_child: {} }, call("reset-live-grant", "mecha_context", { action: "status" }), text("Consumed before reset"), { barrier: "active-reset" }]] });
    await f.open(page); agent = await f.launch(page);
    await f.userTurn(page, "consumed mail"); await expect.poll(() => f!.tableRows("embedded_acks", agent).length).toBe(1);
    const firstAck = f.tableRows("embedded_acks", agent)[0];
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "child")).toBe(true);
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "active-reset")).toBe(true);
    const firstStart = (await f.starts())[0];
    const child = (await f.records()).find(row => row.kind === "child")!;
    const pid = Number(firstStart.pid), childPid = Number(child.pid);
    expect(Number.isSafeInteger(pid) && pid > 1 && Number.isSafeInteger(childPid) && childPid > 1).toBe(true);
    expect(child.parent_pid).toBe(pid);
    const leaderIdentity = processStat(pid), childIdentity = processStat(childPid);
    expect(leaderIdentity?.group).toBe(pid); expect(childIdentity?.group).toBe(pid);
    expect(leaderIdentity?.start).toMatch(/^\d+$/); expect(childIdentity?.start).toMatch(/^\d+$/);
    expect(leaderIdentity?.state).not.toMatch(/[ZXx]/); expect(childIdentity?.state).not.toMatch(/[ZXx]/);
    const grant = await f.grant();
    const liveGrant = await f.mcp(grant, "mecha_context", { action: "status" }, "reset-live-grant");
    expect(liveGrant.status).toBe(200); expect(liveGrant.body.error).toBeUndefined();
    expect(liveGrant.body.result?.isError).not.toBe(true);
    const messageId = randomUUID();
    const { now } = await f.router.advanceClock(1);
    const sent = await f.request(page, "/v1/messages", { id: messageId, from: "reset-fixture", to: agent, body: "unconsumed mail", created_at: new Date(now).toISOString() });
    expect(sent.status).toBe(201); expect(sent.body.message.id).toBe(messageId);
    await expect.poll(() => f!.rows("SELECT state FROM messages WHERE id=?", messageId)[0]?.state).toBe("queued");
    expect(f.tableRows("embedded_attempts", agent).filter(row => row.message_id === messageId)).toEqual([]);
    expect(f.tableRows("embedded_acks", agent)).toEqual([firstAck]);
    expect(await f.inputs()).toHaveLength(1);
    await f.configure({ turns_by_input: [
      { contains: "Context reset completed", steps: [text("Reset wake answered")] },
      { contains: "unconsumed mail", steps: [text("Unconsumed redelivered")] },
    ] });
    await writeFile(join(f.fixtureRoot, "reset-checkpoint.json"), JSON.stringify({ leader: pid, child: childPid, grant }), { mode: 0o600 });
    await resetFromControls(page, agent);
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "reset_checkpoint"), { timeout: 15_000 }).toBe(true);
    const checkpoint = (await f.records()).find(row => row.kind === "reset_checkpoint")!;
    // Snapshot and stale-grant call occur in the replacement launcher before
    // it imports the fake CLI or reads the next execution's input.
    for (const [snapshot, identity] of [[checkpoint.leader, leaderIdentity], [checkpoint.child, childIdentity]] as const) {
      if (snapshot !== null) {
        expect(snapshot.start === identity?.start && !["Z", "X", "x"].includes(snapshot.state)).toBe(false);
      }
    }
    expect(checkpoint.status).toBe(401);
    expect(checkpoint.body.error).toEqual({ code: -32001, message: "invalid process grant" });
    expect(await exists(grant.path)).toBe(false);
    await f.final(page, agent, "Unconsumed redelivered");
    const replacement = f.tableRows("embedded_attempts", agent).find(row => row.message_id === messageId);
    expect(replacement?.epoch).toBe(2);
    const acks = f.tableRows("embedded_acks", agent); expect(acks).toHaveLength(2);
    expect(acks.filter(row => row.message_id === messageId)).toHaveLength(1);
    expect(acks.find(row => row.message_id === messageId)?.attempt_id).toBe(replacement?.attempt_id);
    expect(acks.filter(row => row.message_id === firstAck.message_id)).toEqual([firstAck]);
    const starts = await f.starts();
    expect(starts.length).toBeGreaterThanOrEqual(2);
    expect(starts.at(-1)?.pid).not.toBe(firstStart.pid);
  });

  test("CC-006 checked MCP result replays once and changed arguments conflict", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[call("one-goal-off", "goal", { action: "off" }), text("Checked tool result"), { wait: "interrupt" }]] });
    await f.open(page); agent = await f.launch(page); await f.userTurn(page, "Turn the goal off through MCP");
    await expect.poll(() => f!.tableRows("embedded_goal_calls", agent).length).toBe(1);
    await expect.poll(() => f!.rows("SELECT kind FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent).length).toBe(1);
    const grant = await f.grant(); const before = f.tableRows("embedded_goal_calls", agent);
    const replay = await f.mcp(grant, "goal", { action: "off" }, "one-goal-off");
    expect(replay.status).toBe(200); expect(replay.body.result.isError).not.toBe(true);
    const repeated = await f.mcp(grant, "goal", { action: "off" }, "one-goal-off"); expect(repeated.body.result).toEqual(replay.body.result);
    const conflict = await f.mcp(grant, "goal", { action: "set", goal: "Must not apply" }, "one-goal-off");
    expect(conflict.body.error ?? conflict.body.result?.isError).toBeTruthy();
    expect(f.tableRows("embedded_goal_calls", agent)).toEqual(before);
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(false);
    await page.getByRole("button", { name: "Interrupt worker", exact: true }).click(); await f.idle(agent);
  });

  test("CC-007 failed checked result cannot produce a successful final or duplicate domain call", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[call("uncertain-goal", "goal", { action: "off" }), text("FORBIDDEN successful final")]] });
    const db = new DatabaseSync(f.router.databasePath);
    // Install before worker admission, not during launch transactions. SQLite
    // busy handlers are connection-local; bound fixture DDL contention only.
    try { db.exec("PRAGMA busy_timeout = 5000"); db.exec("CREATE TRIGGER claude_fail_result BEFORE INSERT ON embedded_entries WHEN NEW.kind IN ('toolResult','tool_result') BEGIN SELECT RAISE(ABORT,'injected checked result failure'); END"); }
    finally { db.close(); }
    await f.open(page); agent = await f.launch(page);
    await f.userTurn(page, "Exercise checked MCP failure");
    await expect.poll(() => f!.tableRows("embedded_goal_calls", agent).length).toBe(1);
    await f.idle(agent);
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes("FORBIDDEN successful final"))).toBe(false);
    expect(f.tableRows("embedded_acks", agent)).toHaveLength(1); // consumed input is not rolled back
    expect(f.tableRows("embedded_goal_calls", agent)).toHaveLength(1);
    expect(f.rows("SELECT kind FROM embedded_entries WHERE agent_id=? AND kind IN ('toolResult','tool_result')", agent)).toEqual([]);
  });

  test("CC-006 missing tool ID or checked producer cannot mutate scratchpad", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[text("Producer barrier"), { wait: "interrupt" }]] });
    await f.open(page); agent = await f.launch(page); await f.userTurn(page, "Hold admitted turn");
    await expect.poll(() => f!.tableRows("embedded_acks", agent).length).toBe(1);
    const grant = await f.grant();
    for (const id of [undefined, "not-a-checked-producer"]) {
      const denied = await f.mcp(grant, "scratchpad", { action: "append", content: "Must not persist" }, id);
      expect(denied.body.error ?? denied.body.result?.isError).toBeTruthy();
    }
    expect(f.tableRows("scratchpad_entries", agent)).toEqual([]);
    await page.getByRole("button", { name: "Interrupt worker", exact: true }).click(); await f.idle(agent);
  });

  test("CC-008 idle grant cannot reuse foreground authority for effects", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[call("active-status", "mecha_context", { action: "status" }), { barrier: "grant-active" }, text("Idle grant ready")]] }); await f.open(page); agent = await f.launch(page);
    await f.userTurn(page, "Become idle");
    await expect.poll(async () => (await f!.records()).some(r => r.kind === "barrier" && r.name === "grant-active")).toBe(true);
    const grant = await f.grant();
    const status = await f.mcp(grant, "mecha_context", { action: "status" }, "active-status");
    expect(status.status).toBe(200); expect(status.body.error, JSON.stringify(status.body)).toBeUndefined(); expect(status.body.result, JSON.stringify(status.body)).toBeTruthy(); expect(status.body.result.isError).not.toBe(true);
    await f.releaseBarrier((await f.records()).find(r => r.kind === "barrier" && r.name === "grant-active")!);
    await f.final(page, agent, "Idle grant ready");
    await expect.poll(() => exists(grant.path)).toBe(false);
    const before = f.tableRows("embedded_acks", agent);
    const staleStatus = await f.mcp(grant, "mecha_context", { action: "status" }, "idle-status");
    expect(staleStatus.status).toBe(401);
    expect(staleStatus.body.error).toEqual({ code: -32001, message: "invalid process grant" });
    for (const [name, args] of [["goal", { action: "off" }], ["context_reset", {}], ["goal_snooze", { duration_ms: 1000 }], ["mecha_worker_terminate", {}]] as const) {
      const denied = await f.mcp(grant, name, args, `idle-${name}`);
      expect(denied.status).toBe(401);
      expect(denied.body.error).toEqual({ code: -32001, message: "invalid process grant" });
    }
    expect(f.tableRows("embedded_acks", agent)).toEqual(before);
    expect(f.tableRows("embedded_goal_evaluations", agent)).toEqual([]);
    expect(f.tableRows("managed_agents", agent)[0].session_epoch).toBe(1);
    expect(f.tableRows("managed_agents", agent)[0].desired_state).toBe("running");
  });

  test("CC-009 interrupt preserves partial output and eventually cleans old child and grant", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [{ contains: "Start controlled child", steps: [{ spawn_child: {} }, text("Partial output survives"), { wait: "interrupt" }] }, { contains: "Next turn", steps: [text("Conversation after interrupt")] }] });
    await f.open(page); agent = await f.launch(page); await f.userTurn(page, "Start controlled child");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "child")).toBe(true);
    const child = (await f.records()).find(row => row.kind === "child")!;
    const grant = await f.grant();
    const started = Date.now(); await page.getByRole("button", { name: "Interrupt worker", exact: true }).click(); await f.idle(agent);
    expect(Date.now() - started).toBeLessThan(5000);
    await expect(page.getByText("Partial output survives", { exact: true }).first()).toBeVisible();
    await f.userTurn(page, "Next turn"); await f.final(page, agent, "Conversation after interrupt");
    const starts = await f.starts(); expect(starts).toHaveLength(2);
    expect(starts[1].pid).not.toBe(starts[0].pid);
    expect((await f.request(page, `/v1/managed-agents/${agent}`, undefined, "DELETE")).status).toBe(202);
    await expect.poll(() => { try { process.kill(Number(child.pid), 0); return false; } catch (error) { if (error.code === "ESRCH") return true; throw error; } }, { timeout: 5000 }).toBe(true);
    expect(await exists(grant.path)).toBe(false);
    expect((await f.mcp(grant, "mecha_context", { action: "status" }, "stale-grant")).status).not.toBe(200);
  });

  test("CC-010 partitions models and rejects default and cross-engine mutation", async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [{ contains: "First turn", steps: [text("Before model change")] }, { contains: "Use the newly selected model", steps: [text("After model change")] }] });
    await f.open(page); agent = await f.launch(page); await f.userTurn(page, "First turn"); await f.final(page, agent, "Before model change");
    const denied = await f.request(page, "/v1/settings/default-model", { model: { provider: "claude-code", id: "sonnet" } }, "PUT");
    expect(denied.status).toBe(422);
    const cross = await f.request(page, `/v1/managed-agents/${agent}/restart`, { model: { provider: "openai-codex", id: "gpt-6.1-terra" } });
    expect(cross.status).toBe(422);
    // The selected model applies to the next execution.
    await page.getByRole("button", { name: /claude-code\/sonnet/ }).click();
    const picker = page.getByRole("dialog", { name: "Choose agent model", exact: true });
    await picker.getByRole("searchbox", { name: "Search available models" }).fill("claude-code/haiku");
    const changed = page.waitForResponse(r => new URL(r.url()).pathname === `/v1/managed-agents/${encodeURIComponent(agent)}/restart` && r.request().method() === "POST");
    await picker.getByRole("option").filter({ hasText: "claude-code/haiku" }).click();
    expect((await changed).status()).toBe(202);
    await expect.poll(() => f!.tableRows("managed_agents", agent)[0].selected_model_id).toBe("haiku");
    expect(f.tableRows("managed_agents", agent)[0].selected_model_provider).toBe("claude-code");
    await expect.poll(() => f!.tableRows("managed_agents", agent)[0].lifecycle_state).toBe("running");
    await f.userTurn(page, "Use the newly selected model"); await f.final(page, agent, "After model change");
    const starts = await f.starts(); expect(starts).toHaveLength(2);
    expect(starts[1].pid).not.toBe(starts[0].pid);
    expect(starts[1].argv).toEqual(expect.arrayContaining(["--model", "haiku"]));
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes("Before model change"))).toBe(true);
    expect((await f.api("/v1/models")).default_model.provider).not.toBe("claude-code");
    await page.getByRole("button", { name: /claude-code\/haiku/ }).click();
    await picker.getByRole("searchbox", { name: "Search available models" }).fill("openai-codex/gpt-6.1-terra");
    await expect(picker.getByRole("listbox")).toHaveAttribute("aria-busy", "false");
    await expect(picker.getByText("No matching models", { exact: true })).toBeVisible();
    await expect(picker.getByRole("option").filter({ has: picker.getByText("openai-codex/gpt-6.1-terra", { exact: true }) })).toHaveCount(0);
  });

  for (const trigger of ["browser", "tool"] as const) test(`CC-011 ${trigger} reset preserves ordered seed and old grant is stale`, async ({ page }) => {
    f = await claudeWorkersHarness({ turns_by_input: [
      { contains: "Context reset completed", steps: [text("Fresh reset answer")] },
      { contains: "Reset with tool", steps: [call("reset-now", "context_reset", { window: 20 })] },
      { contains: "Initial ordered seed", steps: [call("seed-note", "scratchpad", { action: "append", content: "Remember violet" }), { barrier: "seed-grant" }, text("History violet")] },
    ] });
    await f.open(page); agent = await f.launch(page, { initial_instruction: "Initial ordered seed" });
    await expect.poll(async () => (await f!.records()).some(r => r.kind === "barrier" && r.name === "seed-grant")).toBe(true);
    const grant = await f.grant();
    await f.releaseBarrier((await f.records()).find(r => r.kind === "barrier" && r.name === "seed-grant")!);
    await f.final(page, agent, "History violet");
    if (trigger === "browser") await resetFromControls(page, agent);
    else await f.userTurn(page, "Reset with tool");
    await f.final(page, agent, "Fresh reset answer");
    const users = await f.inputs(); const seed = JSON.stringify(users.at(-1));
    const parts = ["Initial ordered seed", "Remember violet", "History violet", "Context reset completed"];
    let previous = -1;
    for (const part of parts) { const index = seed.indexOf(part); expect(index).toBeGreaterThan(previous); previous = index; }
    expect(seed.length).toBeLessThanOrEqual(200000);
    expect(f.tableRows("managed_agents", agent)[0].session_epoch).toBe(2);
    expect(await exists(grant.path)).toBe(false);
    expect((await f.mcp(grant, "scratchpad", { action: "fetch" }, "old-epoch")).status).not.toBe(200);
    await expect(page.getByText(/Context reset.*Epoch 2/i).first()).toBeVisible();
  });

  test("CC-012 evaluates checked applied goals using only deterministic Pi default", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[text("Goal still in progress")]] }); await f.open(page); agent = await f.launch(page);
    f.replies.push({ answer: "VERDICT: NOT_DONE\nREASON: More work", evaluation: true }, { answer: "VERDICT: COMPLETED\nREASON: Finished", evaluation: true });
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "Finish local acceptance task" }, "PUT");
    await expect.poll(() => settled(f!, agent).map(row => row.state)).toEqual(["NOT_DONE"]);
    await f.advance(9000);
    expect(settled(f, agent).map(row => row.state)).toEqual(["NOT_DONE"]);
    expect(f.tableRows("embedded_goal_continuations", agent)[0].state).toBe("reserved");
    await f.configure({ turns: [[text("Goal now complete")]] });
    await f.advance(1000);
    await expect.poll(() => settled(f!, agent).map(row => row.state)).toEqual(["NOT_DONE", "DONE"]);
    await f.final(page, agent, "Goal now complete");
    expect((await f.api(`/v1/managed-agents/${agent}/goal`)).goal_state.enabled).toBe(false);
    expect(f.requests).toHaveLength(2); expect(f.violations).toEqual([]);
    expect(f.requests.every(req => String(req.instructions).toLowerCase().startsWith("goal:"))).toBe(true);
  });

  test("CC-013 unavailable configured evaluator records error without fallback or continuation", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[text("No evaluator final")]] }); await f.open(page); agent = await f.launch(page);
    // Evaluation resolves the configured default directly, not through the
    // injected worker resolver. Clearing it safely proves no catalog fallback;
    // selecting seeded Google here could reach its real provider transport.
    await f.api("/v1/settings/default-model", { model: null }, "PUT");
    await f.api(`/v1/managed-agents/${agent}/goal`, { action: "set", goal: "No fallback search" }, "PUT");
    await f.final(page, agent, "No evaluator final");
    await expect(page.getByText(/goal evaluation unavailable/i).first()).toBeVisible();
    await f.advance(60000);
    expect(await f.inputs()).toHaveLength(1); expect(f.requests).toEqual([]);
    expect(f.tableRows("embedded_goal_continuations", agent)).toEqual([]); expect(f.violations).toEqual([]);
  });

  test("CC-014 usage limit leaves queued work unACKed before hold deadline", async ({ page }) => {
    f = await claudeWorkersHarness(); await f.open(page); agent = await f.launch(page);
    const deadline = Math.floor(Date.now() / 1000) + 900;
    await f.configure({ turns: [[{ rate_limit: { resetsAt: deadline } }], [text("Queued after limit")]] });
    await f.userTurn(page, "Hit scripted limit");
    await expect(page.getByText(/Claude usage limit reached/i).first()).toBeVisible();
    const consumed = f.tableRows("embedded_acks", agent);
    await f.userTurn(page, "Wait behind usage limit"); await f.advance(1000);
    expect(f.tableRows("embedded_acks", agent)).toEqual(consumed);
    expect(await f.inputs()).toHaveLength(1);
    expect(f.tableRows("managed_agents", agent)[0].desired_state).toBe("running");
    expect(f.tableRows("embedded_attempts", agent).length).toBeLessThanOrEqual(2);
  });

  test("CC-014 deadline resumes limited input before queued mail exactly once", async ({ page }) => {
    f = await claudeWorkersHarness(); await f.open(page); agent = await f.launch(page);
    const deadline = Math.floor(Date.now() / 1000) + 12;
    // The fake reads its script once at process startup, not on each input.
    await f.configure({ executions_by_input: [
      { contains: "Original limited input", turns: [[{ rate_limit: { resetsAt: deadline } }], [{ barrier: "retry" }, text("Limited work resumed")]] },
      { contains: "Queued during hold", turns: [[text("Queued work completed")]] },
    ] });
    await f.userTurn(page, "Original limited input");
    await expect(page.getByText(/Claude usage limit reached/i).first()).toBeVisible();
    await expect.poll(() => f!.tableRows("embedded_acks", agent).length).toBe(1);
    const originalAck = f.tableRows("embedded_acks", agent)[0];
    const originalAttempts = f.tableRows("embedded_attempts", agent);
    expect(originalAttempts).toHaveLength(1);
    const original = originalAttempts[0];
    expect(original.accepted_body).toBe("Original limited input");
    expect(originalAck.message_id).toBe(original.message_id);
    expect(originalAck.attempt_id).toBe(original.attempt_id);
    const firstStart = (await f.starts())[0];
    expect(firstStart).toBeTruthy();
    const queuedResponse = page.waitForResponse(response => new URL(response.url()).pathname === "/v1/messages" && response.request().method() === "POST");
    await f.userTurn(page, "Queued during hold");
    const sent = await queuedResponse;
    expect(sent.status()).toBe(201);
    const queuedId = (await sent.json()).message.id;
    expect(typeof queuedId).toBe("string");
    const queuedRows = () => f!.rows("SELECT id, body FROM messages WHERE id=?", queuedId);
    await expect.poll(() => queuedRows().length).toBe(1);
    expect(queuedRows()[0].body).toBe("Queued during hold");
    expect(queuedId).not.toBe(original.message_id);
    expect(f.tableRows("embedded_attempts", agent).filter(row => row.message_id === queuedId)).toEqual([]);
    expect(f.tableRows("embedded_acks", agent)).toEqual([originalAck]);
    expect(await f.inputs()).toHaveLength(1);
    await expect.poll(async () => (await f!.records()).some(r => r.kind === "barrier" && r.name === "retry"), { timeout: 15000 }).toBe(true);
    const retry = (await f.records()).find(r => r.kind === "barrier" && r.name === "retry")!;
    expect(await f.inputs()).toHaveLength(2);
    expect(retry.pid).toBe(firstStart.pid);
    expect(f.tableRows("embedded_attempts", agent).map(row => [row.message_id, row.attempt_id])).toEqual([[original.message_id, original.attempt_id]]);
    expect(f.tableRows("embedded_acks", agent)).toEqual([originalAck]);
    await f.releaseBarrier(retry);
    await expect(page.getByText("Limited work resumed", { exact: true }).first()).toBeVisible();
    await expect.poll(async () => (await f!.inputs()).length, {timeout: 10000}).toBeGreaterThanOrEqual(3);
    await f.final(page, agent, "Queued work completed");
    const starts = await f.starts(); expect(starts).toHaveLength(2);
    expect(starts[0].pid).toBe(firstStart.pid);
    expect(starts[1].pid).not.toBe(firstStart.pid);
    const firstArgs = starts[0].argv; const secondArgs = starts[1].argv;
    if (!Array.isArray(firstArgs) || !Array.isArray(secondArgs)) throw Error("missing process argv");
    expect(firstArgs).toContain("--session-id"); expect(secondArgs).toContain("--resume");
    expect(secondArgs[secondArgs.indexOf("--resume") + 1]).toBe(firstArgs[firstArgs.indexOf("--session-id") + 1]);
    const records = await f.records();
    const stdin = records.filter(row => row.kind === "stdin" && typeof row.line === "string" && JSON.parse(row.line).type === "user");
    expect(stdin.map(row => row.pid)).toEqual([firstStart.pid, firstStart.pid, starts[1].pid]);
    const inputs = await f.inputs(); expect(inputs).toHaveLength(3);
    expect(inputs[0].message.content).toEqual([
      { type: "text", text: "Recent conversation preserved by the router:\n" },
      { type: "text", text: "[Direct message from user]: Original limited input" },
    ]);
    expect(inputs[1].message).toEqual(inputs[0].message);
    expect(inputs[2].message.content).toEqual([{ type: "text", text: "[Direct message from user]: Queued during hold" }]);
    const attempts = f.tableRows("embedded_attempts", agent); expect(attempts).toHaveLength(2);
    const queuedAttempts = attempts.filter(row => row.message_id === queuedId); expect(queuedAttempts).toHaveLength(1);
    expect(queuedAttempts[0].accepted_body).toBe("Queued during hold");
    expect(attempts.filter(row => row.message_id === original.message_id).map(row => row.attempt_id)).toEqual([original.attempt_id]);
    const acks = f.tableRows("embedded_acks", agent); expect(acks).toHaveLength(2);
    expect(acks.filter(row => row.message_id === original.message_id)).toEqual([originalAck]);
    const queuedAcks = acks.filter(row => row.message_id === queuedId); expect(queuedAcks).toHaveLength(1);
    expect(queuedAcks[0].attempt_id).toBe(queuedAttempts[0].attempt_id);
    expect(queuedAcks[0].entry_seq).toBeGreaterThan(originalAck.entry_seq);
    expect(f.tableRows("embedded_effects", agent)).toHaveLength(0);
  });

  test("CC-014I interrupt usage-limit hold preserves pending work", async ({ page }) => {
    f = await claudeWorkersHarness(); await f.open(page); agent = await f.launch(page);
    const deadline = Math.floor(Date.now() / 1000) + 900;
    await f.configure({ turns_by_input: [{ contains: "Limited original prompt", steps: [{ rate_limit: { resetsAt: deadline } }] }, { contains: "Pending work after interrupt", steps: [text("Pending work answered")] }, { contains: "Another turn", steps: [text("Reusable process answered")] }] });
    await f.userTurn(page, "Limited original prompt");
    await expect(page.getByText(/Claude usage limit reached/i).first()).toBeVisible();
    await f.userTurn(page, "Pending work after interrupt");
    expect(await f.inputs()).toHaveLength(1);
    await page.getByRole("button", { name: "Interrupt worker", exact: true }).click();
    await f.final(page, agent, "Pending work answered");
    await f.idle(agent);
    const inputs = await f.inputs();
    expect(JSON.stringify(inputs)).toContain("Pending work after interrupt");
    expect(JSON.stringify(inputs).match(/Limited original prompt/g)).toHaveLength(1);
    await f.userTurn(page, "Another turn");
    await f.final(page, agent, "Reusable process answered");
    expect(new Set((await f.starts()).map(start => start.pid)).size).toBe(3);
    expect(await f.starts()).toHaveLength(3);
  });

  test("CC-015 inherits normal configuration and cleans private process artifacts", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[{ barrier: "private-config" }, text("Private config ready")]] }); await f.open(page); agent = await f.launch(page);
    await f.userTurn(page, "Inspect process configuration");
    await expect.poll(async () => (await f!.records()).some(r => r.kind === "barrier" && r.name === "private-config")).toBe(true);
    const start = (await f.starts())[0]; const env = start.env;
    // workflowHarness strips these variables: fixture isolation evidence,
    // not transport-level environment scrubbing coverage.
    expect(env).not.toHaveProperty("TMUX"); expect(env).not.toHaveProperty("TMUX_PANE"); expect(env).not.toHaveProperty("CLAUDECODE");
    expect(start.argv).not.toContain("--strict-mcp-config");
    expect(start.argv).not.toContain("--setting-sources");
    const grant = await f.grant(); const token = grant.headers.Authorization.replace(/^Bearer /, "");
    expect(JSON.stringify(start.argv)).not.toContain(token);
    expect(await readFile(f.router.logPath, "utf8")).not.toContain(token);
    expect(JSON.stringify(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=?", agent))).not.toContain(token);
    await f.releaseBarrier((await f.records()).find(r => r.kind === "barrier" && r.name === "private-config")!);
    await f.final(page, agent, "Private config ready");
    expect((await f.request(page, `/v1/managed-agents/${agent}`, undefined, "DELETE")).status).toBe(202);
    await expect.poll(() => exists(grant.path)).toBe(false);
  });

  test("CC-016 scoped executions resume queued mail and revoke the prior grant", async ({ page }) => {
    f = await claudeWorkersHarness({ executions_by_input: [
      { contains: "First scoped marker", turns: [[{ barrier: "first-scoped" }, text("First scoped answer")]] },
      { contains: "Second scoped marker", turns: [[{ barrier: "second-scoped" }, text("Second scoped answer")]] },
    ] });
    await f.open(page); agent = await f.launch(page);
    await f.userTurn(page, "First scoped marker");
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "first-scoped")).toBe(true);
    const first = (await f.records()).find(row => row.kind === "barrier" && row.name === "first-scoped")!;
    const grant = await f.grant();
    await expect.poll(() => f!.tableRows("embedded_acks", agent).length).toBe(1);
    const firstAck = f.tableRows("embedded_acks", agent)[0];
    const messageId = randomUUID();
    const { now } = await f.router.advanceClock(1);
    const sent = await f.request(page, "/v1/messages", { id: messageId, from: "scoped-fixture", to: agent, body: "Second scoped marker", created_at: new Date(now).toISOString() });
    expect(sent.status, JSON.stringify(sent.body)).toBe(201);
    expect(sent.body.message.id).toBe(messageId);
    await expect.poll(() => f!.rows("SELECT state FROM messages WHERE id=?", messageId)[0]?.state).toBe("queued");
    expect(f.rows("SELECT recipient FROM messages WHERE id=?", messageId)[0]?.recipient).toBe(agent);
    expect(f.tableRows("embedded_attempts", agent).filter(row => row.message_id === messageId)).toEqual([]);
    expect(f.tableRows("embedded_acks", agent)).toEqual([firstAck]);
    expect(await f.inputs()).toHaveLength(1);
    expect(JSON.stringify(await f.inputs())).not.toContain("Second scoped marker");
    await f.releaseBarrier(first);
    await expect.poll(async () => (await f!.records()).some(row => row.kind === "barrier" && row.name === "second-scoped")).toBe(true);
    const second = (await f.records()).find(row => row.kind === "barrier" && row.name === "second-scoped")!;
    expect(second.pid).not.toBe(first.pid);
    const starts = await f.starts(); expect(starts).toHaveLength(2);
    expect(starts.map(row => row.pid)).toEqual([first.pid, second.pid]);
    expect(starts[1].argv).toContain("--resume");
    const firstArgs = starts[0].argv; const secondArgs = starts[1].argv;
    if (!Array.isArray(firstArgs) || !Array.isArray(secondArgs)) throw Error("missing process argv");
    expect(secondArgs[secondArgs.indexOf("--resume") + 1]).toBe(firstArgs[firstArgs.indexOf("--session-id") + 1]);
    await expect.poll(() => f!.tableRows("embedded_acks", agent).length).toBe(2);
    const attempts = f.tableRows("embedded_attempts", agent);
    const queued = attempts.find(row => row.message_id === messageId);
    expect(queued).toBeTruthy();
    const acks = f.tableRows("embedded_acks", agent);
    expect(acks.find(row => row.message_id === messageId)?.attempt_id).toBe(queued!.attempt_id);
    expect(new Set(acks.map(row => row.message_id))).toEqual(new Set(attempts.map(row => row.message_id)));
    expect(new Set(acks.map(row => row.entry_seq)).size).toBe(2);
    expect(await f.inputs()).toHaveLength(2);
    await expect.poll(() => exists(grant.path)).toBe(false);
    expect((await f.mcp(grant, "mecha_context", { action: "status" }, "scoped-stale-grant")).status).not.toBe(200);
    await f.releaseBarrier(second);
    await f.final(page, agent, "Second scoped answer");
    await expect(page.getByText("First scoped answer", { exact: true }).first()).toBeVisible();
    expect(f.violations).toEqual([]);
  });

  for (const [label, step] of [
    ["malformed JSON", { raw: "not-json" }], ["oversize stdout", { raw: { text: "x", repeat: 16777217 } }],
  ] as const) test(`CC-017 ${label} fails closed and holds uncertain input without redelivery`, async ({ page }) => {
    f = await claudeWorkersHarness({ replay_control: "skip", turns: [[{ barrier: "failure" }, step, text("FORBIDDEN protocol final")]] });
    await f.open(page); agent = await f.launch(page);
    await f.userTurn(page, "Trigger deterministic protocol failure");
    await expect.poll(async () => (await f!.records()).some(r => r.kind === "barrier" && r.name === "failure")).toBe(true);
    const failureBarrier = (await f.records()).find(r => r.kind === "barrier" && r.name === "failure")!;
    const original = f.tableRows("embedded_attempts", agent);
    expect(original).toHaveLength(1);
    expect(String(original[0].accepted_body)).toContain("Trigger deterministic protocol failure");
    const effects = f.tableRows("embedded_effects", agent);
    const receipts = f.tableRows("embedded_goal_applied_receipts", agent);
    expect(f.tableRows("embedded_acks", agent)).toEqual([]);
    const failedStart = (await f.starts())[0];
    const args = failedStart.argv;
    if (!Array.isArray(args)) throw Error("missing failed process argv");
    const grantPath = args[args.indexOf("--mcp-config") + 1];
    if (typeof grantPath !== "string") throw Error("missing failed process grant");
    await f.releaseBarrier(failureBarrier);
    await expect.poll(() => exists(grantPath), { timeout: 10_000 }).toBe(false);
    await f.idle(agent);
    expect(f.tableRows("embedded_acks", agent)).toEqual([]);
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes("FORBIDDEN protocol final"))).toBe(false);
    expect(f.tableRows("managed_agents", agent)[0].desired_state).toBe("running");
    expect(f.tableRows("embedded_effects", agent)).toEqual(effects);
    expect(f.tableRows("embedded_goal_applied_receipts", agent)).toEqual(receipts);
    await f.configure({ turns_by_input: [{ contains: "Retry now", steps: [text("Retry after protocol failure")] }] });
    await f.userTurn(page, "Retry now");
    await f.final(page, agent, "Retry after protocol failure");
    const inputs = await f.inputs();
    expect(inputs.filter(input => JSON.stringify(input).includes("Trigger deterministic protocol failure"))).toHaveLength(1);
    const acks = f.tableRows("embedded_acks", agent);
    expect(acks).toHaveLength(1);
    expect(acks[0].attempt_id).not.toBe(original[0].attempt_id);
    expect(f.tableRows("embedded_effects", agent)).toEqual(effects);
    expect(f.tableRows("embedded_goal_applied_receipts", agent)).toEqual(receipts);
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes("FORBIDDEN protocol final"))).toBe(false);
  });

  test("CC-017 write-started mail stays held across reset and router restart", async ({ page }) => {
    f = await claudeWorkersHarness({ replay_control: "hold", turns: [[{ barrier: "held-mail" }]] });
    await f.open(page); agent = await f.launch(page);
    const messageId = randomUUID();
    const { now } = await f.router.advanceClock(1);
    const sent = await f.request(page, "/v1/messages", { id: messageId, from: "hold-fixture", to: agent, body: "Hold this exact mail", created_at: new Date(now).toISOString() });
    expect(sent.status).toBe(201);
    await expect.poll(async () => (await f!.records()).some(r => r.kind === "barrier" && r.name === "replay")).toBe(true);
    const replay = (await f.records()).find(r => r.kind === "barrier" && r.name === "replay")!;
    const heldInputs = (await f.inputs()).filter(input => JSON.stringify(input).includes("Hold this exact mail"));
    expect(heldInputs).toHaveLength(1);
    expect(typeof heldInputs[0].uuid).toBe("string");
    expect(replay.input_id).toBe(heldInputs[0].uuid);
    const attempt = f.tableRows("embedded_attempts", agent).find(row => row.message_id === messageId);
    expect(attempt).toBeTruthy();
    expect(f.rows("SELECT state,claude_stdin_started FROM messages WHERE id=?", messageId)).toEqual([{ state: "queued", claude_stdin_started: 1 }]);
    expect(f.tableRows("embedded_acks", agent).filter(row => row.message_id === messageId)).toEqual([]);
    await f.configure({ turns_by_input: [
      { contains: "Context reset completed", steps: [text("Reset hold checkpoint")] },
      { contains: "Probe after restart", steps: [text("Restart hold checkpoint")] },
      { contains: "Selection probe after reset", steps: [text("Reset mail selection checkpoint")] },
      { contains: "Selection probe after restart", steps: [text("Restart mail selection checkpoint")] },
      { contains: "Hold this exact mail", steps: [text("FORBIDDEN held mail replay")] },
    ] });
    await resetFromControls(page, agent);
    await expect.poll(() => f!.tableRows("managed_agents", agent)[0]?.session_epoch).toBe(2);
    await expect.poll(async () => (await f!.inputs()).some(input => JSON.stringify(input).includes("Context reset completed"))).toBe(true);
    const exactFinal = (checkpoint: string) => f!.rows(`SELECT json_extract(part.value, '$.text') AS text
      FROM embedded_entries e JOIN managed_agents m ON m.agent_id=e.agent_id AND m.session_epoch=e.epoch
      JOIN json_each(e.body, '$.content') part
      WHERE e.agent_id=? AND e.kind='assistant' AND json_extract(e.body, '$.role')='assistant'
        AND json_extract(e.body, '$.stopReason')='stop' AND json_extract(part.value, '$.type')='text'
        `, agent).filter(row => row.text === checkpoint);
    await expect.poll(() => exactFinal("Reset hold checkpoint").length).toBe(1);
    // A later delivered mail proves the actual oldest-first dispatcher selected
    // past the older held message, rather than merely observing an idle lane.
    const selectionProbe = async (label: string, checkpoint: string) => {
      const id = randomUUID();
      const { now: probeNow } = await f!.router.advanceClock(1);
      const result = await f!.request(page, "/v1/messages", { id, from: "hold-fixture", to: agent, body: label, created_at: new Date(probeNow).toISOString() });
      expect(result.status).toBe(201);
      await expect.poll(() => exactFinal(checkpoint).length).toBe(1);
      await expect.poll(() => f!.tableRows("embedded_acks", agent).some(row => row.message_id === id)).toBe(true);
    };
    await selectionProbe("Selection probe after reset", "Reset mail selection checkpoint");
    await f.idle(agent);
    expect(f.tableRows("embedded_attempts", agent).filter(row => row.message_id === messageId)).toEqual([]);
    expect(f.tableRows("embedded_acks", agent).filter(row => row.message_id === messageId)).toEqual([]);
    expect((await f.inputs()).filter(input => JSON.stringify(input).includes("Hold this exact mail"))).toHaveLength(1);
    expect(f.rows("SELECT state,claude_stdin_started FROM messages WHERE id=?", messageId)).toEqual([{ state: "queued", claude_stdin_started: 1 }]);
    await f.restart();
    await f.userTurn(page, "Probe after restart");
    await f.final(page, agent, "Restart hold checkpoint");
    await expect.poll(async () => (await f!.inputs()).some(input => JSON.stringify(input).includes("Probe after restart"))).toBe(true);
    await expect.poll(() => exactFinal("Restart hold checkpoint").length).toBe(1);
    await selectionProbe("Selection probe after restart", "Restart mail selection checkpoint");
    expect(f.rows("SELECT state,claude_stdin_started FROM messages WHERE id=?", messageId)).toEqual([{ state: "queued", claude_stdin_started: 1 }]);
    expect(f.tableRows("embedded_attempts", agent).filter(row => row.message_id === messageId)).toEqual([]);
    expect(f.tableRows("embedded_acks", agent).filter(row => row.message_id === messageId)).toEqual([]);
    expect((await f.inputs()).filter(input => JSON.stringify(input).includes("Hold this exact mail"))).toHaveLength(1);
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes("FORBIDDEN held mail replay"))).toBe(false);
    expect(f.violations).toEqual([]);
  });

  test("CC-018 rejected consumed-user persistence prevents final and goal receipt", async ({ page }) => {
    f = await claudeWorkersHarness({ turns: [[text("FORBIDDEN after rejected echo")]] });
    const db = new DatabaseSync(f.router.databasePath);
    // Same pre-admission fault-installation boundary as CC-007.
    try { db.exec("PRAGMA busy_timeout = 5000"); db.exec("CREATE TRIGGER claude_reject_echo BEFORE INSERT ON embedded_entries WHEN NEW.kind='user' BEGIN SELECT RAISE(ABORT,'injected checked echo rejection'); END"); }
    finally { db.close(); }
    await f.open(page); agent = await f.launch(page);
    await f.userTurn(page, "Reject actual checked consumption");
    await expect.poll(() => f!.tableRows("embedded_attempts", agent).length).toBe(1); await f.idle(agent);
    expect(f.tableRows("embedded_acks", agent)).toEqual([]);
    expect(f.tableRows("embedded_goal_applied_receipts", agent)).toEqual([]);
    expect(f.rows("SELECT CAST(body AS TEXT) AS body FROM embedded_entries WHERE agent_id=? AND kind='assistant'", agent)
      .some(row => String(row.body).includes("FORBIDDEN after rejected echo"))).toBe(false);
  });
});
