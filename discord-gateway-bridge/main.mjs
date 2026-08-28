#!/usr/bin/env node
import { readFileSync, statSync } from "node:fs";
import { isAbsolute, resolve } from "node:path";

import { assertAgentId, createMailboxClient, routerHttpUrl } from "../pi-extensions/mecha-messaging/mailbox-client.js";
import { createBridge } from "./bridge.js";
import { createDiscordGateway, createDiscordRest } from "./discord.js";
import { openBridgeState } from "./state.js";

function localFile(path, description) {
  if (typeof path !== "string" || path.length === 0) throw new Error(`${description} path is required`);
  const mode = statSync(path).mode & 0o777;
  if ((mode & 0o077) !== 0) throw new Error(`${description} must not be group or world readable`);
  return readFileSync(path, "utf8").trim();
}

function requiredPath(value, description) {
  if (typeof value !== "string" || value.length === 0 || !isAbsolute(value)) throw new Error(`${description} must be an absolute path`);
  return resolve(value);
}

export function loadRuntimeConfig(environment = process.env) {
  const configPath = requiredPath(environment.MECHA_DISCORD_CONFIG, "Discord bridge config");
  const config = JSON.parse(localFile(configPath, "Discord bridge config"));
  if (!config || typeof config !== "object" || Array.isArray(config) || Object.keys(config).some((key) => !["agentId", "channelId", "statePath"].includes(key))
    || typeof config.channelId !== "string" || config.channelId.length === 0) {
    throw new Error("Discord bridge config needs exactly one channelId");
  }
  assertAgentId(config.agentId);
  if (config.agentId.length > 128) throw new Error("Discord bridge agentId exceeds the Mailbox protocol limit");
  const statePath = requiredPath(config.statePath, "Discord bridge statePath");
  const tokenPath = requiredPath(environment.MECHA_DISCORD_TOKEN_FILE, "Discord token secret");
  if (statePath === configPath || statePath === tokenPath) throw new Error("Discord bridge statePath must be distinct from config and token files");
  const token = localFile(tokenPath, "Discord token secret");
  if (!token) throw new Error("Discord token secret is empty");
  return { agentId: config.agentId, channelId: config.channelId, token, statePath };
}

export async function run(config = loadRuntimeConfig()) {
  const state = openBridgeState({ databasePath: config.statePath, agentId: config.agentId });
  const rest = createDiscordRest({ token: config.token });
  let client;
  const postEnvelope = async (envelope) => {
    const response = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? "http://127.0.0.1:8765")}/v1/messages`, {
      method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(envelope), signal: AbortSignal.timeout(5_000),
    });
    if (!response.ok) throw Object.assign(new Error(`Mailbox router rejected inbound Discord message (${response.status})`), { status: response.status, retryAfterMs: Number(response.headers.get("retry-after") ?? 1) * 1_000 });
    return response.json();
  };
  const bridge = createBridge({ state, postEnvelope, discord: rest, agentId: config.agentId, channelId: config.channelId });
  client = createMailboxClient({
    agentId: config.agentId,
    onEnvelope: async (message, delivery) => {
      const accepted = bridge.acceptSupervisorMessage(message);
      if (!accepted.accepted) { client.sendReceipt("nack", delivery, "injection_failed"); return; }
      // The SQLite insert is the durable handoff boundary.
      client.sendReceipt("injection_requested", delivery);
      client.sendReceipt("ack", delivery);
      await bridge.flushOutbound();
    },
    log: () => {},
  });
  const gateway = createDiscordGateway({ token: config.token, channelId: config.channelId, onMessage: (event) => bridge.acceptDiscordEvent(event), log: () => {} });
  await client.connect();
  await bridge.flush();
  await gateway.start();
  const shutdown = async () => {
    gateway.close();
    client.close();
    await bridge.close();
    state.close();
  };
  process.once("SIGINT", () => { void shutdown(); });
  process.once("SIGTERM", () => { void shutdown(); });
  return { shutdown };
}

if (import.meta.main) run().catch((error) => { console.error(`discord-gateway-bridge: ${error.message}`); process.exitCode = 1; });
