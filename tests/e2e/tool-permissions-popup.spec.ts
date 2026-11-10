import { test, expect, type Page } from "@playwright/test";
import { DatabaseSync } from "node:sqlite";
import { claudeWorkersHarness } from "./claude-workers-harness";
async function waitForWrite(started: Promise<void>) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  try {
    await Promise.race([started, new Promise<never>((_, reject) => {
      timer = setTimeout(() => reject(new Error('Timed out waiting for the first permission write')), 15_000);
    })]);
  } finally { clearTimeout(timer); }
}
async function agentTools(page: Page) {
  await page.getByRole("button", { name: "More agent controls" }).click();
  await page.getByRole("menu", { name: "Agent controls menu" }).getByRole("button", { name: "Worker tools", exact: true }).click();
}
async function globalTools(page: Page) {
  await page.getByRole("button", { name: "Managed actions" }).click();
  await page.getByRole("menuitem", { name: "Tool Defaults" }).click();
}
test("tool sections persist defaults and Claude grants with workflow override", async ({ page }) => {
  test.setTimeout(120_000);
  const f = await claudeWorkersHarness();
  try {
    await f.open(page); const agent = await f.launch(page);
    const db = new DatabaseSync(f.router.databasePath);
    try { db.exec("PRAGMA busy_timeout=5000; UPDATE managed_tool_defaults SET category='' WHERE tool_name='scratchpad'; UPDATE managed_tool_defaults SET category='General' WHERE tool_name='bash'"); } finally { db.close(); }
    await globalTools(page);
    const global = page.getByRole("dialog", { name: "Global Tool Defaults" });
    const general = global.locator('.tool-category-card').filter({ hasText: 'General' });
    await expect(general.getByRole('switch', { name: 'scratchpad' })).toBeAttached();
    await general.getByRole('button', { name: 'Disable All' }).click();
    await expect.poll(() => f.rows("SELECT enabled FROM managed_tool_defaults WHERE tool_name='scratchpad'")[0]?.enabled).toBe(0);
    await expect(general.getByRole('button', { name: 'Enable All' })).toBeEnabled();
    await general.getByRole('button', { name: 'Enable All' }).click();
    await expect.poll(() => f.rows("SELECT enabled FROM managed_tool_defaults WHERE tool_name='scratchpad'")[0]?.enabled).toBe(1);
    await expect(general.getByRole('button', { name: 'Enable All' })).toBeEnabled();
    await global.getByRole('button', { name: 'Close tool defaults' }).click();
    const workflow = await f.api('/v1/workflows', { name: 'popup workflow' });
    const { workflow_state: state } = await f.api('/v1/workflow-states', { workflow_id: workflow.id, name: 'Restricted', allowed_tools: ['bash'], allow_exit: true });
    await f.api(`/v1/managed-agents/${agent}/workflow-state`, { action: 'activate', workflow_state_id: state.id }, 'PUT');
    await agentTools(page);
    const dialog = page.getByRole('dialog', { name: /Manage Tools:/ });
    const section = dialog.locator('.tool-category-card').filter({ hasText: 'General' });
    await expect(dialog.getByText(/Claude native tools are not restricted/)).toBeVisible();
    await expect(dialog.getByRole('switch', { name: 'bash', exact: true })).toHaveCount(0);
    await expect(dialog.getByText(/overrides tool permissions/)).toBeVisible();
    await section.getByRole('button', { name: 'Revoke All' }).click();
    await expect.poll(() => f.rows("SELECT enabled FROM managed_agent_tools WHERE agent_id=? AND tool_name='scratchpad'", agent)[0]?.enabled).toBe(0);
    await expect(section.getByRole('button', { name: 'Grant All' })).toBeEnabled();
    await section.getByRole('button', { name: 'Grant All' }).click();
    await expect.poll(() => f.rows("SELECT enabled FROM managed_agent_tools WHERE agent_id=? AND tool_name='scratchpad'", agent)[0]?.enabled).toBe(1);
    await expect(section.getByRole('button', { name: 'Grant All' })).toBeEnabled();
    expect(f.rows("SELECT enabled FROM managed_agent_tools WHERE agent_id=? AND tool_name='bash'", agent)).toEqual([]);
    await expect(section.getByText('(effective: blocked)')).toBeVisible();
    await dialog.getByRole('button', { name: 'Close worker tools' }).click();
    await agentTools(page);
    await expect(page.getByRole('switch', { name: 'scratchpad' })).toBeChecked();
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});

test('closing during bulk waits on reopen and reports partial failure', async ({ page }) => {
  test.setTimeout(120_000);
  let release = () => {};
  const f = await claudeWorkersHarness();
  try {
    await f.open(page); const agent = await f.launch(page);
    const db = new DatabaseSync(f.router.databasePath);
    try { db.exec("PRAGMA busy_timeout=5000; UPDATE managed_tool_defaults SET category='General' WHERE tool_name IN ('scratchpad','kb_list')"); } finally { db.close(); }
    await agentTools(page);
    const dialog = page.getByRole('dialog', { name: /Manage Tools:/ });
    const section = dialog.locator('.tool-category-card').filter({ hasText: 'General' });
    await expect(section.getByRole('switch', { name: 'kb_list' })).toBeAttached();
    let observed!: () => void;
    const blocked = new Promise<void>(r => { release = r; });
    const started = new Promise<void>(r => { observed = r; });
    let puts = 0;
    await page.route(`**/v1/managed-agents/${agent}/tools`, async route => {
      if (route.request().method() !== 'PUT') { await route.continue(); return; }
      puts++;
      if (puts === 1) { observed(); await blocked; await route.continue(); }
      else if (puts === 2) await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'injected failure' }) });
      else await route.continue();
    });
    await section.getByRole('button', { name: 'Revoke All' }).click();
    await waitForWrite(started);
    await dialog.getByRole('button', { name: 'Close worker tools' }).click();
    await agentTools(page);
    await expect(page.getByText('Loading worker tools...')).toBeVisible();
    release();
    await expect(dialog.getByRole('alert')).toContainText('Some changes may not have been saved');
    expect(puts).toBe(2);
    const rows = f.rows("SELECT tool_name, enabled FROM managed_agent_tools WHERE agent_id=? AND tool_name IN ('scratchpad','kb_list')", agent);
    expect(rows).toHaveLength(1);
    const saved = rows[0].tool_name as string;
    await expect(dialog.getByRole('switch', { name: saved, exact: true })).not.toBeChecked();
    const untouched = saved === 'scratchpad' ? 'kb_list' : 'scratchpad';
    await expect(dialog.getByRole('switch', { name: untouched, exact: true })).toBeChecked();
    expect(f.violations).toEqual([]);
  } finally { release(); await page.unrouteAll({ behavior: "ignoreErrors" }); await f.close(); }
});

