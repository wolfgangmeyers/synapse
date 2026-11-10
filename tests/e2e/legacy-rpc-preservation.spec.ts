import { test, expect } from "@playwright/test";
import { DatabaseSync } from "node:sqlite";
import { createHash } from "node:crypto";
import { readFile, readdir, writeFile, rm } from "node:fs/promises";
import { join, resolve } from "node:path";
import { claudeWorkersHarness } from "./claude-workers-harness";

const hash = (value: string) => createHash("sha256").update(value).digest("hex");

// Compare every durable row and schema object, not just selected legacy fields.
// Sorting serialized rows also handles tables without a stable natural order.
function snapshot(path: string) {
  const db = new DatabaseSync(path, { readOnly: true });
  try {
    const schema = db.prepare("SELECT type,name,tbl_name,sql FROM sqlite_schema ORDER BY type,name").all();
    const tables = db.prepare("SELECT name FROM sqlite_schema WHERE type='table' AND (name NOT LIKE 'sqlite_%' OR name='sqlite_sequence') ORDER BY name").all();
    const data = Object.fromEntries(tables.map(({ name }) => {
      const table = String(name);
      return [table, db.prepare(`SELECT * FROM "${table.replaceAll('"', '""')}"`).all().map(row => JSON.stringify(row)).sort()];
    }));
    return { schema, data, userVersion: db.prepare("PRAGMA user_version").get() };
  } finally { db.close(); }
}

const insertAgent = `INSERT INTO managed_agents(agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,session_epoch,manifest_revision,manifest_hash,created_at,updated_at,runtime,parent_agent_id)
 VALUES(?,?,1,'/tmp','p','m','running',?, ?,1,?,3,1,'hash','2020','2020',?,?)`;
const insertOperation = `INSERT INTO managed_http_operations(operation_id,session_identity_hash,method,canonical_path,request_id,canonical_body_hash,state,response_status,response_headers_json,response_body,created_at,terminal_at,expires_at)
 VALUES(?, 'identity','POST','/v1/managed-agents/legacy/restart',?,'hash',?,0,'{}','','2020',?,?)`;

