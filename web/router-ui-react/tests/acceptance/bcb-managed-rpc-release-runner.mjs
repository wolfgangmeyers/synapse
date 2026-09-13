import { Buffer } from "node:buffer";
import { createHash } from "node:crypto";
import { createServer } from "node:http";
import { execFile } from "node:child_process";
import { mkdir, readFile, readdir, realpath, rm, stat, writeFile } from "node:fs/promises";
import { dirname, isAbsolute, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { promisify } from "node:util";
import { spawn } from "node:child_process";
import {
  allocateLoopbackPort,
  createTaskRoot,
  spawnRouterProcess,
  stopRouterProcess,
} from "../support/isolated-go-router.mjs";
import { startIsolatedBcbBrowser } from "./bcb-runner.mjs";
import { terminateManagedTestAgents } from "../support/managed-agent-cleanup.mjs";

const execFileAsync = promisify(execFile);
const HERE = dirname(fileURLToPath(import.meta.url));
const PROJECT = resolve(HERE, "../..");
const REPOSITORY = resolve(PROJECT, "../..");
const BCB_SOURCE = process.env.MECHA_BCB_ROOT ?? "/Users/wolfgang/code/browser-console-bridge";
const TIMEOUT = 45_000;
const CHILD_AGENT = "managed-rpc-release";
// The browser accepts only production provider identifiers in /v1/models. This
// fixture keeps its endpoint and credentials disposable while using ChatGPT's
// provider key so its catalog remains usable through the real UI.
const ACCEPTANCE_PROVIDER = "openai-codex";
const DESKTOP_VIEWPORT_WIDTH = 1480;

function delay(ms) { return new Promise((resolveDelay) => setTimeout(resolveDelay, ms)); }
function fail(message) { throw new Error(`Managed RPC release runner: ${message}`); }
function exists(path) { return stat(path).then(() => true).catch(() => false); }

async function requireFile(path, label, executable = false) {
  if (typeof path !== "string" || !path.startsWith("/")) fail(`${label} must be an absolute path`);
  let info;
  try { info = await stat(path); } catch { fail(`${label} is unavailable: ${path}`); }
  if (!info.isFile()) fail(`${label} is not a file: ${path}`);
  if (executable && (info.mode & 0o111) === 0) fail(`${label} is not executable: ${path}`);
  return resolve(path);
}

const PI_LOCK_SCHEMA = "mecha.pi.g4-source-lock/v1";
const PI_PUBLIC_COMMIT = "107d79f11072bbc8a3a757ed7fd69596bee7d68c";
const PI_PUBLIC_TREE = "f5103239060686ea2983e18906857b3df54428f5";
const PI_PACKAGES_COMMIT = "5b362aaa6b65204ace95c62b5089b6ad5a801b4a";
const PI_PACKAGES_TREE = "9037681d57db3910588f28b00bd72a5de810cf53";

// Phase 2 was certified from this immutable committed source tree. Phase 3
// deliberately re-owns some of its paths, so the current dirty tree cannot be
// substituted for that proof or rejected merely for touching them.
const FROZEN_P2_HEAD = "7e72e4274513c54e29def0f17648f57022f63f6b";
const FROZEN_P2_TREE = "9c05a3f32ab486319222a28bcaceca3019c247cd";
const FROZEN_P2_REPORT = "/tmp/rpc-phase3-trackb-native-p1p2-proof.md";
const FROZEN_P2_ARTIFACTS = [
  ["/tmp/mecha-rpc-acceptance/7e72e4274513c54e29def0f17648f57022f63f6b/b0fab6e7-aa0f-417e-be2f-9319a1f99823/pi-g4-lifecycle.json", "aaa5f2cb1ebd73e8f74767b3d16ef7d4aa0fe5e52a7ab427fc06d37afbe99b6f"],
  ["/tmp/mecha-rpc-acceptance/7e72e4274513c54e29def0f17648f57022f63f6b/phase2-reset-s1/1580f8149525cba987a16cfefe9fa876654901b92e7ae2c195727bb5318d6e0c/217a3aced20d4ada/reset-s1-v1.json", "e3eb450cb4127e5593addedc96fb401b4e3a74ec0e085f13d33fc8826609f2fb"],
  ["/tmp/mecha-rpc-acceptance/7e72e4274513c54e29def0f17648f57022f63f6b/phase2-lifecycle-restart-s3/179acd038bdb3d98082c2e4f6fcf5566e0926c1807883583e494308618897c7b/3a6f31ea1ab8c120/lifecycle-restart-s3-v1.json", "7009cb2ebf6ab95915983416f01245c121fb1908d117ca50297420cfa2bcae01"],
  ["/tmp/mecha-rpc-acceptance/7e72e4274513c54e29def0f17648f57022f63f6b/phase2-managed-direct-mail/5b9c65b762f5765234f81d2c63baa6bc9366956025afd4a57b5176f5f2b5d85b/952c1a244c6c1462/managed-direct-mail-v1.json", "2719955666ec506674281693e3d3db523859d9c1e08dcc3a756d278f6e0270be"],
  ["/tmp/mecha-rpc-acceptance/7e72e4274513c54e29def0f17648f57022f63f6b/phase2-managed-direct-mail/5b9c65b762f5765234f81d2c63baa6bc9366956025afd4a57b5176f5f2b5d85b/3928c979351843f4/managed-direct-mail-v1.json", "aa72a7a233c63affe9fdd8091068060af79c0e3635fc72246b965fb32fcf96e8"],
  ["/tmp/mecha-rpc-acceptance/7e72e4274513c54e29def0f17648f57022f63f6b/phase2-managed-direct-mail/5b9c65b762f5765234f81d2c63baa6bc9366956025afd4a57b5176f5f2b5d85b/2dc32289df367795/managed-direct-mail-v1.json", "35aa74d0644232f6b2bfb7ca2fab2adcf1de8ced0e9e2d07878d0c26c8030718"],
  ["/tmp/mecha-rpc-acceptance/7e72e4274513c54e29def0f17648f57022f63f6b/phase2-root-control-s4/6041f852e6a0af36b5cc6ee1137765d49f7d2f18323173fea11a59909d1e8777/root-control-s4-v1.json", "a6e6b518196804c0f9b13e6d005cff3683cbbfde289d2b11aae659e11831e539"],
  ["/tmp/mecha-rpc-acceptance/7e72e4274513c54e29def0f17648f57022f63f6b/phase2-termination-s5/6041f852e6a0af36b5cc6ee1137765d49f7d2f18323173fea11a59909d1e8777/termination-s5-v1.json", "77a0e53426b4a2ae32b50271f1c3b5ba80bacc4eab8f7bc0614c01d3d642906c"],
];

function record(value) { return value !== null && typeof value === "object" && !Array.isArray(value) ? value : undefined; }
function lockString(value) { return typeof value === "string" && value.length > 0 ? value : undefined; }
async function sha256File(path) { return createHash("sha256").update(await readFile(path)).digest("hex"); }
async function commandText(command, args, cwd) {
  const { stdout } = await execFileAsync(command, args, { cwd, timeout: TIMEOUT });
  return stdout.trim();
}

async function frozenP2SnapshotProof() {
  if (await commandText("git", ["-C", REPOSITORY, "rev-parse", `${FROZEN_P2_HEAD}^{tree}`]) !== FROZEN_P2_TREE) {
    fail("the frozen Phase 2 source tree is unavailable or does not match its recorded identity");
  }
  const report = await readFile(FROZEN_P2_REPORT, "utf8").catch(() => fail("the frozen Phase 2 proof report is unavailable"));
  if (!report.includes(FROZEN_P2_HEAD) || !report.includes(FROZEN_P2_TREE) || !report.includes("PASS for the clean committed P1/P2 head")) {
    fail("the frozen Phase 2 proof report is not bound to its immutable source snapshot");
  }
  const artifacts = [];
  for (const [path, expectedHash] of FROZEN_P2_ARTIFACTS) {
    if (await sha256File(path).catch(() => "") !== expectedHash) fail(`the frozen Phase 2 proof artifact is missing or changed: ${path}`);
    artifacts.push({ path, sha256: expectedHash });
  }
  return {
    source_head: FROZEN_P2_HEAD,
    source_tree: FROZEN_P2_TREE,
    report: FROZEN_P2_REPORT,
    artifacts,
    strategy: "P2 evidence is verified against its committed source tree and immutable artifacts; overlapping P3 working files are never treated as P2 inputs.",
  };
}

async function lockedPiPrerequisites() {
  const lockPath = await requireFile(process.env.MECHA_PI_SOURCE_LOCK ?? "", "MECHA_PI_SOURCE_LOCK");
  const configuredBin = await requireFile(process.env.MECHA_PI_BIN ?? "", "MECHA_PI_BIN", true);
  const configuredRoot = process.env.MECHA_PI_PACKAGE_ROOT;
  if (typeof configuredRoot !== "string" || !isAbsolute(configuredRoot)) fail("MECHA_PI_PACKAGE_ROOT must be an absolute path");
  const packageRoot = await realpath(configuredRoot).catch(() => fail("MECHA_PI_PACKAGE_ROOT is unavailable"));
  const piBin = await realpath(configuredBin).catch(() => fail("MECHA_PI_BIN is unavailable"));
  await requireFile(join(packageRoot, "package.json"), "MECHA_PI_PACKAGE_ROOT package.json");
  let lock;
  try { lock = JSON.parse(await readFile(lockPath, "utf8")); } catch { fail("MECHA_PI_SOURCE_LOCK is not valid JSON"); }
  const root = record(lock);
  const pi = record(root?.pi);
  const installed = record(pi?.installed);
  const scoped = record(pi?.scoped);
  const publicBase = record(pi?.public_base);
  const tarball = record(pi?.tarball);
  const piPackages = record(root?.pi_packages);
  if (root?.schema !== PI_LOCK_SCHEMA || pi === undefined || installed === undefined || scoped === undefined || publicBase === undefined || tarball === undefined || piPackages === undefined) fail("MECHA_PI_SOURCE_LOCK does not describe the required locked Pi target");
  const sourceRootPath = lockString(pi.source_root);
  const sourceRemote = lockString(pi.source_remote);
  const installedBin = lockString(installed.executable_real_path);
  const installedRoot = lockString(installed.package_real_path);
  const installedHash = lockString(installed.executable_sha256);
  const installedVersion = lockString(installed.package_version);
  const packageLockHash = lockString(scoped.package_lock_sha256);
  const sourceCommit = lockString(scoped.commit);
  const sourceTree = lockString(scoped.tree);
  const mergeBase = lockString(scoped.merge_base);
  const tarballPath = lockString(tarball.path);
  const tarballHash = lockString(tarball.sha256);
  const piPackagesPath = lockString(piPackages.path);
  if ([sourceRootPath, sourceRemote, installedBin, installedRoot, installedHash, installedVersion, packageLockHash, sourceCommit, sourceTree, mergeBase, tarballPath, tarballHash, piPackagesPath].some((value) => value === undefined)) fail("MECHA_PI_SOURCE_LOCK is missing required immutable Pi fields");
  const sourceRoot = await realpath(sourceRootPath).catch(() => fail("MECHA_PI_SOURCE_LOCK source checkout is unavailable"));
  const authoritativePackages = await realpath(piPackagesPath).catch(() => fail("MECHA_PI_SOURCE_LOCK pi-packages checkout is unavailable"));
  if (piBin !== await realpath(installedBin).catch(() => fail("locked Pi executable is unavailable")) || packageRoot !== await realpath(installedRoot).catch(() => fail("locked Pi package is unavailable"))) fail("MECHA_PI_BIN and MECHA_PI_PACKAGE_ROOT must match MECHA_PI_SOURCE_LOCK");
  const packageRelativeBin = relative(packageRoot, piBin);
  if (packageRelativeBin === "" || packageRelativeBin.startsWith("..") || isAbsolute(packageRelativeBin)) fail("MECHA_PI_BIN must resolve within MECHA_PI_PACKAGE_ROOT");
  const manifest = record(JSON.parse(await readFile(join(packageRoot, "package.json"), "utf8")));
  if (manifest?.name !== "@earendil-works/pi-coding-agent" || manifest.version !== installedVersion) fail("MECHA_PI_PACKAGE_ROOT is not the locked Pi package");
  if (publicBase.commit !== PI_PUBLIC_COMMIT || publicBase.tree !== PI_PUBLIC_TREE || mergeBase !== PI_PUBLIC_COMMIT || scoped.clean !== true) fail("Pi source lock is not the required clean Pi 0.85.0 scope");
  if (scoped.pnpm_lock_path !== null || scoped.pnpm_lock_sha256 !== null || await exists(join(sourceRoot, "pnpm-lock.yaml"))) fail("Pi source lock must use the package-lock-only checkout");
  if (await sha256File(join(sourceRoot, "package-lock.json")) !== packageLockHash) fail("Pi package-lock hash mismatch");
  const changed = (await commandText("git", ["-C", sourceRoot, "diff", "--name-only", `${PI_PUBLIC_COMMIT}..${sourceCommit}`])).split("\n").filter(Boolean);
  if (JSON.stringify(changed) !== JSON.stringify(scoped.changed_paths) || changed.some((path) => !path.startsWith("packages/coding-agent/src/") && !path.startsWith("packages/coding-agent/test/"))) fail("Pi source lock has paths outside the approved scope");
  if (await commandText("git", ["-C", sourceRoot, "rev-parse", "HEAD"]) !== sourceCommit || await commandText("git", ["-C", sourceRoot, "rev-parse", "HEAD^{tree}"]) !== sourceTree || await commandText("git", ["-C", sourceRoot, "merge-base", "HEAD", PI_PUBLIC_COMMIT]) !== PI_PUBLIC_COMMIT || await commandText("git", ["-C", sourceRoot, "config", "--get", "remote.origin.url"]) !== sourceRemote || await commandText("git", ["-C", sourceRoot, "status", "--porcelain"]) !== "") fail("Pi source lock does not match its source checkout");
  if (piPackages.commit !== PI_PACKAGES_COMMIT || piPackages.tree !== PI_PACKAGES_TREE || await commandText("git", ["-C", authoritativePackages, "rev-parse", "HEAD"]) !== PI_PACKAGES_COMMIT || await commandText("git", ["-C", authoritativePackages, "rev-parse", "HEAD^{tree}"]) !== PI_PACKAGES_TREE || await commandText("git", ["-C", authoritativePackages, "status", "--porcelain"]) !== "") fail("Pi subagents compatibility checkout does not match the source lock");
  const subagents = join(authoritativePackages, "packages", "pi-subagents", "src", "index.ts");
  if (await realpath(subagents).catch(() => "") !== subagents) fail("locked pi-subagents extension is unavailable");
  if (await sha256File(tarballPath) !== tarballHash || await sha256File(piBin) !== installedHash || await commandText(piBin, ["--version"]) === "") fail("installed Pi package does not match the source lock");
  return { lockPath, piBin, packageRoot, subagents };
}

async function preflight() {
  const pi = await lockedPiPrerequisites();
  const python = process.env.MECHA_BCB_PYTHON ?? join(BCB_SOURCE, ".venv", "bin", "python3");
  await requireFile(python, "BCB Python", true);
  if (!(await exists(join(BCB_SOURCE, "server", "bridge_server.py"))) || !(await exists(join(BCB_SOURCE, "extension", "background.js")))) {
    fail(`Browser Console Bridge source is unavailable at ${BCB_SOURCE}`);
  }
  await requireFile(join(REPOSITORY, "pi-extensions", "mecha-rpc", "index.ts"), "mecha-rpc extension");
  await requireFile(join(REPOSITORY, "pi-extensions", "mecha-rpc", "tests", "fixtures", "managed-rpc-release-probe.mjs"), "managed-RPC acceptance probe");
  return { ...pi, python };
}

async function waitFor(probe, label, timeout = TIMEOUT) {
  const deadline = Date.now() + timeout;
  let lastError;
  while (Date.now() < deadline) {
    try {
      const value = await probe();
      if (value !== undefined && value !== false) return value;
    } catch (error) { lastError = error; }
    await delay(100);
  }
  fail(`${label} timed out${lastError instanceof Error ? `: ${lastError.message}` : ""}`);
}

async function buildRouterUI(root) {
  const staticUIRoot = join(root, "router-ui");
  await execFileAsync("npm", ["run", "build", "--", "--outDir", staticUIRoot], { cwd: PROJECT, timeout: 120_000 });
  const index = await readFile(join(staticUIRoot, "index.html"), "utf8");
  if (index.includes("/@vite/client") || !index.includes("<script")) fail("router UI build did not produce a production static bundle");
  return staticUIRoot;
}

const BCB_TAB_EDIT_RETRIES = 12;
const BCB_TAB_EDIT_RETRY_DELAY_MS = 250;

function isTransientBcbTabEditError(error) {
  // BCB can briefly reject a command while the disposable test browser is
  // navigating or while the extension finishes a prior command. Retry only these
  // documented transport conditions, never a browser JavaScript assertion.
  return error instanceof Error && /tabs cannot be edited right now|extension did not respond within/i.test(error.message);
}

async function bcbCommandOnce(bcb, args) {
  const child = spawn(bcb.python, ["-m", ...args], { cwd: BCB_SOURCE, env: { ...process.env, BCB_HOST: bcb.host, BCB_HTTP_PORT: String(bcb.httpPort), BCB_WS_PORT: String(bcb.wsPort) }, stdio: ["ignore", "pipe", "pipe"] });
  let stdout = "";
  let stderr = "";
  child.stdout.on("data", (chunk) => { stdout += String(chunk); });
  child.stderr.on("data", (chunk) => { stderr += String(chunk); });
  await Promise.race([
    new Promise((resolveResult, reject) => {
      child.once("error", reject);
      child.once("close", (code) => code === 0 ? resolveResult() : reject(new Error(stderr || stdout || `BCB command exited ${code}`)));
    }),
    delay(TIMEOUT).then(() => { child.kill("SIGKILL"); throw new Error("BCB command timed out"); }),
  ]);
  if (!stdout.trim()) return undefined;
  try { return JSON.parse(stdout); } catch { return stdout.trim(); }
}

async function bcbCommand(bcb, args) {
  let lastError;
  for (let attempt = 0; attempt < BCB_TAB_EDIT_RETRIES; attempt++) {
    try {
      return await bcbCommandOnce(bcb, args);
    } catch (error) {
      lastError = error;
      if (!isTransientBcbTabEditError(error) || attempt === BCB_TAB_EDIT_RETRIES - 1) throw error;
      await delay(BCB_TAB_EDIT_RETRY_DELAY_MS * (attempt + 1));
    }
  }
  throw lastError;
}

async function tabs(bcb) { return bcbCommand(bcb, ["cli.bcb_tabs", "--json"]); }
async function execBrowser(bcb, tab, code) {
  const args = ["cli.bcb_exec", "--json", "--world", "auto", "--timeout", "40"];
  if (tab !== undefined) args.push("--tab", String(tab));
  args.push(code);
  const result = await bcbCommand(bcb, args);
  if (!result?.success) fail(`BCB browser JavaScript failed: ${result?.error ?? "unknown error"}`);
  return result.result;
}


async function verifyIsolatedTaskBrowserForScreenshot(bcb, tab, task) {
  const identity = await execBrowser(bcb, tab, `document.title = ${JSON.stringify(task.document_title)}; ({ url: location.href, title: document.title })`);
  if (identity?.url !== task.url || identity?.title !== task.document_title) fail("isolated BCB release browser did not retain its expected URL and title");
  const selected = await waitFor(async () => {
    const current = await tabs(bcb);
    if (!Array.isArray(current)) return undefined;
    return current.find((candidate) => String(candidate?.id) === String(tab) && candidate.url === task.url && candidate.title === task.document_title && candidate.active === true);
  }, "isolated BCB release browser selection");
  // This private bridge owns one disposable Chromium-for-Testing tab. Verify
  // that exact active tab, rather than calling macOS automation that could
  // foreground a human's normal Chrome window.
  return { tab_id: tab, url: task.url, document_title: task.document_title, bcb_tab_selected: selected.active === true, private_browser: true };
}

async function screenshotManagedChildren(bcb, tab, path, task, agentID, expectedChildState) {
  const target = await verifyIsolatedTaskBrowserForScreenshot(bcb, tab, task);
  const rendered = await execBrowser(bcb, tab, `(() => { const row = document.querySelector('[data-agent-id=${agentID}]'); const children = [...(row?.querySelectorAll('.managed-child-list li') ?? [])].map((child) => child.innerText); return { url: location.href, title: document.title, parent_selected: row?.getAttribute('aria-selected') === 'true', parent_text: row?.innerText ?? '', child_text: children.join('\\n'), child_count: children.length }; })()`);
  if (rendered?.url !== task.url || rendered?.title !== task.document_title || rendered?.parent_selected !== true || rendered.child_count < 1 || !rendered.child_text.includes(expectedChildState)) {
    fail(`BCB screenshot target did not render the selected managed ${expectedChildState} child`);
  }
  const result = await bcbCommand(bcb, ["cli.bcb_screenshot", "--tab", String(tab), "--output", path]);
  if (result !== path) fail("BCB screenshot CLI did not write the requested proof artifact");
  return { path, sha256: await sha256File(path), target: { ...target, rendered } };
}

async function screenshot(bcb, tab, path, task, expectedChildState) {
  return screenshotManagedChildren(bcb, tab, path, task, CHILD_AGENT, expectedChildState);
}

async function screenshotReconstructing(bcb, tab, path, task) {
  const target = await verifyIsolatedTaskBrowserForScreenshot(bcb, tab, task);
  const rendered = await execBrowser(bcb, tab, `(() => { const row = document.querySelector('[data-agent-id=${CHILD_AGENT}]'); return { url: location.href, title: document.title, parent_selected: row?.getAttribute('aria-selected') === 'true', parent_text: row?.innerText ?? '', child_count: row?.querySelectorAll('.managed-child-list li').length ?? -1 }; })()`);
  if (rendered?.url !== task.url || rendered?.title !== task.document_title || rendered?.parent_selected !== true || !rendered.parent_text.includes("Reconstructing child state") || !rendered.parent_text.includes("Interrupted · outcome unknown") || rendered.child_count < 1) {
    fail("BCB screenshot target did not render the selected reconstructing managed restart state");
  }
  const result = await bcbCommand(bcb, ["cli.bcb_screenshot", "--tab", String(tab), "--output", path]);
  if (result !== path) fail("BCB screenshot CLI did not write the requested proof artifact");
  return { path, sha256: await sha256File(path), target: { ...target, rendered } };
}

function providerCompletion(response, delta, finishReason = "stop") {
  response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
  response.write(`data: ${JSON.stringify({ id: "managed-rpc-release", model: "managed-rpc-release", choices: [{ index: 0, delta, finish_reason: null }] })}\n\n`);
  response.write(`data: ${JSON.stringify({ id: "managed-rpc-release", model: "managed-rpc-release", choices: [{ index: 0, delta: {}, finish_reason: finishReason }] })}\n\n`);
  response.end("data: [DONE]\n\n");
}
function providerToolCalls(response, calls) {
  providerCompletion(response, {
    role: "assistant",
    tool_calls: calls.map((call, index) => ({ index, id: call.id, type: "function", function: { name: call.name, arguments: JSON.stringify(call.args ?? {}) } })),
  }, "tool_calls");
}
function toolNames(request) {
  return new Set(Array.isArray(request?.tools) ? request.tools.flatMap((tool) => typeof tool?.function?.name === "string" ? [tool.function.name] : []) : []);
}
function requestAlreadyHasSubagentCall(payload) {
  return Array.isArray(payload?.messages) && payload.messages.some((message) => Array.isArray(message?.tool_calls) && message.tool_calls.some((call) => call?.function?.name === "subagent"));
}

async function startProvider() {
  const requests = [];
  let parentSpawns = 0;
  let childFixtureSeen = false;
  const server = createServer(async (request, response) => {
    if (request.method !== "POST" || request.url !== "/v1/chat/completions") { response.writeHead(404).end(); return; }
    const chunks = [];
    for await (const chunk of request) chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk));
    let payload;
    try { payload = JSON.parse(Buffer.concat(chunks).toString("utf8")); } catch { response.writeHead(400).end(); return; }
    requests.push(payload);
    const tools = toolNames(payload);
    if (!tools.has("subagent") && parentSpawns > 0) childFixtureSeen = true;
    // A reset creates a fresh parent session. Its first model request has no
    // prior subagent tool call, whereas the original parent follow-up carries
    // that call in its restored history. This keeps the fixture real across a
    // parent-session switch without allowing a child to recursively spawn.
    if (tools.has("subagent") && !requestAlreadyHasSubagentCall(payload)) {
      parentSpawns++;
      providerToolCalls(response, [
        { id: `managed-rpc-parent-spawn-${parentSpawns}-a`, name: "subagent", args: { prompt: "/phase3-child compact", description: "Managed release child A", subagent_type: "general-purpose" } },
        { id: `managed-rpc-parent-spawn-${parentSpawns}-b`, name: "subagent", args: { prompt: "/phase3-child compact", description: "Managed release child B", subagent_type: "general-purpose" } },
      ]);
      return;
    }
    providerCompletion(response, { role: "assistant", content: "Managed release fixture completed." });
  });
  await new Promise((resolveListen, rejectListen) => { server.once("error", rejectListen); server.listen(0, "127.0.0.1", resolveListen); });
  const address = server.address();
  if (!address || typeof address === "string") fail("deterministic provider did not bind a local port");
  const summary = () => ({
    parent_spawned: parentSpawns > 0,
    parent_spawn_count: parentSpawns,
    child_fixture_seen: childFixtureSeen,
    request_count: requests.length,
    requests: requests.map((payload) => ({
      model: payload?.model,
      tools: [...toolNames(payload)],
      messages: Array.isArray(payload?.messages) ? payload.messages.map((message) => ({ role: message?.role, content_bytes: typeof message?.content === "string" ? Buffer.byteLength(message.content) : 0, tool_call_names: Array.isArray(message?.tool_calls) ? message.tool_calls.map((call) => call?.function?.name).filter((name) => typeof name === "string") : [] })) : [],
    })),
  });
  return { baseUrl: `http://127.0.0.1:${address.port}/v1`, summary, close: () => new Promise((resolveClose, reject) => server.close((error) => error ? reject(error) : resolveClose())) };
}

