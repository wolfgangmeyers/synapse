#!/usr/bin/env node

import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { extname, join, normalize, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { randomUUID } from "node:crypto";

import WebSocket, { WebSocketServer } from "ws";

const DEFAULT_HOST = "127.0.0.1";
const DEFAULT_PORT = 8787;
const DEFAULT_ROUTER = "http://127.0.0.1:8765";
const AGENT_ID = /^(?!.*@)[A-Za-z0-9][A-Za-z0-9._-]*$/;
const CONTENT_TYPES = new Map([
  [".html", "text/html; charset=utf-8"],
  [".css", "text/css; charset=utf-8"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".svg", "image/svg+xml; charset=utf-8"],
  [".png", "image/png"],
]);

const root = resolve(fileURLToPath(new URL("../web/router-ui", import.meta.url)));

export function parseArguments(argv) {
  const options = { host: DEFAULT_HOST, port: DEFAULT_PORT, router: DEFAULT_ROUTER };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (["--host", "--port", "--router"].includes(argument)) {
      const value = argv[index + 1];
      if (value === undefined || value.startsWith("--")) throw new Error(`${argument} requires a value`);
      options[argument.slice(2)] = value;
      index += 1;
      continue;
    }
    if (argument === "--help") return { help: true };
    throw new Error(`unknown argument: ${argument}`);
  }
  if (!["127.0.0.1", "::1"].includes(options.host)) throw new Error("--host must be 127.0.0.1 or ::1");
  options.port = Number(options.port);
  if (!Number.isSafeInteger(options.port) || options.port < 1 || options.port > 65535) throw new Error("--port must be 1 through 65535");
  normalizeRouterUrl(options.router);
  return options;
}

export async function startRouterWebUi({ host = DEFAULT_HOST, port = 0, router = DEFAULT_ROUTER, output = console.log } = {}) {
  router = normalizeRouterUrl(router);
  const clients = new Set();
  const server = createServer((request, response) => {
    void handleRequest(request, response, { router }).catch(() => sendJson(response, 500, { error: "internal_error" }));
  });
  const browserWss = new WebSocketServer({
    server,
    path: "/ui/events",
    perMessageDeflate: false,
    verifyClient: ({ origin, req }, done) => {
      if (origin === undefined) {
        done(true);
        return;
      }
      const hostHeader = req.headers.host;
      if (origin === `http://${hostHeader}` || origin === `https://${hostHeader}`) {
        done(true);
        return;
      }
      done(false, 403, "browser origin rejected");
    },
  });
  browserWss.on("connection", (browser) => {
    const client = createBrowserClient(browser, { defaultRouter: router });
    clients.add(client);
    browser.on("close", () => {
      clients.delete(client);
      client.close();
    });
  });
  await new Promise((resolveListen, rejectListen) => {
    server.once("error", rejectListen);
    server.listen(port, host, () => {
      server.off("error", rejectListen);
      resolveListen();
    });
  });
  const address = server.address();
  const httpUrl = `http://${address.address === "::1" ? "[::1]" : address.address}:${address.port}`;
  output(`MECHA_ROUTER_WEB_UI_READY http=${httpUrl} router=${normalizeRouterUrl(router)}`);
  return {
    httpUrl,
    async close() {
      for (const client of clients) client.close();
      browserWss.close();
      await new Promise((resolveClose, rejectClose) => server.close((error) => error ? rejectClose(error) : resolveClose()));
    },
  };
}

async function handleRequest(request, response, { router }) {
  const url = new URL(request.url ?? "/", "http://ui.local");
  if (request.method === "GET" && url.pathname === "/ui/config") {
    sendJson(response, 200, { defaultRouter: normalizeRouterUrl(router) });
    return;
  }
  if (request.method !== "GET" && request.method !== "HEAD") {
    sendJson(response, 405, { error: "method_not_allowed" });
    return;
  }
  const pathname = url.pathname === "/" ? "/index.html" : url.pathname;
  const relative = normalize(decodeURIComponent(pathname)).replace(/^\/+/, "");
  const filePath = resolve(join(root, relative));
  if (!filePath.startsWith(`${root}/`) && filePath !== root) {
    sendJson(response, 404, { error: "not_found" });
    return;
  }
  try {
    const body = await readFile(filePath);
    response.writeHead(200, {
      "cache-control": "no-store",
      "content-type": CONTENT_TYPES.get(extname(filePath)) ?? "application/octet-stream",
    });
    if (request.method === "GET") response.end(body);
    else response.end();
  } catch {
    sendJson(response, 404, { error: "not_found" });
  }
}