test.describe("legacy RPC preservation fence", () => {
  test.setTimeout(90_000);

  for (const scenario of ["mixed-child", "terminal-only"] as const) {
    test(`rejects ${scenario} records before migration or recovery and preserves the linked session`, async ({ page }) => {
      const f = await claudeWorkersHarness();
      const launch = "legacy-launch";
      const relative = `${hash(launch)}.jsonl`;
      const sessionPath = join(f.router.root, "managed-home/sessions", relative);
      const session = '{"type":"session","id":"session-original"}\n{"type":"message","content":"unfinished"}\n';
      let before: ReturnType<typeof snapshot>;
      try {
        await f.open(page);
        await writeFile(sessionPath, session);
        await expect(f.router.restart(async path => {
          const db = new DatabaseSync(path);
          try {
            if (scenario === "mixed-child") {
              db.prepare(insertAgent).run("embedded-parent", "embedded-parent", "running", "idle", "parent-session", "embedded", null);
              db.prepare(insertAgent).run("legacy-child", "legacy-child", "running", "idle", "session-original", "rpc", "embedded-parent");
            } else {
              db.prepare(insertAgent).run("legacy-terminal", "legacy-terminal", "terminated", "stopped", "session-original", "rpc", null);
            }
            const legacy = scenario === "mixed-child" ? "legacy-child" : "legacy-terminal";
            db.prepare(insertOperation).run("legacy-pending", "pending-request", "pending", null, null);
            db.prepare(insertOperation).run("legacy-expired", "expired-request", "terminal", "2020", "2020");
            // Stock Pi's durable reservation binds the launch/session to its
            // real root-relative filename, rather than an unrelated sentinel.
            db.prepare(`INSERT INTO managed_stock_pi_session_reservations(reservation_id,agent_id,session_epoch,launch_operation_id,root_relative_path,path_sha256,pi_session_id,state,created_at,updated_at)
              VALUES('reservation',?,3,?,?,?,'session-original','bound','2020','2020')`).run(legacy, launch, relative, hash(relative));
            // A genuine pending migration would clamp this value if startup
            // opened the store before applying the preservation fence.
            db.exec("DELETE FROM schema_migrations WHERE id='127_conversation_receipt_cap_ceiling'; UPDATE conversation_send_receipt_capacity SET max_bytes=314572800 WHERE singleton=1");
          } finally { db.close(); }
          before = snapshot(path);
        })).rejects.toThrow(new RegExp(`unsupported legacy RPC agent legacy-${scenario === "mixed-child" ? "child" : "terminal"}`));
        expect(snapshot(f.router.databasePath)).toEqual(before!);
        expect(await readFile(sessionPath, "utf8")).toBe(session);
        await expect(f.router.restart()).rejects.toThrow(/unsupported legacy RPC agent legacy-/);
        expect(snapshot(f.router.databasePath)).toEqual(before!);
        expect(await readFile(sessionPath, "utf8")).toBe(session);
      } finally { await f.close(); }
    });
  }

  test("rejects a pre-runtime-column database before applying old migrations", async ({ page }) => {
    const f = await claudeWorkersHarness();
    let before: ReturnType<typeof snapshot>;
    try {
      await f.open(page);
      await expect(f.router.restart(async path => {
        // Recreate a sequential 001–022 installation before migration 065
        // added runtime. Apply the real migration SQL and matching ledger.
        await rm(path, { force: true });
        await rm(`${path}-wal`, { force: true });
        await rm(`${path}-shm`, { force: true });
        const migrations = resolve(import.meta.dirname, "../../router-proxy/internal/store/migrations");
        const files = (await readdir(migrations)).filter(name => /^0(?:0[1-9]|1[0-9]|2[0-2])_.*\.sql$/.test(name)).sort();
        expect(files).toHaveLength(22);
        const db = new DatabaseSync(path);
        try {
          db.exec("PRAGMA foreign_keys=ON; CREATE TABLE schema_migrations(id TEXT PRIMARY KEY, applied_at TEXT NOT NULL)");
          for (const file of files) {
            db.exec("BEGIN IMMEDIATE");
            db.exec(await readFile(join(migrations, file), "utf8"));
            db.prepare("INSERT INTO schema_migrations VALUES(?, '2020')").run(file.slice(0, -4));
            db.exec("COMMIT");
          }
          db.exec(`INSERT INTO agents(agent_id,first_seen_at) VALUES('old-legacy','2020');
            INSERT INTO messages(id,sender,recipient,body,created_at,expires_at,state,message_incarnation_id) VALUES('old-message','sender','old-legacy','unfinished','2020','2099','queued','old-incarnation');
            INSERT INTO managed_agents(agent_id,name,slot_held,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,accepting_prompts,pi_session_id,session_epoch,manifest_revision,manifest_hash,created_at,updated_at)
            VALUES('old-legacy','old-legacy',1,'/tmp','p','m','running','running','idle',1,'old-session',3,1,'hash','2020','2020');`);
          db.prepare(insertOperation).run("legacy-pending", "pending-request", "pending", null, null);
        } finally { db.close(); }
        before = snapshot(path);
        expect(before.data.sqlite_sequence).toEqual([JSON.stringify({ name: "messages", seq: 1 })]);
      })).rejects.toThrow(/unsupported legacy RPC agent old-legacy/);
      expect(snapshot(f.router.databasePath)).toEqual(before!);
      await expect(f.router.restart()).rejects.toThrow(/unsupported legacy RPC agent old-legacy/);
      expect(snapshot(f.router.databasePath)).toEqual(before!);
    } finally { await f.close(); }
  });

  test("malformed database fails closed without rewriting its contents", async ({ page }) => {
    const f = await claudeWorkersHarness();
    const malformed = Buffer.from("not a SQLite database: legacy operator data\n");
    try {
      await f.open(page);
      await expect(f.router.restart(async path => {
        await rm(`${path}-wal`, { force: true });
        await rm(`${path}-shm`, { force: true });
        await writeFile(path, malformed);
      })).rejects.toThrow(/legacy RPC preflight.*(not a database|malformed)/i);
      expect(await readFile(f.router.databasePath)).toEqual(malformed);
      await expect(f.router.restart()).rejects.toThrow(/legacy RPC preflight.*(not a database|malformed)/i);
      expect(await readFile(f.router.databasePath)).toEqual(malformed);
    } finally { await f.close(); }
  });

  test("embedded agent recovers normally when no legacy RPC record exists", async ({ page }) => {
    const f = await claudeWorkersHarness({ turns_by_input: [
      { contains: "after restart", steps: [{ text: "recovered response" }] },
    ] });
    try {
      await f.open(page);
      const name = await f.launch(page);
      await f.idle(name);
      expect(f.tableRows("managed_agents", name)[0].runtime).toBe("embedded");
      await f.restart();
      await page.reload();
      await expect(page.locator(`[data-agent-id="${name}"]`)).toBeVisible();
      await page.locator(`[data-agent-id="${name}"]`).click();
      await f.userTurn(page, "after restart");
      await f.final(page, name, "recovered response");
      expect(f.tableRows("managed_agents", name)[0].runtime).toBe("embedded");
    } finally { await f.close(); }
  });
});
