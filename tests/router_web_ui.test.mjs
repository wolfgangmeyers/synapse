import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, readdir, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import MarkdownIt from "markdown-it";
import WebSocket from "ws";

import { openMessageRepository } from "../harness/db.js";
import { createAgentRouter } from "../harness/router.js";
import { parseArguments, startRouterWebUi } from "../tools/router_web_ui.mjs";
import { mergeMessages } from "../web/router-ui/history.js";
import {
  applyLifecycleSnapshot,
  consumePendingModelControl,
  matchesActiveScope,
  matchingPendingModelControl,
  mergeAgentModelProjection,
  modelControlStatusTitle,
  normalizeModelSummaries,
  upsertLifecycleAgent,
} from "../web/router-ui/state.js";
import { isAllowedMarkdownLink, markdownToSafeHtml, renderMarkdownBody, textWithoutMarkdownImages } from "../web/router-ui/markdown.js";

const webAgentId = "web-test-client";

const lifecycleAgent = (version, count = 1, description = "Safe child") => ({
  agent_id: "lifecycle-parent", subagent_version: version, subagent_count: count,
  subagents: count === 0 ? [] : [{ id: "child", description }],
});

test("upserts an unknown parent from a live lifecycle update", () => {
  const agents = upsertLifecycleAgent([], lifecycleAgent(3));
  assert.deepEqual(agents, [{ agent_id: "lifecycle-parent", activity_state: "idle", ...lifecycleAgent(3) }]);
});

test("ignores an older live lifecycle update", () => {
  const newer = upsertLifecycleAgent([], lifecycleAgent(3));
  assert.deepEqual(upsertLifecycleAgent(newer, lifecycleAgent(2, 0)), newer);
});

test("does not let an older snapshot erase newer live lifecycle state", () => {
  const newer = upsertLifecycleAgent([], lifecycleAgent(4));
  const result = applyLifecycleSnapshot(newer, [], 3, 0);
  assert.deepEqual(result.agents, newer);
});

test("does not create a durable parent from a snapshot before the agent refresh", () => {
  const result = applyLifecycleSnapshot([], [lifecycleAgent(3)], 3);
  assert.deepEqual(result.agents, []);
});

test("keeps snapshot state for an agent already present in the connected list", () => {
  const connected = [{ agent_id: "lifecycle-parent", activity_state: "active" }];
  const result = applyLifecycleSnapshot(connected, [lifecycleAgent(3)], 3);
  assert.deepEqual(result.agents, [{ ...connected[0], ...lifecycleAgent(3) }]);
});

test("a live lifecycle update still applies after the connected refresh", () => {
  const beforeRefresh = applyLifecycleSnapshot([], [lifecycleAgent(3)], 3);
  const connected = [{ agent_id: "lifecycle-parent", activity_state: "idle" }];
  const afterUpdate = upsertLifecycleAgent(connected, lifecycleAgent(4, 2, "New child"));
  assert.deepEqual(beforeRefresh.agents, []);
  assert.equal(afterUpdate[0].subagent_version, 4);
  assert.equal(afterUpdate[0].subagent_count, 2);
  assert.deepEqual(afterUpdate[0].subagents, [{ id: "child", description: "New child" }]);
});

test("clears lifecycle badge state from a newer zero-count snapshot", () => {
  const result = applyLifecycleSnapshot(upsertLifecycleAgent([], lifecycleAgent(3)), [lifecycleAgent(4, 0)], 4, 3);
  assert.equal(result.agents[0].subagent_count, 0);
  assert.deepEqual(result.agents[0].subagents, []);
});

test("matches model control results only to the pending control and target", () => {
  const pending = new Map([["control-1", { peer: "target-agent", model: { provider: "provider", id: "model" } }]]);
  assert.equal(matchingPendingModelControl(pending, {
    controlId: "control-1",
    targetAgentId: "target-agent",
  }), pending.get("control-1"));
  assert.equal(matchingPendingModelControl(pending, {
    controlId: "control-1",
    targetAgentId: "other-agent",
  }), undefined);
  assert.equal(matchingPendingModelControl(pending, {
    controlId: "late-control",
    targetAgentId: "target-agent",
  }), undefined);
});

test("model control status distinguishes confirmed rejection from transport uncertainty", () => {
  assert.equal(modelControlStatusTitle(true), "Model changed");
  assert.equal(modelControlStatusTitle(false, "model_unavailable"), "Model change rejected");
  for (const errorCode of ["control_timeout", "router_disconnected", "router_reconnecting", "ui_not_connected"]) {
    assert.equal(modelControlStatusTitle(false, errorCode), "Model change not confirmed");
  }
});

test("model control state applies authoritative current models without replacing catalogs", () => {
  const current = { provider: "provider", id: "current" };
  const target = { provider: "provider", id: "target" };
  const catalog = [current, target];
  const pending = new Map([
    ["accepted-control", { peer: "target-agent", model: target }],
    ["rejected-control", { peer: "target-agent", model: target }],
  ]);
  const agents = [{
    agent_id: "target-agent",
    instance_id: "target-instance",
    activity_state: "idle",
    current_model: current,
    available_models: catalog,
    model_catalog_total_count: 3,
    model_catalog_truncated: true,
  }];

  const accepted = consumePendingModelControl(pending, {
    controlId: "accepted-control",
    targetAgentId: "target-agent",
    accepted: true,
    currentModel: target,
  });
  assert.deepEqual(accepted.projection, { currentModel: target });
  const acceptedState = mergeAgentModelProjection(agents, "target-agent", "target-instance", accepted.projection);
  assert.deepEqual(acceptedState.agents[0].current_model, target);
  assert.deepEqual(acceptedState.agents[0].available_models, catalog);
  assert.equal(acceptedState.agents[0].model_catalog_total_count, 3);
  assert.equal(acceptedState.agents[0].model_catalog_truncated, true);

  const rejected = consumePendingModelControl(pending, {
    controlId: "rejected-control",
    targetAgentId: "target-agent",
    accepted: false,
    currentModel: current,
  });
  const rejectedState = mergeAgentModelProjection(acceptedState.agents, "target-agent", "target-instance", rejected.projection);
  assert.deepEqual(rejectedState.agents[0].current_model, current);
  assert.deepEqual(rejectedState.agents[0].available_models, catalog);
  assert.equal(pending.size, 0);
  assert.equal(consumePendingModelControl(pending, {
    controlId: "accepted-control",
    targetAgentId: "target-agent",
  }), undefined);
  assert.equal(normalizeModelSummaries([target, target]).length, 1);
  const supplementaryId = "🚀".repeat(256);
  assert.equal(normalizeModelSummaries([{ provider: "provider", id: supplementaryId }])[0].id, supplementaryId);
  const stale = mergeAgentModelProjection(
    rejectedState.agents,
    "target-agent",
    "retired-instance",
    { currentModel: { provider: "provider", id: "stale" } },
    { replaceInstance: false },
  );
  assert.equal(stale.changed, false);
  assert.deepEqual(stale.agents[0].current_model, current);
  assert.deepEqual(normalizeModelSummaries([{ provider: "provider", id: `${supplementaryId}🚀` }]), []);
});

const renderMarkdown = (body) => markdownToSafeHtml(body, MarkdownIt);

test("router web UI renders common Markdown safely", () => {
  const html = renderMarkdown(`First paragraph\n\nSecond paragraph\n\n- one\n- two\n\n[site](https://example.com)\n\n\`\`\`js\nconst value = "<safe>";\n\`\`\``);
  assert.match(html, /<p>First paragraph<\/p>\n<p>Second paragraph<\/p>/);
  assert.match(html, /<ul>\n<li>one<\/li>\n<li>two<\/li>\n<\/ul>/);
  assert.match(html, /<a href="https:\/\/example\.com">site<\/a>/);
  assert.match(html, /<pre><code class="language-js">const value = &quot;&lt;safe&gt;&quot;;\n<\/code><\/pre>/);
});

test("router web UI preserves plain multiline text in Markdown output", () => {
  assert.equal(renderMarkdown("first line\nsecond line"), "<p>first line<br>\nsecond line</p>\n");
});

