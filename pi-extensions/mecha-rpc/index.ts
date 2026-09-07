import { randomUUID } from "node:crypto";

import { createChildBridge } from "./child-bridge.js";
import { hashCanonicalJson, type DesiredManifest, type JsonRecord } from "./manifest.js";
import { createPolicyBridge } from "./policy-bridge.js";
import {
  CUSTOM_MESSAGE_TYPES,
  decodeHiddenEnvelope,
  encodeHiddenEnvelope,
  HIDDEN_COMMAND_NAMES,
  LEGACY_COMMAND_NAMES,
  legacyCommandDefinitions,
  validateChildSnapshotEnvelope,
  validateControlResultPayload,
  validateMailboxDeliverEnvelope,
  validatePolicyApplyEnvelope,
} from "./protocol.js";
import { emitControlRequest, emitLegacyCommandControl, sendHiddenCustomMessage } from "./rpc-client.js";
import { createToolRegistry } from "./tool-registry.js";

type CommandContext = {
  sessionManager?: {
    getSessionId?: () => string | undefined;
    getHeader?: () => { parentSession?: unknown } | null | undefined;
  };
  ui?: { notify?: (message: string, level?: string) => void };
};
type Command = { description: string; handler: (argumentsText: string, context: CommandContext) => Promise<unknown> };
type PiApi = {
  getAllTools?: () => unknown[];
  getActiveTools?: () => string[];
  setActiveTools?: (toolNames: string[]) => void;
  getAllCommands?: () => unknown[];
  registerTool: (tool: object) => void;
  registerCommand: (name: string, command: Command) => void;
  sendMessage: (message: JsonRecord, options?: JsonRecord) => void | Promise<void>;
  on: (name: string, handler: (event: JsonRecord, context: CommandContext) => unknown) => void;
  events?: { on?: (name: string, handler: (event: JsonRecord) => void) => void };
};

type ChildExtensionFactoryService = {
  registerChildExtensionV1: (registration: {
    name: string;
    factory: (pi: PiApi) => void | Promise<void>;
  }) => () => void;
};

export type MechaRpcOptions = {
  getRuntimeGeneration?: () => string;
  getSessionEpoch?: () => number;
  /** A parent-validated fixed manifest installed synchronously by a child factory. */
  initialAcceptedManifest?: DesiredManifest;
};

const SUBAGENTS_SERVICE_REGISTRY = Symbol.for("@gotgenes/pi-subagents:service-registry");
const CHILD_FACTORY_NAME = "mecha-rpc-managed-child-v1";

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isUnknownRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** Read only the published public service capability; never its manager or child sessions. */
function isChildExtensionFactoryService(value: unknown): value is ChildExtensionFactoryService {
  return isUnknownRecord(value) && typeof value.registerChildExtensionV1 === "function";
}

function childExtensionFactoryService(ownerSessionId: string): ChildExtensionFactoryService | undefined {
  const registry = Object.getOwnPropertyDescriptor(globalThis, SUBAGENTS_SERVICE_REGISTRY)?.value;
  if (!isUnknownRecord(registry) || !(registry.services instanceof Map)) return undefined;
  const service = registry.services.get(ownerSessionId);
  return isChildExtensionFactoryService(service) ? service : undefined;
}

function sessionIdFromContext(context: unknown): string | undefined {
  if (!isUnknownRecord(context) || !isUnknownRecord(context.sessionManager) || typeof context.sessionManager.getSessionId !== "function") return undefined;
  const sessionId = context.sessionManager.getSessionId();
  return typeof sessionId === "string" && sessionId.length > 0 ? sessionId : undefined;
}

function isCommandContext(value: unknown): value is CommandContext {
  return isUnknownRecord(value)
    && (value.sessionManager === undefined || isUnknownRecord(value.sessionManager))
    && (value.ui === undefined || isUnknownRecord(value.ui));
}

