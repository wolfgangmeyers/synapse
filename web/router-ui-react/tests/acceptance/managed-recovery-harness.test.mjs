import test from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { performance } from "node:perf_hooks";
import { fileURLToPath } from "node:url";
import {
  captureSharedResourceSnapshot,
  execBrowser,
  persistRouterRecoveryEvidence,
  recoveryPreflight,
} from "./bcb-managed-recovery-runner.mjs";
import * as managedRecoveryHarness from "./managed-recovery-harness.mjs";
import {
  DEADLINE_MS,
  assertArtifactRedacted,
  assertCleanupComplete,
  assertSharedResourcesUnchanged,
  parseManagedRecoveryArguments,
  runDeadlineBoundOperation,
  runManagedRecoverySequence,
  serializeCleanup,
  signalRouterAtBoundary,
  validateBarrier,
  validateCompletion,
  validateFreshness,
} from "./managed-recovery-harness.mjs";

const fence = Object.freeze({
  pi_session_id: "session-private",
  runtime_generation: "generation-after",
  process_incarnation: "incarnation-after",
  process_pid: 4021,
  process_start_marker: "start-after",
  updated_at: "2030-01-01T00:00:01.000Z",
  projection_version: 12,
});

function persistedReceipt(overrides = {}) {
  return {
    event: "managed_recovery_completion_persisted",
    receipt_id: "receipt-1",
    trial_id: "trial-1",
    token: "token-1",
    agent_id: "agent-1",
    observed_monotonic_ms: 121,
    provider_request_seq: 2,
    runtime_generation: fence.runtime_generation,
    process_incarnation: fence.process_incarnation,
    ...overrides,
  };
}

function validation(overrides = {}) {
  return {
    trial: { trial_id: "trial-1", workload_token: "token-1", agent_id: "agent-1", pi_session_id: fence.pi_session_id },
    restart_boundary: 100,
    deadline: 30_100,
    current_fence: fence,
    lifecycle: { lifecycle_state: "running", accepting: true, activity_state: "idle" },
    persisted_receipt: persistedReceipt(),
    provider_completion: { completed: true, active: false, sequence: 2 },
    transcript_completion: true,
    ...overrides,
  };
}

function taskResources(number) {
  return {
    router: `router-${number}`,
    database: `database-${number}`,
    bcb_bridge: `bcb-bridge-${number}`,
    browser_profile: `browser-profile-${number}`,
    browser_tab: `browser-tab-${number}`,
    workload_token: `workload-token-${number}`,
  };
}

test("managed recovery preflight does not require managed-release provenance lock environment", async () => {
  const root = await mkdtemp(join(tmpdir(), "managed-recovery-preflight-"));
  try {
    const piBin = join(root, "pi");
    const subagents = join(root, "pi-subagents.ts");
    const bcbRoot = join(root, "bcb");
    const python = join(bcbRoot, ".venv", "bin", "python3");
    await mkdir(join(bcbRoot, "server"), { recursive: true });
    await mkdir(join(bcbRoot, "extension"), { recursive: true });
    await mkdir(join(bcbRoot, ".venv", "bin"), { recursive: true });
    await Promise.all([
      writeFile(piBin, "#!/bin/sh\n", { mode: 0o700 }),
      writeFile(subagents, "export {};\n"),
      writeFile(python, "#!/bin/sh\n", { mode: 0o700 }),
      writeFile(join(bcbRoot, "server", "bridge_server.py"), "# fixture\n"),
      writeFile(join(bcbRoot, "extension", "background.js"), "// fixture\n"),
    ]);
    const prerequisites = await recoveryPreflight({
      PATH: process.env.PATH,
      MECHA_PI_BIN: piBin,
      MECHA_PI_SUBAGENTS_EXTENSION: subagents,
      MECHA_BCB_ROOT: bcbRoot,
      MECHA_BCB_PYTHON: python,
    });
    assert.deepEqual(prerequisites, {
      nodeBin: await import("node:fs/promises").then(({ realpath }) => realpath(process.execPath)),
      piBin,
      subagents,
      subagentsGitSHA: null,
      subagentsVersion: null,
      python,
    });
  } finally {
    await rm(root, { recursive: true, force: true });
  }
});

