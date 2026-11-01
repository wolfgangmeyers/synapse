import { test, expect } from "@playwright/test";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, writeFile, readFile, copyFile, chmod, stat, symlink, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const exec = promisify(execFile);
const repo = resolve(import.meta.dirname, "../..");
const cleanup: string[] = [];

test.afterEach(async () => {
  await Promise.all(cleanup.splice(0).map(path => rm(path, { recursive: true, force: true })));
});

async function fixture() {
  const root = await mkdtemp(join(tmpdir(), "mecha-initialize-"));
  cleanup.push(root);
  const checkout = join(root, "projects", "mecha-developer");
  const home = join(root, "home");
  const bin = join(root, "bin");
  const log = join(root, "commands.jsonl");
  await mkdir(join(checkout, "tools"), { recursive: true });
  await mkdir(home);
  await chmod(home, 0o750);
  await mkdir(bin);
  await copyFile(join(repo, "tools/initialize.sh"), join(checkout, "tools/initialize.sh"));
  for (const name of ["bash", "dirname", "mkdir", "chmod"]) {
    await symlink(`/bin/${name}`, join(bin, name));
  }
  const mock = join(bin, "mock");
  await writeFile(mock, `#!${process.execPath}
const fs = require('node:fs');
const path = require('node:path');
const cp = require('node:child_process');
const name = path.basename(process.argv[1]);
const args = process.argv.slice(2);
if (name === 'node') {
  if (args[0] === '--version') { console.log(process.env.TEST_NODE_VERSION || process.version); process.exit(0); }
  const result = cp.spawnSync(${JSON.stringify(process.execPath)}, args, {stdio:'inherit'});
  process.exit(result.status ?? 1);
}
fs.appendFileSync(process.env.TEST_LOG, JSON.stringify([name,...args])+'\\n');
if ([name,...args].join(' ').includes(process.env.TEST_FAIL || '\\u0000')) {
  console.error('deterministic install failure'); process.exit(42);
}
if (name === 'git' && args.includes('clone')) {
  const dest = args.at(-1);
  fs.mkdirSync(path.join(dest,'.git'),{recursive:true});
  fs.mkdirSync(path.join(dest,'packages/pi-subagents/src'),{recursive:true});
  fs.writeFileSync(path.join(dest,'packages/pi-subagents/src/index.ts'),'// local fork fixture');
  fs.writeFileSync(path.join(dest,'package.json'),JSON.stringify({packageManager:'pnpm@11.5.2'}));
}
if (name === 'npm' && args.includes('-g') && args.includes('@earendil-works/pi-coding-agent@latest')) {
  fs.symlinkSync('mock',path.join(process.env.TEST_BIN,'pi'));
}
`);
  await chmod(mock, 0o700);
  for (const name of ["git", "node", "npm", "go", "make"]) await symlink("mock", join(bin, name));
  const env = { ...process.env, HOME: home, PATH: bin, TEST_BIN: bin, TEST_LOG: log };
  return {
    root, checkout, home, bin,
    fork: join(root, "projects", "pi-packages"),
    run: async (extra: Record<string, string> = {}) => {
      try {
        const result = await exec("/bin/bash", [join(checkout, "tools/initialize.sh")], { cwd: root, env: { ...env, ...extra } });
        return { code: 0, output: result.stdout + result.stderr };
      } catch (error) {
        const failure = error as Error & { code: number; stdout: string; stderr: string };
        return { code: failure.code, output: failure.stdout + failure.stderr };
      }
    },
    commands: async (): Promise<string[][]> => {
      try { return (await readFile(log, "utf8")).trim().split("\n").filter(Boolean).map(line => JSON.parse(line)); }
      catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return []; throw error; }
    },
  };
}

