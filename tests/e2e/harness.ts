import { test } from "@playwright/test";
import { createServer, type Server } from "node:http";
import { createServer as createNetServer } from "node:net";
import { spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, chmod, rm, writeFile, mkdir, readFile, access, realpath } from "node:fs/promises";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);

export interface MockLLMServer {
  port: number;
  url: string;
  close: () => Promise<void>;
  setResponse: (response: any) => void;
  getHistory: () => any[];
}

export interface IsolatedRouter {
  root: string;
  databasePath: string;
  logPath: string;
  readinessStderr: () => string;
  stderr: () => string;
  countStderr: (needle: string) => number;
  stderrLineContains: (...needles: string[]) => boolean;
  port: number;
  httpUrl: string;
  websocketUrl: string;
  process: ChildProcess;
  close: () => Promise<void>;
  restart: (prepareDatabase?: (databasePath: string) => Promise<void>) => Promise<void>;
  advanceClock: (deltaMs: number) => Promise<{ now: string; advanced_ms: number }>;
}

export interface IsolatedProxy {
  port: number;
  url: string;
  process: ChildProcess;
  close: () => Promise<void>;
}

export interface IsolatedFrontend {
  port: number;
  url: string;
  server: Server;
  close: () => Promise<void>;
}

export interface E2ETestHarness {
  root: string;
  router: IsolatedRouter;
  proxy?: IsolatedProxy;
  mockLLM: MockLLMServer;
  frontend: IsolatedFrontend;
  close: () => Promise<void>;
  advanceClock: (deltaMs: number) => Promise<{ now: string; advanced_ms: number }>;
}

export async function allocatePort(): Promise<number> {
  const srv = createNetServer();
  await new Promise<void>((resolve, reject) => {
    srv.once("error", reject);
    srv.listen(0, "127.0.0.1", () => resolve());
  });
  const address = srv.address();
  if (!address || typeof address === "string") {
    throw new Error("Unable to obtain ephemeral loopback port");
  }
  const port = address.port;
  await new Promise<void>((resolve) => srv.close(() => resolve()));
  return port;
}

export async function startMockLLMServer(): Promise<MockLLMServer> {
  const port = await allocatePort();
  let customResponse: any = null;
  const requests: any[] = [];

  const srv = createServer((req, res) => {
    let body = "";
    req.on("data", (chunk) => {
      body += chunk;
    });
    req.on("end", () => {
      let parsedBody: any = {};
      try {
        if (body) parsedBody = JSON.parse(body);
      } catch {}
      requests.push({
        method: req.method,
        url: req.url,
        headers: req.headers,
        body: parsedBody,
      });

      // Default OpenAI-compatible mock response
      const defaultResponse = {
        id: "chatcmpl-mock-" + Date.now(),
        object: "chat.completion",
        created: Math.floor(Date.now() / 1000),
        model: "mock-model",
        choices: [
          {
            index: 0,
            message: {
              role: "assistant",
              content: "Mock LLM response: Hello from deterministic test harness!",
            },
            finish_reason: "stop",
          },
        ],
        usage: {
          prompt_tokens: 10,
          completion_tokens: 10,
          total_tokens: 20,
        },
      };

      res.writeHead(200, { "Content-Type": "application/json" });
      res.end(JSON.stringify(customResponse ?? defaultResponse));
    });
  });

  await new Promise<void>((resolve, reject) => {
    srv.once("error", reject);
    srv.listen(port, "127.0.0.1", () => resolve());
  });

  return {
    port,
    url: `http://127.0.0.1:${port}`,
    close: async () => {
      await new Promise<void>((resolve) => srv.close(() => resolve()));
    },
    setResponse: (resp: any) => {
      customResponse = resp;
    },
    getHistory: () => requests,
  };
}