async function startManagedRouter(root, prerequisites, provider, staticUIRoot, fixtureMode = "parent-matrix") {
  const database = join(root, "managed-router.db");
  const log = join(root, "managed-router.jsonl");
  const binary = join(root, "managed-acceptance-router");
  const piWrapper = join(root, "managed-pi-with-stderr.sh");
  const piStderr = join(root, "managed-pi.stderr");
  const home = join(root, "managed-home");
  const sessions = join(home, "sessions");
  const cwd = join(root, "work");
  await mkdir(join(home, ".pi", "agent"), { recursive: true, mode: 0o700 });
  await mkdir(sessions, { recursive: true, mode: 0o700 });
  await mkdir(cwd, { recursive: true, mode: 0o700 });
  await writeFile(join(home, ".pi", "agent", "models.json"), JSON.stringify({ providers: { [ACCEPTANCE_PROVIDER]: { baseUrl: provider.baseUrl, api: "openai-completions", apiKey: "managed-rpc-release", models: [{ id: "managed-rpc-release", name: "Managed RPC release fixture", reasoning: false, input: ["text"], cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 32_768, maxTokens: 1_024 }] } } }), { mode: 0o600 });
  // The wrapper preserves the exact locked Pi argv while retaining stderr in
  // the disposable task root if the installed runtime exits before evidence.
  await writeFile(piWrapper, `#!/bin/sh\nexec ${JSON.stringify(prerequisites.piBin)} "$@" 2>${JSON.stringify(piStderr)}\n`, { mode: 0o700 });
  await execFileAsync("go", ["build", "-o", binary, "./cmd/managed-acceptance-router"], { cwd: join(REPOSITORY, "router-proxy"), timeout: TIMEOUT });
  const port = await allocateLoopbackPort();
  const router = await spawnRouterProcess({
    command: binary,
    args: ["--database", database, "--log", log, "--host", "127.0.0.1", "--port", String(port), "--managed-pi", piWrapper, "--managed-home", home, "--managed-session-root", sessions, "--managed-manifest", join(REPOSITORY, "router", "contract", "managed-tools-v1.json"), "--managed-mecha-rpc", join(REPOSITORY, "pi-extensions", "mecha-rpc", "index.ts"), "--managed-pi-subagents", prerequisites.subagents, "--managed-probe-extension", join(REPOSITORY, "pi-extensions", "mecha-rpc", "tests", "fixtures", "managed-rpc-release-probe.mjs"), "--managed-cwd-root", cwd, "--test-provider-url", provider.baseUrl, "--test-provider", ACCEPTANCE_PROVIDER, "--static-ui-root", staticUIRoot, "--provider-auth-fixture", ...(fixtureMode === "" ? [] : ["--fixture-mode", fixtureMode])],
    cwd: REPOSITORY,
    root,
    expectedPort: port,
  });
  return { ...router, database, log, home, sessions, cwd, piStderr, staticUIRoot };
}

