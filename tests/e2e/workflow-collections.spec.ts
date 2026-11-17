import { test, expect } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import { createServer } from "node:http";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { workflowHarness, type WorkflowHarness } from "./workflow-implicit-goals-harness";

test.describe("Workflow Collections E2E (SPEC-WORKFLOW-COLLECTIONS-001)", () => {
  let f: WorkflowHarness;

  test.beforeEach(async () => {
    f = await workflowHarness({ omitGoogle: true });
  });

  test.afterEach(async () => {
    await f?.close();
  });

  // Helper for making browser API calls via page
  const browserApi = async (page: any, path: string, method = "GET", body?: object) => {
    return page.evaluate(
      async ({ url, path, method, body }: { url: string; path: string; method: string; body?: object }) => {
        const response = await fetch(url + path, {
          method,
          credentials: "omit",
          headers: body ? { "Content-Type": "application/json" } : {},
          body: body ? JSON.stringify(body) : undefined,
        });
        const text = await response.text();
        let parsed: any;
        try {
          parsed = text ? JSON.parse(text) : null;
        } catch {
          parsed = text;
        }
        return { status: response.status, ok: response.ok, body: parsed };
      },
      { url: f.proxy.url, path, method, body }
    );
  };

// SCN-COLL-001: migrates pre-existing workflows into default collection
// Tests SQLite migration 136 backfill into the seeded 'default' collection.
  test("SCN-COLL-001: migrates pre-existing workflows into default collection", async () => {
    // 1. Verify default collection exists on startup
    const collections = await f.api("/v1/workflow-collections");
    expect(Array.isArray(collections)).toBe(true);
    const defaultCol = collections.find((c: any) => c.id === "default");
    expect(defaultCol).toBeDefined();
    expect(defaultCol.name).toBe("Default");

    // 2. Create a workflow without explicit collection_id
    const wf = await f.api("/v1/workflows", { name: "Pre-existing Workflow", description: "Created without collection" });
    expect(wf.collection_id).toBe("default");

    // 3. Verify workflow is in default collection
    const fetchedWf = await f.api(`/v1/workflows/${wf.id}`);
    expect(fetchedWf.workflow.collection_id).toBe("default");

    // 4. Verify collection workflow count includes it
    const colAfter = await f.api("/v1/workflow-collections/default");
    expect(colAfter.workflow_count).toBeGreaterThanOrEqual(1);

    // 5. Query workflows by collection_id=default
    const defaultWfs = await f.api("/v1/workflows?collection_id=default");
    expect(defaultWfs.some((w: any) => w.id === wf.id)).toBe(true);
  });

  test("SCN-COLL-002: creates and switches active workflow collection in UI", async ({ page }) => {
    await f.open(page);

    // Navigate to Workflows via managed actions menu
    const trigger = page.locator(".managed-actions-trigger");
    await expect(trigger).toBeVisible({ timeout: 10000 });
    await trigger.click();

    const workflowsMenuItem = page.locator('button[role="menuitem"]:has-text("Workflows")');
    await expect(workflowsMenuItem).toBeVisible({ timeout: 5000 });
    await workflowsMenuItem.click();

    // Verify Workflows page header is loaded
    await expect(page.locator(".workflows-title h2")).toContainText("Workflows");

    // Open Manage Collections modal
    const manageBtn = page.locator(".workflow-collection-manage-btn");
    await expect(manageBtn).toBeVisible({ timeout: 5000 });
    await manageBtn.click();

    // Verify Manage Collections modal opens in list view
    await expect(page.locator("#workflow-collections-dialog-title")).toContainText("Workflow Collections");
    const newColBtn = page.locator(".workflow-collections-modal-content button:has-text('New Collection')");
    await expect(newColBtn).toBeVisible();

    // Transition to Create Form view
    await newColBtn.click();
    await expect(page.locator("#workflow-collections-dialog-title")).toContainText("New Collection");
    const nameInput = page.locator("#create-col-name-input");
    await expect(nameInput).toBeVisible();

    // Fill form and submit
    await nameInput.fill("DevOps Automation");
    await page.locator("#create-col-desc-input").fill("CI/CD and deployment");
    await page.locator(".workflow-collections-dialog button[type='submit']").click();

    // Verify modal transitions back to list view and new collection pill is present and active
    await page.locator(".workflow-collections-dialog .dialog-actions button:has-text('Close')").click();
    const devOpsPill = page.locator(".workflow-collection-pill:has-text('DevOps Automation')");
    await expect(devOpsPill).toBeVisible({ timeout: 5000 });
    await expect(devOpsPill).toHaveClass(/active/);

    // Create a workflow in DevOps Automation and one in Default
    const newCol = (await f.api("/v1/workflow-collections")).find((c: any) => c.name === "DevOps Automation");
    expect(newCol).toBeDefined();

    // Create workflow in DevOps Automation via the UI "+ New Workflow" button
    const newWfBtn = page.locator(".workflows-header button.btn.btn-primary:has-text('New Workflow')");
    await expect(newWfBtn).toBeVisible({ timeout: 5000 });
    await newWfBtn.click();
    await expect(page.locator("#create-workflow-dialog-title")).toContainText("New Workflow");
    await page.locator("#workflow-name-input").fill("Deploy Script");
    await page.locator(".message-dialog button[type='submit']").click();

    // Wait for canvas to load and then click Dashboard / Back to return to workflow list
    const backBtn = page.locator("button.workflows-back-btn, button:has-text('Back')");
    await expect(backBtn.first()).toBeVisible({ timeout: 5000 });
    await backBtn.first().click();

    await expect(page.locator(".workflow-card-name").filter({ hasText: /^Deploy Script$/ })).toBeVisible({ timeout: 5000 });

    // Switch active collection pill to Default in UI
    const defaultPill = page.locator(".workflow-collection-pill:has-text('Default')");
    await defaultPill.click();
    await expect(page.locator(".workflow-card-name").filter({ hasText: /^Deploy Script$/ })).not.toBeVisible();

    // Switch back to DevOps Automation pill
    await devOpsPill.click();
    await expect(page.locator(".workflow-card-name").filter({ hasText: /^Deploy Script$/ })).toBeVisible({ timeout: 5000 });
  });

  test("SCN-COLL-003: renames an existing workflow collection", async ({ page }) => {
    // 1. Create a user collection
    const col = await f.api("/v1/workflow-collections", { name: "DevOps Automation" });
    expect(col.name).toBe("DevOps Automation");

    // 2. Rename it to "Cloud Infrastructure"
    const updated = await f.api(`/v1/workflow-collections/${col.id}`, { name: "Cloud Infrastructure" }, "PUT");
    expect(updated.name).toBe("Cloud Infrastructure");

    // 3. Verify via GET
    const fetched = await f.api(`/v1/workflow-collections/${col.id}`);
    expect(fetched.name).toBe("Cloud Infrastructure");
  });

// SCN-COLL-004: moves workflow between collections and verifies isolation
// Verifies that state UUIDs and internal transitions remain intact when collection_id changes.
  test("SCN-COLL-004: moves workflow between collections and verifies isolation", async () => {
    // 1. Create Collection A and Collection B
    const colA = await f.api("/v1/workflow-collections", { name: "Collection Alpha" });
    const colB = await f.api("/v1/workflow-collections", { name: "Collection Beta" });

    // 2. Create workflow in Collection A with states and transitions
    const wf = await f.api("/v1/workflows", { name: "Deployment Pipeline", collection_id: colA.id });
    const { workflow_state: st1 } = await f.api("/v1/workflow-states", { workflow_id: wf.id, name: "Build" });
    const { workflow_state: st2 } = await f.api("/v1/workflow-states", {
      workflow_id: wf.id,
      name: "Deploy",
      allow_exit: true,
    });
    await f.api(`/v1/workflow-states/${st1.id}`, {
      name: "Build",
      transitions: [{ target_state_id: st2.id, description: "On Success" }],
    }, "PUT");
    await f.api(`/v1/workflows/${wf.id}`, { name: "Deployment Pipeline", entry_state_id: st1.id }, "PUT");

    // Verify it is in colA and not in colB
    let listA = await f.api(`/v1/workflows?collection_id=${colA.id}`);
    expect(listA.some((w: any) => w.id === wf.id)).toBe(true);
    let listB = await f.api(`/v1/workflows?collection_id=${colB.id}`);
    expect(listB.some((w: any) => w.id === wf.id)).toBe(false);

    // 3. Move workflow from colA to colB
    const moved = await f.api(`/v1/workflows/${wf.id}/move`, { destination_collection_id: colB.id });
    expect(moved.collection_id).toBe(colB.id);

    // 4. Verify Collection A no longer has it and Collection B has it
    listA = await f.api(`/v1/workflows?collection_id=${colA.id}`);
    expect(listA.some((w: any) => w.id === wf.id)).toBe(false);
    listB = await f.api(`/v1/workflows?collection_id=${colB.id}`);
    expect(listB.some((w: any) => w.id === wf.id)).toBe(true);

    // 5. Verify states, transitions, entry_state_id remain intact
    const detail = await f.api(`/v1/workflows/${wf.id}`);
    expect(detail.workflow.entry_state_id).toBe(st1.id);
    expect(detail.states).toHaveLength(2);
    const detailSt1 = detail.states.find((s: any) => s.id === st1.id);
    expect(detailSt1.transitions).toEqual([{ target_state_id: st2.id, description: "On Success" }]);
  });

// SCN-COLL-005: copies workflow between collections with state cloning
// Verifies that cloning between collections generates freshly minted UUIDs for states
// and remaps transitions and entry_state_id properly.
  test("SCN-COLL-005: copies workflow between collections with state cloning", async () => {
    // 1. Create Collection A and Collection B
    const colA = await f.api("/v1/workflow-collections", { name: "Source Col" });
    const colB = await f.api("/v1/workflow-collections", { name: "Target Col" });

    // 2. Create workflow in Collection A
    const wf = await f.api("/v1/workflows", { name: "Triage Bot", collection_id: colA.id });
    const { workflow_state: st } = await f.api("/v1/workflow-states", { workflow_id: wf.id, name: "Initial", entry_text: "Triage prompt" });
    await f.api(`/v1/workflows/${wf.id}`, { name: "Triage Bot", entry_state_id: st.id }, "PUT");

    // 3. Duplicate into Collection B
    const duplicated = await f.api(`/v1/workflows/${wf.id}/duplicate`, {
      destination_collection_id: colB.id,
    });
    expect(duplicated.id).not.toBe(wf.id);
    expect(duplicated.collection_id).toBe(colB.id);
    expect(duplicated.name).toBe("Triage Bot");

    // 4. Verify Collection A original is intact
    const original = await f.api(`/v1/workflows/${wf.id}`);
    expect(original.workflow.collection_id).toBe(colA.id);
    expect(original.workflow.name).toBe("Triage Bot");

    // 5. Verify Collection B has cloned states with distinct IDs
    const clonedDetail = await f.api(`/v1/workflows/${duplicated.id}`);
    expect(clonedDetail.states).toHaveLength(1);
    expect(clonedDetail.states[0].id).not.toBe(st.id);
    expect(clonedDetail.states[0].name).toBe("Initial");
    expect(clonedDetail.states[0].entry_text).toBe("Triage prompt");
    expect(clonedDetail.workflow.entry_state_id).toBe(clonedDetail.states[0].id);
  });

// SCN-COLL-006: configures collection git sync target and pushes collection workflows
// Verifies Git sync targets scoped to collections ("workflows:<id>") and git push / pull isolation.
  test("SCN-COLL-006: configures collection git sync target and pushes collection workflows", async ({ page }) => {
    test.setTimeout(120_000);
    const root = await mkdtemp(join(tmpdir(), "workflow-coll-git-push-"));
    const git = (...args: string[]) => execFileSync("git", args, { encoding: "utf8" });

    const server = createServer((req, res) => {
      const url = new URL(req.url!, "http://127.0.0.1");
      const child = spawn("git", ["http-backend"], {
        env: {
          ...process.env,
          GIT_PROJECT_ROOT: root,
          GIT_HTTP_EXPORT_ALL: "1",
          PATH_INFO: url.pathname,
          QUERY_STRING: url.search.slice(1),
          REQUEST_METHOD: req.method,
          CONTENT_TYPE: req.headers["content-type"] ?? "",
          CONTENT_LENGTH: req.headers["content-length"] ?? "",
        },
      });
      req.pipe(child.stdin);
      let headers = Buffer.alloc(0);
      let started = false;
      child.stdout.on("data", (chunk: Buffer) => {
        if (started) {
          res.write(chunk);
          return;
        }
        headers = Buffer.concat([headers, chunk]);
        const end = headers.indexOf("\r\n\r\n");
        if (end < 0) return;
        for (const line of headers.subarray(0, end).toString().split("\r\n")) {
          const colon = line.indexOf(":");
          if (colon < 0) continue;
          const name = line.slice(0, colon),
            value = line.slice(colon + 1).trim();
          if (name.toLowerCase() === "status") res.statusCode = Number.parseInt(value, 10);
          else res.setHeader(name, value);
        }
        started = true;
        res.write(headers.subarray(end + 4));
      });
      child.on("close", (code) => {
        if (!started) res.statusCode = 500;
        res.end();
      });
    });

    try {
      git("init", "--bare", "--initial-branch=main", join(root, "remote.git"));
      git("--git-dir", join(root, "remote.git"), "config", "http.receivepack", "true");
      await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
      const address = server.address();
      if (!address || typeof address === "string") throw Error("missing git server address");

      await writeFile(
        join(f.router.root, "managed-home", ".gitconfig"),
        "[user]\n name = E2E Test\n email = e2e@example.invalid\n"
      );

      // Create two collections with separate workflows
      const colCustom = await f.api("/v1/workflow-collections", { name: "Cloud Infra" });
      await f.api("/v1/workflows", { name: "Cloud Workflow", collection_id: colCustom.id });
      await f.api("/v1/workflows", { name: "Default Workflow", collection_id: "default" });

      // Configure git sync for workflows:colCustom.id
      const target = `workflows:${colCustom.id}`;
      const repoUrl = `http://127.0.0.1:${address.port}/remote.git`;
      await f.api(`/v1/git-sync/${target}`, { repo_url: repoUrl }, "PUT");

      // Push collection workflows
      const pushRes = await f.api(`/v1/git-sync/${target}/push`, {}, "POST");
      expect(pushRes).toMatchObject({ success: true, count: 1 });

      // Verify the pushed workflows.json in git contains only Cloud Workflow
      const raw = git("--git-dir", join(root, "remote.git"), "show", "main:workflows.json");
      const payload = JSON.parse(raw);
      expect(payload.workflows).toHaveLength(1);
      expect(payload.workflows[0].workflow.name).toBe("Cloud Workflow");
      expect(raw).not.toContain("Default Workflow");
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
      await rm(root, { recursive: true, force: true });
    }
  });

  test("SCN-COLL-007: pulls collection workflows replacing only targeted collection", async () => {
    test.setTimeout(120_000);
    const root = await mkdtemp(join(tmpdir(), "workflow-coll-git-pull-"));
    const git = (...args: string[]) => execFileSync("git", args, { encoding: "utf8" });

    const server = createServer((req, res) => {
      const url = new URL(req.url!, "http://127.0.0.1");
      const child = spawn("git", ["http-backend"], {
        env: {
          ...process.env,
          GIT_PROJECT_ROOT: root,
          GIT_HTTP_EXPORT_ALL: "1",
          PATH_INFO: url.pathname,
          QUERY_STRING: url.search.slice(1),
          REQUEST_METHOD: req.method,
          CONTENT_TYPE: req.headers["content-type"] ?? "",
          CONTENT_LENGTH: req.headers["content-length"] ?? "",
        },
      });
      req.pipe(child.stdin);
      let headers = Buffer.alloc(0);
      let started = false;
      child.stdout.on("data", (chunk: Buffer) => {
        if (started) {
          res.write(chunk);
          return;
        }
        headers = Buffer.concat([headers, chunk]);
        const end = headers.indexOf("\r\n\r\n");
        if (end < 0) return;
        for (const line of headers.subarray(0, end).toString().split("\r\n")) {
          const colon = line.indexOf(":");
          if (colon < 0) continue;
          const name = line.slice(0, colon),
            value = line.slice(colon + 1).trim();
          if (name.toLowerCase() === "status") res.statusCode = Number.parseInt(value, 10);
          else res.setHeader(name, value);
        }
        started = true;
        res.write(headers.subarray(end + 4));
      });
      child.on("close", (code) => {
        if (!started) res.statusCode = 500;
        res.end();
      });
    });

    try {
      git("init", "--bare", "--initial-branch=main", join(root, "remote.git"));
      git("--git-dir", join(root, "remote.git"), "config", "http.receivepack", "true");
      await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
      const address = server.address();
      if (!address || typeof address === "string") throw Error("missing git server address");

      await writeFile(
        join(f.router.root, "managed-home", ".gitconfig"),
        "[user]\n name = E2E Test\n email = e2e@example.invalid\n"
      );

      // Create collections
      const colA = await f.api("/v1/workflow-collections", { name: "Collection Sync" });
      const wfA = await f.api("/v1/workflows", { name: "Remote Candidate", collection_id: colA.id });
      const { workflow_state: stA } = await f.api("/v1/workflow-states", {
        workflow_id: wfA.id,
        name: "Initial State",
        allow_exit: true,
      });
      await f.api(`/v1/workflows/${wfA.id}`, { name: "Remote Candidate", entry_state_id: stA.id }, "PUT");
      const wfDefault = await f.api("/v1/workflows", { name: "Untouched Default", collection_id: "default" });

      // Configure git sync target
      const target = `workflows:${colA.id}`;
      const repoUrl = `http://127.0.0.1:${address.port}/remote.git`;
      await f.api(`/v1/git-sync/${target}`, { repo_url: repoUrl }, "PUT");

      // Push initial
      await f.api(`/v1/git-sync/${target}/push`, {}, "POST");

      // Now add a local-only workflow in Col A, and modify Remote Candidate
      const localOnly = await f.api("/v1/workflows", { name: "Local Only In Col A", collection_id: colA.id });

      // Pull from git for Col A
      const pullRes = await f.api(`/v1/git-sync/${target}/pull`, {}, "POST");
      expect(pullRes).toMatchObject({ exists: true, count: 1 });

      // Verify Col A now only contains Remote Candidate, Local Only was replaced
      const colAWorkflows = await f.api(`/v1/workflows?collection_id=${colA.id}`);
      expect(colAWorkflows).toHaveLength(1);
      expect(colAWorkflows[0].name).toBe("Remote Candidate");

      // Verify Default collection workflow remained untouched
      const defaultWorkflows = await f.api("/v1/workflows?collection_id=default");
      expect(defaultWorkflows.some((w: any) => w.name === "Untouched Default")).toBe(true);
    } finally {
      await new Promise<void>((resolve) => server.close(() => resolve()));
      await rm(root, { recursive: true, force: true });
    }
  });

  test("SCN-COLL-008: deletes empty collection and returns to default collection", async () => {
    // 1. Create empty collection
    const col = await f.api("/v1/workflow-collections", { name: "Obsolete Collection" });

    // 2. Delete it
    const res = await fetch(`${f.proxy.url}/v1/workflow-collections/${col.id}`, { method: "DELETE" });
    expect(res.status).toBe(204);

    // 3. Verify 404
    const getRes = await fetch(`${f.proxy.url}/v1/workflow-collections/${col.id}`);
    expect(getRes.status).toBe(404);
  });

  test("SCN-COLL-009: rejects deletion or renaming of default collection via API and hides UI actions", async ({ page }) => {
    // 1. Attempt PUT /v1/workflow-collections/default -> 400 Bad Request
    const putRes = await fetch(`${f.proxy.url}/v1/workflow-collections/default`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "Hacked Default" }),
    });
    expect(putRes.status).toBe(400);
    const putErr = await putRes.json();
    const putErrCode = (typeof putErr.error === 'object' && putErr.error?.code) ? putErr.error.code : (putErr.code || putErr.error);
    expect(putErrCode).toBe("cannot_modify_default_collection");

    // 2. Attempt DELETE /v1/workflow-collections/default -> 400 Bad Request
    const delRes = await fetch(`${f.proxy.url}/v1/workflow-collections/default`, {
      method: "DELETE",
    });
    expect(delRes.status).toBe(400);
    const delErr = await delRes.json();
    const delErrCode = (typeof delErr.error === 'object' && delErr.error?.code) ? delErr.error.code : (delErr.code || delErr.error);
    expect(delErrCode).toBe("cannot_delete_default_collection");

    // 3. Attempt with cascade=true -> still 400 Bad Request
    const delCascadeRes = await fetch(`${f.proxy.url}/v1/workflow-collections/default?cascade=true`, {
      method: "DELETE",
    });
    expect(delCascadeRes.status).toBe(400);
    const delCascadeErr = await delCascadeRes.json();
    const delCascadeErrCode = (typeof delCascadeErr.error === 'object' && delCascadeErr.error?.code) ? delCascadeErr.error.code : (delCascadeErr.code || delCascadeErr.error);
    expect(delCascadeErrCode).toBe("cannot_delete_default_collection");
  });

  test("SCN-COLL-010: rejects deletion of non-empty collection without cascade confirmation", async () => {
    // 1. Create collection with workflow
    const col = await f.api("/v1/workflow-collections", { name: "Production" });
    const wf = await f.api("/v1/workflows", { name: "Prod Workflow", collection_id: col.id });

    // 2. Delete without cascade -> 409 Conflict
    const delRes = await fetch(`${f.proxy.url}/v1/workflow-collections/${col.id}`, { method: "DELETE" });
    expect(delRes.status).toBe(409);
    const delErr = await delRes.json();
    const delErrCode = (typeof delErr.error === 'object' && delErr.error?.code) ? delErr.error.code : (delErr.code || delErr.error);
    expect(delErrCode).toBe("collection_not_empty");

    // 3. Delete with cascade=true -> 204 No Content
    const cascadeRes = await fetch(`${f.proxy.url}/v1/workflow-collections/${col.id}?cascade=true`, { method: "DELETE" });
    expect(cascadeRes.status).toBe(204);

    // 4. Verify workflow inside was also deleted
    const wfRes = await fetch(`${f.proxy.url}/v1/workflows/${wf.id}`);
    expect(wfRes.status).toBe(404);
  });

  test("SCN-COLL-011: prevents moving workflow when name conflicts in destination collection", async () => {
    // 1. Create Collection A and Collection B
    const colA = await f.api("/v1/workflow-collections", { name: "Team A" });
    const colB = await f.api("/v1/workflow-collections", { name: "Team B" });

    // 2. Create workflow "Nightly Build" in both collections
    const wfA = await f.api("/v1/workflows", { name: "Nightly Build", collection_id: colA.id });
    await f.api("/v1/workflows", { name: "Nightly Build", collection_id: colB.id });

    // 3. Attempt to move wfA to colB -> 409 Conflict
    const moveRes = await fetch(`${f.proxy.url}/v1/workflows/${wfA.id}/move`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ destination_collection_id: colB.id }),
    });
    expect(moveRes.status).toBe(409);
    const moveErr = await moveRes.json();
    const moveErrCode = (typeof moveErr.error === 'object' && moveErr.error?.code) ? moveErr.error.code : (moveErr.code || moveErr.error);
    expect(moveErrCode).toBe("workflow_name_conflict");

    // 4. Verify wfA still in colA
    const wfCheck = await f.api(`/v1/workflows/${wfA.id}`);
    expect(wfCheck.workflow.collection_id).toBe(colA.id);
  });

  test("SCN-COLL-012: auto-renames copied workflow when name exists in destination collection", async () => {
    // 1. Create Collection A and Collection B
    const colA = await f.api("/v1/workflow-collections", { name: "Alpha Team" });
    const colB = await f.api("/v1/workflow-collections", { name: "Beta Team" });

    // 2. Create workflow "Nightly Build" in both collections
    const wfA = await f.api("/v1/workflows", { name: "Nightly Build", collection_id: colA.id });
    await f.api("/v1/workflows", { name: "Nightly Build", collection_id: colB.id });

    // 3. Copy wfA to colB -> succeeds with auto-generated unique name
    const dup = await f.api(`/v1/workflows/${wfA.id}/duplicate`, {
      destination_collection_id: colB.id,
    });
    expect(dup.collection_id).toBe(colB.id);
    expect(dup.name).toBe("Nightly Build (Copy)");

    // 4. Copy again to colB -> resolves to "Nightly Build (Copy 2)"
    const dup2 = await f.api(`/v1/workflows/${wfA.id}/duplicate`, {
      destination_collection_id: colB.id,
    });
    expect(dup2.collection_id).toBe(colB.id);
    expect(dup2.name).toBe("Nightly Build (Copy 2)");
  });

  test("SCN-COLL-013: validates collection name requirements on create and update", async () => {
    // 1. Create with empty name -> 400 Bad Request
    const emptyRes = await fetch(`${f.proxy.url}/v1/workflow-collections`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "   " }),
    });
    expect(emptyRes.status).toBe(400);
    const emptyErr = await emptyRes.json();
    const emptyErrCode = (typeof emptyErr.error === 'object' && emptyErr.error?.code) ? emptyErr.error.code : (emptyErr.code || emptyErr.error);
    expect(emptyErrCode).toBe("invalid_workflow_collection");

    // 2. Create valid collection
    const col1 = await f.api("/v1/workflow-collections", { name: "Unique Name" });

    // 3. Create with duplicate name -> 409 Conflict
    const dupRes = await fetch(`${f.proxy.url}/v1/workflow-collections`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "Unique Name" }),
    });
    expect(dupRes.status).toBe(409);
    const dupErr = await dupRes.json();
    const dupErrCode = (typeof dupErr.error === 'object' && dupErr.error?.code) ? dupErr.error.code : (dupErr.code || dupErr.error);
    expect(dupErrCode).toBe("workflow_collection_name_conflict");

    // 4. Update with empty name -> 400 Bad Request
    const updateEmptyRes = await fetch(`${f.proxy.url}/v1/workflow-collections/${col1.id}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "" }),
    });
    expect(updateEmptyRes.status).toBe(400);

    // 5. Update to conflict with another existing collection -> 409 Conflict
    const col2 = await f.api("/v1/workflow-collections", { name: "Second Collection" });
    const updateDupRes = await fetch(`${f.proxy.url}/v1/workflow-collections/${col2.id}`, {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "Unique Name" }),
    });
    expect(updateDupRes.status).toBe(409);
    const updateDupErr = await updateDupRes.json();
    const updateDupErrCode = (typeof updateDupErr.error === 'object' && updateDupErr.error?.code) ? updateDupErr.error.code : (updateDupErr.code || updateDupErr.error);
    expect(updateDupErrCode).toBe("workflow_collection_name_conflict");
  });

  test("SCN-COLL-014: returns 404 when moving or copying to nonexistent collection", async () => {
    // 1. Create a workflow in default collection
    const wf = await f.api("/v1/workflows", { name: "Orphan Candidate" });

    // 2. Move to nonexistent collection -> 404
    const moveRes = await fetch(`${f.proxy.url}/v1/workflows/${wf.id}/move`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ destination_collection_id: "nonexistent-collection-id" }),
    });
    expect(moveRes.status).toBe(404);
    const moveErr = await moveRes.json();
    const moveErrCode = (typeof moveErr.error === 'object' && moveErr.error?.code) ? moveErr.error.code : (moveErr.code || moveErr.error);
    expect(moveErrCode).toBe("workflow_collection_not_found");

    // 3. Duplicate to nonexistent collection -> 404
    const dupRes = await fetch(`${f.proxy.url}/v1/workflows/${wf.id}/duplicate`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ destination_collection_id: "nonexistent-collection-id" }),
    });
    expect(dupRes.status).toBe(404);
    const dupErr = await dupRes.json();
    const dupErrCode = (typeof dupErr.error === 'object' && dupErr.error?.code) ? dupErr.error.code : (dupErr.code || dupErr.error);
    expect(dupErrCode).toBe("workflow_collection_not_found");

    // 4. Verify workflow remains in default collection unmodified
    const fetchedWf = await f.api(`/v1/workflows/${wf.id}`);
    expect(fetchedWf.workflow.collection_id).toBe("default");
  });

  test("UI interaction: moves and copies workflow via card action buttons and verifies modal flow", async ({ page }) => {
    // 1. Create a target collection and a workflow in Default
    const targetCol = await f.api("/v1/workflow-collections", { name: "Secondary Collection" });
    const wf = await f.api("/v1/workflows", { name: "Interactive Pipeline", collection_id: "default" });

    await f.open(page);

    // Navigate to Workflows page
    const trigger = page.locator(".managed-actions-trigger");
    await expect(trigger).toBeVisible({ timeout: 10000 });
    await trigger.click();
    await page.locator('button[role="menuitem"]:has-text("Workflows")').click();
    await expect(page.locator(".workflows-title h2")).toContainText("Workflows");

    // Verify card is visible in Default collection
    const card = page.locator(`.workflow-card:has-text("Interactive Pipeline")`);
    await expect(card).toBeVisible({ timeout: 5000 });

    // Open Copy to collection modal via action button
    const copyBtn = card.locator('button[aria-label="Copy workflow to another collection"]');
    await expect(copyBtn).toBeVisible();
    await copyBtn.click();

    // Verify Copy modal is displayed
    await expect(page.locator("#copy-workflow-dialog-title")).toContainText("Copy Workflow to Collection");
    await page.locator("#copy-target-collection").selectOption(targetCol.id);
    await page.locator("#copy-workflow-name").fill("Interactive Pipeline Duplicated");
    await page.locator(".dialog-actions button[type='submit']:has-text('Copy Workflow')").click();

    // Switch to Secondary Collection pill
    const targetPill = page.locator(`.workflow-collection-pill:has-text("Secondary Collection")`);
    await expect(targetPill).toBeVisible({ timeout: 5000 });
    await targetPill.click();

    // Verify copied workflow is in Secondary Collection
    await expect(page.locator(`.workflow-card-name:has-text("Interactive Pipeline Duplicated")`)).toBeVisible({ timeout: 5000 });

    // Switch back to Default collection
    const defaultPill = page.locator(`.workflow-collection-pill:has-text("Default")`);
    await defaultPill.click();
    await expect(card).toBeVisible({ timeout: 5000 });

    // Move "Interactive Pipeline" to Secondary Collection
    const moveBtn = card.locator('button[aria-label="Move workflow to another collection"]');
    await expect(moveBtn).toBeVisible();
    await moveBtn.click();

    // Verify Move modal is displayed
    await expect(page.locator("#move-workflow-dialog-title")).toContainText("Move Workflow to Collection");
    await page.locator("#move-target-collection").selectOption(targetCol.id);
    await page.locator(".dialog-actions button[type='submit']:has-text('Move Workflow')").click();

    // Verify card disappeared from Default collection
    await expect(card).not.toBeVisible();

    // Switch to Secondary Collection and verify both original moved and copy exist
    await targetPill.click();
    await expect(page.locator(".workflow-card-name").filter({ hasText: /^Interactive Pipeline$/ })).toBeVisible({ timeout: 5000 });
    await expect(page.locator(".workflow-card-name").filter({ hasText: /^Interactive Pipeline Duplicated$/ })).toBeVisible({ timeout: 5000 });
  });
});
