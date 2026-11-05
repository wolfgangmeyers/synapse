import { test, expect, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import { rm } from "node:fs/promises";
import { resolve } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { promisify } from "node:util";
import { setupE2EHarness, type E2ETestHarness } from "./harness";

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, "../..");
const migration = "123_workflow_evidence_retention_guards";
const guards = [
  "embedded_goal_continuation_transition", "embedded_goal_receipt_immutable_delete",
  "embedded_goal_evaluation_immutable_delete", "embedded_goal_continuation_no_delete",
  "embedded_goal_snooze_call_no_delete", "embedded_goal_snooze_preference_no_delete",
];
const evidenceTables = [
  "embedded_goal_applied_receipts", "embedded_goal_evaluations", "embedded_goal_continuations",
  "embedded_goal_snooze_calls", "embedded_goal_snooze_preferences",
];

function database<T>(path: string, use: (db: DatabaseSync) => T): T {
  const db = new DatabaseSync(path);
  try { db.exec("PRAGMA foreign_keys=ON"); return use(db); }
  finally { db.close(); }
}

function savedRows(db: DatabaseSync) {
  return Object.fromEntries(["embedded_entries", ...evidenceTables].map(table => [table,
    db.prepare(`SELECT * FROM ${table} ORDER BY rowid`).all().map(row => {
      // Migration 123 adds this column; all previously stored fields must survive.
      const { retention_expires_at_ms, ...saved } = row;
      return saved;
    }),
  ]));
}

async function legacyDatabase(path: string, missing: string[] = []) {
  // Called only after the owned router has exited. Never replace a live file.
  for (const suffix of ["", "-wal", "-shm"]) await rm(path + suffix, { force: true });
  await exec("go", ["run", "./internal/store/testdata/retention-migration", path], { cwd: resolve(repo, "router-proxy") });
  return database(path, db => {
    const now = new Date().toISOString();
    db.prepare(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at)
      VALUES('migration-agent','migration-agent',?,'google','fixture-model','running','running','idle','embedded','session',1,'generation',1,?,?,?)`).run(repo, "a".repeat(64), now, now);
    db.exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current,owner_token) VALUES('migration-agent','session',1,'generation','root',1,1)`);
    for (const [seq, parent, kind, digest, body] of [
      [1, "genesis", "user", "saved-user-digest", '{"role":"user","content":"Keep my saved work"}'],
      [2, "saved-user-digest", "assistant", "saved-final-digest", '{"role":"assistant","content":[]}'],
    ] as const) {
      db.prepare(`INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest)
        VALUES('migration-agent','session',1,'generation',?,?,?,?,?)`).run(seq, parent, kind, Buffer.from(body), digest);
    }
    db.prepare(`INSERT INTO managed_history_incarnations(incarnation_id,agent_id,start_sequence,created_at) VALUES('migration-history','migration-agent',0,?)`).run(now);
    db.prepare(`INSERT INTO managed_agent_policies(agent_id,history_scope_id,desired_revision,applied_revision,goal_enabled,goal_text,settings_json,created_at,updated_at)
      VALUES('migration-agent','migration-history',1,1,1,'Saved goal','{}',?,?)`).run(now, now);
    db.prepare(`INSERT INTO embedded_goal_applied_receipts(turn_key,agent_id,session_id,epoch,generation,role,turn_entry_seq,turn_entry_digest,goal_revision,goal_enabled,goal_text,applied_at)
      VALUES('saved-turn','migration-agent','session',1,'generation','root',1,'saved-user-digest',1,1,'Saved goal',?)`).run(now);
    db.prepare(`INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,claimed_at)
      VALUES('migration-agent','session',1,'generation',1,'saved-user-digest',1,1,2,'saved-final-digest','UNKNOWN',?)`).run(now);
    db.prepare(`UPDATE embedded_goal_evaluations SET state='NOT_DONE',completed_at=?`).run(now);
    db.prepare(`INSERT INTO embedded_goal_continuations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,state,disposition,reserved_at,due_at)
      VALUES('migration-agent','session',1,'generation',1,'saved-user-digest',1,'reserved','pending',?,'2099-01-01T00:00:00Z')`).run(now);
    db.exec(`INSERT INTO embedded_goal_snooze_calls(call_id,agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,args_json,result_json,policy_version)
      VALUES('saved-call','migration-agent','session',1,'generation','root',1,'saved-user-digest',1,'{}','{}',1);
      INSERT INTO embedded_goal_snooze_preferences(agent_id,session_id,epoch,generation,role,user_seq,user_digest,goal_revision,policy_version,call_id,due_at)
      VALUES('migration-agent','session',1,'generation','root',1,'saved-user-digest',1,1,'saved-call','2099-01-01T00:00:00Z')`);
    for (const guard of missing) db.exec(`DROP TRIGGER ${guard}`);
    expect(db.prepare("PRAGMA foreign_key_check").all()).toEqual([]);
    return savedRows(db);
  });
}

