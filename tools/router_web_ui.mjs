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
const DEFAULT_AGENT_ID = "wolfgang-web";
const LOOPBACK_HOSTNAMES = new Set(["127.0.0.1", "::1", "localhost"]);
const HOSTNAME = /^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)(?:\.(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?))*$/i;
const AGENT_REFRESH_INTERVAL_MS = 10_000;
// The router owns a 30-second pending-control TTL and sweeps once a second.
// Keep the browser fallback beyond that authoritative rejection window.
const MODEL_CONTROL_TIMEOUT_MS = 35_000;
const GOAL_CONTROL_TIMEOUT_MS = 35_000;
const CAPABILITY_DISCOVERY_TIMEOUT_MS = 1_000;
const MAX_PENDING_MODEL_CONTROLS = 100;
const AGENT_ID = /^(?!.*@)[A-Za-z0-9][A-Za-z0-9._-]*$/;
const CONTROL_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;
const CONTENT_TYPES = new Map([
  [".html", "text/html; charset=utf-8"],
  [".css", "text/css; charset=utf-8"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".svg", "image/svg+xml; charset=utf-8"],
  [".png", "image/png"],
]);

const root = resolve(fileURLToPath(new URL("../web/router-ui", import.meta.url)));
const markdownItBrowserPath = resolve(fileURLToPath(new URL("../web/router-ui/vendor/markdown-it.js", import.meta.url)));

