import assert from "node:assert/strict";
import test from "node:test";

import {
  createModelControlProjection,
  modelControlErrorCode,
  modelProjection,
  projectAvailableModels,
  projectModelMetadata,
  ModelControlError,
} from "../pi-extensions/mecha-messaging/model-control-projection.ts";
import type { ModelContext } from "../pi-extensions/mecha-messaging/model-control-projection.ts";

type Context = ModelContext;

function context(models: unknown[], model: unknown = models[0]): Context {
  return {
    model,
    modelRegistry: { getAvailable: () => models },
    sessionManager: { getSessionId: () => "session" },
  };
}

test("model projection requires provider and id and preserves bounded optional metadata", () => {
  assert.equal(projectModelMetadata(null), undefined);
  assert.equal(projectModelMetadata({ provider: "provider" }), undefined);
  assert.deepEqual(projectModelMetadata({
    provider: " provider ", id: " model ", name: " Model ", contextWindow: 100, maxTokens: 20,
  }), { provider: "provider", id: "model", name: "Model", contextWindow: 100, maxTokens: 20 });
  assert.deepEqual(projectModelMetadata({
    provider: "provider", id: "model", name: null, contextWindow: -1, maxTokens: Number.MAX_SAFE_INTEGER + 1,
  }), { provider: "provider", id: "model" });
  assert.equal(projectModelMetadata({ provider: "p".repeat(129), id: "model" }), undefined);
  assert.deepEqual(modelProjection(context([

    { provider: "provider", id: "same", name: "first" },
    { provider: "other", id: "same" },
    { provider: "provider" },
    "malformed",
  ])).available, [
    { provider: "provider", id: "same", name: "first" },
    { provider: "other", id: "same" },
  ]);
  const boundedCatalog = Array.from({ length: 1_001 }, (_, index) => ({ provider: "provider", id: `model-${index}` }));
  assert.equal(modelProjection(context(boundedCatalog)).available.length, 1_000);
  assert.deepEqual(modelProjection({ model: { provider: "provider", id: "current" }, sessionManager: { getSessionId: () => "session" } }), {
    current: { provider: "provider", id: "current" }, available: [],
  });
  assert.deepEqual(modelProjection({
    model: { provider: "provider", id: "current" },
    modelRegistry: { getAvailable: () => { throw new Error("registry failed"); } },
    sessionManager: { getSessionId: () => "session" },
  }).available, []);
  assert.deepEqual(modelProjection({
    model: { provider: "provider", id: "current" },
    modelRegistry: { getAvailable: () => undefined as never },
    sessionManager: { getSessionId: () => "session" },
  }).available, []);
  assert.deepEqual(projectAvailableModels(context([
    { provider: "provider", id: "valid" }, null, { provider: "provider" },
  ])), [{ provider: "provider", id: "valid" }]);
  assert.deepEqual(projectAvailableModels(context([
    null,
    { provider: "provider", id: "numeric", contextWindow: 4, maxTokens: 2 },
  ])), [{ provider: "provider", id: "numeric", contextWindow: 4, maxTokens: 2 }]);
  assert.throws(() => projectAvailableModels({
    sessionManager: { getSessionId: () => "session" },
  }), (error: unknown) => error instanceof ModelControlError && error.code === "model_registry_unavailable");
  assert.throws(() => projectAvailableModels({
    modelRegistry: { getAvailable: () => undefined as never },
    sessionManager: { getSessionId: () => "session" },
  }), (error: unknown) => error instanceof ModelControlError && error.code === "model_registry_unavailable");
  assert.throws(() => projectAvailableModels({
    modelRegistry: { getAvailable: () => { throw "registry failed"; } },
    sessionManager: { getSessionId: () => "session" },
  }), (error: unknown) => error instanceof ModelControlError
    && error.code === "model_registry_failed"
    && error.message === "registry failed"
    && error.cause === undefined);
  assert.throws(() => projectAvailableModels({
    modelRegistry: { getAvailable: () => { throw { message: "registry failed" }; } },
    sessionManager: { getSessionId: () => "session" },
  }), (error: unknown) => error instanceof ModelControlError
    && error.code === "model_registry_failed"
    && error.message === "registry failed"
    && error.cause === undefined);
});