test("initializes a fresh checkout from another working directory", async () => {
  const f = await fixture();
  const result = await f.run();
  expect(result, result.output).toMatchObject({ code: 0 });
  const commands = await f.commands();
  expect(commands).toContainEqual(["npm", "install", "-g", "@earendil-works/pi-coding-agent@latest"]);
  expect(commands.find(args => args[0] === "git" && args.includes("clone"))).toEqual([
    "git", "clone", "--branch", "main", "https://github.com/wolfgangmeyers/pi-packages.git", f.fork,
  ]);
  expect(commands.some(args => args.includes("pnpm@11.5.2") && args.includes("--frozen-lockfile"))).toBe(true);
  for (const directory of ["", "auth-helper", "pi-extensions/mecha-rpc", "web/router-ui-react"]) {
    expect(commands).toContainEqual(["npm", "--prefix", join(f.checkout, directory), "ci"]);
  }
  expect(commands).toContainEqual(["npm", "--prefix", join(f.checkout, "web/router-ui-react"), "run", "build"]);
  expect(commands).toContainEqual(["make", "-C", join(f.checkout, "router-proxy"), "build-router-local"]);
  for (const directory of [".mecha-developer", ".mecha-developer/managed-pi", ".mecha-developer/managed-pi/sessions", ".pi/agent"]) {
    expect((await stat(join(f.home, directory))).mode & 0o777).toBe(0o700);
  }
  expect(result.output).toContain("MECHA_ROUTER_MANAGED_PI_SUBAGENTS=");
  expect(result.output).toContain("tools/run_router.sh");
  expect(result.output).toContain("tools/run_webui.sh");
  expect(result.output).toMatch(/sign in/i);
});

test("preserves existing state and fork edits on rerun", async () => {
  const f = await fixture();
  expect((await f.run()).code).toBe(0);
  const credential = join(f.home, ".mecha-developer/codex-credentials.json");
  const forkEntry = join(f.fork, "packages/pi-subagents/src/index.ts");
  await writeFile(credential, "existing credential contents", { mode: 0o600 });
  await writeFile(forkEntry, "// local uncommitted edits");
  await chmod(join(f.home, ".mecha-developer"), 0o775);
  const firstCount = (await f.commands()).length;
  const result = await f.run();
  expect(result, result.output).toMatchObject({ code: 0 });
  expect(await readFile(credential, "utf8")).toBe("existing credential contents");
  expect(await readFile(forkEntry, "utf8")).toBe("// local uncommitted edits");
  expect((await stat(join(f.home, ".mecha-developer"))).mode & 0o777).toBe(0o700);
  expect((await stat(f.home)).mode & 0o777).toBe(0o750);
  const rerun = (await f.commands()).slice(firstCount);
  expect(rerun.some(args => args[0] === "git")).toBe(false);
  expect(rerun.some(args => args.includes("-g"))).toBe(false);
});

test("reports prerequisite errors before installing", async () => {
  for (const name of ["git", "node", "npm", "go", "make"]) {
    const f = await fixture();
    await rm(join(f.bin, name));
    const result = await f.run();
    expect(result.code, result.output).not.toBe(0);
    expect(result.output).toContain(name);
    expect(await f.commands()).toEqual([]);
    await expect(stat(join(f.home, ".mecha-developer"))).rejects.toThrow();
  }
  const f = await fixture();
  const oldNode = await f.run({ TEST_NODE_VERSION: "v22.18.0" });
  expect(oldNode.code).not.toBe(0);
  expect(oldNode.output).toContain("22.19");
  expect(await f.commands()).toEqual([]);
});

test("stops at the failing install or build step", async () => {
  for (const failure of ["npm install -g", "ci", "run build", "build-router-local"]) {
    const f = await fixture();
    const result = await f.run({ TEST_FAIL: failure });
    expect(result.code).toBe(42);
    expect(result.output).toContain("deterministic install failure");
    expect(result.output).toMatch(/failed/i);
    expect(result.output).not.toContain("Initialization complete");
    const commands = await f.commands();
    expect(commands.at(-1)?.join(" ")).toContain(failure);
  }
});

