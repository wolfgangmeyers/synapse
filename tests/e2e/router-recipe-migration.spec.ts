import { test, expect, type Page } from "@playwright/test";
import { execFile } from "node:child_process";
import { rm } from "node:fs/promises";
import { resolve } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { promisify } from "node:util";
import { setupE2EHarness, type E2ETestHarness } from "./harness";

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, "../..");
const migration = "131_optional_retired_rpc_recipe";

function database<T>(path: string, use: (db: DatabaseSync) => T): T {
  const db = new DatabaseSync(path);
  try { db.exec("PRAGMA foreign_keys=ON"); return use(db); }
  finally { db.close(); }
}

function saved(db: DatabaseSync) {
  return {
    recipes: db.prepare("SELECT rowid AS saved_rowid,* FROM managed_launch_recipes ORDER BY rowid").all(),
    entries: db.prepare("SELECT * FROM embedded_entries ORDER BY rowid").all(),
    debt: db.prepare('SELECT * FROM pragma_foreign_key_check ORDER BY "table",rowid,parent,fkid').all(),
  };
}

async function legacyDatabase(path: string, orphaned = false) {
  // The harness joins the old process before replacing any SQLite files.
  for (const suffix of ["", "-wal", "-shm"]) await rm(path + suffix, { force: true });
  await exec("go", ["run", "./internal/store/testdata/recipe-migration", path], { cwd: resolve(repo, "router-proxy") });
  return database(path, db => {
    const now = new Date().toISOString();
    db.prepare(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at)
      VALUES('recipe-agent','recipe-agent',?,'openai-codex','fixture-model','running','running','idle','embedded','session',1,'generation',1,?,?,?)`).run(repo, "a".repeat(64), now, now);
    db.prepare(`INSERT INTO managed_launch_recipes(rowid,agent_id,pi_executable,session_root,cwd,provider,model_id,mecha_rpc_extension,pi_subagents_extension,cursor_extension,test_extensions_json,environment_json,recipe_sha256,created_at,updated_at,antigravity_extension)
      VALUES(41,'recipe-agent','/fixture/pi','/fixture/sessions',?,'openai-codex','fixture-model','/fixture/retired.ts','/fixture/subagents.ts','','[]','[]',?,?,?,'')`).run(repo, "b".repeat(64), now, now);
    db.exec(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current,owner_token) VALUES('recipe-agent','session',1,'generation','root',1,1)`);
    db.prepare(`INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest)
      VALUES('recipe-agent','session',1,'generation',1,'genesis','user',?,'saved-recipe-entry')`).run(Buffer.from('{"role":"user","content":"Keep this saved conversation"}'));
    if (orphaned) {
      db.exec(`PRAGMA foreign_keys=OFF;
        INSERT INTO managed_launch_recipes SELECT 'missing-agent',pi_executable,session_root,cwd,provider,model_id,mecha_rpc_extension,pi_subagents_extension,cursor_extension,test_extensions_json,environment_json,recipe_sha256,created_at,updated_at,antigravity_extension FROM managed_launch_recipes;
        UPDATE managed_launch_recipes SET rowid=99 WHERE agent_id='missing-agent';
        CREATE TABLE historical_recipe_debt(agent_id TEXT REFERENCES managed_agents(agent_id));
        INSERT INTO historical_recipe_debt VALUES('other-missing-agent');
        PRAGMA foreign_keys=ON`);
    }
    return saved(db);
  });
}