function commandNames(value: unknown[]): Set<string> {
  const result = new Set<string>();
  for (const entry of value) {
    if (typeof entry === "string") result.add(entry.replace(/^\//, ""));
    else if (isRecord(entry) && typeof entry.name === "string") result.add(entry.name.replace(/^\//, ""));
  }
  return result;
}

function toolNames(value: unknown[]): Set<string> {
  const result = new Set<string>();
  for (const entry of value) if (isRecord(entry) && typeof entry.name === "string") result.add(entry.name);
  return result;
}

function contentBlocks(event: JsonRecord): JsonRecord[] {
  const message = isRecord(event.message) ? event.message : event;
  return Array.isArray(message.content) ? message.content.filter(isRecord) : [];
}

function requireCurrentRuntime(value: JsonRecord, generation: string, epoch: number, manifestHash?: string): void {
  if (value.runtime_generation !== generation || value.session_epoch !== epoch) throw new TypeError("STALE_SESSION");
  if (manifestHash !== undefined && value.manifest_sha256 !== undefined && value.manifest_sha256 !== manifestHash) throw new TypeError("STALE_PROCESS");
}

function requireControlResultRuntime(value: JsonRecord, generation: string, epoch: number, manifestHash?: string): void {
  if (value.runtime_generation !== generation || value.target_session_epoch !== epoch) throw new TypeError("STALE_SESSION");
  if (manifestHash !== undefined && value.manifest_sha256 !== manifestHash) throw new TypeError("STALE_PROCESS");
}

function configuredSessionEpoch(): number {
  const raw = process.env.MECHA_RPC_SESSION_EPOCH;
  if (raw === undefined) return 1;
  if (!/^[1-9][0-9]{0,15}$/.test(raw)) throw new TypeError("Managed session epoch environment is invalid");
  const value = Number(raw);
  if (!Number.isSafeInteger(value)) throw new TypeError("Managed session epoch environment is invalid");
  return value;
}

/** Registers the fixed Phase 1 transport. It never opens a router or Mailbox connection. */
export function createMechaRpcExtension(pi: PiApi, options: MechaRpcOptions = {}) {
  const generation = options.getRuntimeGeneration?.() ?? process.env.MECHA_RPC_RUNTIME_GENERATION ?? randomUUID();
  const epoch = options.getSessionEpoch?.() ?? configuredSessionEpoch();
  const initialAcceptedManifest = options.initialAcceptedManifest === undefined
    ? undefined
    : structuredClone(options.initialAcceptedManifest);
  if (typeof generation !== "string" || !UUID.test(generation) || typeof epoch !== "number" || !Number.isSafeInteger(epoch) || epoch < 1) throw new TypeError("Managed runtime identity is invalid");
  let acceptedHash: string | undefined;
  const rootPendingRequestIds = new Set<string>();
  const childBridge = createChildBridge({
    getOwnerSessionId: (context) => {
      if (!isUnknownRecord(context) || !isUnknownRecord(context.sessionManager) || typeof context.sessionManager.getSessionId !== "function") return undefined;
      const sessionId = context.sessionManager.getSessionId();
      return typeof sessionId === "string" && sessionId.length > 0 ? sessionId : undefined;
    },
    getSessionIdentity: (context) => {
      if (!isUnknownRecord(context) || !isUnknownRecord(context.sessionManager) || typeof context.sessionManager.getSessionId !== "function") return undefined;
      const sessionId = context.sessionManager.getSessionId();
      if (typeof sessionId !== "string" || sessionId.length === 0) return undefined;
      const header = typeof context.sessionManager.getHeader === "function" ? context.sessionManager.getHeader() : undefined;
      const parentSessionId = isUnknownRecord(header) && typeof header.parentSession === "string" && header.parentSession.length > 0
        ? header.parentSession
        : undefined;
      return { sessionId, ...(parentSessionId === undefined ? {} : { parentSessionId }) };
    },
  });
  const policyBridge = createPolicyBridge();
  const toolRegistry = createToolRegistry(pi, {
    emitControl: (request) => emitControlRequest(pi, request),
    getRuntime: () => ({ runtimeGeneration: generation, manifestSha256: acceptedHash ?? "", sessionEpoch: epoch }),
    getContextRef: (context) => {
      if (!isCommandContext(context)) return undefined;
      if (childBridge.isChildSession(context)) return childBridge.getLiveChildContextRef(context);
      if (!isUnknownRecord(context.sessionManager) || typeof context.sessionManager.getSessionId !== "function") return undefined;
      const sessionId = context.sessionManager.getSessionId();
      return typeof sessionId === "string" && sessionId.length > 0 ? "root" : undefined;
    },
    onFatalRegistrationFailure: () => {
      // Pi has no unregister API. Once a registry write fails, a partial tool set
      // could otherwise remain model-visible, so this incarnation must not continue.
      const exit = setTimeout(() => process.exit(1), 0);
      exit.unref();
    },
    onRequestQueued: (request) => {
      const requestId = request.request_id;
      const contextRef = request.context_ref;
      if (typeof requestId !== "string" || typeof contextRef !== "string") return;
      if (contextRef === "root") rootPendingRequestIds.add(requestId);
      else if (!childBridge.bindPendingRequestForContext(requestId, contextRef)) throw new TypeError("STALE_CHILD_CONTEXT");
      while (rootPendingRequestIds.size > 512) {
        const oldest = rootPendingRequestIds.values().next().value;
        if (typeof oldest !== "string") break;
        rootPendingRequestIds.delete(oldest);
      }
    },
  });
  // Inline factories execute while DefaultResourceLoader.reload() assembles the
  // child extension set. Register the trusted snapshot now so AgentSession sees
  // every managed definition before bindExtensions or a first model turn.
  if (initialAcceptedManifest !== undefined) {
    acceptedHash = hashCanonicalJson(toolRegistry.installAcceptedSnapshot(initialAcceptedManifest));
  }

  const commands = [...HIDDEN_COMMAND_NAMES, ...LEGACY_COMMAND_NAMES];
  let commandsRegistered = false;
  let registeredOwnerSessionId: string | undefined;
  let disposeChildFactory: (() => void) | undefined;
  const queuedCommands: Array<{ name: string; command: Command }> = [];
  const queueCommand = (name: string, command: Command): void => {
    queuedCommands.push({ name, command });
  };

  /** Register exactly one fixed child factory after this parent accepts its manifest. */
  const registerAcceptedChildFactory = (context: CommandContext): void => {
    if (initialAcceptedManifest !== undefined || disposeChildFactory !== undefined) return;
    const ownerSessionId = sessionIdFromContext(context);
    if (ownerSessionId === undefined) return;
    const service = childExtensionFactoryService(ownerSessionId);
    if (service === undefined) return;
    const acceptedManifest = toolRegistry.acceptedManifest();
    if (acceptedManifest === undefined) throw new TypeError("Managed child factory requires an accepted manifest.");
    // This closure intentionally captures only the cloned, parent-validated manifest.
    disposeChildFactory = service.registerChildExtensionV1({
      name: CHILD_FACTORY_NAME,
      factory: (childPi) => {
        createMechaRpcExtension(childPi, { initialAcceptedManifest: acceptedManifest });
      },
    });
  };

  const rejected = async (reason: string): Promise<void> => {
    await sendHiddenCustomMessage(pi, "mecha.tools.rejected.v1", "Managed tool manifest rejected.", {
      protocol: "mecha.tools/v1", reason: reason.slice(0, 2_000), runtime_generation: generation, session_epoch: epoch,
    }, { triggerTurn: false });
  };

  queueCommand("mecha-bootstrap-v1", {
    description: "Reserved managed bootstrap command.",
    async handler(argumentsText, context) {
      try {
        const envelope = decodeHiddenEnvelope(argumentsText);
        requireCurrentRuntime(envelope, generation, epoch);
        const desired = toolRegistry.installFull(envelope);
        acceptedHash = hashCanonicalJson(desired);
        registerAcceptedChildFactory(context);
        if (Array.isArray(envelope.restored_request_ids)) {
          for (const requestId of envelope.restored_request_ids) {
            if (typeof requestId === "string") rootPendingRequestIds.add(requestId);
          }
        }
        policyBridge.apply(isRecord(envelope.inline_policy) && isRecord(envelope.recall_projection)
          ? { inline: envelope.inline_policy, recall: { content: envelope.recall_projection.content ?? "" } }
          : {});
        await sendHiddenCustomMessage(pi, "mecha.tools.accepted.v1", "Managed tool manifest accepted.", {
          protocol: "mecha.tools/v1", manifest_sha256: acceptedHash, runtime_generation: generation, session_epoch: epoch,
        }, { triggerTurn: false });
        return { accepted: true };
      } catch (error) {
        await rejected(error instanceof Error ? error.message : "invalid bootstrap");
        throw error;
      }
    },
  });

  queueCommand("mecha-install-tools-v1", {
    description: "Reserved managed supplemental install command.",
    async handler(argumentsText) {
      try {
        const envelope = decodeHiddenEnvelope(argumentsText);
        requireCurrentRuntime(envelope, generation, epoch, acceptedHash);
        const desired = toolRegistry.installSupplemental(envelope);
        acceptedHash = hashCanonicalJson(desired);
        await sendHiddenCustomMessage(pi, "mecha.tools.accepted.v1", "Managed tool manifest accepted.", {
          protocol: "mecha.tools/v1", manifest_sha256: acceptedHash, runtime_generation: generation, session_epoch: epoch,
        }, { triggerTurn: false });
        return { accepted: true };
      } catch (error) {
        await rejected(error instanceof Error ? error.message : "invalid supplemental manifest");
        throw error;
      }
    },
  });

  queueCommand("mecha-control-result-v1", {
    description: "Reserved managed control result command.",
    async handler(argumentsText, context) {
      const payload = validateControlResultPayload(decodeHiddenEnvelope(argumentsText));
      requireControlResultRuntime(payload, generation, epoch, acceptedHash);
      if (childBridge.hasPendingRequest(context, payload.request_id)) return childBridge.appendControlResult(context, payload);
      if (!rootPendingRequestIds.has(payload.request_id)) throw new TypeError("STALE_CHILD_CONTEXT");
      const details: JsonRecord = {
        protocol: payload.protocol,
        result_id: payload.result_id,
        request_id: payload.request_id,
        target_session_epoch: payload.target_session_epoch,
        runtime_generation: payload.runtime_generation,
        manifest_sha256: payload.manifest_sha256,
        status: payload.status,
        details: payload.details,
        ...(payload.error === undefined ? {} : { error: payload.error }),
      };
      await sendHiddenCustomMessage(pi, "mecha.control.result.v1", payload.content, details, { triggerTurn: false });
      rootPendingRequestIds.delete(payload.request_id);
      return { kind: "accepted", result_id: payload.result_id };
    },
  });

  queueCommand("mecha-mailbox-deliver-v1", {
    description: "Reserved direct managed mailbox delivery command.",
    async handler(argumentsText) {
      const envelope = validateMailboxDeliverEnvelope(decodeHiddenEnvelope(argumentsText));
      requireCurrentRuntime(envelope, generation, epoch, acceptedHash);
      if (typeof envelope.content !== "string" || typeof envelope.message_id !== "string") throw new TypeError("INVALID_ENVELOPE");
      await sendHiddenCustomMessage(pi, "mecha.mailbox.appended.v1", envelope.content, {
        protocol: "mecha.mailbox/v1", message_id: envelope.message_id, runtime_generation: generation, session_epoch: epoch,
      }, { deliverAs: "followUp", triggerTurn: true });
      return { appended: true };
    },
  });

  queueCommand("mecha-policy-apply-v1", {
    description: "Reserved managed policy projection command.",
    async handler(argumentsText) {
      const envelope = validatePolicyApplyEnvelope(decodeHiddenEnvelope(argumentsText));
      requireCurrentRuntime(envelope, generation, epoch, acceptedHash);
      const projection = policyBridge.apply(envelope.policy);
      await sendHiddenCustomMessage(pi, "mecha.policy.applied.v1", "Managed policy applied.", {
        protocol: "mecha.policy/v1", runtime_generation: generation, session_epoch: epoch,
        applied: {
          inline_enabled: projection.inlineEnabled,
          inline_credits: { ...projection.inlineCredits },
          recall_content: projection.recallContent,
          settled_enabled: projection.settledEnabled,
          goal_lock_enabled: projection.goalLockEnabled,
          compaction_deferred: projection.compactionDeferred,
        },
      }, { triggerTurn: false });
      return projection;
    },
  });

  queueCommand("mecha-child-snapshot-v1", {
    description: "Reserved managed child snapshot command.",
    async handler(argumentsText, context) {
      const envelope = validateChildSnapshotEnvelope(decodeHiddenEnvelope(argumentsText));
      requireCurrentRuntime(envelope, generation, epoch, acceptedHash);
      const snapshot = childBridge.refreshSnapshot(context);
      await sendHiddenCustomMessage(pi, "mecha.child.snapshot.v1", "Managed child snapshot updated.", snapshot, { triggerTurn: false });
      return snapshot;
    },
  });

  const registerLegacyCommandBridge = (): void => {
    for (const name of LEGACY_COMMAND_NAMES) {
      const definition = legacyCommandDefinitions[name];
      queueCommand(name, {
        description: definition.description,
        async handler(argumentsText, context) {
          const result = await emitLegacyCommandControl(pi, definition, argumentsText, context, generation, epoch, childBridge.isChildSession(context));
          return { request_id: result.requestId, status: "queued" };
        },
      });
    }
  };
  const registerCommands = (): void => {
    if (commandsRegistered) return;
    const existingCommands = commandNames(pi.getAllCommands?.() ?? []);
    const existingTools = toolNames(pi.getAllTools?.() ?? []);
    for (const name of HIDDEN_COMMAND_NAMES) {
      if (existingCommands.has(name) || existingTools.has(name)) throw new TypeError(`Managed command collision: ${name}`);
    }
    // A child receives the accepted managed tools during loader construction.
    // Keep those tools callable rather than shadowing one with a legacy slash
    // command; the parent has no such tool until after its startup bridge exists.
    const legacyCommandsToRegister = new Set<string>();
    for (const name of LEGACY_COMMAND_NAMES) {
      if (existingCommands.has(name)) throw new TypeError(`Managed command collision: ${name}`);
      if (!existingTools.has(name)) legacyCommandsToRegister.add(name);
    }
    registerLegacyCommandBridge();
    if (queuedCommands.length !== commands.length) throw new TypeError("Managed command registry is incomplete");
    for (const { name, command } of queuedCommands) {
      if (HIDDEN_COMMAND_NAMES.some((hiddenName) => hiddenName === name) || legacyCommandsToRegister.has(name)) {
        pi.registerCommand(name, command);
      }
    }
    commandsRegistered = true;
  };

  // Pi only exposes registry inspection after its extension runtime starts. This
  // is still before get_commands or any model request can observe this extension.
  pi.on("session_start", async (_event, context) => {
    registeredOwnerSessionId = sessionIdFromContext(context);
    registerCommands();
    if (initialAcceptedManifest !== undefined) {
      const desired = toolRegistry.acceptedManifest();
      if (desired === undefined || acceptedHash === undefined) throw new TypeError("Managed child manifest installation was lost before bind.");
      // Child sessions begin with their agent profile's coding-tool allowlist.
      // The factory installed this manifest during loader reload, and this bind
      // callback makes its already-registered names model-visible before turn one.
      const active = pi.getActiveTools?.() ?? [];
      pi.setActiveTools?.([...new Set([...active, ...desired.tools.map((tool) => tool.name)])]);
      await sendHiddenCustomMessage(pi, "mecha.tools.accepted.v1", "Managed tool manifest accepted.", {
        protocol: "mecha.tools/v1", manifest_sha256: acceptedHash, runtime_generation: generation, session_epoch: epoch,
      }, { triggerTurn: false });
    }
  });
  pi.on("session_shutdown", () => {
    disposeChildFactory?.();
    disposeChildFactory = undefined;
    childBridge.clearOwnerSession(registeredOwnerSessionId);
    rootPendingRequestIds.clear();
    registeredOwnerSessionId = undefined;
  });

  pi.on("message_end", (event) => {
    for (const block of contentBlocks(event)) {
      if (block.type !== "toolCall" || typeof block.id !== "string" || typeof block.name !== "string") continue;
      toolRegistry.captureRaw(block.id, block.name, block.arguments);
    }
  });
  pi.on("tool_call", (event, context) => {
    const toolCallId = typeof event.toolCallId === "string" ? event.toolCallId : typeof event.tool_call_id === "string" ? event.tool_call_id : undefined;
    const name = typeof event.toolName === "string" ? event.toolName : typeof event.name === "string" ? event.name : undefined;
    if (toolCallId !== undefined && name !== undefined && toolRegistry.isManagedToolName(name) && !toolRegistry.capturedRawCallIsValid(toolCallId, name)) {
      return { block: true, reason: "Managed tool arguments were not captured as a valid raw value." };
    }
    if (name !== "bash" && name !== "edit" && name !== "write") return undefined;
    if (childBridge.isChildSession(context)) return undefined;
    if (policyBridge.consumeInlineCredit(name)) return undefined;
    return { block: true, reason: "This managed runtime requires an inline-tool allowance." };
  });
  pi.on("before_agent_start", (event) => {
    const recall = policyBridge.snapshot().recallContent;
    if (recall.length === 0 || typeof event.systemPrompt !== "string") return undefined;
    return { systemPrompt: `${event.systemPrompt}\n\n${recall}` };
  });

  pi.events?.on?.("subagents:lifecycle-v2", (event) => {
    try {
      const wire = childBridge.applyLifecycleDelta(event);
      void sendHiddenCustomMessage(pi, "mecha.child.delta.v1", "Managed child lifecycle updated.", wire, { triggerTurn: false });
    } catch {
      // Source events are optional. Invalid service-only data cannot enter router wire frames.
    }
  });

  return {
    bindPendingRequest: childBridge.bindPendingRequest,
    clearChildBindings: () => {
      childBridge.clearOwnerSession(registeredOwnerSessionId);
      rootPendingRequestIds.clear();
    },
    hashManifest: hashCanonicalJson,
    acceptedManifestHash: () => acceptedHash,
    protocol: { commands: commands.slice(), customTypes: CUSTOM_MESSAGE_TYPES.slice() },
  };
}

export default createMechaRpcExtension;
