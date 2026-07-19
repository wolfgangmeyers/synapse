#!/usr/bin/env node
import { readFileSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

import { createMailboxClient, routerHttpUrl } from "../harness/mailbox-client.js";
import { createBridge, BRIDGE_AGENT_ID } from "./bridge.js";
import { createDiscordGateway, createDiscordRest } from "./discord.js";
import { openBridgeState } from "./state.js";

function localFile(path, description) {
  if (typeof path !== "string" || path.length === 0) throw new Error(`${description} path is required`);
  const mode = statSync(path).mode & 0o777;
  if ((mode & 0o077) !== 0) throw new Error(`${description} must not be group or world readable`);
  return readFileSync(path, "utf8").trim();
}

export function loadRuntimeConfig(environment = process.env) {
  const config = JSON.parse(localFile(environment.MECHA_DISCORD_CONFIG, "Discord bridge config"));
  if (!Array.isArray(config.allowlist) || config.allowlist.length === 0 || config.allowlist.some((entry) => typeof entry?.guildId !== "string" || typeof entry?.channelId !== "string")) {
    throw new Error("Discord bridge config needs a non-empty guild/channel allowlist");
  }
  const token = localFile(environment.MECHA_DISCORD_TOKEN_FILE, "Discord token secret");
  if (!token) throw new Error("Discord token secret is empty");
  return { allowlist: config.allowlist, token, statePath: environment.MECHA_DISCORD_STATE_PATH ?? join(homedir(), ".local", "state", "mecha", "discord-gateway-bridge.sqlite") };
}

export async function run(config = loadRuntimeConfig()) {
  const state = openBridgeState({ databasePath: config.statePath });
  const rest = createDiscordRest({ token: config.token });
  let client;
  const postEnvelope = async (envelope) => {
    const response = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? "http://127.0.0.1:8765")}/v1/messages`, {
      method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify(envelope), signal: AbortSignal.timeout(5_000),
    });
    if (!response.ok) throw Object.assign(new Error(`Mailbox router rejected inbound Discord message (${response.status})`), { status: response.status, retryAfterMs: Number(response.headers.get("retry-after") ?? 1) * 1_000 });
    return response.json();
  };
  const bridge = createBridge({ state, postEnvelope, discord: rest });
  client = createMailboxClient({
    agentId: BRIDGE_AGENT_ID,
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
  const gateway = createDiscordGateway({ token: config.token, allowlist: config.allowlist, onMessage: (event) => bridge.acceptDiscordEvent(event), log: () => {} });
  await client.connect();
  await bridge.flush();
  await gateway.start();
  const shutdown = () => { gateway.close(); bridge.close(); client.close(); state.close(); };
  process.once("SIGINT", shutdown); process.once("SIGTERM", shutdown);
  return { shutdown };
}

if (import.meta.main) run().catch((error) => { console.error(`discord-gateway-bridge: ${error.message}`); process.exitCode = 1; });
