import { randomUUID } from "node:crypto";

import {
  hashCanonicalJson,
  parameterSchemaJson,
  ROOT_ONLY_OPERATION_IDS,
  type DesiredManifest,
  type JsonRecord,
  type ManifestTool,
  validateArguments,
  validateDesiredManifest,
} from "./manifest.js";
import { validateControlResultPayload } from "./protocol.js";

type ToolResult = { content: Array<{ type: "text"; text: string }>; details: JsonRecord; isError?: boolean };
type PiTool = {
  name: string;
  label: string;
  description: string;
  parameters: JsonRecord;
  promptSnippet?: string;
  promptGuidelines?: string[];
  execute: (toolCallId: string, input: unknown, signal?: AbortSignal, onUpdate?: unknown, context?: unknown) => Promise<ToolResult>;
};
type PiRuntime = { getAllTools?: () => unknown[]; registerTool: (tool: PiTool) => void };

export type RuntimeFence = { runtimeGeneration: string; manifestSha256: string; sessionEpoch: number };
export type ToolRegistryOptions = {
  emitControl: (request: JsonRecord) => Promise<void> | void;
  getRuntime: () => RuntimeFence;
  getContextRef: (context: unknown) => string | undefined;
  onRequestQueued: (request: JsonRecord) => void;
  // Root request delivery must be registered before Pi can flush its completed
  // event; child requests retain their existing bridge-owned post-send path.
  onRootRequestPrepared?: (request: JsonRecord) => void;
  onRootRequestSendFailed?: (request: JsonRecord) => void;
  onFatalRegistrationFailure?: (error: unknown) => void;
};

type BootstrapEnvelope = JsonRecord & {
  protocol: "mecha.bootstrap/v1";
  desired_manifest: DesiredManifest;
  manifest_sha256: string;
  runtime_generation: string;
  session_epoch: number;
};
type SupplementalEnvelope = JsonRecord & {
  protocol: "mecha.tools-install/v1";
  desired_manifest: DesiredManifest;
  manifest_sha256: string;
  base_manifest_sha256: string;
  runtime_generation: string;
  session_epoch: number;
  added_name: string;
};

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;
const SHA256 = /^[a-f0-9]{64}$/;

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function errorResult(message: string): ToolResult {
  return { content: [{ type: "text", text: message }], details: {}, isError: true };
}

function toolResult(name: string, requestId: string): ToolResult {
  return {
    content: [{ type: "text", text: `Queued router operation ${name} (${requestId}). The result will arrive in a later control message.` }],
    details: { request_id: requestId, status: "queued" },
  };
}

function assertEnvelopeFence(value: JsonRecord, expectedProtocol: string, runtime: RuntimeFence): void {
  if (value.protocol !== expectedProtocol || typeof value.runtime_generation !== "string" || typeof value.session_epoch !== "number") {
    throw new TypeError("Invalid managed tool installation envelope");
  }
  if (value.runtime_generation !== runtime.runtimeGeneration || value.session_epoch !== runtime.sessionEpoch) {
    throw new TypeError("Stale managed tool installation envelope");
  }
  if (typeof value.delivery_id !== "string" || !UUID.test(value.delivery_id)) throw new TypeError("Installation delivery ID is invalid");
}