function createBrowserClient(browser, { defaultRouter }) {
  let routerSocket;
  let reconnectTimer;
  let refreshTimer;
  let renewTimer;
  let connectionGeneration = 0;
  const pendingDeliveries = new Map();
  let configured = {
    routerUrl: normalizeRouterUrl(defaultRouter),
    agentId: `web-${randomUUID().slice(0, 8)}`,
    instanceId: randomUUID(),
    leaseId: "",
  };

  const sendBrowser = (payload) => {
    if (browser.readyState !== WebSocket.OPEN) return false;
    browser.send(JSON.stringify(payload));
    return true;
  };
  const setStatus = (state, detail) => sendBrowser({ type: "status", state, detail, agentId: configured.agentId, routerUrl: configured.routerUrl });
  const refreshAgents = async () => {
    const routerUrl = configured.routerUrl;
    const agentId = configured.agentId;
    try {
      const response = await fetch(new URL("/v1/agents", routerUrl));
      const body = await response.json();
      if (!response.ok) throw new Error(body.error?.code ?? `HTTP ${response.status}`);
      sendBrowser({ type: "agents", agents: body.agents ?? [], generatedAt: body.generated_at, agentId, routerUrl });
    } catch (error) {
      sendBrowser({ type: "agents_error", message: error.message, agentId, routerUrl });
    }
  };

  const connectRouter = () => {
    clearTimeout(reconnectTimer);
    clearInterval(refreshTimer);
    clearInterval(renewTimer);
    configured = { ...configured, instanceId: randomUUID(), leaseId: "" };
    const generation = ++connectionGeneration;
    routerSocket?.close(1000, "reconnecting");
    const wsUrl = routerWsUrl(configured.routerUrl);
    setStatus("connecting", "Connecting to your agents…");
    routerSocket = new WebSocket(wsUrl, { origin: undefined, perMessageDeflate: false });
    routerSocket.on("open", () => {
      routerSocket.send(JSON.stringify({
        version: "v1",
        frame_id: `register-${randomUUID()}`,
        type: "register",
        agent_id: configured.agentId,
        instance_id: configured.instanceId,
        agent_role: "user_interaction",
        client_type: "web",
        ack_confirmation: "correlated_v1",
      }));
    });
    routerSocket.on("message", (payload) => {
      if (generation !== connectionGeneration) return;
      let frame;
      try {
        frame = JSON.parse(payload.toString("utf8"));
      } catch {
        setStatus("error", "Router sent an invalid response");
        return;
      }
      if (frame.type === "registered") {
        configured.leaseId = frame.lease_id;
        setStatus("connected", `Connected as ${configured.agentId}`);
        clearInterval(renewTimer);
        renewTimer = setInterval(() => renewLease(), 10_000);
        refreshAgents();
        clearInterval(refreshTimer);
        refreshTimer = setInterval(refreshAgents, 30_000);
        return;
      }
      if (frame.type === "lease_rejected" || frame.type === "error") {
        setStatus("error", frame.code ?? "Router rejected the connection");
        return;
      }
      if (frame.type === "message") {
        const deliveryToken = randomUUID();
        const timer = setTimeout(() => {
          pendingDeliveries.delete(deliveryToken);
          routerSocket?.close(1011, "browser delivery timed out");
        }, 20_000);
        pendingDeliveries.set(deliveryToken, { frame, timer });
        if (!sendBrowser({ type: "message", deliveryToken, message: frame.message, attemptNumber: frame.attempt_number, messageIncarnationId: frame.message_incarnation_id, agentId: configured.agentId, routerUrl: configured.routerUrl })) {
          clearTimeout(timer);
          pendingDeliveries.delete(deliveryToken);
          routerSocket?.close(1011, "browser unavailable");
        }
        return;
      }
      if (frame.type === "delivery_acknowledged") {
        sendBrowser({ type: "delivery_acknowledged", messageId: frame.message_id, messageIncarnationId: frame.message_incarnation_id });
      }
    });
    routerSocket.on("close", () => {
      if (generation !== connectionGeneration) return;
      setStatus("reconnecting", "Router connection closed; retrying…");
      reconnectTimer = setTimeout(connectRouter, 1_000);
    });
    routerSocket.on("error", () => {
      if (generation !== connectionGeneration) return;
      setStatus("reconnecting", "Router unavailable; retrying…");
    });
  };

  const renewLease = () => {
    if (routerSocket?.readyState !== WebSocket.OPEN || configured.leaseId === "") return;
    routerSocket.send(JSON.stringify({
      version: "v1",
      frame_id: `renew-${randomUUID()}`,
      type: "lease_renew",
      agent_id: configured.agentId,
      instance_id: configured.instanceId,
      lease_id: configured.leaseId,
    }));
  };

  const sendReceipt = (type, frame) => {
    if (routerSocket?.readyState !== WebSocket.OPEN || configured.leaseId === "") return;
    routerSocket.send(JSON.stringify({
      version: "v1",
      frame_id: `${type}-${randomUUID()}`,
      type,
      agent_id: configured.agentId,
      instance_id: configured.instanceId,
      lease_id: configured.leaseId,
      message_id: frame.message.id,
      attempt_number: frame.attempt_number,
    }));
  };

  browser.on("message", (payload) => {
    void (async () => {
      let command;
      try {
        command = JSON.parse(payload.toString("utf8"));
        if (command.type === "connect") {
          configured.routerUrl = normalizeRouterUrl(command.routerUrl ?? configured.routerUrl);
          configured.agentId = normalizeAgentId(command.agentId ?? configured.agentId);
          connectRouter();
          return;
        }
        if (command.type === "refresh_agents") {
          await refreshAgents();
          return;
        }
        if (command.type === "message_received") {
          const pending = pendingDeliveries.get(command.deliveryToken);
          if (pending !== undefined) {
            pendingDeliveries.delete(command.deliveryToken);
            clearTimeout(pending.timer);
            sendReceipt("injection_requested", pending.frame);
            sendReceipt("ack", pending.frame);
          }
          return;
        }
        if (command.type === "send_message") {
          await sendMessage(command, configured, sendBrowser, refreshAgents);
        }
      } catch (error) {
        sendBrowser({ type: "command_error", message: error.message });
      }
    })();
  });
  sendBrowser({ type: "ready", defaultRouter: configured.routerUrl, suggestedAgentId: configured.agentId });

  return {
    close() {
      clearTimeout(reconnectTimer);
      clearInterval(refreshTimer);
      clearInterval(renewTimer);
      connectionGeneration++;
      for (const pending of pendingDeliveries.values()) clearTimeout(pending.timer);
      pendingDeliveries.clear();
      routerSocket?.close(1000, "browser closed");
    },
  };
}