test('bulk operation completes after close and reopen reads its committed result', async ({ page }) => {
  test.setTimeout(120_000);
  let release = () => {};
  const f = await claudeWorkersHarness();
  try {
    await f.open(page); const agent = await f.launch(page);
    const db = new DatabaseSync(f.router.databasePath);
    try { db.exec("PRAGMA busy_timeout=5000; UPDATE managed_tool_defaults SET category='General' WHERE tool_name IN ('scratchpad','kb_list')"); } finally { db.close(); }
    await agentTools(page);
    const dialog = page.getByRole('dialog', { name: /Manage Tools:/ });
    const section = dialog.locator('.tool-category-card').filter({ hasText: 'General' });
    await expect(section.getByRole('switch', { name: 'kb_list' })).toBeAttached();
    let observed!: () => void;
    const blocked = new Promise<void>(r => { release = r; });
    const started = new Promise<void>(r => { observed = r; });
    let puts = 0;
    await page.route(`**/v1/managed-agents/${agent}/tools`, async route => {
      if (route.request().method() === 'PUT' && ++puts === 1) { observed(); await blocked; }
      await route.continue();
    });
    await section.getByRole('button', { name: 'Revoke All' }).click();
    await waitForWrite(started); await dialog.getByRole('button', { name: 'Close worker tools' }).click();
    await agentTools(page); await expect(page.getByText('Loading worker tools...')).toBeVisible();
    release();
    await expect(dialog.getByRole('switch', { name: 'scratchpad' })).not.toBeChecked();
    await expect(dialog.getByRole('switch', { name: 'kb_list' })).not.toBeChecked();
    expect(puts).toBe(2);
    expect(f.rows("SELECT tool_name, enabled FROM managed_agent_tools WHERE agent_id=? AND tool_name IN ('scratchpad','kb_list') ORDER BY tool_name", agent)).toEqual([{tool_name:'kb_list',enabled:0},{tool_name:'scratchpad',enabled:0}]);
    expect(f.violations).toEqual([]);
  } finally { release(); await page.unrouteAll({ behavior: "ignoreErrors" }); await f.close(); }
});

