import { GOAL_CONTROL_REQUEST_EVENT } from "../mecha-goal/index.js";

export type ModelInput = {
  provider?: string;
  id?: string;
  name?: string;
  contextWindow?: number;
  maxTokens?: number;
};

export type ModelMetadata = {
  provider: string;
  id: string;
  name?: string;
  contextWindow?: number;
  maxTokens?: number;
};

export type ModelRegistry = {
  getAvailable?: () => unknown[];
};

export type ModelContext = {
  model?: unknown;
  modelRegistry?: ModelRegistry;
  sessionManager: { getSessionId: () => string };
};

export type ModelControlRequest = {
  controlId: string;
  sourceAgentId: string;
  action: string;
  model: ModelInput | undefined;
};

export type ModelControlResult = {
  accepted: boolean;
  errorCode?: string;
  currentModel: ModelMetadata | null;
};

export const MAX_MODEL_METADATA_ITEMS = 1_000;
export const MAX_MODEL_PROVIDER_CHARS = 128;
export const MAX_MODEL_ID_CHARS = 256;
export const MAX_MODEL_NAME_CHARS = 256;
const MODEL_SOURCE_LIMIT = 1_000_000;
const GOAL_CONTROL_RESPONSE_TIMEOUT_MS = 1_000;

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function errorMessage(error: unknown): string {
  return error instanceof Error
    ? error.message
    : isRecord(error) && typeof error.message === "string" ? error.message : String(error);
}

function errorCode(error: unknown): string | undefined {
  return isRecord(error) && typeof error.code === "string" ? error.code : undefined;
}

export function boundedModelString(value: unknown, maximum: number): string | undefined {
  if (typeof value !== "string") return undefined;
  const normalized = value.trim();
  return normalized.length > 0 && [...normalized].length <= maximum ? normalized : undefined;
}

export function projectModelMetadata(model: unknown): ModelMetadata | undefined {
  if (!isRecord(model)) return undefined;
  const provider = boundedModelString(model.provider, MAX_MODEL_PROVIDER_CHARS);
  const id = boundedModelString(model.id, MAX_MODEL_ID_CHARS);
  const name = boundedModelString(model.name, MAX_MODEL_NAME_CHARS);
  const contextWindow = typeof model.contextWindow === "number" && Number.isSafeInteger(model.contextWindow) && model.contextWindow >= 0
    ? model.contextWindow : undefined;
  const maxTokens = typeof model.maxTokens === "number" && Number.isSafeInteger(model.maxTokens) && model.maxTokens >= 0
    ? model.maxTokens : undefined;
  if (provider === undefined || id === undefined) return undefined;
  return {
    provider,
    id,
    ...(name === undefined ? {} : { name }),
    ...(contextWindow === undefined ? {} : { contextWindow }),
    ...(maxTokens === undefined ? {} : { maxTokens }),
  };
}

export function isModelInput(value: unknown): value is ModelInput {
  return isRecord(value)
    && (value.provider === undefined || typeof value.provider === "string")
    && (value.id === undefined || typeof value.id === "string")
    && (value.name === undefined || typeof value.name === "string")
    && (value.contextWindow === undefined || typeof value.contextWindow === "number")
    && (value.maxTokens === undefined || typeof value.maxTokens === "number");
}

export function sameModelIdentity(
  left: ModelMetadata | null | undefined,
  right: ModelMetadata | null | undefined,
): boolean {
  return left?.provider === right?.provider && left?.id === right?.id;
}

function availableModelsForContext(context: ModelContext | undefined): unknown[] | undefined {
  try {
    const available = context?.modelRegistry?.getAvailable?.();
    return Array.isArray(available) ? available : undefined;
  } catch {
    return undefined;
  }
}

export class ModelControlError extends Error {
  code: string;

  constructor(code: string, message: string, options: { cause?: Error } = {}) {
    super(message, options);
    this.name = "ModelControlError";
    this.code = code;
  }
}