test("router web UI keeps code formatting when the Markdown vendor is unavailable", () => {
  const originalMarkdownIt = globalThis.markdownit;
  globalThis.markdownit = undefined;
  try {
    const html = markdownToSafeHtml("Run `npm test`\n\n```sh\nnpm test\n```");
    assert.match(html, /Run <code>npm test<\/code>/);
    assert.match(html, /<pre><code>npm test<\/code><\/pre>/);
    assert.doesNotMatch(html, /```/);
  } finally {
    globalThis.markdownit = originalMarkdownIt;
  }
});

test("router web UI rejects unsafe Markdown HTML, URLs, and images", () => {
  const html = renderMarkdown(`<script>alert(1)</script>\n\n[bad](javascript:alert(1))\n[relative](/local)\n[protocol](//example.com)\n[ftp](ftp://example.com/file)\n![track](https://example.com/pixel.png)`);
  assert.match(html, /&lt;script&gt;alert\(1\)&lt;\/script&gt;/);
  assert.doesNotMatch(html, /<script>/);
  assert.doesNotMatch(html, /href="javascript:/);
  assert.doesNotMatch(html, /href="\/local"/);
  assert.doesNotMatch(html, /href="\/\/example\.com"/);
  assert.doesNotMatch(html, /href="ftp:/);
  assert.doesNotMatch(html, /<img\b/);
  assert.doesNotMatch(html, /href="https:\/\/example\.com\/pixel\.png"/);
  assert.match(html, /!\[track\]\(https:\/\/example\.com\/pixel\.png\)/);
});

test("router web UI renders Markdown into a DOM container and normalizes links", () => {
  const links = [{ target: "", rel: "" }];
  const fakeDocument = {
    createElement(tagName) {
      assert.equal(tagName, "div");
      return {
        className: "",
        innerHTML: "",
        querySelectorAll(selector) {
          assert.equal(selector, "a[href]");
          return links;
        },
      };
    },
  };
  const originalMarkdownIt = globalThis.markdownit;
  globalThis.markdownit = MarkdownIt;
  try {
    const element = markdownToSafeHtml("[site](https://example.com)");
    assert.match(element, /<a href="https:\/\/example\.com">site<\/a>/);
  } finally {
    globalThis.markdownit = originalMarkdownIt;
  }
  globalThis.markdownit = MarkdownIt;
  try {
    const element = renderMarkdownBody(fakeDocument, "[site](https://example.com)");
    assert.equal(element.className, "markdown-body");
    assert.match(element.innerHTML, /<a href="https:\/\/example\.com">site<\/a>/);
    assert.equal(links[0].target, "_blank");
    assert.equal(links[0].rel, "noopener noreferrer");
  } finally {
    globalThis.markdownit = originalMarkdownIt;
  }
});

test("router web UI allows only absolute http, https, and mailto Markdown links", () => {
  for (const url of ["http://example.com", "https://example.com/path", "mailto:test@example.com"]) {
    assert.equal(isAllowedMarkdownLink(url), true, url);
  }
  for (const url of ["/relative", "relative/path", "//example.com", "javascript:alert(1)", "data:text/html,<b>x</b>", "ftp://example.com", "http://", "http:example.com", "http:/example.com", "https:example.com", "https://[::1", " https://example.com", "https://example.com ", ""]) {
    assert.equal(isAllowedMarkdownLink(url), false, url);
  }
  assert.equal(isAllowedMarkdownLink(null), false);
  assert.equal(isAllowedMarkdownLink({ href: "https://example.com" }), false);
});

test("router web UI strips Markdown image syntax before legacy attachment scanning", () => {
  const stripped = textWithoutMarkdownImages("keep /v1/attachments/real.png\n![ignore](/v1/attachments/tracker.png)\n![remote](https://example.com/pixel.png \"title\")\n![ref][Image Ref]\n![Shortcut Ref]\n\n[image ref]: /v1/attachments/ref.png\n[shortcut ref]: /v1/attachments/shortcut.png");
  assert.match(stripped, /keep \/v1\/attachments\/real\.png/);
  assert.doesNotMatch(stripped, /tracker\.png/);
  assert.doesNotMatch(stripped, /pixel\.png/);
  assert.doesNotMatch(stripped, /ref\.png/);
  assert.doesNotMatch(stripped, /shortcut\.png/);
});

test("router web UI accepts remote HTTP routers and rejects unsafe router URLs", () => {
  assert.equal(parseArguments(["--router", "http://brick.tailcdfd89.ts.net"]).router, "http://brick.tailcdfd89.ts.net");
  assert.equal(parseArguments(["--router", "https://router.example.test:8765/path?x=1"]).router, "https://router.example.test:8765/path?x=1");
  assert.equal(parseArguments([]).defaultAgentId, "wolfgang-web");
  assert.equal(parseArguments(["--default-agent-id", "wolfgang"]).defaultAgentId, "wolfgang");

  for (const routerUrl of [
    "ws://brick.tailcdfd89.ts.net",
    "file:///tmp/router",
    "javascript:alert(1)",
    "http://user:pass@brick.tailcdfd89.ts.net",
  ]) {
    assert.throws(() => parseArguments(["--router", routerUrl]), /http|credentials/);
  }
});

test("router HTTP API limits browser CORS to public web UI request routes", async () => {
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
    assert.equal(agents.headers.get("access-control-allow-origin"), null);

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

test("router HTTP API rejects escaped newline text in message bodies", async () => {
  const router = await startTestRouter();
  try {
    for (const [id, body] of [["escaped-n", "first\\nsecond"], ["escaped-r", "first\\rsecond"]]) {
      const response = await fetch(new URL("/v1/messages", router.httpUrl), {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ id, from: "escaped-sender", to: "escaped-peer", body, created_at: "2026-07-28T14:30:00.000Z" }),
      });
      assert.equal(response.status, 400);
      const payload = await response.json();
      assert.equal(payload.error.code, "escaped_newlines");
      assert.match(payload.error.message, /Send real multiline content/);
    }

    const multiline = await fetch(new URL("/v1/messages", router.httpUrl), {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ id: "real-newline", from: "escaped-sender", to: "escaped-peer", body: "first\nsecond", created_at: "2026-07-28T14:30:00.000Z" }),
    });
    assert.equal(multiline.status, 201);
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
    assert.match(appHtml, /<script src="\/vendor\/markdown-it\.js"><\/script>/);
    const vendorResponse = await fetch(new URL("/vendor/markdown-it.js", ui.httpUrl));
    assert.equal(vendorResponse.status, 200);
    assert.match(vendorResponse.headers.get("content-type") ?? "", /text\/javascript/);
    assert.match(await vendorResponse.text(), /markdownit/);
    const vendorHeadResponse = await fetch(new URL("/vendor/markdown-it.js", ui.httpUrl), { method: "HEAD" });
    assert.equal(vendorHeadResponse.status, 200);
    assert.equal(await vendorHeadResponse.text(), "");
    const missingVendorResponse = await fetch(new URL("/vendor/markdown-it.min.js", ui.httpUrl));
    assert.equal(missingVendorResponse.status, 404);
    const iconResponse = await fetch(new URL("/mecha-icon.png", ui.httpUrl));
    assert.equal(iconResponse.status, 200);
    assert.equal(iconResponse.headers.get("content-type"), "image/png");
    const configResponse = await fetch(new URL("/ui/config", ui.httpUrl));
    assert.deepEqual(await configResponse.json(), { defaultRouter: routerHttpUrl, defaultAgentId: "wolfgang-web" });

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
    assert.equal(agents.agents.find((agent) => agent.agent_id === "peer-agent").activity_state, "idle");
    assert.equal(agents.agents.find((agent) => agent.agent_id === "peer-agent").instance_id, "peer-agent-instance");

    const activeEvent = waitForEvent(browser, (event) => event.type === "agent_activity" && event.agent === "peer-agent");
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "peer-agent-active",
      type: "activity",
      agent_id: "peer-agent",
      instance_id: "peer-agent-instance",
      lease_id: peer.registered.lease_id,
      activity_state: "active",
      subagent_count: 2,
    }));
    const activity = await activeEvent;
    assert.equal(activity.activityState, "active");
    assert.equal(activity.subagentCount, 2);
    assert.equal(Object.hasOwn(activity, "availableModels"), false);

    const catalog = Array.from({ length: 150 }, (_, index) => ({
      provider: "catalog-provider",
      id: `model-${index}`,
      name: `Model ${index}`,
    }));
    const catalogEvent = waitForEvent(browser, (event) => event.type === "agent_model_catalog" && event.agent === "peer-agent");
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "peer-agent-catalog",
      type: "model_catalog",
      agent_id: "peer-agent",
      instance_id: "peer-agent-instance",
      lease_id: peer.registered.lease_id,
      model_summaries: catalog,
    }));
    const catalogProjection = await catalogEvent;
    assert.equal(catalogProjection.availableModels.length, catalog.length);
    assert.deepEqual(catalogProjection.availableModels.at(-1), catalog.at(-1));
    assert.equal(catalogProjection.totalCount, catalog.length);
    assert.equal(catalogProjection.truncated, false);
    const postCatalogActivityEvent = waitForEvent(browser, (event) => event.type === "agent_activity"
      && event.agent === "peer-agent" && event.activityState === "idle");
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "peer-agent-post-catalog-activity",
      type: "activity",
      agent_id: "peer-agent",
      instance_id: "peer-agent-instance",
      lease_id: peer.registered.lease_id,
      activity_state: "settled",
      subagent_count: 0,
    }));
    const postCatalogActivity = await postCatalogActivityEvent;
    assert.equal(Object.hasOwn(postCatalogActivity, "availableModels"), false);
    const postActivitySnapshot = await (await fetch(`${routerHttpUrl}/v1/agents`)).json();
    assert.deepEqual(
      postActivitySnapshot.agents.find((agent) => agent.agent_id === "peer-agent").available_models,
      catalog,
    );
    const lightweightSnapshot = await (await fetch(`${routerHttpUrl}/v1/agents?models=omit`)).json();
    assert.equal(
      Object.hasOwn(lightweightSnapshot.agents.find((agent) => agent.agent_id === "peer-agent"), "available_models"),
      false,
    );

    const targetModel = catalog[0];
    const controlId = "browser-control-accepted";
    const forwardedControl = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control");
    browser.send(JSON.stringify({
      type: "set_model",
      controlId,
      targetAgentId: "peer-agent",
      model: { provider: targetModel.provider, id: targetModel.id },
    }));
    const control = await forwardedControl;
    assert.equal(control.control_id, controlId);
    assert.equal(control.agent_id, "peer-agent");
    assert.equal(control.instance_id, "peer-agent-instance");
    assert.equal(control.lease_id, peer.registered.lease_id);
    assert.equal(control.source_agent_id, webAgentId);
    assert.deepEqual(control.model, { provider: targetModel.provider, id: targetModel.id });
    assert.match(control.expires_at, /^\d{4}-\d{2}-\d{2}T/);

    const modelControlResult = waitForEvent(browser, (event) => event.type === "model_control_result" && event.controlId === control.control_id);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "peer-agent-model-result",
      type: "model_control_result",
      agent_id: "peer-agent",
      instance_id: "peer-agent-instance",
      lease_id: peer.registered.lease_id,
      source_agent_id: webAgentId,
      control_id: control.control_id,
      accepted: true,
      current_model: targetModel,
    }));
    const result = await modelControlResult;
    assert.equal(result.agentId, webAgentId);
    assert.equal(result.targetAgentId, "peer-agent");
    assert.equal(result.instanceId, "peer-agent-instance");
    assert.equal(result.accepted, true);
    assert.equal(matchesActiveScope(result, { routerUrl: routerHttpUrl, agentId: webAgentId }), true);
    const updatedAgents = await (await fetch(`${routerHttpUrl}/v1/agents`)).json();
    const updatedPeer = updatedAgents.agents.find((agent) => agent.agent_id === "peer-agent");
    assert.deepEqual(updatedPeer.current_model, targetModel);
    assert.deepEqual(updatedPeer.available_models, catalog);

    const arrivalEvent = waitForEvent(browser, (event) => event.type === "message_arrival" && event.agent === "peer-agent");
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "peer-agent-arrival",
      type: "message_arrival",
      agent_id: "peer-agent",
      instance_id: "peer-agent-instance",
      lease_id: peer.registered.lease_id,
      from: "supervisor",
      subject: "Check this",
      source: { kind: "agent" },
      preview: "short arrival preview",
      body: "full arrival body\nwith details that do not fit in the row",
      created_at: "2026-07-28T12:59:00.000Z",
      message_id: "peer-arrival-1",
    }));
    const arrival = await arrivalEvent;
    assert.equal(arrival.agent, "peer-agent");
    assert.equal(arrival.from, "supervisor");
    assert.equal(arrival.subject, "Check this");
    assert.equal(arrival.sourceKind, "agent");
    assert.equal(arrival.preview, "short arrival preview");
    assert.equal(arrival.body, "full arrival body\nwith details that do not fit in the row");
    assert.equal(arrival.createdAt, "2026-07-28T12:59:00.000Z");
    assert.equal(arrival.messageId, "peer-arrival-1");

    const deliveredToPeer = nextRouterFrame(peer.socket, (frame) => frame.type === "message" && frame.message.from === webAgentId);
    browser.send(JSON.stringify({ type: "send_message", to: "peer-agent", body: "hello from the browser", localId: "local-1" }));
    const sendResult = await waitForEvent(browser, (event) => event.type === "send_result" && event.localId === "local-1");
    assert.equal(sendResult.ok, true);
    assert.equal(sendResult.message.from, webAgentId);
    assert.deepEqual(sendResult.message.source, { kind: "web_ui" });
    const firstDelivery = await deliveredToPeer;
    assert.equal(firstDelivery.message.body, "hello from the browser\n\n[Reply via mailbox to web-test-client.]");
    assert.deepEqual(firstDelivery.message.source, { kind: "web_ui" });
    ackDelivery(peer.socket, peer.registered, "peer-agent", "peer-agent-instance", firstDelivery);

    const deliveredImage = nextRouterFrame(peer.socket, (frame) => frame.type === "message" && frame.message.from === webAgentId && frame.message.id !== sendResult.message.id);
    browser.send(JSON.stringify({
      type: "send_message",
      to: "peer-agent",
      body: "see screenshots",
      localId: "local-image-1",
      images: [
        { name: "one.jpg", dataUrl: tinyJpegDataUrl() },
        { name: "two.jpg", dataUrl: tinyJpegDataUrl() },
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
    assert.match(imageDelivery.message.body, /see screenshots\n\nAttached images:\n1\. .*\.jpg\n2\. .*\.jpg/);
    assert.doesNotMatch(imageDelivery.message.body, /data:image/);
    ackDelivery(peer.socket, peer.registered, "peer-agent", "peer-agent-instance", imageDelivery);
    const attachmentResponse = await fetch(imageResult.message.attachments[0].url);
    assert.equal(attachmentResponse.status, 200);
    assert.equal(attachmentResponse.headers.get("content-type"), "image/jpeg");

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
    assert.deepEqual(inboundEvent.message.source, { kind: "agent" });
    await waitFor(() => repository.messageFor("peer-to-web-1") === undefined);

    repository.createReminder({
      id: "web-ui-live-reminder",
      target: webAgentId,
      creator: "tester",
      subject: "Reminder subject",
      body: "hello from a reminder",
      created_at: "2026-07-28T13:00:00.000Z",
      next_due_at: "2026-07-28T13:00:00.000Z",
      interval_ms: null,
    });
    const reminderLive = waitForEvent(browser, (event) => event.type === "conversation_message" && event.message.id === "reminder-web-ui-live-reminder-0");
    const firedReminder = router.sweepDueReminders();
    assert.equal(firedReminder.length, 1);
    assert.deepEqual(firedReminder[0].conversation_message.source, { kind: "reminder" });
    const reminderEvent = await reminderLive;
    assert.equal(reminderEvent.message.from, "reminders");
    assert.equal(reminderEvent.message.subject, "Reminder subject");
    assert.equal(reminderEvent.message.body, "hello from a reminder");
    assert.deepEqual(reminderEvent.message.source, { kind: "reminder" });
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router web UI removes an expired lifecycle child from a v2 update and reconnect snapshot", async () => {
  let now = Date.parse("2030-01-01T00:00:00.000Z");
  const router = await startTestRouter({
    clock: () => new Date(now),
    routerOptions: { leaseDurationMs: 8 * 24 * 60 * 60 * 1000 },
  });
  const sockets = [];
  let ui;
  try {
    ui = await startRouterWebUi({ router: router.httpUrl, output: () => {} });
    const reader = await connectBrowserWithLifecycleSnapshot(ui.httpUrl, router.httpUrl, webAgentId, sockets);
    assert.equal(reader.snapshot.routerVersion, 0);

    const producer = await connectRouterAgent(router.wsUrl, "retention-ui-parent", sockets);
    const runningUpdate = waitForEvent(reader.socket, (event) => event.type === "subagent_state_update" && event.agent.agent_id === "retention-ui-parent");
    const runningAcknowledged = nextRouterFrame(producer.socket, (frame) => frame.type === "subagent_lifecycle_acknowledged");
    producer.socket.send(JSON.stringify({
      version: "v1", frame_id: "retention-ui-running", type: "subagent_lifecycle_snapshot",
      agent_id: "retention-ui-parent", instance_id: "retention-ui-parent-instance",
      parent_session_id: "retention-ui-parent-session", lease_id: producer.registered.lease_id,
      producer_epoch: "66666666-6666-4666-8666-666666666666", client_sequence: 1,
      event_id: "retention-ui-running", subagents: [{ id: "retention-ui-child", description: "Running child" }],
    }));
    const v1 = await runningAcknowledged;
    assert.equal(v1.router_version, 1);
    const renderedRunning = upsertLifecycleAgent([], (await runningUpdate).agent);
    assert.equal(renderedRunning[0].subagent_count, 1);
    assert.deepEqual(renderedRunning[0].subagents, [{ id: "retention-ui-child", description: "Running child" }]);
    router.repository.releaseAgentLease({
      agentId: "retention-ui-parent",
      instanceId: "retention-ui-parent-instance",
      leaseId: producer.registered.lease_id,
    });

    now += 7 * 24 * 60 * 60 * 1000;
    const expiredUpdate = waitForEvent(reader.socket, (event) => event.type === "subagent_state_update"
      && event.agent.agent_id === "retention-ui-parent" && event.agent.subagent_count === 0);
    const sweep = router.router.sweepMessageLifecycles();
    assert.equal(sweep.lifecycle.expiredRunning, 1);
    const v2 = await expiredUpdate;
    assert.equal(v2.routerVersion, 2);
    const renderedExpired = upsertLifecycleAgent(renderedRunning, v2.agent);
    assert.equal(renderedExpired[0].subagent_count, 0);
    assert.deepEqual(renderedExpired[0].subagents, []);

    reader.socket.close();
    await onceClose(reader.socket);
    const reconnected = await connectBrowserWithLifecycleSnapshot(ui.httpUrl, router.httpUrl, webAgentId, sockets);
    assert.equal(reconnected.snapshot.routerVersion, 2);
    assert.deepEqual(reconnected.snapshot.agents, []);
    const renderedReconnect = applyLifecycleSnapshot(
      renderedExpired,
      reconnected.snapshot.agents,
      reconnected.snapshot.routerVersion,
      v2.routerVersion,
    );
    assert.equal(renderedReconnect.agents[0].subagent_count, 0);
    assert.deepEqual(renderedReconnect.agents[0].subagents, []);
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
  }
});

test("router web UI passes received subagent activity through /v1/agents and live frames and clears on zero", async () => {
  const router = await startTestRouter();
  const sockets = [];
  let ui;
  try {
    const peer = await connectRouterAgent(router.wsUrl, "activity-peer", sockets);
    ui = await startRouterWebUi({ router: router.httpUrl, output: () => {} });
    const browser = await connectBrowser(ui.httpUrl, router.httpUrl, webAgentId, sockets);

    const summaries = [{ id: "child-a", type: "general-purpose", description: "First running subagent" }];
    const activityEvent = waitForEvent(browser, (event) => event.type === "agent_activity" && event.agent === "activity-peer" && event.subagentCount === 1);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "activity-peer-with-subagents",
      type: "activity",
      agent_id: "activity-peer",
      instance_id: "activity-peer-instance",
      lease_id: peer.registered.lease_id,
      activity_state: "active",
      subagent_count: 1,
      subagents: summaries,
    }));
    const activity = await activityEvent;
    assert.equal(activity.activityState, "active");
    assert.equal(activity.subagentCount, 1);
    assert.deepEqual(activity.subagents, summaries);

    const agentsAfterUpdate = await routerAgents(router.httpUrl);
    const peerSnapshot = agentsAfterUpdate.find((agent) => agent.agent_id === "activity-peer");
    assert.ok(peerSnapshot, "activity-peer missing from /v1/agents after activity");
    assert.equal(peerSnapshot.activity_state, "active");
    assert.equal(peerSnapshot.subagent_count, 1);
    assert.deepEqual(peerSnapshot.subagents, summaries);

    const clearedEvent = waitForEvent(browser, (event) => event.type === "agent_activity" && event.agent === "activity-peer" && event.subagentCount === 0);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "activity-peer-zero",
      type: "activity",
      agent_id: "activity-peer",
      instance_id: "activity-peer-instance",
      lease_id: peer.registered.lease_id,
      activity_state: "settled",
      subagent_count: 0,
      subagents: [],
    }));
    const cleared = await clearedEvent;
    assert.equal(cleared.activityState, "idle");
    assert.equal(cleared.subagentCount, 0);
    assert.deepEqual(cleared.subagents, []);

    await waitFor(async () => {
      const agents = await routerAgents(router.httpUrl);
      const peerAgent = agents.find((agent) => agent.agent_id === "activity-peer");
      return peerAgent !== undefined && peerAgent.subagent_count === 0;
    });
    const agentsAfterClear = await routerAgents(router.httpUrl);
    const peerCleared = agentsAfterClear.find((agent) => agent.agent_id === "activity-peer");
    assert.ok(peerCleared);
    assert.equal(peerCleared.subagent_count, 0);
    assert.deepEqual(peerCleared.subagents, []);
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
  }
});

