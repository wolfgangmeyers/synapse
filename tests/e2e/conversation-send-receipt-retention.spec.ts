import { expect, test, type Page } from "@playwright/test";
import { readFile } from "node:fs/promises";
import { DatabaseSync } from "node:sqlite";
import { setupE2EHarness, type E2ETestHarness } from "./harness";

const horizon = 30 * 60 * 1000;
const migration = "126_conversation_receipt_thirty_minutes";
const createdAt = "2026-01-01T00:00:00.000Z";

type Envelope = { id: string; from: string; to: string; body: string; created_at: string };
function envelope(id: string, to: string, body: string): Envelope {
  return { id, from: "receipt-browser", to, body, created_at: createdAt };
}
function database<T>(path: string, action: (db: DatabaseSync) => T): T {
  const db = new DatabaseSync(path);
  try { db.exec("PRAGMA busy_timeout=5000; PRAGMA foreign_keys=ON"); return action(db); }
  finally { db.close(); }
}
function counts(path: string) {
  return database(path, db => ({
    capacity: db.prepare("SELECT used_count,used_bytes,max_count,max_bytes FROM conversation_send_receipt_capacity").get()!,
    actual: db.prepare("SELECT count(*) AS count,coalesce(sum(length(CAST(message_id AS BLOB))+length(CAST(conversation_json AS BLOB))+length(CAST(delivery_json AS BLOB))),0) AS bytes FROM conversation_send_receipts").get()!,
    messages: db.prepare("SELECT id,state,message_incarnation_id FROM messages ORDER BY id").all(),
    history: db.prepare("SELECT message_id,message_incarnation_id FROM conversation_messages ORDER BY conversation_sequence").all(),
  }));
}
async function post(page: Page, url: string, message: Envelope) {
  return page.evaluate(async ({ url, message }) => {
    const response = await fetch(url + "/v1/messages", { method: "POST", credentials: "omit", headers: { "Content-Type": "application/json" }, body: JSON.stringify(message) });
    return { status: response.status, body: await response.json() };
  }, { url, message });
}
async function connect(page: Page, url: string, agent: string, autoAck: boolean) {
  await page.evaluate(async ({ url, agent, autoAck }) => {
    const state = window as typeof window & { receiptFrames: any[]; receiptSocket: WebSocket };
    state.receiptFrames = [];
    const socket = new WebSocket(url.replace(/^http/, "ws") + "/v1/agents");
    state.receiptSocket = socket;
    await new Promise<void>((resolve, reject) => {
      const timeout = setTimeout(() => reject(new Error("WebSocket registration timeout")), 5000);
      let lease = "";
      socket.onerror = () => reject(new Error("WebSocket error"));
      socket.onopen = () => socket.send(JSON.stringify({ version: "v1", type: "register", frame_id: "register", agent_id: agent, instance_id: "receipt-instance", agent_role: "agent", client_type: "test", ack_confirmation: "correlated_v1", delivery_receipts: "delivery_items_v1" }));
      socket.onmessage = event => {
        const frame = JSON.parse(event.data);
        state.receiptFrames.push(frame);
        if (frame.type === "registered") { lease = frame.lease_id; clearTimeout(timeout); resolve(); }
        if (frame.type === "message" && autoAck) {
          const receipt = { version: "v1", agent_id: agent, instance_id: "receipt-instance", lease_id: lease, message_id: frame.message.id, attempt_number: frame.attempt_number, ...(frame.delivery_items ? { delivery_items: frame.delivery_items } : {}) };
          socket.send(JSON.stringify({ ...receipt, type: "injection_requested", frame_id: `inject-${frame.message.id}` }));
          socket.send(JSON.stringify({ ...receipt, type: "ack", frame_id: `ack-${frame.message.id}` }));
        }
      };
    });
  }, { url, agent, autoAck });
}
async function observed(page: Page, type: string) {
  return page.evaluate(type => (window as typeof window & { receiptFrames: any[] }).receiptFrames.filter(frame => frame.type === type).length, type);
}

let f: E2ETestHarness;
test.beforeEach(async ({ page }) => {
  f = await setupE2EHarness({ useProxy: true });
  await page.goto(f.frontend.url);
});
test.afterEach(async () => { await f?.close(); });