export function parseArguments(argv) {
  const options = {
    host: DEFAULT_HOST,
    port: DEFAULT_PORT,
    router: DEFAULT_ROUTER,
    defaultAgentId: DEFAULT_AGENT_ID,
    allowedHost: process.env.MECHA_WEB_UI_ALLOWED_HOST,
  };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (["--host", "--port", "--router", "--default-agent-id"].includes(argument)) {
      const value = argv[index + 1];
      if (value === undefined || value.startsWith("--")) throw new Error(`${argument} requires a value`);
      options[argument.slice(2).replace(/-([a-z])/g, (_match, letter) => letter.toUpperCase())] = value;
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
  options.defaultAgentId = normalizeAgentId(options.defaultAgentId);
  return options;
}

export async function startRouterWebUi({
  host = DEFAULT_HOST,
  port = 0,
  router = DEFAULT_ROUTER,
  defaultAgentId = DEFAULT_AGENT_ID,
  allowedHost = process.env.MECHA_WEB_UI_ALLOWED_HOST,
  modelControlTimeoutMs,
  goalControlTimeoutMs,
  maxPendingModelControls = MAX_PENDING_MODEL_CONTROLS,
  capabilitiesFetchImpl = fetch,
  capabilityDiscoveryTimeoutMs = CAPABILITY_DISCOVERY_TIMEOUT_MS,
  output = console.log,
} = {}) {
  router = normalizeRouterUrl(router);
  defaultAgentId = normalizeAgentId(defaultAgentId);
  allowedHost = normalizeAllowedHost(allowedHost);
  if (modelControlTimeoutMs !== undefined
    && (!Number.isSafeInteger(modelControlTimeoutMs) || modelControlTimeoutMs < 1)) {
    throw new Error("modelControlTimeoutMs must be a positive integer");
  }
  if (goalControlTimeoutMs !== undefined
    && (!Number.isSafeInteger(goalControlTimeoutMs) || goalControlTimeoutMs < 1)) {
    throw new Error("goalControlTimeoutMs must be a positive integer");
  }
  if (!Number.isSafeInteger(maxPendingModelControls) || maxPendingModelControls < 1) throw new Error("maxPendingModelControls must be a positive integer");
  if (typeof capabilitiesFetchImpl !== "function") throw new Error("capabilitiesFetchImpl must be a function");
  if (!Number.isSafeInteger(capabilityDiscoveryTimeoutMs) || capabilityDiscoveryTimeoutMs < 1) throw new Error("capabilityDiscoveryTimeoutMs must be a positive integer");
  const clients = new Set();
  const server = createServer((request, response) => {
    if (!browserAuthorityIsAllowed(request.headers.host, allowedHost)) {
      sendJson(response, 403, { error: "request_host_rejected" });
      return;
    }
    void handleRequest(request, response, { router, defaultAgentId }).catch(() => sendJson(response, 500, { error: "internal_error" }));
  });
  const browserWss = new WebSocketServer({
    server,
    path: "/ui/events",
    perMessageDeflate: false,
    verifyClient: ({ origin, req }, done) => {
      if (!browserAuthorityIsAllowed(req.headers.host, allowedHost)) {
        done(false, 403, "browser host rejected");
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
    const client = createBrowserClient(browser, {
      defaultRouter: router,
      modelControlTimeoutMs,
      goalControlTimeoutMs,
      maxPendingModelControls,
      capabilitiesFetchImpl,
      capabilityDiscoveryTimeoutMs,
    });
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

function normalizeAllowedHost(value) {
  if (value === undefined || value === "") return undefined;
  const host = String(value).trim().toLowerCase();
  if (!HOSTNAME.test(host)) throw new Error("allowedHost must be a hostname");
  return host;
}

function browserAuthorityIsAllowed(authority, allowedHost) {
  if (typeof authority !== "string" || authority.length === 0) return false;
  try {
    const hostname = new URL(`http://${authority}`).hostname.replace(/^\[|\]$/g, "");
    return LOOPBACK_HOSTNAMES.has(hostname.toLowerCase())
      || (allowedHost !== undefined && hostname.toLowerCase() === allowedHost);
  } catch {
    return false;
  }
}

async function handleRequest(request, response, { router, defaultAgentId }) {
  const url = new URL(request.url ?? "/", "http://ui.local");
  if (request.method === "GET" && url.pathname === "/ui/config") {
    sendJson(response, 200, { defaultRouter: normalizeRouterUrl(router), defaultAgentId });
    return;
  }
  if ((request.method === "GET" || request.method === "HEAD") && url.pathname === "/vendor/markdown-it.js") {
    const body = await readFile(markdownItBrowserPath);
    response.writeHead(200, { "cache-control": "no-store", "content-type": "text/javascript; charset=utf-8" });
    if (request.method === "GET") response.end(body);
    else response.end();
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

function createBrowserClient(browser, {
  defaultRouter,
  modelControlTimeoutMs,
  goalControlTimeoutMs,
  maxPendingModelControls,
  capabilitiesFetchImpl,
  capabilityDiscoveryTimeoutMs,
}) {
  let routerSocket;
  let reconnectTimer;
  let refreshTimer;
  let refreshGeneration = 0;
  let renewTimer;
  let connectionGeneration = 0;
  let lifecycleEnabled = false;
  let lifecycleVersion = 0;
  let goalControlBlockingSupported = false;
  let includeModelsInNextRefresh = true;
  let effectiveModelControlTimeoutMs = modelControlTimeoutMs ?? MODEL_CONTROL_TIMEOUT_MS;
  const effectiveGoalControlTimeoutMs = goalControlTimeoutMs ?? GOAL_CONTROL_TIMEOUT_MS;
  const pendingDeliveries = new Map();
  const pendingControlsByControlId = new Map();
  const pendingControlsByRequestId = new Map();
  const pendingGoalControlsByControlId = new Map();
  const pendingGoalControlsByRequestId = new Map();
  const pendingReminderLockControlsByControlId = new Map();
  const pendingReminderLockControlsByRequestId = new Map();
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
  const settlePendingControl = (controlId, result) => {
    const pending = pendingControlsByControlId.get(controlId);
    if (pending === undefined) return false;
    pendingControlsByControlId.delete(controlId);
    pendingControlsByRequestId.delete(pending.requestId);
    clearTimeout(pending.timer);
    const accepted = result.accepted === true;
    sendBrowser({
      type: "model_control_result",
      controlId,
      targetAgentId: pending.targetAgentId,
      accepted,
      ...(!accepted && result.errorCode === undefined ? { errorCode: "model_control_failed" } : {}),
      ...(!accepted && result.errorCode !== undefined ? { errorCode: result.errorCode } : {}),
      ...(result.instanceId === undefined ? {} : { instanceId: result.instanceId }),
      ...(result.currentModel === undefined ? {} : { currentModel: result.currentModel }),
      agentId: configured.agentId,
      routerUrl: configured.routerUrl,
    });
    return true;
  };
  const settlePendingGoalControl = (controlId, result) => {
    const pending = pendingGoalControlsByControlId.get(controlId);
    if (pending === undefined) return false;
    pendingGoalControlsByControlId.delete(controlId);
    pendingGoalControlsByRequestId.delete(pending.requestId);
    clearTimeout(pending.timer);
    sendBrowser({
      type: "goal_control_result",
      controlId,
      targetAgentId: pending.targetAgentId,
      targetInstanceId: pending.targetInstanceId,
      accepted: result.accepted === true,
      ...(result.errorCode === undefined ? {} : { errorCode: result.errorCode }),
      ...(result.message === undefined ? {} : { message: result.message }),
      ...(result.revision === undefined ? {} : { revision: result.revision }),
      ...(result.goalState === undefined ? {} : { goalState: result.goalState }),
      agentId: configured.agentId,
      routerUrl: configured.routerUrl,
    });
    return true;
  };
  const settlePendingReminderLockControl = (controlId, result) => {
    const pending = pendingReminderLockControlsByControlId.get(controlId);
    if (pending === undefined) return false;
    pendingReminderLockControlsByControlId.delete(controlId);
    pendingReminderLockControlsByRequestId.delete(pending.requestId);
    clearTimeout(pending.timer);
    sendBrowser({
      type: "reminder_lock_control_result",
      controlId,
      targetAgentId: pending.targetAgentId,
      targetInstanceId: pending.targetInstanceId,
      ...(result.leaseId === undefined ? {} : { targetLeaseId: result.leaseId }),
      reminderId: pending.reminderId,
      action: pending.action,
      accepted: result.accepted === true,
      ...(result.errorCode === undefined ? {} : { errorCode: result.errorCode }),
      agentId: configured.agentId,
      routerUrl: configured.routerUrl,
    });
    return true;
  };
  const rejectAllPendingControls = (errorCode) => {
    for (const controlId of [...pendingControlsByControlId.keys()]) {
      settlePendingControl(controlId, { accepted: false, errorCode });
    }
    for (const controlId of [...pendingGoalControlsByControlId.keys()]) {
      settlePendingGoalControl(controlId, { accepted: false, errorCode });
    }
    for (const controlId of [...pendingReminderLockControlsByControlId.keys()]) {
      settlePendingReminderLockControl(controlId, { accepted: false, errorCode });
    }
  };
  const trackPendingControl = ({ controlId, requestId, targetAgentId }) => {
    if (pendingControlsByControlId.has(controlId)) throw new Error("Control ID is already pending");
    while (pendingControlsByControlId.size >= maxPendingModelControls) {
      const oldestControlId = pendingControlsByControlId.keys().next().value;
      settlePendingControl(oldestControlId, { accepted: false, errorCode: "too_many_pending_controls" });
    }
    const timer = setTimeout(() => {
      settlePendingControl(controlId, { accepted: false, errorCode: "control_timeout" });
    }, effectiveModelControlTimeoutMs);
    const pending = { controlId, requestId, targetAgentId, timer };
    pendingControlsByControlId.set(controlId, pending);
    pendingControlsByRequestId.set(requestId, pending);
  };
  const trackPendingGoalControl = ({ controlId, requestId, targetAgentId, targetInstanceId }) => {
    if (pendingGoalControlsByControlId.has(controlId)) throw new Error("Goal control ID is already pending");
    while (pendingGoalControlsByControlId.size >= maxPendingModelControls) {
      const oldestControlId = pendingGoalControlsByControlId.keys().next().value;
      settlePendingGoalControl(oldestControlId, { accepted: false, errorCode: "too_many_pending_controls" });
    }
    const timer = setTimeout(() => {
      settlePendingGoalControl(controlId, { accepted: false, errorCode: "control_timeout", message: "The goal change timed out." });
    }, effectiveGoalControlTimeoutMs);
    const pending = { controlId, requestId, targetAgentId, targetInstanceId, timer };
    pendingGoalControlsByControlId.set(controlId, pending);
    pendingGoalControlsByRequestId.set(requestId, pending);
  };
  const trackPendingReminderLockControl = ({ controlId, requestId, targetAgentId, targetInstanceId, reminderId, action }) => {
    if (pendingReminderLockControlsByControlId.has(controlId)) throw new Error("Reminder lock control ID is already pending");
    while (pendingReminderLockControlsByControlId.size >= maxPendingModelControls) {
      const oldestControlId = pendingReminderLockControlsByControlId.keys().next().value;
      settlePendingReminderLockControl(oldestControlId, { accepted: false, errorCode: "too_many_pending_controls" });
    }
    const timer = setTimeout(() => {
      settlePendingReminderLockControl(controlId, { accepted: false, errorCode: "control_timeout" });
    }, effectiveModelControlTimeoutMs);
    const pending = { controlId, requestId, targetAgentId, targetInstanceId, reminderId, action, timer };
    pendingReminderLockControlsByControlId.set(controlId, pending);
    pendingReminderLockControlsByRequestId.set(requestId, pending);
  };

  const refreshAgents = async () => {
    const generation = ++refreshGeneration;
    const routerUrl = configured.routerUrl;
    const agentId = configured.agentId;
    const load = async (path) => {
      const response = await fetch(new URL(path, routerUrl));
      const body = await response.json();
      if (!response.ok) throw new Error(body.error?.code ?? `HTTP ${response.status}`);
      return body;
    };
    const [agentsResult, remindersResult] = await Promise.allSettled([
      load(includeModelsInNextRefresh ? "/v1/agents" : "/v1/agents?models=omit"),
      load("/v1/reminders"),
    ]);
    if (generation !== refreshGeneration) return;
    if (agentsResult.status === "fulfilled") {
      const agentsBody = agentsResult.value;
      includeModelsInNextRefresh = false;
      sendBrowser({ type: "agents", agents: agentsBody.agents ?? [], generatedAt: agentsBody.generated_at, activitySnapshotVersion: agentsBody.activity_snapshot_version, agentId, routerUrl });
    } else {
      sendBrowser({ type: "agents_error", message: agentsResult.reason.message, agentId, routerUrl });
    }
    if (remindersResult.status === "fulfilled") {
      const remindersBody = remindersResult.value;
      sendBrowser({ type: "reminders", reminders: remindersBody.reminders ?? [], agentId, routerUrl });
    } else {
      sendBrowser({ type: "reminders_error", message: remindersResult.reason.message, agentId, routerUrl });
    }
  };

  const connectRouter = () => {
    clearTimeout(reconnectTimer);
    clearInterval(refreshTimer);
    clearInterval(renewTimer);
    rejectAllPendingControls("router_reconnecting");
    includeModelsInNextRefresh = true;
    effectiveModelControlTimeoutMs = modelControlTimeoutMs ?? MODEL_CONTROL_TIMEOUT_MS;
    configured = { ...configured, instanceId: randomUUID(), leaseId: "" };
    lifecycleEnabled = false;
    goalControlBlockingSupported = false;
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
        conversation_sync: "v1",
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
        clearInterval(renewTimer);
        renewTimer = setInterval(() => renewLease(), 10_000);
        void (async () => {
          let discoveredModelControlTimeoutMs = MODEL_CONTROL_TIMEOUT_MS;
          let discoveredGoalControlBlockingSupported = false;
          try {
            const response = await capabilitiesFetchImpl(new URL("/v1/capabilities", configured.routerUrl), {
              signal: AbortSignal.timeout(capabilityDiscoveryTimeoutMs),
            });
            const capabilities = await response.json();
            const routerTtlMs = capabilities?.model_control_limits?.pending_ttl_ms;
            if (response.ok && Number.isSafeInteger(routerTtlMs) && routerTtlMs > 0) {
              discoveredModelControlTimeoutMs = routerTtlMs + 2_000;
            }
            discoveredGoalControlBlockingSupported = response.ok && capabilities?.goal_control_blocking_v1 === true;
          } catch {
            discoveredModelControlTimeoutMs = MODEL_CONTROL_TIMEOUT_MS;
            discoveredGoalControlBlockingSupported = false;
          }
          if (generation !== connectionGeneration || configured.leaseId !== frame.lease_id) return;
          goalControlBlockingSupported = discoveredGoalControlBlockingSupported;
          effectiveModelControlTimeoutMs = modelControlTimeoutMs ?? discoveredModelControlTimeoutMs;
          setStatus("connected", `Connected as ${configured.agentId}`);
          refreshAgents();
          void enableLifecycleSync(generation);
          clearInterval(refreshTimer);
          refreshTimer = setInterval(refreshAgents, AGENT_REFRESH_INTERVAL_MS);
        })();
        return;
      }
      if (frame.type === "error") {
        const goalPending = pendingGoalControlsByRequestId.get(frame.request_id);
        if (goalPending !== undefined) {
          settlePendingGoalControl(goalPending.controlId, {
            accepted: false,
            errorCode: frame.code ?? "router_error",
            message: frame.message,
          });
          return;
        }
        const pendingReminderLock = pendingReminderLockControlsByRequestId.get(frame.request_id);
        if (pendingReminderLock !== undefined) {
          settlePendingReminderLockControl(pendingReminderLock.controlId, { accepted: false, errorCode: frame.code ?? "router_error" });
          return;
        }
        const pending = pendingControlsByRequestId.get(frame.request_id);
        if (pending !== undefined) {
          settlePendingControl(pending.controlId, { accepted: false, errorCode: frame.code ?? "router_error" });
          return;
        }
        setStatus("error", frame.code ?? "Router rejected the connection");
        return;
      }
      if (frame.type === "lease_rejected") {
        setStatus("error", frame.code ?? "Router rejected the connection");
        return;
      }
      if (frame.type === "conversation_message") {
        sendBrowser({
          type: "conversation_message",
          message: messageWithAbsoluteAttachments(frame.message, configured.routerUrl),
          agentId: configured.agentId,
          routerUrl: configured.routerUrl,
        });
        return;
      }
      if (frame.type === "agent_goal_state") {
        sendBrowser({
          type: "agent_goal_state",
          agent: frame.agent_id,
          instanceId: frame.instance_id,
          goalState: frame.goal_state,
          generatedAt: frame.generated_at,
          agentId: configured.agentId,
          routerUrl: configured.routerUrl,
        });
        return;
      }
      if (frame.type === "agent_activity") {
        refreshGeneration += 1;
        void refreshAgents();
        sendBrowser({
          type: "agent_activity",
          agent: frame.agent_id,
          ...(frame.instance_id === undefined ? {} : { instanceId: frame.instance_id }),
          activityState: frame.activity_state,
          subagentCount: frame.subagent_count ?? 0,
          subagents: Array.isArray(frame.subagents) ? frame.subagents : [],
          ...(frame.goal === undefined ? {} : { goal: frame.goal }),
          ...(frame.current_model === undefined ? {} : { currentModel: frame.current_model }),
          generatedAt: frame.generated_at,
          agentId: configured.agentId,
          routerUrl: configured.routerUrl,
        });
        return;
      }
      if (frame.type === "agent_model_catalog") {
        refreshGeneration += 1;
        void refreshAgents();
        sendBrowser({
          type: "agent_model_catalog",
          agent: frame.agent_id,
          ...(frame.instance_id === undefined ? {} : { instanceId: frame.instance_id }),
          ...(frame.current_model === undefined ? {} : { currentModel: frame.current_model }),
          availableModels: Array.isArray(frame.model_summaries) ? frame.model_summaries : [],
          totalCount: frame.total_count ?? frame.model_summaries?.length ?? 0,
          truncated: frame.truncated === true,
          generatedAt: frame.generated_at,
          agentId: configured.agentId,
          routerUrl: configured.routerUrl,
        });
        return;
      }
      if (frame.type === "reminder_lock_control_result") {
        const pending = pendingReminderLockControlsByControlId.get(frame.control_id);
        if (pending === undefined
          || pending.targetAgentId !== frame.agent_id
          || pending.targetInstanceId !== frame.instance_id
          || pending.reminderId !== frame.reminder_id
          || frame.source_agent_id !== configured.agentId) return;
        settlePendingReminderLockControl(frame.control_id, {
          accepted: frame.accepted,
          ...(frame.error_code === undefined ? {} : { errorCode: frame.error_code }),
          ...(frame.lease_id === undefined ? {} : { leaseId: frame.lease_id }),
        });
        return;
      }
      if (frame.type === "goal_control_result") {
        const pending = pendingGoalControlsByControlId.get(frame.control_id);
        if (pending === undefined
          || pending.targetAgentId !== frame.agent_id
          || pending.targetInstanceId !== frame.instance_id
          || frame.source_agent_id !== configured.agentId) return;
        settlePendingGoalControl(frame.control_id, {
          accepted: frame.accepted,
          ...(frame.error_code === undefined ? {} : { errorCode: frame.error_code }),
          ...(frame.goal_state === undefined ? {} : { goalState: frame.goal_state }),
          ...(frame.revision === undefined ? {} : { revision: frame.revision }),
        });
        return;
      }
      if (frame.type === "model_control_result") {
        refreshGeneration += 1;
        void refreshAgents();
        const pending = pendingControlsByControlId.get(frame.control_id);
        if (pending === undefined || pending.targetAgentId !== frame.agent_id) return;
        settlePendingControl(frame.control_id, {
          accepted: frame.accepted,
          ...(frame.error_code === undefined ? {} : { errorCode: frame.error_code }),
          ...(frame.current_model === undefined ? {} : { currentModel: frame.current_model }),
          instanceId: frame.instance_id,
        });
        return;
      }
      if (frame.type === "subagent_state_snapshot") {
        lifecycleVersion = Math.max(lifecycleVersion, frame.router_version ?? 0);
        sendBrowser({ type: "subagent_state_snapshot", routerVersion: frame.router_version ?? 0, agents: frame.agents ?? [], agentId: configured.agentId, routerUrl: configured.routerUrl });
        return;
      }
      if (frame.type === "subagent_state_update") {
        lifecycleVersion = Math.max(lifecycleVersion, frame.router_version ?? 0);
        sendBrowser({ type: "subagent_state_update", routerVersion: frame.router_version ?? 0, agent: frame.agent, agentId: configured.agentId, routerUrl: configured.routerUrl });
        return;
      }
      if (frame.type === "subagent_state_replay") {
        lifecycleVersion = Math.max(lifecycleVersion, frame.router_version ?? 0);
        sendBrowser({ type: "subagent_state_replay", routerVersion: frame.router_version ?? 0, updates: frame.updates ?? [], agentId: configured.agentId, routerUrl: configured.routerUrl });
        return;
      }
      if (frame.type === "message_arrival") {
        sendBrowser({
          type: "message_arrival",
          agent: frame.agent_id,
          from: frame.from,
          subject: frame.subject,
          sourceKind: frame.source?.kind,
          preview: frame.preview,
          body: frame.body,
          createdAt: frame.created_at,
          messageId: frame.message_id,
          agentId: configured.agentId,
          routerUrl: configured.routerUrl,
        });
        return;
      }
      if (frame.type === "message" && configured.leaseId !== "") {
        sendReceipt("injection_requested", frame);
        sendReceipt("ack", frame);
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
      rejectAllPendingControls("router_disconnected");
      setStatus("reconnecting", "Router connection closed; retrying…");
      reconnectTimer = setTimeout(connectRouter, 1_000);
    });
    routerSocket.on("error", () => {
      if (generation !== connectionGeneration) return;
      rejectAllPendingControls("router_unavailable");
      setStatus("reconnecting", "Router unavailable; retrying…");
    });
  };

  const enableLifecycleSync = async (generation) => {
    try {
      const response = await fetch(new URL("/v1/capabilities", configured.routerUrl));
      const capabilities = await response.json().catch(() => ({}));
      if (generation !== connectionGeneration || routerSocket?.readyState !== WebSocket.OPEN || configured.leaseId === "" || capabilities.subagent_lifecycle_v2 !== true) return;
      lifecycleEnabled = true;
      routerSocket.send(JSON.stringify({
        version: "v1", frame_id: `subagent-enable-${randomUUID()}`, type: "subagent_sync_enable",
        agent_id: configured.agentId, instance_id: configured.instanceId, lease_id: configured.leaseId,
        ...(lifecycleVersion > 0 ? { after_version: lifecycleVersion } : {}),
      }));
    } catch {
      // A 404 or an unavailable old router keeps the legacy refresh path alive.
    }
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
        if (command.type === "get_history") {
          await fetchHistory(command, configured, sendBrowser);
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
          return;
        }
        if (command.type === "reminder_crud") {
          await sendReminderCrud(command, configured, sendBrowser);
          return;
        }
        if (command.type === "goal_control") {
          const controlId = normalizeControlId(command.controlId ?? "");
          const targetAgentId = normalizeAgentId(command.targetAgentId ?? "");
          const targetInstanceId = normalizeInstanceId(command.targetInstanceId ?? "");
          const action = String(command.action ?? "");
          const goal = String(command.goal ?? "");
          const settings = normalizeGoalControlSettings(command.settings);
          if (!['set', 'off'].includes(action)) throw new Error("Goal action is invalid");
          if (action === "set" && (goal.trim() === "" || [...goal].length > 4000)) throw new Error("A goal description is required");
          if (!goalControlBlockingSupported && settings.blocking === false) {
            const error = new Error("This router does not support disabling goal blocking.");
            error.code = "goal_control_blocking_unavailable";
            throw error;
          }
          if (routerSocket?.readyState !== WebSocket.OPEN || configured.leaseId === "") throw new Error("Router is not connected");
          const { blocking: unusedBlocking, ...legacySettings } = settings;
          const requestId = `goal-control-${randomUUID()}`;
          trackPendingGoalControl({ controlId, requestId, targetAgentId, targetInstanceId });
          routerSocket.send(JSON.stringify({
            version: "v1",
            frame_id: requestId,
            type: "goal_control",
            agent_id: configured.agentId,
            instance_id: configured.instanceId,
            lease_id: configured.leaseId,
            target_agent_id: targetAgentId,
            target_instance_id: targetInstanceId,
            control_id: controlId,
            action,
            goal,
            settings: goalControlBlockingSupported ? settings : legacySettings,
          }));
          return;
        }
        if (command.type === "reminder_lock_control") {
          const controlId = normalizeControlId(command.controlId ?? "");
          const targetAgentId = normalizeAgentId(command.targetAgentId ?? "");
          const targetInstanceId = normalizeInstanceId(command.targetInstanceId ?? "");
          const reminderId = normalizeReminderId(command.reminderId ?? "");
          const action = String(command.action ?? "");
          if (!["set", "clear"].includes(action)) throw new Error("Reminder lock action is invalid");
          if (routerSocket?.readyState !== WebSocket.OPEN || configured.leaseId === "") throw new Error("Router is not connected");
          const requestId = `reminder-lock-control-${randomUUID()}`;
          trackPendingReminderLockControl({ controlId, requestId, targetAgentId, targetInstanceId, reminderId, action });
          routerSocket.send(JSON.stringify({
            version: "v1",
            frame_id: requestId,
            type: "reminder_lock_control",
            agent_id: configured.agentId,
            instance_id: configured.instanceId,
            lease_id: configured.leaseId,
            target_agent_id: targetAgentId,
            target_instance_id: targetInstanceId,
            control_id: controlId,
            reminder_id: reminderId,
            action,
          }));
          return;
        }
        if (command.type === "set_model") {
          const controlId = normalizeControlId(command.controlId ?? "");
          const targetAgentId = normalizeAgentId(command.targetAgentId ?? "");
          const targetInstanceId = normalizeInstanceId(command.targetInstanceId ?? "");
          const provider = String(command.model?.provider ?? "").trim();
          const id = String(command.model?.id ?? "").trim();
          // Pi model IDs commonly include routing/size suffixes such as
          // `@272k:fast`; the target extension performs the authoritative
          // availability check, so the relay only rejects control characters
          // and oversized values here.
          if (provider.length === 0 || [...provider].length > 128 || id.length === 0 || [...id].length > 256
            || /[\u0000-\u001f\u007f]/u.test(provider) || /[\u0000-\u001f\u007f]/u.test(id)) {
            throw new Error("Model provider and id are required");
          }
          if (routerSocket?.readyState !== WebSocket.OPEN || configured.leaseId === "") throw new Error("Router is not connected");
          const requestId = `model-control-${randomUUID()}`;
          trackPendingControl({ controlId, requestId, targetAgentId });
          routerSocket.send(JSON.stringify({
            version: "v1",
            frame_id: requestId,
            type: "model_control",
            agent_id: configured.agentId,
            instance_id: configured.instanceId,
            lease_id: configured.leaseId,
            target_agent_id: targetAgentId,
            target_instance_id: targetInstanceId,
            control_id: controlId,
            action: "set_model",
            model: { provider, id },
          }));
        }
      } catch (error) {
        const goalControlId = command?.type === "goal_control" && CONTROL_ID.test(command.controlId)
          ? command.controlId
          : undefined;
        if (goalControlId !== undefined) {
          const errorCode = error?.code === "goal_control_blocking_unavailable"
            ? error.code
            : "command_error";
          if (!settlePendingGoalControl(goalControlId, { accepted: false, errorCode, message: error.message })) {
            sendBrowser({
              type: "goal_control_result",
              controlId: goalControlId,
              targetAgentId: String(command.targetAgentId ?? ""),
              targetInstanceId: String(command.targetInstanceId ?? ""),
              accepted: false,
              errorCode,
              message: error.message,
              agentId: configured.agentId,
              routerUrl: configured.routerUrl,
            });
          }
          return;
        }
        const reminderCrudRequestId = command?.type === "reminder_crud" && CONTROL_ID.test(command.requestId)
          ? command.requestId
          : undefined;
        if (reminderCrudRequestId !== undefined) {
          sendBrowser({
            type: "reminder_crud_result",
            requestId: reminderCrudRequestId,
            operation: String(command.operation ?? ""),
            reminderId: String(command.reminderId ?? ""),
            accepted: false,
            status: 0,
            body: { error: { code: "request_failed", message: error.message } },
            agentId: configured.agentId,
            routerUrl: configured.routerUrl,
          });
          return;
        }
        const reminderLockControlId = command?.type === "reminder_lock_control" && CONTROL_ID.test(command.controlId)
          ? command.controlId
          : undefined;
        if (reminderLockControlId !== undefined) {
          if (!settlePendingReminderLockControl(reminderLockControlId, { accepted: false, errorCode: "command_error" })) {
            sendBrowser({
              type: "reminder_lock_control_result",
              controlId: reminderLockControlId,
              targetAgentId: String(command.targetAgentId ?? ""),
              targetInstanceId: String(command.targetInstanceId ?? ""),
              reminderId: String(command.reminderId ?? ""),
              action: String(command.action ?? ""),
              accepted: false,
              errorCode: "command_error",
              agentId: configured.agentId,
              routerUrl: configured.routerUrl,
            });
          }
          return;
        }
        const controlId = command?.type === "set_model" && CONTROL_ID.test(command.controlId)
          ? command.controlId
          : undefined;
        if (controlId !== undefined) {
          if (!settlePendingControl(controlId, { accepted: false, errorCode: "command_error" })) {
            sendBrowser({
              type: "model_control_result",
              controlId,
              targetAgentId: String(command.targetAgentId ?? ""),
              accepted: false,
              errorCode: "command_error",
              agentId: configured.agentId,
              routerUrl: configured.routerUrl,
            });
          }
        } else {
          sendBrowser({ type: "command_error", message: error.message });
        }
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
      for (const pending of pendingControlsByControlId.values()) clearTimeout(pending.timer);
      pendingControlsByControlId.clear();
      pendingControlsByRequestId.clear();
      for (const pending of pendingGoalControlsByControlId.values()) clearTimeout(pending.timer);
      pendingGoalControlsByControlId.clear();
      pendingGoalControlsByRequestId.clear();
      for (const pending of pendingReminderLockControlsByControlId.values()) clearTimeout(pending.timer);
      pendingReminderLockControlsByControlId.clear();
      pendingReminderLockControlsByRequestId.clear();
      routerSocket?.close(1000, "browser closed");
    },
  };
}

async function sendReminderCrud(command, configured, sendBrowser) {
  const requestId = normalizeControlId(command.requestId ?? "");
  const operation = String(command.operation ?? "");
  const methodByOperation = { create: "POST", update: "PATCH", delete: "DELETE" };
  const method = methodByOperation[operation];
  if (method === undefined) throw new Error("Reminder operation is invalid");
  const reminderId = method === "POST" ? "" : normalizeReminderId(command.reminderId ?? "");
  const url = new URL(method === "POST" ? "/v1/reminders" : `/v1/reminders/${encodeURIComponent(reminderId)}`, configured.routerUrl);
  const options = { method, headers: { "content-type": "application/json" } };
  if (method !== "DELETE") {
    if (command.payload === null || typeof command.payload !== "object" || Array.isArray(command.payload)) throw new Error("Reminder payload is invalid");
    options.body = JSON.stringify(command.payload);
  }
  const response = await fetch(url, options);
  const body = await response.json().catch(() => ({}));
  sendBrowser({
    type: "reminder_crud_result",
    requestId,
    operation,
    reminderId: reminderId || body.reminder?.id,
    accepted: response.ok,
    status: response.status,
    body,
    agentId: configured.agentId,
    routerUrl: configured.routerUrl,
  });
}

async function fetchHistory(command, configured, sendBrowser) {
  const peer = normalizeConversationPeer(command.peer ?? "");
  const url = new URL("/v1/conversations", configured.routerUrl);
  url.searchParams.set("owner", configured.agentId);
  url.searchParams.set("peer", peer);
  const response = await fetch(url);
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    sendBrowser({
      type: "history_error",
      peer,
      requestId: command.requestId,
      message: body.error?.message ?? `Router rejected history (${response.status})`,
      agentId: configured.agentId,
      routerUrl: configured.routerUrl,
    });
    return;
  }
  sendBrowser({
    type: "history",
    peer,
    requestId: command.requestId,
    highWaterSequence: body.high_water_sequence ?? 0,
    messages: (body.messages ?? []).map((message) => messageWithAbsoluteAttachments(message, configured.routerUrl)),
    agentId: configured.agentId,
    routerUrl: configured.routerUrl,
  });
}

async function sendMessage(command, configured, sendBrowser, refreshAgents) {
  const routerUrl = configured.routerUrl;
  const agentId = configured.agentId;
  const to = normalizeAgentId(command.to ?? "");
  const body = String(command.body ?? "");
  const attachments = normalizeImageAttachments(command.images ?? (command.image === undefined ? [] : [command.image]));
  if (body.trim() === "" && attachments.length === 0) throw new Error("Message cannot be blank");
  const envelope = {
    id: randomUUID(),
    from: agentId,
    to,
    body,
    ...(attachments.length === 0 ? {} : { attachments }),
    created_at: new Date().toISOString(),
    source: { kind: "web_ui" },
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
  const accepted = responseBody.conversation_message ?? responseBody.message ?? envelope;
  const displayAttachments = absoluteAttachmentUrls(accepted.attachments, routerUrl);
  const displayMessage = {
    ...accepted,
    from: agentId,
    to,
    body: displayAttachments.length === 0 ? accepted.body ?? body : body,
    attachments: displayAttachments,
    created_at: accepted.created_at ?? envelope.created_at,
    source: accepted.source ?? envelope.source,
  };
  sendBrowser({ type: "send_result", ok: true, message: displayMessage, localId: command.localId, agentId, routerUrl });
  await refreshAgents();
}

function absoluteAttachmentUrls(attachments = [], routerUrl) {
  return attachments.map((attachment) => ({
    ...attachment,
    url: new URL(attachment.url, routerUrl).toString(),
  }));
}

function messageWithAbsoluteAttachments(message, routerUrl) {
  return {
    ...message,
    attachments: absoluteAttachmentUrls(message.attachments, routerUrl),
  };
}

function normalizeImageAttachments(images) {
  if (!Array.isArray(images)) throw new Error("Images must be an array");
  if (images.length > 3) throw new Error("You can attach up to 3 images");
  return images.map((image) => {
    const dataUrl = String(image?.dataUrl ?? image?.data_url ?? "");
    if (!dataUrl.startsWith("data:image/")) throw new Error("Images must be data URLs");
    return { type: "image", data_url: dataUrl, name: image?.name === undefined ? undefined : String(image.name) };
  });
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

function normalizeControlId(value) {
  const controlId = String(value).trim();
  if (!CONTROL_ID.test(controlId)) throw new Error("Control ID is invalid");
  return controlId;
}

function normalizeInstanceId(value) {
  const instanceId = String(value).trim();
  if (!CONTROL_ID.test(instanceId)) throw new Error("Instance ID is invalid");
  return instanceId;
}

function normalizeReminderId(value) {
  const reminderId = String(value).trim();
  if (!/^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$/.test(reminderId)) throw new Error("Reminder ID is invalid");
  return reminderId;
}

function normalizeGoalControlSettings(value) {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error("Goal settings are required");
  const required = [
    "goal_lock_enabled", "snooze_enabled", "snooze_duration_ms", "snooze_turn_limit",
    "snooze_budget_cap_ms", "snooze_refill_yield_ms", "snooze_refill_elapsed_ms",
  ];
  const allowed = [...required, "blocking"];
  if (Object.keys(value).some((key) => !allowed.includes(key)) || required.some((key) => !Object.hasOwn(value, key))) {
    throw new Error("Goal settings are incomplete");
  }
  if ((Object.hasOwn(value, "blocking") && typeof value.blocking !== "boolean")
    || typeof value.goal_lock_enabled !== "boolean" || typeof value.snooze_enabled !== "boolean"
    || !positiveInteger(value.snooze_duration_ms) || !positiveInteger(value.snooze_refill_yield_ms)
    || !positiveInteger(value.snooze_refill_elapsed_ms)
    || !(value.snooze_turn_limit === null || (Number.isSafeInteger(value.snooze_turn_limit) && value.snooze_turn_limit >= 1 && value.snooze_turn_limit <= 20))
    || !(value.snooze_budget_cap_ms === null || positiveInteger(value.snooze_budget_cap_ms))) {
    throw new Error("Goal settings are invalid");
  }
  return {
    blocking: value.blocking !== false,
    goal_lock_enabled: value.goal_lock_enabled,
    snooze_enabled: value.snooze_enabled,
    snooze_duration_ms: value.snooze_duration_ms,
    snooze_turn_limit: value.snooze_turn_limit,
    snooze_budget_cap_ms: value.snooze_budget_cap_ms,
    snooze_refill_yield_ms: value.snooze_refill_yield_ms,
    snooze_refill_elapsed_ms: value.snooze_refill_elapsed_ms,
  };
}

function positiveInteger(value) {
  return Number.isSafeInteger(value) && value > 0;
}

function normalizeConversationPeer(value) {
  const peer = String(value).trim();
  if (peer.length === 0 || peer.length > 128) throw new Error("Conversation peer is invalid");
  return peer;
}

function sendJson(response, statusCode, payload) {
  response.writeHead(statusCode, { "cache-control": "no-store", "content-type": "application/json; charset=utf-8" });
  response.end(JSON.stringify(payload));
}

function usage() {
  return "Usage: node tools/router_web_ui.mjs [--host 127.0.0.1] [--port 8787] [--router http://127.0.0.1:8765] [--default-agent-id wolfgang-web]";
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