test("managed recovery browser execution keeps the 45 second startup default and adds outer watchdog grace", async () => {
  const calls = [];
  const expected = { status: 202, body: { accepted: true } };
  const result = await execBrowser(
    { python: "/task/bcb/python" },
    17,
    "({ accepted: true })",
    undefined,
    async (...args) => { calls.push(args); return expected; },
  );
  assert.strictEqual(result, expected);
  assert.deepEqual(calls, [[
    { python: "/task/bcb/python" },
    ["cli.bcb_exec", "--timeout", "45", "--tab", "17", "({ accepted: true })"],
    46_000,
  ]]);
});

test("managed recovery completion poll keeps its one second internal timeout and adds outer watchdog grace", async () => {
  const calls = [];
  await execBrowser(
    { python: "/task/bcb/python" },
    17,
    "document.body.dataset.state",
    1_000,
    async (...args) => { calls.push(args); return "idle"; },
  );
  assert.deepEqual(calls, [[
    { python: "/task/bcb/python" },
    ["cli.bcb_exec", "--timeout", "1", "--tab", "17", "document.body.dataset.state"],
    2_000,
  ]]);
});

test("managed recovery runner reads extension-owned completion evidence after router startup", async () => {
  const runner = await readFile(fileURLToPath(new URL("./bcb-managed-recovery-runner.mjs", import.meta.url)), "utf8");
  const probe = await readFile(fileURLToPath(new URL("../../../../pi-extensions/mecha-rpc/tests/fixtures/managed-rpc-release-probe.mjs", import.meta.url)), "utf8");
  const routerStart = runner.indexOf("router = await startManagedRouter(");
  const completionGet = runner.indexOf("const completionURL = `${router.httpUrl}/v1/acceptance/managed-recovery?kind=completion&trial_id=");
  assert.ok(routerStart >= 0);
  assert.ok(completionGet > routerStart);
  assert.match(runner.slice(completionGet), /fetch\(completionURL, \{ signal: .* cache: "no-store" \}\)/u);
  assert.doesNotMatch(runner, /persistRouterRecoveryEvidence\(router\.httpUrl, "completion"/u);
  assert.match(runner, /startManagedRouter\(root, prerequisites, provider, staticUIRoot, "", \{/u);
  assert.doesNotMatch(runner, /startManagedRouter\(root, prerequisites, provider, staticUIRoot, "managed-recovery"/u);
  assert.doesNotMatch(probe, /persistManagedRecoveryCompletion|pi\.on\(["']agent_end["']|\/v1\/acceptance\/managed-recovery/u);
});

test("GET completion evidence validates as the router-persisted receipt", () => {
  const receipt = persistedReceipt();
  assert.strictEqual(validateCompletion(validation({ persisted_receipt: receipt })), receipt);
});

test("trial records and validates GET-only router-persisted evidence without the obsolete boolean", async () => {
  const runner = await readFile(fileURLToPath(new URL("./bcb-managed-recovery-runner.mjs", import.meta.url)), "utf8");
  const completionGetAt = runner.indexOf("const completionURL = `${router.httpUrl}/v1/acceptance/managed-recovery?kind=completion&trial_id=");
  const artifactAt = runner.indexOf("artifact.persisted_receipt = observed.receipt", completionGetAt);
  const validateAt = runner.indexOf("validateCompletion({", artifactAt);
  assert.ok(completionGetAt >= 0 && artifactAt > completionGetAt && validateAt > artifactAt);
  assert.match(runner.slice(completionGetAt, artifactAt), /fetch\(completionURL, \{ signal: .* cache: "no-store" \}\)/u);
  assert.match(runner.slice(validateAt, runner.indexOf("validateFreshness", validateAt)), /persisted_receipt: observed\.receipt/u);
  assert.doesNotMatch(runner, /persistRouterRecoveryEvidence\(router\.httpUrl, "completion"|response_persisted/u);
});

test("managed recovery passes the observed transcript result and recovery token", async () => {
  const runner = await readFile(fileURLToPath(new URL("./bcb-managed-recovery-runner.mjs", import.meta.url)), "utf8");
  assert.match(runner, /MECHA_MANAGED_RECOVERY_TOKEN: trial\.workload_token/u);
  assert.match(runner, /const transcriptCompletionPersisted = await sessionContains/u);
  assert.match(runner, /return \{ completedRequest, current, projection, dom, receipt, transcriptCompletionPersisted \}/u);
  assert.match(runner, /transcript_completion: observed\.transcriptCompletionPersisted/u);
  assert.doesNotMatch(runner, /transcript_shapes|contains_completion/u);
});

test("shared managed router wrapper uses recovery node runtime without changing release execution", async () => {
  const runner = await readFile(fileURLToPath(new URL("./bcb-managed-rpc-release-runner.mjs", import.meta.url)), "utf8");
  assert.match(runner, /prerequisites\.nodeBin === undefined/u);
  assert.match(runner, /exec \$\{JSON\.stringify\(prerequisites\.nodeBin\)\} \$\{JSON\.stringify\(prerequisites\.piBin\)\} "\$@"/u);
  assert.match(runner, /exec \$\{JSON\.stringify\(prerequisites\.piBin\)\} "\$@"/u);
});

test("ordinary trial failures capture diagnostics before cleanup without becoming timeouts", async () => {
  const runner = await readFile(fileURLToPath(new URL("./bcb-managed-recovery-runner.mjs", import.meta.url)), "utf8");
  const catchStart = runner.indexOf("  } catch (error) {", runner.indexOf("async function runTrial"));
  const cleanupStart = runner.indexOf("  } finally {", catchStart);
  const catchPath = runner.slice(catchStart, cleanupStart);
  assert.match(catchPath, /artifact\.result = "failed"/u);
  assert.match(catchPath, /artifact\.final_timeout_evidence === null/u);
  assert.match(catchPath, /await timeoutEvidence\(\{ markTimedOut: false \}\)/u);
  assert.match(runner, /pi_stderr: router === undefined \? "" : await readFile\(router\.piStderr, "utf8"\)/u);
});

test("timeout evidence records the persisted completion marker and private database fence without transcript contents", async () => {
  const runner = await readFile(fileURLToPath(new URL("./bcb-managed-recovery-runner.mjs", import.meta.url)), "utf8");
  const evidenceStart = runner.indexOf("  const timeoutEvidence = async");
  const evidenceEnd = runner.indexOf("\n  try {", evidenceStart);
  const evidencePath = runner.slice(evidenceStart, evidenceEnd);
  assert.match(evidencePath, /completion_marker_persisted: router !== undefined && await sessionContains\(router\.sessions, `\$\{COMPLETE_PREFIX\}\$\{trial\.workload_token\}`\)\.catch\(\(\) => false\)/u);
  assert.match(evidencePath, /const privateFenceAfterTimeout = router === undefined \? null : await databaseFence\(router\.database, trial\.agent_id, 2_000\)\.catch\(\(error\) => \(\{ error: error instanceof Error \? error\.message : String\(error\) \}\)\)/u);
  assert.match(evidencePath, /private_db_fence_after_timeout: privateFenceAfterTimeout/u);
  assert.doesNotMatch(evidencePath, /completion_marker_(?:contents|transcript)|transcript_contents/u);
});

test("managed recovery dispatcher requires exactly five trials", () => {
  assert.deepEqual(parseManagedRecoveryArguments(["--managed-recovery", "--trials", "5"]), { managedRecovery: true, trials: 5 });
  for (const args of [
    ["--managed-recovery"],
    ["--managed-recovery", "--trials", "4"],
    ["--managed-recovery", "--trials", "6"],
    ["--managed-recovery", "--trials", "nope"],
  ]) assert.throws(() => parseManagedRecoveryArguments(args), /--managed-recovery --trials 5/);
});

test("signal false fails at the restart boundary", () => {
  const child = { pid: 123, kill: () => false };
  assert.throws(() => signalRouterAtBoundary(child, () => 41), /SIGTERM returned false/);
});

test("signal throw fails at the restart boundary", () => {
  const child = { pid: 123, kill: () => { throw new Error("signal denied"); } };
  assert.throws(() => signalRouterAtBoundary(child, () => 42), /SIGTERM threw: signal denied/);
});

test("successful signal fixes the absolute 30 second deadline", () => {
  const calls = [];
  const result = signalRouterAtBoundary({ pid: 123, kill: (signal) => { calls.push(signal); return true; } }, () => 900.5);
  assert.deepEqual(calls, ["SIGTERM"]);
  assert.deepEqual(result, { restart_boundary: 900.5, deadline: 900.5 + DEADLINE_MS });
});

test("timeout captures evidence before cleanup and rejects late success", async () => {
  const order = [];
  let finish;
  const operation = new Promise((resolve) => { finish = resolve; });
  await assert.rejects(runDeadlineBoundOperation({
    operation,
    deadline: performance.now() + 10,
    captureTimeoutEvidence: async () => { order.push("evidence"); return { router: "stale" }; },
    cleanup: async () => { order.push("cleanup"); return { complete: true }; },
  }), (error) => {
    assert.equal(error.code, "MANAGED_RECOVERY_TIMEOUT");
    assert.deepEqual(error.timeoutEvidence, { router: "stale" });
    assert.deepEqual(error.cleanup, { complete: true });
    return true;
  });
  finish({ status: "late success" });
  await new Promise((resolve) => setTimeout(resolve, 5));
  assert.deepEqual(order, ["evidence", "cleanup"]);
});

test("absolute deadline wins when the operation rejects while timeout evidence is still being captured", async () => {
  let failOperation;
  let releaseEvidence;
  const operation = new Promise((_, reject) => { failOperation = reject; });
  const evidenceGate = new Promise((resolve) => { releaseEvidence = resolve; });
  const result = runDeadlineBoundOperation({
    operation,
    deadline: performance.now() + 5,
    captureTimeoutEvidence: async () => { await evidenceGate; return { router: "late" }; },
    cleanup: async () => ({ complete: true }),
  });
  const assertion = assert.rejects(result, (error) => {
    assert.equal(error.code, "MANAGED_RECOVERY_TIMEOUT");
    assert.deepEqual(error.timeoutEvidence, { router: "late" });
    return true;
  });
  await new Promise((resolve) => setTimeout(resolve, 15));
  failOperation(new Error("late BCB subprocess watchdog"));
  await new Promise((resolve) => setTimeout(resolve, 0));
  releaseEvidence();
  await assertion;
});

test("deadline timeout and final cleanup callers await the same populated cleanup promise", async () => {
  let cleanupCalls = 0;
  let releaseCleanup;
  let markCleanupStarted;
  const cleanupStarted = new Promise((resolve) => { markCleanupStarted = resolve; });
  const cleanupGate = new Promise((resolve) => { releaseCleanup = resolve; });
  const cleanupResult = { complete: true };
  const cleanup = serializeCleanup(async () => {
    cleanupCalls += 1;
    markCleanupStarted();
    await cleanupGate;
    return cleanupResult;
  });
  const deadlineResult = runDeadlineBoundOperation({
    operation: new Promise(() => {}),
    deadline: performance.now() + 5,
    captureTimeoutEvidence: async () => ({ router: "stale" }),
    cleanup,
  });
  const timeoutAssertion = assert.rejects(deadlineResult, (error) => {
    assert.strictEqual(error.cleanup, cleanupResult);
    return true;
  });
  await cleanupStarted;
  const finalCleanup = cleanup();
  assert.strictEqual(finalCleanup, cleanup());
  assert.equal(cleanupCalls, 1);
  releaseCleanup();
  assert.strictEqual(await finalCleanup, cleanupResult);
  await timeoutAssertion;
  assert.equal(cleanupCalls, 1);
});

test("managed recovery runner awaits serialized cleanup before the final artifact write", async () => {
  const runner = await readFile(fileURLToPath(new URL("./bcb-managed-recovery-runner.mjs", import.meta.url)), "utf8");
  assert.match(runner, /const cleanup = serializeCleanup\(async \(\) => \{/u);
  assert.doesNotMatch(runner, /cleanupDone/u);
  const finallyStart = runner.indexOf("  } finally {", runner.indexOf("async function runTrial"));
  const cleanupAt = runner.indexOf("await cleanup()", finallyStart);
  const writeAt = runner.indexOf("await writeArtifact()", cleanupAt);
  assert.ok(finallyStart >= 0 && cleanupAt > finallyStart && writeAt > cleanupAt);
});

test("stale completion receipt is rejected", () => {
  assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ observed_monotonic_ms: 99 }) })), /after the restart boundary/);
});

test("late completion receipt is rejected", () => {
  assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ observed_monotonic_ms: 30_101 }) })), /no later than the absolute deadline/);
});

