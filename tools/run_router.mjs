#!/usr/bin/env node

import { mkdir, readFile } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { openMessageRepository } from "../harness/db.js";
import { createJsonlLogger } from "../harness/jsonl_logger.js";
import { createAgentRouter } from "../harness/router.js";

const LOOPBACK_HOSTS = new Set(["127.0.0.1", "::1"]);

export function parseArguments(argv) {
  const options = { host: "127.0.0.1", port: 8765 };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--database" || argument === "--host" || argument === "--port" || argument === "--log" || argument === "--sentinel-receiver-policies") {
      const value = argv[index + 1];
      if (value === undefined || value.startsWith("--")) {
        throw new Error(`${argument} requires a value`);
      }
      options[argument.slice(2)] = value;
      index += 1;
      continue;
    }
    if (argument === "--help") return { help: true };
    throw new Error(`unknown argument: ${argument}`);
  }

  if (typeof options.database !== "string" || options.database.length === 0) {
    throw new Error("--database is required");
  }
  if (!LOOPBACK_HOSTS.has(options.host)) {
    throw new Error("--host must be a loopback address (127.0.0.1 or ::1)");
  }
  const port = Number(options.port);
  if (!Number.isSafeInteger(port) || port < 1 || port > 65535) {
    throw new Error("--port must be an integer from 1 through 65535");
  }
  return {
    database: options.database,
    log: options.log ?? `${options.database}.jsonl`,
    host: options.host,
    port,
    sentinelReceiverPoliciesPath: options["sentinel-receiver-policies"],
  };
}

export async function loadSentinelReceiverPolicies(path) {
  if (typeof path !== "string" || path.length === 0) throw new TypeError("sentinel receiver policy path is required");
  let parsed;
  try {
    parsed = JSON.parse(await readFile(path, "utf8"));
  } catch {
    throw new Error("sentinel receiver policies must be valid JSON");
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("sentinel receiver policies must be an object");
  }
  for (const [name, policy] of Object.entries(parsed)) {
    if (!/^[a-z][a-z0-9_-]*$/.test(name)
      || policy === null
      || typeof policy !== "object"
      || Array.isArray(policy)
      || Object.keys(policy).some((key) => !["receiver", "senders", "models", "execution"].includes(key))
      || typeof policy.receiver !== "string"
      || policy.receiver.length === 0
      || !Array.isArray(policy.senders)
      || policy.senders.length === 0
      || !policy.senders.every((sender) => typeof sender === "string" && sender.length > 0)
      || !Array.isArray(policy.models)
      || policy.models.length === 0
      || !policy.models.every((model) => typeof model === "string" && model.length > 0)
      || policy.execution === null
      || typeof policy.execution !== "object"
      || Array.isArray(policy.execution)
      || Object.keys(policy.execution).some((key) => !["supervisor", "auth_path", "tools"].includes(key))
      || typeof policy.execution.supervisor !== "string"
      || !policy.senders.includes(policy.execution.supervisor)
      || typeof policy.execution.auth_path !== "string"
      || policy.execution.auth_path.length === 0
      || !Array.isArray(policy.execution.tools)
      || policy.execution.tools.length !== 0) {
      throw new Error("sentinel receiver policy has an invalid fixed profile");
    }
  }
  return parsed;
}

export async function startRouter({
  database,
  host,
  port,
  log,
  logPath,
  output = console.log,
  sentinelReceiverPolicies = {},
} = {}) {
  if (typeof database !== "string" || database.length === 0) {
    throw new TypeError("database is required");
  }
  const readyOutput = typeof log === "function" ? log : output;
  const resolvedLogPath = typeof log === "string" ? log : (logPath ?? `${database}.jsonl`);
  await mkdir(dirname(resolve(database)), { recursive: true, mode: 0o700 });
  const repository = openMessageRepository({ databasePath: database });
  let jsonlLogger;
  let router;
  try {
    jsonlLogger = createJsonlLogger({ logPath: resolvedLogPath });
    const eventLogger = (event, fields) => jsonlLogger.log(event, fields);
    for (const migrationId of repository.migrationIds()) {
      eventLogger("schema_migration", { migration_id: migrationId });
    }
    router = createAgentRouter({ repository, host, port, eventLogger, sentinelReceiverPolicies });
    const websocketUrl = await router.listen();
    const httpUrl = websocketUrl.replace(/^ws:/, "http:").replace(/\/v1\/agents$/, "");
    readyOutput(`MECHA_V2_ROUTER_READY http=${httpUrl} ws=${websocketUrl} database=${database} log=${resolvedLogPath}`);
    return {
      httpUrl,
      websocketUrl,
      logPath: resolvedLogPath,
      async close() {
        try {
          await router.close();
        } finally {
          try {
            repository.close();
          } finally {
            jsonlLogger.close();
          }
        }
      },
    };
  } catch (error) {
    jsonlLogger?.log("router_error", { error_code: "startup_failed" });
    if (router !== undefined) {
      try {
        await router.close();
      } catch {
        // The original startup failure is what the caller needs.
      }
    }
    try {
      repository.close();
    } finally {
      jsonlLogger?.close();
    }
    throw error;
  }
}

function usage() {
  return "Usage: node tools/run_router.mjs --database <sqlite-path> [--log <jsonl-path>] [--host 127.0.0.1] [--port 8765] [--sentinel-receiver-policies <json-file>]";
}

async function main() {
  let options;
  try {
    options = parseArguments(process.argv.slice(2));
  } catch (error) {
    console.error(`run_router.mjs: ${error.message}`);
    console.error(usage());
    process.exitCode = 2;
    return;
  }
  if (options.help) {
    console.log(usage());
    return;
  }

  let running;
  try {
    const sentinelReceiverPolicies = options.sentinelReceiverPoliciesPath === undefined
      ? {}
      : await loadSentinelReceiverPolicies(options.sentinelReceiverPoliciesPath);
    running = await startRouter({ ...options, sentinelReceiverPolicies });
  } catch (error) {
    console.error(`run_router.mjs: ${error.message}`);
    process.exitCode = 1;
    return;
  }
  let closing = false;
  const close = async (signal) => {
    if (closing) return;
    closing = true;
    console.log(`MECHA_V2_ROUTER_STOP signal=${signal}`);
    try {
      await running.close();
    } catch (error) {
      console.error(`run_router.mjs: shutdown failed: ${error.message}`);
      process.exitCode = 1;
    }
  };
  process.once("SIGINT", () => { void close("SIGINT"); });
  process.once("SIGTERM", () => { void close("SIGTERM"); });
}

if (process.argv[1] !== undefined && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