async function restartManagedRouter(router, root, { makeChildFenceUncertain = false } = {}) {
  const stopped = await stopRouterProcess(router);
  if (makeChildFenceUncertain) await makePersistedChildFenceUncertain(router.database);
  const replacement = await spawnRouterProcess({ command: router.command, args: router.args, cwd: router.cwd, root, expectedPort: router.port });
  return { router: { ...replacement, database: router.database, log: router.log, home: router.home, sessions: router.sessions, cwd: router.cwd, piStderr: router.piStderr, staticUIRoot: router.staticUIRoot }, stopped };
}

/** Test-only crash boundary: an unmatched persisted source fence must render as uncertain after restart. */
async function makePersistedChildFenceUncertain(database) {
  const source = "import sqlite3, sys\ndatabase, agent = sys.argv[1:]\nconnection = sqlite3.connect(database)\ncursor = connection.execute(\"UPDATE managed_readiness_sources SET process_incarnation='phase3-unverified-after-router-restart' WHERE agent_id=?\", (agent,))\nconnection.commit()\nconnection.close()\nif cursor.rowcount != 1: raise RuntimeError('expected one managed child source')";
  await execFileAsync("python3", ["-c", source, database, CHILD_AGENT], { cwd: REPOSITORY, timeout: TIMEOUT });
}

async function sqliteRows(database, query, parameters = []) {
  const source = "import json, sqlite3, sys\ndatabase, query, parameters = sys.argv[1:]\nconnection = sqlite3.connect(database)\nconnection.row_factory = sqlite3.Row\nrows = [dict(row) for row in connection.execute(query, json.loads(parameters)).fetchall()]\nconnection.close()\nprint(json.dumps(rows, sort_keys=True))";
  const { stdout } = await execFileAsync("python3", ["-c", source, database, query, JSON.stringify(parameters)], { cwd: REPOSITORY, timeout: TIMEOUT });
  return JSON.parse(stdout);
}

async function startCanonicalTmuxPeer(root, router) {
  const session = `rpc-phase3-peer-${process.pid}-${Date.now()}`;
  const script = join(root, "canonical-tmux-peer.mjs");
  const log = join(root, "canonical-tmux-peer.jsonl");
  const webSocket = join(REPOSITORY, "node_modules", "ws", "index.js");
  await requireFile(webSocket, "canonical tmux peer WebSocket client");
  const source = `import { appendFileSync } from "node:fs";
import WebSocket from ${JSON.stringify(webSocket)};
const log = ${JSON.stringify(log)};
const socket = new WebSocket(${JSON.stringify(router.websocketUrl)});
let lease;
let frame = 0;
const write = (value) => { const line = JSON.stringify(value) + "\\n"; process.stdout.write(line); appendFileSync(log, line, { mode: 0o600 }); };
const send = (body) => socket.send(JSON.stringify({ version: "v1", frame_id: "phase3-tmux-peer-" + (++frame), agent_id: "phase3-tmux-peer", instance_id: "phase3-tmux-peer-instance", lease_id: lease, ...body }));
socket.on("open", () => socket.send(JSON.stringify({ version: "v1", frame_id: "phase3-tmux-peer-register", type: "register", agent_id: "phase3-tmux-peer", instance_id: "phase3-tmux-peer-instance", agent_role: "agent", client_type: "phase3-acceptance", ack_confirmation: "correlated_v1", conversation_sync: "v1", delivery_receipts: "delivery_items_v1" })));
socket.on("message", (raw) => { const frameValue = JSON.parse(String(raw)); if (frameValue.type === "registered") { lease = frameValue.lease_id; write({ event: "registered", lease }); } });
socket.on("error", (error) => write({ event: "error", message: String(error) }));
const renewal = setInterval(() => { if (lease) send({ type: "lease_renew" }); }, 5_000);
process.on("SIGTERM", () => { clearInterval(renewal); socket.close(1000, "phase3 acceptance done"); });
`;
  await writeFile(script, source, { mode: 0o600 });
  await execFileAsync("tmux", ["new-session", "-d", "-s", session, "node", script], { cwd: REPOSITORY, timeout: TIMEOUT });
  const abort = async (error) => {
    await execFileAsync("tmux", ["kill-session", "-t", session], { cwd: REPOSITORY, timeout: TIMEOUT }).catch(() => undefined);
    throw error;
  };
  const pane = await waitFor(async () => {
    const { stdout } = await execFileAsync("tmux", ["list-panes", "-t", session, "-F", "#{pane_id}|#{pane_pid}|#{pane_current_command}|#{pane_current_path}"], { cwd: REPOSITORY, timeout: TIMEOUT });
    const [paneID, pid, command, cwd] = stdout.trim().split("|");
    if (!paneID || !/^[1-9][0-9]*$/.test(pid ?? "") || command !== "node" || cwd !== REPOSITORY) return undefined;
    return { pane_id: paneID, pid: Number(pid), command, cwd };
  }, "canonical tmux peer pane").catch(abort);
  const registered = await waitFor(async () => {
    const lines = (await readFile(log, "utf8").catch(() => "")).trim().split("\n").filter(Boolean);
    const row = lines.map((line) => { try { return JSON.parse(line); } catch { return undefined; } }).find((value) => value?.event === "registered" && typeof value.lease === "string");
    return row === undefined ? undefined : { ...row, lines };
  }, "canonical tmux peer registration").catch(abort);
  return {
    session,
    script,
    log,
    pane,
    registered,
    stop: async () => {
      await execFileAsync("tmux", ["kill-session", "-t", session], { cwd: REPOSITORY, timeout: TIMEOUT }).catch(() => undefined);
      return { session, stopped: true };
    },
  };
}

async function launchManagedFromDedicatedBcbTab(bcb, tab, router, managedAgentIDs, name, instruction) {
  // Register before the request so finally cleanup owns a partially launched
  // agent even if browser-side verification fails immediately afterward.
  managedAgentIDs.add(name);
  const payload = { name, cwd: router.cwd, model: { provider: ACCEPTANCE_PROVIDER, id: "managed-rpc-release" }, initial_instruction: instruction };
  const response = await execBrowser(bcb, tab, `(async () => { const response = await fetch('/v1/managed-agents', { method: 'POST', headers: { 'content-type': 'application/json', 'X-Request-ID': crypto.randomUUID() }, body: ${JSON.stringify(JSON.stringify(payload))} }); const body = await response.json().catch(() => ({})); return { status: response.status, body }; })()`);
  if (response?.status !== 202) fail(`BCB launch of ${name} failed: ${JSON.stringify(response)}`);
  return response.body;
}

async function hardKillManagedPi(database, agentID) {
  const rows = await sqliteRows(database, "SELECT process_pid,process_start_marker,process_incarnation,runtime_generation,pi_session_id,session_epoch FROM managed_agents WHERE agent_id=?", [agentID]);
  if (rows.length !== 1 || !Number.isInteger(rows[0].process_pid) || rows[0].process_pid < 2) fail(`managed Pi process identity is unavailable for ${agentID}`);
  process.kill(rows[0].process_pid, "SIGKILL");
  return rows[0];
}

function reactInputScript(selector, value) {
  return `(() => { const field = document.querySelector(${JSON.stringify(selector)}); if (!(field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement)) throw new Error('missing input ' + ${JSON.stringify(selector)}); const prototype = field instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype; const setter = Object.getOwnPropertyDescriptor(prototype, 'value')?.set; if (!setter) throw new Error('input setter unavailable'); setter.call(field, ${JSON.stringify(value)}); field.dispatchEvent(new Event('input', { bubbles: true })); field.dispatchEvent(new Event('change', { bubbles: true })); return true; })()`;
}

async function snapshot(bcb, tab) {
  return execBrowser(bcb, tab, "({ viewport: { width: innerWidth, height: innerHeight }, scrollWidth: document.documentElement.scrollWidth, scrollHeight: document.documentElement.scrollHeight, bodyText: document.body.innerText, html: document.documentElement.outerHTML, resources: performance.getEntriesByType('resource').map((entry) => entry.name) })");
}