test("unbound completion receipt is rejected", () => {
  assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ agent_id: "other-agent" }) })), /not bound to the trial/);
});

test("generation mismatch completion receipt is rejected", () => {
  assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ runtime_generation: "stale-generation" }) })), /current fence/);
  assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ process_incarnation: "stale-incarnation" }) })), /current fence/);
});

test("completion requires a persisted response and the canonical no-stuck lifecycle", () => {
  assert.throws(() => validateCompletion(validation({ persisted_receipt: undefined })), /router-persisted completion receipt/);
  for (const lifecycle of [
    { lifecycle_state: "ready", accepting: true, activity_state: "idle" },
    { lifecycle_state: "running", accepting: true, activity_state: "active" },
    { lifecycle_state: "running", accepting: false, activity_state: "idle" },
  ]) assert.throws(() => validateCompletion(validation({ lifecycle })), /running, idle, and accepting prompts/);
  assert.doesNotThrow(() => validateCompletion(validation()));
});

test("completion requires the observed transcript completion boolean", () => {
  assert.throws(() => validateCompletion(validation({ transcript_completion: false })), /completion transcript evidence/);
  assert.throws(() => validateCompletion(validation({ transcript_completion: 1 })), /completion transcript evidence/);
  assert.doesNotThrow(() => validateCompletion(validation({ transcript_completion: true })));
});