// Builds can be cold on CI. The Playwright budget must include setup as well
// as the test body; each build adds its own deadline to the active test.
const DEFAULT_BUILD_TIMEOUT_MS = 300_000;
const buildBudgets = new WeakMap<object, number>();
export async function buildGo(args: string[], cwd: string, label: string, timeoutMs: number): Promise<void> {
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) throw new Error("buildTimeoutMs must be positive and finite");
  try {
    const info = test.info();
    const budget = Math.max(info.timeout, buildBudgets.get(info) ?? 0) + timeoutMs + 5_000;
    buildBudgets.set(info, budget);
    info.setTimeout(budget);
  } catch {
    // The harness is also used outside a Playwright test.
  }
  await new Promise<void>((resolve, reject) => {
    const posix = process.platform !== "win32";
    const build = spawn("go", args, { cwd, detached: posix, stdio: ["ignore", "ignore", "pipe"] });
    let settled = false;
    // Drain compiler output without retaining or exposing potentially sensitive data.
    build.stderr.resume();
    // Best effort while the direct child is alive. SIGKILL of the worker, a
    // signal without an exit event, or descendants leaving the group cannot be
    // handled here; Windows only terminates the direct child.
    const terminate = () => {
      if (!build.pid || build.exitCode !== null || build.signalCode !== null) return;
      try { if (posix) process.kill(-build.pid, "SIGKILL"); else build.kill("SIGKILL"); }
      catch (error) { if ((error as NodeJS.ErrnoException).code !== "ESRCH") console.error(`${label} build termination failed:`, error); }
    };
    process.on("exit", terminate);
    const cleanup = () => {
      clearTimeout(timer);
      process.removeListener("exit", terminate);
    };
    const timer = setTimeout(() => {
      settled = true;
      terminate();
      // A descendant outside the group may still hold stderr, and an exited
      // leader's process group cannot safely be signalled. Never await close.
      build.stderr.destroy();
      cleanup();
      reject(new Error(`${label} build timed out after ${timeoutMs}ms`));
    }, timeoutMs);
    build.once("error", error => {
      if (settled) return;
      settled = true;
      cleanup();
      reject(new Error(`${label} build failed to start: ${error.message}`));
    });
    build.once("close", (code, signal) => {
      if (settled) return;
      settled = true;
      cleanup();
      if (code === 0) resolve();
      else reject(new Error(`${label} build failed (${code ?? signal})`));
    });
  });
}

// Join the executable, not its stdio: descendants may inherit the pipes.
async function stopRouterChild(child: ChildProcess, signal: NodeJS.Signals = "SIGTERM"): Promise<void> {
  if (child.exitCode !== null || child.signalCode !== null) {
    child.stdout?.destroy(); child.stderr?.destroy();
    return;
  }
  await new Promise<void>((resolve, reject) => {
    const finish = (error?: Error) => {
      clearTimeout(escalation); clearTimeout(deadline);
      child.removeListener("exit", onExit);
      child.stdout?.destroy(); child.stderr?.destroy();
      error ? reject(error) : resolve();
    };
    const onExit = () => finish();
    child.once("exit", onExit);
    const escalation = setTimeout(() => child.kill("SIGKILL"), 2000);
    const deadline = setTimeout(() => finish(new Error("Isolated router did not exit after SIGKILL")), 5000);
    child.kill(signal);
  });
}