function validateBootstrapProjections(inlinePolicy: unknown, recallProjection: unknown): void {
  if (!isRecord(inlinePolicy) || Object.keys(inlinePolicy).some((key) => key !== "enabled" && key !== "credits") || typeof inlinePolicy.enabled !== "boolean" || !isRecord(inlinePolicy.credits)) {
    throw new TypeError("Bootstrap inline policy projection is invalid");
  }
  for (const [name, count] of Object.entries(inlinePolicy.credits)) {
    if ((name !== "bash" && name !== "edit" && name !== "write") || typeof count !== "number" || !Number.isSafeInteger(count) || count < 0 || count > 5) {
      throw new TypeError("Bootstrap inline credits are invalid");
    }
  }
  if (!isRecord(recallProjection) || Object.keys(recallProjection).some((key) => key !== "enabled" && key !== "stores" && key !== "content")
    || typeof recallProjection.content !== "string" || Buffer.byteLength(recallProjection.content, "utf8") > 24_000) {
    throw new TypeError("Bootstrap recall projection is invalid");
  }
  if (recallProjection.enabled !== undefined && typeof recallProjection.enabled !== "boolean") throw new TypeError("Bootstrap recall enabled flag is invalid");
  if (recallProjection.stores !== undefined && (!Array.isArray(recallProjection.stores) || recallProjection.stores.length > 64
    || recallProjection.stores.some((store) => typeof store !== "string" || Buffer.byteLength(store, "utf8") > 256))) {
    throw new TypeError("Bootstrap recall stores are invalid");
  }
}

function validateBootstrapEnvelope(value: unknown, runtime: RuntimeFence): BootstrapEnvelope {
  if (!isRecord(value)) throw new TypeError("Bootstrap envelope must be an object");
  const allowed = new Set(["protocol", "delivery_id", "runtime_generation", "session_epoch", "desired_manifest", "manifest_sha256", "restored_request_ids", "inline_policy", "recall_projection"]);
  if (Object.keys(value).some((key) => !allowed.has(key))) throw new TypeError("Bootstrap envelope has unexpected fields");
  assertEnvelopeFence(value, "mecha.bootstrap/v1", runtime);
  const desired = validateDesiredManifest(value.desired_manifest, { requireBaselineCapabilities: true });
  if (typeof value.manifest_sha256 !== "string" || !SHA256.test(value.manifest_sha256) || value.manifest_sha256 !== hashCanonicalJson(desired)) {
    throw new TypeError("Bootstrap manifest hash is invalid");
  }
  if (!Array.isArray(value.restored_request_ids) || value.restored_request_ids.some((id) => typeof id !== "string" || !UUID.test(id))) {
    throw new TypeError("Bootstrap request restoration is invalid");
  }
  validateBootstrapProjections(value.inline_policy, value.recall_projection);
  const runtimeGeneration = value.runtime_generation;
  const sessionEpoch = value.session_epoch;
  if (typeof runtimeGeneration !== "string" || typeof sessionEpoch !== "number") throw new TypeError("Bootstrap runtime identity is invalid");
  return { ...value, protocol: "mecha.bootstrap/v1", desired_manifest: desired, manifest_sha256: value.manifest_sha256, runtime_generation: runtimeGeneration, session_epoch: sessionEpoch };
}

function validateSupplementalEnvelope(value: unknown, current: DesiredManifest, runtime: RuntimeFence): SupplementalEnvelope {
  if (!isRecord(value)) throw new TypeError("Supplemental envelope must be an object");
  const allowed = new Set(["protocol", "delivery_id", "runtime_generation", "session_epoch", "base_manifest_sha256", "desired_manifest", "manifest_sha256", "added_name"]);
  if (Object.keys(value).some((key) => !allowed.has(key))) throw new TypeError("Supplemental envelope has unexpected fields");
  assertEnvelopeFence(value, "mecha.tools-install/v1", runtime);
  const desired = validateDesiredManifest(value.desired_manifest, { requireBaselineCapabilities: true });
  const currentHash = hashCanonicalJson(current);
  if (value.base_manifest_sha256 !== currentHash || typeof value.manifest_sha256 !== "string" || value.manifest_sha256 !== hashCanonicalJson(desired)) {
    throw new TypeError("Supplemental manifest identity is invalid");
  }
  if (desired.revision !== current.revision + 1 || typeof value.added_name !== "string") throw new TypeError("Supplemental manifest revision is invalid");
  const previous = new Map(current.tools.map((tool) => [tool.name, tool]));
  const newTools = desired.tools.filter((tool) => !previous.has(tool.name));
  if (newTools.length !== 1 || newTools[0]?.name !== value.added_name || newTools[0]?.name !== `${newTools[0]?.capability_id}__${newTools[0]?.definition_sha256}`) {
    throw new TypeError("Supplemental manifest must add exactly one immutable name");
  }
  for (const tool of current.tools) {
    const target = desired.tools.find((candidate) => candidate.name === tool.name);
    if (target === undefined || hashCanonicalJson(target) !== hashCanonicalJson(tool)) throw new TypeError("Supplemental manifest mutates an installed name");
  }
  const baseManifestHash = value.base_manifest_sha256;
  const runtimeGeneration = value.runtime_generation;
  const sessionEpoch = value.session_epoch;
  if (typeof baseManifestHash !== "string" || typeof runtimeGeneration !== "string" || typeof sessionEpoch !== "number") {
    throw new TypeError("Supplemental runtime identity is invalid");
  }
  return { ...value, protocol: "mecha.tools-install/v1", desired_manifest: desired, manifest_sha256: value.manifest_sha256, base_manifest_sha256: baseManifestHash, runtime_generation: runtimeGeneration, session_epoch: sessionEpoch, added_name: value.added_name };
}

