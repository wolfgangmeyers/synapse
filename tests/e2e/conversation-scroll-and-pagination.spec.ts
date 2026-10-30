import { test, expect } from "@playwright/test";
import { setupE2EHarness, type E2ETestHarness } from "./harness";
import { WebSocket } from "ws";

test.describe("Conversation Scroll Anchoring, Proximity Auto-Scroll, and Natural Unload E2E", () => {
  let harness: E2ETestHarness;

  test.beforeAll(async () => {
    harness = await setupE2EHarness({ useProxy: true });
  });

  test.afterAll(async () => {
    if (harness) {
      await harness.close();
    }
  });

  test("SCN-SCROLL-001..004 & SCN-UNLOAD-001..002: proximity auto-scroll, read locking, agent switch reset, and 150-message unload threshold", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;
    const agentId1 = "agent-scroll-worker-1";
    const agentId2 = "agent-scroll-worker-2";

    // 1. Connect agent 1 via real WebSocket with auto-ack
    const agent1Ws = new WebSocket(`${targetRouterUrl.replace(/^http/, "ws")}/v1/agents`);
    const agent1InstanceId = "inst-" + Date.now();
    let agent1LeaseId = "";

    const agent1Ready = new Promise<void>((resolve, reject) => {
      agent1Ws.on("open", () => {
        agent1Ws.send(
          JSON.stringify({
            version: "v1",
            frame_id: "agent-reg-scroll-1",
            type: "register",
            agent_id: agentId1,
            instance_id: agent1InstanceId,
            agent_role: "agent",
            client_type: "test",
            ack_confirmation: "correlated_v1",
            conversation_sync: "v1",
            delivery_receipts: "delivery_items_v1",
          })
        );
      });
      agent1Ws.on("message", (data) => {
        try {
          const parsed = JSON.parse(data.toString());
          if (parsed.type === "registered") {
            agent1LeaseId = parsed.lease_id;
            resolve();
          } else if (parsed.type === "message") {
            // Mailbox V2 delivery lifecycle: send injection_requested, then wait for ack / send ack sequentially
            const receiptFrame = {
              version: "v1",
              agent_id: agentId1,
              instance_id: agent1InstanceId,
              lease_id: agent1LeaseId,
              message_id: parsed.message.id,
              attempt_number: parsed.attempt_number,
              ...(parsed.delivery_items ? { delivery_items: parsed.delivery_items } : {}),
            };
            agent1Ws.send(
              JSON.stringify({
                ...receiptFrame,
                frame_id: "inj-" + Date.now() + "-" + Math.random(),
                type: "injection_requested",
              })
            );
            setTimeout(() => {
              agent1Ws.send(
                JSON.stringify({
                  ...receiptFrame,
                  frame_id: "ack-" + Date.now() + "-" + Math.random(),
                  type: "ack",
                })
              );
            }, 10);
          } else if (parsed.type === "error") {
            console.log("Agent 1 WS error frame:", parsed);
          } else {
            console.log("Agent 1 WS received frame:", parsed.type);
          }
        } catch {}
      });
      agent1Ws.on("error", reject);
    });
    await agent1Ready;

    // 2. Connect agent 2 via real WebSocket with auto-ack
    const agent2Ws = new WebSocket(`${targetRouterUrl.replace(/^http/, "ws")}/v1/agents`);
    const agent2InstanceId = "inst-" + (Date.now() + 1);
    let agent2LeaseId = "";

    const agent2Ready = new Promise<void>((resolve, reject) => {
      agent2Ws.on("open", () => {
        agent2Ws.send(
          JSON.stringify({
            version: "v1",
            frame_id: "agent-reg-scroll-2",
            type: "register",
            agent_id: agentId2,
            instance_id: agent2InstanceId,
            agent_role: "agent",
            client_type: "test",
            ack_confirmation: "correlated_v1",
            conversation_sync: "v1",
            delivery_receipts: "delivery_items_v1",
          })
        );
      });
      agent2Ws.on("message", (data) => {
        try {
          const parsed = JSON.parse(data.toString());
          if (parsed.type === "registered") {
            agent2LeaseId = parsed.lease_id;
            resolve();
          } else if (parsed.type === "message") {
            const receiptFrame = {
              version: "v1",
              agent_id: agentId2,
              instance_id: agent2InstanceId,
              lease_id: agent2LeaseId,
              message_id: parsed.message.id,
              attempt_number: parsed.attempt_number,
              ...(parsed.delivery_items ? { delivery_items: parsed.delivery_items } : {}),
            };
            agent2Ws.send(
              JSON.stringify({
                ...receiptFrame,
                frame_id: "inj-" + Date.now() + "-" + Math.random(),
                type: "injection_requested",
              })
            );
            setTimeout(() => {
              agent2Ws.send(
                JSON.stringify({
                  ...receiptFrame,
                  frame_id: "ack-" + Date.now() + "-" + Math.random(),
                  type: "ack",
                })
              );
            }, 10);
          }
        } catch {}
      });
      agent2Ws.on("error", reject);
    });
    await agent2Ready;

    // 3. Pre-seed agent 1 with 20 messages so the feed is scrollable
    for (let i = 1; i <= 20; i++) {
      const res = await fetch(`${targetRouterUrl}/v1/messages`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: `msg-scroll-seed-${i}`,
          from: i % 2 === 0 ? "user" : agentId1,
          to: i % 2 === 0 ? agentId1 : "user",
          body: `Seed message #${i} with some paragraph content to create viewport height in conversation feed.`,
          created_at: new Date(Date.now() - (25 - i) * 1000).toISOString(),
          source: { kind: i % 2 === 0 ? "web_ui" : "agent" },
        }),
      });
      expect([200, 201]).toContain(res.status);
    }

    // 4. Configure localStorage for Web UI to target test router proxy and select agent 1
    await page.goto(harness.frontend.url);
    await page.evaluate(
      ({ routerUrl, selectedAgent }) => {
        localStorage.setItem("mecha.web.activeRouter", routerUrl);
        localStorage.setItem("mecha.web.routers", JSON.stringify([routerUrl]));
        localStorage.setItem(
          "mecha.web.selectedPeers",
          JSON.stringify({ [`${routerUrl}\u001fuser`]: selectedAgent })
        );
      },
      { routerUrl: targetRouterUrl, selectedAgent: agentId1 }
    );

    // 5. Reload to initialize router connection with configured storage
    await page.reload();

    const messagesContainer = page.locator(".messages");
    await expect(messagesContainer).toBeVisible({ timeout: 5000 });

    // Verify initial messages rendered
    await expect(page.locator(".bubble-wrap").filter({ hasText: "Seed message #20" })).toBeVisible({
      timeout: 5000,
    });

    // -------------------------------------------------------------------------
    // Scenario 1: SCN-SCROLL-001 Auto-scroll followership when near bottom
    // -------------------------------------------------------------------------
    // Near bottom means distance <= 180px
    await expect(async () => {
      const isNearBottom = await messagesContainer.evaluate((el) => {
        const distance = el.scrollHeight - (el.scrollTop + el.clientHeight);
        return distance <= 180;
      });
      expect(isNearBottom).toBe(true);
    }).toPass({ timeout: 5000 });

    // Send a new live message via REST
    await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        id: "msg-scroll-live-bottom-1",
        from: agentId1,
        to: "user",
        body: "Live message while near bottom should follow to bottom.",
        created_at: new Date().toISOString(),
        source: { kind: "agent" },
      }),
    });

    const liveBubble1 = page.locator(".bubble-wrap").filter({
      hasText: "Live message while near bottom should follow to bottom.",
    });
    await expect(liveBubble1.first()).toBeVisible({ timeout: 5000 });

    // Ensure it followed to bottom (distance <= 180)
    await expect(async () => {
      const isFollowed = await messagesContainer.evaluate((el) => {
        const distance = el.scrollHeight - (el.scrollTop + el.clientHeight);
        return distance <= 180;
      });
      expect(isFollowed).toBe(true);
    }).toPass({ timeout: 5000 });

    // -------------------------------------------------------------------------
    // Scenario 2: SCN-SCROLL-002 Reading position preserved when scrolled up > 180px
    // -------------------------------------------------------------------------
    // Scroll up into older history so distance > 180px
    await messagesContainer.evaluate((el) => {
      el.scrollTop = 100; // reading position near the top
    });

    await page.waitForTimeout(300);

    const distanceScrolledUp = await messagesContainer.evaluate(
      (el) => el.scrollHeight - (el.scrollTop + el.clientHeight)
    );
    expect(distanceScrolledUp).toBeGreaterThan(180);

    const savedScrollTop = await messagesContainer.evaluate((el) => el.scrollTop);

    // Send another live message while user is scrolled up reading
    await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        id: "msg-scroll-live-locked-1",
        from: agentId1,
        to: "user",
        body: "Live message while user reading should NOT snap to bottom.",
        created_at: new Date().toISOString(),
        source: { kind: "agent" },
      }),
    });

    // Wait a brief moment to allow WebSocket message receipt and DOM update
    await page.waitForTimeout(500);

    // Verify reading position preserved (scrollTop did NOT jump to scrollHeight)
    const currentScrollTop = await messagesContainer.evaluate((el) => el.scrollTop);
    expect(Math.abs(currentScrollTop - savedScrollTop)).toBeLessThan(50);

    // -------------------------------------------------------------------------
    // Scenario 3: SCN-SCROLL-003 Auto-scroll resumes when returning to bottom
    // -------------------------------------------------------------------------
    // User scrolls back down to within 180px of the bottom
    await messagesContainer.evaluate((el) => {
      el.scrollTop = el.scrollHeight - el.clientHeight;
    });

    // Trigger onScroll event handler
    await messagesContainer.dispatchEvent("scroll");
    await page.waitForTimeout(200);

    // Next live message arrives
    await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        id: "msg-scroll-live-resumed-1",
        from: agentId1,
        to: "user",
        body: "Live message after returning to bottom resumes followership.",
        created_at: new Date().toISOString(),
        source: { kind: "agent" },
      }),
    });

    const resumedBubble = page.locator(".bubble-wrap").filter({
      hasText: "Live message after returning to bottom resumes followership.",
    });
    await expect(resumedBubble.first()).toBeVisible({ timeout: 5000 });

    await expect(async () => {
      const isNear = await messagesContainer.evaluate((el) => {
        const distance = el.scrollHeight - (el.scrollTop + el.clientHeight);
        return distance <= 180;
      });
      expect(isNear).toBe(true);
    }).toPass({ timeout: 5000 });

    // -------------------------------------------------------------------------
    // Scenario 4: SCN-SCROLL-004 Clean reset on agent switch
    // -------------------------------------------------------------------------
    // Seed agent 2 with some messages
    for (let i = 1; i <= 10; i++) {
      await fetch(`${targetRouterUrl}/v1/messages`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: `msg-agent2-seed-${i}`,
          from: agentId2,
          to: "user",
          body: `Agent 2 greeting entry #${i}`,
          created_at: new Date(Date.now() - (15 - i) * 1000).toISOString(),
          source: { kind: "agent" },
        }),
      });
    }

    // Scroll agent 1 up again
    await messagesContainer.evaluate((el) => {
      el.scrollTop = 50;
    });
    await page.waitForTimeout(200);

    // Switch to Agent 2 in sidebar
    const agent2SidebarItem = page.locator(`.agent-row, .agent-row-select, [aria-label*="agent-scroll-worker-2"]`).filter({
      hasText: "Agent Scroll Worker 2",
    });
    await agent2SidebarItem.first().click();

    // Verify conversation switched to agent 2
    const agent2Header = page.locator('h2:has-text("Agent Scroll Worker 2")');
    await expect(agent2Header).toBeVisible({ timeout: 5000 });

    // Verify view automatically reset to near bottom for Agent 2
    await expect(async () => {
      const isAtBottomAgent2 = await messagesContainer.evaluate((el) => {
        const distance = el.scrollHeight - (el.scrollTop + el.clientHeight);
        return distance <= 180;
      });
      expect(isAtBottomAgent2).toBe(true);
    }).toPass({ timeout: 5000 });

    // -------------------------------------------------------------------------
    // Scenario 5: SCN-UNLOAD-001 Natural unloading when exceeding 150 messages
    // and SCN-UNLOAD-002 Re-fetching unloaded chunks via "Load older history"
    // -------------------------------------------------------------------------
    // Switch back to agent 1
    const agent1SidebarItem = page.locator(`.agent-row, .agent-row-select, [aria-label*="agent-scroll-worker-1"]`).filter({
      hasText: "Agent Scroll Worker 1",
    });
    await agent1SidebarItem.first().click();
    await expect(page.locator('h2:has-text("Agent Scroll Worker 1")')).toBeVisible({ timeout: 5000 });

    // Push 160 messages over REST in batches with small delays to exceed CONVERSATION_UNLOAD_THRESHOLD (150)
    // Alternate between "user" and agentId1 so delivery queue is promptly cleared and rate limits aren't breached
    const totalPushed = 160;
    const baseTime = Date.now();
    for (let i = 1; i <= totalPushed; i++) {
      const sender = `agent-subworker-${i % 30}`;
      const res = await fetch(`${targetRouterUrl}/v1/messages`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: `msg-unload-stream-${i}`,
          from: sender,
          to: agentId1,
          body: `Streaming message payload index ${i} for natural unload test.`,
          created_at: new Date(baseTime + i * 100).toISOString(),
          source: { kind: "agent" },
        }),
      });
      if (![200, 201].includes(res.status)) {
        const text = await res.text();
        console.log(`POST failed at i=${i} status=${res.status}: ${text}`);
      }
      expect([200, 201]).toContain(res.status);

      // Brief yield every 10 messages to let agent1 WebSocket drain delivery queue
      if (i % 10 === 0) {
        await new Promise((r) => setTimeout(r, 80));
      }
    }

    // Verify newest message is present
    await expect(
      page.locator(".bubble-wrap").filter({
        hasText: `Streaming message payload index ${totalPushed} for natural unload test.`,
      }).first()
    ).toBeVisible({ timeout: 10000 });

    // Check DOM item count in messages: should be bounded by unload threshold (<= 150 items plus headers)
    const bubbleCount = await page.locator(".bubble-wrap").count();
    expect(bubbleCount).toBeLessThanOrEqual(155);

    // Oldest messages (e.g. index 1) should naturally be trimmed from view
    const trimmedMessageLocator = page.locator(".bubble-wrap").filter({
      hasText: "Streaming message payload index 1 for natural unload test.",
    });
    expect(await trimmedMessageLocator.count()).toBe(0);

    // SCN-UNLOAD-002: hasOlder indicator / "Load older history" button remains available
    const loadOlderBtn = page.locator('.load-older-button, button:has-text("Load older history")');
    await expect(loadOlderBtn.first()).toBeVisible({ timeout: 5000 });

    // Close agent WebSockets
    agent1Ws.close();
    agent2Ws.close();
  });
});