test("model control validates before switching and uses provider plus id", async () => {
  let current = context([{ provider: "provider", id: "current" }, { provider: "provider", id: "target" }]);
  const switched: unknown[] = [];
  const refreshed: Context[] = [];
  const projection = createModelControlProjection({
    getParentContext: () => current,
    setModel: (model) => { switched.push(model); current.model = model; return true; },
    refresh: (value) => { refreshed.push(value); },
  });

  const invalid = await projection.handleControl({ controlId: "invalid", sourceAgentId: "source", action: "set_model", model: { provider: "provider", id: "target", contextWindow: 1 } });
  assert.equal(invalid.accepted, false);
  assert.equal(invalid.errorCode, "invalid_model");
  assert.equal(switched.length, 0);

  const invalidProviderModel = { provider: "provider", id: "target" };
  Object.defineProperty(invalidProviderModel, "provider", { value: 1 });
  const invalidProvider = await projection.handleControl({
    controlId: "invalid-provider", sourceAgentId: "source", action: "set_model", model: invalidProviderModel,
  });
  assert.equal(invalidProvider.errorCode, "invalid_model");
  const invalidIdModel = { provider: "provider", id: "target" };
  Object.defineProperty(invalidIdModel, "id", { value: 1 });
  const invalidId = await projection.handleControl({
    controlId: "invalid-id", sourceAgentId: "source", action: "set_model", model: invalidIdModel,
  });
  assert.equal(invalidId.errorCode, "invalid_model");
  assert.equal(switched.length, 0);

  const unavailableProjection = createModelControlProjection({
    getParentContext: () => current,
    refresh() {},
  });
  const unavailable = await unavailableProjection.handleControl({ controlId: "unavailable", sourceAgentId: "source", action: "set_model", model: { provider: "provider", id: "target" } });
  assert.equal(unavailable.errorCode, "model_switch_unavailable");

  const selected = await projection.handleControl({ controlId: "valid", sourceAgentId: "source", action: "set_model", model: { provider: "provider", id: "target" } });
  assert.equal(selected.accepted, true);
  assert.deepEqual(selected.currentModel, { provider: "provider", id: "target" });
  assert.equal(switched.length, 1);
  assert.equal(refreshed.length, 1);
});

test("model control selects a colliding id by provider and id", async () => {
  const models = [
    { provider: "provider-a", id: "shared" },
    { provider: "provider-b", id: "shared" },
  ];
  const switched: unknown[] = [];
  const projection = createModelControlProjection({
    getParentContext: () => context(models),
    setModel: (model) => { switched.push(model); return true; },
    refresh() {},
  });
  const result = await projection.handleControl({
    controlId: "control",
    sourceAgentId: "source",
    action: "set_model",
    model: { provider: "provider-b", id: "shared" },
  });
  assert.equal(result.accepted, true);
  assert.deepEqual(switched, [models[1]]);
});

test("model control skips malformed candidates before a valid numeric model", async () => {
  const models: unknown[] = [
    { provider: "provider", id: "bad-name", name: 1 },
    { provider: "provider", id: "bad-context", name: "name", contextWindow: "bad" },
    { provider: "provider", id: "bad-max", name: "name", contextWindow: 1, maxTokens: "bad" },
    { provider: "provider", id: "target", name: "target", contextWindow: 4, maxTokens: 2 },
  ];
  const switched: unknown[] = [];
  const projection = createModelControlProjection({
    getParentContext: () => context(models),
    setModel: (model) => { switched.push(model); return true; },
    refresh() {},
  });

  const result = await projection.handleControl({
    controlId: "control", sourceAgentId: "source", action: "set_model",
    model: { provider: "provider", id: "target" },
  });
  assert.equal(result.accepted, true);
  assert.deepEqual(switched, [models[3]]);
});

