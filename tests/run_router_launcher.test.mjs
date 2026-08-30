import assert from "node:assert/strict";
import { chmod, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { execFile } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
const ROOT = new URL("../", import.meta.url).pathname;
const LAUNCHER = new URL("../tools/run_router.sh", import.meta.url).pathname;
const NODE_ROUTER = new URL("../tools/run_router.mjs", import.meta.url).pathname;

async function runWithFake(command, launcherArgs = []) {
  const directory = await mkdtemp(join(tmpdir(), "run-router-launcher-"));
  try {
    const capture = join(directory, "args");
    const fake = join(directory, command);
    await writeFile(fake, "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$CAPTURE\"\n");
    await chmod(fake, 0o755);
    const environment = {
      ...process.env,
      PATH: `${directory}:${process.env.PATH}`,
      CAPTURE: capture,
      MECHA_ROUTER_DATABASE: join(directory, "router.sqlite"),
      MECHA_ROUTER_LOG: join(directory, "router.jsonl"),
      MECHA_ROUTER_HOST: "127.0.0.1",
      MECHA_ROUTER_PORT: "18765",
      MECHA_ROUTER_MAX_BATCH_CHARS: "4096",
    };
    await execFileAsync("bash", [LAUNCHER, ...launcherArgs], { cwd: ROOT, env: environment });
    return {
      args: (await readFile(capture, "utf8")).trim().split("\n"),
      database: environment.MECHA_ROUTER_DATABASE,
      log: environment.MECHA_ROUTER_LOG,
    };
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

test("supported router launcher delegates to the Go Make target with configured paths", async () => {
  const { args, database, log } = await runWithFake("make");

  assert.deepEqual(args, [
    "-C",
    join(ROOT, "router-proxy"),
    "run-router",
    `ROUTER_DATABASE=${database}`,
    "ROUTER_HOST=127.0.0.1",
    "ROUTER_PORT=18765",
    `ROUTER_LOG=${log}`,
    "ROUTER_MAX_BATCH_CHARS=4096",
  ]);
});

test("router launcher keeps an explicit Node rollback path", async () => {
  const { args, log } = await runWithFake("node", ["--node"]);

  assert.equal(args[0], NODE_ROUTER);
  assert.equal(args[1], "--database");
  assert.match(args[2], /router\.sqlite$/);
  assert.deepEqual(args.slice(-8), ["--host", "127.0.0.1", "--port", "18765", "--log", log, "--max-batch-chars", "4096"]);
});
