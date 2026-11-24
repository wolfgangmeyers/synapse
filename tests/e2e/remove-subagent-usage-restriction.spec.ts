import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import { claudeWorkersHarness } from "./claude-workers-harness";
import { model } from "./workflow-implicit-goals-harness";
import { startIsolatedRouter, startRouterProxy } from "./harness";

// SPEC-SUBAGENT-RESTRICTION-REMOVAL-001: Subagent usage restriction removal.
// Verifies:
// 1. Newly launched managed agents default to allow_inline_tools = 1 in DB and true in API projection.
// 2. Explicitly setting allow_inline_tools: false persists allow_inline_tools = 0 and false in API projection.
// 3. Embedded tools list (/v1/tools/catalog) for mecha_request_inline_tool_allowance contains non-threatening text.
test("managed agents default to allow_inline_tools=true, respect false flag, and tool definitions are non-threatening", async () => {
  const f = await claudeWorkersHarness();
  try {
    // 1. Create managed agent via POST /v1/managed-agents without specifying inline tool options:
    //    - Database has allow_inline_tools = 1
    //    - GET /v1/managed-agents/:name returns allow_inline_tools: true
    const defaultAgent = `agent-default-${randomUUID()}`;
    await f.api("/v1/managed-agents", {
      name: defaultAgent,
      cwd: `${f.router.root}/work`,
      runtime: "embedded",
      model,
    });

    const defaultDbRows = f.rows("SELECT allow_inline_tools FROM managed_agents WHERE agent_id=?", defaultAgent);
    expect(defaultDbRows).toEqual([{ allow_inline_tools: 1 }]);

    const defaultAgentRes = await f.api(`/v1/managed-agents/${defaultAgent}`, undefined, "GET");
    expect(defaultAgentRes).toBeDefined();
    expect(defaultAgentRes.agent).toBeDefined();
    expect(defaultAgentRes.agent.allow_inline_tools).toBe(true);

    // 2. Create managed agent via POST /v1/managed-agents with allow_inline_tools: false:
    //    - Database has allow_inline_tools = 0
    //    - GET /v1/managed-agents/:name returns allow_inline_tools: false
    const restrictedAgent = `agent-restricted-${randomUUID()}`;
    await f.api("/v1/managed-agents", {
      name: restrictedAgent,
      cwd: `${f.router.root}/work`,
      runtime: "embedded",
      model,
      allow_inline_tools: false,
    });

    const restrictedDbRows = f.rows("SELECT allow_inline_tools FROM managed_agents WHERE agent_id=?", restrictedAgent);
    expect(restrictedDbRows).toEqual([{ allow_inline_tools: 0 }]);

    const restrictedAgentRes = await f.api(`/v1/managed-agents/${restrictedAgent}`, undefined, "GET");
    expect(restrictedAgentRes).toBeDefined();
    expect(restrictedAgentRes.agent).toBeDefined();
    expect(restrictedAgentRes.agent.allow_inline_tools).toBe(false);

    // 3. Embedded tools list (/v1/tools/catalog or embedded tool definitions) for mecha_request_inline_tool_allowance
    //    contains non-threatening text (e.g. does not say "Do not abuse the inline tool allowance, or it will be disabled").
    const catalogRes = await f.api("/v1/tools/catalog", undefined, "GET");
    expect(catalogRes).toBeDefined();
    expect(Array.isArray(catalogRes.tools)).toBe(true);

    const allowanceTool = catalogRes.tools.find(
      (t: { tool_name?: string; name?: string }) => (t.tool_name || t.name) === "mecha_request_inline_tool_allowance"
    );
    expect(allowanceTool).toBeDefined();

    const toolText = JSON.stringify(allowanceTool);
    expect(toolText).not.toContain("Do not abuse the inline tool allowance, or it will be disabled");
    expect(toolText.toLowerCase()).not.toContain("do not abuse");
    expect(toolText.toLowerCase()).not.toContain("abusing");
    expect(toolText.toLowerCase()).not.toContain("will be disabled");

    // Verify it contains a clear and non-threatening description
    expect(typeof allowanceTool.description).toBe("string");
    expect(allowanceTool.description.length).toBeGreaterThan(0);

    // Also verify per-agent tools endpoint reflects the same non-threatening text
    const agentToolsRes = await f.api(`/v1/managed-agents/${defaultAgent}/tools`, undefined, "GET");
    const agentAllowanceTool = agentToolsRes.tools?.find(
      (t: { tool_name?: string; name?: string }) => (t.tool_name || t.name) === "mecha_request_inline_tool_allowance"
    );
    expect(agentAllowanceTool).toBeDefined();
    expect(JSON.stringify(agentAllowanceTool).toLowerCase()).not.toContain("do not abuse");
    expect(JSON.stringify(agentAllowanceTool).toLowerCase()).not.toContain("will be disabled");
  } finally {
    await f.close();
  }
});