test("literal persistence flags cannot replace a router-persisted completion receipt", () => {
  assert.throws(
    () => validateCompletion(validation({ response_persisted: true, persisted_receipt: undefined })),
    /router-persisted completion receipt/,
  );
});

test("completion receipt must match the current completion fence", () => {
  for (const [key, value] of [
    ["trial_id", "other-trial"],
    ["token", "other-token"],
    ["agent_id", "other-agent"],
  ]) {
    assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ [key]: value }) })), /completion receipt is not bound to the trial/);
  }
  assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ provider_request_seq: 3 }) })), /completion receipt does not match provider completion/);
  for (const [key, value] of [
    ["runtime_generation", "stale-generation"],
    ["process_incarnation", "stale-incarnation"],
  ]) {
    assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ [key]: value }) })), /completion receipt does not match the current fence/);
  }
  assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ observed_monotonic_ms: 100 }) })), /after the restart boundary/);
  assert.throws(() => validateCompletion(validation({ persisted_receipt: persistedReceipt({ observed_monotonic_ms: 30_101 }) })), /no later than the absolute deadline/);
});

test("literal acknowledgement is not router-persisted barrier evidence", () => {
  const runnerBuiltBarrier = {
    event: "managed_recovery_in_progress",
    trial_id: "trial-1",
    token: "token-1",
    agent_id: "agent-1",
    provider_request_seq: 1,
    acknowledged: true,
    request_active: true,
    router_pid: 4010,
    observed_monotonic_ms: 90,
    fence: { ...fence, runtime_generation: "generation-before", process_incarnation: "incarnation-before", process_pid: 4009 },
  };
  assert.throws(
    () => validateBarrier(runnerBuiltBarrier, { trial_id: "trial-1", workload_token: "token-1", agent_id: "agent-1", router_pid: 4010 }, runnerBuiltBarrier.fence),
    /router-persisted acknowledgement/,
  );
});