export async function startIsolatedRouter(options: {
  repoRoot: string;
  routerExecutableFixture?: string;
  onRouterSpawnFixture?: (child: ChildProcess) => void;
  startupTimeoutFixtureMs?: number;
  managed?: boolean;
  buildTimeoutMs?: number;
  testClock?: boolean;
  embedded?: { endpoint: string; signedOut?: boolean; kbDir?: (home: string) => string; omitGoogle?: boolean };
  codexCredentialPath?: string;
  fakeEmbeddedCatalog?: boolean;
  restartCodexConsumerFixture?: boolean;
  backgroundExpiryInterval?: string;
  rootControlFixture?: boolean;
  piReviewBarrier?: boolean;
  capacityFixture?: boolean;
  backgroundCountRetentionFixture?: boolean;
  mcpQueueFixture?: boolean;
  embeddedRoot?: { name: string; requestId: string; model: { provider: string; id: string }; instruction: string };
}): Promise<IsolatedRouter> {
  if (options.embedded && !options.managed) throw new Error("Embedded E2E requires managed mode");
  if (options.backgroundExpiryInterval && options.testClock === false) throw new Error("Background expiry interval requires the test clock");
  // Credential storage rejects symlinked ancestors; macOS tmpdir commonly uses /var.
  const root = await mkdtemp(join(await realpath(tmpdir()), "mecha-e2e-"));
  let ownedChild: ChildProcess | undefined;
  try {
  await chmod(root, 0o700);

  const databasePath = join(root, "router.sqlite");
  const logPath = join(root, "router.jsonl");
  const port = await allocatePort();

  const routerProxyDir = join(options.repoRoot, "router-proxy");
  const binaryPath = options.routerExecutableFixture || join(root, "mailbox-router");

  // The count-retention fixture changes only the compiled source, never production configuration.
  let overlayArgs: string[] = [];
  if (options.backgroundCountRetentionFixture) {
    const source = join(routerProxyDir, "internal/managed/background_bash_lifecycle.go");
    const original = await readFile(source, "utf8");
    const needle = "BackgroundBashMaxRecords     = 256";
    if (original.split(needle).length !== 2) throw new Error("count-retention overlay requires exactly one matching production limit");
    const replacement = join(root, "background_bash_lifecycle.go");
    const overlay = join(root, "overlay.json");
    await writeFile(replacement, original.replace(needle, "BackgroundBashMaxRecords     = 3"));
    await writeFile(overlay, JSON.stringify({ Replace: { [source]: replacement } }));
    overlayArgs = [`-overlay=${overlay}`];
  }
  if (!options.routerExecutableFixture) await buildGo(["build", ...overlayArgs, ...(options.embedded ? [`-tags=embedded_restart_test_only${options.capacityFixture ? ",claude_capacity_test_only" : ""}${options.mcpQueueFixture ? ",claude_mcp_queue_test_only" : ""}`] : []), "-o", binaryPath, "./cmd/mailbox-router"], routerProxyDir, "mailbox-router", options.buildTimeoutMs ?? DEFAULT_BUILD_TIMEOUT_MS);

  const isManaged = options.managed ?? false;
  const args = [
    "--database",
    databasePath,
    "--log",
    logPath,
    "--host",
    "127.0.0.1",
    "--port",
    String(port),
    `--managed=${isManaged}`,
  ];

  if (options.testClock !== false) {
    args.push("--test-clock");
  }

  if (isManaged) {
    const home = join(root, "managed-home");
    const piAgentDir = join(home, ".pi", "agent");
    const sessionRoot = join(home, "sessions");
    const cwdRoot = join(root, "work");
    await mkdir(piAgentDir, { recursive: true, mode: 0o700 });
    await mkdir(sessionRoot, { recursive: true, mode: 0o700 });
    await mkdir(cwdRoot, { recursive: true, mode: 0o700 });

    const fixtureDir = resolve(options.repoRoot, "web/router-ui-react/tests/functional/fixtures");
    const piPath = join(root, "fake-managed-pi.mjs");
    const authPath = options.codexCredentialPath
      ? resolve(options.repoRoot, "auth-helper/index.mjs")
      : resolve(fixtureDir, "fake-provider-auth.mjs");

    const { readFile, copyFile } = await import("node:fs/promises");
    let fakePiContent = await readFile(resolve(fixtureDir, "fake-managed-pi.mjs"), "utf8");
    if (fakePiContent.startsWith("#!/usr/bin/env node")) {
      fakePiContent = `#!${process.execPath}\n` + fakePiContent.slice("#!/usr/bin/env node\n".length);
      await writeFile(piPath, fakePiContent);
    } else {
      await copyFile(resolve(fixtureDir, "fake-managed-pi.mjs"), piPath);
    }
    await chmod(piPath, 0o700);

    args.push(
      "--managed-pi", piPath,
      "--managed-home", home,
      "--managed-pi-agent-dir", piAgentDir,
      "--managed-session-root", sessionRoot,
      "--managed-manifest", join(options.repoRoot, "router", "contract", "managed-tools-v1.json"),
      "--managed-pi-subagents", join(resolve(options.repoRoot, ".."), "pi-packages", "packages", "pi-subagents", "src", "index.ts"),
      "--managed-auth-helper", authPath,
      "--managed-runtime-root", options.repoRoot,
      "--managed-cwd-roots", root,
      "--managed-quiet-recipient", "functional-supervisor"
    );
  }

  if (options.embeddedRoot) {
    if (!options.managed) throw new Error("Embedded root requires managed mode");
    args.push("--embedded-root-bootstrap", "--embedded-root-request-id", options.embeddedRoot.requestId,
      "--embedded-root-name", options.embeddedRoot.name, "--embedded-root-cwd", join(root, "work"),
      "--embedded-root-model-provider", options.embeddedRoot.model.provider, "--embedded-root-model-id", options.embeddedRoot.model.id,
      "--embedded-root-initial-instruction", options.embeddedRoot.instruction);
  }

  if (options.embedded) {
    const privateDir = join(root, "private");
    await mkdir(privateDir, { mode: 0o700 });
    if (!options.embedded.signedOut) {
      const claims = Buffer.from(JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: "terra-test-account" } })).toString("base64url");
      await writeFile(join(privateDir, "codex.json"), JSON.stringify({ access_token: `fake.${claims}.terra-test-secret`, refresh_token: "terra-refresh-secret", account_id: "terra-test-account", expires_at: "2099-01-01T00:00:00Z" }), { mode: 0o600 });
    }
    args[args.indexOf("--managed-auth-helper") + 1] = join(options.repoRoot, "auth-helper/index.mjs");
    if (options.fakeEmbeddedCatalog) {
      // Catalog discovery is an external boundary too. The real helper imports
      // the entire SDK and exceeds CodexBrowserAuth's 5s discovery deadline on
      // this host. Use the supported deterministic catalog fixture, while keeping
      // the real private credential consumer beside it (as providerFixture does).
      const { copyFile, symlink } = await import("node:fs/promises");
      const fixtureAuth = join(root, "provider-fixture");
      await mkdir(fixtureAuth, { mode: 0o700 });
      await copyFile(join(options.repoRoot, "web/router-ui-react/tests/functional/fixtures/fake-provider-auth.mjs"), join(fixtureAuth, "index.mjs"));
      if (options.embedded.omitGoogle) {
        // Restrict this disposable catalog to the loopback Codex fixture.
        const fixturePath = join(fixtureAuth, "index.mjs");
        const { readFile } = await import("node:fs/promises");
        const source = await readFile(fixturePath, "utf8");
        const providers = ["google", "google-vertex"];
        const lines = source.split("\n");
        for (const provider of providers) {
          const matches = lines.filter(line => line.includes("available: true") && line.includes(`provider: "${provider}"`));
          if (matches.length !== 1) throw new Error(`expected exactly one available ${provider} catalog fixture entry, found ${matches.length}`);
        }
        const filtered = lines.filter(line => !providers.some(provider => line.includes("available: true") && line.includes(`provider: "${provider}"`))).join("\n");
        await writeFile(fixturePath, filtered);
      }
      await copyFile(join(options.repoRoot, options.restartCodexConsumerFixture ? "tests/e2e/restart-codex-consumer.mjs" : "auth-helper/codex-consumer.mjs"), join(fixtureAuth, "codex-consumer.mjs"));
      if (!options.restartCodexConsumerFixture) {
      // Worktrees do not share installed packages. An explicit dependency path
      // supports isolated checkouts without inheriting Node's loader environment.
      const dependencies = resolve(process.env.MECHA_E2E_AUTH_NODE_MODULES || join(options.repoRoot, "auth-helper/node_modules"));
      try { await access(join(dependencies, "@earendil-works/pi-coding-agent/package.json")); }
      catch { throw new Error("E2E auth-helper dependencies unavailable; run npm ci --prefix auth-helper or set MECHA_E2E_AUTH_NODE_MODULES"); }
      await symlink(dependencies, join(fixtureAuth, "node_modules"), "dir");
      }
      const helperIndex = args.indexOf("--managed-auth-helper");
      args[helperIndex + 1] = join(fixtureAuth, "index.mjs");
      // Keep the manifest within the disposable fixture runtime root.
      await copyFile(join(options.repoRoot, "router/contract/managed-tools-v1.json"), join(root, "manifest.json"));
      args[args.indexOf("--managed-runtime-root") + 1] = root;
      args[args.indexOf("--managed-manifest") + 1] = join(root, "manifest.json");
    }
    args.push("--codex-credential-path", join(privateDir, "codex.json"));
    await writeFile(join(root, "managed-home/.pi/agent/auth.json"), JSON.stringify(options.embedded.omitGoogle ? {} : { google: { type: "api_key", key: "other-provider-test-secret" } }), { mode: 0o600 });
    await writeFile(join(root, "work/note.txt"), "terra harmless tool evidence", { mode: 0o600 });
  }
  if (options.codexCredentialPath) args.push("--codex-credential-path", options.codexCredentialPath);

  // Build tooling uses the caller environment; the router and its descendants
  // never receive machine credentials, cloud config, or Node preload hooks.
  for (const directory of ["managed-home", "config", "cache", "tmp"]) await mkdir(join(root, directory), { recursive: true, mode: 0o700 });
  let runtimeEnvironment: NodeJS.ProcessEnv = {
    PATH: process.env.PATH, LANG: "C.UTF-8", HOME: join(root, "managed-home"),
    XDG_CONFIG_HOME: join(root, "config"), XDG_CACHE_HOME: join(root, "cache"), TMPDIR: join(root, "tmp"),
    CLOUDSDK_CONFIG: join(root, "config", "gcloud"),
    ...(options.embedded ? { EMBEDDED_RESTART_FAKE_ENABLE: "1", EMBEDDED_RESTART_FAKE_ENDPOINT: options.embedded.endpoint } : {}),
    ...(options.embedded?.kbDir ? { MECHA_KB_DIR: options.embedded.kbDir(join(root, "managed-home")) } : {}),
  };
  if (options.capacityFixture) runtimeEnvironment = { ...runtimeEnvironment, MECHA_TEST_CLAUDE_CAPACITY_DIR: root };
  if (options.piReviewBarrier) runtimeEnvironment = { ...runtimeEnvironment, MECHA_PI_REVIEW_BARRIER: root };
  if (options.backgroundExpiryInterval) {
    runtimeEnvironment = { ...runtimeEnvironment, MECHA_TEST_BACKGROUND_EXPIRY_INTERVAL: options.backgroundExpiryInterval };
  }
  if (options.rootControlFixture) {
    if (!isManaged) throw new Error("Root control fixture requires managed mode");
    await writeFile(join(root, "managed-home/sessions/root-control-fixture"), options.repoRoot, { mode: 0o600 });
    const wrapper = join(root, "python-sentinel");
    await writeFile(wrapper, `#!/bin/sh\nprintf '%s\\n' "$*" >> '${root}/python-spawns.log'\nexec "$MECHA_TEST_PYTHON" "$@"\n`, { mode: 0o700 });
    runtimeEnvironment = { ...runtimeEnvironment,
      PYTHON: wrapper, MECHA_TEST_PYTHON: process.env.PYTHON || "python3", PYTHONDONTWRITEBYTECODE: "1", MECHA_SUPERVISOR_TOOLS: join(options.repoRoot, "tools"),
      MECHA_MANAGED_ROOT_CONTROL_HOME: join(root, "managed-home"), MECHA_TEST_ROOT_CONTROL: root,
      MECHA_TEST_ROOT_MANIFEST: join(options.repoRoot, "router/contract/managed-tools-v1.json") };
  }
  // Join the executable before reopening SQLite or removing its fixture.
  const stop = async () => { if (ownedChild) await stopRouterChild(ownedChild); };
  let readinessStderr = "";
  let capturedStderr = "";
  const launch = async () => {
    readinessStderr = "";
    const child = spawn(binaryPath, args, {
      cwd: options.repoRoot,
      env: runtimeEnvironment,
      stdio: ["ignore", "pipe", "pipe"],
    });

    ownedChild = child;
    options.onRouterSpawnFixture?.(child);
    let settled = false;
    let stdout = "";
    let stderr = "";

    await new Promise<void>((res, rej) => {
      const timeout = setTimeout(() => {
        if (!settled) {
          settled = true;
          child.kill("SIGQUIT");
          rej(new Error(`Isolated router timed out during startup.${readinessStderr ? `\n${readinessStderr}` : ""}`));
        }
      }, options.startupTimeoutFixtureMs ?? (options.embedded ? 45_000 : 15_000));

      const checkReady = (chunk: Buffer) => {
        const text = chunk.toString();
        stdout += text;
        const ready = stdout.includes("MECHA_V2_ROUTER_READY");
        stdout = stdout.slice(-128);
        if (ready) {
          if (!settled) {
            settled = true;
            clearTimeout(timeout);
            res();
          }
        }
      };

      child.stdout.on("data", checkReady);
      child.stderr.on("data", (chunk) => {
        stderr = (stderr + chunk.toString()).slice(-1024);
        capturedStderr = (capturedStderr + chunk.toString()).slice(-65536);
        // Closed vocabulary only: never expose arbitrary helper stderr, auth,
        // stack locals or credentials through readiness artifacts.
        if (stderr.includes("managed provider status is unavailable")) {
          readinessStderr = "managed provider status is unavailable";
        }
      });

      child.once("error", error => {
        if (!settled) { settled = true; clearTimeout(timeout); rej(error); }
      });
      child.once("exit", (code) => {
        if (!settled) {
          settled = true;
          clearTimeout(timeout);
          rej(new Error(`Isolated router exited prematurely (code ${code}).${readinessStderr ? ` ${readinessStderr}` : ""}`));
        }
      });
    });

  };
  await launch();

  const httpUrl = `http://127.0.0.1:${port}`;
  const websocketUrl = `ws://127.0.0.1:${port}/v1/agents`;

  const advanceClock = async (deltaMs: number): Promise<{ now: string; advanced_ms: number }> => {
    const res = await fetch(`${httpUrl}/v1/test/clock/advance`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ delta_ms: deltaMs }),
    });
    if (!res.ok) {
      const text = await res.text();
      throw new Error(`Failed to advance clock (${res.status}): ${text}`);
    }
    return res.json();
  };

  let closing: Promise<void> | undefined;
  return {
    root,
    databasePath,
    logPath,
    readinessStderr: () => readinessStderr,
    // Never return free-form process output; these fixed signatures are safe to attach.
    stderr: () => ["managed provider status is unavailable", "embedded head mismatch", "tool outcome UNKNOWN after execution", "event log write failed"]
      .filter(signature => capturedStderr.includes(signature)).join("\n"),
    countStderr: needle => needle ? capturedStderr.split(needle).length - 1 : 0,
    stderrLineContains: (...needles) => needles.length > 0 && capturedStderr.split("\n").some(line => needles.every(needle => needle !== "" && line.includes(needle))),
    port,
    httpUrl,
    websocketUrl,
    get process() { return ownedChild!; },
    restart: async (prepareDatabase) => {
      if (closing) throw new Error("Cannot restart a closed isolated router");
      await stop();
      // Upgrade fixtures may replace SQLite only after its owning process exits.
      if (prepareDatabase) await prepareDatabase(databasePath);
      try { await launch(); } catch (error) { await cleanupAll([stop], [error]); throw error; }
    },
    advanceClock,
    close: () => {
      closing ??= (async () => {
        await cleanupAll([async () => {
          const errors: unknown[] = [];
          try { await stop(); } catch (error) { errors.push(error); }
          await cleanupAll([() => rm(root, { recursive: true, force: true })], errors);
        }]);
      })();
      return closing;
    },
  };
  } catch (error) {
    const errors: unknown[] = [error];
    try {
      if (ownedChild) await stopRouterChild(ownedChild, "SIGKILL");
    } catch (cleanupError) { errors.push(cleanupError); }
    await cleanupAll([() => rm(root, { recursive: true, force: true })], errors);
    throw error;
  }
}