test("model control accepts a native mutation that throws only when identity changed", async () => {
  let current = context([{ provider: "provider", id: "current" }, { provider: "provider", id: "target" }]);
  let refreshCount = 0;
  const projection = createModelControlProjection({
    getParentContext: () => current,
    setModel: (model) => {
      current.model = model;
      throw new Error("updated before reporting failure");
    },
    refresh() { refreshCount += 1; },
  });
  const accepted = await projection.handleControl({ controlId: "control", sourceAgentId: "source", action: "set_model", model: { provider: "provider", id: "target" } });
  assert.equal(accepted.accepted, true);
  assert.equal(refreshCount, 1);

  current = context([{ provider: "provider", id: "current" }, { provider: "provider", id: "target" }]);
  const rejectedProjection = createModelControlProjection({
    getParentContext: () => current,
    setModel: () => { throw new Error("switch failed"); },
    refresh() {},
  });
  const rejected = await rejectedProjection.handleControl({ controlId: "control", sourceAgentId: "source", action: "set_model", model: { provider: "provider", id: "target" } });
  assert.equal(rejected.accepted, false);
  assert.equal(rejected.errorCode, "model_switch_failed");
});

test("model control preserves string error names in diagnostics", async () => {
  const traces: Array<{ event: string; fields?: Record<string, unknown> }> = [];
  const models = [{ provider: "provider", id: "current" }, { provider: "provider", id: "target" }];
  const projection = createModelControlProjection({
    getParentContext: () => context(models),
    setModel: () => { throw { name: "NamedSwitchFailure" }; },
    refresh() {},
    trace: (event, fields) => { traces.push({ event, fields }); },
  });

  const result = await projection.handleControl({
    controlId: "control", sourceAgentId: "source", action: "set_model", model: models[1],
  });

  assert.equal(result.accepted, false);
  assert.deepEqual(traces.at(-1), {
    event: "extension_model_control_completed",
    fields: {
      control_id: "control",
      source_agent_id: "source",
      accepted: false,
      error_code: "model_switch_failed",
      error_name: "NamedSwitchFailure",
      current_provider: "provider",
      current_model_id: "current",
    },
  });
});

test("model control maps all expected errors and outcomes", async () => {
  const codes = [
    "invalid_model", "unsupported_model_control", "model_registry_unavailable", "model_registry_failed",
    "model_unavailable", "model_switch_unavailable", "model_switch_rejected",
  ];
  for (const code of codes) assert.equal(modelControlErrorCode(new ModelControlError(code, code)), code);
  assert.equal(modelControlErrorCode(new ModelControlError("unexpected", "unexpected")), "model_switch_failed");
  assert.equal(modelControlErrorCode(new Error("failed")), "model_switch_failed");

  const models = [{ provider: "provider", id: "current" }, { provider: "provider", id: "target" }];
  const request = { controlId: "control", sourceAgentId: "source", action: "set_model", model: models[1] };
  const unsupported = createModelControlProjection({ getParentContext: () => context(models), refresh() {} });
  assert.equal((await unsupported.handleControl({ ...request, action: "unsupported" })).errorCode, "unsupported_model_control");
  const noContext = createModelControlProjection({ getParentContext: () => undefined, refresh() {} });
  assert.equal((await noContext.handleControl(request)).errorCode, "model_switch_unavailable");
  const noRegistry = createModelControlProjection({
    getParentContext: () => ({ model: models[0], sessionManager: { getSessionId: () => "session" } }),
    setModel: () => true,
    refresh() {},
  });
  assert.equal((await noRegistry.handleControl(request)).errorCode, "model_registry_unavailable");
  const failedRegistry = createModelControlProjection({
    getParentContext: () => ({ ...context(models), modelRegistry: { getAvailable: () => { throw new Error("read failed"); } } }),
    setModel: () => true,
    refresh() {},
  });
  assert.equal((await failedRegistry.handleControl(request)).errorCode, "model_registry_failed");
  const missing = createModelControlProjection({
    getParentContext: () => context([models[0]]), setModel: () => true, refresh() {},
  });
  assert.equal((await missing.handleControl(request)).errorCode, "model_unavailable");
  const rejected = createModelControlProjection({
    getParentContext: () => context(models), setModel: () => false, refresh() {},
  });
  assert.equal((await rejected.handleControl(request)).errorCode, "model_switch_rejected");
  const nonErrorFailure = createModelControlProjection({
    getParentContext: () => context(models), setModel: () => { throw "switch failed"; }, refresh() {},
  });
  assert.equal((await nonErrorFailure.handleControl(request)).errorCode, "model_switch_failed");
});