test("barrier is tokenized, current, acknowledged, and tied to the router and DB fence", () => {
  const barrier = {
    event: "managed_recovery_in_progress",
    acknowledgement_id: "barrier-ack-1",
    trial_id: "trial-1",
    token: "token-1",
    agent_id: "agent-1",
    provider_request_seq: 1,
    acknowledged: true,
    request_active: true,
    router_pid: 4010,
    observed_monotonic_ms: 90,
    fence: { ...fence, runtime_generation: "generation-before", process_incarnation: "incarnation-before", process_pid: 4009 },
  };
  assert.doesNotThrow(() => validateBarrier(barrier, { trial_id: "trial-1", workload_token: "token-1", agent_id: "agent-1", router_pid: 4010 }, barrier.fence));
  assert.throws(() => validateBarrier({ ...barrier, request_active: false }, { trial_id: "trial-1", workload_token: "token-1", agent_id: "agent-1", router_pid: 4010 }, barrier.fence), /active/);
  assert.throws(() => validateBarrier({ ...barrier, acknowledgement_id: "" }, { trial_id: "trial-1", workload_token: "token-1", agent_id: "agent-1", router_pid: 4010 }, barrier.fence), /acknowledgement_id/);
  assert.throws(() => validateBarrier({ ...barrier, observed_monotonic_ms: Number.NaN }, { trial_id: "trial-1", workload_token: "token-1", agent_id: "agent-1", router_pid: 4010 }, barrier.fence), /observation time/);
  assert.throws(() => validateBarrier({ ...barrier, fence: { ...barrier.fence, runtime_generation: "wrong" } }, { trial_id: "trial-1", workload_token: "token-1", agent_id: "agent-1", router_pid: 4010 }, barrier.fence), /DB fence/);
});

