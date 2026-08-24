import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { existsSync } from "node:fs";
import { join } from "node:path";
import test from "node:test";
import { pathToFileURL } from "node:url";

import { createMechaMessagingExtension } from "../pi-extensions/mecha-messaging.js";

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function waitFor(check, description, attempts = 100) {
  for (let attempt = 0; attempt < attempts; attempt += 1) {
    if (check()) return;
    await sleep(10);
  }
  assert.ok(check(), description);
}

async function installedPiRuntime(t) {
  const root = process.env.MECHA_PI_PACKAGE_ROOT ?? (() => {
    try {
      return join(execFileSync("npm", ["root", "-g"], { encoding: "utf8" }).trim(), "@earendil-works", "pi-coding-agent");
    } catch {
      return undefined;
    }
  })();
  const codingPath = root === undefined ? undefined : join(root, "dist", "index.js");
  const aiPath = root === undefined ? undefined : join(root, "node_modules", "@earendil-works", "pi-ai", "dist", "index.js");
  if (codingPath === undefined || aiPath === undefined || !existsSync(codingPath) || !existsSync(aiPath)) {
    t.skip("requires an installed Pi runtime; set MECHA_PI_PACKAGE_ROOT to run this integration test");
    return undefined;
  }
  return {
    coding: await import(pathToFileURL(codingPath).href),
    ai: await import(pathToFileURL(aiPath).href),
  };
}

function restoreCompactionPolicy(t, policy) {
  const previous = process.env.MECHA_PI_COMPACTION_POLICY;
  process.env.MECHA_PI_COMPACTION_POLICY = JSON.stringify(policy);
  t.after(() => {
    if (previous === undefined) delete process.env.MECHA_PI_COMPACTION_POLICY;
    else process.env.MECHA_PI_COMPACTION_POLICY = previous;
  });
}

function mailboxExtension(pi) {
  createMechaMessagingExtension(pi, {
    resolveCurrentAgentId: () => "hard-compaction-e2e",
    createClient: () => ({
      connect: async () => {},
      close() {},
      sendActivity() {},
      sendReceipt() {},
      sendReply: async () => ({ id: "message", state: "queued" }),
    }),
  });
}

function usage(input) {
  return {
    input,
    output: 1,
    cacheRead: 0,
    cacheWrite: 0,
    totalTokens: input + 1,
    cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, total: 0 },
  };
}

async function createRuntimeSession(t, runtime, {
  responses,
  extensionFactories = [],
  withMailbox = false,
  contextWindow = 1_000,
  tokensPerSecond = 1_000,
  seed,
} = {}) {
  const { coding, ai } = runtime;
  const faux = ai.fauxProvider({
    provider: "hard-compaction-test",
    models: [{
      id: "hard-compaction-model",
      name: "Hard Compaction Model",
      reasoning: false,
      input: ["text"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 },
      contextWindow,
      maxTokens: 256,
    }],
    tokenSize: { min: 1, max: 1 },
    tokensPerSecond,
  });
  faux.setResponses(responses);

  const modelRuntime = await coding.ModelRuntime.create({ modelsPath: null, refreshOnCreate: false });
  modelRuntime.registerNativeProvider(faux.provider);
  const settingsManager = coding.SettingsManager.inMemory({
    compaction: { enabled: false, reserveTokens: 1, keepRecentTokens: 1 },
    retry: { enabled: false },
  });
  const sessionManager = coding.SessionManager.inMemory(process.cwd());
  seed?.(sessionManager, faux.getModel());
  const resourceLoader = new coding.DefaultResourceLoader({
    cwd: process.cwd(),
    agentDir: process.cwd(),
    noExtensions: true,
    noSkills: true,
    noPromptTemplates: true,
    noThemes: true,
    noContextFiles: true,
    extensionFactories: [...(withMailbox ? [mailboxExtension] : []), ...extensionFactories],
  });
  await resourceLoader.reload();
  const { session } = await coding.createAgentSession({
    cwd: process.cwd(),
    modelRuntime,
    model: faux.getModel(),
    settingsManager,
    sessionManager,
    resourceLoader,
    noTools: "builtin",
  });
  const events = [];
  session.subscribe((event) => events.push(event));
  t.after(() => session.dispose());
  return { ai, events, faux, session };
}

