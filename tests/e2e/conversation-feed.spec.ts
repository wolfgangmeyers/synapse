import { test, expect } from "@playwright/test";
import { setupE2EHarness, type E2ETestHarness } from "./harness";
import { WebSocket } from "ws";

test.describe("Conversation Feed & Agent Interaction E2E", () => {
  let harness: E2ETestHarness;

  test.beforeAll(async () => {
    harness = await setupE2EHarness({ useProxy: true });
  });

  test.afterAll(async () => {
    if (harness) {
      await harness.close();
    }
  });

  test("App loads cleanly, connects to isolated router proxy, and renders conversation feed", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;

    // 1. Emulate a connected agent on the router via real WebSocket using register frame
    const agentId = "agent-e2e-worker";
    const agentWs = new WebSocket(`${targetRouterUrl.replace(/^http/, "ws")}/v1/agents`);

    const agentReady = new Promise<void>((resolve, reject) => {
      agentWs.on("open", () => {
        agentWs.send(
          JSON.stringify({
            version: "v1",
            frame_id: "agent-reg-1",
            type: "register",
            agent_id: agentId,
            instance_id: "inst-" + Date.now(),
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
            resolve();
          }
        } catch {}
      });
      agentWs.on("error", reject);
    });

    await agentReady;

    // 2. Configure localStorage for the Web UI to target this test router proxy
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

    // 3. Reload page to initialize router connection with configured storage
    await page.reload();

    // 4. Verify UI shell and active router selector in the sidebar
    const routerSelector = page.locator('.router-selector');
    await expect(routerSelector).toBeVisible({ timeout: 5000 });
    await expect(page.locator('.router-selector-subtitle')).toHaveText(/Signed in as user/);

    // 5. Verify the connected agent appears in the sidebar and is selected
    const agentHeading = page.locator('h2:has-text("Agent E2e Worker")');
    await expect(agentHeading).toBeVisible({ timeout: 5000 });

    // 6. Post a message to the agent via router REST API to verify live timeline feed rendering
    const messagePayload = {
      id: "msg-e2e-001",
      from: agentId,
      to: "user",
      body: "Hello Wolfgang, I am ready for instructions.",
      created_at: new Date().toISOString(),
      source: { kind: "agent" },
    };

    const postRes = await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(messagePayload),
    });
    expect([200, 201]).toContain(postRes.status);

    // 7. Verify the message arrives and renders in the conversation timeline as an assistant speech bubble
    const assistantBubble = page.locator('.bubble-wrap.incoming').filter({
      hasText: "Hello Wolfgang, I am ready for instructions.",
    });
    await expect(assistantBubble.first()).toBeVisible({ timeout: 8000 });
    // Verify it is NOT an envelope card or tool activity box
    await expect(assistantBubble.locator('.activity-entry')).toHaveCount(0);

    // 8. User sends a speech bubble message via the composer
    const composerTextarea = page.locator('.composer-textarea, textarea[placeholder*="message"], textarea');
    await expect(composerTextarea.first()).toBeVisible();
    await composerTextarea.first().fill("Please review the auth PR.");
    await page.keyboard.press("Enter");

    // Verify user message renders as an outgoing speech bubble
    const userBubble = page.locator('.bubble-wrap.outgoing').filter({
      hasText: "Please review the auth PR.",
    });
    await expect(userBubble.first()).toBeVisible({ timeout: 8000 });

    // 9. Verify inter-agent mailbox message renders as envelope card with compact subject header (not bloated title)
    const mailboxMessagePayload = {
      id: "msg-e2e-peer-001",
      from: agentId,
      to: "agent-peer-reviewer",
      subject: "Review Auth Schema",
      body: "This is a detailed multi-paragraph message body that shouldn't bloat the header title.\n\nSecond paragraph with more information.",
      created_at: new Date().toISOString(),
      source: { kind: "agent" },
    };

    const peerPostRes = await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(mailboxMessagePayload),
    });
    expect([200, 201]).toContain(peerPostRes.status);

    // Verify envelope card renders with compact subject header
    const envelopeCard = page.locator('.activity-entry').filter({
      hasText: "Review Auth Schema",
    });
    await expect(envelopeCard.first()).toBeVisible({ timeout: 8000 });

    // Header label should show recipient/sender and subject cleanly without whole body in header title
    const envelopeSubject = envelopeCard.locator('.activity-entry-subject');
    await expect(envelopeSubject).toHaveText(/• Review Auth Schema/);
    const envelopeHeaderLabel = envelopeCard.locator('.activity-entry-label strong');
    await expect(envelopeHeaderLabel).toHaveText(/To agent-peer-reviewer/);

    // 10. Verify message with attachment renders image gallery
    const transparent1pxPng = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";
    const attachmentMessagePayload = {
      id: "msg-e2e-attachment-001",
      from: agentId,
      to: "user",
      body: "Here is the visual diagram.",
      created_at: new Date().toISOString(),
      attachments: [
        {
          type: "image",
          data_url: transparent1pxPng,
          name: "sample-diagram.png",
        },
      ],
      source: { kind: "agent" },
    };

    const attachPostRes = await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(attachmentMessagePayload),
    });
    expect([200, 201]).toContain(attachPostRes.status);

    const attachmentBubble = page.locator('.bubble-wrap.incoming').filter({
      hasText: "Here is the visual diagram.",
    });
    await expect(attachmentBubble.first()).toBeVisible({ timeout: 8000 });
    // Attachment gallery item should be rendered
    const galleryItem = attachmentBubble.locator('.bubble-images, .attachment-image-button, img[alt*="Attachment"], img[alt*="sample-diagram"]');
    await expect(galleryItem.first()).toBeVisible({ timeout: 5000 });

    // 11. Verify stable scroll position on the timeline
    const messagesContainer = page.locator('.messages');
    await expect(messagesContainer).toBeVisible();

    // Check scrollTop is anchored near bottom
    await expect(async () => {
      const scrollInfo = await messagesContainer.evaluate((el) => {
        return {
          scrollHeight: el.scrollHeight,
          scrollTop: el.scrollTop,
          clientHeight: el.clientHeight,
          diff: el.scrollHeight - el.scrollTop - el.clientHeight,
        };
      });
      console.log("SCROLL INFO 2:", scrollInfo);
      const isAtBottom = scrollInfo.diff < 50;
      expect(isAtBottom).toBe(true);
    }).toPass({ timeout: 5000 });

    // 12. SCN-FEED-001 Verification: Real-time event push over WebSocket without page reload
    // Send a live message directly over the router while the page remains active
    const liveMessagePayload = {
      id: "msg-e2e-live-ws-002",
      from: agentId,
      to: "user",
      body: "REALTIME_WS_PUSH_VERIFICATION_MESSAGE",
      created_at: new Date().toISOString(),
      source: { kind: "agent" },
    };

    const liveRes = await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(liveMessagePayload),
    });
    expect([200, 201]).toContain(liveRes.status);

    // Verify it immediately appears in the DOM without any page.reload() call!
    const liveBubble = page.locator('.bubble-wrap.incoming').filter({
      hasText: "REALTIME_WS_PUSH_VERIFICATION_MESSAGE",
    });
    await expect(liveBubble.first()).toBeVisible({ timeout: 5000 });

    // Cleanup agent WS
    agentWs.close();
  });
});