test("DOM freshness rejects pre-boundary, stale, and router-mismatched observations", () => {
  const fresh = { pre_token: 10, router_token: 12, dom_token: 12, restart_boundary: 100, observed_monotonic_ms: 120 };
  assert.doesNotThrow(() => validateFreshness(fresh));
  assert.throws(() => validateFreshness({ ...fresh, observed_monotonic_ms: 100 }), /post-boundary/);
  assert.throws(() => validateFreshness({ ...fresh, dom_token: 10 }), /change/);
  assert.throws(() => validateFreshness({ ...fresh, dom_token: 11 }), /current router/);
});

test("shared-state proof rejects literal flags and changed captured identities", () => {
  const before = {
    router: { endpoint: "http://127.0.0.1:8765", start_marker: "router-start" },
    web_ui: { endpoint: "http://127.0.0.1:5173", asset_sha256: "ui-sha256" },
    configuration: { sha256: "configuration-sha256" },
    processes: [{ pid: 101, start_marker: "shared-process-start" }],
    user_tabs: [{ window_id: "window-1", tab_id: "tab-1", url: "http://127.0.0.1:5173/" }],
  };
  const validEvidence = { before, after: structuredClone(before) };

  for (const literalFlags of [
    { mutated: false },
    {
      before: { router: true, web_ui: true, configuration: true, processes: true, user_tabs: true },
      after: { router: true, web_ui: true, configuration: true, processes: true, user_tabs: true },
    },
  ]) assert.throws(() => managedRecoveryHarness.assertSharedResourcesUnchanged(literalFlags), /shared-state evidence|captured identity/);

  assert.doesNotThrow(() => managedRecoveryHarness.assertSharedResourcesUnchanged(validEvidence));
  assert.throws(
    () => managedRecoveryHarness.assertSharedResourcesUnchanged({
      before,
      after: { ...structuredClone(before), processes: [{ pid: 102, start_marker: "replacement-process-start" }] },
    }),
    /processes identity changed/,
  );
});

test("shared snapshot capture fails closed when a required observation is missing", async () => {
  await assert.rejects(
    captureSharedResourceSnapshot({
      routerURL: "http://127.0.0.1:18765",
      webUIURL: "http://127.0.0.1:18787",
      configurationPaths: [fileURLToPath(import.meta.url)],
    }, {
      fetchImpl: async (url) => String(url).endsWith("/v1/health")
        ? new Response(JSON.stringify({ readiness: "ok", status: "ok", version: "v1", runtime: {} }))
        : new Response("<!doctype html>"),
      runCommand: async () => ({ stdout: "" }),
      listUserTabs: async () => undefined,
    }),
    /user browser tab identity capture is unavailable/,
  );
});

