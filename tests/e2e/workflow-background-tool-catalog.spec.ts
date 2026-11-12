import { test, expect } from "@playwright/test";
import { join } from "node:path";
import { workflowHarness } from "./workflow-implicit-goals-harness";

const tools = ["launch_background_bash", "list_background_bash_jobs", "get_background_bash_job", "kill_background_bash_job"];

test("restricted workflows expose and persist background controls including catalog fallback", async ({ page }) => {
  test.setTimeout(120_000);
  const outputs: unknown[] = [];
  let jobId = "";
  const f = await workflowHarness({ inspectRequest: request => {
    const step = outputs.length;
    if (step > 0) {
      const input = request.input;
      if (!Array.isArray(input)) return { error: "missing tool result input" };
      const result = input.filter((item: Record<string, unknown>) => item.type === "function_call_output").at(-1);
      if (!result || typeof result.output !== "string") return { error: "missing tool output" };
      try { outputs.push(JSON.parse(result.output)); }
      catch { return { error: `invalid tool output: ${result.output}` }; }
    } else {
      // Mark the first request without conflating it with a tool result.
      outputs.push(null);
    }
    switch (outputs.length) {
      case 1: return { reply: { tool: tools[0], args: { command: "exec tail -f /dev/null" } } };
      case 2: {
        const launch = outputs[1] as { job_id?: unknown; status?: unknown };
        if (typeof launch?.job_id !== "string" || !launch.job_id || launch.status !== "running") return { error: `launch failed: ${JSON.stringify(launch)}` };
        jobId = launch.job_id;
        return { reply: { tool: tools[1], args: {} } };
      }
      case 3: return { reply: { tool: tools[2], args: { job_id: jobId } } };
      case 4: return { reply: { tool: tools[3], args: { job_id: jobId } } };
      case 5: return { reply: { answer: "Catalog execution complete" } };
      default: return { error: "unexpected model request" };
    }
  } });
  try {
    const catalog = await f.api("/v1/workflow-tools");
    const ids = catalog.tools.map((tool: { id: string }) => tool.id);
    for (const name of tools) expect(ids.filter((id: string) => id === name)).toHaveLength(1);
    expect(ids).not.toContain("wait_background_bash_job");
    const workflow = await f.api("/v1/workflows", { name: "Background catalog" });
    const { workflow_state: state } = await f.api("/v1/workflow-states", { workflow_id: workflow.id, name: "Restricted", allowed_tools: ["read"], allow_exit: true });
    for (const invalid of ["wait_background_bash_job", "unknown_tool"]) {
      const response = await fetch(`${f.proxy.url}/v1/workflow-states`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ workflow_id: workflow.id, name: "Invalid", allowed_tools: [invalid] }) });
      expect(response.status).toBe(400);
    }
    await f.open(page);
    await page.evaluate(() => { window.location.hash = "#/workflows"; });
    const normalCatalog = page.waitForResponse(response => response.url().endsWith("/v1/workflow-tools") && response.status() === 200);
    await page.getByText("Background catalog", { exact: true }).first().click();
    await normalCatalog;
    await page.getByRole("button", { name: "Configure Restricted" }).click();
    const dialog = page.locator("dialog.state-config-dialog[open]");
    for (const name of tools) {
      const toggle = dialog.getByTestId(`tool-toggle-${name}`);
      await expect(toggle).toBeVisible();
      await toggle.click();
    }
    await expect(dialog.getByTestId("tool-toggle-wait_background_bash_job")).toHaveCount(0);
    await dialog.getByRole("button", { name: "Save State Settings" }).click();
    await expect(dialog).toHaveCount(0);
    const saved = (await f.api(`/v1/workflow-states/${state.id}`)).workflow_state;
    expect(saved.allowed_tools.sort()).toEqual([...tools, "read"].sort());
    // Reopen with an unavailable catalog: saved selections remain visible and
    // the local fallback still offers exactly the supported background tools.
    await page.route("**/v1/workflow-tools", route => route.fulfill({ status: 503, body: "unavailable" }));
    await page.reload();
    const fallbackCatalog = page.waitForResponse(response => response.url().endsWith("/v1/workflow-tools") && response.status() === 503);
    await page.getByText("Background catalog", { exact: true }).first().click();
    await fallbackCatalog;
    await page.getByRole("button", { name: "Configure Restricted" }).click();
    for (const name of tools) await expect(dialog.getByTestId(`tool-toggle-${name}`)).toBeVisible();
    await expect(dialog.getByTestId("tool-toggle-wait_background_bash_job")).toHaveCount(0);
    await dialog.getByRole("button", { name: "Save State Settings" }).click();
    await expect(dialog).toHaveCount(0);
    expect((await f.api(`/v1/workflow-states/${state.id}`)).workflow_state.allowed_tools.sort()).toEqual([...tools, "read"].sort());
    await f.api(`/v1/workflows/${workflow.id}`, { name: workflow.name, entry_state_id: state.id }, "PUT");
    await f.api(`/v1/workflows/${workflow.id}/run`, { cwd: join(f.router.root, "work") });
    await expect.poll(() => outputs.length, { timeout: 30_000 }).toBe(5);
    const names = (f.requests[0].tools as { name: string }[]).map(t => t.name);
    for (const name of tools) expect(names).toContain(name);
    expect(names).not.toContain("bash");
    expect(names).not.toContain("wait_background_bash_job");
    expect(outputs[2]).toEqual(expect.arrayContaining([expect.objectContaining({ job_id: jobId, status: "running" })]));
    expect(outputs[3]).toMatchObject({ job_id: jobId, status: "running" });
    expect(outputs[4]).toMatchObject({ job_id: jobId, status: "killed" });
    expect(f.violations).toEqual([]);
  } finally { await f.close(); }
});