function runtimeArgumentsBounded(value: unknown, depth = 1, state = { nodes: 0, bytes: 0 }): value is import("./manifest.js").JsonValue {
  if (depth > 8 || ++state.nodes > 2_048) return false;
  if (value === null) { state.bytes += 4; return state.bytes <= 32 * 1024; }
  if (typeof value === "boolean") { state.bytes += value ? 4 : 5; return state.bytes <= 32 * 1024; }
  if (typeof value === "number") {
    state.bytes += Buffer.byteLength(String(value), "utf8");
    return Number.isFinite(value) && Math.abs(value) <= Number.MAX_SAFE_INTEGER && state.bytes <= 32 * 1024;
  }
  if (typeof value === "string") {
    const bytes = Buffer.byteLength(value, "utf8");
    state.bytes += bytes;
    return bytes <= 8 * 1024 && state.bytes <= 32 * 1024;
  }
  if (Array.isArray(value)) return value.length <= 256 && value.every((entry) => runtimeArgumentsBounded(entry, depth + 1, state));
  if (!isRecord(value) || Object.keys(value).length > 256) return false;
  return Object.entries(value).every(([key, entry]) => {
    const keyBytes = Buffer.byteLength(key, "utf8");
    state.bytes += keyBytes;
    return key !== "__proto__" && key !== "prototype" && key !== "constructor" && keyBytes <= 8 * 1024 && state.bytes <= 32 * 1024 && runtimeArgumentsBounded(entry, depth + 1, state);
  });
}

