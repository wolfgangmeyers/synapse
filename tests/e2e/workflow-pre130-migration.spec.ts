import { test, expect } from "@playwright/test";
import { execFile } from "node:child_process";
import { rm } from "node:fs/promises";
import { resolve } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { promisify } from "node:util";
import { setupE2EHarness } from "./harness";

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, "../..");
const migration = "130_workflow_binding_predecessor";

// IR-001: an actual populated pre-130 database is upgraded, never mined for
// a predecessor even when its retained history describes an A -> B transition.
test("IR-001: upgrade does not infer a predecessor from transition history", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await setupE2EHarness({ useProxy: true });
  try {
    await f.router.restart(async path => {
      for (const suffix of ["", "-wal", "-shm"]) await rm(path + suffix, { force: true });
      await exec("go", ["run", "./internal/store/testdata/pre130-migration", path], { cwd: resolve(repo, "router-proxy") });
      const db = new DatabaseSync(path);
      try {
        db.exec("PRAGMA foreign_keys=ON");
        expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all("129_workflow_allow_implicit_return")).toHaveLength(1);
        expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all(migration)).toHaveLength(0);
        expect(db.prepare("PRAGMA table_info(managed_agents)").all().some(c => c.name === "workflow_predecessor_state_id")).toBe(false);
        const now = new Date().toISOString();
        db.prepare("INSERT INTO workflows(id,name,created_at,updated_at) VALUES('wf','Saved workflow',?,?)").run(now, now);
        db.prepare("INSERT INTO workflow_states(id,name,workflow_id,created_at,updated_at,allow_implicit_return) VALUES(?,?, 'wf',?,?,?)").run("a", "A", now, now, 0);
        db.prepare("INSERT INTO workflow_states(id,name,workflow_id,created_at,updated_at,allow_implicit_return) VALUES(?,?, 'wf',?,?,?)").run("b", "B", now, now, 1);
        db.prepare(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at,active_workflow_id,active_workflow_state_id)
          VALUES('legacy-agent','legacy-agent',?,'google','fixture-model','terminated','terminated','idle','embedded','session',1,'generation',1,?,?,?,'wf','b')`).run(repo, "a".repeat(64), now, now);
        db.prepare(`INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json,tool_input_json)
          VALUES('legacy-agent','managed_message','info','Workflow state transitioned',?,'workflow-transition:legacy-agent:a:b:old',?,?)`).run(now, JSON.stringify({kind:'state',outcome:'success',from:'A',to:'B',summary:'Workflow transitioned from A to B'}), JSON.stringify({action:'transition',target_state:'B'}));
        expect(db.prepare("PRAGMA foreign_key_check").all()).toEqual([]);
      } finally { db.close(); }
    });
    const check = () => {
      const db = new DatabaseSync(f.router.databasePath);
      try {
        expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all(migration)).toHaveLength(1);
        expect(db.prepare("SELECT active_workflow_id,active_workflow_state_id,workflow_predecessor_state_id,workflow_binding_generation FROM managed_agents WHERE agent_id='legacy-agent'").get()).toMatchObject({active_workflow_id:'wf',active_workflow_state_id:'b',workflow_predecessor_state_id:null,workflow_binding_generation:0});
        expect(db.prepare("SELECT name,allow_implicit_return FROM workflow_states ORDER BY name").all()).toEqual([{name:'A',allow_implicit_return:0},{name:'B',allow_implicit_return:1}]);
        expect(db.prepare("SELECT message,presentation_json FROM managed_complete_history WHERE event_key='workflow-transition:legacy-agent:a:b:old'").get()).toMatchObject({message:'Workflow state transitioned',presentation_json: expect.stringContaining('Workflow transitioned from A to B')});
        expect(db.prepare("PRAGMA foreign_key_check").all()).toEqual([]);
      } finally { db.close(); }
    };
    check();
    await page.goto(f.frontend.url);
    expect(await page.evaluate(async url => (await fetch(url + '/v1/health', {credentials:'omit'})).ok, f.proxy!.url)).toBe(true);
    await f.router.restart();
    check();
    expect(f.mockLLM.getHistory()).toEqual([]);
  } finally { await f.close(); }
});

// IR-001: upgrade states that existed before the option's column existed.
test("IR-001: pre-129 states retain their configuration and default return off", async ({ page }) => {
  test.setTimeout(90_000);
  const f = await setupE2EHarness({ useProxy: true });
  const ids = ["11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"];
  let before: Record<string, unknown>[] = [];
  let publicationsBefore: Record<string, unknown>[] = [];
  let evaluationsBefore: Record<string, unknown>[] = [];
  try {
    await f.router.restart(async path => {
      for (const suffix of ["", "-wal", "-shm"]) await rm(path + suffix, { force: true });
      await exec("go", ["run", "./internal/store/testdata/pre130-migration", path, "129_workflow_allow_implicit_return"], { cwd: resolve(repo, "router-proxy") });
      const db = new DatabaseSync(path);
      try {
        db.exec("PRAGMA foreign_keys=ON");
        expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all("129_workflow_allow_implicit_return")).toHaveLength(0);
        expect(db.prepare("PRAGMA table_info(workflow_states)").all().some(c => c.name === "allow_implicit_return")).toBe(false);
        const now = "2024-01-02T03:04:05Z";
        db.prepare("INSERT INTO workflows(id,name,created_at,updated_at) VALUES('wf','Legacy workflow',?,?)").run(now, now);
        const insert = db.prepare(`INSERT INTO workflow_states(id,workflow_id,name,description,entry_text,created_at,updated_at,allowed_tools_json,transitions_json,allow_exit,ui_pos_x,ui_pos_y)
          VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`);
        insert.run(ids[0], "wf", "First", "Legacy description", "Legacy instructions", now, now, '["read"]', JSON.stringify([{target_state_id: ids[1], description: "Continue"}]), 0, 12, 34);
        insert.run(ids[1], "wf", "Second", "Other description", "Other instructions", now, now, '[]', '[]', 1, -5, 6);
        db.prepare(`INSERT INTO managed_agents(agent_id,name,cwd,selected_model_provider,selected_model_id,desired_state,lifecycle_state,activity_state,runtime,pi_session_id,session_epoch,runtime_generation,manifest_revision,manifest_hash,created_at,updated_at,active_workflow_id,active_workflow_state_id)
          VALUES('pre129-agent','pre129-agent',?,'google','fixture-model','terminated','terminated','idle','embedded','session',1,'generation',1,?,?,?,'wf',?)`).run(repo, "a".repeat(64), now, now, ids[1]);
        db.prepare(`INSERT INTO managed_complete_history(agent_id,category,level,message,occurred_at,event_key,presentation_json,tool_input_json)
          VALUES('pre129-agent','managed_message','info','Workflow state transitioned',?,'workflow-transition:pre129:first:second:old',?,?)`).run(now, JSON.stringify({kind:'state',outcome:'success',from:'First',to:'Second'}), JSON.stringify({action:'transition',target_state:'Second'}));
        // Seed an evaluation and publication after migration 128, before 129/130.
        expect(db.prepare("SELECT id FROM schema_migrations WHERE id='128_goal_activity_publication'").all()).toHaveLength(1);
        db.prepare(`INSERT INTO embedded_sessions(agent_id,session_id,epoch,generation,role,current,owner_token)
          VALUES('pre129-agent','session',1,'generation','root',0,1)`).run();
        const entry = db.prepare(`INSERT INTO embedded_entries(agent_id,session_id,epoch,generation,seq,parent_digest,kind,body,digest,retention_admitted_at_ms)
          VALUES('pre129-agent','session',1,'generation',?,?,?,?,?,?)`);
        const admitted = Date.now();
        entry.run(1, 'genesis', 'user', 'goal input', 'user-digest', admitted);
        entry.run(2, 'user-digest', 'assistant', 'goal output', 'final-digest', admitted);
        db.prepare(`INSERT INTO embedded_goal_evaluations(agent_id,session_id,epoch,generation,user_seq,user_digest,goal_revision,owner_token,final_seq,final_digest,state,reason,claimed_at,completed_at)
          VALUES('pre129-agent','session',1,'generation',1,'user-digest',1,1,2,'final-digest','DONE','goal achieved',?,?)`).run(now, now);
        db.prepare(`INSERT INTO embedded_goal_activity_publications(agent_id,session_id,epoch,user_seq,user_digest,goal_revision,event_key,reason)
          VALUES('pre129-agent','session',1,1,'user-digest',1,'goal-evaluation:pre129-agent:1:2:COMPLETED','goal achieved')`).run();
        publicationsBefore = db.prepare("SELECT * FROM embedded_goal_activity_publications").all();
        evaluationsBefore = db.prepare("SELECT * FROM embedded_goal_evaluations").all();
        expect(publicationsBefore).toHaveLength(1);
        expect(evaluationsBefore).toHaveLength(1);
        expect(db.prepare("PRAGMA table_info(managed_agents)").all().some(c => c.name === "workflow_predecessor_state_id")).toBe(false);
        before = db.prepare("SELECT * FROM workflow_states ORDER BY id").all();
        expect(before).toHaveLength(2);
        expect(db.prepare("PRAGMA foreign_key_check").all()).toEqual([]);
      } finally { db.close(); }
    });
    const check = async () => {
      const db = new DatabaseSync(f.router.databasePath);
      try {
        expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all("129_workflow_allow_implicit_return")).toHaveLength(1);
        expect(db.prepare("SELECT id FROM schema_migrations WHERE id=?").all(migration)).toHaveLength(1);
        expect(db.prepare("SELECT active_workflow_id,active_workflow_state_id,workflow_predecessor_state_id,workflow_binding_generation FROM managed_agents WHERE agent_id='pre129-agent'").get()).toMatchObject({active_workflow_id:'wf',active_workflow_state_id:ids[1],workflow_predecessor_state_id:null,workflow_binding_generation:0});
        expect(db.prepare("SELECT message FROM managed_complete_history WHERE event_key='workflow-transition:pre129:first:second:old'").get()).toMatchObject({message:'Workflow state transitioned'});
        expect(db.prepare("SELECT * FROM embedded_goal_activity_publications").all()).toEqual(publicationsBefore);
        expect(db.prepare("SELECT * FROM embedded_goal_evaluations").all()).toEqual(evaluationsBefore);
        const rows = db.prepare("SELECT * FROM workflow_states ORDER BY id").all();
        expect(rows.map(({ allow_implicit_return, ...rest }) => ({ ...rest }))).toEqual(before.map(row => ({ ...row })));
        expect(rows.map(row => row.allow_implicit_return)).toEqual([0, 0]);
        expect(db.prepare("PRAGMA foreign_key_check").all()).toEqual([]);
      } finally { db.close(); }
      const states = await page.evaluate(async ({ url, ids }) => Promise.all(ids.map(async id => {
        const response = await fetch(`${url}/v1/workflow-states/${id}`, { credentials: "omit" });
        if (!response.ok) throw new Error(`state fetch failed: ${response.status}`);
        return (await response.json()).workflow_state;
      })), { url: f.proxy!.url, ids });
      expect(states[0]).toMatchObject({ id: ids[0], workflow_id: "wf", name: "First", description: "Legacy description", entry_text: "Legacy instructions", allowed_tools: ["read"], transitions: [{target_state_id: ids[1], description: "Continue"}], allow_exit: false, allow_implicit_return: false, ui_pos_x: 12, ui_pos_y: 34 });
      expect(states[1]).toMatchObject({ id: ids[1], workflow_id: "wf", name: "Second", description: "Other description", entry_text: "Other instructions", allow_exit: true, allow_implicit_return: false });
    };
    await page.goto(f.frontend.url);
    await check();
    await f.router.restart();
    await check();
    expect(f.mockLLM.getHistory()).toEqual([]);
  } finally { await f.close(); }
});