function upgraded(path: string) {
  return database(path, db => {
    expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all(migration)).toHaveLength(1);
    expect(db.prepare("SELECT name FROM sqlite_master WHERE type='index' AND name='managed_launch_recipes_hash_idx'").all()).toHaveLength(1);
    expect(db.prepare("PRAGMA foreign_key_list(managed_launch_recipes)").get()).toMatchObject({ table: "managed_agents", from: "agent_id", to: "agent_id", on_delete: "CASCADE" });
    expect(() => db.exec("UPDATE managed_launch_recipes SET agent_id='new-missing-agent' WHERE rowid=41")).toThrow(/FOREIGN KEY constraint failed/);
    return saved(db);
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
      ws.onopen = () => ws.send(JSON.stringify({ version: "v1", frame_id: "recipe-register", type: "register", agent_id: "recipe-browser", instance_id: "recipe-instance", agent_role: "agent", client_type: "test", ack_confirmation: "correlated_v1", conversation_sync: "v1", delivery_receipts: "delivery_items_v1" }));
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

test("preserves orphaned recipes and relationship debt across upgrade and restart", async ({ page }) => {
  let before: ReturnType<typeof saved>;
  await f.router.restart(async path => { before = await legacyDatabase(path, true); });
  expect(before!.debt).toHaveLength(2);
  expect(upgraded(f.router.databasePath)).toEqual(before!);
  await browserConnection(page, f);
  await f.router.restart();
  expect(upgraded(f.router.databasePath)).toEqual(before!);
  await browserConnection(page, f);
});

test("starts fresh and upgrades healthy recipes without weakening constraints", async ({ page }) => {
  database(f.router.databasePath, db => {
    expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all(migration)).toHaveLength(1);
  });
  await browserConnection(page, f);
  let before: ReturnType<typeof saved>;
  await f.router.restart(async path => { before = await legacyDatabase(path); });
  expect(upgraded(f.router.databasePath)).toEqual(before!);
  database(f.router.databasePath, db => {
    db.exec("UPDATE managed_launch_recipes SET mecha_rpc_extension='' WHERE rowid=41");
    expect(db.prepare("SELECT mecha_rpc_extension FROM managed_launch_recipes WHERE rowid=41").get()?.mecha_rpc_extension).toBe("");
    for (const assignment of ["mecha_rpc_extension=NULL", "mecha_rpc_extension='" + "x".repeat(4097) + "'", "pi_subagents_extension=''", "recipe_sha256='bad'"]) {
      expect(() => db.exec(`UPDATE managed_launch_recipes SET ${assignment} WHERE rowid=41`)).toThrow(/constraint failed/);
    }
    // Use a separate minimal parent to exercise cascading deletes without
    // disturbing the retained conversation or its other protected children.
    db.prepare(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at)
      SELECT 'cascade-agent','cascade-agent',cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,'cascade-session',session_epoch,'cascade-generation',manifest_revision,manifest_hash,created_at,updated_at FROM managed_agents WHERE agent_id='recipe-agent'`).run();
    db.exec(`INSERT INTO managed_launch_recipes SELECT 'cascade-agent',pi_executable,session_root,cwd,provider,model_id,mecha_rpc_extension,pi_subagents_extension,cursor_extension,test_extensions_json,environment_json,recipe_sha256,created_at,updated_at,antigravity_extension FROM managed_launch_recipes WHERE rowid=41;
      DELETE FROM managed_agents WHERE agent_id='cascade-agent'`);
    expect(db.prepare("SELECT agent_id FROM managed_launch_recipes WHERE agent_id='cascade-agent'").all()).toEqual([]);
  });
  await browserConnection(page, f);
});

test("rolls back a recipe rebuild conflict and retries normally", async ({ page }) => {
  let before: ReturnType<typeof saved>;
  let oldSchema: unknown;
  await expect(f.router.restart(async path => {
    before = await legacyDatabase(path, true);
    database(path, db => {
      oldSchema = db.prepare("SELECT sql FROM sqlite_master WHERE name='managed_launch_recipes'").get()?.sql;
      db.exec("DROP INDEX managed_launch_recipes_hash_idx; CREATE TABLE managed_launch_recipes_hash_idx(conflict TEXT)");
    });
  })).rejects.toThrow(/exited prematurely/);
  expect(f.router.stderrLineContains(migration, "managed_launch_recipes_hash_idx")).toBe(true);
  database(f.router.databasePath, db => {
    expect(saved(db)).toEqual(before!);
    expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all(migration)).toEqual([]);
    expect(db.prepare("SELECT name FROM sqlite_master WHERE name='managed_launch_recipes_131'").all()).toEqual([]);
    expect(db.prepare("SELECT sql FROM sqlite_master WHERE name='managed_launch_recipes'").get()?.sql).toBe(oldSchema);
    db.exec("DROP TABLE managed_launch_recipes_hash_idx");
  });
  await f.router.restart();
  expect(upgraded(f.router.databasePath)).toEqual(before!);
  await browserConnection(page, f);
});