test("installed Pi aborts an active stream before starting compaction", async (t) => {
  const runtime = await installedPiRuntime(t);
  if (runtime === undefined) return;

  let requested = false;
  const compactDuringStream = (pi) => {
    pi.on("message_update", (_event, context) => {
      if (requested) return;
      requested = true;
      context.compact();
    });
  };
  const { events, session } = await createRuntimeSession(t, runtime, {
    extensionFactories: [compactDuringStream],
    responses: [
      runtime.ai.fauxAssistantMessage("stream ".repeat(600)),
      runtime.ai.fauxAssistantMessage("compaction summary"),
    ],
  });

  await session.prompt("start the stream");
  await waitFor(() => events.some((event) => event.type === "compaction_end"), "compaction should finish after the stream aborts");

  assert.equal(requested, true);
  const agentEnd = events.findIndex((event) => event.type === "agent_end");
  const compactionStart = events.findIndex((event) => event.type === "compaction_start");
  assert.ok(agentEnd >= 0);
  assert.ok(compactionStart > agentEnd);
  assert.equal(events.some((event) => event.type === "message_end" && event.message?.stopReason === "aborted"), true);
  assert.equal(session.isIdle, true);
});

test("installed Pi hard guard aborts the next run and compacts it", async (t) => {
  const runtime = await installedPiRuntime(t);
  if (runtime === undefined) return;
  restoreCompactionPolicy(t, { levels: [40, { percent: 50, mandatory: true }] });

  const { events, session } = await createRuntimeSession(t, runtime, {
    withMailbox: true,
    seed(sessionManager, model) {
      sessionManager.appendMessage({ role: "user", content: "prior request ".repeat(1_000), timestamp: Date.now() - 2 });
      sessionManager.appendMessage({
        role: "assistant",
        content: [{ type: "text", text: "prior response" }],
        api: model.api,
        provider: model.provider,
        model: model.id,
        usage: usage(600),
        stopReason: "stop",
        timestamp: Date.now() - 1,
      });
    },
    responses: [
      runtime.ai.fauxAssistantMessage("normal response that must be aborted"),
      runtime.ai.fauxAssistantMessage("compaction summary"),
    ],
  });

  await session.prompt("continue the task");
  await waitFor(() => events.some((event) => event.type === "compaction_end"), "hard guard compaction should finish");
  const agentEnd = events.findIndex((event) => event.type === "agent_end");
  const compactionStart = events.findIndex((event) => event.type === "compaction_start");
  assert.ok(agentEnd >= 0);
  assert.ok(compactionStart > agentEnd);
  assert.equal(session.messages.some((message) => message.role === "assistant" && message.content.some((part) => part.type === "text" && part.text.includes("normal response that must be aborted"))), false);

  assert.equal(session.isIdle, true);
});

test("installed Pi waits for a tool that ignores its abort signal", async (t) => {
  const runtime = await installedPiRuntime(t);
  if (runtime === undefined) return;

  let releaseTool;
  let markToolStarted;
  const toolStarted = new Promise((resolve) => { markToolStarted = resolve; });
  const blockingTool = (pi) => {
    pi.registerTool({
      name: "hold_until_released",
      label: "Hold until released",
      description: "Test-only tool that deliberately ignores cancellation.",
      parameters: runtime.ai.Type.Object({}),
      async execute(_id, _params, _signal, _onUpdate, context) {
        markToolStarted();
        context.compact();
        return new Promise((resolve) => {
          releaseTool = () => resolve({ content: [{ type: "text", text: "released" }], details: {} });
        });
      },
    });
  };
  const { events, session } = await createRuntimeSession(t, runtime, {
    extensionFactories: [blockingTool],
    responses: [
      runtime.ai.fauxAssistantMessage(runtime.ai.fauxToolCall("hold_until_released", {})),
      runtime.ai.fauxAssistantMessage("compaction summary"),
    ],
  });

  const prompt = session.prompt("run the holding tool");
  await toolStarted;
  assert.equal(events.some((event) => event.type === "compaction_start"), false);

  releaseTool();
  await prompt;
  await waitFor(() => events.some((event) => event.type === "compaction_end"), "compaction should finish after the tool releases");
  const agentEnd = events.findIndex((event) => event.type === "agent_end");
  const compactionStart = events.findIndex((event) => event.type === "compaction_start");
  assert.ok(agentEnd >= 0);
  assert.ok(compactionStart > agentEnd);
  assert.equal(session.isIdle, true);
});