export async function startRouterProxy(options: {
  repoRoot: string;
  upstreamPort: number;
  buildTimeoutMs?: number;
  onSpawn?: (child: ChildProcess) => void;
  proxyExecutableFixture?: string;
  startupTimeoutFixtureMs?: number;
}): Promise<IsolatedProxy> {
  const root = await mkdtemp(join(tmpdir(), "mecha-e2e-proxy-"));
  let child: ChildProcess | undefined;
  const stop = async () => { if (child) await stopRouterChild(child); };
  const cleanup = async () => {
    const errors: unknown[] = [];
    try { await stop(); } catch (error) { errors.push(error); }
    await cleanupAll([() => rm(root, { recursive: true, force: true })], errors);
  };
  try {
    const binary = options.proxyExecutableFixture || join(root, "router-proxy");
    if (!options.proxyExecutableFixture) await buildGo(["build", "-o", binary, "./"], join(options.repoRoot, "router-proxy"), "Proxy", options.buildTimeoutMs ?? DEFAULT_BUILD_TIMEOUT_MS);
    const port = await allocatePort();
    child = spawn(binary, ["--upstream-host", "127.0.0.1", "--upstream-port", String(options.upstreamPort), "--listen-port", String(port), "--listen-host", "127.0.0.1"], { env: { HOME: root, TMPDIR: root, LANG: "C.UTF-8" }, stdio: ["ignore", "pipe", "pipe"] });
    const processChild = child;
    await new Promise<void>((res, rej) => {
      let stdout = "", settled = false;
      const finish = (error?: Error) => {
        if (settled) return;
        settled = true;
        clearTimeout(timeout);
        error ? rej(error) : res();
      };
      const timeout = setTimeout(() => finish(new Error("Router proxy timed out.")), options.startupTimeoutFixtureMs ?? 15_000);
      processChild.stdout!.on("data", chunk => { stdout += String(chunk); const ready = stdout.includes("MECHA_ROUTER_PROXY_READY"); stdout = stdout.slice(-128); if (ready) finish(); });
      processChild.stderr!.resume(); // Drain without exposing arbitrary process output.
      processChild.once("error", error => finish(error));
      processChild.once("exit", code => finish(new Error(`Router proxy exited (code ${code}).`)));
      options.onSpawn?.(processChild);
    });
    let closing: Promise<void> | undefined;
    return { port, url: `http://127.0.0.1:${port}`, process: processChild, close: () => closing ??= cleanup() };
  } catch (error) {
    await cleanupAll([cleanup], [error]);
    throw error;
  }
}