test("unchanged snapshots use real config, process, router, and web UI observations", async (t) => {
  const router = createServer((_request, response) => response.end(JSON.stringify({ readiness: "ok", status: "ok", version: "v1", runtime: { source_commit: "test", source_tree: "clean" } })));
  const webUI = createServer((_request, response) => response.end("<!doctype html><title>shared UI</title>"));
  await Promise.all([
    new Promise((resolveListen) => router.listen(0, "127.0.0.1", resolveListen)),
    new Promise((resolveListen) => webUI.listen(0, "127.0.0.1", resolveListen)),
  ]);
  t.after(async () => Promise.all([
    new Promise((resolveClose) => router.close(resolveClose)),
    new Promise((resolveClose) => webUI.close(resolveClose)),
  ]));
  const routerPort = router.address().port;
  const webUIPort = webUI.address().port;
  const listeners = [routerPort, webUIPort, 18080, 18081]
    .map((port) => `LISTEN 0 128 127.0.0.1:${port} 0.0.0.0:* users:((\"node\",pid=${process.pid},fd=1))`)
    .join("\n");
  const options = {
    routerURL: `http://127.0.0.1:${routerPort}`,
    webUIURL: `http://127.0.0.1:${webUIPort}`,
    configurationPaths: [fileURLToPath(import.meta.url)],
  };
  const dependencies = {
    runCommand: async () => ({ stdout: listeners }),
    listUserTabs: async () => [{ id: 41, url: "http://127.0.0.1:8787/", title: "Mecha", active: true }],
  };
  const before = await captureSharedResourceSnapshot(options, dependencies);
  const after = await captureSharedResourceSnapshot(options, dependencies);
  assert.doesNotThrow(() => assertSharedResourcesUnchanged({ before, after }));
  assert.equal(before.processes[0].pid, process.pid);
  assert.match(before.configuration.files[0].sha256, /^[a-f0-9]{64}$/u);
  assert.match(before.web_ui.document_sha256, /^[a-f0-9]{64}$/u);
  assert.deepEqual(before.user_tabs, [{ id: "41", title: "Mecha", url: "http://127.0.0.1:8787/" }]);
});

test("captured shared resource mutation fails closed", async () => {
  const snapshot = {
    router: { endpoint: "http://127.0.0.1:8765", listeners: [{ pid: 100, start_marker: "1" }] },
    web_ui: { endpoint: "http://127.0.0.1:8787", document_sha256: "a" },
    configuration: { files: [{ path: "/config", sha256: "b" }], process_commands: [] },
    processes: [{ pid: 100, start_marker: "1", executable: "/router" }],
    user_tabs: [{ id: "7", url: "http://127.0.0.1:8787/", title: "Mecha" }],
  };
  const after = structuredClone(snapshot);
  after.user_tabs[0].url = "https://example.test/mutated";
  assert.throws(() => assertSharedResourcesUnchanged({ before: snapshot, after }), /shared user_tabs identity changed/);
});

test("artifact redaction rejects private identities in public and DOM evidence", () => {
  const secrets = [fence.pi_session_id, fence.runtime_generation, fence.process_incarnation, String(fence.process_pid), fence.process_start_marker];
  assert.doesNotThrow(() => assertArtifactRedacted({ bcb_dom: { lifecycle: "ready", projection_version: "12" }, bcb_console: [] }, secrets));
  assert.throws(() => assertArtifactRedacted({ bcb_dom: { text: fence.runtime_generation }, bcb_console: [] }, secrets), /private runtime identity/);
  assert.throws(() => assertArtifactRedacted({ bcb_dom: { runtime_generation: "redacted" }, bcb_console: [] }, []), /private runtime field/);
});

test("shared-resource proof requires matching captured identities", () => {
  const snapshot = {
    router: { pid: 100, start_marker: "router-start" },
    web_ui: { pid: 101, start_marker: "web-start" },
    configuration: { sha256: "config-hash" },
    processes: [{ pid: 100 }, { pid: 101 }],
    user_tabs: [{ id: "tab-1", url: "http://shared-ui" }],
  };
  assert.throws(() => assertSharedResourcesUnchanged({ mutated: false }), /captured before and after/);
  assert.doesNotThrow(() => assertSharedResourcesUnchanged({ before: snapshot, after: structuredClone(snapshot) }));
  for (const key of ["router", "web_ui", "configuration", "processes", "user_tabs"]) {
    assert.throws(
      () => assertSharedResourcesUnchanged({ before: snapshot, after: { ...structuredClone(snapshot), [key]: { changed: true } } }),
      new RegExp(`shared ${key}`),
    );
  }
});

