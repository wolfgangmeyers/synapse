import { test, expect } from "@playwright/test";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { randomUUID } from "node:crypto";
import { startIsolatedRouter, type IsolatedRouter } from "./harness";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

test.describe("Scratchpad Truncation API and Lifecycle", () => {
  let router: IsolatedRouter;

  test.beforeEach(async () => {
    const repoRoot = resolve(__dirname, "../..");
    router = await startIsolatedRouter({ repoRoot, managed: true });
  });

  test.afterEach(async () => {
    if (router) {
      await router.close();
    }
  });

  function createTestAgent(agentId: string) {
    const db = new DatabaseSync(router.databasePath);
    const now = new Date().toISOString();
    db.prepare(`
      INSERT INTO managed_agents (
        agent_id, name, cwd, selected_model_provider, selected_model_id,
        desired_state, lifecycle_state, activity_state, runtime, session_epoch,
        manifest_revision, manifest_hash, created_at, updated_at
      ) VALUES (?, ?, ?, 'google', 'gemini-3.8-flash', 'running', 'running', 'active', 'embedded', 1, 1, 'hash', ?, ?)
    `).run(agentId, agentId, router.root, now, now);
    db.close();
  }

  function insertScratchpadEntry(agentId: string, content: string, pinned = false): { id: string; content: string; pinned: boolean } {
    const db = new DatabaseSync(router.databasePath);
    const id = randomUUID();
    const now = new Date().toISOString();
    db.prepare(`
      INSERT INTO scratchpad_entries (id, agent_id, content, pinned, created_at, updated_at)
      VALUES (?, ?, ?, ?, ?, ?)
    `).run(id, agentId, content, pinned ? 1 : 0, now, now);
    db.close();
    return { id, content, pinned };
  }

  test("SCN-SP-001: Truncate by before_id deletes older unpinned entries and preserves pinned and cutoff", async () => {
    const agentId = "test-truncation-before-id";
    createTestAgent(agentId);

    const e1 = insertScratchpadEntry(agentId, "entry 1", false);
    const p1 = insertScratchpadEntry(agentId, "pinned 1", true);
    const e2 = insertScratchpadEntry(agentId, "entry 2", false);
    const e3 = insertScratchpadEntry(agentId, "entry 3", false);
    const e4 = insertScratchpadEntry(agentId, "entry 4", false);

    const truncateRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ before_id: e3.id }),
    });
    expect(truncateRes.status).toBe(200);
    const truncateJson = await truncateRes.json();
    expect(truncateJson.ok).toBe(true);
    expect(truncateJson.agent_id).toBe(agentId);
    expect(truncateJson.deleted_count).toBe(2);
    expect(truncateJson.pinned_preserved).toBe(true);

    const getRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad`);
    expect(getRes.status).toBe(200);
    const getJson = await getRes.json();
    const entries = getJson.entries;
    const ids = entries.map((e: any) => e.id);
    expect(ids).toContain(p1.id);
    expect(ids).toContain(e3.id);
    expect(ids).toContain(e4.id);
    expect(ids).not.toContain(e1.id);
    expect(ids).not.toContain(e2.id);
  });

  test("SCN-SP-002: Truncate unpinned entries by keep_last retaining N newest and all pinned", async () => {
    const agentId = "test-truncation-keep-last";
    createTestAgent(agentId);

    const e1 = insertScratchpadEntry(agentId, "entry 1", false);
    const p1 = insertScratchpadEntry(agentId, "pinned 1", true);
    const e2 = insertScratchpadEntry(agentId, "entry 2", false);
    const e3 = insertScratchpadEntry(agentId, "entry 3", false);
    const p2 = insertScratchpadEntry(agentId, "pinned 2", true);
    const e4 = insertScratchpadEntry(agentId, "entry 4", false);

    const truncateRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ keep_last: 2 }),
    });
    expect(truncateRes.status).toBe(200);
    const truncateJson = await truncateRes.json();
    expect(truncateJson.ok).toBe(true);
    expect(truncateJson.agent_id).toBe(agentId);
    expect(truncateJson.deleted_count).toBe(2);
    expect(truncateJson.pinned_preserved).toBe(true);

    const getRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad`);
    expect(getRes.status).toBe(200);
    const getJson = await getRes.json();
    const entries = getJson.entries;

    const ids = entries.map((e: any) => e.id);
    expect(ids).toContain(p1.id);
    expect(ids).toContain(p2.id);
    expect(ids).toContain(e3.id);
    expect(ids).toContain(e4.id);
    expect(ids).not.toContain(e1.id);
    expect(ids).not.toContain(e2.id);
  });

  test("SCN-SP-003: Preserves pinned entries older than before_id and under aggressive keep_last", async () => {
    const agentId = "test-truncation-pinned-preserved";
    createTestAgent(agentId);

    const pOld = insertScratchpadEntry(agentId, "ancient critical decision", true);
    const eOld = insertScratchpadEntry(agentId, "ancient scratchpad log", false);
    const pMid = insertScratchpadEntry(agentId, "mid milestone", true);
    const eNew = insertScratchpadEntry(agentId, "new unpinned task", false);

    // Truncate with keep_last: 1 -> only eOld should be deleted; pOld, pMid, eNew preserved
    const truncateRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ keep_last: 1 }),
    });
    expect(truncateRes.status).toBe(200);
    const truncateJson = await truncateRes.json();
    expect(truncateJson.ok).toBe(true);
    expect(truncateJson.deleted_count).toBe(1);
    expect(truncateJson.pinned_preserved).toBe(true);

    const getRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad`);
    expect(getRes.status).toBe(200);
    const getJson = await getRes.json();
    const ids = getJson.entries.map((e: any) => e.id);

    expect(ids).toContain(pOld.id);
    expect(ids).toContain(pMid.id);
    expect(ids).toContain(eNew.id);
    expect(ids).not.toContain(eOld.id);
  });

  test("SCN-SP-004: Validates mutual exclusivity and rejects invalid parameters", async () => {
    const agentId = "test-truncation-validation";
    createTestAgent(agentId);

    const bothRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ keep_last: 5, before_id: "some-id" }),
    });
    expect(bothRes.status).toBe(400);

    const neitherRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({}),
    });
    expect(neitherRes.status).toBe(400);

    const negRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ keep_last: -1 }),
    });
    expect(negRes.status).toBe(400);

    const zeroRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentId}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ keep_last: 0 }),
    });
    expect(zeroRes.status).toBe(400);
  });

  test("SCN-SP-005: Returns 404 when before_id is nonexistent or belongs to another agent", async () => {
    const agentAlpha = "test-agent-alpha";
    const agentBravo = "test-agent-bravo";
    createTestAgent(agentAlpha);
    createTestAgent(agentBravo);

    const entryBravo = insertScratchpadEntry(agentBravo, "entry owned by bravo", false);
    const entryAlpha = insertScratchpadEntry(agentAlpha, "entry owned by alpha", false);

    // 1. Non-existent before_id
    const notFoundRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentAlpha}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ before_id: "non-existent-id" }),
    });
    expect(notFoundRes.status).toBe(404);

    // 2. Foreign before_id (entry belongs to agentBravo, requested on agentAlpha)
    const foreignRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentAlpha}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ before_id: entryBravo.id }),
    });
    expect(foreignRes.status).toBe(404);

    // Verify alpha and bravo entries still exist untouched
    const alphaGet = await fetch(`${router.httpUrl}/v1/managed-agents/${agentAlpha}/scratchpad`);
    const alphaJson = await alphaGet.json();
    expect(alphaJson.entries.map((e: any) => e.id)).toContain(entryAlpha.id);

    const bravoGet = await fetch(`${router.httpUrl}/v1/managed-agents/${agentBravo}/scratchpad`);
    const bravoJson = await bravoGet.json();
    expect(bravoJson.entries.map((e: any) => e.id)).toContain(entryBravo.id);
  });

  test("SCN-SP-006: Complete cross-agent scratchpad isolation during truncation", async () => {
    const agentA = "test-agent-iso-a";
    const agentB = "test-agent-iso-b";
    createTestAgent(agentA);
    createTestAgent(agentB);

    // Create 5 entries each for Agent A and Agent B
    const aEntries = [];
    const bEntries = [];
    for (let i = 1; i <= 5; i++) {
      aEntries.push(insertScratchpadEntry(agentA, `Agent A note ${i}`, false));
      bEntries.push(insertScratchpadEntry(agentB, `Agent B note ${i}`, false));
    }

    // Truncate Agent A with keep_last: 2 -> should delete 3 notes from Agent A
    const truncRes = await fetch(`${router.httpUrl}/v1/managed-agents/${agentA}/scratchpad/truncate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ keep_last: 2 }),
    });
    expect(truncRes.status).toBe(200);
    const truncJson = await truncRes.json();
    expect(truncJson.deleted_count).toBe(3);

    // Agent A has only 2 newest entries left
    const aGet = await fetch(`${router.httpUrl}/v1/managed-agents/${agentA}/scratchpad`);
    const aJson = await aGet.json();
    expect(aJson.total_count).toBe(2);
    expect(aJson.entries.map((e: any) => e.id)).toEqual([aEntries[4].id, aEntries[3].id]);

    // Agent B still has all 5 entries untouched
    const bGet = await fetch(`${router.httpUrl}/v1/managed-agents/${agentB}/scratchpad`);
    const bJson = await bGet.json();
    expect(bJson.total_count).toBe(5);
    expect(bJson.entries.length).toBe(5);
  });
});