export async function serveFrontendDist(distDir: string): Promise<IsolatedFrontend> {
  const { createReadStream, existsSync, statSync } = await import("node:fs");
  const { join, extname } = await import("node:path");

  const mimeTypes: Record<string, string> = {
    ".html": "text/html",
    ".js": "application/javascript",
    ".css": "text/css",
    ".json": "application/json",
    ".png": "image/png",
    ".jpg": "image/jpeg",
    ".svg": "image/svg+xml",
    ".ico": "image/x-icon",
  };

  const port = await allocatePort();

  const srv = createServer((req, res) => {
    const urlPath = req.url?.split("?")[0] || "/";
    let filePath = join(distDir, urlPath);

    if (existsSync(filePath) && statSync(filePath).isDirectory()) {
      filePath = join(filePath, "index.html");
    }

    if (!existsSync(filePath)) {
      // SPA fallback
      filePath = join(distDir, "index.html");
    }

    const ext = extname(filePath).toLowerCase();
    const contentType = mimeTypes[ext] || "application/octet-stream";

    res.writeHead(200, {
      "Content-Type": contentType,
      "Access-Control-Allow-Origin": "*",
    });
    createReadStream(filePath).pipe(res);
  });

  await new Promise<void>((resolve, reject) => {
    srv.once("error", reject);
    srv.listen(port, "127.0.0.1", () => resolve());
  });

  return {
    port,
    url: `http://127.0.0.1:${port}`,
    server: srv,
    close: async () => {
      srv.closeAllConnections();
      await new Promise<void>((resolve) => srv.close(() => resolve()));
    },
  };
}