test("preserves an invalid existing fork path", async () => {
  const f = await fixture();
  await mkdir(f.fork);
  await writeFile(join(f.fork, "keep.txt"), "unrelated work");
  const result = await f.run();
  expect(result.code).not.toBe(0);
  expect(result.output).toContain(f.fork);
  expect(await readFile(join(f.fork, "keep.txt"), "utf8")).toBe("unrelated work");
  expect((await f.commands()).some(args => args[0] === "git")).toBe(false);
});

// These tests use a real managed router, SQLite, proxy, browser REST and
// WebSocket transports. No model is prompted and provider discovery is offline.
import { setupE2EHarness, startIsolatedRouter } from "./harness";

for (const mode of [0o750, 0o755, 0o775]) {
  test(`starts with ordinary credential-directory permissions ${mode.toString(8)}`, async ({ page }) => {
    const f = await fixture();
    expect((await f.run()).code).toBe(0);
    const credentialDirectory = join(f.home, ".mecha-developer");
    await chmod(credentialDirectory, mode);
    const credentialPath = join(credentialDirectory, "codex-credentials.json");
    const harness = await setupE2EHarness({ managed: true, useProxy: true, codexCredentialPath: credentialPath });
    try {
      const routerURL = harness.proxy!.url;
      await page.goto(harness.frontend.url);
      const observed = await page.evaluate(async ({ routerURL }) => {
        const health = await fetch(`${routerURL}/v1/health`, { credentials: "omit" }).then(r => r.json());
        const auth = await fetch(`${routerURL}/v1/provider-auth`, { credentials: "omit" }).then(r => r.json());
        const socket = await new Promise<string>((resolve, reject) => {
          const ws = new WebSocket(`${routerURL.replace(/^http/, "ws")}/v1/agents`);
          const timer = setTimeout(() => { ws.close(); reject(new Error("registration timed out")); }, 5000);
          ws.onerror = () => { clearTimeout(timer); reject(new Error("WebSocket failed")); };
          ws.onopen = () => ws.send(JSON.stringify({
            version: "v1", frame_id: "init-register", type: "register", agent_id: "init-test", instance_id: "init-test-instance",
            agent_role: "agent", client_type: "test", ack_confirmation: "correlated_v1", conversation_sync: "v1", delivery_receipts: "delivery_items_v1",
          }));
          ws.onmessage = event => {
            const frame = JSON.parse(event.data);
            if (frame.type === "registered") { clearTimeout(timer); ws.close(); resolve(frame.type); }
          };
        });
        return { health, auth, socket };
      }, { routerURL });
      expect(observed.health.readiness).toBe("ok");
      expect(observed.socket).toBe("registered");
      expect(observed.auth.providers.find((p: { provider: string }) => p.provider === "openai-codex").authenticated).toBe(false);
      expect((await readFile(harness.router.databasePath)).subarray(0, 16).toString()).toBe("SQLite format 3\0");
      expect((await stat(credentialDirectory)).mode & 0o777).toBe(mode);
      await expect(stat(credentialPath)).rejects.toThrow();
      expect((await stat(`${credentialPath}.lock`)).mode & 0o777).toBe(0o600);
    } finally {
      await harness.close();
    }
  });
}

test("rejects unsafe credential files before binding", async () => {
  const directory = await mkdtemp(join(tmpdir(), "mecha-unsafe-credential-"));
  cleanup.push(directory);
  await chmod(directory, 0o755);
  const credential = join(directory, "credential.json");
  await writeFile(credential, "{}", { mode: 0o644 });
  await expect(startIsolatedRouter({ repoRoot: repo, managed: true, codexCredentialPath: credential })).rejects.toThrow(/credential file/);
  const link = join(directory, "linked.json");
  await symlink(credential, link);
  await expect(startIsolatedRouter({ repoRoot: repo, managed: true, codexCredentialPath: link })).rejects.toThrow(/credential/);
});