test("replays through the proxy and permits reuse after expiry", async ({ page }) => {
  const url = f.proxy!.url;
  await connect(page, url, "receipt-target", true);
  const message = envelope("acknowledged", "receipt-target", "original");
  const first = await post(page, url, message);
  expect(first.status).toBe(201);
  await expect.poll(() => observed(page, "delivery_acknowledged")).toBe(1);
  expect((await post(page, url, message)).body.conversation_message).toEqual(first.body.conversation_message);
  expect((await post(page, url, { ...message, body: "changed" })).body.error.code).toBe("idempotency_conflict");
  await f.advanceClock(horizon - 1);
  expect((await post(page, url, message)).body.conversation_message).toEqual(first.body.conversation_message);
  await f.advanceClock(1);
  const late = await post(page, url, { ...message, body: "new effect" });
  expect(late.status).toBe(201);
  expect(late.body.message.message_incarnation_id).not.toBe(first.body.message.message_incarnation_id);
  const persisted = counts(f.router.databasePath);
  expect(persisted.history.filter(row => row.message_id === message.id)).toHaveLength(2);
  expect(persisted.messages.filter(row => row.id === message.id)).toHaveLength(1);
  expect(f.mockLLM.getHistory()).toEqual([]);
});

test("cleans unresolved and alias receipts without deleting deliveries", async ({ page }) => {
  const url = f.proxy!.url;
  await connect(page, url, "receipt-observer", false);
  const a = envelope("shared-a", "unresolved-target", "shared body");
  const alias = { ...a, id: "shared-b" };
  const dead = envelope("dead-letter", "dead-target", "dead body");
  expect((await post(page, url, a)).status).toBe(201);
  const dedup = await post(page, url, alias);
  expect(dedup.status).toBe(200);
  expect(dedup.body.message.id).toBe(a.id);
  expect((await post(page, url, dead)).status).toBe(201);
  // Fail unrelated lifecycle cleanup, proving receipts do not depend on it.
  // The database and trigger belong only to this disposable test router.
  database(f.router.databasePath, db => {
    db.exec(`UPDATE messages SET state='dead_letter' WHERE id='dead-letter';
      INSERT INTO agents(agent_id,first_seen_at) VALUES('receipt-stale-fixture','2000-01-01T00:00:00.000Z');
      CREATE TRIGGER receipt_fail_unrelated_sweep BEFORE DELETE ON agents WHEN OLD.agent_id='receipt-stale-fixture' BEGIN SELECT RAISE(ABORT,'injected unrelated sweep failure'); END;
      CREATE TRIGGER receipt_fail_cleanup BEFORE DELETE ON conversation_send_receipts BEGIN SELECT RAISE(ABORT,'injected receipt cleanup failure'); END`);
  });
  const before = counts(f.router.databasePath);
  expect(before.capacity.used_count).toBe(3);
  await f.advanceClock(horizon);
  await expect.poll(async () => (await readFile(f.router.logPath, "utf8")).includes('"conversation_receipt_cleanup_failed"'), { timeout: 5000 }).toBe(true);
  expect(counts(f.router.databasePath).capacity.used_count).toBe(3);
  database(f.router.databasePath, db => db.exec("DROP TRIGGER receipt_fail_cleanup"));
  // No send or successful lifecycle sweep is needed to release all receipts.
  await expect.poll(() => counts(f.router.databasePath).capacity.used_count, { timeout: 5000 }).toBe(0);
  const after = counts(f.router.databasePath);
  expect(after.capacity.used_bytes).toBe(0);
  expect(after.actual).toEqual({ count: 0, bytes: 0 });
  expect(after.messages).toEqual(before.messages);
  expect(after.history).toEqual(before.history);
  expect((await post(page, url, a)).status).toBe(200); // live ID still owns its delivery
  expect((await post(page, url, { ...a, body: "changed" })).body.error.code).toBe("idempotency_conflict");
  const lateAlias = await post(page, url, alias);
  expect(lateAlias.status).toBe(200);
  expect(lateAlias.body.message.id).toBe(a.id);
  expect(counts(f.router.databasePath).history.filter(row => row.message_id === alias.id)).toHaveLength(2);
  await expect.poll(async () => (await readFile(f.router.logPath, "utf8")).includes('"sweep_failed"')).toBe(true);
});