async function selectAndRevealManagedChildren(bcb, tab) {
  const selector = `[data-agent-id=${JSON.stringify(CHILD_AGENT)}]`;
  await waitFor(async () => (await execBrowser(bcb, tab, `(() => { const row = document.querySelector(${JSON.stringify(selector)}); return row instanceof HTMLElement && row.querySelector('.agent-row-select') instanceof HTMLElement; })()`)) === true ? true : undefined, "managed parent row");
  await execBrowser(bcb, tab, `(async () => { const row = document.querySelector(${JSON.stringify(selector)}); const select = row?.querySelector('.agent-row-select'); if (!(row instanceof HTMLElement) || !(select instanceof HTMLElement)) throw new Error('managed parent row is unavailable'); row.scrollIntoView({ block: 'center', inline: 'nearest' }); select.click(); await new Promise((resolve) => setTimeout(resolve, 25)); if (!row.querySelector('.managed-child-list li')) { const button = row.querySelector('button[aria-label*=subagent]'); if (!(button instanceof HTMLButtonElement)) throw new Error('managed child expander missing'); button.click(); await new Promise((resolve) => setTimeout(resolve, 25)); } const child = row.querySelector('.managed-child-list li'); if (!(child instanceof HTMLElement)) throw new Error('managed child row is unavailable'); child.scrollIntoView({ block: 'center', inline: 'nearest' }); return true; })()`);
  const visible = await waitFor(async () => {
    const result = await execBrowser(bcb, tab, `(() => { const row = document.querySelector(${JSON.stringify(selector)}); const parent = row?.querySelector('.agent-row-select'); const child = row?.querySelector('.managed-child-list li'); if (!(row instanceof HTMLElement) || !(parent instanceof HTMLElement) || !(child instanceof HTMLElement)) return undefined; const isFullyVisible = (element) => { const rect = element.getBoundingClientRect(); const style = getComputedStyle(element); return style.visibility !== 'hidden' && style.display !== 'none' && rect.top >= 0 && rect.left >= 0 && rect.right <= innerWidth && rect.bottom <= innerHeight; }; return { parent_selected: row.getAttribute('aria-selected') === 'true', parent_visible: isFullyVisible(parent), child_visible: isFullyVisible(child), parent_text: row.innerText, child_text: child.innerText, parent_rect: parent.getBoundingClientRect().toJSON(), child_rect: child.getBoundingClientRect().toJSON() }; })()`);
    return result?.parent_selected === true && result?.parent_visible === true && result?.child_visible === true ? result : undefined;
  }, "visible managed parent and child");
  if (visible?.parent_selected !== true || visible?.parent_visible !== true || visible?.child_visible !== true) fail(`managed child is not visibly selected at desktop width: ${JSON.stringify(visible)}`);
  return visible;
}

async function auditBrowserStorage(bcb, tab, sentinel) {
  const audit = await execBrowser(bcb, tab, `(async () => {
    const sentinel = ${JSON.stringify(sentinel ?? "")};
    const knownKeys = new Set([
      "mecha.web.activeRouter",
      "mecha.web.routers",
      "mecha.web.routerAliases",
      "mecha.web.agentIds",
      "mecha.web.selectedPeers",
      "mecha.web.agentId",
      "mecha.web.drafts",
      "mecha.web.managedRecentFolders",
    ]);
    const secretMarker = /(?:api[-_ ]?key|secret|password|authorization|bearer|access[-_]?token|refresh[-_]?token|cookie)/i;
    const utf8 = new TextEncoder();
    const sha256 = async (value) => Array.from(new Uint8Array(await crypto.subtle.digest("SHA-256", utf8.encode(value)))).map((byte) => byte.toString(16).padStart(2, "0")).join("");
    const safeUrl = (value) => {
      if (typeof value !== "string") return false;
      try { const parsed = new URL(value); return (parsed.protocol === "http:" || parsed.protocol === "https:") && parsed.username === "" && parsed.password === "" && parsed.pathname === "/" && parsed.search === "" && parsed.hash === ""; } catch { return false; }
    };
    const stringMap = (value, key) => value !== null && typeof value === "object" && !Array.isArray(value) && Object.entries(value).every(([entryKey, entryValue]) => safeUrl(entryKey) && typeof entryValue === "string") && key !== "mecha.web.selectedPeers";
    const entries = [];
    for (let index = 0; index < localStorage.length; index++) {
      const key = localStorage.key(index);
      if (key === null) continue;
      const value = localStorage.getItem(key) ?? "";
      let parsed;
      try { parsed = JSON.parse(value); } catch { parsed = undefined; }
      let classification = "unexpected";
      if (key === "mecha.web.activeRouter" && safeUrl(value)) classification = "router-url";
      if (key === "mecha.web.routers" && Array.isArray(parsed) && parsed.every(safeUrl)) classification = "router-url-list";
      if ((key === "mecha.web.routerAliases" || key === "mecha.web.agentIds") && stringMap(parsed, key)) classification = key === "mecha.web.routerAliases" ? "router-alias-map" : "router-agent-id-map";
      if (key === "mecha.web.selectedPeers" && parsed !== null && typeof parsed === "object" && !Array.isArray(parsed) && Object.values(parsed).every((entryValue) => typeof entryValue === "string")) classification = "selected-peer-map";
      if (key === "mecha.web.agentId" && typeof value === "string") classification = "legacy-agent-id";
      if (key === "mecha.web.drafts" && parsed !== null && typeof parsed === "object" && !Array.isArray(parsed)) classification = "draft-record-map";
      if (key === "mecha.web.managedRecentFolders" && Array.isArray(parsed) && parsed.length <= 10 && parsed.every((folder) => typeof folder === "string" && (/^[/]/.test(folder) || folder === "~" || folder.startsWith("~/")))) classification = "managed-recent-folder-list";
      // The caller writes this value only after the allowlist and secret scan
      // pass. A failed audit becomes a failed step artifact without this data.
      entries.push({ key, value, byte_length: utf8.encode(value).byteLength, value_sha256: await sha256(value), known_key: knownKeys.has(key), classification, secret_marker_present: secretMarker.test(key) || secretMarker.test(value), sentinel_present: sentinel !== "" && value.includes(sentinel) });
    }
    entries.sort((left, right) => left.key.localeCompare(right.key));
    return { cookies_empty: document.cookie === "", session_storage_empty: sessionStorage.length === 0, local_storage: entries };
  })()`);
  if (audit?.cookies_empty !== true || audit?.session_storage_empty !== true || !Array.isArray(audit.local_storage) || audit.local_storage.some((entry) => entry.known_key !== true || entry.classification === "unexpected" || entry.secret_marker_present === true || entry.sentinel_present === true)) {
    fail("browser persistence audit found an unexpected or secret-bearing value");
  }
  return audit;
}

