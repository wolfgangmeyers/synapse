import { createServer, type Server } from "node:http";
import { createServer as createNetServer } from "node:net";
import { spawn, type ChildProcess } from "node:child_process";
import { mkdtemp, chmod, rm, writeFile, mkdir } from "node:fs/promises";
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
  port: number;
  httpUrl: string;
  websocketUrl: string;
  process: ChildProcess;
  close: () => Promise<void>;
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

export async function startIsolatedRouter(options: {
  repoRoot: string;
  managed?: boolean;
  testClock?: boolean;
  embedded?: { endpoint: string; signedOut?: boolean };
  codexCredentialPath?: string;
}): Promise<IsolatedRouter> {
  if (options.embedded && !options.managed) throw new Error("Embedded E2E requires managed mode");
  const root = await mkdtemp(join(tmpdir(), "mecha-e2e-"));
  let ownedChild: ChildProcess | undefined;
  try {
  await chmod(root, 0o700);

  const databasePath = join(root, "router.sqlite");
  const logPath = join(root, "router.jsonl");
  const port = await allocatePort();

  const routerProxyDir = join(options.repoRoot, "router-proxy");
  const binaryPath = join(root, "mailbox-router");

  // Compile router binary into the temporary directory for isolation
  await new Promise<void>((res, rej) => {
    const cp = spawn("go", ["build", ...(options.embedded ? ["-tags=embedded_restart_test_only"] : []), "-o", binaryPath, "./cmd/mailbox-router"], {
      cwd: routerProxyDir,
      stdio: "pipe",
    });
    cp.on("close", (code) => {
      if (code === 0) res();
      else rej(new Error(`Failed to compile mailbox-router, code ${code}`));
    });
  });

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
      "--managed-mecha-rpc", join(options.repoRoot, "pi-extensions", "mecha-rpc", "index.ts"),
      "--managed-pi-subagents", join(resolve(options.repoRoot, ".."), "pi-packages", "packages", "pi-subagents", "src", "index.ts"),
      "--managed-auth-helper", authPath,
      "--managed-runtime-root", options.repoRoot,
      "--managed-cwd-roots", root,
      "--managed-quiet-recipient", "functional-supervisor"
    );
  }

  if (options.embedded) {
    const privateDir = join(root, "private");
    await mkdir(privateDir, { mode: 0o700 });
    if (!options.embedded.signedOut) {
      const claims = Buffer.from(JSON.stringify({ "https://api.openai.com/auth": { chatgpt_account_id: "terra-test-account" } })).toString("base64url");
      await writeFile(join(privateDir, "codex.json"), JSON.stringify({ access_token: `fake.${claims}.terra-test-secret`, refresh_token: "terra-refresh-secret", account_id: "terra-test-account", expires_at: "2099-01-01T00:00:00Z" }), { mode: 0o600 });
    }
    // Real helper and owned credential projection, never the generic fake auth helper.
    const helperIndex = args.indexOf("--managed-auth-helper");
    args[helperIndex + 1] = join(options.repoRoot, "auth-helper/index.mjs");
    args.push("--codex-credential-path", join(privateDir, "codex.json"));
    await writeFile(join(root, "managed-home/.pi/agent/auth.json"), JSON.stringify({ google: { type: "api_key", key: "other-provider-test-secret" } }), { mode: 0o600 });
    await writeFile(join(root, "work/note.txt"), "terra harmless tool evidence", { mode: 0o600 });
  }
  if (options.codexCredentialPath) args.push("--codex-credential-path", options.codexCredentialPath);

  // Keep build/toolchain settings above; allow only disposable runtime config here.
  let runtimeEnvironment = process.env;
  if (options.embedded || options.codexCredentialPath) {
    for (const directory of ["config", "cache", "tmp"]) await mkdir(join(root, directory), { mode: 0o700 });
    runtimeEnvironment = {
      PATH: process.env.PATH, LANG: "C.UTF-8", HOME: join(root, "managed-home"),
      XDG_CONFIG_HOME: join(root, "config"), XDG_CACHE_HOME: join(root, "cache"), TMPDIR: join(root, "tmp"),
      ...(options.embedded ? { EMBEDDED_RESTART_FAKE_ENABLE: "1", EMBEDDED_RESTART_FAKE_ENDPOINT: options.embedded.endpoint } : {}),
    };
  }
  const child = spawn(binaryPath, args, {
    cwd: options.repoRoot,
    env: runtimeEnvironment,
    stdio: ["ignore", "pipe", "pipe"],
  });

  ownedChild = child;
  let settled = false;
  let stdout = "";
  let stderr = "";

  await new Promise<void>((res, rej) => {
    const timeout = setTimeout(() => {
      if (!settled) {
        settled = true;
        child.kill("SIGKILL");
        rej(new Error(`Isolated router timed out during startup.\nStdout: ${stdout}\nStderr: ${stderr}`));
      }
    }, 15_000);

    const checkReady = (chunk: Buffer) => {
      const text = chunk.toString();
      stdout += text;
      if (text.includes("MECHA_V2_ROUTER_READY")) {
        if (!settled) {
          settled = true;
          clearTimeout(timeout);
          res();
        }
      }
    };

    child.stdout.on("data", checkReady);
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });

    child.once("close", (code) => {
      if (!settled) {
        settled = true;
        clearTimeout(timeout);
        rej(new Error(`Isolated router exited prematurely (code ${code}).\nStderr: ${stderr}`));
      }
    });
  });

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

  return {
    root,
    databasePath,
    logPath,
    port,
    httpUrl,
    websocketUrl,
    process: child,
    advanceClock,
    close: async () => {
      if (child.exitCode === null) {
        child.kill("SIGTERM");
        await new Promise((r) => setTimeout(r, 200));
        if (child.exitCode === null) child.kill("SIGKILL");
      }
      await rm(root, { recursive: true, force: true }).catch(() => {});
    },
  };
  } catch (error) {
    if (ownedChild && ownedChild.exitCode === null && ownedChild.signalCode === null) {
      const closed = new Promise<void>(resolve => ownedChild!.once("close", () => resolve()));
      ownedChild.kill("SIGKILL");
      await closed;
    }
    await rm(root, { recursive: true, force: true });
    throw error;
  }
}