function readAvailableModelsForContext(context: ModelContext): unknown[] {
  const getAvailable = context.modelRegistry?.getAvailable;
  if (typeof getAvailable !== "function") {
    throw new ModelControlError("model_registry_unavailable", "Pi model registry is unavailable");
  }
  let available: unknown[];
  try {
    available = getAvailable.call(context.modelRegistry);
  } catch (error) {
    throw new ModelControlError("model_registry_failed", errorMessage(error), {
      cause: error instanceof Error ? error : undefined,
    });
  }
  if (!Array.isArray(available)) {
    throw new ModelControlError("model_registry_unavailable", "Pi model registry is unavailable");
  }
  return available;
}

export function projectAvailableModels(context: ModelContext): ModelMetadata[] {
  return readAvailableModelsForContext(context)
    .slice(0, MODEL_SOURCE_LIMIT)
    .map(projectModelMetadata)
    .filter((model): model is ModelMetadata => model !== undefined);
}

export function modelProjection(
  context: ModelContext | undefined,
  getCurrentModel: (context: ModelContext | undefined) => unknown = (value) => value?.model,
): { current: ModelMetadata | null; available: ModelMetadata[] } {
  const availableModels = availableModelsForContext(context);
  return {
    current: projectModelMetadata(getCurrentModel(context)) ?? null,
    available: (availableModels ?? [])
      .slice(0, MAX_MODEL_METADATA_ITEMS)
      .map(projectModelMetadata)
      .filter((model): model is ModelMetadata => model !== undefined),
  };
}

function modelSelector(input: unknown): { provider: string; id: string } {
  if (!isRecord(input) || Object.keys(input).some((key) => !["provider", "id", "name"].includes(key))) {
    throw new ModelControlError("invalid_model", "model provider and id are required");
  }
  const provider = typeof input.provider === "string" ? input.provider.trim() : "";
  const id = typeof input.id === "string" ? input.id.trim() : "";
  if (provider.length === 0 || [...provider].length > MAX_MODEL_PROVIDER_CHARS
    || id.length === 0 || [...id].length > MAX_MODEL_ID_CHARS) {
    throw new ModelControlError(
      "invalid_model",
      `model provider and id must be non-empty strings of at most ${MAX_MODEL_PROVIDER_CHARS} and ${MAX_MODEL_ID_CHARS} characters`,
    );
  }
  return { provider, id };
}

export function modelControlErrorCode(error: unknown): string {
  const code = errorCode(error);
  const knownCodes = [
    "invalid_model",
    "unsupported_model_control",
    "model_registry_unavailable",
    "model_registry_failed",
    "model_unavailable",
    "model_switch_unavailable",
    "model_switch_rejected",
  ];
  return code !== undefined && knownCodes.includes(code) ? code : "model_switch_failed";
}

type ModelSwitchOverride = {
  sessionId: string;
  model: ModelMetadata;
  sourceContext: ModelContext | undefined;
  sourceModel: ModelMetadata | null;
};

export type ModelControlProjection = {
  catalog: () => ModelMetadata[];
  current: () => ModelMetadata | null;
  handleControl: (control: ModelControlRequest) => Promise<ModelControlResult>;
  onContext: (context: ModelContext | undefined) => void;
  reset: () => void;
};

