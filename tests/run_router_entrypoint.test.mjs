import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { mkdtemp, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const ROUTER_PATH = fileURLToPath(new URL("../tools/run_router.mjs", import.meta.url));
const TOOLS_DIR = dirname(ROUTER_PATH);
const { ensureStartupTmux } = await import("../tools/run_router.mjs");

// The router is the single point of failure for all mailbox traffic, and its entrypoint
// check had the same defect the sidecar's did: comparing an unresolved `process.argv[1]`
// against a loader-resolved `import.meta.url`. Launched through a symlinked directory it
// would exit 0 having started nothing — no router, no error, no log.
//
// `--help` is the discriminator: it proves main() ran while binding no port and opening
// no database, so the test cannot disturb a router already running on this machine.
async function runRouterThroughDirectory(directory) {
  const { stdout, stderr } = await execFileAsync(
    process.execPath,
    [join(directory, "run_router.mjs"), "--help"],
    { timeout: 30_000 },
  );
  return { stdout, stderr };
}

test("router CLI runs when invoked through a symlinked tools directory", async (t) => {
  const parent = await mkdtemp(join(tmpdir(), "run-router-symlink-"));
  t.after(() => rm(parent, { recursive: true, force: true }));
  const symlinkedTools = join(parent, "tools");
  await symlink(TOOLS_DIR, symlinkedTools, "dir");

  const { stdout } = await runRouterThroughDirectory(symlinkedTools);

  assert.match(
    stdout,
    /Usage: node tools\/run_router\.mjs/,
    "router printed nothing through the symlinked path — main() never ran",
  );
});

test("router CLI runs when invoked through its real tools directory", async () => {
  const { stdout } = await runRouterThroughDirectory(TOOLS_DIR);

  assert.match(stdout, /Usage: node tools\/run_router\.mjs/);
  assert.match(stdout, /--host 127\.0\.0\.1\|::1\|0\.0\.0\.0/);
});

test("startup tmux provisions the session and supervisor window when absent", async () => {
  const calls = [];
  let first = true;
  const run = async (_command, args) => {
    calls.push(args);
    if (first) { first = false; const error = new Error("missing"); error.code = 1; throw error; }
  };
  assert.deepEqual(await ensureStartupTmux({ session: "missing", window: "supervisor", run }), {
    session: "missing", window: "supervisor", createdSession: true, createdWindow: true,
  });
  assert.deepEqual(calls, [["has-session", "-t", "missing"], ["new-session", "-d", "-s", "missing", "-n", "supervisor"]]);
});

test("startup tmux selects the first listed session containing literal mecha", async () => {
  const calls = [];
  const run = async (_command, args) => {
    calls.push(args);
    return { stdout: "worker\nfirst-mecha-session\nsecond-mecha-session\n" };
  };
  assert.deepEqual(await ensureStartupTmux({ session: "mecha", window: "ignored", run }), {
    session: "first-mecha-session", window: "supervisor", createdSession: false, createdWindow: false,
  });
  assert.deepEqual(calls, [["list-sessions", "-F", "#{session_name}"]]);
});

test("startup tmux provisions exactly mecha with supervisor when no match exists", async () => {
  const calls = [];
  const run = async (_command, args) => {
    calls.push(args);
    if (args[0] === "list-sessions") return { stdout: "worker\n" };
    return { stdout: "" };
  };
  assert.deepEqual(await ensureStartupTmux({ session: "mecha", window: "ignored", run }), {
    session: "mecha", window: "supervisor", createdSession: true, createdWindow: true,
  });
  assert.deepEqual(calls, [
    ["list-sessions", "-F", "#{session_name}"],
    ["new-session", "-d", "-s", "mecha", "-n", "supervisor"],
  ]);
});

test("importing the router does not start it", async () => {
  const module = await import("../tools/run_router.mjs");

  // Importing must expose the API without running main(); a started router would have
  // failed on the missing --database argument.
  assert.equal(typeof module.startRouter, "function");
  assert.equal(typeof module.parseArguments, "function");
});