test("model control rejects a throw after mutation when the model was already current", async () => {
  const current = context([{ provider: "provider", id: "target" }]);
  const projection = createModelControlProjection({
    getParentContext: () => current,
    setModel: (model) => { current.model = model; throw new Error("already current"); },
    refresh() {},
  });
  const result = await projection.handleControl({
    controlId: "control", sourceAgentId: "source", action: "set_model", model: { provider: "provider", id: "target" },
  });
  assert.equal(result.accepted, false);
  assert.equal(result.errorCode, "model_switch_failed");
});

test("model control leaves current null when a successful switch has no session identity", async () => {
  const current: Context = {
    modelRegistry: { getAvailable: () => [{ provider: "provider", id: "target" }] },
    sessionManager: { getSessionId: () => "session" },
  };
  Object.defineProperty(current.sessionManager, "getSessionId", { value: () => undefined });
  const projection = createModelControlProjection({
    getParentContext: () => current,
    setModel: () => true,
    refresh() {},
  });

  const result = await projection.handleControl({
    controlId: "control", sourceAgentId: "source", action: "set_model",
    model: { provider: "provider", id: "target" },
  });
  assert.equal(result.accepted, true);
  assert.equal(result.currentModel, null);
  assert.equal(projection.current(), null);

  const omittedModel = await projection.handleControl({
    controlId: "omitted", sourceAgentId: "source", action: "set_model", model: undefined,
  });
  assert.equal(omittedModel.accepted, false);
  assert.equal(omittedModel.errorCode, "invalid_model");
  assert.equal(omittedModel.currentModel, null);
});

test("model control returns an empty current projection for a stale context without a model", async () => {
  const modelA = { provider: "provider", id: "model-a" };
  const target = { provider: "provider", id: "target" };
  const contextA = context([modelA, target], modelA);
  contextA.sessionManager = { getSessionId: () => "session-a" };
  const contextB: Context = {
    modelRegistry: { getAvailable: () => [target] },
    sessionManager: { getSessionId: () => "session-b" },
  };
  let activeContext: Context = contextA;
  let resolveSwitch: ((switched: boolean) => void) | undefined;
  const projection = createModelControlProjection({
    getParentContext: () => activeContext,
    setModel: () => new Promise<boolean>((resolve) => { resolveSwitch = resolve; }),
    refresh() {},
  });

  const pending = projection.handleControl({
    controlId: "control", sourceAgentId: "source", action: "set_model", model: target,
  });
  await new Promise<void>((resolve) => setImmediate(resolve));
  activeContext = contextB;
  resolveSwitch?.(true);

  const result = await pending;
  assert.equal(result.accepted, true);
  assert.equal(result.currentModel, null);
});

test("model control reports the active context after an async switch rejection", async () => {
  const modelA = { provider: "provider", id: "model-a" };
  const target = { provider: "provider", id: "target" };
  const modelB = { provider: "provider", id: "model-b" };
  const contextA = context([modelA, target], modelA);
  contextA.sessionManager = { getSessionId: () => "session-a" };
  const contextB = context([modelB, target], modelB);
  contextB.sessionManager = { getSessionId: () => "session-b" };
  let activeContext: Context = contextA;
  let rejectSwitch: ((error: Error) => void) | undefined;
  const projection = createModelControlProjection({
    getParentContext: () => activeContext,
    setModel: () => new Promise<boolean>((_resolve, reject) => { rejectSwitch = reject; }),
    refresh() {},
  });

  const pending = projection.handleControl({
    controlId: "control", sourceAgentId: "source", action: "set_model", model: target,
  });
  await new Promise<void>((resolve) => setImmediate(resolve));
  activeContext = contextB;
  rejectSwitch?.(new Error("switch failed"));

  const result = await pending;
  assert.equal(result.accepted, false);
  assert.equal(result.errorCode, "model_switch_failed");
  assert.deepEqual(result.currentModel, modelB);
  assert.deepEqual(projection.current(), modelB);
});

