import { chmod, copyFile, mkdir, readFile, rm } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { dirname, join, resolve } from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import process from "node:process";
import {
  REPOSITORY_ROOT,
  allocateLoopbackPort,
  assertPrivatePath,
  createTaskRoot,
  spawnRouterProcess,
  stopRouterProcess,
  verifyRouterReadiness,
} from "../support/isolated-go-router.mjs";

const execFileAsync = promisify(execFile);
const FIXTURES = resolve(dirname(fileURLToPath(import.meta.url)), "fixtures");

async function copyExecutable(source, destination) {
  await copyFile(source, destination);
  await chmod(destination, 0o700);
}

export async function startManagedGoRouter({ repositoryRoot = REPOSITORY_ROOT, timeoutMs = 30_000 } = {}) {
  const root = await createTaskRoot("webui-functional-router");
  const databasePath = join(root, "router.db");
  const logPath = join(root, "router.jsonl");
  const binaryPath = join(root, "mailbox-router");
  const piPath = join(root, "fake-managed-pi.mjs");
  const authPath = resolve(FIXTURES, "fake-provider-auth.mjs");
  const home = join(root, "managed-home");
  const piAgentDir = join(home, ".pi", "agent");
  const sessionRoot = join(home, "sessions");
  const cwdRoot = join(root, "work");
  await mkdir(piAgentDir, { recursive: true, mode: 0o700 });
  await mkdir(sessionRoot, { recursive: true, mode: 0o700 });
  await mkdir(cwdRoot, { recursive: true, mode: 0o700 });
  await copyExecutable(resolve(FIXTURES, "fake-managed-pi.mjs"), piPath);
  const port = await allocateLoopbackPort();
  let processInfo;
  try {
    const configuredBinary = process.env.MECHA_GO_ROUTER_BINARY;
    if (configuredBinary === undefined) {
      await execFileAsync("go", ["build", "-buildvcs=false", "-o", binaryPath, "./cmd/mailbox-router"], { cwd: join(repositoryRoot, "router-proxy"), timeout: timeoutMs });
    } else {
      await copyExecutable(resolve(configuredBinary), binaryPath);
    }
    const args = [
      "--database", databasePath, "--log", logPath, "--host", "127.0.0.1", "--port", String(port), "--managed=true",
      "--managed-pi", piPath, "--managed-home", home, "--managed-pi-agent-dir", piAgentDir,
      "--managed-session-root", sessionRoot, "--managed-manifest", join(repositoryRoot, "router", "contract", "managed-tools-v1.json"),
      "--managed-mecha-rpc", join(repositoryRoot, "pi-extensions", "mecha-rpc", "index.ts"),
      "--managed-pi-subagents", join(resolve(repositoryRoot, ".."), "pi-packages", "packages", "pi-subagents", "src", "index.ts"),
      "--managed-auth-helper", authPath, "--managed-runtime-root", repositoryRoot,
      "--managed-cwd-roots", root, "--managed-quiet-recipient", "functional-supervisor",
    ];
    processInfo = await spawnRouterProcess({ command: binaryPath, args, cwd: repositoryRoot, root, expectedPort: port, timeoutMs });
    const verified = await verifyRouterReadiness(processInfo.httpUrl);
    await assertPrivatePath(root, "functional router root");
    await assertPrivatePath(databasePath, "functional router database");
    await assertPrivatePath(logPath, "functional router log");
    let current = processInfo;
    let closed = false;
    const runtime = {
      ...processInfo,
      rootPath: root,
      databasePath,
      logPath,
      attachmentsPath: `${databasePath}.attachments`,
      home,
      piAgentDir,
      sessionRoot,
      cwdRoot,
      health: verified.health,
      capabilities: verified.capabilities,
      async restart() {
        if (closed) throw new Error("cannot restart a closed functional router");
        await stopRouterProcess(current);
        current = await spawnRouterProcess({ command: binaryPath, args, cwd: repositoryRoot, root, expectedPort: port, timeoutMs });
        const restarted = await verifyRouterReadiness(current.httpUrl);
        Object.assign(runtime, current, restarted, { rootPath: root, databasePath, logPath, attachmentsPath: `${databasePath}.attachments`, home, piAgentDir, sessionRoot, cwdRoot, health: restarted.health, capabilities: restarted.capabilities });
      },
      async close() {
        if (closed) return { alreadyClosed: true };
        closed = true;
        const stop = await stopRouterProcess(current);
        const logText = await readFile(logPath, "utf8").catch(() => "");
        const shutdownError = logText.includes('"event":"router_stopped"') ? undefined : new Error("functional Go router log lacks router_stopped");
        await rm(root, { recursive: true, force: true });
        if (shutdownError !== undefined) throw shutdownError;
        return { stop, logText, rootRemoved: true };
      },
    };
    return runtime;
  } catch (error) {
    if (processInfo !== undefined) await stopRouterProcess(processInfo).catch(() => undefined);
    await rm(root, { recursive: true, force: true });
    throw error;
  }
}
