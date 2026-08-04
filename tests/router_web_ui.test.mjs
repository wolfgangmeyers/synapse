import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, readdir, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { parseArguments, startRouterWebUi } from "../tools/router_web_ui.mjs";
import { mergeMessages } from "../web/router-ui/history.js";

const webAgentId = "web-test-client";

test("router web UI accepts remote HTTP routers and rejects unsafe router URLs", () => {
  assert.equal(parseArguments(["--router", "http://brick.tailcdfd89.ts.net"]).router, "http://brick.tailcdfd89.ts.net");
  assert.equal(parseArguments(["--router", "https://router.example.test:8765/path?x=1"]).router, "https://router.example.test:8765/path?x=1");

  for (const routerUrl of [
    "ws://brick.tailcdfd89.ts.net",
    "file:///tmp/router",
    "javascript:alert(1)",
    "http://user:pass@brick.tailcdfd89.ts.net",
  ]) {
    assert.throws(() => parseArguments(["--router", routerUrl]), /http|credentials/);
  }
});

test("router HTTP API allows browser CORS for web UI requests", async () => {
  const router = await startTestRouter();
  try {
    const preflight = await fetch(new URL("/v1/messages", router.httpUrl), {
      method: "OPTIONS",
      headers: {
        origin: "https://anywhere.example",
        "access-control-request-method": "POST",
        "access-control-request-headers": "content-type",
      },
    });
    assert.equal(preflight.status, 204);
    assert.equal(preflight.headers.get("access-control-allow-origin"), "*");
    assert.match(preflight.headers.get("access-control-allow-methods") ?? "", /POST/);
    assert.match(preflight.headers.get("access-control-allow-headers") ?? "", /content-type/);

    const agents = await fetch(new URL("/v1/agents", router.httpUrl), { headers: { origin: "https://anywhere.example" } });
    assert.equal(agents.status, 200);
    assert.equal(agents.headers.get("access-control-allow-origin"), "*");

    const history = await fetch(new URL("/v1/conversations?owner=cors-web&peer=cors-peer", router.httpUrl), {
      headers: { origin: "https://untrusted.example" },
    });
    assert.equal(history.status, 200);
    assert.equal(history.headers.get("access-control-allow-origin"), null);

    const message = await fetch(new URL("/v1/messages", router.httpUrl), {
      method: "POST",
      headers: { origin: "https://anywhere.example", "content-type": "application/json" },
      body: JSON.stringify({ id: "cors-message", from: "cors-web", to: "cors-peer", body: "hello", created_at: "2026-07-28T14:30:00.000Z" }),
    });
    assert.equal(message.status, 201);
    assert.equal(message.headers.get("access-control-allow-origin"), "*");
  } finally {
    await router.close();
  }
});