test("router web app renders the agent-row subagent badge from received activity without a second source of truth", async (t) => {
  const router = await startTestRouter();
  const sockets = [];
  let ui;
  let domBrowser;
  try {
    const peer = await connectRouterAgent(router.wsUrl, "dom-activity-peer", sockets);
    ui = await startRouterWebUi({ router: router.httpUrl, output: () => {} });
    const browser = await connectBrowser(ui.httpUrl, router.httpUrl, webAgentId, sockets);
    try {
      domBrowser = await startElectronDomBrowser(ui.httpUrl);
    } catch (error) {
      if (error.code === "ELECTRON_UNAVAILABLE") {
        t.skip(`Electron cannot launch in this test environment: ${error.message}`);
        return;
      }
      throw error;
    }

    browser.send(JSON.stringify({ type: "refresh_agents" }));
    await waitForEvent(browser, (event) => event.type === "agents" && event.agents.some((agent) => agent.agent_id === "dom-activity-peer"));

    const summaries = [{ id: "child-a", type: "general-purpose", description: "First running subagent" }];
    const activityEvent = waitForEvent(browser, (event) => event.type === "agent_activity" && event.agent === "dom-activity-peer" && event.subagentCount === 1);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-activity-peer-with-subagents",
      type: "activity",
      agent_id: "dom-activity-peer",
      instance_id: "dom-activity-peer-instance",
      lease_id: peer.registered.lease_id,
      activity_state: "active",
      subagent_count: 1,
      subagents: summaries,
    }));
    await activityEvent;

    const row = await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        const candidate = [...document.querySelectorAll(".agent-row")].find((element) => element.textContent.includes("Dom Activity Peer"));
        if (candidate?.classList.contains("has-subagents")) return { className: candidate.className, innerHTML: candidate.innerHTML };
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      const candidate = [...document.querySelectorAll(".agent-row")].find((element) => element.textContent.includes("Dom Activity Peer"));
      return candidate ? { className: candidate.className, innerHTML: candidate.innerHTML } : null;
    })()`);
    assert.ok(row, "activity peer row was not rendered");
    assert.match(row.className, /has-subagents/);
    assert.match(row.innerHTML, /1 subagent/);
    assert.match(row.innerHTML, /subagent-indicator/);

    const clearedActivityEvent = waitForEvent(browser, (event) => event.type === "agent_activity" && event.agent === "dom-activity-peer" && event.subagentCount === 0);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-activity-peer-without-subagents",
      type: "activity",
      agent_id: "dom-activity-peer",
      instance_id: "dom-activity-peer-instance",
      lease_id: peer.registered.lease_id,
      activity_state: "active",
      subagent_count: 0,
      subagents: [],
    }));
    await clearedActivityEvent;

    const clearedRow = await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        const candidate = [...document.querySelectorAll(".agent-row")].find((element) => element.textContent.includes("Dom Activity Peer"));
        if (candidate && !candidate.classList.contains("has-subagents")) return { className: candidate.className, innerHTML: candidate.innerHTML };
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      const candidate = [...document.querySelectorAll(".agent-row")].find((element) => element.textContent.includes("Dom Activity Peer"));
      return candidate ? { className: candidate.className, innerHTML: candidate.innerHTML } : null;
    })()`);
    assert.ok(clearedRow, "activity peer row was not rendered after clearing subagents");
    assert.doesNotMatch(clearedRow.className, /has-subagents/);
    assert.doesNotMatch(clearedRow.innerHTML, /subagent-indicator/);
    assert.doesNotMatch(clearedRow.innerHTML, /1 subagent/);
  } finally {
    await domBrowser?.close();
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
  }
});