export async function startRouterProxy(options: {
  repoRoot: string;
  upstreamPort: number;
}): Promise<IsolatedProxy> {
  const port = await allocatePort();
  const routerProxyDir = join(options.repoRoot, "router-proxy");

  const child = spawn(
    "go",
    [
      "run",
      "./",
      "--upstream-host",
      "127.0.0.1",
      "--upstream-port",
      String(options.upstreamPort),
      "--listen-port",
      String(port),
      "--listen-host",
      "127.0.0.1",
    ],
    {
      cwd: routerProxyDir,
      stdio: ["ignore", "pipe", "pipe"],
    }
  );

  let settled = false;
  let stdout = "";
  let stderr = "";

  await new Promise<void>((res, rej) => {
    const timeout = setTimeout(() => {
      if (!settled) {
        settled = true;
        child.kill("SIGKILL");
        rej(new Error(`Router proxy timed out.\nStdout: ${stdout}\nStderr: ${stderr}`));
      }
    }, 15_000);

    const checkReady = (chunk: Buffer) => {
      const text = chunk.toString();
      stdout += text;
      if (text.includes("MECHA_ROUTER_PROXY_READY")) {
        if (!settled) {
          settled = true;
          clearTimeout(timeout);
          res();
        }
      }
    };

    child.stdout.on("data", checkReady);
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });

    child.once("close", (code) => {
      if (!settled) {
        settled = true;
        clearTimeout(timeout);
        rej(new Error(`Router proxy exited (code ${code}).\nStderr: ${stderr}`));
      }
    });
  });

  return {
    port,
    url: `http://127.0.0.1:${port}`,
    process: child,
    close: async () => {
      if (child.exitCode === null) {
        child.kill("SIGTERM");
        await new Promise((r) => setTimeout(r, 200));
        if (child.exitCode === null) child.kill("SIGKILL");
      }
    },
  };
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

export async function setupE2EHarness(options?: {
  useProxy?: boolean;
  managed?: boolean;
  testClock?: boolean;
  codexCredentialPath?: string;
}): Promise<E2ETestHarness> {
  const repoRoot = resolve(__dirname, "../..");
  const distDir = join(repoRoot, "web/router-ui-react/dist");

  // Ensure frontend build exists
  const { existsSync } = await import("node:fs");
  if (!existsSync(join(distDir, "index.html"))) {
    throw new Error("Frontend dist/index.html not found. Run 'npm --prefix web/router-ui-react run build' first.");
  }

  const [mockLLM, router, frontend] = await Promise.all([
    startMockLLMServer(),
    startIsolatedRouter({ repoRoot, managed: options?.managed ?? false, testClock: options?.testClock, codexCredentialPath: options?.codexCredentialPath }),
    serveFrontendDist(distDir),
  ]);

  let proxy: IsolatedProxy | undefined;
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
      await Promise.allSettled([
        proxy ? proxy.close() : Promise.resolve(),
        router.close(),
        mockLLM.close(),
        frontend.close(),
      ]);
    },
  };
}