// Run every cleanup even if another throws synchronously or rejects.
async function cleanupAll(actions: Array<() => Promise<void>>, primaryErrors: unknown[] = []): Promise<void> {
  const results = await Promise.allSettled(actions.map(action => Promise.resolve().then(action)));
  const errors = [...primaryErrors, ...results.flatMap(result => result.status === "rejected" ? [result.reason] : [])];
  if (errors.length === 1) throw errors[0];
  if (errors.length > 1) throw new AggregateError(errors, "Harness setup or cleanup failed");
}

export async function setupE2EHarness(options?: {
  useProxy?: boolean;
  managed?: boolean;
  testClock?: boolean;
  codexCredentialPath?: string;
  backgroundExpiryInterval?: string;
}): Promise<E2ETestHarness> {
  const repoRoot = resolve(__dirname, "../..");
  const distDir = process.env.MECHA_E2E_FRONTEND_DIST ?? join(repoRoot, "web/router-ui-react/dist");

  // Ensure frontend build exists
  const { existsSync } = await import("node:fs");
  if (!existsSync(join(distDir, "index.html"))) {
    throw new Error("Frontend dist/index.html not found. Run 'npm --prefix web/router-ui-react run build' first.");
  }

  const started = await Promise.allSettled([
    startMockLLMServer(),
    startIsolatedRouter({ repoRoot, managed: options?.managed ?? false, testClock: options?.testClock, codexCredentialPath: options?.codexCredentialPath, backgroundExpiryInterval: options?.backgroundExpiryInterval }),
    serveFrontendDist(distDir),
  ] as const);
  const [mockResult, routerResult, frontendResult] = started;
  if (mockResult.status === "rejected" || routerResult.status === "rejected" || frontendResult.status === "rejected") {
    await cleanupAll(
      started.flatMap(result => result.status === "fulfilled" ? [() => result.value.close()] : []),
      started.flatMap(result => result.status === "rejected" ? [result.reason] : []),
    );
    throw new Error("Harness setup failed");
  }
  const mockLLM = mockResult.value;
  const router = routerResult.value;
  const frontend = frontendResult.value;
  let proxy: IsolatedProxy | undefined;
  try {
    if (options?.useProxy) {
      proxy = await startRouterProxy({ repoRoot, upstreamPort: router.port });
    }

    return {
      root: router.root,
      router,
      proxy,
      mockLLM,
      frontend,
      advanceClock: (deltaMs: number) => router.advanceClock(deltaMs),
      close: async () => {
        await cleanupAll([
          ...(proxy ? [() => proxy!.close()] : []), () => router.close(), () => mockLLM.close(), () => frontend.close(),
        ]);
      },
    };
  } catch (error) {
    await cleanupAll([() => router.close(), () => mockLLM.close(), () => frontend.close()], [error]);
    throw error;
  }
}

