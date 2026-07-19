#!/usr/bin/env node

import { mkdir } from "node:fs/promises";
import { homedir } from "node:os";
import { join } from "node:path";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { openMessageRepository } from "../harness/db.js";
import { openMemoryCatalog } from "../harness/memory-catalog.js";
import { createJsonlLogger } from "../harness/jsonl_logger.js";
import { createAgentRouter } from "../harness/router.js";

const LOOPBACK_HOSTS = new Set(["127.0.0.1", "::1"]);

export function parseArguments(argv) {
  const options = { host: "127.0.0.1", port: 8765 };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    if (argument === "--database" || argument === "--host" || argument === "--port" || argument === "--log") {
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
  };
}

export async function startRouter({
  database,
  host,
  port,
  log,
  logPath,
  output = console.log,
} = {}) {
  if (typeof database !== "string" || database.length === 0) {
    throw new TypeError("database is required");
  }
  const readyOutput = typeof log === "function" ? log : output;
  const resolvedLogPath = typeof log === "string" ? log : (logPath ?? `${database}.jsonl`);
  await mkdir(dirname(resolve(database)), { recursive: true, mode: 0o700 });
  let repository;
  let memoryCatalog;
  let jsonlLogger;
  let router;
  try {
    repository = openMessageRepository({ databasePath: database });
    const kbRoot = process.env.MECHA_KB_ROOT ?? join(homedir(), ".mecha-developer", "kb");
    memoryCatalog = openMemoryCatalog({ kbRoot, databasePath: `${database}.memory.sqlite` });
    await memoryCatalog.rebuild();
    jsonlLogger = createJsonlLogger({ logPath: resolvedLogPath });
    const eventLogger = (event, fields) => jsonlLogger.log(event, fields);
    for (const migrationId of repository.migrationIds()) {
      eventLogger("schema_migration", { migration_id: migrationId });
    }
    router = createAgentRouter({ repository, memoryCatalog, host, port, eventLogger });
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
            repository?.close();
            memoryCatalog?.close();
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
      repository?.close();
      memoryCatalog?.close();
    } finally {
      jsonlLogger?.close();
    }
    throw error;
  }
}

function usage() {
  return "Usage: node tools/run_router.mjs --database <sqlite-path> [--log <jsonl-path>] [--host 127.0.0.1] [--port 8765]";
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
    running = await startRouter(options);
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