test("cleanup rejects retained roots, profiles, processes, tabs, or listeners", () => {
  const clean = { task_root_absent: true, profile_absent: true, processes_absent: true, ports_available: true, private_tabs_remaining: 0 };
  assert.doesNotThrow(() => assertCleanupComplete(clean));
  for (const key of ["task_root_absent", "profile_absent", "processes_absent", "ports_available"]) {
    assert.throws(() => assertCleanupComplete({ ...clean, [key]: false }), /cleanup incomplete/);
  }
  assert.throws(() => assertCleanupComplete({ ...clean, private_tabs_remaining: 1 }), /cleanup incomplete/);
});

function trialResources(number) {
  return {
    router: `router-${number}`,
    database: `database-${number}`,
    bcb_bridge: `bcb-${number}`,
    browser_profile: `profile-${number}`,
    browser_tab: `tab-${number}`,
    workload_token: `token-${number}`,
  };
}

test("five-trial sequence allocates each trial inside the iteration and stops on failure", async () => {
  const started = [];
  const completed = await runManagedRecoverySequence({ trials: 5, runTrial: async (number) => { started.push(number); return { trial_id: `trial-${number}`, result: "passed", resources: trialResources(number) }; } });
  assert.deepEqual(started, [1, 2, 3, 4, 5]);
  assert.equal(completed.length, 5);

  const failed = [];
  await assert.rejects(runManagedRecoverySequence({ trials: 5, runTrial: async (number) => { failed.push(number); if (number === 3) throw new Error("development failure"); return { trial_id: `trial-${number}`, result: "passed", resources: trialResources(number) }; } }), /development failure/);
  assert.deepEqual(failed, [1, 2, 3]);
});

test("five-trial sequence rejects missing or reused resource identities", async () => {
  await assert.rejects(runManagedRecoverySequence({ trials: 5, runTrial: async (number) => ({ trial_id: `trial-${number}`, result: "passed" }) }), /fresh resource evidence/);
  await assert.rejects(runManagedRecoverySequence({ trials: 5, runTrial: async (number) => ({
    trial_id: `trial-${number}`,
    result: "passed",
    resources: { ...trialResources(number), router: "reused-router" },
  }) }), /reused router/);
});

test("five passing trials require fresh task-local resource evidence", async () => {
  const missingResources = runManagedRecoverySequence({
    trials: 5,
    runTrial: async (number) => number === 3
      ? { trial_id: `trial-${number}`, result: "passed" }
      : { trial_id: `trial-${number}`, result: "passed", resources: taskResources(number) },
  });
  const duplicateResources = runManagedRecoverySequence({
    trials: 5,
    runTrial: async (number) => ({
      trial_id: `trial-${number}`,
      result: "passed",
      resources: { ...taskResources(number), router: "reused-router" },
    }),
  });

  const outcomes = await Promise.allSettled([missingResources, duplicateResources]);
  assert.deepEqual(outcomes.map(({ status }) => status), ["rejected", "rejected"]);
  assert.match(outcomes[0].reason.message, /resources/);
  assert.match(outcomes[1].reason.message, /router.*unique/);
});

test("deadline operation integration orders signal, replacement work, evidence, and isolated cleanup", async () => {
  const order = [];
  const signal = signalRouterAtBoundary({ pid: 99, kill: () => { order.push("signal"); return true; } }, () => performance.now());
  const result = await runDeadlineBoundOperation({
    operation: (async () => { order.push("restart"); order.push("observe"); return "complete"; })(),
    deadline: signal.deadline,
    captureTimeoutEvidence: async () => { order.push("evidence"); },
    cleanup: async () => { order.push("cleanup"); },
  });
  assert.equal(result, "complete");
  assert.deepEqual(order, ["signal", "restart", "observe"]);
});