export function createModelControlProjection({
  getParentContext,
  setModel,
  refresh,
  trace = () => {},
  getCurrentModel = (context) => context?.model,
  isModelSwitchAvailable = () => typeof setModel === "function",
}: {
  getParentContext: () => ModelContext | undefined;
  setModel?: (model: ModelInput) => Promise<boolean> | boolean;
  isModelSwitchAvailable?: () => boolean;
  refresh: (context: ModelContext) => void;
  trace?: (event: string, fields?: Record<string, unknown>) => void;
  getCurrentModel?: (context: ModelContext | undefined) => unknown;
}): ModelControlProjection {
  let modelSwitchOverride: ModelSwitchOverride | undefined;

  function nativeProjection(context: ModelContext | undefined) {
    return modelProjection(context, getCurrentModel);
  }

  function currentForContext(context: ModelContext | undefined) {
    const projection = nativeProjection(context);
    const sessionId = context?.sessionManager.getSessionId();
    if (modelSwitchOverride?.sessionId !== undefined
      && modelSwitchOverride.sessionId === sessionId) {
      const contextChanged = context !== modelSwitchOverride.sourceContext;
      const reflected = sameModelIdentity(projection.current, modelSwitchOverride.model);
      const locallyChanged = !sameModelIdentity(projection.current, modelSwitchOverride.sourceModel);
      if (contextChanged || reflected || locallyChanged) {
        modelSwitchOverride = undefined;
        return projection;
      }
      return { ...projection, current: modelSwitchOverride.model };
    }
    return projection;
  }

  function current(): ModelMetadata | null {
    return currentForContext(getParentContext()).current;
  }

  function rememberSuccessfulModelSwitch(context: ModelContext, model: ModelInput): void {
    const projected = projectModelMetadata(model);
    const sessionId = context.sessionManager.getSessionId();
    if (projected !== undefined && sessionId !== undefined) {
      modelSwitchOverride = {
        sessionId,
        model: projected,
        sourceContext: context,
        sourceModel: nativeProjection(context).current,
      };
    }
  }

  async function switchModel(input: unknown, context: ModelContext): Promise<ModelInput> {
    const requested = modelSelector(input);
    const before = currentForContext(context);
    const nativeBefore = nativeProjection(context);
    trace("extension_model_switch_started", {
      requested_provider: requested.provider,
      requested_model_id: requested.id,
      ...(before.current?.provider === undefined ? {} : { current_provider: before.current.provider }),
      ...(before.current?.id === undefined ? {} : { current_model_id: before.current.id }),
    });
    if (typeof setModel !== "function" || !isModelSwitchAvailable()) {
      throw new ModelControlError("model_switch_unavailable", "Pi model switching is unavailable");
    }
    const availableModels = readAvailableModelsForContext(context);
    trace("extension_model_registry_read", {
      requested_provider: requested.provider,
      requested_model_id: requested.id,
      available_count: availableModels.length,
    });
    const selectedModel = availableModels.find((candidate): candidate is ModelInput => {
      if (!isModelInput(candidate)) return false;
      const projected = projectModelMetadata(candidate);
      return projected?.provider === requested.provider && projected?.id === requested.id;
    });
    if (selectedModel === undefined) {
      throw new ModelControlError("model_unavailable", `Model ${requested.provider}/${requested.id} is not available`);
    }
    let switched: boolean | undefined;
    try {
      switched = await setModel(selectedModel);
    } catch (error) {
      const after = nativeProjection(context);
      if (!sameModelIdentity(nativeBefore.current, projectModelMetadata(selectedModel))
        && sameModelIdentity(after.current, projectModelMetadata(selectedModel))) {
        trace("extension_model_set_result", {
          requested_provider: requested.provider,
          requested_model_id: requested.id,
          switched: true,
          mutation_before_throw: true,
        });
        return selectedModel;
      }
      throw error;
    }
    trace("extension_model_set_result", {
      requested_provider: requested.provider,
      requested_model_id: requested.id,
      switched: switched === true,
    });
    if (switched !== true) {
      throw new ModelControlError("model_switch_rejected", `Pi rejected model ${requested.provider}/${requested.id}`);
    }
    return selectedModel;
  }

  async function handleControl({ controlId, sourceAgentId, action, model }: ModelControlRequest): Promise<ModelControlResult> {
    const context = getParentContext();
    trace("extension_model_control_entered", {
      control_id: controlId,
      source_agent_id: sourceAgentId,
      action,
      ...(model?.provider === undefined ? {} : { requested_provider: model.provider }),
      ...(model?.id === undefined ? {} : { requested_model_id: model.id }),
      available: context !== undefined,
    });
    try {
      if (action !== "set_model") {
        throw new ModelControlError("unsupported_model_control", "unsupported model control action");
      }
      if (context === undefined) {
        throw new ModelControlError("model_switch_unavailable", "Pi model switching is unavailable");
      }
      const switchedModel = await switchModel(model ?? {}, context);
      rememberSuccessfulModelSwitch(context, switchedModel);
      refresh(context);
      const projection = currentForContext(context);
      trace("extension_model_control_completed", {
        control_id: controlId,
        source_agent_id: sourceAgentId,
        accepted: true,
        ...(projection.current?.provider === undefined ? {} : { current_provider: projection.current.provider }),
        ...(projection.current?.id === undefined ? {} : { current_model_id: projection.current.id }),
      });
      return { accepted: true, currentModel: projection.current };
    } catch (error) {
      const projection = currentForContext(context);
      trace("extension_model_control_completed", {
        control_id: controlId,
        source_agent_id: sourceAgentId,
        accepted: false,
        error_code: modelControlErrorCode(error),
        error_name: error instanceof Error ? error.name : "Error",
        ...(projection.current?.provider === undefined ? {} : { current_provider: projection.current.provider }),
        ...(projection.current?.id === undefined ? {} : { current_model_id: projection.current.id }),
      });
      return {
        accepted: false,
        errorCode: modelControlErrorCode(error),
        currentModel: projection.current,
      };
    }
  }

  return {
    catalog: () => {
      const context = getParentContext();
      if (context === undefined) throw new Error("Pi context is unavailable");
      return projectAvailableModels(context);
    },
    current,
    handleControl,
    onContext: () => { current(); },
    reset: () => { modelSwitchOverride = undefined; },
  };
}

