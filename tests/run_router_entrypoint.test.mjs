import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { chmod, mkdtemp, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

const ROUTER_PATH = fileURLToPath(new URL("../tools/run_router.mjs", import.meta.url));
const TOOLS_DIR = dirname(ROUTER_PATH);
const { ensureStartupTmux, startRouter } = await import("../tools/run_router.mjs");

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

test("startup tmux selects the current session before listed mecha sessions", async () => {
  const calls = [];
  const run = async (_command, args) => {
    calls.push(args);
    if (args[0] === "display-message") return { stdout: "mecha-1\n" };
    throw new Error("listing should not be needed");
  };
  assert.deepEqual(await ensureStartupTmux({
    session: "mecha",
    window: "ignored",
    env: { TMUX: "/tmp/tmux/default,123,1", TMUX_PANE: "%1" },
    run,
  }), {
    session: "mecha-1", window: "supervisor", createdSession: false, createdWindow: false,
  });
  assert.deepEqual(calls, [["display-message", "-p", "-t", "%1", "#{session_name}"]]);
});

test("startup tmux falls back after empty current-session output", async () => {
  const calls = [];
  const run = async (_command, args) => {
    calls.push(args);
    if (args[0] === "display-message") return { stdout: " \n" };
    return { stdout: "listed-mecha\n" };
  };
  assert.deepEqual(await ensureStartupTmux({
    session: "mecha",
    env: { TMUX: "default,123,1" },
    run,
  }), {
    session: "listed-mecha", window: "supervisor", createdSession: false, createdWindow: false,
  });
  assert.deepEqual(calls, [
    ["display-message", "-p", "#{session_name}"],
    ["list-sessions", "-F", "#{session_name}"],
  ]);
});

test("startup tmux falls back after invalid current-session output", async () => {
  const calls = [];
  const run = async (_command, args) => {
    calls.push(args);
    if (args[0] === "display-message") return { stdout: "not a valid session name!\n" };
    return { stdout: "listed-mecha\n" };
  };
  assert.deepEqual(await ensureStartupTmux({
    session: "mecha",
    env: { TMUX: "default,123,1" },
    run,
  }), {
    session: "listed-mecha", window: "supervisor", createdSession: false, createdWindow: false,
  });
  assert.deepEqual(calls, [
    ["display-message", "-p", "#{session_name}"],
    ["list-sessions", "-F", "#{session_name}"],
  ]);
});

test("startup tmux falls back after current-session display exits with code 1", async () => {
  const calls = [];
  const run = async (_command, args) => {
    calls.push(args);
    if (args[0] === "display-message") {
      const error = new Error("no current pane");
      error.code = 1;
      throw error;
    }
    return { stdout: "listed-mecha\n" };
  };
  assert.deepEqual(await ensureStartupTmux({
    session: "mecha",
    env: { TMUX: "default,123,1" },
    run,
  }), {
    session: "listed-mecha", window: "supervisor", createdSession: false, createdWindow: false,
  });
  assert.deepEqual(calls, [
    ["display-message", "-p", "#{session_name}"],
    ["list-sessions", "-F", "#{session_name}"],
  ]);
});

test("startup tmux propagates unexpected current-session discovery errors", async () => {
  const run = async (_command, args) => {
    if (args[0] === "display-message") {
      const error = new Error("permission denied");
      error.code = 13;
      throw error;
    }
    throw new Error("listing should not be needed");
  };
  await assert.rejects(
    ensureStartupTmux({ session: "mecha", env: { TMUX: "default,123,1" }, run }),
    { message: "tmux session discovery failed: permission denied" },
  );
});

test("startup tmux propagates unexpected listed-session discovery errors", async () => {
  const run = async () => {
    const error = new Error("tmux socket unavailable");
    error.code = 2;
    throw error;
  };
  await assert.rejects(
    ensureStartupTmux({ session: "mecha", env: {}, run }),
    { message: "tmux session discovery failed: tmux socket unavailable" },
  );
});

test("startup tmux falls back to the first listed mecha session outside tmux", async () => {
  const calls = [];
  const run = async (_command, args) => {
    calls.push(args);
    return { stdout: "worker\nfirst-mecha-session\nsecond-mecha-session\n" };
  };
  assert.deepEqual(await ensureStartupTmux({ session: "mecha", window: "ignored", env: {}, run }), {
    session: "first-mecha-session", window: "supervisor", createdSession: false, createdWindow: false,
  });
  assert.deepEqual(calls, [["list-sessions", "-F", "#{session_name}"]]);
});

test("startup tmux provisions exactly mecha with supervisor when no session exists", async () => {
  const calls = [];
  const run = async (_command, args) => {
    calls.push(args);
    if (args[0] === "list-sessions") return { stdout: "worker\n" };
    return { stdout: "" };
  };
  assert.deepEqual(await ensureStartupTmux({ session: "mecha", window: "ignored", env: {}, run }), {
    session: "mecha", window: "supervisor", createdSession: true, createdWindow: true,
  });
  assert.deepEqual(calls, [
    ["list-sessions", "-F", "#{session_name}"],
    ["new-session", "-d", "-s", "mecha", "-n", "supervisor"],
  ]);
});

test("startRouter passes the selected startup session into createAgentRouter", async (t) => {
  const directory = await mkdtemp(join(tmpdir(), "run-router-start-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const previousKbRoot = process.env.MECHA_KB_ROOT;
  process.env.MECHA_KB_ROOT = join(directory, "kb");
  t.after(() => {
    if (previousKbRoot === undefined) delete process.env.MECHA_KB_ROOT;
    else process.env.MECHA_KB_ROOT = previousKbRoot;
  });

  let routerOptions;
  let closed = false;
  const fakeTmux = join(directory, "tmux");
  await writeFile(fakeTmux, "#!/bin/sh\nif [ \"$1\" = \"display-message\" ]; then printf 'current-session\\n'; exit 0; fi\nexit 99\n", { mode: 0o700 });
  await chmod(fakeTmux, 0o700);
  const previousPath = process.env.PATH;
  const previousTmux = process.env.TMUX;
  process.env.PATH = `${directory}:${previousPath ?? ""}`;
  process.env.TMUX = "default,123,1";
  t.after(() => {
    if (previousPath === undefined) delete process.env.PATH;
    else process.env.PATH = previousPath;
    if (previousTmux === undefined) delete process.env.TMUX;
    else process.env.TMUX = previousTmux;
  });

  const running = await startRouter({
    database: join(directory, "router.sqlite"),
    log: join(directory, "router.jsonl"),
    output: () => {},
    createRouter: (options) => {
      routerOptions = options;
      return {
        listen: async () => "ws://127.0.0.1:12345/v1/agents",
        close: async () => { closed = true; },
      };
    },
  });
  t.after(async () => {
    if (!closed) await running.close();
  });

  assert.equal(routerOptions.startupSession, "current-session");
  await running.close();
  assert.equal(closed, true);
});

test("importing the router does not start it", async () => {
  const module = await import("../tools/run_router.mjs");

  // Importing must expose the API without running main(); a started router would have
  // failed on the missing --database argument.
  assert.equal(typeof module.startRouter, "function");
  assert.equal(typeof module.parseArguments, "function");
});