test("router web app sends a model control from the picker and applies the result", async (t) => {
  const router = await startTestRouter();
  const sockets = [];
  let ui;
  let domBrowser;
  try {
    const peer = await connectRouterAgent(router.wsUrl, "dom-model-peer", sockets);
    ui = await startRouterWebUi({ router: router.httpUrl, output: () => {} });
    try {
      domBrowser = await startElectronDomBrowser(ui.httpUrl);
    } catch (error) {
      if (error.code === "ELECTRON_UNAVAILABLE") {
        t.skip(`Electron cannot launch in this test environment: ${error.message}`);
        return;
      }
      throw error;
    }

    const currentModel = { provider: "provider", id: "current", name: "Current model" };
    const targetModel = { provider: "provider", id: "target", name: "Target model" };
    const fallbackModel = { provider: "provider", id: "fallback", name: "Fallback model" };
    const peerReady = await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        if ([...document.querySelectorAll(".agent-row")].some((element) => element.textContent.includes("Dom Model Peer"))) return true;
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      return false;
    })()`);
    assert.equal(peerReady, true);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-model-peer-catalog",
      type: "model_catalog",
      agent_id: "dom-model-peer",
      instance_id: "dom-model-peer-instance",
      lease_id: peer.registered.lease_id,
      model_summaries: [currentModel, targetModel],
    }));

    const targetReady = await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        const row = [...document.querySelectorAll(".agent-row")].find((element) => element.textContent.includes("Dom Model Peer"));
        if (row) {
          row.click();
          const controls = document.querySelector("#model-controls");
          if (!controls?.hidden && controls.querySelectorAll(".model-option").length === 2) return true;
        }
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      return false;
    })()`);
    assert.equal(targetReady, true);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-model-peer-activity-after-catalog",
      type: "activity",
      agent_id: "dom-model-peer",
      instance_id: "dom-model-peer-instance",
      lease_id: peer.registered.lease_id,
      activity_state: "active",
      subagent_count: 0,
    }));
    const catalogPreserved = await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        const controls = document.querySelector("#model-controls");
        if (!controls?.hidden && controls.querySelectorAll(".model-option").length === 2) return true;
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      return false;
    })()`);
    assert.equal(catalogPreserved, true);

    const forwardedControl = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control");
    const clicked = await domBrowser.evaluate(`(() => {
      const option = [...document.querySelectorAll(".model-option")].find((element) => element.textContent.includes("Target model"));
      option?.click();
      return option !== undefined;
    })()`);
    assert.equal(clicked, true);
    const control = await forwardedControl;
    assert.equal(control.agent_id, "dom-model-peer");
    assert.equal(control.source_agent_id, "wolfgang-web");
    assert.deepEqual(control.model, { provider: targetModel.provider, id: targetModel.id });

    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-model-peer-result",
      type: "model_control_result",
      agent_id: "dom-model-peer",
      instance_id: "dom-model-peer-instance",
      lease_id: peer.registered.lease_id,
      source_agent_id: "wolfgang-web",
      control_id: control.control_id,
      accepted: true,
      current_model: targetModel,
    }));
    const applied = await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        if (document.querySelector("#model-current-label")?.textContent === "provider/target") return true;
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      return false;
    })()`);
    assert.equal(applied, true);
    const successStatus = await domBrowser.evaluate(`(() => ({
      title: document.querySelector("#status-title")?.textContent,
      detail: document.querySelector("#status-detail")?.textContent,
    }))()`);
    assert.equal(successStatus.title, "Model changed");
    assert.equal(successStatus.detail, "dom-model-peer · provider/target");

    const rejectedControlFrame = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id !== control.control_id);
    const selectedCurrent = await domBrowser.evaluate(`(() => {
      document.querySelector("#model-current")?.click();
      const option = [...document.querySelectorAll(".model-option")].find((element) => element.textContent.includes("Current model"));
      option?.click();
      return option !== undefined;
    })()`);
    assert.equal(selectedCurrent, true);
    const rejectedControl = await rejectedControlFrame;
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-model-peer-fallback-catalog",
      type: "model_catalog",
      agent_id: "dom-model-peer",
      instance_id: "dom-model-peer-instance",
      lease_id: peer.registered.lease_id,
      model_summaries: [fallbackModel, targetModel],
    }));
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-model-peer-rejected-result",
      type: "model_control_result",
      agent_id: "dom-model-peer",
      instance_id: "dom-model-peer-instance",
      lease_id: peer.registered.lease_id,
      source_agent_id: "wolfgang-web",
      control_id: rejectedControl.control_id,
      accepted: false,
      error_code: "model_unavailable",
      current_model: fallbackModel,
    }));
    const rejectedStatus = await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        const title = document.querySelector("#status-title")?.textContent;
        const detail = document.querySelector("#status-detail")?.textContent;
        const current = document.querySelector("#model-current-label")?.textContent;
        const options = [...document.querySelectorAll(".model-option")].map((element) => element.textContent);
        if (title === "Model change rejected" && detail === "dom-model-peer · model_unavailable"
          && current === "provider/fallback" && options.length === 2) return { title, detail, current, options };
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      return {
        title: document.querySelector("#status-title")?.textContent,
        detail: document.querySelector("#status-detail")?.textContent,
        current: document.querySelector("#model-current-label")?.textContent,
        options: [...document.querySelectorAll(".model-option")].map((element) => element.textContent),
      };
    })()`);
    assert.equal(rejectedStatus.title, "Model change rejected");
    assert.equal(rejectedStatus.detail, "dom-model-peer · model_unavailable");
    assert.equal(rejectedStatus.current, "provider/fallback");
    assert.equal(rejectedStatus.options.length, 2);

    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-model-peer-duplicate-result",
      type: "model_control_result",
      agent_id: "dom-model-peer",
      instance_id: "dom-model-peer-instance",
      lease_id: peer.registered.lease_id,
      source_agent_id: "wolfgang-web",
      control_id: rejectedControl.control_id,
      accepted: true,
      current_model: targetModel,
    }));
    await new Promise((resolve) => setTimeout(resolve, 100));
    assert.deepEqual(await domBrowser.evaluate(`(() => ({
      title: document.querySelector("#status-title")?.textContent,
      current: document.querySelector("#model-current-label")?.textContent,
    }))()`), { title: "Model change rejected", current: "provider/fallback" });

    const emptyCatalogControlFrame = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id !== rejectedControl.control_id);
    assert.equal(await domBrowser.evaluate(`(() => {
      document.querySelector("#model-current")?.click();
      const option = [...document.querySelectorAll(".model-option")].find((element) => element.textContent.includes("Target model"));
      option?.click();
      return option !== undefined;
    })()`), true);
    const emptyCatalogControl = await emptyCatalogControlFrame;
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-model-peer-empty-catalog",
      type: "model_catalog",
      agent_id: "dom-model-peer",
      instance_id: "dom-model-peer-instance",
      lease_id: peer.registered.lease_id,
      model_summaries: [],
    }));
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-model-peer-empty-catalog-result",
      type: "model_control_result",
      agent_id: "dom-model-peer",
      instance_id: "dom-model-peer-instance",
      lease_id: peer.registered.lease_id,
      source_agent_id: "wolfgang-web",
      control_id: emptyCatalogControl.control_id,
      accepted: false,
      error_code: "model_unavailable",
      current_model: fallbackModel,
    }));
    assert.equal(await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        if (document.querySelector("#model-controls")?.hidden) return true;
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      return false;
    })()`), true);

    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "dom-model-peer-catalog-for-disconnect",
      type: "model_catalog",
      agent_id: "dom-model-peer",
      instance_id: "dom-model-peer-instance",
      lease_id: peer.registered.lease_id,
      model_summaries: [fallbackModel, targetModel],
    }));
    const disconnectControlFrame = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id !== emptyCatalogControl.control_id);
    assert.equal(await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        const currentButton = document.querySelector("#model-current");
        if (!document.querySelector("#model-controls")?.hidden) {
          currentButton?.click();
          const option = [...document.querySelectorAll(".model-option")].find((element) => element.textContent.includes("Target model"));
          if (option) {
            option.click();
            return document.querySelector("#model-current-label")?.textContent === "provider/target";
          }
        }
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      return false;
    })()`), true);
    await disconnectControlFrame;
    await ui.close();
    ui = undefined;
    assert.equal(await domBrowser.evaluate(`(async () => {
      const deadline = Date.now() + 2_000;
      while (Date.now() < deadline) {
        const label = document.querySelector("#model-current-label")?.textContent;
        if (label === "provider/fallback") return label;
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      return document.querySelector("#model-current-label")?.textContent;
    })()`), "provider/fallback");
    assert.deepEqual(await domBrowser.evaluate(`(() => {
      document.querySelector("#model-current")?.click();
      const option = [...document.querySelectorAll(".model-option")].find((element) => element.textContent.includes("Target model"));
      option?.click();
      return {
        title: document.querySelector("#status-title")?.textContent,
        detail: document.querySelector("#status-detail")?.textContent,
        current: document.querySelector("#model-current-label")?.textContent,
      };
    })()`), {
      title: "Model change not confirmed",
      detail: "dom-model-peer · ui_not_connected",
      current: "provider/fallback",
    });
  } finally {
    await domBrowser?.close();
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
  }
});

test("router web UI correlates target-not-connected errors to the caller control ID", async () => {
  const router = await startTestRouter();
  const sockets = [];
  let ui;
  try {
    ui = await startRouterWebUi({ router: router.httpUrl, output: () => {} });
    const browser = await connectBrowser(ui.httpUrl, router.httpUrl, webAgentId, sockets);
    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "missing-target-control",
      targetAgentId: "missing-target",
      model: { provider: "provider", id: "model" },
    }));
    const rejected = await waitForEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "missing-target-control");
    assert.equal(rejected.accepted, false);
    assert.equal(rejected.errorCode, "target_not_connected");
    assert.equal(rejected.targetAgentId, "missing-target");

    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "invalid-command-control",
      targetAgentId: "missing-target",
      model: { provider: "", id: "model" },
    }));
    const invalid = await waitForEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "invalid-command-control");
    assert.equal(invalid.accepted, false);
    assert.equal(invalid.errorCode, "command_error");
    assert.equal(invalid.targetAgentId, "missing-target");
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
  }
});

test("router web UI rejects timed out and reconnecting controls and ignores late results", async () => {
  const router = await startTestRouter();
  const sockets = [];
  let ui;
  try {
    const peer = await connectRouterAgent(router.wsUrl, "control-peer", sockets);
    ui = await startRouterWebUi({
      router: router.httpUrl,
      modelControlTimeoutMs: 50,
      output: () => {},
    });
    const browser = await connectBrowser(ui.httpUrl, router.httpUrl, webAgentId, sockets);

    const timedOutForward = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id === "timed-out-control");
    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "timed-out-control",
      targetAgentId: "control-peer",
      model: { provider: "provider", id: "slow" },
    }));
    const timedOutControl = await timedOutForward;
    const timedOut = await waitForEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "timed-out-control");
    assert.equal(timedOut.accepted, false);
    assert.equal(timedOut.errorCode, "control_timeout");

    const noLateTimeout = expectNoEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "timed-out-control", 150);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "late-timeout-result",
      type: "model_control_result",
      agent_id: "control-peer",
      instance_id: "control-peer-instance",
      lease_id: peer.registered.lease_id,
      source_agent_id: webAgentId,
      control_id: timedOutControl.control_id,
      accepted: true,
      current_model: { provider: "provider", id: "slow" },
    }));
    await noLateTimeout;

    const reconnectForward = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id === "reconnecting-control");
    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "reconnecting-control",
      targetAgentId: "control-peer",
      model: { provider: "provider", id: "reconnect" },
    }));
    const reconnectControl = await reconnectForward;
    const reconnectRejected = waitForEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "reconnecting-control");
    const reconnected = waitForEvent(browser, (event) => event.type === "status" && event.state === "connected", 2_000);
    browser.send(JSON.stringify({ type: "connect", routerUrl: router.httpUrl, agentId: webAgentId }));
    const rejection = await reconnectRejected;
    assert.equal(rejection.accepted, false);
    assert.equal(rejection.errorCode, "router_reconnecting");
    await reconnected;

    const noLateReconnect = expectNoEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "reconnecting-control", 150);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "late-reconnect-result",
      type: "model_control_result",
      agent_id: "control-peer",
      instance_id: "control-peer-instance",
      lease_id: peer.registered.lease_id,
      source_agent_id: webAgentId,
      control_id: reconnectControl.control_id,
      accepted: false,
      error_code: "late_result",
    }));
    await noLateReconnect;
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
  }
});

test("router web UI uses the router pending-control TTL as the authoritative timeout", async () => {
  const router = await startTestRouter({
    routerOptions: { pendingModelControlTtlMs: 80, timeoutSweepIntervalMs: 10 },
  });
  const sockets = [];
  let ui;
  try {
    const peer = await connectRouterAgent(router.wsUrl, "ttl-peer", sockets);
    ui = await startRouterWebUi({ router: router.httpUrl, output: () => {} });
    const browser = await connectBrowser(ui.httpUrl, router.httpUrl, webAgentId, sockets);
    const forwarded = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id === "ttl-control");
    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "ttl-control",
      targetAgentId: "ttl-peer",
      model: { provider: "provider", id: "slow" },
    }));
    await forwarded;
    const expired = await waitForEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "ttl-control", 2_000);
    assert.equal(expired.accepted, false);
    assert.equal(expired.errorCode, "model_control_expired");
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
  }
});

test("router web UI bounds delayed capability discovery and falls back to the default control timeout", async () => {
  const router = await startTestRouter();
  const sockets = [];
  let ui;
  try {
    const peer = await connectRouterAgent(router.wsUrl, "delayed-capability-peer", sockets);
    ui = await startRouterWebUi({
      router: router.httpUrl,
      capabilityDiscoveryTimeoutMs: 20,
      capabilitiesFetchImpl: (_url, { signal }) => new Promise((_resolve, reject) => {
        signal.addEventListener("abort", () => reject(signal.reason), { once: true });
      }),
      output: () => {},
    });
    const browser = await connectBrowser(ui.httpUrl, router.httpUrl, webAgentId, sockets);
    const forwarded = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id === "delayed-capability-control");
    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "delayed-capability-control",
      targetAgentId: "delayed-capability-peer",
      model: { provider: "provider", id: "slow" },
    }));
    const control = await forwarded;
    await expectNoEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === control.control_id, 100);
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
  }
});

test("router web UI resets a discovered control timeout when reconnect capability discovery fails", async () => {
  const directory = await mkdtemp(join(tmpdir(), "router-web-ui-timeout-reset-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite") });
  let router = createAgentRouter({
    repository,
    pendingModelControlTtlMs: 1,
    timeoutSweepIntervalMs: 60_000,
  });
  const sockets = [];
  let ui;
  let capabilityCalls = 0;
  try {
    let wsUrl = await router.listen();
    const routerHttpUrl = wsUrl.replace("ws://", "http://").replace("/v1/agents", "");
    const routerPort = Number(new URL(routerHttpUrl).port);
    ui = await startRouterWebUi({
      router: routerHttpUrl,
      capabilitiesFetchImpl: async (url, options) => {
        capabilityCalls += 1;
        if (capabilityCalls > 1) throw new Error("capabilities unavailable after restart");
        return fetch(url, options);
      },
      output: () => {},
    });
    const browser = await connectBrowser(ui.httpUrl, routerHttpUrl, "timeout-reset-web", sockets);
    const reconnecting = waitForEvent(browser, (event) => event.type === "status" && event.state === "reconnecting", 2_000);
    await router.close();
    await reconnecting;

    router = createAgentRouter({
      repository,
      port: routerPort,
      timeoutSweepIntervalMs: 60_000,
    });
    wsUrl = await router.listen();
    const reconnected = waitForEvent(browser, (event) => event.type === "status" && event.state === "connected", 3_000);
    const peer = await connectRouterAgent(wsUrl, "timeout-reset-peer", sockets);
    await reconnected;
    const forwarded = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id === "timeout-reset-control");
    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "timeout-reset-control",
      targetAgentId: "timeout-reset-peer",
      model: { provider: "provider", id: "slow" },
    }));
    await forwarded;
    await expectNoEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "timeout-reset-control", 2_100);
    assert.equal(capabilityCalls, 2);
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
    repository.close();
    await rm(directory, { recursive: true, force: true });
  }
});

test("router web UI evicts the oldest pending model control at capacity", async () => {
  const router = await startTestRouter();
  const sockets = [];
  let ui;
  try {
    const peer = await connectRouterAgent(router.wsUrl, "capacity-peer", sockets);
    ui = await startRouterWebUi({
      router: router.httpUrl,
      maxPendingModelControls: 1,
      output: () => {},
    });
    const browser = await connectBrowser(ui.httpUrl, router.httpUrl, webAgentId, sockets);

    const firstForward = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id === "capacity-control-one");
    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "capacity-control-one",
      targetAgentId: "capacity-peer",
      model: { provider: "provider", id: "one" },
    }));
    const firstControl = await firstForward;

    const evicted = waitForEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "capacity-control-one");
    const secondForward = nextRouterFrame(peer.socket, (frame) => frame.type === "model_control"
      && frame.control_id === "capacity-control-two");
    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "capacity-control-two",
      targetAgentId: "capacity-peer",
      model: { provider: "provider", id: "two" },
    }));
    assert.equal((await evicted).errorCode, "too_many_pending_controls");
    const secondControl = await secondForward;

    const noLateFirstResult = expectNoEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "capacity-control-one", 150);
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "capacity-result-one",
      type: "model_control_result",
      agent_id: "capacity-peer",
      instance_id: "capacity-peer-instance",
      lease_id: peer.registered.lease_id,
      source_agent_id: webAgentId,
      control_id: firstControl.control_id,
      accepted: true,
      current_model: { provider: "provider", id: "one" },
    }));
    await noLateFirstResult;

    const accepted = waitForEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "capacity-control-two");
    peer.socket.send(JSON.stringify({
      version: "v1",
      frame_id: "capacity-result-two",
      type: "model_control_result",
      agent_id: "capacity-peer",
      instance_id: "capacity-peer-instance",
      lease_id: peer.registered.lease_id,
      source_agent_id: webAgentId,
      control_id: secondControl.control_id,
      accepted: true,
      current_model: { provider: "provider", id: "two" },
    }));
    assert.equal((await accepted).accepted, true);
  } finally {
    for (const socket of sockets) socket.close?.();
    await ui?.close();
    await router.close();
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
      images: [{ name: "shared.jpg", dataUrl: tinyJpegDataUrl() }],
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
  let router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000, deliveryOfferCoalesceMs: 0 });
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

    const controlPeer = await connectRouterAgent(wsUrl, "restart-control-peer", sockets);
    const forwardedControl = nextRouterFrame(controlPeer.socket, (frame) => frame.type === "model_control"
      && frame.control_id === "router-close-control");
    browser.send(JSON.stringify({
      type: "set_model",
      controlId: "router-close-control",
      targetAgentId: "restart-control-peer",
      model: { provider: "provider", id: "pending-at-close" },
    }));
    await forwardedControl;
    const closeRejected = waitForEvent(browser, (event) => event.type === "model_control_result"
      && event.controlId === "router-close-control", 2_000);
    const reconnecting = waitForEvent(browser, (event) => event.type === "status" && event.state === "reconnecting", 2_000);
    await router.close();
    const closeResult = await closeRejected;
    assert.equal(closeResult.accepted, false);
    assert.equal(closeResult.errorCode, "router_disconnected");
    assert.equal(closeResult.targetAgentId, "restart-control-peer");
    await reconnecting;
    router = createAgentRouter({ repository, port: routerPort, timeoutSweepIntervalMs: 60_000, deliveryOfferCoalesceMs: 0 });
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
  assert.match(html, /id="router-alias"/);
  assert.match(html, /id="open-add-router"/);
  assert.match(html, /id="router-setup" class="router-setup" hidden/);
  assert.match(html, /id="image-input" type="file" accept="image\/\*" multiple/);
  assert.match(html, /font-awesome\/6\.7\.2\/css\/all\.min\.css/);
  assert.match(html, /<script src="\/vendor\/markdown-it\.js"><\/script>/);
  assert.match(html, /id="refresh" type="button" aria-label="Refresh agents"/);
  assert.match(html, /id="attach-image" class="secondary attach-image" type="button" disabled aria-label="Attach images"/);
  assert.match(html, /id="open-add-router" type="button" aria-label="Add router"/);
  assert.match(html, /id="close-router-dialog" value="cancel" aria-label="Close routers"/);
  assert.match(html, /id="model-picker" class="model-picker"/);
  assert.match(html, /id="model-current" class="model-current" type="button"/);
  assert.match(html, /id="model-search" class="model-search" type="text"/);
  assert.match(html, /id="model-catalog-note" class="model-catalog-note" hidden/);
  assert.match(html, /id="model-options" class="model-options" role="listbox"/);
  assert.doesNotMatch(html, /model-apply|datalist/);
  assert.match(html, /<i class="fa-solid fa-arrows-rotate" aria-hidden="true"><\/i>/);
  assert.match(html, /<i class="fa-solid fa-chevron-down" aria-hidden="true"><\/i>/);
  assert.match(html, /<i class="fa-solid fa-paperclip" aria-hidden="true"><\/i>/);
  assert.match(html, /<i class="fa-solid fa-plus" aria-hidden="true"><\/i>/);
  assert.match(html, /<i class="fa-solid fa-xmark" aria-hidden="true"><\/i>/);
  assert.doesNotMatch(html, /[↻⌄＋×]/);
  assert.doesNotMatch(html, />\+<\/button>/);
  assert.match(app, /const MAX_IMAGES = 3;/);
  assert.match(app, /const MAX_IMAGE_EDGE = 1600;/);
  assert.match(app, /canvas\.toBlob\(resolve, "image\/jpeg", IMAGE_JPEG_QUALITY\)/);
  assert.match(app, /context\.fillStyle = "#fff"/);
  assert.match(app, /elements\.composer\.addEventListener\("drop"/);
  assert.match(app, /elements\.draft\.addEventListener\("paste"/);
  assert.match(app, /function renderImagePreview\(\)/);
  assert.match(app, /function renderMessageImages\(attachments\)/);
  assert.match(app, /function formatMessageTimestamp\(message\)/);
  assert.match(app, /timestamp\.dateTime = message\.created_at \|\| message\.createdAt \|\| message\.recorded_at \|\| ""/);
  assert.match(app, /label\.append\(sender, timestamp\)/);
  assert.match(styles, /\.bubble-label \{ display: flex; align-items: baseline; gap: 8px;/);
  assert.match(app, /state\.reminders = Array\.isArray\(event\.reminders\) \? event\.reminders : \[\];/);
  assert.match(app, /reminder\.target === peer && \["scheduled", "due", "message_queued"\]\.includes\(reminder\.state\)/);
  assert.match(app, /function reminderIndicatorMarkup\(reminders, peerName\)/);
  assert.match(app, /aria-haspopup="dialog" aria-controls="reminders-dialog"/);
  assert.match(app, /class="agent-name-line">\$\{reminderIndicatorMarkup\(reminders, humanName\(peer\)\)\}/);
  assert.match(html, /<dialog id="reminders-dialog" class="message-dialog"/);
  assert.match(styles, /#reminders-dialog-body \{ max-height: min\(52vh, 520px\); overflow-y: auto;/);
  assert.match(styles, /\.reminder-indicator \{[\s\S]*width: 24px;[\s\S]*height: 24px;[\s\S]*border-radius: 999px;/);
  assert.match(styles, /\.agent-name-line \{ display: flex; align-items: center; gap: 8px;/);
  assert.match(app, /const MESSAGE_BOTTOM_THRESHOLD_PX = 80;/);
  assert.match(app, /elements\.messages\.addEventListener\("scroll", \(\) => \{ state\.followMessages = isMessagesNearBottom\(\); \}\)/);
  assert.match(app, /function scrollMessagesToBottom\(\)/);
  assert.match(app, /function scrollMessagesToBottomIfFollowing\(\)/);
  assert.match(app, /const appendedMessage = previousLastKey && nextLastKey !== previousLastKey && messages\.length >= previousCount;/);
  assert.match(app, /const wasFollowing = state\.followMessages \|\| isMessagesNearBottom\(\);/);
  assert.match(app, /const shouldFollow = wasFollowing && \(!previousLastKey \|\| appendedMessage \|\| messages\.length >= previousCount\);/);
  assert.match(app, /if \(state\.renderedPeer === peer && state\.renderedMessageSignatures\[peer\] === signature\) return;/);
  assert.match(app, /if \(shouldFollow\) scrollMessagesToBottom\(\);/);
  assert.match(app, /else elements\.messages\.scrollTop = scrollTop;/);
  assert.match(app, /image\.addEventListener\("load", scrollMessagesToBottomIfFollowing, \{ once: true \}\)/);
  assert.doesNotMatch(app, /localStorage\.setItem\([^\n]+data:image/);
  assert.match(app, /function messageForStorage\(message\)/);
  assert.match(app, /startsWith\("data:image\/"\)/);
  assert.match(app, /mecha\.web\.agentIds/);
  assert.match(app, /WEB_DEFAULT_AGENT_ID = "wolfgang-web"/);
  assert.match(app, /config\.defaultAgentId/);
  assert.match(app, /mecha\.web\.routerAliases/);
  assert.match(app, /function setRouterAlias\(routerUrl, alias\)/);
  assert.match(app, /function openRouterEditor\(mode, routerUrl = ""\)/);
  assert.match(app, /function saveRouterEditor\(\)/);
  assert.match(app, /edit\.setAttribute\("aria-label", `Edit \$\{displayRouter\(routerUrl\)\}`\)/);
  assert.match(app, /icon\("fa-pen"\)/);
  assert.match(app, /icon\("fa-trash"\)/);
  assert.match(app, /icon\("fa-xmark"\)/);
  assert.match(app, /element\.setAttribute\("aria-hidden", "true"\)/);
  assert.doesNotMatch(app, /[↻⌄＋✎🗑×]/);
  assert.match(app, /remove\.setAttribute\("aria-label", `Remove \$\{displayRouter\(routerUrl\)\}`\)/);
  assert.ok(app.includes('remove.setAttribute("aria-label", `Remove ${image.name || `image ${index + 1}`}`)'));
  assert.match(app, /elements\.selectedRouterLabel\.textContent = displayRouter\(state\.routerUrl\)/);
  assert.match(app, /title\.textContent = alias \|\| new URL\(routerUrl\)\.host/);
  assert.match(app, /elements\.peerName\.innerHTML = peer \? `\$\{escapeHtml\(humanName\(peer\)\)\}\$\{active \? '<span class="working-indicator inline"/);
  assert.match(app, /compacting \? compactingBadgeMarkup\("inline"\)/);
  assert.match(app, /arrivalIndicatorMarkup\(arrival, "header"\)/);
  assert.match(app, /attachSubagentToggle\(elements\.peerDetail, peer, !compacting && subagents\.length > 0, renderConversation\)/);
  assert.match(app, /attachArrivalActions\(elements\.peerDetail, arrival\)/);
  assert.match(styles, /#peer-name \{[\s\S]*display: flex;[\s\S]*gap: 10px;/);
  assert.match(styles, /\.peer-activity \{[\s\S]*display: flex;[\s\S]*gap: 8px;/);
  assert.doesNotMatch(app, /peerDetail\.textContent = peer \? `\$\{peer\} · \$\{active \? "Working" : "Available"\}/);
  assert.doesNotMatch(app, /peerDetail\.textContent = peer \? `\$\{peer\} · \$\{displayRouter\(state\.routerUrl\)\}`/);
  assert.match(styles, /\.router-row-actions/);
  assert.match(styles, /\.icon-button/);
  assert.match(webState, /function matchesActiveScope/);
  assert.match(webState, /"message"/);
  assert.match(webState, /"agent_activity"/);
  assert.match(webState, /"message_arrival"/);
  assert.match(webState, /event\.routerUrl === active\.routerUrl && event\.agentId === active\.agentId/);
  assert.match(app, /row\.addEventListener\("click", \(\) => \{ void switchRouter\(routerUrl, \{ connect: true \}\); \}\)/);
  assert.doesNotMatch(app, /Select"/);
  assert.match(app, /url\.username !== "" \|\| url\.password !== ""/);
  assert.match(app, /event\.key === "Enter" && !event\.shiftKey/);
  assert.match(app, /elements\.draft\.addEventListener\("input", \(\) => \{ normalizeDraftEscapedNewlines\(\); saveCurrentDraft\(\); updateSendState\(\); \}\)/);
  assert.match(app, /function normalizeEscapedNewlines\(value\)/);
  assert.match(app, /return value\.replace\(\/\\\\r\\\\n\|\\\\n\|\\\\r\/g, "\\n"\)/);
  assert.match(app, /const body = normalizeEscapedNewlines\(elements\.draft\.value\);/);
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
  assert.match(app, /if \(event\.state === "connected"\) backfillSelectedConversation\(\)/);
  assert.match(app, /function backfillSelectedConversation\(\) \{[\s\S]*requestHistory\(peer\);[\s\S]*setTimeout\(\(\) => \{[\s\S]*state\.selectedPeer === peer && state\.routerUrl === routerUrl && state\.agentId === agentId[\s\S]*requestHistory\(peer\);[\s\S]*\}, 500\);[\s\S]*\}/);
  assert.match(app, /elements\.agentFilter\.addEventListener\("input"/);
  assert.match(app, /elements\.modelCurrent\.addEventListener\("click"/);
  assert.match(app, /elements\.modelSearch\.addEventListener\("input"/);
  assert.match(app, /function setModelPickerOpen/);
  assert.match(app, /function selectModel/);
  assert.match(app, /sendCommand\(\{ type: "set_model", controlId, targetAgentId: peer, model: \{ provider: model\.provider, id: model\.id \} \}\)/);
  assert.match(app, /event\.type === "agent_model_catalog"/);
  assert.match(app, /const settled = consumePendingModelControl\(state\.pendingModelControls, event\);/);
  assert.match(app, /updateAgentModelCatalog\(targetAgentId, event\.instanceId, projection, \{ render: false, replaceInstance: false \}\)/);
  assert.match(app, /function updateAgentModelCatalog/);
  assert.match(app, /`Showing \$\{models\.length\} of \$\{totalCount\} models\.`/);
  assert.match(app, /if \(modelPickerOpen\) renderModelOptions\(models\);/);
  assert.match(webState, /const MAX_MODEL_SUMMARIES = 1_000;/);
  assert.match(webState, /\.slice\(0, MAX_MODEL_SUMMARIES\)/);
  assert.match(app, /elements\.modelOptions\.replaceChildren/);
  assert.doesNotMatch(app, /modelSelect|modelApply/);
  assert.match(app, /event\.type === "agent_activity"/);
  assert.match(app, /event\.type === "message_arrival"/);
  assert.match(app, /ARRIVED_MESSAGE_VISIBLE_MS = 60_000/);
  assert.match(app, /function showMessageArrival\(event\)/);
  assert.match(app, /!\["agent", "discord", "reminder"\]\.includes\(event\.sourceKind\)/);
  assert.match(app, /setTimeout\(\(\) => hideMessageArrival\(event\.agent\), ARRIVED_MESSAGE_VISIBLE_MS\)/);
  assert.match(app, /function showArrivedMessageDialog\(arrival\)/);
  assert.match(app, /<i class="fa-solid fa-envelope arrival-icon" aria-hidden="true"><\/i>/);
  assert.doesNotMatch(app, /arrival-dot/);
  assert.match(app, /elements\.arrivedDialogBody\.replaceChildren\(renderMarkdownBody\(document, arrival\.body \|\| ""\)\)/);
  assert.match(app, /const label = arrival\.subject \|\| arrival\.preview \|\| "New message";/);
  assert.match(app, /truncateText\(label, 80\)/);
  assert.match(app, /elements\.arrivedDialog\.showModal\(\)/);
  assert.match(styles, /\.arrival-indicator \{[\s\S]*rgba\(255,209,102,0\.96\)/);
  assert.match(styles, /\.arrival-icon \{[\s\S]*font-size: 11px;/);
  assert.doesNotMatch(styles, /arrival-dot/);
  assert.match(styles, /\.message-dialog-body \{[\s\S]*word-break: break-word;/);
  assert.doesNotMatch(styles, /\.message-dialog-body \{[^}]*white-space: pre-wrap;/);
  assert.match(app, /sameSubagents\(current\.subagents, summaries\)\) return;/);
  assert.match(app, /className = `agent-row\$\{peer === state\.selectedPeer \? " active" : ""\}\$\{active \? " working" : ""\}\$\{compacting \? " compacting" : ""\}\$\{subagentCount > 0 \? " has-subagents" : ""\}\$\{arrival \? " has-arrival" : ""\}\$\{expanded \? " expanded" : ""\}`/);
  assert.match(app, /function compactingBadgeMarkup/);
  assert.match(app, /working-indicator/);
  assert.match(app, /aria-label="Agent is working"/);
  assert.match(app, /class="subagent-indicator\$\{canExpandSubagents/);
  assert.match(app, /aria-label="\$\{subagentCount\} running subagent\$\{subagentCount === 1 \? "" : "s"\}"/);
  assert.match(app, /compacting \? compactingBadgeMarkup\(\) : subagentBadgeMarkup\(peer, subagentCount, subagents, expanded, "row"\)/);
  assert.match(app, /arrivalIndicatorMarkup\(arrival\)/);
  assert.match(app, /expandedSubagentMarkup\(countLabel, subagents\)/);
  assert.match(app, /join\("\\n\\n"\)/);
  assert.match(styles, /\.subagent-indicator \{[\s\S]*rgba\(38,106,255,0\.26\)/);
  assert.match(styles, /\.compacting-indicator \{[\s\S]*rgba\(126,34,206,0\.28\)/);
  assert.match(styles, /@keyframes subagent-pulse/);
  assert.match(styles, /\.sidebar \{[\s\S]*overflow-x: hidden;/);
  assert.match(styles, /\.agents \{ display: grid; gap: 10px; min-width: 0; overflow-x: hidden; \}/);
  assert.match(styles, /\.agent-row \{[\s\S]*max-width: 100%;[\s\S]*min-width: 0;[\s\S]*overflow: hidden;[\s\S]*position: relative;[\s\S]*padding: 14px 44px 14px 14px;/);
  assert.match(styles, /\.working-indicator \{[\s\S]*position: absolute;[\s\S]*right: 14px;[\s\S]*transform: translateY\(-50%\);/);
  assert.match(styles, /\.agent-row-text \{ min-width: 0; overflow: hidden; flex: 1 1 auto; display: block; \}/);
  assert.match(styles, /\.agent-row strong \{ display: block; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; \}/);
  assert.doesNotMatch(app, /for \(const peer of Object\.keys\(state\.messages\)\) known\.add\(peer\)/);
  assert.doesNotMatch(app, /for \(const peer of state\.draftPeers\) known\.add\(peer\)/);
  assert.match(app, /`\$\{agent\.agent_id\} \$\{humanName\(agent\.agent_id\)\}`\.toLowerCase\(\)\.includes\(filter\)/);
  assert.doesNotMatch(app, /<span class="meta">\$\{escapeHtml\(peer\)\}<\/span>/);
  assert.doesNotMatch(app, /\$\{escapeHtml\(peer\)\} · \$\{active \? "Working" : "Available"\}/);
  assert.doesNotMatch(app, /working-indicator[^`]*Working/);
  assert.doesNotMatch(app, /\? "Available" : "History"/);
  assert.match(app, /state\.agentFilter = "";\n  elements\.agentFilter\.value = "";/);
  assert.match(app, /stateName === "connected" \? "" :/);
  assert.match(styles, /p:empty \{ display: none; \}/);
  assert.match(app, /import \{ HISTORY_RETENTION_MS, mergeMessages, messageKey, messageTime, pruneHistoryMessages, reconcileHistoryMessages \} from "\.\/history\.js"/);
  assert.doesNotMatch(app, /#main-connect/);
  assert.match(styles, /body \{[\s\S]*overflow: hidden;/);
  assert.match(styles, /\.shell \{[\s\S]*height: 100dvh;[\s\S]*overflow: hidden;/);
  assert.match(styles, /\.messages \{[\s\S]*overflow: auto;/);
  assert.match(styles, /\.model-picker \{[\s\S]*position: relative;/);
  assert.match(styles, /\.model-menu \{[\s\S]*position: absolute;[\s\S]*right: 0;/);
  assert.match(styles, /\.model-current i \{[\s\S]*font-size: 0\.7rem;/);
  assert.match(webState, /"agent_model_catalog"/);
  assert.match(webState, /function matchingPendingModelControl/);
  assert.match(webState, /function mergeAgentModelProjection/);
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
  assert.match(app, /canvas\.toBlob\(resolve, "image\/jpeg", HISTORY_IMAGE_QUALITY\)/);
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
  assert.match(server, /frame\.type === "agent_model_catalog"[\s\S]*type: "agent_model_catalog"[\s\S]*availableModels:/);
  assert.match(server, /const AGENT_REFRESH_INTERVAL_MS = 10_000;/);
  assert.match(server, /refreshTimer = setInterval\(refreshAgents, AGENT_REFRESH_INTERVAL_MS\)/);
  assert.match(server, /conversation_sync: "v1"/);
  assert.match(server, /type: "conversation_message"/);
  assert.doesNotMatch(server, /refreshTimer = setInterval\(refreshAgents, 60_000\)/);
});

async function startTestRouter({ clock = undefined, routerOptions = {} } = {}) {
  const directory = await mkdtemp(join(tmpdir(), "router-web-ui-"));
  const repository = openMessageRepository({ databasePath: join(directory, "router.sqlite"), ...(clock === undefined ? {} : { clock }) });
  const router = createAgentRouter({ repository, timeoutSweepIntervalMs: 60_000, ...routerOptions, ...(clock === undefined ? {} : { clock }) });
  const wsUrl = await router.listen();
  return {
    directory,
    repository,
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

async function connectBrowserWithLifecycleSnapshot(uiHttpUrl, routerHttpUrl, agentId, sockets) {
  const socket = new WebSocket(`${uiHttpUrl.replace("http://", "ws://")}/ui/events`);
  sockets.push(socket);
  await onceOpen(socket);
  const connected = waitForEvent(socket, (event) => event.type === "status" && event.state === "connected", 2_000);
  const snapshot = waitForEvent(socket, (event) => event.type === "subagent_state_snapshot", 2_000);
  socket.send(JSON.stringify({ type: "connect", routerUrl: routerHttpUrl, agentId }));
  assert.equal((await connected).agentId, agentId);
  return { socket, snapshot: await snapshot };
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

async function startElectronDomBrowser(url) {
  const electronPath = join(process.cwd(), "node_modules/.bin/electron");
  const temporaryDirectory = await mkdtemp(join(tmpdir(), "router-web-ui-electron-"));
  const scriptPath = join(temporaryDirectory, "browser.cjs");
  const script = `const { app, BrowserWindow } = require("electron");
const readline = require("node:readline");
let window;
app.whenReady().then(async () => {
  window = new BrowserWindow({
    show: false,
    webPreferences: {
      contextIsolation: true,
      sandbox: true,
      partition: "router-ui-test-" + Date.now() + "-" + Math.random(),
    },
  });
  window.webContents.session.webRequest.onBeforeRequest({ urls: ["https://cdnjs.cloudflare.com/*"] }, (_details, callback) => callback({ cancel: true }));
  await window.loadURL(process.env.MECHA_TEST_UI_URL);
  process.stdout.write("READY\\n");
  const input = readline.createInterface({ input: process.stdin });
  input.on("line", async (line) => {
    try {
      const request = JSON.parse(line);
      const result = await window.webContents.executeJavaScript(request.script);
      process.stdout.write(JSON.stringify({ ok: true, result }) + "\\n");
    } catch (error) {
      process.stdout.write(JSON.stringify({ ok: false, error: error.message }) + "\\n");
    }
  });
}).catch((error) => { process.stderr.write(error.stack + "\\n"); app.quit(); });`;
  await writeFile(scriptPath, script);
  const child = spawn(electronPath, ["--no-sandbox", scriptPath], {
    env: { ...process.env, MECHA_TEST_UI_URL: url },
    stdio: ["pipe", "pipe", "pipe"],
    detached: true,
  });
  let output = "";
  let settled = false;
  const lines = [];
  child.stdout.setEncoding("utf8");
  child.stdout.on("data", (chunk) => {
    output += chunk;
    let newline;
    while ((newline = output.indexOf("\\n")) !== -1) {
      lines.push(output.slice(0, newline));
      output = output.slice(newline + 1);
    }
  });
  const readLine = (timeoutMs = 5_000) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => { cleanup(); reject(new Error("timed out waiting for Electron")); }, timeoutMs);
    const check = () => {
      if (lines.length > 0) { const line = lines.shift(); cleanup(); resolve(line); }
      else if (child.exitCode !== null) { cleanup(); reject(new Error(`Electron exited with code ${child.exitCode}`)); }
      else setTimeout(check, 10);
    };
    const cleanup = () => clearTimeout(timer);
    check();
  });
  const cleanupTemporaryDirectory = () => rm(temporaryDirectory, { recursive: true, force: true });
  try {
    await readLine(15_000);
  } catch (error) {
    terminateElectron(child);
    await cleanupTemporaryDirectory();
    const unavailable = new Error(error.message);
    unavailable.code = "ELECTRON_UNAVAILABLE";
    throw unavailable;
  }
  settled = true;
  return {
    async evaluate(scriptToEvaluate) {
      child.stdin.write(`${JSON.stringify({ script: scriptToEvaluate })}\\n`);
      const response = JSON.parse(await readLine());
      if (!response.ok) throw new Error(response.error);
      return response.result;
    },
    async close() {
      if (!settled) return;
      settled = false;
      child.stdin.end();
      await new Promise((resolve) => {
        const timer = setTimeout(() => { terminateElectron(child); resolve(); }, 1_000);
        child.once("exit", () => { clearTimeout(timer); resolve(); });
      });
      await cleanupTemporaryDirectory();
    },
  };
}

function terminateElectron(child) {
  try { process.kill(-child.pid); } catch { child.kill(); }
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

function expectNoEvent(socket, predicate, timeoutMs = 100) {
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => cleanup(resolve), timeoutMs);
    const onMessage = (payload) => {
      const event = JSON.parse(payload.toString("utf8"));
      if (predicate(event)) cleanup(reject, new Error(`received unexpected ${event.type} event`));
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

function tinyJpegDataUrl() {
  return "data:image/jpeg;base64,/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAP//////////////////////////////////////////////////////////////////////////////////////2wBDAf//////////////////////////////////////////////////////////////////////////////////////wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAX/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIQAxAAAAH/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oACAEBAAEFAqf/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oACAEDAQE/ASP/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oACAECAQE/ASP/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oACAEBAAY/Aqf/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oACAEBAAE/IV//2gAMAwEAAgADAAAAEP/EFBQRAQAAAAAAAAAAAAAAAAAAABD/2gAIAQMBAT8QH//EFBQRAQAAAAAAAAAAAAAAAAAAABD/2gAIAQIBAT8QH//EFBABAQAAAAAAAAAAAAAAAAAAABD/2gAIAQEAAT8QH//Z";
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