test("recovers count and byte capacity without partial sends", async ({ page }) => {
  const url = f.proxy!.url;
  await connect(page, url, "capacity-observer", false);
  database(f.router.databasePath, db => db.exec("UPDATE conversation_send_receipt_capacity SET max_count=1"));
  const first = envelope("cap-one", "cap-target-1", "é first");
  expect((await post(page, url, first)).status).toBe(201);
  const rejected = envelope("cap-rejected", "cap-target-2", "different");
  let failure = await post(page, url, rejected);
  expect(failure.status).toBe(507);
  expect(failure.body.error.code).toBe("conversation_receipt_capacity");
  expect((await post(page, url, first)).status).toBe(200);
  let state = counts(f.router.databasePath);
  expect(state.messages.some(row => row.id === rejected.id)).toBe(false);
  expect(state.history.some(row => row.message_id === rejected.id)).toBe(false);
  expect(state.capacity.used_bytes).toBe(state.actual.bytes);
  await expect.poll(async () => (await readFile(f.router.logPath, "utf8")).includes('"conversation_receipt_capacity"')).toBe(true);
  await f.advanceClock(horizon);
  expect((await post(page, url, rejected)).status).toBe(201);
  state = counts(f.router.databasePath);
  expect(state.messages.map(row => row.id)).toEqual(expect.arrayContaining([first.id, rejected.id]));
  database(f.router.databasePath, db => db.prepare("UPDATE conversation_send_receipt_capacity SET max_count=100,max_bytes=?").run(state.capacity.used_bytes));
  const byteRejected = envelope("byte-rejected", "cap-target-3", "é byte charge");
  failure = await post(page, url, byteRejected);
  expect(failure.status).toBe(507);
  expect(failure.body.error.code).toBe("conversation_receipt_capacity");
  state = counts(f.router.databasePath);
  expect(state.capacity.used_count).toBe(state.actual.count);
  expect(state.capacity.used_bytes).toBe(state.actual.bytes);
  expect(state.messages.some(row => row.id === byteRejected.id)).toBe(false);
  await f.advanceClock(horizon);
  const smaller = envelope("b", "x", "x");
  expect((await post(page, url, smaller)).status).toBe(201);
  state = counts(f.router.databasePath);
  expect(state.capacity.used_count).toBe(state.actual.count);
  expect(state.capacity.used_bytes).toBe(state.actual.bytes);
  // At 60 minutes the first delivery may separately expire under its own lifecycle.
  expect(state.messages.map(row => row.id)).toEqual(expect.arrayContaining([rejected.id, smaller.id]));
});

test("upgrades old deadlines and defaults without losing pending work", async ({ page }) => {
  const url = f.proxy!.url;
  await connect(page, url, "upgrade-observer", false);
  for (const id of ["upgrade-old", "upgrade-recent", "upgrade-short"]) expect((await post(page, url, envelope(id, id, id))).status).toBe(201);
  const before = counts(f.router.databasePath);
  const now = Date.now();
  const accepted = { "upgrade-old": new Date(now - 2 * horizon).toISOString(), "upgrade-recent": new Date(now - 5 * 60_000).toISOString(), "upgrade-short": new Date(now - 10 * 60_000).toISOString() };
  const shortDeadline = new Date(now + 5 * 60_000).toISOString();
  await f.router.restart(async path => database(path, db => {
    db.exec(`DELETE FROM schema_migrations WHERE id='${migration}'; UPDATE conversation_send_receipt_capacity SET max_count=17,max_bytes=67108864`);
    const update = db.prepare("UPDATE conversation_send_receipts SET accepted_at=?,deadline_at=?,conversation_json=json_set(conversation_json,'$.recorded_at',?) WHERE message_id=?");
    for (const [id, at] of Object.entries(accepted)) update.run(at, id === "upgrade-short" ? shortDeadline : new Date(now + 30 * 86400_000).toISOString(), at, id);
  }));
  let after = counts(f.router.databasePath);
  expect(after.capacity.max_count).toBe(17);
  expect(after.capacity.max_bytes).toBe(200 * 1024 * 1024);
  expect(after.capacity.used_count).toBe(2);
  expect(after.capacity.used_bytes).toBe(after.actual.bytes);
  expect(after.messages).toEqual(before.messages);
  expect(after.history).toEqual(before.history);
  const deadlines = () => database(f.router.databasePath, db => db.prepare("SELECT message_id,deadline_at FROM conversation_send_receipts ORDER BY message_id").all());
  expect(deadlines()).toEqual([
    { message_id: "upgrade-recent", deadline_at: new Date(Date.parse(accepted["upgrade-recent"]) + horizon).toISOString() },
    { message_id: "upgrade-short", deadline_at: shortDeadline },
  ]);
  const savedDeadlines = deadlines();
  await f.router.restart(async path => database(path, db => db.exec(`DELETE FROM schema_migrations WHERE id='${migration}'; UPDATE conversation_send_receipt_capacity SET max_bytes=12345678`)));
  after = counts(f.router.databasePath);
  expect(after.capacity.max_bytes).toBe(12345678);
  expect(deadlines()).toEqual(savedDeadlines);
  expect(after.messages).toEqual(before.messages);
  expect(after.history).toEqual(before.history);
  expect(database(f.router.databasePath, db => db.prepare("SELECT count(*) AS n FROM schema_migrations WHERE id=?").get(migration)!.n)).toBe(1);
  expect((await post(page, url, envelope("upgrade-new", "new-target", "after upgrade"))).status).toBe(201);
});
