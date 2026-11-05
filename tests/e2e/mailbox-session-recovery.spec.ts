import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { join, resolve } from "node:path";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { startEmbeddedSafetyFixture } from "./harness";

test("SCN-MSR-001/002: context reset unblocks retired-epoch unstarted mail", async ({ page }) => {
  // Mode: real-capable. Only the external Codex HTTP boundary is mocked.
  test.setTimeout(120_000);
  const f = await startEmbeddedSafetyFixture();
  try {
    await page.addInitScript(url => {
      localStorage.setItem("mecha.web.activeRouter", url);
      localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
    }, f.proxy.url);
    const replies: string[] = [];
    page.on("websocket", ws => ws.on("framereceived", ({ payload }) => {
      const frame = JSON.parse(payload.toString());
      if (frame.type === "history_entry" && frame.entry?.presentation?.to === "user" && replies.length < 32) replies.push(frame.entry.message_content);
    }));
    await page.goto(f.frontend.url);
    const name = "mail-recovery";
    expect((await f.api("/v1/managed-agents", { name, cwd: join(f.router.root, "work"), runtime: "embedded", model: { provider: "openai-codex", id: "gpt-6.1-terra" }, initial_instruction: "Start" })).status).toBe(202);
    await page.locator(`[data-agent-id="${name}"]`).click();
    await expect(page.getByText("startup settled", { exact: true })).toBeVisible();
    const send = async (body: string, id = randomUUID()) => {
      const envelope = { id, from: "user", to: name, body, created_at: new Date().toISOString(), source: { kind: "web_ui" } };
      const accepted = await f.api("/v1/messages", envelope);
      expect([200, 201, 202], JSON.stringify(accepted.body)).toContain(accepted.status);
      return envelope;
    };
    f.setReply({ tool: "context_reset", args: { reason: "regression session replacement", window: 20 } }, true);
    await send("Reset context before continuing mailbox work");
    await expect.poll(() => f.requests).toBe(2);
    const pending = { id: randomUUID(), from: "user", to: name, body: "reserved-before-reset", created_at: new Date().toISOString(), source: { kind: "web_ui" } };
    // Given: accept, resolve and reserve through public Store APIs while the
    // reset response is gated at the external model boundary. REST acceptance
    // here would steer pending mail into the busy turn instead of leaving an
    // unstarted reservation. This helper touches only this fixture's database;
    // it never resets ownership, edits queue rows, or repairs a failed assertion.
    const binding = f.db.prepare("SELECT agent_id AS AgentID,session_id AS SessionID,epoch AS Epoch,generation AS Generation,owner_token AS OwnerToken FROM embedded_sessions WHERE agent_id=? AND current=1").get(name)!;
    const moduleRoot = resolve(import.meta.dirname, "../../router-proxy");
    const helper = await mkdtemp(join(moduleRoot, ".e2e-reserve-"));
    try {
      await writeFile(join(helper, "main.go"), `package main
import (
  "context"
  "encoding/json"
  "os"
  "github.com/reflexivity/mecha-developer/router-proxy/internal/store"
  "github.com/reflexivity/mecha-developer/router-proxy/internal/protocol"
)
func main() {
  ctx := context.Background()
  s, err := store.Open(ctx, os.Args[1], store.Options{})
  if err != nil { panic(err) }
  defer s.Close()
  var binding store.EmbeddedBinding
  if err = json.Unmarshal([]byte(os.Args[2]), &binding); err != nil { panic(err) }
  var envelope protocol.Envelope
  if err = json.Unmarshal([]byte(os.Args[3]), &envelope); err != nil { panic(err) }
  if _, err = s.Enqueue(ctx, envelope); err != nil { panic(err) }
  target, err := s.ResolveMessageTarget(ctx, envelope.ID)
  if err != nil { panic(err) }
  if target == nil { panic("no target") }
  attempt, err := s.ReserveEmbeddedMail(ctx, binding, envelope.ID)
  if err != nil { panic(err) }
  if attempt == nil { panic("no reservation") }
}
`);
      await promisify(execFile)("go", ["run", join(helper, "main.go"), f.router.databasePath, JSON.stringify(binding), JSON.stringify(pending)], { cwd: moduleRoot, timeout: 30_000 });
    } finally { await rm(helper, { recursive: true, force: true }); }
    const attempt = f.db.prepare("SELECT * FROM embedded_attempts WHERE message_id=?").get(pending.id)!;
    expect(attempt.accepted_body).toBe(pending.body);
    expect(f.db.prepare("SELECT count(*) AS n FROM embedded_acks WHERE message_id=?").get(pending.id)!.n).toBe(0);
    // Give the reset wake its own response, so it cannot satisfy the mail reply.
    f.setReply({ text: "reset wake settled" }, true);
    f.release();
    await expect.poll(() => f.requests).toBe(3);
    f.setReply({ text: "recovered mailbox reply" });
    f.release();
    await expect.poll(() => f.db.prepare("SELECT session_epoch FROM managed_agents WHERE agent_id=?").get(name)!.session_epoch).toBeGreaterThan(Number(attempt.epoch));
    expect(f.db.prepare("SELECT runtime_generation FROM managed_agents WHERE agent_id=?").get(name)!.runtime_generation).toBe(attempt.generation);
    await expect.poll(() => f.db.prepare("SELECT count(*) AS n FROM embedded_acks WHERE message_id=?").get(pending.id)!.n,
      { message: "retired-epoch unstarted reservation must be consumed by successor" }).toBe(1);
    await expect(page.getByText("recovered mailbox reply", { exact: true })).toBeVisible();
    const recovered = f.db.prepare("SELECT * FROM embedded_attempts WHERE message_id=?").get(pending.id)!;
    // Normal reset may retire the reservation and issue a new attempt/key.
    // Accepted message identity, not internal attempt stability, is the contract.
    expect(recovered.message_id).toBe(pending.id);
    expect(recovered.message_incarnation_id).toBe(attempt.message_incarnation_id);
    expect(recovered.snapshot_id).toBe(attempt.snapshot_id);
    expect(recovered.accepted_digest).toBe(attempt.accepted_digest);
    expect(recovered.epoch).toBeGreaterThan(Number(attempt.epoch));
    expect(recovered.session_id).not.toBe(attempt.session_id);
    expect(recovered.generation).toBe(attempt.generation);
    expect(recovered.accepted_body).toBe(pending.body);
    expect(recovered.sender).toBe(pending.from);
    await expect.poll(() => replies.filter(text => text === "recovered mailbox reply").length).toBe(1);
    f.setReply({ text: "followup mailbox reply" });
    const later = await send("followup-after-reset");
    await expect(page.getByText("followup mailbox reply", { exact: true })).toBeVisible();
    await expect.poll(() => f.db.prepare("SELECT count(*) AS n FROM embedded_acks WHERE message_id IN (?,?)").get(pending.id, later.id)!.n).toBe(2);
    expect(f.db.prepare("SELECT count(*) AS n FROM embedded_attempts WHERE message_id IN (?,?)").get(pending.id, later.id)!.n).toBe(2);
    await expect.poll(() => replies.filter(text => text === "followup mailbox reply").length).toBe(1);
  } catch (error) { console.error(f.diagnostics); throw error; } finally { await f.close(); }
});