test("router image attachments reject unsafe parent temp directories", async () => {
  const directory = await mkdtemp(join(tmpdir(), "router-web-ui-images-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const targetDirectory = join(directory, "target-images");
  const attachmentDirectory = join(directory, "images-link");
  const router = createAgentRouter({ repository, imageAttachmentDirectory: attachmentDirectory, timeoutSweepIntervalMs: 60_000 });
  try {
    await mkdir(targetDirectory);
    await symlink(targetDirectory, attachmentDirectory);
    const routerWsUrl = await router.listen();
    const routerHttpUrl = routerWsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    const response = await fetch(new URL("/v1/messages", routerHttpUrl), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ id: "unsafe-dir-message", from: "image-sender", to: "peer-agent", body: "image", created_at: "2026-07-28T14:30:00.000Z", attachments: [{ type: "image", data_url: tinyWebpDataUrl(), name: "bad.webp" }] }),
    });
    assert.equal(response.status, 400);
    assert.deepEqual(await readdir(targetDirectory).catch((error) => error.code === "ENOENT" ? [] : Promise.reject(error)), []);
  } finally {
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router image attachments reject truncated image structures", async () => {
  const directory = await mkdtemp(join(tmpdir(), "router-web-ui-images-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const attachmentDirectory = join(directory, "images");
  const router = createAgentRouter({ repository, imageAttachmentDirectory: attachmentDirectory, timeoutSweepIntervalMs: 60_000 });
  try {
    const routerWsUrl = await router.listen();
    const routerHttpUrl = routerWsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    const truncated = `data:image/webp;base64,${Buffer.from("RIFF\x10\x00\x00\x00WEBPVP8 ", "binary").toString("base64")}`;
    const response = await fetch(new URL("/v1/messages", routerHttpUrl), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ id: "truncated-image", from: "sender", to: "peer", body: "bad", created_at: "2026-07-28T14:30:00.000Z", attachments: [{ type: "image", data_url: truncated }] }),
    });
    assert.equal(response.status, 400);
    assert.deepEqual(await readdir(attachmentDirectory).catch((error) => error.code === "ENOENT" ? [] : Promise.reject(error)), []);
  } finally {
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router image attachments validate before writing temp files", async () => {
  const directory = await mkdtemp(join(tmpdir(), "router-web-ui-images-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const attachmentDirectory = join(directory, "images");
  const router = createAgentRouter({ repository, imageAttachmentDirectory: attachmentDirectory, timeoutSweepIntervalMs: 60_000 });
  try {
    const routerWsUrl = await router.listen();
    const routerHttpUrl = routerWsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    const response = await fetch(new URL("/v1/messages", routerHttpUrl), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ id: "bad-image-message", from: "bad-sender", to: "bad@peer", body: "bad", created_at: "2026-07-28T14:30:00.000Z", attachments: [{ type: "image", data_url: tinyWebpDataUrl(), name: "bad.webp" }] }),
    });
    assert.equal(response.status, 400);
    assert.deepEqual(await readdir(attachmentDirectory).catch((error) => error.code === "ENOENT" ? [] : Promise.reject(error)), []);
  } finally {
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router web UI serves the app and relays live router messages", async () => {
  const directory = await mkdtemp(join(tmpdir(), "router-web-ui-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  let ui;
  try {
    const routerWsUrl = await router.listen();
    const routerHttpUrl = routerWsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    ui = await startRouterWebUi({ router: routerHttpUrl, output: () => {} });

    const appResponse = await fetch(ui.httpUrl);
    assert.equal(appResponse.status, 200);
    const appHtml = await appResponse.text();
    assert.match(appHtml, /Your agent network/);
    assert.match(appHtml, /rel="icon" type="image\/png" href="\/mecha-icon\.png"/);
    const iconResponse = await fetch(new URL("/mecha-icon.png", ui.httpUrl));
    assert.equal(iconResponse.status, 200);
    assert.equal(iconResponse.headers.get("content-type"), "image/png");

    const peer = await connectRouterAgent(routerWsUrl, "peer-agent", sockets);
    const browser = new WebSocket(`${ui.httpUrl.replace("http://", "ws://")}/ui/events`);
    sockets.push(browser);
    await onceOpen(browser);
    browser.send(JSON.stringify({ type: "connect", routerUrl: routerHttpUrl, agentId: webAgentId }));
    const connected = await waitForEvent(browser, (event) => event.type === "status" && event.state === "connected");
    assert.equal(connected.routerUrl, routerHttpUrl);
    assert.equal(connected.agentId, webAgentId);

    browser.send(JSON.stringify({ type: "refresh_agents" }));
    const agents = await waitForEvent(browser, (event) => event.type === "agents" && event.agents.some((agent) => agent.agent_id === "peer-agent"));
    assert.equal(agents.routerUrl, routerHttpUrl);
    assert.equal(agents.agentId, webAgentId);
    assert.deepEqual(agents.agents.find((agent) => agent.agent_id === "peer-agent").agent_role, "agent");

    const deliveredToPeer = nextRouterFrame(peer.socket, (frame) => frame.type === "message" && frame.message.from === webAgentId);
    browser.send(JSON.stringify({ type: "send_message", to: "peer-agent", body: "hello from the browser", localId: "local-1" }));
    const sendResult = await waitForEvent(browser, (event) => event.type === "send_result" && event.localId === "local-1");
    assert.equal(sendResult.ok, true);
    assert.equal(sendResult.message.from, webAgentId);
    const firstDelivery = await deliveredToPeer;
    assert.equal(firstDelivery.message.body, "hello from the browser\n\n[Reply via mailbox to web-test-client.]");
    ackDelivery(peer.socket, peer.registered, "peer-agent", "peer-agent-instance", firstDelivery);

    const deliveredImage = nextRouterFrame(peer.socket, (frame) => frame.type === "message" && frame.message.from === webAgentId && frame.message.id !== sendResult.message.id);
    browser.send(JSON.stringify({
      type: "send_message",
      to: "peer-agent",
      body: "see screenshots",
      localId: "local-image-1",
      images: [
        { name: "one.webp", dataUrl: tinyWebpDataUrl() },
        { name: "two.webp", dataUrl: tinyWebpDataUrl() },
      ],
    }));
    const imageResult = await waitForEvent(browser, (event) => event.type === "send_result" && event.localId === "local-image-1");
    assert.equal(imageResult.ok, true);
    assert.equal(imageResult.message.attachments.length, 2);
    assert.equal("path" in imageResult.message.attachments[0], false);
    assert.doesNotMatch(JSON.stringify(imageResult.message), new RegExp(tmpdir().replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
    assert.match(imageResult.message.attachments[0].url, /^http:\/\/127\.0\.0\.1:\d+\/v1\/attachments\//);
    assert.doesNotMatch(imageResult.message.body, /data:image/);
    assert.doesNotMatch(imageResult.message.body, /Attached images:/);
    const imageDelivery = await deliveredImage;
    assert.match(imageDelivery.message.body, /see screenshots\n\nAttached images:\n1\. .*\.webp\n2\. .*\.webp/);
    assert.doesNotMatch(imageDelivery.message.body, /data:image/);
    ackDelivery(peer.socket, peer.registered, "peer-agent", "peer-agent-instance", imageDelivery);
    const attachmentResponse = await fetch(imageResult.message.attachments[0].url);
    assert.equal(attachmentResponse.status, 200);
    assert.equal(attachmentResponse.headers.get("content-type"), "image/webp");

    const inbound = waitForEvent(browser, (event) => event.type === "conversation_message" && event.message.from === "peer-agent");
    router.enqueueMessage({
      id: "peer-to-web-1",
      from: "peer-agent",
      to: webAgentId,
      body: "hello from a live agent",
      created_at: "2026-07-28T13:00:00.000Z",
    });
    const inboundEvent = await inbound;
    assert.equal(inboundEvent.message.body, "hello from a live agent");
    await waitFor(() => repository.messageFor("peer-to-web-1") === undefined);
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("two web sessions with one username receive live fanout and reconnect history", async () => {
  const router = await startTestRouter();
  const sockets = [];
  let ui;
  try {
    ui = await startRouterWebUi({ router: router.httpUrl, output: () => {} });
    const peer = await connectRouterAgent(router.wsUrl, "shared-peer", sockets);
    acknowledgeIncoming(peer.socket, peer.registered, "shared-peer", "shared-peer-instance");
    const first = await connectBrowser(ui.httpUrl, router.httpUrl, "shared-web-user", sockets);
    const second = await connectBrowser(ui.httpUrl, router.httpUrl, "shared-web-user", sockets);

    const firstOutbound = waitForEvent(first, (event) => event.type === "conversation_message" && event.message.body === "from first web");
    const secondOutbound = waitForEvent(second, (event) => event.type === "conversation_message" && event.message.body === "from first web");
    first.send(JSON.stringify({ type: "send_message", to: "shared-peer", body: "from first web", localId: "shared-web-outbound" }));
    const firstLive = await firstOutbound;
    const secondLive = await secondOutbound;
    assert.equal(firstLive.message.body, "from first web");
    assert.equal(secondLive.message.body, "from first web");
    assert.equal(firstLive.message.sequence, secondLive.message.sequence);

    const firstInbound = waitForEvent(first, (event) => event.type === "conversation_message" && event.message.id === "shared-web-inbound");
    const secondInbound = waitForEvent(second, (event) => event.type === "conversation_message" && event.message.id === "shared-web-inbound");
    router.router.enqueueMessage({ id: "shared-web-inbound", from: "shared-peer", to: "shared-web-user", body: "to both web sessions", created_at: "2026-07-28T15:00:00.000Z" });
    await Promise.all([firstInbound, secondInbound]);

    second.send(JSON.stringify({ type: "get_history", peer: "shared-peer", requestId: "history-before-reconnect" }));
    const history = await waitForHistory(second, "shared-peer");
    assert.equal(history.requestId, "history-before-reconnect");
    assert.deepEqual(history.messages.map(({ body }) => body), ["from first web", "to both web sessions"]);
    assert.equal(history.highWaterSequence, history.messages.at(-1).sequence);

    second.close();
    await onceClose(second);
    const reconnected = await connectBrowser(ui.httpUrl, router.httpUrl, "shared-web-user", sockets);
    reconnected.send(JSON.stringify({ type: "get_history", peer: "shared-peer", requestId: "history-after-reconnect" }));
    const rehydrated = await waitForHistory(reconnected, "shared-peer");
    assert.equal(rehydrated.requestId, "history-after-reconnect");
    assert.deepEqual(rehydrated.messages.map(({ id }) => id), history.messages.map(({ id }) => id));

    const firstImage = waitForEvent(first, (event) => event.type === "conversation_message" && event.message.body === "shared image");
    const reconnectedImage = waitForEvent(reconnected, (event) => event.type === "conversation_message" && event.message.body === "shared image");
    first.send(JSON.stringify({
      type: "send_message",
      to: "shared-peer",
      body: "shared image",
      localId: "shared-web-image",
      images: [{ name: "shared.webp", dataUrl: tinyWebpDataUrl() }],
    }));
    const [imageOne, imageTwo] = await Promise.all([firstImage, reconnectedImage]);
    assert.equal(imageOne.message.attachments.length, 1);
    assert.deepEqual(imageTwo.message.attachments, imageOne.message.attachments);
    assert.match(imageOne.message.attachments[0].url, /^http:\/\/127\.0\.0\.1:\d+\/v1\/attachments\//);
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
  }
});

test("router web UI relay reconnects after router restart and resumes one canonical transcript", async () => {
  const directory = await mkdtemp(join(tmpdir(), "router-web-ui-reconnect-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), maxBatchBodyChars: 0 });
  let router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  const sockets = [];
  let ui;
  try {
    let wsUrl = await router.listen();
    const routerHttpUrl = wsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    const routerPort = Number(new URL(routerHttpUrl).port);
    ui = await startRouterWebUi({ router: routerHttpUrl, output: () => {} });
    const browser = await connectBrowser(ui.httpUrl, routerHttpUrl, "reconnecting-web-user", sockets);
    const conversationEvents = [];
    browser.on("message", (payload) => {
      const event = JSON.parse(payload.toString("utf8"));
      if (event.type === "conversation_message") conversationEvents.push(event.message);
    });

    const firstLive = waitForEvent(browser, (event) => event.type === "conversation_message" && event.message.id === "before-restart");
    const firstResponse = await fetch(new URL("/v1/messages", routerHttpUrl), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        id: "before-restart",
        from: "peer",
        to: "reconnecting-web-user",
        body: "before restart",
        created_at: "2026-08-03T00:00:00.000Z",
      }),
    });
    assert.equal(firstResponse.status, 201);
    const first = (await firstLive).message;

    const reconnecting = waitForEvent(browser, (event) => event.type === "status" && event.state === "reconnecting", 2_000);
    await router.close();
    await reconnecting;
    router = createAgentRouter({ repository, port: routerPort, timeoutSweepIntervalMs: 60_000 });
    wsUrl = await router.listen();
    assert.equal(wsUrl.replace("ws://", "http://").replace("/v1/agents", ""), routerHttpUrl);
    const reconnected = await waitForEvent(browser, (event) => event.type === "status" && event.state === "connected", 3_000);
    assert.equal(reconnected.agentId, "reconnecting-web-user");

    browser.send(JSON.stringify({ type: "get_history", peer: "peer", requestId: "after-router-restart" }));
    const history = await waitForEvent(browser, (event) => event.type === "history" && event.requestId === "after-router-restart", 2_000);
    assert.deepEqual(history.messages.map(({ id }) => id), ["before-restart"]);
    let transcript = mergeMessages([first], history.messages);
    assert.deepEqual(transcript.map(({ id }) => id), ["before-restart"]);

    const retryResponse = await fetch(new URL("/v1/messages", routerHttpUrl), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        id: "before-restart",
        from: "peer",
        to: "reconnecting-web-user",
        body: "before restart",
        created_at: "2026-08-03T00:00:00.000Z",
      }),
    });
    assert.equal(retryResponse.status, 200);

    const resumedLive = waitForEvent(browser, (event) => event.type === "conversation_message" && event.message.id === "after-restart", 2_000);
    const resumedResponse = await fetch(new URL("/v1/messages", routerHttpUrl), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        id: "after-restart",
        from: "peer",
        to: "reconnecting-web-user",
        body: "after restart",
        created_at: "2026-08-03T00:00:01.000Z",
      }),
    });
    assert.equal(resumedResponse.status, 201);
    transcript = mergeMessages(transcript, [(await resumedLive).message]);
    assert.deepEqual(transcript.map(({ id }) => id), ["before-restart", "after-restart"]);
    assert.equal(conversationEvents.filter(({ id }) => id === "before-restart").length, 1);
    assert.equal(conversationEvents.filter(({ id }) => id === "after-restart").length, 1);
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router web app keeps router UX, scoped events, Enter handling, and fixed page layout", async () => {
  const app = await readFile(new URL("../web/router-ui/app.js", import.meta.url), "utf8");
  const webState = await readFile(new URL("../web/router-ui/state.js", import.meta.url), "utf8");
  const styles = await readFile(new URL("../web/router-ui/styles.css", import.meta.url), "utf8");
  const html = await readFile(new URL("../web/router-ui/index.html", import.meta.url), "utf8");
  assert.match(html, /id="agent-filter"/);
  assert.match(html, />Connected agents<\/span>/);
  assert.match(html, /placeholder="Filter connected agents"/);
  assert.match(html, /id="image-preview"/);
  assert.match(html, /id="image-input" type="file" accept="image\/\*" multiple/);
  assert.match(app, /const MAX_IMAGES = 3;/);
  assert.match(app, /const MAX_IMAGE_EDGE = 1600;/);
  assert.match(app, /canvas\.toBlob\(resolve, "image\/webp", IMAGE_WEBP_QUALITY\)/);
  assert.match(app, /elements\.composer\.addEventListener\("drop"/);
  assert.match(app, /elements\.draft\.addEventListener\("paste"/);
  assert.match(app, /function renderImagePreview\(\)/);
  assert.match(app, /function renderMessageImages\(attachments\)/);
  assert.match(app, /const MESSAGE_BOTTOM_THRESHOLD_PX = 80;/);
  assert.match(app, /elements\.messages\.addEventListener\("scroll", \(\) => \{ state\.followMessages = isMessagesNearBottom\(\); \}\)/);
  assert.match(app, /function scrollMessagesToBottom\(\)/);
  assert.match(app, /function scrollMessagesToBottomIfFollowing\(\)/);
  assert.match(app, /if \(shouldFollow\) scrollMessagesToBottom\(\);/);
  assert.match(app, /image\.addEventListener\("load", scrollMessagesToBottomIfFollowing, \{ once: true \}\)/);
  assert.doesNotMatch(app, /localStorage\.setItem\([^\n]+data:image/);
  assert.match(app, /function messageForStorage\(message\)/);
  assert.match(app, /startsWith\("data:image\/"\)/);
  assert.match(app, /mecha\.web\.agentIds/);
  assert.match(webState, /function matchesActiveScope/);
  assert.match(webState, /"message"/);
  assert.match(webState, /event\.routerUrl === active\.routerUrl && event\.agentId === active\.agentId/);
  assert.match(app, /row\.addEventListener\("click", \(\) => \{ void switchRouter\(routerUrl, \{ connect: true \}\); \}\)/);
  assert.doesNotMatch(app, /Select"/);
  assert.match(app, /url\.username !== "" \|\| url\.password !== ""/);
  assert.match(app, /event\.key === "Enter" && !event\.shiftKey/);
  assert.match(app, /elements\.draft\.addEventListener\("input", \(\) => \{ saveCurrentDraft\(\); updateSendState\(\); \}\)/);
  assert.match(webState, /const DRAFT_LIMIT = 200;/);
  assert.match(webState, /const DRAFT_RETENTION_MS = 30 \* 24 \* 60 \* 60 \* 1000;/);
  assert.match(webState, /function draftScope\(routerUrl, agentId, peer\)/);
  assert.match(webState, /function pruneDrafts\(drafts, now = Date\.now\(\)\)/);
  assert.match(app, /drafts\[key\] = \{ body: value, updatedAtMs: Date\.now\(\) \}/);
  assert.match(app, /function restoreDraftForSelectedPeer\(\{ focus = false \} = \{\}\)/);
  assert.match(app, /function focusDraftAtEnd\(\)/);
  assert.match(app, /elements\.draft\.setSelectionRange\(end, end\)/);
  assert.match(app, /restoreDraftForSelectedPeer\(\{ focus: true \}\)/);
  assert.match(app, /state\.selectedPeer = loadSelectedPeer\(routerUrl, state\.agentId\)/);
  assert.match(app, /elements\.agentFilter\.addEventListener\("input"/);
  assert.match(app, /const peers = state\.agents\n    \.map\(\(agent\) => agent\.agent_id\)/);
  assert.doesNotMatch(app, /for \(const peer of Object\.keys\(state\.messages\)\) known\.add\(peer\)/);
  assert.doesNotMatch(app, /for \(const peer of state\.draftPeers\) known\.add\(peer\)/);
  assert.match(app, /`\$\{peer\} \$\{humanName\(peer\)\}`\.toLowerCase\(\)\.includes\(filter\)/);
  assert.match(app, /\$\{escapeHtml\(peer\)\} · Available/);
  assert.doesNotMatch(app, /\? "Available" : "History"/);
  assert.match(app, /state\.agentFilter = "";\n  elements\.agentFilter\.value = "";/);
  assert.match(app, /stateName === "connected" \? "" :/);
  assert.match(styles, /p:empty \{ display: none; \}/);
  assert.match(app, /import \{ HISTORY_RETENTION_MS, mergeMessages, messageKey, messageTime, pruneHistoryMessages, reconcileHistoryMessages \} from "\.\/history\.js"/);
  assert.doesNotMatch(app, /#main-connect/);
  assert.match(styles, /body \{[\s\S]*overflow: hidden;/);
  assert.match(styles, /\.shell \{[\s\S]*height: 100dvh;[\s\S]*overflow: hidden;/);
  assert.match(styles, /\.messages \{[\s\S]*overflow: auto;/);
});

test("router web app stores bounded scoped conversation history in IndexedDB", async () => {
  const app = await readFile(new URL("../web/router-ui/app.js", import.meta.url), "utf8");
  const historyHelpers = await readFile(new URL("../web/router-ui/history.js", import.meta.url), "utf8");
  const webState = await readFile(new URL("../web/router-ui/state.js", import.meta.url), "utf8");
  assert.match(historyHelpers, /const HISTORY_LIMIT = 200;/);
  assert.match(historyHelpers, /const HISTORY_RETENTION_MS = 30 \* 24 \* 60 \* 60 \* 1000;/);
  assert.match(app, /indexedDB\.open\(HISTORY_DB_NAME, HISTORY_DB_VERSION\)/);
  assert.match(app, /const HISTORY_DB_VERSION = 2;/);
  assert.match(app, /const IMAGE_STORE_NAME = "image_attachments";/);
  assert.match(app, /createObjectStore\(HISTORY_STORE_NAME, \{ keyPath: "key" \}\)/);
  assert.match(app, /store\.createIndex\("ownerScope", "ownerScope"/);
  assert.match(app, /store\.createIndex\("peerScope", "peerScope"/);
  assert.match(app, /store\.createIndex\("createdAtMs", "createdAtMs"/);
  assert.match(webState, /function ownerScope\(routerUrl, agentId\)/);
  assert.match(webState, /function peerScope\(routerUrl, agentId, peer\)/);
  assert.match(historyHelpers, /\.filter\(\(message\) => messageTime\(message\) >= cutoff\)/);
  assert.match(historyHelpers, /\.slice\(-HISTORY_LIMIT\)/);
  assert.match(app, /function messageWithCachedImages\(peer, acceptedMessage, pendingMessage\)/);
  assert.match(app, /function saveCachedImage\(key, dataUrl, attachment\)/);
  assert.match(app, /const HISTORY_IMAGE_EDGE = 360;/);
  assert.match(app, /canvas\.toBlob\(resolve, "image\/webp", HISTORY_IMAGE_QUALITY\)/);
  assert.match(app, /function cachedImageUrl\(key\)/);
  assert.match(app, /cacheKey/);
  assert.doesNotMatch(app, /localStorage\.setItem\(HISTORY_STORE_KEY/);
  assert.match(app, /type: "get_history"/);
  assert.match(app, /event\.type === "conversation_message"/);
  assert.match(app, /historyRequestMatches\(event, state\.selectedPeer, state\.historyRequests\.get\(event\.peer\)\)/);
  assert.match(app, /saveRecordsForPeer\(db, routerUrl, agentId, peer, messages\)/);
  assert.doesNotMatch(app, /saveRecordsForPeer\(db, state\.routerUrl, state\.agentId/);
  const server = await readFile(new URL("../tools/router_web_ui.mjs", import.meta.url), "utf8");
  assert.match(server, /type: "message"[\s\S]*agentId: configured\.agentId[\s\S]*routerUrl: configured\.routerUrl/);
  assert.match(server, /refreshTimer = setInterval\(refreshAgents, 60_000\)/);
  assert.match(server, /conversation_sync: "v1"/);
  assert.match(server, /type: "conversation_message"/);
  assert.doesNotMatch(server, /refreshTimer = setInterval\(refreshAgents, 30_000\)/);
});

async function startTestRouter() {
  const directory = await mkdtemp(join(tmpdir(), "router-web-ui-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000 });
  const wsUrl = await router.listen();
  return {
    directory,
    router,
    wsUrl,
    httpUrl: wsUrl.replace("ws://", "http://").replace("/v1/agents", ""),
    async close() {
      await router.close();
      repository.close();
      await rm(directory, { recursive: true, force: true });
    },
  };
}

async function connectBrowser(uiHttpUrl, routerHttpUrl, agentId, sockets) {
  const browser = new WebSocket(`${uiHttpUrl.replace("http://", "ws://")}/ui/events`);
  sockets.push(browser);
  await onceOpen(browser);
  browser.send(JSON.stringify({ type: "connect", routerUrl: routerHttpUrl, agentId }));
  const connected = await waitForEvent(browser, (event) => event.type === "status" && event.state === "connected", 2_000);
  assert.equal(connected.routerUrl, routerHttpUrl);
  assert.equal(connected.agentId, agentId);
  return browser;
}

async function connectRouterAgent(routerWsUrl, agentId, sockets) {
  const socket = new WebSocket(routerWsUrl);
  sockets.push(socket);
  await onceOpen(socket);
  socket.send(JSON.stringify({
    version: "v1",
    frame_id: `${agentId}-register`,
    type: "register",
    agent_id: agentId,
    instance_id: `${agentId}-instance`,
  }));
  const registered = await nextRouterFrame(socket, (frame) => frame.type === "registered");
  return { socket, registered };
}

function onceOpen(socket) {
  return new Promise((resolve, reject) => {
    socket.once("open", resolve);
    socket.once("error", reject);
  });
}

function onceClose(socket) {
  return new Promise((resolve) => {
    if (socket.readyState === WebSocket.CLOSED) resolve();
    else socket.once("close", resolve);
  });
}

function waitForEvent(socket, predicate, timeoutMs = 1000) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => cleanup(reject, new Error("timed out waiting for UI event")), timeoutMs);
    const onMessage = (payload) => {
      const event = JSON.parse(payload.toString("utf8"));
      if (predicate(event)) cleanup(resolve, event);
    };
    const onError = (error) => cleanup(reject, error);
    const cleanup = (done, value) => {
      clearTimeout(timer);
      socket.off("message", onMessage);
      socket.off("error", onError);
      done(value);
    };
    socket.on("message", onMessage);
    socket.once("error", onError);
  });
}

function nextRouterFrame(socket, predicate, timeoutMs = 1000) {
  return waitForEvent(socket, predicate, timeoutMs);
}

function tinyWebpDataUrl() {
  return "data:image/webp;base64,UklGRioCAABXRUJQVlA4WAoAAAAgAAAAHwAAHwAASUNDUMgBAAAAAAHIAAAAAAQwAABtbnRyUkdCIFhZWiAH4AABAAEAAAAAAABhY3NwAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAQAA9tYAAQAAAADTLQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAlkZXNjAAAA8AAAACRyWFlaAAABFAAAABRnWFlaAAABKAAAABRiWFlaAAABPAAAABR3dHB0AAABUAAAABRyVFJDAAABZAAAAChnVFJDAAABZAAAAChiVFJDAAABZAAAAChjcHJ0AAABjAAAADxtbHVjAAAAAAAAAAEAAAAMZW5VUwAAAAgAAAAcAHMAUgBHAEJYWVogAAAAAAAAb6IAADj1AAADkFhZWiAAAAAAAABimQAAt4UAABjaWFlaIAAAAAAAACSgAAAPhAAAts9YWVogAAAAAAAA9tYAAQAAAADTLXBhcmEAAAAAAAQAAAACZmYAAPKnAAANWQAAE9AAAApbAAAAAAAAAABtbHVjAAAAAAAAAAEAAAAMZW5VUwAAACAAAAAcAEcAbwBvAGcAbABlACAASQBuAGMALgAgADIAMAAxADZWUDggPAAAAFADAJ0BKiAAIAA+YS6TRyQioaEoCACADAllAHYA/AAAgyggAP7lhz//6En4JPwSfun///hVuGIZOgAAAA==";
}

function ackDelivery(socket, registered, agentId, instanceId, frame) {
  for (const type of ["injection_requested", "ack"]) {
    socket.send(JSON.stringify({
      version: "v1",
      frame_id: `${agentId}-${type}-${frame.message.id}`,
      type,
      agent_id: agentId,
      instance_id: instanceId,
      lease_id: registered.lease_id,
      message_id: frame.message.id,
      attempt_number: frame.attempt_number,
    }));
  }
}

function acknowledgeIncoming(socket, registered, agentId, instanceId) {
  let frameId = 0;
  socket.on("message", (payload) => {
    const frame = JSON.parse(payload.toString("utf8"));
    if (frame.type !== "message") return;
    for (const type of ["injection_requested", "ack"]) {
      socket.send(JSON.stringify({
        version: "v1",
        frame_id: `${agentId}-receipt-${++frameId}`,
        type,
        agent_id: agentId,
        instance_id: instanceId,
        lease_id: registered.lease_id,
        message_id: frame.message.id,
        attempt_number: frame.attempt_number,
      }));
    }
  });
}

function waitForHistory(socket, peer) {
  return waitForEvent(socket, (event) => event.type === "history" && event.peer === peer, 2_000);
}

async function routerAgents(routerHttpUrl) {
  const response = await fetch(new URL("/v1/agents", routerHttpUrl));
  return (await response.json()).agents ?? [];
}

async function waitFor(predicate, timeoutMs = 1000) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    if (await predicate()) return;
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
  assert.fail("timed out waiting for condition");
}