test('switching agents during bulk keeps writes and displayed permissions scoped', async ({ page }) => {
  test.setTimeout(120_000);
  let release = () => {};
  const f = await claudeWorkersHarness();
  try {
    await f.open(page);
    const first = await f.launch(page);
    const second = await f.launch(page);
    const db = new DatabaseSync(f.router.databasePath);
    try { db.exec("PRAGMA busy_timeout=5000; UPDATE managed_tool_defaults SET category='General' WHERE tool_name IN ('scratchpad','kb_list')"); } finally { db.close(); }
    await page.locator(`[data-agent-id="${first}"]`).click();
    await agentTools(page);
    const dialog = page.getByRole('dialog', { name: /Manage Tools:/ });
    const section = dialog.locator('.tool-category-card').filter({ hasText: 'General' });
    await expect(section.getByRole('switch', { name: 'kb_list' })).toBeAttached();
    let observed!: () => void;
    const blocked = new Promise<void>(r => { release = r; });
    const started = new Promise<void>(r => { observed = r; });
    let puts = 0;
    await page.route(`**/v1/managed-agents/${first}/tools`, async route => {
      if (route.request().method() === 'PUT' && ++puts === 1) { observed(); await blocked; }
      await route.continue();
    });
    await section.getByRole('button', { name: 'Revoke All' }).click();
    await waitForWrite(started);
    await dialog.getByRole('button', { name: 'Close worker tools' }).click();
    await page.locator(`[data-agent-id="${second}"]`).click();
    await agentTools(page);
    await expect(dialog.getByRole('switch', { name: 'scratchpad' })).toBeChecked();
    release();
    await expect.poll(() => f.rows("SELECT count(*) AS n FROM managed_agent_tools WHERE agent_id=? AND tool_name IN ('scratchpad','kb_list') AND enabled=0", first)[0]?.n).toBe(2);
    await expect(dialog.getByRole('switch', { name: 'scratchpad' })).toBeChecked();
    expect(f.rows("SELECT tool_name FROM managed_agent_tools WHERE agent_id=?", second)).toEqual([]);
    await dialog.getByRole('button', { name: 'Close worker tools' }).click();
    await page.locator(`[data-agent-id="${first}"]`).click();
    await agentTools(page);
    await expect(dialog.getByRole('switch', { name: 'scratchpad' })).not.toBeChecked();
    await expect(dialog.getByRole('switch', { name: 'kb_list' })).not.toBeChecked();
    expect(puts).toBe(2);
    expect(f.violations).toEqual([]);
  } finally { release(); await page.unrouteAll({ behavior: 'ignoreErrors' }); await f.close(); }
});

test('global partial bulk failure survives close and reopen with saved settings', async ({ page }) => {
  test.setTimeout(120_000);
  let release = () => {};
  const f = await claudeWorkersHarness();
  try {
    await f.open(page);
    const db = new DatabaseSync(f.router.databasePath);
    try { db.exec("PRAGMA busy_timeout=5000; UPDATE managed_tool_defaults SET category='Global Test' WHERE tool_name IN ('scratchpad','kb_list')"); } finally { db.close(); }
    await globalTools(page);
    const dialog = page.getByRole('dialog', { name: 'Global Tool Defaults' });
    const section = dialog.locator('.tool-category-card').filter({ hasText: 'Global Test' });
    await expect(section.getByRole('switch', { name: 'kb_list' })).toBeAttached();
    let observed!: () => void;
    const blocked = new Promise<void>(r => { release = r; });
    const started = new Promise<void>(r => { observed = r; });
    let puts = 0;
    await page.route('**/v1/settings/tools', async route => {
      if (route.request().method() !== 'PUT') return route.continue();
      puts++;
      if (puts === 1) { observed(); await blocked; await route.continue(); }
      else if (puts === 2) await route.fulfill({ status: 500, contentType: 'application/json', body: '{"error":"injected failure"}' });
      else await route.continue();
    });
    await section.getByRole('button', { name: 'Disable All' }).click();
    await waitForWrite(started);
    await dialog.getByRole('button', { name: 'Close tool defaults' }).click();
    await globalTools(page);
    await expect(page.getByText('Loading tool defaults...')).toBeVisible();
    release();
    await expect(dialog.getByRole('alert')).toContainText('Some changes may not have been saved');
    const rows = f.rows("SELECT tool_name, enabled FROM managed_tool_defaults WHERE tool_name IN ('scratchpad','kb_list') ORDER BY tool_name");
    expect(rows.map(row => row.enabled).sort()).toEqual([0, 1]);
    for (const row of rows) {
      const toggle = dialog.getByRole('switch', { name: String(row.tool_name), exact: true });
      if (row.enabled) await expect(toggle).toBeChecked(); else await expect(toggle).not.toBeChecked();
    }
    expect(puts).toBe(2);
    expect(f.violations).toEqual([]);
  } finally { release(); await page.unrouteAll({ behavior: 'ignoreErrors' }); await f.close(); }
});
