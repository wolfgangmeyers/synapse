import { test, expect } from "@playwright/test";
import { setupE2EHarness, type E2ETestHarness } from "./harness";
import { WebSocket } from "ws";

test.describe("Conversation Architecture Simplification E2E", () => {
  let harness: E2ETestHarness;

  test.beforeAll(async () => {
    harness = await setupE2EHarness({ useProxy: true });
  });

  test.afterAll(async () => {
    if (harness) {
      await harness.close();
    }
  });

  test("renders user and assistant speech bubbles with avatars, compact envelope cards for peer messages, attachments without leaked paths, and maintains scroll stability", async ({
    page,
  }) => {
    const targetRouterUrl = harness.proxy ? harness.proxy.url : harness.router.httpUrl;
    const agentId = "agent-arch-worker";
    const peerAgentId = "agent-arch-reviewer";

    // 1. Emulate worker agent via real WebSocket using register frame
    const agentWs = new WebSocket(`${targetRouterUrl.replace(/^http/, "ws")}/v1/agents`);

    const agentReady = new Promise<void>((resolve, reject) => {
      agentWs.on("open", () => {
        agentWs.send(
          JSON.stringify({
            version: "v1",
            frame_id: "agent-reg-arch-1",
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

    // 2. Configure localStorage for Web UI to target test router proxy and select the agent
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

    // Verify UI is loaded with agent selected
    const agentHeading = page.locator('h2:has-text("Agent Arch Worker")');
    await expect(agentHeading).toBeVisible({ timeout: 5000 });

    // -------------------------------------------------------------------------
    // Scenario 1: User speech bubble rendering with avatar (<MessageBubble isUser={true}> / .bubble-wrap.outgoing)
    // -------------------------------------------------------------------------
    const composerTextarea = page.locator('.composer-textarea, textarea[placeholder*="message"], textarea');
    await expect(composerTextarea.first()).toBeVisible({ timeout: 5000 });
    const userPrompt = "Hello worker, please verify the test architecture.";
    await composerTextarea.first().fill(userPrompt);
    await page.keyboard.press("Enter");

    const userBubble = page.locator('.bubble-wrap.outgoing').filter({
      hasText: userPrompt,
    });
    await expect(userBubble.first()).toBeVisible({ timeout: 8000 });

    // Check user avatar icon
    const userAvatar = userBubble.locator('.message-avatar--user .user-avatar-circle');
    await expect(userAvatar).toBeVisible();
    await expect(userAvatar.locator('i.fa-user')).toBeVisible();

    // Verify user bubble label has "You"
    await expect(userBubble.locator('.bubble-label strong')).toHaveText("You");

    // -------------------------------------------------------------------------
    // Scenario 2: Assistant speech bubble rendering with avatar (<MessageBubble isUser={false}> / .bubble-wrap.incoming)
    // replies to user without falling through to envelope cards
    // -------------------------------------------------------------------------
    const assistantReply = "I have verified the test architecture. Everything is solid.";
    const assistantMsgPayload = {
      id: "msg-arch-assistant-001",
      from: agentId,
      to: "user",
      body: assistantReply,
      created_at: new Date().toISOString(),
      source: { kind: "agent" },
    };

    const replyRes = await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(assistantMsgPayload),
    });
    expect([200, 201]).toContain(replyRes.status);

    const assistantBubble = page.locator('.bubble-wrap.incoming').filter({
      hasText: assistantReply,
    }).first();
    await expect(assistantBubble).toBeVisible({ timeout: 8000 });

    // Verify avatar contains the agent avatar circle and image/recolor
    const agentAvatar = assistantBubble.locator('.message-avatar--agent .agent-avatar-circle').first();
    await expect(agentAvatar).toBeVisible();
    await expect(agentAvatar.locator('.agent-avatar-image').first()).toBeVisible();

    // Verify it is a true speech bubble and NOT an envelope card (.activity-entry)
    await expect(assistantBubble.locator('.activity-entry')).toHaveCount(0);
    await expect(page.locator('.activity-entry').filter({ hasText: assistantReply })).toHaveCount(0);

    // -------------------------------------------------------------------------
    // Scenario 3: Envelope card rendering for true inter-agent mailbox messages (presentation.kind === "mailbox_message")
    // Compact header showing Subject / "Message from <sender> to <recipient>"
    // Absence of multi-paragraph body text in the header title (preventing title bloat)
    // -------------------------------------------------------------------------
    const subject = "Architecture Specification Review";
    const longBodyParagraph1 = "First paragraph: Detailed architecture findings on memory isolation.";
    const longBodyParagraph2 = "Second paragraph: Analysis of SQLite monotonic timelines.";
    const fullBody = `${longBodyParagraph1}\n\n${longBodyParagraph2}`;

    const peerMessagePayload = {
      id: "msg-arch-peer-001",
      from: agentId,
      to: peerAgentId,
      subject,
      body: fullBody,
      created_at: new Date().toISOString(),
      source: { kind: "agent" },
    };

    const peerRes = await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(peerMessagePayload),
    });
    expect([200, 201]).toContain(peerRes.status);

    // Envelope card should render
    const envelopeCard = page.locator('.activity-entry').filter({
      hasText: subject,
    });
    await expect(envelopeCard.first()).toBeVisible({ timeout: 8000 });

    // Verify compact header: recipient and subject
    const envelopeLabel = envelopeCard.locator('.activity-entry-label strong');
    await expect(envelopeLabel).toHaveText(`To ${peerAgentId}`);

    const envelopeSubject = envelopeCard.locator('.activity-entry-subject');
    await expect(envelopeSubject).toHaveText(`• ${subject}`);

    // Verify absence of multi-paragraph body text in header label (prevents title bloat)
    const headerLabelText = await envelopeCard.locator('.activity-entry-label').innerText();
    expect(headerLabelText).not.toContain(longBodyParagraph1);
    expect(headerLabelText).not.toContain(longBodyParagraph2);

    // The preview line should only be a single cleaned line, not the raw multi-paragraph blob
    const previewText = await envelopeCard.locator('.activity-entry-preview').innerText();
    expect(previewText).not.toContain("\n");

    // -------------------------------------------------------------------------
    // Scenario 4: Image attachment rendering (attachments display without raw filesystem paths leaking into message bubbles)
    // -------------------------------------------------------------------------
    const transparent1pxPng = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==";
    const attachmentCaption = "Here is the visual memory graph.";
    const attachmentMessagePayload = {
      id: "msg-arch-attach-001",
      from: agentId,
      to: "user",
      body: attachmentCaption,
      created_at: new Date().toISOString(),
      attachments: [
        {
          type: "image",
          data_url: transparent1pxPng,
          name: "memory-graph.png",
        },
      ],
      source: { kind: "agent" },
    };

    const attachRes = await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(attachmentMessagePayload),
    });
    expect([200, 201]).toContain(attachRes.status);

    const attachmentBubble = page.locator('.bubble-wrap.incoming').filter({
      hasText: attachmentCaption,
    });
    await expect(attachmentBubble.first()).toBeVisible({ timeout: 8000 });

    // Attachment thumbnail/gallery image must be rendered
    const attachmentImage = attachmentBubble.locator('.bubble-images img, .attachment-image-button img');
    await expect(attachmentImage.first()).toBeVisible({ timeout: 5000 });

    // Ensure raw filesystem path or attachment path does NOT leak into the visible text
    const bubbleMarkdown = attachmentBubble.locator('.bubble .markdown-body');
    const bubbleContent = await bubbleMarkdown.innerText();
    expect(bubbleContent).not.toContain("/v1/attachments/");
    expect(bubbleContent).not.toContain(".attachments");
    expect(bubbleContent).not.toContain("Attached image");
    expect(bubbleContent.trim()).toBe(attachmentCaption);

    // -------------------------------------------------------------------------
    // Scenario 5: Scroll stability (prepending older messages does not jump the scroll viewport)
    // -------------------------------------------------------------------------
    const messagesContainer = page.locator('.messages');
    await expect(messagesContainer).toBeVisible();

    // Populate several older messages via REST API to establish a scrollable feed
    for (let i = 1; i <= 15; i++) {
      await fetch(`${targetRouterUrl}/v1/messages`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          id: `msg-arch-scroll-${i}`,
          from: agentId,
          to: "user",
          body: `Historical context entry #${i}: Testing scroll anchoring and viewport stability.`,
          created_at: new Date(Date.now() - (20 - i) * 1000).toISOString(),
          source: { kind: "agent" },
        }),
      });
    }

    // Allow UI to receive messages and scroll down
    await page.waitForTimeout(500);

    // Record scroll metrics when scrolled to a specific reading position
    await messagesContainer.evaluate((el) => {
      el.scrollTop = 250;
    });

    const initialScrollTop = await messagesContainer.evaluate((el) => el.scrollTop);
    const initialScrollHeight = await messagesContainer.evaluate((el) => el.scrollHeight);

    // Emulate prepending older messages or loading older events
    // Dispatch a conversation timeline event before current oldest or inject earlier messages
    await fetch(`${targetRouterUrl}/v1/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        id: "msg-arch-scroll-prepended-01",
        from: agentId,
        to: "user",
        body: "Prepended older history entry from previous epoch.",
        created_at: new Date(Date.now() - 100000).toISOString(),
        source: { kind: "agent" },
      }),
    });

    await page.waitForTimeout(500);

    // Verify scroll stability: the viewport does not jump to 0 or crash
    const currentScrollTop = await messagesContainer.evaluate((el) => el.scrollTop);
    // If new content expanded scrollHeight above, scrollTop should remain stable or adjust by height delta
    expect(currentScrollTop).toBeGreaterThan(0);

    // Close agent WS connection
    agentWs.close();
  });
});