async function sendMessage(command, configured, sendBrowser, refreshAgents) {
  const routerUrl = configured.routerUrl;
  const agentId = configured.agentId;
  const to = normalizeAgentId(command.to ?? "");
  const body = String(command.body ?? "");
  if (body.trim() === "") throw new Error("Message cannot be blank");
  const envelope = {
    id: randomUUID(),
    from: agentId,
    to,
    body,
    created_at: new Date().toISOString(),
  };
  const response = await fetch(new URL("/v1/messages", routerUrl), {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(envelope),
  });
  const responseBody = await response.json().catch(() => ({}));
  if (!response.ok) {
    sendBrowser({ type: "send_result", ok: false, error: responseBody.error?.message ?? `Router rejected the message (${response.status})`, localId: command.localId, agentId, routerUrl });
    return;
  }
  const accepted = responseBody.message ?? envelope;
  const displayMessage = { ...accepted, from: agentId, to, body, created_at: accepted.created_at ?? envelope.created_at };
  sendBrowser({ type: "send_result", ok: true, message: displayMessage, localId: command.localId, agentId, routerUrl });
  await refreshAgents();
}

function normalizeRouterUrl(value) {
  const url = new URL(String(value));
  if (!["http:", "https:"].includes(url.protocol)) throw new Error("router URL must start with http:// or https://");
  if (url.hostname === "") throw new Error("router URL must include a host");
  if (url.username !== "" || url.password !== "") throw new Error("router URL must not include credentials");
  url.pathname = "";
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/$/, "");
}

function routerWsUrl(routerUrl) {
  const url = new URL("/v1/agents", routerUrl);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  return url.toString();
}

function normalizeAgentId(value) {
  const agentId = String(value).trim();
  if (!AGENT_ID.test(agentId) || agentId.length > 128) throw new Error("Agent IDs use letters, numbers, ., _, and -");
  return agentId;
}

function sendJson(response, statusCode, payload) {
  response.writeHead(statusCode, { "cache-control": "no-store", "content-type": "application/json; charset=utf-8" });
  response.end(JSON.stringify(payload));
}

function usage() {
  return "Usage: node tools/router_web_ui.mjs [--host 127.0.0.1] [--port 8787] [--router http://127.0.0.1:8765]";
}

async function main() {
  let options;
  try {
    options = parseArguments(process.argv.slice(2));
  } catch (error) {
    console.error(`router_web_ui.mjs: ${error.message}`);
    console.error(usage());
    process.exitCode = 2;
    return;
  }
  if (options.help) {
    console.log(usage());
    return;
  }
  const running = await startRouterWebUi(options);
  let closing = false;
  const close = async (signal) => {
    if (closing) return;
    closing = true;
    console.log(`MECHA_ROUTER_WEB_UI_STOP signal=${signal}`);
    await running.close();
  };
  process.once("SIGINT", () => { void close("SIGINT"); });
  process.once("SIGTERM", () => { void close("SIGTERM"); });
}

if (process.argv[1] !== undefined && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
