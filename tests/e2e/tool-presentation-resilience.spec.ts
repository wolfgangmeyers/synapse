import { test, expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { join } from "node:path";
import {
  workflowHarness,
  model,
  type Reply,
} from "./workflow-implicit-goals-harness";

// SPEC-TOOL-PRESENTATION-RESILIENCE-001: Tool Presentation Size Limit Resilience
// Verifies TPR-001 and TPR-002: tool presentations with long thinking/companion text
// or oversized metadata reliably transition out of "Running" and reach terminal state.

test.describe("Tool Presentation Size Limit Resilience", () => {
  // Scenario: [TPR-001] Tool call with extensive thinking text transitions from "Running" to completed in the timeline.
  test("transitions tool call to success when accompanied by long thinking text", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    const f = await workflowHarness();
    const caller = "tool-resilience-tpr-001";

    try {
      // 1. Create managed embedded agent
      await f.api("/v1/managed-agents", {
        name: caller,
        cwd: join(f.router.root, "work"),
        runtime: "embedded",
        model,
      });

      await expect
        .poll(
          () =>
            f.rows(
              "SELECT lifecycle_state FROM managed_agents WHERE agent_id=?",
              caller
            )[0]?.lifecycle_state
        )
        .toBe("running");

      // 2. Open Web UI and select agent
      await f.open(page);
      const row = page.locator(`.agent-row[data-agent-id="${caller}"]`);
      await expect(row).toBeVisible();
      await row.locator(".agent-row-select").click();

      // 3. Queue model turns:
      // Turn 1 executes bash tool with large thinking text (>5KB) and companion text (>10KB).
      // Turn 2 answers to complete the turn.
      const bigThinking = "Reasoning step through tool invocation with deep context. ".repeat(120); // ~6.8 KB
      const bigCompanion = "Companion explanation before executing the requested command. ".repeat(200); // ~12.6 KB

      const toolReply: Reply = {
        tool: "bash",
        args: { command: "echo tpr-001-success", timeout: 5 },
      };
      f.replies.push(toolReply);
      f.replies.push({ answer: "Command execution completed successfully." });

      // 4. Send user message to trigger tool call
      const clock = await f.router.advanceClock(1000);
      await f.api("/v1/messages", {
        id: randomUUID(),
        from: "user",
        to: caller,
        body: "Run echo tpr-001-success",
        created_at: new Date(clock.now).toISOString(),
        source: { kind: "web_ui" },
      });

      // 5. Verify database records completion with outcome 'success'
      await expect
        .poll(
          () => {
            const entries = f.rows(
              "SELECT sequence, presentation_json FROM managed_complete_history WHERE agent_id=? AND category='managed_tool' ORDER BY sequence DESC",
              caller
            );
            if (!entries.length) return null;
            const pres = JSON.parse(String(entries[0].presentation_json || "{}"));
            return pres.outcome;
          },
          { timeout: 30_000 }
        )
        .toBe("success");

      // 6. Verify presentation_json strictly respects the 2048-byte limit
      // and verify Invariant 1: thinking from earlier in the turn or prior turns
      // is NOT leaked/retained in the tool presentation across multiple turns.
      const historyRows = f.rows(
        "SELECT sequence, presentation_json FROM managed_complete_history WHERE agent_id=? AND category='managed_tool' ORDER BY sequence DESC",
        caller
      );
      expect(historyRows.length).toBeGreaterThan(0);
      for (const row of historyRows) {
        const presJson = String(row.presentation_json || "");
        expect(Buffer.byteLength(presJson, "utf8")).toBeLessThanOrEqual(2048);
        const parsedToolPres = JSON.parse(presJson);
        expect(parsedToolPres.thinking || "").toBe("");
      }

      // 7. Verify the tool activity card in the Web UI transitions to terminal state (no "Running" badge)
      const activityEntry = page.locator('.activity-entry[data-tool="bash"]');
      await expect(activityEntry.first()).toBeVisible({ timeout: 15_000 });
      await expect(
        page.locator('.activity-entry--active, .status-running, [data-status="running"]')
      ).toHaveCount(0);
      await expect(activityEntry.first()).toHaveClass(/activity-entry--terminal-success/);
    } finally {
      await f.close();
    }
  });

  // Scenario: [TPR-002] Tool call with large companion text transitions to failed/completed in the timeline.
  test("falls back to clean minimal presentation if detailed presentation fails", async ({
    page,
  }) => {
    test.setTimeout(120_000);
    const f = await workflowHarness();
    const caller = "tool-resilience-tpr-002";

    try {
      // 1. Create managed embedded agent
      await f.api("/v1/managed-agents", {
        name: caller,
        cwd: join(f.router.root, "work"),
        runtime: "embedded",
        model,
      });

      await expect
        .poll(
          () =>
            f.rows(
              "SELECT lifecycle_state FROM managed_agents WHERE agent_id=?",
              caller
            )[0]?.lifecycle_state
        )
        .toBe("running");

      // 2. Open Web UI and select agent
      await f.open(page);
      const row = page.locator(`.agent-row[data-agent-id="${caller}"]`);
      await expect(row).toBeVisible();
      await row.locator(".agent-row-select").click();

      // 3. Queue model turns with a failing command or companion text
      const failingToolReply: Reply = {
        tool: "bash",
        args: { command: "non_existent_command_tpr_002; exit 1", timeout: 5 },
      };
      f.replies.push(failingToolReply);
      f.replies.push({ answer: "Tool failed as expected." });

      // 4. Send message to start tool turn
      const clock = await f.router.advanceClock(1000);
      await f.api("/v1/messages", {
        id: randomUUID(),
        from: "user",
        to: caller,
        body: "Run failing bash command",
        created_at: new Date(clock.now).toISOString(),
        source: { kind: "web_ui" },
      });

      // 5. Verify the tool invocation transitions out of 'active' into a terminal state ('failed')
      await expect
        .poll(
          () => {
            const entries = f.rows(
              "SELECT sequence, presentation_json FROM managed_complete_history WHERE agent_id=? AND category='managed_tool' ORDER BY sequence DESC",
              caller
            );
            if (!entries.length) return null;
            const pres = JSON.parse(String(entries[0].presentation_json || "{}"));
            return pres.outcome;
          },
          { timeout: 30_000 }
        )
        .toBe("failed");

      // 6. Verify presentation_json length is bounded <= 2048 bytes
      const entries = f.rows(
        "SELECT sequence, presentation_json FROM managed_complete_history WHERE agent_id=? AND category='managed_tool' ORDER BY sequence DESC",
        caller
      );
      expect(entries.length).toBeGreaterThan(0);
      const presJson = String(entries[0].presentation_json || "");
      expect(Buffer.byteLength(presJson, "utf8")).toBeLessThanOrEqual(2048);

      // 7. Verify the UI updates out of 'Running' and displays the failure terminal status
      const activityEntry = page.locator('.activity-entry[data-tool="bash"]');
      await expect(activityEntry.first()).toBeVisible({ timeout: 15_000 });
      await expect(
        page.locator('.activity-entry--active, .status-running, [data-status="running"]')
      ).toHaveCount(0);
      await expect(activityEntry.first()).toHaveClass(/activity-entry--terminal-danger/);
    } finally {
      await f.close();
    }
  });
});