export function createToolRegistry(pi: PiRuntime, options: ToolRegistryOptions) {
  let accepted: DesiredManifest | undefined;
  let fatal = false;
  const rawCalls = new Map<string, { name: string; arguments: unknown }>();

  const existingNames = (): Set<string> => new Set((pi.getAllTools?.() ?? []).flatMap((tool) => isRecord(tool) && typeof tool.name === "string" ? [tool.name] : []));

  const register = (tool: ManifestTool): void => {
    const closure: PiTool = {
      name: tool.name,
      label: tool.label,
      description: tool.description,
      parameters: parameterSchemaJson(tool.parameters),
      ...(tool.prompt_snippet === undefined ? {} : { promptSnippet: tool.prompt_snippet }),
      ...(tool.prompt_guidelines === undefined ? {} : { promptGuidelines: tool.prompt_guidelines }),
      async execute(toolCallId, input, _signal, _onUpdate, context) {
        if (fatal) return errorResult("Managed tool registration is unavailable after a registration failure.");
        const raw = rawCalls.get(toolCallId);
        if (raw === undefined || raw.name !== tool.name || !runtimeArgumentsBounded(raw.arguments) || !validateArguments(raw.arguments, tool.parameters)) {
          return errorResult("The router rejected tool arguments before control delivery.");
        }
        const runtime = options.getRuntime();
        if (accepted === undefined || runtime.manifestSha256 !== hashCanonicalJson(accepted)) return errorResult("The accepted managed manifest is stale.");
        const requestId = randomUUID();
        const contextRef = options.getContextRef(context);
        if (contextRef === undefined) return errorResult("The current managed context is unavailable.");
        if ((ROOT_ONLY_OPERATION_IDS as readonly string[]).includes(tool.operation_id) && contextRef !== "root") {
          return errorResult("This managed tool requires the root context.");
        }
        const request: JsonRecord = {
          protocol: "mecha.control/v1",
          request_id: requestId,
          tool_call_id: toolCallId,
          capability_id: tool.capability_id,
          tool_name: tool.name,
          definition_id: tool.definition_id,
          definition_sha256: tool.definition_sha256,
          operation_id: tool.operation_id,
          manifest_sha256: runtime.manifestSha256,
          runtime_generation: runtime.runtimeGeneration,
          session_epoch: runtime.sessionEpoch,
          context_ref: contextRef,
          arguments: structuredClone(raw.arguments),
        };
        let rootRegistered = false;
        try {
          if (contextRef === "root") {
            options.onRootRequestPrepared?.(request);
            rootRegistered = true;
          }
          const emission = options.emitControl(request);
          if (contextRef === "root") {
            // Root tools acknowledge after local enqueue. Waiting for Pi's send
            // promise would hold the current turn and deadlock an external reset
            // that needs that turn to settle before it can replace the session.
            void Promise.resolve(emission).catch(() => { options.onRootRequestSendFailed?.(request); });
            return toolResult(tool.name, requestId);
          }
          // Child result delivery remains ordered behind the bridge's send.
          await emission;
          options.onRequestQueued(request);
          return toolResult(tool.name, requestId);
        } catch {
          if (rootRegistered) options.onRootRequestSendFailed?.(request);
          return errorResult("The managed control request was not queued.");
        }
      },
    };
    pi.registerTool(closure);
  };

  const installValidatedFull = (desired: DesiredManifest, checkExistingNames = true, omitRootOnly = false): DesiredManifest => {
    if (accepted !== undefined || fatal) throw new TypeError("A full manifest may install only into a fresh extension runtime");
    const installedTools = omitRootOnly
      ? desired.tools.filter((tool) => !(ROOT_ONLY_OPERATION_IDS as readonly string[]).includes(tool.operation_id))
      : desired.tools;
    const names = checkExistingNames ? existingNames() : new Set<string>();
    for (const tool of installedTools) if (names.has(tool.name)) throw new TypeError(`Managed tool name collision: ${tool.name}`);
    try {
      for (const tool of installedTools) register(tool);
    } catch (error) {
      fatal = true;
      options.onFatalRegistrationFailure?.(error);
      throw error;
    }
    accepted = desired;
    return accepted;
  };

  const installFull = (value: unknown): DesiredManifest => {
    const envelope = validateBootstrapEnvelope(value, options.getRuntime());
    return installValidatedFull(envelope.desired_manifest);
  };

  /**
   * Install a previously accepted parent manifest while the child loader builds
   * its extension set. Pi actions such as getAllTools are intentionally not
   * available at this phase; the already-validated bounded snapshot supplies
   * its own duplicate proof, and Pi rejects any loader-level name collision.
   */
  const installAcceptedSnapshot = (value: DesiredManifest): DesiredManifest => {
    const desired = validateDesiredManifest(value, { requireBaselineCapabilities: true });
    // Native child factories retain the full accepted identity/fence, but the
    // root-only workflow transition definition is never registered or exposed.
    return installValidatedFull(desired, false, true);
  };

  const installSupplemental = (value: unknown): DesiredManifest => {
    if (accepted === undefined || fatal) throw new TypeError("A supplemental manifest requires an accepted full manifest");
    const envelope = validateSupplementalEnvelope(value, accepted, options.getRuntime());
    const name = envelope.added_name;
    if (existingNames().has(name)) throw new TypeError(`Managed tool name collision: ${name}`);
    const tool = envelope.desired_manifest.tools.find((candidate) => candidate.name === name);
    if (tool === undefined) throw new TypeError("Supplemental manifest lost its new tool");
    try {
      register(tool);
    } catch (error) {
      fatal = true;
      options.onFatalRegistrationFailure?.(error);
      throw error;
    }
    accepted = envelope.desired_manifest;
    return accepted;
  };

  return {
    captureRaw(toolCallId: string, name: string, argumentsValue: unknown): void {
      if (typeof toolCallId !== "string" || typeof name !== "string" || !runtimeArgumentsBounded(argumentsValue)) return;
      rawCalls.set(toolCallId, { name, arguments: structuredClone(argumentsValue) });
      while (rawCalls.size > 512) {
        const oldest = rawCalls.keys().next().value;
        if (typeof oldest !== "string") break;
        rawCalls.delete(oldest);
      }
    },
    acceptedManifest: (): DesiredManifest | undefined => accepted === undefined ? undefined : structuredClone(accepted),
    installFull,
    installAcceptedSnapshot,
    installSupplemental,
    manifestHash: (): string | undefined => accepted === undefined ? undefined : hashCanonicalJson(accepted),
    validateControlResult: validateControlResultPayload,
    isManagedToolName(name: string): boolean {
      return accepted?.tools.some((tool) => tool.name === name) ?? false;
    },
    capturedRawCallIsValid(toolCallId: string, name: string): boolean {
      const raw = rawCalls.get(toolCallId);
      const tool = accepted?.tools.find((candidate) => candidate.name === name);
      return raw !== undefined && tool !== undefined && raw.name === name
        && runtimeArgumentsBounded(raw.arguments) && validateArguments(raw.arguments, tool.parameters);
    },
    // Native S4 proof uses this narrow test hook to exercise the installed
    // manifest definition without creating a provider/model turn. It shares
    // the production envelope and root-pending ordering exactly.
    async emitInstalledRootRequestForTest(name: string, argumentsValue: unknown, toolCallId: string): Promise<string> {
      const tool = accepted?.tools.find((candidate) => candidate.name === name);
      if (tool === undefined || !runtimeArgumentsBounded(argumentsValue) || !validateArguments(argumentsValue, tool.parameters)
        || typeof toolCallId !== "string" || toolCallId.length === 0) {
        throw new TypeError("Test root control arguments are invalid");
      }
      const installed = accepted;
      const runtime = options.getRuntime();
      if (installed === undefined || runtime.manifestSha256 !== hashCanonicalJson(installed)) throw new TypeError("The accepted managed manifest is stale.");
      const requestId = randomUUID();
      const request: JsonRecord = {
        protocol: "mecha.control/v1",
        request_id: requestId,
        tool_call_id: toolCallId,
        capability_id: tool.capability_id,
        tool_name: tool.name,
        definition_id: tool.definition_id,
        definition_sha256: tool.definition_sha256,
        operation_id: tool.operation_id,
        manifest_sha256: runtime.manifestSha256,
        runtime_generation: runtime.runtimeGeneration,
        session_epoch: runtime.sessionEpoch,
        context_ref: "root",
        arguments: structuredClone(argumentsValue),
      };
      options.onRootRequestPrepared?.(request);
      const emission = options.emitControl(request);
      // This fixture follows the production root contract: return after local
      // enqueue while the shared send promise settles in the background.
      void Promise.resolve(emission).catch(() => { options.onRootRequestSendFailed?.(request); });
      return requestId;
    },
  };
}