test("model control does not refresh a stale context after an async switch", async () => {
  const modelA = { provider: "provider", id: "model-a" };
  const target = { provider: "provider", id: "target" };
  const modelB = { provider: "provider", id: "model-b" };
  const contextA = context([modelA, target], modelA);
  contextA.sessionManager = { getSessionId: () => "session-a" };
  const contextB = context([modelB, target], modelB);
  contextB.sessionManager = { getSessionId: () => "session-b" };
  let activeContext: Context = contextA;
  let resolveSwitch: ((switched: boolean) => void) | undefined;
  const published: Context[] = [];
  const projection = createModelControlProjection({
    getParentContext: () => activeContext,
    setModel: () => new Promise<boolean>((resolve) => { resolveSwitch = resolve; }),
    refresh: (value) => { published.push(value); },
  });

  const pending = projection.handleControl({
    controlId: "control", sourceAgentId: "source", action: "set_model", model: target,
  });
  await new Promise<void>((resolve) => setImmediate(resolve));
  activeContext = contextB;
  resolveSwitch?.(true);

  const result = await pending;
  assert.equal(result.accepted, true);
  assert.deepEqual(result.currentModel, modelB);
  assert.equal(activeContext, contextB);
  assert.deepEqual(published, []);
  assert.deepEqual(projection.current(), modelB);
});

test("model control override clears on reset or context change and refresh runs before result", async () => {
  let current = context([{ provider: "provider", id: "current" }, { provider: "provider", id: "target" }]);
  const events: string[] = [];
  let refreshCompleted = false;
  let projectedAfterRefresh = false;
  const projection = createModelControlProjection({
    getParentContext: () => current,
    getCurrentModel: (value) => {
      if (refreshCompleted) projectedAfterRefresh = true;
      return value?.model;
    },
    setModel: () => { events.push("set"); return true; },
    refresh: (value) => {
      events.push("refresh");
      refreshCompleted = true;
      value.model = { provider: "provider", id: "target" };
    },
  });
  const request = { controlId: "control", sourceAgentId: "source", action: "set_model", model: { provider: "provider", id: "target" } };
  const result = await projection.handleControl(request);
  assert.deepEqual(events, ["set", "refresh"]);
  assert.equal(projectedAfterRefresh, true);
  assert.deepEqual(result.currentModel, { provider: "provider", id: "target" });

  current = context([{ provider: "provider", id: "current" }, { provider: "provider", id: "target" }]);
  projection.onContext(current);
  assert.deepEqual(projection.current(), { provider: "provider", id: "current" });

  const overrideProjection = createModelControlProjection({
    getParentContext: () => current,
    setModel: () => true,
    refresh() {},
  });
  await overrideProjection.handleControl(request);
  assert.deepEqual(overrideProjection.current(), { provider: "provider", id: "target" });
  overrideProjection.reset();
  assert.deepEqual(overrideProjection.current(), { provider: "provider", id: "current" });
});

test("model control catalog requires a parent context", () => {
  const projection = createModelControlProjection({ getParentContext: () => undefined, refresh() {} });
  assert.throws(() => projection.catalog(), /Pi context is unavailable/);
  const availableProjection = createModelControlProjection({
    getParentContext: () => context([{ provider: "provider", id: "model" }]),
    refresh() {},
  });
  assert.deepEqual(availableProjection.catalog(), [{ provider: "provider", id: "model" }]);
});
