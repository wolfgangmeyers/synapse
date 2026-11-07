import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";

test("CD-005 rejects explicit and saved Claude RPC before reservation", async ({ page }) => {
  const f = await claudeWorkersHarness({}, { defaultModel: null });
  try {
    await f.open(page);
    const model = { provider: "claude-code", id: "sonnet" };
    for (const [name, selection] of [["explicit", { model }], ["saved", {}], ["implicit-runtime", {}]] as const) {
      if (name === "saved") expect((await f.request(page, "/v1/settings/default-model", { model }, "PUT")).status).toBe(200);
      const result = await f.request(page, "/v1/managed-agents", {
        name: `rpc-${name}`, cwd: `${f.router.root}/work`, ...(name === "implicit-runtime" ? {} : { runtime: "rpc" }), ...selection,
      });
      expect(result.status).toBe(422);
      expect(JSON.stringify(result.body)).toContain("use the embedded runtime");
      expect(f.rows("SELECT agent_id FROM managed_agents WHERE agent_id=?", `rpc-${name}`)).toEqual([]);
    }
    expect(await f.starts()).toEqual([]);
  } finally { await f.close(); }
});