// Real embedded provider boundary shared by the session/goal regression slices.
// Request bodies are bounded and discarded on close; credentials are not retained.
export async function startEmbeddedSafetyFixture() {
  const { DatabaseSync } = await import("node:sqlite");
  const { execFileSync } = await import("node:child_process");
  const repoRoot = resolve(__dirname, "../..");
  type Reply = { text: string } | { tool: string; args: object } | { failure: true };
  let reply: Reply = { text: "startup settled" };
  let gate: (() => void) | undefined;
  let hold = false;
  let requests = 0;
  const server = createServer((req, res) => {
    let body = "";
    req.on("data", chunk => {
      body += chunk;
      if (Buffer.byteLength(body) > 2 * 1024 * 1024) req.destroy();
    });
    req.on("end", () => {
      if (req.method !== "POST" || req.url !== "/codex/responses") { res.writeHead(400).end(); return; }
      const parsed = JSON.parse(body);
      requests++;
      const selected = reply;
      const send = () => {
        if ("failure" in selected) {
          res.writeHead(403, { "Content-Type": "application/json" });
          res.end(JSON.stringify({ error: { message: "deterministic safety backend failure" } })); return;
        }
        const item = "tool" in selected
          ? { type: "function_call", id: `fc_${requests}`, call_id: `call_${requests}`, name: selected.tool, arguments: JSON.stringify(selected.args) }
          : { type: "message", id: `msg_${requests}`, role: "assistant", status: "completed", content: [{ type: "output_text", text: selected.text }] };
        const events: object[] = [{ type: "response.created", response: { id: `resp_${requests}` } },
          { type: "response.output_item.added", output_index: 0, item: "tool" in selected ? { ...item, arguments: "" } : { ...item, content: [] } }];
        if ("text" in selected) events.push(
          { type: "response.content_part.added", output_index: 0, content_index: 0, part: { type: "output_text", text: "" } },
          { type: "response.output_text.delta", output_index: 0, content_index: 0, delta: selected.text },
          { type: "response.output_text.done", output_index: 0, content_index: 0, text: selected.text });
        events.push({ type: "response.output_item.done", output_index: 0, item },
          { type: "response.completed", response: { id: `resp_${requests}`, status: "completed" } });
        res.writeHead(200, { "Content-Type": "text/event-stream" });
        res.end(events.map(event => `data: ${JSON.stringify(event)}\n\n`).join(""));
      };
      if (hold) { hold = false; gate = send; } else send();
    });
  });
  await new Promise<void>(r => server.listen(0, "127.0.0.1", r));
  const address = server.address();
  if (!address || typeof address === "string") throw Error("No embedded fixture port");
  let router: IsolatedRouter | undefined;
  let proxy: IsolatedProxy | undefined;
  let frontend: IsolatedFrontend | undefined;
  const close = async () => {
    gate?.(); gate = undefined;
    if (proxy?.process.pid && proxy.process.exitCode === null) {
      try {
        const children = execFileSync("ps", ["-o", "pid=", "--ppid", String(proxy.process.pid)], { encoding: "utf8" });
        for (const pid of children.trim().split(/\s+/).filter(Boolean)) {
          try { process.kill(Number(pid), "SIGTERM"); } catch (error) { if (error.code !== "ESRCH") throw error; }
        }
      } catch (error) { if (error.status !== 1) throw error; }
    }
    await Promise.allSettled([proxy?.close(), router?.close(), frontend?.close()]);
    server.closeAllConnections();
    await new Promise<void>(r => server.close(() => r()));
  };
  try {
    router = await startIsolatedRouter({ repoRoot, managed: true, embedded: { endpoint: `http://127.0.0.1:${address.port}` } });
    proxy = await startRouterProxy({ repoRoot, upstreamPort: router.port });
    frontend = await serveFrontendDist(process.env.MECHA_E2E_FRONTEND_DIST ?? join(repoRoot, "web/router-ui-react/dist"));

    const db = new DatabaseSync(router.databasePath);
    db.exec("PRAGMA busy_timeout=5000");
    const url = proxy.url;
    return {
      router, proxy, frontend, db,
      get diagnostics() { return router!.stderr(); },
      get requests() { return requests; },
      setReply(value: Reply, held = false) { reply = value; hold = held; },
      release() { const send = gate; gate = undefined; send?.(); },
      async api(path: string, body?: object, method = "POST") {
        const response = await fetch(url + path, body === undefined ? undefined : {
          method, headers: { "Content-Type": "application/json", "X-Request-ID": crypto.randomUUID() }, body: JSON.stringify(body),
        });
        return { status: response.status, body: await response.json() };
      },
      async close() { db.close(); await close(); },
    };
  } catch (error) { await close(); throw error; }
}
