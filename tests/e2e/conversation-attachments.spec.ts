import { test, expect, type Page } from "@playwright/test";
import { setupE2EHarness, type E2ETestHarness } from "./harness";
import { WebSocket } from "ws";

test.describe("SPEC-CONV-002: Conversation Attachments", () => {
  test.setTimeout(60_000);

  let harness: E2ETestHarness;
  let targetRouterUrl: string;
  let agentWs: WebSocket;
  let leaseRenewalTimer: NodeJS.Timeout;
  let instanceId: string;
  let leaseId: string;
  let serial = 0;

  const agentId = "agent-e2e-worker";
  // 100x100 solid color PNG
  const samplePngBase64 =
    "iVBORw0KGgoAAAANSUhEUgAAAGQAAABkCAIAAAD/gAIDAAAAtUlEQVR4nO3QQQkAIADAQHvZztC+reBHhnCwAOPGXFuXjfzgo2DBgpUHCxasPFiwYOXBggUrDxYsWHmwYMHKgwULVh4sWLDyYMGClQcLFqw8WLBg5cGCBSsPFixYebBgwcqDBQtWHixYsPJgwYKVBwsWrDxYsGDlwYIFKw8WLFh5sGDByoMFC1YeLFiw8mDBgpUHCxasPFiwYOXBggUrDxYsWHmwYMHKgwULVh4sWLDyYMF60wFjAWQPvtJeWQAAAABJRU5ErkJggg==";

  test.beforeAll(async () => {
    harness = await setupE2EHarness({ useProxy: true });
    targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;

    instanceId = "inst-" + Date.now();
    agentWs = new WebSocket(`${targetRouterUrl.replace(/^http/, "ws")}/v1/agents`);

    const agentReady = new Promise<void>((resolve, reject) => {
      agentWs.on("open", () => {
        agentWs.send(
          JSON.stringify({
            version: "v1",
            frame_id: "agent-reg-att-1",
            type: "register",
            agent_id: agentId,
            instance_id: instanceId,
            agent_role: "agent",
            client_type: "test",
            ack_confirmation: "correlated_v1",
            conversation_sync: "v1",
            delivery_receipts: "delivery_items_v1",
          })
        );
      });

      agentWs.on("message", (data) => {
        try {
          const parsed = JSON.parse(data.toString());
          if (parsed.type === "registered") {
            leaseId = parsed.lease_id;
            resolve();
          }
        } catch {}
      });

      agentWs.on("error", reject);
    });

    await agentReady;

    // Periodically renew agent lease to prevent expiry during tests
    leaseRenewalTimer = setInterval(() => {
      if (agentWs.readyState === WebSocket.OPEN && leaseId) {
        agentWs.send(
          JSON.stringify({
            version: "v1",
            frame_id: `agent-renew-${++serial}`,
            type: "lease_renew",
            agent_id: agentId,
            instance_id: instanceId,
            lease_id: leaseId,
          })
        );
      }
    }, 10_000);
  });

  test.afterAll(async () => {
    if (leaseRenewalTimer) {
      clearInterval(leaseRenewalTimer);
    }
    if (agentWs) {
      agentWs.terminate();
    }
    if (harness) {
      await harness.close();
    }
  });

  async function setupConversationWithAttachment(
    page: Page,
    messageId: string,
    caption: string,
  ) {
    // 1. Configure localStorage for Web UI to target test router proxy and select the agent
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
      { routerUrl: targetRouterUrl, selectedAgent: agentId }
    );

    // 2. Reload page to initialize router connection with configured storage
    await page.reload();

    // 3. Select active agent from sidebar and verify active agent header
    const agentRow = page.locator(`.agent-row[data-agent-id="${agentId}"] .agent-row-select`);
    await expect(agentRow).toBeVisible({ timeout: 10000 });
    await agentRow.click();

    const agentHeading = page.locator('h2:has-text("Agent E2e Worker")').first();
    await expect(agentHeading).toBeVisible({ timeout: 5000 });

    // 4. Post message with image attachment via router REST API
    const messagePayload = {
      id: messageId,
      from: agentId,
      to: "user",
      body: caption,
      created_at: new Date().toISOString(),
      attachments: [
        {
          type: "image",
          data_url: `data:image/png;base64,${samplePngBase64}`,
          name: "diagram.png",
        },
      ],
      source: { kind: "agent" },
    };

    const postRes = await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(messagePayload),
    });
    expect([200, 201]).toContain(postRes.status);

    // 5. Verify the message and thumbnail button render in the timeline
    const bubble = page.locator(".bubble-wrap.incoming").filter({
      hasText: caption,
    });
    await expect(bubble.first()).toBeVisible({ timeout: 8000 });

    const thumbBtn = bubble.locator(".attachment-thumb-btn").first();
    await expect(thumbBtn).toBeVisible({ timeout: 5000 });

    return { bubble, thumbBtn };
  }

  test("[ATT-001] opens and closes lightbox modal via thumbnail and close button", async ({
    page,
  }) => {
    const { thumbBtn } = await setupConversationWithAttachment(
      page,
      "msg-e2e-att-001",
      "Message for ATT-001 thumbnail test",
    );

    // Initial state: no lightbox in DOM
    await expect(page.locator(".image-lightbox")).toHaveCount(0);

    // Click attachment thumbnail button
    await thumbBtn.click();

    // Assert .image-lightbox is visible and direct child of body
    const lightbox = page.locator("body > .image-lightbox");
    await expect(lightbox).toBeVisible();
    await expect(lightbox).toHaveAttribute("role", "dialog");
    await expect(lightbox).toHaveAttribute("aria-modal", "true");
    await expect(lightbox).toHaveAttribute("aria-label", "Image preview");

    // Assert body scrolling is locked
    const lockedOverflow = await page.evaluate(() => document.body.style.overflow);
    expect(lockedOverflow).toBe("hidden");

    // Assert close button .image-lightbox-close is visible
    const closeBtn = lightbox.locator(".image-lightbox-close");
    await expect(closeBtn).toBeVisible();
    await expect(closeBtn).toHaveClass(/icon-btn-round/);

    // Click close button .image-lightbox-close
    await closeBtn.click();

    // Assert modal is closed / removed from DOM
    await expect(page.locator(".image-lightbox")).toHaveCount(0);

    // Assert body scrolling is restored
    const restoredOverflow = await page.evaluate(() => document.body.style.overflow);
    expect(restoredOverflow).not.toBe("hidden");
  });

  test("[ATT-002] dismisses preview modal via Escape key and backdrop click", async ({
    page,
  }) => {
    const { thumbBtn } = await setupConversationWithAttachment(
      page,
      "msg-e2e-att-002",
      "Message for ATT-002 dismissal test",
    );

    const lightbox = page.locator("body > .image-lightbox");

    // Part 1: Open modal via thumbnail and dismiss via Escape key
    await thumbBtn.click();
    await expect(lightbox).toBeVisible();
    expect(await page.evaluate(() => document.body.style.overflow)).toBe("hidden");

    // Press Escape key
    await page.keyboard.press("Escape");

    // Assert modal is closed / removed from DOM and scroll restored
    await expect(page.locator(".image-lightbox")).toHaveCount(0);
    expect(await page.evaluate(() => document.body.style.overflow)).not.toBe("hidden");

    // Part 2: Open modal again via thumbnail and dismiss via backdrop click
    await thumbBtn.click();
    await expect(lightbox).toBeVisible();
    expect(await page.evaluate(() => document.body.style.overflow)).toBe("hidden");

    // Click backdrop outside image content (top-left padding area of lightbox)
    await lightbox.click({ position: { x: 10, y: 10 } });

    // Assert modal is closed / removed from DOM and scroll restored
    await expect(page.locator(".image-lightbox")).toHaveCount(0);
    expect(await page.evaluate(() => document.body.style.overflow)).not.toBe("hidden");
  });

  test("[ATT-003] clicking image content does not dismiss modal", async ({
    page,
  }) => {
    const { thumbBtn } = await setupConversationWithAttachment(
      page,
      "msg-e2e-att-003",
      "Message for ATT-003 click propagation test",
    );

    const lightbox = page.locator("body > .image-lightbox");

    // Open modal via thumbnail
    await thumbBtn.click();
    await expect(lightbox).toBeVisible();
    expect(await page.evaluate(() => document.body.style.overflow)).toBe("hidden");

    // Click on the modal preview image
    const previewImage = lightbox.locator("img");
    await expect(previewImage).toBeVisible();
    await previewImage.click();

    // Assert modal remains visible and open
    await expect(lightbox).toBeVisible();
    expect(await page.evaluate(() => document.body.style.overflow)).toBe("hidden");

    // Clean up: dismiss via close button
    const closeBtn = lightbox.locator(".image-lightbox-close");
    await closeBtn.click();
    await expect(page.locator(".image-lightbox")).toHaveCount(0);
    expect(await page.evaluate(() => document.body.style.overflow)).not.toBe("hidden");
  });
});