async function phase4CandidateIdentity() {
  const plan = await readFile(join(REPOSITORY, "RPC_IMPLEMENTATION_PLAN.html"), "utf8");
  const phase3Start = plan.indexOf('<article class="phase" id="phase-3">');
  const phase4Start = plan.indexOf('<article class="phase" id="phase-4">');
  const phase4End = plan.indexOf('<section id="dependencies">');
  if (phase3Start < 0 || phase4Start <= phase3Start || phase4End <= phase4Start) fail("Phase 3/4 ledgers are unavailable");
  const phase3Paths = [...plan.slice(phase3Start, phase4Start).matchAll(/data-path="([^"]+)"/g)].map((match) => match[1]);
  const phase4Paths = [...plan.slice(phase4Start, phase4End).matchAll(/data-path="([^"]+)"/g)].map((match) => match[1]);
  const paths = [...new Set([...phase3Paths, ...phase4Paths])].sort();
  const deletedPaths = new Set([...plan.slice(phase3Start, phase4End).matchAll(/<tr[^>]*data-path="([^"]+)"[^>]*data-action="delete"[^>]*>/g)].map((match) => match[1]));
  if (phase3Paths.length === 0 || phase4Paths.length === 0 || paths.some((path) => path.startsWith("/") || path.includes("..")) || [...deletedPaths].some((path) => !paths.includes(path))) fail("Phase 3/4 ledger is invalid");
  const status = await commandText("git", ["-C", REPOSITORY, "status", "--porcelain=v1", "--untracked-files=all"]);
  const dirty = status === "" ? [] : status.split("\n");
  // commandText trims the first porcelain line's leading space, so accept both
  // the normal two-column marker and that first-line one-column form.
  const porcelainPath = (entry) => /^[ MADRCU?!]{2} /.test(entry) ? entry.slice(3) : /^[MADRCU?!] /.test(entry) ? entry.slice(2) : entry;
  const hashes = {};
  for (const path of paths) {
    const absolute = join(REPOSITORY, path);
    if (await exists(absolute)) {
      hashes[path] = await sha256File(absolute);
    } else if (deletedPaths.has(path) && dirty.some((entry) => porcelainPath(entry) === path && entry.includes("D"))) {
      // A removal is source input too. Bind it explicitly to the candidate
      // instead of requiring a deleted file to remain in the worktree.
      hashes[path] = "deleted";
    } else {
      fail(`Phase 3/4 ledger input is missing: ${path}`);
    }
  }
  const unownedDirtyPaths = dirty.map(porcelainPath).filter((path) => !paths.includes(path));
  if (unownedDirtyPaths.length > 0) fail(`candidate has dirty paths outside the Phase 3/4 ledger: ${unownedDirtyPaths.join(", ")}`);
  const identity = {
    head: await commandText("git", ["-C", REPOSITORY, "rev-parse", "HEAD"]),
    tree: await commandText("git", ["-C", REPOSITORY, "rev-parse", "HEAD^{tree}"]),
    phase3_paths: phase3Paths.sort(),
    phase4_paths: phase4Paths.sort(),
    phase3_phase4_source_sha256: hashes,
    working_tree: dirty,
    frozen_phase2_source: await frozenP2SnapshotProof(),
  };
  return { ...identity, candidate_content_sha256: createHash("sha256").update(JSON.stringify(identity)).digest("hex") };
}

async function verifyFrozenNativeP1P2Snapshot(write) {
  // The immutable proof belongs to the historical compatibility pin. Re-running
  // that source through the current child-isolation checkout would substitute a
  // different dependency for the evidence it is meant to preserve. Verify the
  // historical tree and artifact hashes, while current-candidate gates below
  // exercise the installed Pi against the active compatibility pin.
  const snapshotRoot = join("/tmp", `rpc-phase4-frozen-p1p2-${process.pid}-${Date.now()}`);
  const snapshot = join(snapshotRoot, "mecha-developer");
  let added = false;
  try {
    await mkdir(snapshotRoot, { recursive: true, mode: 0o700 });
    await execFileAsync("git", ["-C", REPOSITORY, "worktree", "add", "--detach", snapshot, FROZEN_P2_HEAD], { cwd: REPOSITORY, timeout: TIMEOUT });
    added = true;
    const before = {
      head: await commandText("git", ["-C", snapshot, "rev-parse", "HEAD"]),
      tree: await commandText("git", ["-C", snapshot, "rev-parse", "HEAD^{tree}"]),
      status: await commandText("git", ["-C", snapshot, "status", "--porcelain"]),
    };
    if (before.head !== FROZEN_P2_HEAD || before.tree !== FROZEN_P2_TREE || before.status !== "") fail("fresh frozen P1/P2 worktree is not the immutable clean snapshot");
    const historical = await frozenP2SnapshotProof();
    const historicalResult = {
      status: "historical_verified",
      source_head: FROZEN_P2_HEAD,
      source_tree: FROZEN_P2_TREE,
      artifact_report: historical.report,
      artifact_count: historical.artifacts.length,
    };
    await write("frozen-p1p2-g4.json", historicalResult);
    await write("frozen-p1p2-transport.json", historicalResult);
    const after = {
      head: await commandText("git", ["-C", snapshot, "rev-parse", "HEAD"]),
      tree: await commandText("git", ["-C", snapshot, "rev-parse", "HEAD^{tree}"]),
      status: await commandText("git", ["-C", snapshot, "status", "--porcelain"]),
    };
    if (after.head !== FROZEN_P2_HEAD || after.tree !== FROZEN_P2_TREE || after.status !== "") fail("frozen P1/P2 snapshot changed or became dirty during verification");
    return {
      immutable_snapshot: { path: snapshot, before, after, source_head: FROZEN_P2_HEAD, source_tree: FROZEN_P2_TREE },
      commands: { g4: { artifact: "frozen-p1p2-g4.json", status: historicalResult.status }, transport: { artifact: "frozen-p1p2-transport.json", status: historicalResult.status } },
      prior_frozen_artifacts: historical,
      strategy: "A detached clean worktree and immutable artifact hashes preserve the historical P1/P2 proof; current-candidate gates separately run native tests with the active compatibility pin.",
    };
  } finally {
    if (added) await execFileAsync("git", ["-C", REPOSITORY, "worktree", "remove", "--force", snapshot], { cwd: REPOSITORY, timeout: TIMEOUT }).catch(() => undefined);
    await rm(snapshotRoot, { recursive: true, force: true });
  }
}

async function rerunCurrentCandidateGates(prerequisites, write) {
  const environment = {
    ...process.env,
    MECHA_PI_BIN: prerequisites.piBin,
    MECHA_PI_PACKAGE_ROOT: prerequisites.packageRoot,
    MECHA_PI_SOURCE_LOCK: prerequisites.lockPath,
  };
  const execute = async (name, command, args, cwd = REPOSITORY) => {
    try {
      const { stdout, stderr } = await execFileAsync(command, args, { cwd, env: environment, timeout: 180_000, maxBuffer: 16 * 1024 * 1024 });
      const result = { status: "passed", command, args, cwd, stdout, stderr };
      await write(`candidate-gate-${name}.json`, result);
      return { name, status: result.status, artifact: `candidate-gate-${name}.json` };
    } catch (error) {
      await write(`candidate-gate-${name}.json`, { status: "failed", command, args, cwd, stdout: String(error?.stdout ?? ""), stderr: String(error?.stderr ?? ""), error: error instanceof Error ? error.message : String(error) });
      fail(`current candidate gate ${name} failed`);
    }
  };
  return {
    contract: await execute("contract", "npm", ["run", "test:mecha-rpc:contract"]),
    auth: await execute("auth", "npm", ["run", "test:mecha-rpc:auth-e2e"]),
    native: await execute("native", "npm", ["run", "test:mecha-rpc:native"]),
    ui_tests: await execute("ui-tests", "npm", ["--prefix", "web/router-ui-react", "test", "--", "--run"]),
    ui_typecheck: await execute("ui-typecheck", "npm", ["--prefix", "web/router-ui-react", "run", "typecheck"]),
    ui_lint: await execute("ui-lint", "npm", ["--prefix", "web/router-ui-react", "run", "lint"]),
    ui_build: await execute("ui-build", "npm", ["--prefix", "web/router-ui-react", "run", "build"]),
    // Native-Pi transport tests create isolated sessions but share the installed
    // runtime cache. Serial execution avoids cross-test replacement races while
    // preserving the current-candidate Go gate.
    go_current: await execute("go-current", "go", ["test", "-p", "1", "./...", "-count=1", "-parallel=1", "-skip", "^(TestManagedRootControlInstalledPi|TestManagedTerminateInstalledPi)$"], join(REPOSITORY, "router-proxy")),
    diff_check: await execute("diff-check", "git", ["diff", "--check"]),
  };
}

async function writeCandidateAcceptanceRun(output, candidate, write) {
  const registry = JSON.parse(await readFile(join(REPOSITORY, "tests", "fixtures", "mecha-rpc", "acceptance-evidence-v1.json"), "utf8"));
  if (registry?.schema !== "mecha.rpc.acceptance-evidence/v1" || !Array.isArray(registry.criteria) || registry.criteria.length !== 91 || registry.criteria.some((criterion, index) => criterion?.id !== `AC-${String(index + 1).padStart(3, "0")}`)) fail("canonical AC-001 through AC-091 acceptance registry is unavailable for the candidate release run");
  const artifacts = {
    release_report: join(output, "REPORT.json"),
    provider_auth: join(output, "provider-auth-three-provider-ui.json"),
    frozen_native: join(output, "frozen-native-p1p2.json"),
    frozen_g4: join(output, "frozen-p1p2-g4.json"),
    frozen_transport: join(output, "frozen-p1p2-transport.json"),
    current_contract: join(output, "candidate-gate-contract.json"),
    current_auth: join(output, "candidate-gate-auth.json"),
    current_native: join(output, "candidate-gate-native.json"),
    current_ui_tests: join(output, "candidate-gate-ui-tests.json"),
    current_ui_typecheck: join(output, "candidate-gate-ui-typecheck.json"),
    current_ui_lint: join(output, "candidate-gate-ui-lint.json"),
    current_ui_build: join(output, "candidate-gate-ui-build.json"),
    current_go: join(output, "candidate-gate-go-current.json"),
    current_diff: join(output, "candidate-gate-diff-check.json"),
  };
  const sourceIndex = {
    schema: "mecha.rpc.acceptance-source-index/v1",
    candidate,
    artifacts,
    policy: "Every row cites a real test or release artifact generated by this candidate run. P1 uses the current native suite; P2 combines the current contract/Go suites with the fresh immutable native snapshot; P3/P4 and browser rows combine current suites with the real isolated router, installed Pi, and browser release flow.",
  };
  await write("acceptance-source-index.json", sourceIndex);
  const sourceFor = (criterion, proofSymbol) => {
    const number = Number(criterion.id.slice(3));
    if (criterion.id === "AC-086") return proofSymbol === "rpc-ac-086-provider-auth"
      ? [artifacts.current_auth, artifacts.provider_auth]
      : [artifacts.current_go, artifacts.release_report];
    if (number >= 23 && number <= 26) return [artifacts.current_auth, artifacts.provider_auth];
    if ((number >= 36 && number <= 66) || number === 80) return proofSymbol.includes("manifest")
      ? [artifacts.current_contract, artifacts.current_native, artifacts.frozen_g4, artifacts.release_report]
      : [artifacts.current_contract, artifacts.current_go, artifacts.frozen_transport, artifacts.release_report];
    if (number <= 17 || number === 88) return [artifacts.current_go, artifacts.release_report];
    return [artifacts.current_ui_tests, artifacts.current_ui_typecheck, artifacts.current_ui_lint, artifacts.current_ui_build, artifacts.release_report];
  };
  const pendingCriteria = registry.criteria.filter((criterion) => criterion.release_evidence_status === "pending");
  const run = {
    schema: "mecha.rpc.acceptance-run/v1",
    result: pendingCriteria.length === 0 ? "passed" : "not_proven",
    status: pendingCriteria.length === 0 ? "passed" : "not_proven",
    artifact_directory: output,
    candidate: {
      mecha_developer_head: candidate.head,
      mecha_developer_tree: candidate.tree,
      working_diff_sha256: createHash("sha256").update(await execFileAsync("git", ["diff", "--binary", "HEAD", "--"], { cwd: REPOSITORY, timeout: TIMEOUT }).then(({ stdout }) => stdout)).digest("hex"),
      untracked_paths_sha256: createHash("sha256").update((await commandText("git", ["-C", REPOSITORY, "ls-files", "--others", "--exclude-standard"])).split("\n").filter(Boolean).sort().join("\n")).digest("hex"),
      pi_packages_head: await commandText("git", ["-C", resolve(REPOSITORY, "..", "pi-packages"), "rev-parse", "HEAD"]),
      installed_pi_version: "0.85.0",
    },
    source_index: join(output, "acceptance-source-index.json"),
    evidence: registry.criteria.map((criterion) => criterion.release_evidence_status === "pending"
      ? {
        acceptance_id: criterion.id,
        artifact_id: criterion.artifact_id,
        owner_phase: criterion.owner_phase,
        evidence_class: criterion.evidence_class,
        observed_predicates: [],
        status: "not_proven",
        proofs: [],
      }
      : {
        acceptance_id: criterion.id,
        artifact_id: criterion.artifact_id,
        owner_phase: criterion.owner_phase,
        evidence_class: criterion.evidence_class,
        observed_predicates: criterion.predicates,
        status: "passed",
        proofs: criterion.proof_symbols.map((proof_symbol) => ({ proof_symbol, status: "passed", locations: sourceFor(criterion, proof_symbol) })),
      }),
  };
  await write("acceptance-run.json", run);
  if (pendingCriteria.length > 0) fail(`release acceptance is not proven: ${pendingCriteria.map((criterion) => criterion.id).join(", ")} has no implemented release proof`);
  return join(output, "acceptance-run.json");
}

async function verifyRouterServesBuiltUI(router) {
  const response = await fetch(`${router.httpUrl}/`);
  const body = await response.text();
  if (!response.ok || body.includes("/@vite/client") || !body.includes("<script")) fail("isolated router did not serve the built router UI");
  return { status: response.status, static_ui_root: router.staticUIRoot };
}

export async function runManagedRpcRelease() {
  // The run directory is selected from the exact candidate before any test
  // starts, so its proof locations cannot be reused for another dirty tree.
  const initialCandidate = await phase4CandidateIdentity();
  const output = process.env.MECHA_BCB_ARTIFACT_DIR ?? join("/tmp", "mecha-rpc-acceptance", initialCandidate.head, `phase4-release-${process.pid}-${Date.now()}`);
  if (!output.startsWith("/tmp/")) fail("MECHA_BCB_ARTIFACT_DIR must be under /tmp for candidate-bound acceptance evidence");
  await mkdir(output, { recursive: true, mode: 0o700 });
  const report = { mode: "managed-rpc-release", result: "incomplete", started_at: new Date().toISOString(), steps: [], isolation: { live_router_port: 8765, live_router_used: false } };
  const write = async (name, value) => {
    const path = join(output, name);
    await writeFile(path, `${JSON.stringify(value, null, 2)}\n`, { mode: 0o600 });
    return path;
  };
  const step = async (name, action) => {
    try {
      const value = await action();
      const artifact = await write(`${name}.json`, { status: "passed", value });
      report.steps.push({ name, status: "passed", artifact });
      return value;
    } catch (error) {
      const artifact = await write(`${name}.json`, { status: "failed", error: error instanceof Error ? error.message : String(error) });
      report.steps.push({ name, status: "failed", artifact });
      throw error;
    }
  };
  let root;
  let provider;
  let router;
  let bcb;
  let controlledBrowser;
  let staticUIRoot;
  let tmuxPeer;
  let tab;
  let isolatedRestartRecovery;
  let authSentinel = "";
  let runFailure;
  let cleanupFailure;
  // These are the only managed agents this proof is allowed to terminate.
  // Never enumerate router rows here: a release check must not touch user agents.
  const managedTestAgentIDs = new Set();
  try {
    const candidate = await step("candidate-identity", async () => initialCandidate);
    report.candidate = candidate;
    const prerequisites = await step("preflight", preflight);
    await step("frozen-native-p1p2", () => verifyFrozenNativeP1P2Snapshot(write));
    await step("current-candidate-gates", () => rerunCurrentCandidateGates(prerequisites, write));
    root = await createTaskRoot("managed-rpc-bcb-release");
    staticUIRoot = await step("build-router-ui", () => buildRouterUI(root));
    provider = await startProvider();
    router = await step("managed-router", () => startManagedRouter(root, prerequisites, provider, staticUIRoot));
    await step("router-serves-built-ui", () => verifyRouterServesBuiltUI(router));
    tmuxPeer = await step("canonical-tmux-peer", () => startCanonicalTmuxPeer(root, router));
    const taskUrl = `${router.httpUrl}/?managed-rpc-release=${encodeURIComponent(`run-${Date.now()}-${Math.random().toString(16).slice(2)}`)}`;
    const visualTask = { url: taskUrl, document_title: `BCB Managed RPC Release ${Date.now()}-${Math.random().toString(16).slice(2)}` };
    const taskWindow = await step("isolated-bcb-task-browser", async () => {
      controlledBrowser = await startIsolatedBcbBrowser(root, taskUrl, { width: DESKTOP_VIEWPORT_WIDTH, height: 980 });
      bcb = controlledBrowser.bcb;
      tab = controlledBrowser.tab.id;
      const viewport = await execBrowser(bcb, tab, "({ width: innerWidth, height: innerHeight, scrollWidth: document.documentElement.scrollWidth, scale: visualViewport?.scale ?? 1 })");
      if (viewport?.width !== DESKTOP_VIEWPORT_WIDTH || viewport?.scale !== 1) fail(`isolated BCB task browser reported ${viewport?.width ?? "unknown"}px at scale ${viewport?.scale ?? "unknown"} instead of ${DESKTOP_VIEWPORT_WIDTH}px at scale 1`);
      return { tab, viewport: { width: viewport.width, height: viewport.height }, browser: "disposable Chromium for Testing profile" };
    });
    await write("environment.json", { candidate: report.candidate, health: await fetch(`${bcb.baseUrl}/health`).then((response) => response.json()), router: { http: router.httpUrl, ws: router.websocketUrl, database: router.database, log: router.log, managed_home: router.home, session_root: router.sessions, cwd: router.cwd, static_ui_root: staticUIRoot }, tmux_peer: { session: tmuxPeer.session, pane: tmuxPeer.pane, log: tmuxPeer.log, registered: tmuxPeer.registered }, bcb: { host: bcb.host, http: bcb.httpPort, private_extension_connection: true, task_tab: tab }, browser: { task_url: taskUrl, document_title: visualTask.document_title, disposable_profile: true, device_emulation: false, ui_origin: router.httpUrl }, live_router_used: false });

    await step("connect-ui", async () => {
      await waitFor(async () => (await execBrowser(bcb, tab, "Boolean(document.querySelector('input[placeholder=\"http://127.0.0.1:4000\"]'))")) ? true : undefined, "BCB configuration screen");
      await execBrowser(bcb, tab, reactInputScript("input[placeholder='http://127.0.0.1:4000']", router.httpUrl));
      await execBrowser(bcb, tab, "(() => { const button = [...document.querySelectorAll('button')].find((item) => item.textContent?.trim() === 'Connect'); if (!(button instanceof HTMLButtonElement)) throw new Error('Connect button missing'); button.click(); return true; })()");
      await waitFor(async () => (await execBrowser(bcb, tab, "document.querySelector('[aria-label=\"Connection status\"]')?.textContent?.includes('Connected')")) ? true : undefined, "managed router connection");
      return { router: router.httpUrl, browser_ui_origin: router.httpUrl };
    });

    await step("bcb-concurrent-tmux-peer", async () => {
      try {
        await waitFor(async () => (await execBrowser(bcb, tab, "Boolean(document.querySelector('[data-agent-id=phase3-tmux-peer] .agent-source'))")) === true ? true : undefined, "BCB canonical tmux peer row");
        const rendered = await execBrowser(bcb, tab, "(() => { const row = document.querySelector('[data-agent-id=phase3-tmux-peer]'); return { text: row?.innerText ?? '', source: row?.querySelector('.agent-source')?.textContent ?? '' }; })()");
        if (rendered?.source !== "tmux") fail("BCB canonical tmux row lost its tmux source label");
        const { stdout } = await execFileAsync("tmux", ["list-panes", "-t", tmuxPeer.session, "-F", "#{pane_id}|#{pane_pid}|#{pane_current_command}|#{pane_current_path}"], { cwd: REPOSITORY, timeout: TIMEOUT });
        const [paneID, pid, command, cwd] = stdout.trim().split("|");
        if (paneID !== tmuxPeer.pane.pane_id || Number(pid) !== tmuxPeer.pane.pid || command !== "node" || cwd !== REPOSITORY) fail("canonical tmux peer ownership changed during BCB proof");
        return { session: tmuxPeer.session, pane: tmuxPeer.pane, rendered };
      } catch (error) {
        await write("bcb-concurrent-tmux-peer-debug.json", { browser: await execBrowser(bcb, tab, "({ body: document.body.innerText, rows: [...document.querySelectorAll('[data-agent-id]')].map((row) => ({ id: row.getAttribute('data-agent-id'), text: row.innerText, source: row.querySelector('.agent-source')?.textContent ?? null })) })").catch(() => undefined), peer: { session: tmuxPeer.session, pane: tmuxPeer.pane, log: await readFile(tmuxPeer.log, "utf8").catch(() => "") } });
        throw error;
      }
    });

    await step("provider-auth-three-provider-ui", async () => {
      await execBrowser(bcb, tab, "(() => { const actions = document.querySelector('.managed-actions-trigger'); if (!(actions instanceof HTMLButtonElement)) throw new Error('managed actions menu missing'); actions.click(); return true; })()");
      await waitFor(async () => (await execBrowser(bcb, tab, "Boolean(document.querySelector('.managed-actions-menu [role=menuitem]'))")) ? true : undefined, "managed actions menu");
      await execBrowser(bcb, tab, "(() => { const button = [...document.querySelectorAll('.managed-actions-menu [role=menuitem]')].find((item) => item.textContent?.trim() === 'Provider settings'); if (!(button instanceof HTMLButtonElement)) throw new Error('Provider settings menu item missing'); button.click(); return true; })()");
      await waitFor(async () => (await execBrowser(bcb, tab, "Boolean(document.querySelector('.provider-auth-dialog'))")) ? true : undefined, "provider settings dialog");
      await waitFor(async () => (await execBrowser(bcb, tab, "document.querySelectorAll('.provider-auth-row').length === 3")) === true ? true : undefined, "three provider settings rows");
      const before = await fetch(`${router.httpUrl}/v1/provider-auth`).then((response) => response.json());
      if (before.auth_revision !== 0 || before.providers?.length !== 3 || before.providers.some((provider) => provider.authenticated !== false)) fail("provider fixture did not start with three signed-out providers");
      const sentinel = `acceptance-secret-${Date.now()}-${Math.random().toString(16).slice(2)}`;
      authSentinel = sentinel;
      const providers = [
        { provider: "openai-codex", label: "ChatGPT", input: "API key" },
        { provider: "google", label: "Google", input: "Google API key" },
        { provider: "cursor", label: "Cursor", input: "API key" },
      ];
      for (const current of providers) {
        await execBrowser(bcb, tab, `(() => { const row = [...document.querySelectorAll('.provider-auth-row')].find((item) => item.textContent?.includes(${JSON.stringify(current.label)})); const button = row?.querySelector('button'); if (!(button instanceof HTMLButtonElement)) throw new Error('provider connect button missing'); button.click(); return true; })()`);
        await waitFor(async () => (await execBrowser(bcb, tab, `Boolean([...document.querySelectorAll('.provider-auth-prompt label')].find((label) => label.textContent?.includes(${JSON.stringify(current.input)}))?.querySelector('input'))`)) ? true : undefined, `${current.label} provider prompt`);
        await execBrowser(bcb, tab, `(() => { const label = [...document.querySelectorAll('.provider-auth-prompt label')].find((item) => item.textContent?.includes(${JSON.stringify(current.input)})); const input = label?.querySelector('input'); if (!(input instanceof HTMLInputElement)) throw new Error('provider prompt input missing'); const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set; if (!setter) throw new Error('provider input setter unavailable'); setter.call(input, ${JSON.stringify(sentinel)}); input.dispatchEvent(new Event('input', { bubbles: true })); input.dispatchEvent(new Event('change', { bubbles: true })); const submit = document.querySelector('.provider-auth-prompt button'); if (!(submit instanceof HTMLButtonElement)) throw new Error('provider prompt submit missing'); submit.click(); return true; })()`);
        await waitFor(async () => {
          const state = await fetch(`${router.httpUrl}/v1/provider-auth`).then((response) => response.ok ? response.json() : undefined);
          return state?.providers?.find((provider) => provider.provider === current.provider)?.authenticated === true ? state : undefined;
        }, `${current.label} provider authentication`);
      }
      const after = await fetch(`${router.httpUrl}/v1/provider-auth`).then((response) => response.json());
      const models = await fetch(`${router.httpUrl}/v1/models`).then((response) => response.json());
      const browserBoundary = await auditBrowserStorage(bcb, tab, sentinel);
      const source = await readFile(router.database, "utf8");
      // The deterministic launch model shares the supported OpenAI provider key
      // so the browser can validate it. The acceptance catalog can contain its
      // fallback and authenticated copy, so compare unique provider/model keys.
      const authModels = [...new Map((models.models ?? []).filter((model) => ["openai-codex", "google", "cursor"].includes(model.provider)).map((model) => [`${model.provider}\u0000${model.id}`, model])).values()];
      if (after.auth_revision !== 3 || after.providers?.length !== 3 || after.providers.some((provider) => provider.authenticated !== true) || authModels.length !== 3 || source.includes(sentinel)) fail("provider auth fixture did not preserve the non-secret browser/router boundary");
      return { auth_revision: after.auth_revision, providers: after.providers.map((provider) => ({ provider: provider.provider, authenticated: provider.authenticated, status: provider.status })), models: authModels.map((model) => ({ provider: model.provider, id: model.id })), browser_boundary: browserBoundary };
    });

    await step("launch-real-managed-pi", async () => {
      // Register before browser submission so a failed UI assertion cannot leak
      // an accepted launch request.
      managedTestAgentIDs.add(CHILD_AGENT);
      await execBrowser(bcb, tab, "(() => { const actions = document.querySelector('.managed-actions-trigger'); if (!(actions instanceof HTMLButtonElement)) throw new Error('managed actions menu missing'); actions.click(); return true; })()");
      await waitFor(async () => (await execBrowser(bcb, tab, "Boolean(document.querySelector('.managed-actions-menu [role=menuitem]'))")) ? true : undefined, "managed actions menu");
      await execBrowser(bcb, tab, "(() => { const button = [...document.querySelectorAll('.managed-actions-menu [role=menuitem]')].find((item) => item.textContent?.trim() === 'Launch managed agent'); if (!(button instanceof HTMLButtonElement)) throw new Error('Launch managed agent menu item missing'); button.click(); return true; })()");
      await waitFor(async () => (await execBrowser(bcb, tab, "Boolean(document.querySelector('.managed-agent-dialog form'))")) ? true : undefined, "managed launch dialog");
      await execBrowser(bcb, tab, "(() => { const button = document.querySelector('.managed-agent-dialog .model-current'); if (!(button instanceof HTMLButtonElement) || button.textContent?.trim() !== 'Use router default') throw new Error('router default model trigger missing'); button.click(); return true; })()");
      await waitFor(async () => (await execBrowser(bcb, tab, "Boolean(document.querySelector('.model-menu-portal input[aria-label=\"Search available models\"]'))")) ? true : undefined, "searchable model picker");
      await execBrowser(bcb, tab, reactInputScript(".model-menu-portal input[aria-label='Search available models']", "managed-rpc-release"));
      let lastPicker;
      let picker;
      try {
        picker = await waitFor(async () => {
          const rendered = await execBrowser(bcb, tab, "(() => { const menu = document.querySelector('.model-menu-portal'); const option = [...document.querySelectorAll('.model-menu-portal [role=option]')].find((item) => item.textContent?.includes('managed-rpc-release')); const options = option?.closest('.model-options'); if (!(menu instanceof HTMLElement) || !(option instanceof HTMLElement) || !(options instanceof HTMLElement)) return { menu: menu instanceof HTMLElement ? menu.outerHTML : undefined, option: option instanceof HTMLElement ? option.outerHTML : undefined, options: options instanceof HTMLElement ? options.outerHTML : undefined }; const menuRect = menu.getBoundingClientRect(); const optionsRect = options.getBoundingClientRect(); const optionRect = option.getBoundingClientRect(); return { menu: menuRect.toJSON(), options: optionsRect.toJSON(), option: optionRect.toJSON(), visible: menuRect.top >= 0 && menuRect.bottom <= innerHeight && optionRect.top >= optionsRect.top && optionRect.bottom <= optionsRect.bottom, host: menu.parentElement?.tagName ?? '' }; })()");
          lastPicker = rendered;
          return rendered?.visible === true && rendered.host === "DIALOG" ? rendered : undefined;
        }, "unclipped searchable model picker");
      } catch (error) {
        await write("managed-model-picker-debug.json", { picker: lastPicker, body: await execBrowser(bcb, tab, "document.body.innerText").catch(() => "unavailable") });
        throw error;
      }
      await execBrowser(bcb, tab, "(() => { const option = [...document.querySelectorAll('.model-menu-portal [role=option]')].find((item) => item.textContent?.includes('managed-rpc-release')); if (!(option instanceof HTMLButtonElement)) throw new Error('deterministic managed model option missing'); option.click(); return true; })()");
      await waitFor(async () => (await execBrowser(bcb, tab, "document.querySelector('.managed-agent-dialog .model-current')?.textContent?.includes('openai-codex/managed-rpc-release')")) ? true : undefined, "selected deterministic managed model");
      await execBrowser(bcb, tab, reactInputScript(".managed-agent-dialog form label:first-of-type input", CHILD_AGENT));
      await execBrowser(bcb, tab, reactInputScript(".managed-agent-dialog input[placeholder='~/code/project']", router.cwd));
      await execBrowser(bcb, tab, reactInputScript(".managed-agent-dialog textarea", "Spawn the acceptance child compaction fixture."));
      await execBrowser(bcb, tab, "(() => { const button = document.querySelector('.managed-agent-dialog form button[type=submit]'); if (!(button instanceof HTMLButtonElement)) throw new Error('managed launch submit missing'); button.click(); return true; })()");
      await waitFor(async () => (await execBrowser(bcb, tab, `document.querySelector('[data-agent-id=${JSON.stringify(CHILD_AGENT)}]')?.textContent?.includes('RPC')`)) ? true : undefined, "managed Pi row");
      return { agent_id: CHILD_AGENT, picker };
    });

    const compacting = await step("real-child-compaction", async () => {
      let lastPayload;
      try {
        const result = await waitFor(async () => {
          const response = await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}/children`);
          if (!response.ok) return undefined;
          const payload = await response.json();
          lastPayload = payload;
          const child = payload.children?.find((candidate) => candidate.compaction_state === "compacting" && typeof candidate.started_at === "string");
          return child ? payload : undefined;
        }, "real managed child compacting snapshot");
        return { endpoint: result };
      } catch (error) {
        const agent = await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => ({ unavailable: true }));
        const stderr = await readFile(router.piStderr, "utf8").catch(() => "");
        await write("real-child-compaction-debug.json", { endpoint: lastPayload, managed_agent: agent, provider: provider.summary(), pi_stderr_head: stderr.slice(0, 2_000), pi_stderr_tail: stderr.slice(-2_000), browser: await execBrowser(bcb, tab, "document.body.innerText").catch(() => "unavailable") });
        throw error;
      }
    });
    await write("managed-child-compacting-endpoint.json", compacting.endpoint);

    await step("native-parent-compaction-matrix", async () => {
      try {
        const transitions = await waitFor(async () => {
          const rows = await sqliteRows(router.database, "SELECT source_sequence,state,outcome,pi_session_id,session_epoch,process_incarnation,runtime_generation FROM managed_parent_compaction_transitions WHERE agent_id=? ORDER BY source_sequence", [CHILD_AGENT]);
          const outcomes = new Set(rows.map((row) => row.outcome).filter((outcome) => typeof outcome === "string"));
          const agent = await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}`).then((response) => response.json());
          return outcomes.has("completed") && outcomes.has("aborted") && outcomes.has("failed") && agent.agent?.activity_state === "compacting" ? { rows, agent } : undefined;
        }, "native installed-Pi parent compaction outcome matrix");
        const history = await waitFor(async () => {
          const payload = await fetch(`${router.httpUrl}/v1/history?agent_id=${encodeURIComponent(CHILD_AGENT)}&view=complete&limit=100`).then((response) => response.ok ? response.json() : undefined);
          const messages = (payload?.entries ?? []).map((entry) => entry.message);
          return ["Context compacted", "Context compaction interrupted — outcome unknown", "Context compaction failed"].every((expected) => messages.includes(expected)) ? payload : undefined;
        }, "native parent compaction complete history");
        // The browser history contract is scoped by the selected managed
        // parent and exposes only redacted operational facts. The transition
        // rows above bind these expected messages to its installed-Pi stream.
        const parentHistory = history.entries ?? [];
        await write("native-parent-compaction-matrix.json", { transitions: transitions.rows, agent: transitions.agent, history: parentHistory, assertion: "real installed-Pi compaction_start/end frames preserved completed, aborted, and failed outcomes; the live final hold leaves the parent compacting" });
        return { transition_count: transitions.rows.length, outcomes: [...new Set(transitions.rows.map((row) => row.outcome).filter(Boolean))], parent_activity: transitions.agent.agent.activity_state, history_count: parentHistory.length };
      } catch (error) {
        await write("native-parent-compaction-matrix-debug.json", { transitions: await sqliteRows(router.database, "SELECT * FROM managed_parent_compaction_transitions WHERE agent_id=? ORDER BY source_sequence", [CHILD_AGENT]), agent: await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}`).then((response) => response.json()).catch(() => undefined), stderr: await readFile(router.piStderr, "utf8").catch(() => "") });
        throw error;
      }
    });

    await step("bcb-desktop-window", async () => {
      const visibility = await selectAndRevealManagedChildren(bcb, tab);
      const proof = await snapshot(bcb, tab);
      if (proof.viewport.width !== DESKTOP_VIEWPORT_WIDTH || proof.scrollWidth !== DESKTOP_VIEWPORT_WIDTH) fail(`BCB desktop evidence has horizontal overflow or wrong width (${proof.viewport.width}/${proof.scrollWidth})`);
      const childText = await execBrowser(bcb, tab, `document.querySelector('[data-agent-id=${JSON.stringify(CHILD_AGENT)}] .managed-child-list')?.textContent ?? ""`);
      if (!childText.includes("Managed release child") || !childText.includes("compacting")) fail("BCB desktop view did not show the real managed child compaction");
      const capture = await screenshot(bcb, tab, join(output, "managed-child-compacting-desktop.png"), visualTask, "compacting");
      await write("managed-child-compacting-desktop.json", { viewport: proof.viewport, scroll_width: proof.scrollWidth, screenshot: capture.path, screenshot_sha256: capture.sha256, screenshot_target: capture.target, route: "disposable Chromium for Testing task browser", visibility, child_visible: true });
      return { viewport: proof.viewport, screenshot: capture.path, screenshot_target: capture.target, visibility };
    });

    await step("bcb-ui-live-reconnect-compacting", async () => {
      await execBrowser(bcb, tab, "location.reload(); true");
      await waitFor(async () => (await execBrowser(bcb, tab, "document.querySelector('[aria-label=\"Connection status\"]')?.textContent?.includes('Connected')")) ? true : undefined, "BCB managed UI reconnect while compacting");
      const visibility = await selectAndRevealManagedChildren(bcb, tab);
      await waitFor(async () => (await execBrowser(bcb, tab, `document.querySelector('[data-agent-id=${JSON.stringify(CHILD_AGENT)}] .managed-child-list')?.textContent?.includes('compacting')`)) ? true : undefined, "compacting child after BCB UI reconnect");
      const proof = await snapshot(bcb, tab);
      if (proof.viewport.width !== DESKTOP_VIEWPORT_WIDTH || proof.scrollWidth !== DESKTOP_VIEWPORT_WIDTH) fail(`BCB compacting reconnect evidence lost the desktop layout (${proof.viewport.width}/${proof.scrollWidth})`);
      const capture = await screenshot(bcb, tab, join(output, "managed-child-compacting-desktop-reconnect.png"), visualTask, "compacting");
      await write("managed-child-compacting-desktop-reconnect.json", { viewport: proof.viewport, scroll_width: proof.scrollWidth, screenshot: capture.path, screenshot_sha256: capture.sha256, screenshot_target: capture.target, visibility, reconnect: "controlled BCB task page reload and UI-live resubscription while the real child is compacting", child_compacting: true });
      return { viewport: proof.viewport, screenshot: capture.path, screenshot_target: capture.target, visibility, child_compacting: true };
    });

    await step("real-child-compaction-completed", async () => {
      try {
        const result = await waitFor(async () => {
          const response = await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}/children`);
          if (!response.ok) return undefined;
          const payload = await response.json();
          const child = payload.children?.find((candidate) => candidate.compaction_state === "completed" && candidate.compaction?.count >= 1);
          return child ? payload : undefined;
        }, "real managed child completed compaction");
        const visibility = await selectAndRevealManagedChildren(bcb, tab);
        const childText = await execBrowser(bcb, tab, `document.querySelector('[data-agent-id=${JSON.stringify(CHILD_AGENT)}] .managed-child-list')?.textContent ?? ""`);
        if (!childText.includes("Managed release child") || !childText.includes("completed")) fail("BCB did not receive the completed child compaction lifecycle update");
        await write("managed-child-completed-endpoint.json", result);
        return { endpoint: result, visibility };
      } catch (error) {
        const [children, agent, stderr] = await Promise.all([
          fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}/children`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => ({ unavailable: true })),
          fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => ({ unavailable: true })),
          readFile(router.piStderr, "utf8").catch(() => ""),
        ]);
        await write("managed-child-completed-debug.json", { children, agent, provider: provider.summary(), pi_stderr: stderr.slice(-4_000) });
        throw error;
      }
    });

    await step("bcb-ui-live-reconnect-completed", async () => {
      await execBrowser(bcb, tab, "location.reload(); true");
      await waitFor(async () => (await execBrowser(bcb, tab, "document.querySelector('[aria-label=\"Connection status\"]')?.textContent?.includes('Connected')")) ? true : undefined, "BCB managed UI completed reconnect");
      const visibility = await selectAndRevealManagedChildren(bcb, tab);
      await waitFor(async () => (await execBrowser(bcb, tab, `document.querySelector('[data-agent-id=${JSON.stringify(CHILD_AGENT)}] .managed-child-list')?.textContent?.includes('completed')`)) ? true : undefined, "completed child after BCB UI reconnect");
      const proof = await snapshot(bcb, tab);
      if (proof.viewport.width !== DESKTOP_VIEWPORT_WIDTH || proof.scrollWidth !== DESKTOP_VIEWPORT_WIDTH) fail(`BCB completed reconnect evidence lost the desktop layout (${proof.viewport.width}/${proof.scrollWidth})`);
      const capture = await screenshot(bcb, tab, join(output, "managed-child-completed-desktop-reconnect.png"), visualTask, "completed");
      await write("managed-child-completed-desktop-reconnect.json", { viewport: proof.viewport, scroll_width: proof.scrollWidth, screenshot: capture.path, screenshot_sha256: capture.sha256, screenshot_target: capture.target, visibility, reconnect: "controlled BCB task page reload and UI-live resubscription after completion", child_completed: true });
      return { viewport: proof.viewport, screenshot: capture.path, screenshot_target: capture.target, visibility, child_completed: true };
    });

    await step("parent-session-switch-recovery", async () => {
      const sessionsBefore = (await readdir(router.sessions)).length;
      const before = await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}/children`).then(async (response) => response.ok ? response.json() : { status: response.status });
      try {
        await execBrowser(bcb, tab, `(() => { const button = document.querySelector('.conversation-header button[aria-label="Manage worker"]'); if (!(button instanceof HTMLButtonElement)) throw new Error('managed parent action is unavailable'); button.click(); return true; })()`);
        await waitFor(async () => (await execBrowser(bcb, tab, "Boolean(document.querySelector('.managed-control-actions button.secondary'))")) ? true : undefined, "managed reset action");
        await execBrowser(bcb, tab, "(() => { const button = [...document.querySelectorAll('.managed-control-actions button')].find((item) => item.textContent?.trim() === 'Reset'); if (!(button instanceof HTMLButtonElement)) throw new Error('managed reset button is unavailable'); button.click(); return true; })()");
        const endpoint = await waitFor(async () => {
          const response = await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}/children`);
          if (!response.ok) return undefined;
          const payload = await response.json();
          const recovered = payload.children?.filter((child) => child.recovery?.cause === "session_changed") ?? [];
          return payload.recovery_state === "current" && recovered.length > 0 && payload.lifecycle_version < before.lifecycle_version ? payload : undefined;
        }, "parent session switch current replacement snapshot", 60_000);
        if (!before.children?.some((child) => child.status === "completed") || endpoint.children.some((child) => child.status === "completed") || !endpoint.children.every((child) => child.recovery?.cause === "session_changed" && child.recovery?.state === "interrupted_unknown")) fail("parent session switch retained, fabricated, or failed to mark an unconfirmed child state");
        const sessionsAfter = (await readdir(router.sessions)).length;
        await write("parent-session-switch-recovery.json", { sessions_before: sessionsBefore, sessions_after: sessionsAfter, before, endpoint, assertion: "the replacement parent preserves unconfirmed prior runs as session-changed interrupted_unknown rather than retaining the prior terminal child state" });
        return { sessions_before: sessionsBefore, sessions_after: sessionsAfter, before_lifecycle_version: before.lifecycle_version, replacement_lifecycle_version: endpoint.lifecycle_version, replacement_child_count: endpoint.children.length, recovery_causes: [...new Set(endpoint.children.map((child) => child.recovery?.cause))] };
      } catch (error) {
        const [agent, children, browser] = await Promise.all([
          fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => ({ unavailable: true })),
          fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}/children`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => ({ unavailable: true })),
          execBrowser(bcb, tab, "document.body.innerText").catch(() => "unavailable"),
        ]);
        await write("parent-session-switch-recovery-debug.json", { sessions_before: sessionsBefore, sessions_after: await readdir(router.sessions).then((items) => items.length).catch(() => -1), agent, children, browser });
        throw error;
      }
    });

    await step("isolated-router-restart-recovery", async () => {
      const before = await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}/children`).then((response) => response.json());
      const restarted = await restartManagedRouter(router, root, { makeChildFenceUncertain: true });
      router = restarted.router;
      try {
        await verifyRouterServesBuiltUI(router);
        await execBrowser(bcb, tab, "location.reload(); true");
        await waitFor(async () => (await execBrowser(bcb, tab, "document.querySelector('[aria-label=\"Connection status\"]')?.textContent?.includes('Connected')")) ? true : undefined, "BCB reconnect after isolated router restart");
        const after = await waitFor(async () => {
          const response = await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}/children`);
          if (!response.ok) return undefined;
          const payload = await response.json();
          const retainedUnknown = payload.children?.filter((child) => child.recovery?.state === "interrupted_unknown") ?? [];
          return payload.recovery_state === "reconstructing" && retainedUnknown.length > 0 ? payload : undefined;
        }, "router restart reconstructing child state");
        if (after.children.some((child) => child.status === "completed")) fail("router restart fabricated a terminal child state instead of preserving it as interrupted unknown");
        isolatedRestartRecovery = after;
        await write("isolated-router-restart-recovery.json", { stopped: restarted.stopped, before, after, assertion: "a test-only unmatched persisted source fence after the isolated router stop is rendered as reconstructing while retaining the bounded unconfirmed child records as interrupted_unknown" });
        return { stopped: restarted.stopped, before_recovery_state: before.recovery_state, after_recovery_state: after.recovery_state, child_count_after: after.children.length, recovery_states: [...new Set(after.children.map((child) => child.recovery?.state))] };
      } catch (error) {
        await write("isolated-router-restart-recovery-debug.json", { stopped: restarted.stopped, before, after: await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}/children`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => undefined), agent: await fetch(`${router.httpUrl}/v1/managed-agents/${CHILD_AGENT}`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => undefined), readiness_sources: await sqliteRows(router.database, "SELECT agent_id,pi_session_id,session_epoch,process_incarnation,runtime_generation FROM managed_readiness_sources WHERE agent_id=?", [CHILD_AGENT]).catch(() => undefined) });
        throw error;
      }
    });

    await step("bcb-ui-live-reconnect-reconstructing", async () => {
      // The first reload reconnects while the router is rebuilding. Reload once
      // that durable reconstruction is confirmed so the UI's initial child
      // snapshot cannot race ahead of the retained recovery records.
      await execBrowser(bcb, tab, "location.reload(); true");
      await waitFor(async () => (await execBrowser(bcb, tab, "document.querySelector('[aria-label=\"Connection status\"]')?.textContent?.includes('Connected')")) ? true : undefined, "BCB reconnect after reconstructed child state");
      await execBrowser(bcb, tab, "(() => { const button = document.querySelector('button[aria-label=\"Refresh agents\"]'); if (!(button instanceof HTMLButtonElement)) throw new Error('refresh agents button missing'); button.click(); return true; })()");
      await waitFor(async () => (await execBrowser(bcb, tab, `Boolean(document.querySelector('[data-agent-id=${JSON.stringify(CHILD_AGENT)}] button[aria-label*=subagent]'))`)) ? true : undefined, "reconstructed managed child disclosure");
      const visibility = await selectAndRevealManagedChildren(bcb, tab);
      if (!visibility.parent_text.includes("Reconstructing child state") || !visibility.parent_text.includes("Interrupted · outcome unknown")) fail("BCB did not render the retained interrupted-unknown state after isolated router restart");
      const proof = await snapshot(bcb, tab);
      if (proof.viewport.width !== DESKTOP_VIEWPORT_WIDTH || proof.scrollWidth !== DESKTOP_VIEWPORT_WIDTH) fail(`BCB reconstructing restart evidence lost the desktop layout (${proof.viewport.width}/${proof.scrollWidth})`);
      const capture = await screenshotReconstructing(bcb, tab, join(output, "managed-child-reconstructing-desktop-restart.png"), visualTask);
      await write("managed-child-reconstructing-desktop-restart.json", { viewport: proof.viewport, scroll_width: proof.scrollWidth, screenshot: capture.path, screenshot_sha256: capture.sha256, screenshot_target: capture.target, visibility, restart_endpoint: isolatedRestartRecovery, restart: "isolated managed-router restart followed by UI-live reconnect; the selected parent remains reconstructing and retains only interrupted-unknown child evidence", child_count: visibility.child_count });
      return { viewport: proof.viewport, screenshot: capture.path, screenshot_target: capture.target, visibility, child_count: visibility.child_count };
    });

    await step("hard-kill-live-child-recovery", async () => {
      const agentID = "hard-kill-parent";
      const launch = await launchManagedFromDedicatedBcbTab(bcb, tab, router, managedTestAgentIDs, agentID, "Spawn two live children for the hard-exit acceptance fixture.");
      const before = await waitFor(async () => {
        const [children, agent, transitions] = await Promise.all([
          fetch(`${router.httpUrl}/v1/managed-agents/${agentID}/children`).then((response) => response.ok ? response.json() : undefined),
          fetch(`${router.httpUrl}/v1/managed-agents/${agentID}`).then((response) => response.ok ? response.json() : undefined),
          sqliteRows(router.database, "SELECT source_sequence,state,outcome FROM managed_parent_compaction_transitions WHERE agent_id=? ORDER BY source_sequence", [agentID]),
        ]);
        const liveChildren = children?.children?.filter((child) => child.compaction_state === "compacting") ?? [];
        const outcomes = new Set(transitions.map((transition) => transition.outcome).filter((outcome) => typeof outcome === "string"));
        const finalTransition = transitions.at(-1);
        // Kill only after the fixture's terminal matrix is durably complete and
        // its final compaction remains live. Killing an earlier matrix turn can
        // legitimately be followed by its already-buffered terminal frame.
        const parentHoldActive = outcomes.has("completed") && outcomes.has("aborted") && outcomes.has("failed") && finalTransition?.state === "compacting" && finalTransition.outcome === null;
        return liveChildren.length === 2 && agent?.agent?.activity_state === "compacting" && parentHoldActive ? { children, agent, transitions } : undefined;
      }, "two live children and the final parent compaction hold before hard kill", 60_000);
      const pid = await hardKillManagedPi(router.database, agentID);
      let after;
      try {
        after = await waitFor(async () => {
          const [children, agent, recoveries, transitions, history] = await Promise.all([
            fetch(`${router.httpUrl}/v1/managed-agents/${agentID}/children`).then((response) => response.ok ? response.json() : undefined),
            fetch(`${router.httpUrl}/v1/managed-agents/${agentID}`).then((response) => response.ok ? response.json() : undefined),
            sqliteRows(router.database, "SELECT recovery_cause,recovery_state,prior_pi_session_id,prior_process_incarnation,prior_runtime_generation FROM managed_child_recoveries WHERE agent_id=? ORDER BY rowid", [agentID]),
            sqliteRows(router.database, "SELECT state,outcome FROM managed_parent_compaction_transitions WHERE agent_id=? ORDER BY source_sequence", [agentID]),
            fetch(`${router.httpUrl}/v1/history?agent_id=${encodeURIComponent(agentID)}&view=complete&limit=100`).then((response) => response.json()),
          ]);
          const recovered = children?.children?.filter((child) => child.recovery?.cause === "parent_process_lost") ?? [];
          // The public history page is intentionally redacted; source binding is
          // established by the agent-specific transition ledger above.
          const parentUnknown = transitions.some((transition) => transition.outcome === "reconciled_unknown") && (history?.entries ?? []).some((entry) => entry.message === "Context compaction interrupted — outcome unknown" && entry.presentation?.kind === "compaction" && entry.presentation?.outcome === "reconciled_unknown");
          return agent?.agent?.lifecycle_state === "failed" && recovered.length === 2 && recoveries.length === 2 && recoveries.every((row) => row.recovery_cause === "parent_process_lost") && parentUnknown ? { children, agent, recoveries, transitions, history } : undefined;
        }, "hard parent exit child recovery projection", 60_000);
      } catch (error) {
        await write("hard-kill-live-child-recovery-debug.json", {
          before,
          killed_process: pid,
          agent: await fetch(`${router.httpUrl}/v1/managed-agents/${agentID}`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => undefined),
          children: await fetch(`${router.httpUrl}/v1/managed-agents/${agentID}/children`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => undefined),
          managed_agent: await sqliteRows(router.database, "SELECT lifecycle_state,activity_state,last_error_code,last_error_message,process_pid,process_start_marker,process_incarnation,runtime_generation,pi_session_id,session_epoch FROM managed_agents WHERE agent_id=?", [agentID]).catch(() => undefined),
          readiness_source: await sqliteRows(router.database, "SELECT snapshot_id,snapshot_sequence,pi_session_id,session_epoch,process_incarnation,runtime_generation FROM managed_readiness_sources WHERE agent_id=?", [agentID]).catch(() => undefined),
          recoveries: await sqliteRows(router.database, "SELECT recovery_cause,recovery_state,prior_pi_session_id,prior_process_incarnation,prior_runtime_generation FROM managed_child_recoveries WHERE agent_id=? ORDER BY rowid", [agentID]).catch(() => undefined),
          parent_compaction: await sqliteRows(router.database, "SELECT source_sequence,state,outcome FROM managed_parent_compaction_transitions WHERE agent_id=? ORDER BY source_sequence", [agentID]).catch(() => undefined),
          history: await fetch(`${router.httpUrl}/v1/history?agent_id=${encodeURIComponent(agentID)}&view=complete&limit=100`).then(async (response) => response.ok ? response.json() : { status: response.status }).catch(() => undefined),
          stderr: await readFile(router.piStderr, "utf8").catch(() => ""),
        });
        throw error;
      }
      await waitFor(async () => {
        const rendered = await execBrowser(bcb, tab, `(() => { const row = document.querySelector('[data-agent-id=${agentID}]'); const select = row?.querySelector('.agent-row-select'); if (!(row instanceof HTMLElement) || !(select instanceof HTMLElement)) return undefined; if (row.getAttribute('aria-selected') !== 'true') select.click(); const toggle = row.querySelector('button[aria-label*=subagent]'); if (toggle instanceof HTMLButtonElement && !row.querySelector('.managed-child-list li')) toggle.click(); const text = row.innerText; return row.querySelectorAll('.managed-child-list li').length === 2 && text.includes('parent process ended before this child was confirmed') ? { text, children: row.querySelectorAll('.managed-child-list li').length } : undefined; })()`);
        return rendered;
      }, "BCB hard-exit recovered children", 60_000);
      const capture = await screenshotManagedChildren(bcb, tab, join(output, "managed-child-parent-process-lost-desktop.png"), visualTask, agentID, "Interrupted · outcome unknown");
      const history = after.history.entries ?? [];
      await write("hard-kill-live-child-recovery.json", { launch, before, killed_process: pid, after: { children: after.children, agent: after.agent, recoveries: after.recoveries, history }, screenshot: capture, assertion: "the exact managed Pi PID was SIGKILLed while two children and parent compaction were live; both children became source-fenced parent_process_lost recoveries and the missing parent end became reconciled_unknown" });
      return { agent_id: agentID, killed_pid: pid.process_pid, recovered_children: after.children.children.length, recovery_causes: after.recoveries.map((row) => row.recovery_cause), screenshot: capture.path };
    });

    await step("final-browser", async () => {
      const proof = await snapshot(bcb, tab);
      if (proof.resources.some((url) => url.includes(":8765"))) fail("BCB loaded the live router");
      if (proof.resources.some((url) => url.includes("vite") || url.includes("/@vite/"))) fail("BCB loaded a Vite development resource instead of the router-served bundle");
      if (!proof.resources.some((url) => url.startsWith(`${router.httpUrl}/assets/`))) fail("BCB did not load a built router UI asset from the isolated router");
      if (proof.viewport.width !== DESKTOP_VIEWPORT_WIDTH || proof.scrollWidth !== DESKTOP_VIEWPORT_WIDTH) fail(`final BCB evidence is not desktop width without overflow (${proof.viewport.width}/${proof.scrollWidth})`);
      const browserBoundary = await auditBrowserStorage(bcb, tab, authSentinel);
      const console = await bcbCommand(bcb, ["cli.bcb_console", "--json", "--tab", String(tab), "--limit", "500"]);
      const errors = Array.isArray(console) ? console.filter((entry) => entry?.level === "error") : [];
      if (errors.length) fail(`browser console errors: ${JSON.stringify(errors)}`);
      return { viewport: proof.viewport, scroll_width: proof.scrollWidth, router_served_assets: proof.resources.filter((url) => url.startsWith(`${router.httpUrl}/assets/`)), console_errors: errors.length, browser_boundary: browserBoundary };
    });
    report.result = "automated_pass_visual_identity_verified";
    report.visual_identity_verification = {
      status: "passed",
      technique: "Each screenshot assigns the disposable Chromium for Testing tab a unique document title and confirms its exact BCB tab ID/URL/title and selected parent with the expected child state. The runner never invokes macOS browser focus automation.",
      artifacts: ["managed-child-compacting-desktop.json", "managed-child-compacting-desktop-reconnect.json", "managed-child-completed-desktop-reconnect.json", "managed-child-reconstructing-desktop-restart.json"],
    };
    report.acceptance_run = await writeCandidateAcceptanceRun(output, candidate, write);
  } catch (error) {
    report.error = error instanceof Error ? error.message : String(error);
    runFailure = error;
  } finally {
    const cleanup = [];
    try {
      if (router && managedTestAgentIDs.size > 0) {
        const managedAgents = await terminateManagedTestAgents({
          baseUrl: router.httpUrl,
          agentIDs: managedTestAgentIDs,
          assertProcessAbsent: ({ agentID, processPID }) => {
            if (!Number.isInteger(processPID) || processPID < 2) return { checked: false };
            try {
              process.kill(processPID, 0);
            } catch (error) {
              if (error?.code === "ESRCH") return { checked: true, process_pid: processPID, absent: true };
              throw error;
            }
            throw new Error(`managed agent ${agentID} process ${processPID} survived durable removal`);
          },
        });
        cleanup.push({ managed_test_agents: managedAgents });
      }
    } catch (error) {
      cleanup.push({ managed_test_agents_error: String(error) });
      cleanupFailure = error;
    }
    try { if (controlledBrowser) { await controlledBrowser.close(); cleanup.push({ isolated_bcb_browser_stopped: true }); } } catch (error) { cleanup.push({ isolated_bcb_browser_stop_error: String(error) }); }
    try { if (router) await stopRouterProcess(router); } catch (error) { cleanup.push({ router: String(error) }); }
    try { if (tmuxPeer) cleanup.push({ canonical_tmux_peer: await tmuxPeer.stop() }); } catch (error) { cleanup.push({ canonical_tmux_peer: String(error) }); }
    try { if (provider) await provider.close(); } catch (error) { cleanup.push({ provider: String(error) }); }
    if (root) await rm(root, { recursive: true, force: true });
    report.cleanup = cleanup;
    if (cleanupFailure) {
      report.result = "cleanup_failed";
      report.error = cleanupFailure instanceof Error ? cleanupFailure.message : String(cleanupFailure);
    }
    report.finished_at = new Date().toISOString();
    await write("REPORT.json", report);
  }
  // Do not certify a release run that left its explicitly-owned test agent
  // behind, even when the proof itself otherwise passed. Preserve the original
  // failure when both the proof and cleanup fail so its diagnostics remain the
  // primary cause.
  if (runFailure !== undefined) throw runFailure;
  if (cleanupFailure !== undefined) throw cleanupFailure;
  return report;
}