export type GoalControlState = object;
export type GoalControlResult<T extends GoalControlState = Record<string, unknown>> = { accepted: boolean; errorCode?: string; goalState?: T };

export function createGoalControlBridge<T extends GoalControlState>({
  emit,
  getContext,
  getState,
  normalizeState,
  setState,
  publish,
  cloneState = (state) => state,
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
}: {
  emit: (event: string, value: Record<string, unknown>) => void;
  getContext: () => unknown;
  getState: () => T;
  normalizeState: (value: unknown) => T | undefined;
  setState: (state: T) => void;
  publish: () => void;
  cloneState?: (state: T) => T;
  setTimeoutImpl?: typeof setTimeout;
  clearTimeoutImpl?: typeof clearTimeout;
}): (control: unknown) => Promise<GoalControlResult<T>> {
  const clone = (state: T): T => cloneState(state);
  const safeRejection = (errorCode = "unavailable"): GoalControlResult<T> => ({
    accepted: false,
    errorCode,
    goalState: clone(getState()),
  });
  const normalizeResult = (result: unknown): GoalControlResult<T> => {
    if (!isRecord(result) || typeof result.accepted !== "boolean") return safeRejection("malformed_response");
    if (result.accepted === true && result.goalState === undefined) return safeRejection("malformed_response");
    const responseState = result.goalState === undefined ? clone(getState()) : normalizeState(result.goalState);
    if (responseState === undefined) return safeRejection("malformed_response");
    setState(responseState);
    publish();
    return {
      accepted: result.accepted,
      ...(typeof result.errorCode === "string" && /^[a-z][a-z0-9_]{0,127}$/.test(result.errorCode)
        ? { errorCode: result.errorCode } : {}),
      goalState: clone(responseState),
    };
  };

  return async (control: unknown): Promise<GoalControlResult<T>> => {
    const record = isRecord(control) ? control : undefined;
    const request = record === undefined ? undefined : {
      correlationId: record.correlationId ?? record.controlId,
      controlId: record.controlId,
      sourceAgentId: record.sourceAgentId,
      targetInstanceId: record.targetInstanceId ?? record.instanceId,
      revision: record.revision,
      action: record.action,
      goal: record.goal,
      settings: record.settings,
      context: getContext(),
    };
    if (request?.controlId === undefined || request?.targetInstanceId === undefined) return safeRejection("invalid_control");
    let timer: ReturnType<typeof setTimeout> | undefined;
    let response: unknown;
    let responded = false;
    let resolveResponse: (value: unknown) => void = () => {};
    const responsePromise = new Promise<unknown>((resolve) => { resolveResponse = resolve; });
    const respond = (value: unknown) => {
      if (responded) return;
      responded = true;
      resolveResponse(value);
    };
    try {
      emit(GOAL_CONTROL_REQUEST_EVENT, { ...request, respond });
    } catch {
      return safeRejection("unavailable");
    }
    if (!responded) {
      timer = setTimeoutImpl(() => respond(undefined), GOAL_CONTROL_RESPONSE_TIMEOUT_MS);
      timer?.unref?.();
      response = await responsePromise;
      if (timer !== undefined) clearTimeoutImpl(timer);
    } else {
      response = await responsePromise;
    }
    if (!responded || response === undefined) return safeRejection("unavailable");
    return normalizeResult(response);
  };
}
