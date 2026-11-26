import { test, expect } from "@playwright/test";
import { workflowHarness } from "./workflow-implicit-goals-harness";

test.describe("Server-Driven UI (Block Kit) API & Layout Contracts", () => {
  test("bootstraps root surface and hydrates remote slots", async () => {
    const f = await workflowHarness();
    try {
      const resp = await fetch(`${f.proxy.url}/v1/ui/bootstrap`);
      expect(resp.status).toBe(200);
      const data = await resp.json();

      expect(data.surface_id).toBe("root");
      expect(data.blocks).toBeInstanceOf(Array);

      const slotBlocks = data.blocks.filter((b: any) => b.type === "remote_slot");
      expect(slotBlocks.length).toBeGreaterThanOrEqual(2);

      const sidebarSlot = slotBlocks.find((b: any) => b.slot_id === "sidebar_slot");
      expect(sidebarSlot).toBeDefined();
      expect(sidebarSlot.view_url).toContain("/v1/ui/views/sidebar");

      const mainSlot = slotBlocks.find((b: any) => b.slot_id === "main_slot");
      expect(mainSlot).toBeDefined();
      expect(mainSlot.view_url).toContain("/v1/ui/views/agents/");

      const resolveUrl = (url: string) => url.startsWith("http") ? url : `${f.proxy.url}${url.startsWith("/") ? "" : "/"}${url}`;

      // Hydrate sidebar slot
      const sidebarResp = await fetch(resolveUrl(sidebarSlot.view_url));
      expect(sidebarResp.status).toBe(200);
      const sidebarData = await sidebarResp.json();
      expect(sidebarData.surface_id).toBe("sidebar");
      expect(sidebarData.blocks.length).toBeGreaterThan(0);

      // Hydrate main slot
      const mainResp = await fetch(resolveUrl(mainSlot.view_url));
      expect(mainResp.status).toBe(200);
      const mainData = await mainResp.json();
      expect(mainData.surface_id).toContain("conversation_");
      expect(mainData.blocks.length).toBeGreaterThan(0);
    } finally {
      await f.close();
    }
  });

  test("switches target slot conversation on peer select", async () => {
    const f = await workflowHarness();
    try {
      const actionPayload = {
        action_id: "select_peer",
        block_id: "peer_worker_beta",
        value: "worker-beta",
        slot_id: "main_slot",
      };

      const resp = await fetch(`${f.proxy.url}/v1/ui/action`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(actionPayload),
      });

      expect(resp.status).toBe(200);
      const data = await resp.json();
      expect(data.response_action).toBe("update_slot");
      expect(data.slot_id).toBe("main_slot");
      expect(data.view_url).toBe("/v1/ui/views/agents/worker-beta/conversation");
    } finally {
      await f.close();
    }
  });

  test("dispatches send action and clears composer input", async () => {
    const f = await workflowHarness();
    try {
      const actionPayload = {
        action_id: "send_message",
        agent_id: "alpha",
        state: {
          values: {
            composer_input: {
              message_text: "Test instruction message",
            },
          },
        },
      };

      const resp = await fetch(`${f.proxy.url}/v1/ui/action`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(actionPayload),
      });

      expect(resp.status).toBe(200);
      const data = await resp.json();
      expect(data.response_action).toBe("clear_inputs");
      expect(data.clear_inputs).toContain("composer_input");
    } finally {
      await f.close();
    }
  });

  test("renders inline field errors from error response", async () => {
    const f = await workflowHarness();
    try {
      const actionPayload = {
        action_id: "save_config",
        agent_id: "alpha",
        state: {
          values: {
            cfg_name: {
              name_input: "",
            },
            cfg_temperature: {
              temperature_input: "2.5",
            },
          },
        },
      };

      const resp = await fetch(`${f.proxy.url}/v1/ui/action`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(actionPayload),
      });

      expect(resp.status).toBe(200);
      const data = await resp.json();
      expect(data.response_action).toBe("errors");
      expect(data.errors).toBeDefined();
      expect(data.errors.cfg_name).toBe("Agent name cannot be empty");
      expect(data.errors.cfg_temperature).toBe("Temperature must be between 0.0 and 1.0");
    } finally {
      await f.close();
    }
  });

  test("pushes modal surface on navigation action", async () => {
    const f = await workflowHarness();
    try {
      const actionPayload = {
        action_id: "open_config",
        agent_id: "alpha",
      };

      const resp = await fetch(`${f.proxy.url}/v1/ui/action`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(actionPayload),
      });

      expect(resp.status).toBe(200);
      const data = await resp.json();
      expect(data.response_action).toBe("push");
      expect(data.surface).toBeDefined();
      expect(data.surface.surface_id).toBe("config_alpha");
      expect(data.surface.title).toBe("Agent Configuration");
      expect(data.surface.blocks.length).toBeGreaterThan(0);
    } finally {
      await f.close();
    }
  });

  test("updates surface in-place on update response", async () => {
    const f = await workflowHarness();
    try {
      const actionPayload = {
        action_id: "trigger_update",
      };

      const resp = await fetch(`${f.proxy.url}/v1/ui/action`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(actionPayload),
      });

      expect(resp.status).toBe(200);
      const data = await resp.json();
      expect(data.response_action).toBe("update");
      expect(data.surface).toBeDefined();
      expect(data.surface.surface_id).toBe("root");
    } finally {
      await f.close();
    }
  });
});