function upgraded(path: string) {
  return database(path, db => {
    expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all(migration)).toHaveLength(1);
    expect(db.prepare(`SELECT name FROM sqlite_master WHERE type='trigger' AND name IN (${guards.map(() => "?").join(",")})`).all(...guards)).toHaveLength(6);
    expect(db.prepare("PRAGMA foreign_key_check").all()).toEqual([]);
    for (const table of evidenceTables) {
      expect(db.prepare(`SELECT count(*) AS n FROM ${table}`).get()?.n, table).toBe(1);
      expect(() => db.exec(`DELETE FROM ${table}`)).toThrow(/retention evidence is immutable while protected/);
    }
    expect(() => db.exec("UPDATE embedded_goal_continuations SET state=state")).toThrow(/continuation transition invalid/);
    return {
      rows: savedRows(db),
      deadlines: db.prepare("SELECT retention_expires_at_ms FROM embedded_goal_continuations").all(),
      sessions: db.prepare("SELECT legacy_evidence_expires_at_ms FROM embedded_sessions").all(),
    };
  });
}

async function browserConnection(page: Page, f: E2ETestHarness) {
  const url = f.proxy!.url;
  await page.addInitScript(url => {
    localStorage.setItem("mecha.web.activeRouter", url);
    localStorage.setItem("mecha.web.routers", JSON.stringify([url]));
  }, url);
  await page.goto(f.frontend.url);
  const observed = await page.evaluate(async url => {
    const health = await fetch(url + "/v1/health", { credentials: "omit" }).then(r => r.json());
    const registered = await new Promise<string>((resolve, reject) => {
      const ws = new WebSocket(url.replace(/^http/, "ws") + "/v1/agents");
      const timer = setTimeout(() => { ws.close(); reject(Error("registration timed out")); }, 5000);
      ws.onerror = () => { clearTimeout(timer); ws.close(); reject(Error("WebSocket failed")); };
      ws.onopen = () => ws.send(JSON.stringify({ version: "v1", frame_id: "migration-register", type: "register", agent_id: "migration-browser", instance_id: "migration-instance", agent_role: "agent", client_type: "test", ack_confirmation: "correlated_v1", conversation_sync: "v1", delivery_receipts: "delivery_items_v1" }));
      ws.onmessage = event => {
        const frame = JSON.parse(event.data);
        if (frame.type === "registered") { clearTimeout(timer); ws.close(); resolve(frame.type); }
      };
    });
    return { readiness: health.readiness, registered };
  }, url);
  expect(observed).toEqual({ readiness: "ok", registered: "registered" });
  expect(f.mockLLM.getHistory()).toEqual([]);
}

let f: E2ETestHarness;
test.beforeEach(async () => { f = await setupE2EHarness({ useProxy: true }); });
test.afterEach(async () => { await f?.close(); });

test("upgrades missing guards without losing evidence and reopens safely", async ({ page }) => {
  let before: ReturnType<typeof savedRows>;
  await f.router.restart(async path => { before = await legacyDatabase(path, guards); });
  const first = upgraded(f.router.databasePath);
  expect(first.rows).toEqual(before!);
  await browserConnection(page, f);
  const cutoff = () => database(f.router.databasePath, db => Number(db.prepare("SELECT evidence_expired_through_ms FROM embedded_sessions").get()?.evidence_expired_through_ms));
  const beforeCutoff = cutoff();
  await f.router.restart();
  expect(upgraded(f.router.databasePath)).toEqual(first);
  // Cleanup's clock fence advances on reopen; saved admission/deadlines do not.
  expect(cutoff()).toBeGreaterThanOrEqual(beforeCutoff);
  await browserConnection(page, f);
});

test("starts with intact guards or a fresh database", async ({ page }) => {
  database(f.router.databasePath, db => {
    expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all(migration)).toHaveLength(1);
    expect(db.prepare(`SELECT name FROM sqlite_master WHERE type='trigger' AND name IN (${guards.map(() => "?").join(",")})`).all(...guards)).toHaveLength(6);
  });
  await browserConnection(page, f);
  let before: ReturnType<typeof savedRows>;
  await f.router.restart(async path => { before = await legacyDatabase(path); });
  expect(upgraded(f.router.databasePath).rows).toEqual(before!);
  await browserConnection(page, f);
});

test("rolls back unrelated migration errors and retries normally", async ({ page }) => {
  let before: ReturnType<typeof savedRows>;
  let transition: unknown;
  await expect(f.router.restart(async path => {
    before = await legacyDatabase(path);
    database(path, db => {
      transition = db.prepare("SELECT sql FROM sqlite_master WHERE name='embedded_goal_continuation_transition'").get()?.sql;
      db.exec("CREATE VIEW embedded_retention_turns AS SELECT 1 AS conflict");
    });
  })).rejects.toThrow(/view embedded_retention_turns already exists/);
  database(f.router.databasePath, db => {
    expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all(migration)).toEqual([]);
    expect(db.prepare("SELECT name FROM pragma_table_info('embedded_sessions') WHERE name='legacy_evidence_expires_at_ms'").all()).toEqual([]);
    expect(db.prepare("SELECT name FROM pragma_table_info('embedded_goal_continuations') WHERE name='retention_expires_at_ms'").all()).toEqual([]);
    expect(db.prepare("SELECT sql FROM sqlite_master WHERE name='embedded_goal_continuation_transition'").get()?.sql).toBe(transition);
    expect(savedRows(db)).toEqual(before!);
    db.exec("DROP VIEW embedded_retention_turns");
  });
  await f.router.restart();
  expect(upgraded(f.router.databasePath).rows).toEqual(before!);
  await browserConnection(page, f);
});
