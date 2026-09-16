import { randomUUID } from "node:crypto";
import { writeFileSync } from "node:fs";

import { getSubagentsService, type SubagentsService } from "@gotgenes/pi-subagents";
import type { ExtensionAPI } from "@earendil-works/pi-coding-agent";

import { createChildBridge } from "./child-bridge.js";
import { BoundedChildDeltaBuffer } from "./child-delta-buffer.js";
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
import {
  emitControlRequest,
  emitLegacyCommandControl,
  type ExplicitCustomMessageAppendOutcome,
  type ExplicitHiddenMessage,
  type ExplicitHiddenMessageOptions,
  sendHiddenCustomMessage,
  sendHiddenCustomMessageWithEntryId,
} from "./rpc-client.js";
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
  getCommands?: () => unknown[];
  registerTool(tool: object): void;
  registerCommand(name: string, command: Command): void;
  sendMessage(message: ExplicitHiddenMessage, options: ExplicitHiddenMessageOptions): Promise<ExplicitCustomMessageAppendOutcome>;
  sendMessage(message: JsonRecord, options?: JsonRecord): void | Promise<void>;
  on(name: string, handler: (event: JsonRecord, context: CommandContext) => unknown): void;
  events?: { on?: (name: string, handler: (event: JsonRecord) => void) => void };
};


export type MechaRpcOptions = {
  getRuntimeGeneration?: () => string;
  getSessionEpoch?: () => number;
  /** A parent-validated fixed manifest installed synchronously by a child factory. */
  initialAcceptedManifest?: DesiredManifest;
};

const CHILD_FACTORY_NAME = "mecha-rpc-managed-child-v1";
const FIXED_SUBAGENTS_TOOL_NAMES = ["subagent", "get_subagent_result", "steer_subagent"] as const;
// Pi owns these built-ins. They remain outside the router manifest because
// they execute locally; the existing tool_call policy guards mutations.
const FIXED_MANAGED_CODING_TOOL_NAMES = ["read", "bash", "edit", "write"] as const;
const MAX_INSTALL_DELIVERY_RECEIPTS = 128;

// These hooks exist only while the installed-Pi S4 fixture is enabled. They
// expose no production command or message type: the fixture uses them to drive
// the same installed root emitter and to return public-safe test observations.
type NativeRootControlTestHooks = {
  emit: () => Promise<string>;
  inspect: () => JsonRecord;
  release: () => void;
  duplicateAppend: () => Promise<ExplicitCustomMessageAppendOutcome>;
  changedAppend: () => Promise<ExplicitCustomMessageAppendOutcome>;
};

const nativeRootControlTestHooks = new Map<string, NativeRootControlTestHooks>();
export async function emitInstalledRootControlForTest(sessionId: string): Promise<string> {
  const hooks = nativeRootControlTestHooks.get(sessionId);
  if (hooks === undefined) throw new TypeError("S4 root control test hook is unavailable.");
  return hooks.emit();
}
export function inspectInstalledRootControlForTest(sessionId: string): JsonRecord {
  const hooks = nativeRootControlTestHooks.get(sessionId);
  if (hooks === undefined) throw new TypeError("S4 root control test hook is unavailable.");
  return hooks.inspect();
}
export function releaseInstalledRootControlForTest(sessionId: string): void {
  const hooks = nativeRootControlTestHooks.get(sessionId);
  if (hooks === undefined) throw new TypeError("S4 root control test hook is unavailable.");
  hooks.release();
}
export async function duplicateInstalledRootControlResultForTest(sessionId: string): Promise<ExplicitCustomMessageAppendOutcome> {
  const hooks = nativeRootControlTestHooks.get(sessionId);
  if (hooks === undefined) throw new TypeError("S4 root control test hook is unavailable.");
  return hooks.duplicateAppend();
}
export async function changedInstalledRootControlResultForTest(sessionId: string): Promise<ExplicitCustomMessageAppendOutcome> {
  const hooks = nativeRootControlTestHooks.get(sessionId);
  if (hooks === undefined) throw new TypeError("S4 root control test hook is unavailable.");
  return hooks.changedAppend();
}

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

type InstallationReceipt = {
  fingerprint: string;
  accepted: boolean;
  manifestSha256?: string;
  manifestRevision?: number;
  activeTools?: string[];
  reason?: string;
};

type InstallationDelivery = { deliveryId: string; fingerprint: string };

function isRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function isUnknownRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

type ChildExtensionFactoryService = Pick<SubagentsService, "registerChildExtensionV1">;

/** Read the package's public owner-bound service capability, never its manager or child sessions. */
function childExtensionFactoryService(ownerSessionId: string): ChildExtensionFactoryService | undefined {
  const service = getSubagentsService(ownerSessionId);
  return service !== undefined && typeof service.registerChildExtensionV1 === "function" ? service : undefined;
}
function installationDelivery(value: JsonRecord, protocol: "mecha.bootstrap/v1" | "mecha.tools-install/v1"): InstallationDelivery | undefined {
  if (value.protocol !== protocol || typeof value.delivery_id !== "string" || !UUID.test(value.delivery_id)) return undefined;
  return { deliveryId: value.delivery_id, fingerprint: hashCanonicalJson(value) };
}

function sessionIdFromContext(context: unknown): string | undefined {
  if (!isUnknownRecord(context) || !isUnknownRecord(context.sessionManager) || typeof context.sessionManager.getSessionId !== "function") return undefined;
  const sessionId = context.sessionManager.getSessionId();
  return typeof sessionId === "string" && sessionId.length > 0 ? sessionId : undefined;
}

/** Validates Pi's public ExtensionFactory boundary before entering the narrower RPC adapter. */
function isPiApi(value: unknown): value is PiApi {
  return isUnknownRecord(value)
    && typeof value.registerTool === "function"
    && typeof value.registerCommand === "function"
    && typeof value.sendMessage === "function"
    && typeof value.on === "function";
}

function isCommandContext(value: unknown): value is CommandContext {
  return isUnknownRecord(value)
    && (value.sessionManager === undefined || isUnknownRecord(value.sessionManager))
    && (value.ui === undefined || isUnknownRecord(value.ui));
}

function commandNames(value: unknown[]): Set<string> {
  const result = new Set<string>();
  const canonicalName = (name: string): string => name.replace(/^\//, "").replace(/:\d+$/, "");
  for (const entry of value) {
    if (typeof entry === "string") result.add(canonicalName(entry));
    else if (isRecord(entry) && typeof entry.name === "string") result.add(canonicalName(entry.name));
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
  const nativeRootControlTestEnabled = process.env.MECHA_RPC_TEST_ROOT_CONTROL_PROBE === "1";
  const nativeRootControlHoldFirstEmission = process.env.MECHA_RPC_TEST_ROOT_CONTROL_HOLD_FIRST_EMISSION === "1";
  let nativeRootControlNextOrdinal = 0;
  let nativeRootControlHoldAvailable = nativeRootControlHoldFirstEmission;
  type NativeRootControlTestRecord = {
    ordinal: number;
    requestId: string;
    pendingBeforeEmission: boolean;
    sendStarted: boolean;
    requestEmitted: boolean;
    sendResolved: boolean;
    sendRejectedBeforeEmission: boolean;
    pendingAfterSendFailure: boolean | undefined;
    pendingBeforeResult: boolean | undefined;
    sendResolvedBeforeResult: boolean | undefined;
    appendOutcome: ExplicitCustomMessageAppendOutcome["kind"] | undefined;
    pendingAfterAppend: boolean | undefined;
    release: (() => void) | undefined;
    releaseCalled: boolean;
    resultContent: string | undefined;
    resultDetails: JsonRecord | undefined;
    resultEntryId: string | undefined;
    duplicateOutcome: ExplicitCustomMessageAppendOutcome["kind"] | undefined;
    changedAppendAttempted: boolean;
    changedAppendRejected: boolean | undefined;
  };
  const nativeRootControlRecords = new Map<string, NativeRootControlTestRecord>();
  const nativeRootControlSnapshot = (): JsonRecord => ({
    records: [...nativeRootControlRecords.values()].map((record) => ({
      ordinal: record.ordinal,
      pending_before_emission: record.pendingBeforeEmission,
      send_started: record.sendStarted,
      request_emitted: record.requestEmitted,
      send_resolved: record.sendResolved,
      send_rejected_before_emission: record.sendRejectedBeforeEmission,
      ...(record.pendingAfterSendFailure === undefined ? {} : { pending_after_send_failure: record.pendingAfterSendFailure }),
      ...(record.pendingBeforeResult === undefined ? {} : { pending_before_result: record.pendingBeforeResult }),
      ...(record.sendResolvedBeforeResult === undefined ? {} : { send_resolved_before_result: record.sendResolvedBeforeResult }),
      ...(record.appendOutcome === undefined ? {} : { append_outcome: record.appendOutcome }),
      ...(record.pendingAfterAppend === undefined ? {} : { pending_after_append: record.pendingAfterAppend }),
      release_called: record.releaseCalled,
      ...(record.duplicateOutcome === undefined ? {} : { duplicate_outcome: record.duplicateOutcome }),
      changed_append_attempted: record.changedAppendAttempted,
      ...(record.changedAppendRejected === undefined ? {} : { changed_append_rejected: record.changedAppendRejected }),
    })),
  });
  const nativeRootControlObservationPath = nativeRootControlTestEnabled ? process.env.MECHA_RPC_TEST_ROOT_CONTROL_OBSERVATION_PATH : undefined;
  const publishNativeRootControlObservation = (): void => {
    if (nativeRootControlObservationPath === undefined) return;
    writeFileSync(nativeRootControlObservationPath, JSON.stringify(nativeRootControlSnapshot()));
  };
  const installationReceipts = new Map<string, InstallationReceipt>();
  const retainInstallationReceipt = (delivery: InstallationDelivery | undefined, receipt: InstallationReceipt): void => {
    if (delivery === undefined || installationReceipts.has(delivery.deliveryId)) return;
    while (installationReceipts.size >= MAX_INSTALL_DELIVERY_RECEIPTS) {
      const oldest = installationReceipts.keys().next().value;
      if (typeof oldest !== "string") break;
      installationReceipts.delete(oldest);
    }
    installationReceipts.set(delivery.deliveryId, receipt);
  };
  const sendInstallationReceipt = async (receipt: InstallationReceipt): Promise<void> => {
    if (receipt.accepted) {
      if (receipt.manifestSha256 === undefined) throw new TypeError("Accepted installation receipt is invalid.");
      await sendHiddenCustomMessage(pi, "mecha.tools.accepted.v1", "Managed tool manifest accepted.", {
        protocol: "mecha.tools/v1", manifest_sha256: receipt.manifestSha256, runtime_generation: generation, session_epoch: epoch,
        ...(receipt.manifestRevision === undefined ? {} : { manifest_revision: receipt.manifestRevision }),
        ...(receipt.activeTools === undefined ? {} : { active_tools: [...receipt.activeTools] }),
      }, { triggerTurn: false });
      return;
    }
    await sendHiddenCustomMessage(pi, "mecha.tools.rejected.v1", "Managed tool manifest rejected.", {
      protocol: "mecha.tools/v1", reason: receipt.reason ?? "invalid installation", runtime_generation: generation, session_epoch: epoch,
    }, { triggerTurn: false });
  };
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
  type GoalEvaluationTurn = {
    evaluationId: string;
    goalRevision: number;
    settledSequence: number;
    manifestHash: string;
    goal: string;
    activeTools: string[];
    assistantText: string;
    // Pi lifecycle events do not identify the turn that emitted them. Keep
    // this slot occupied through cancellation and only replace it after its
    // terminal event has restored the exact tool snapshot it captured.
    phase: "armed" | "running" | "cancelled";
    runtimeGeneration: string;
    sessionEpoch: number;
  };
  let goalEvaluationTurn: GoalEvaluationTurn | undefined;
  const evaluatorPrompt = (goal: string): string => `Evaluate the managed goal using the available conversation only.\nGoal: ${goal}\nReturn exactly two lines:\nDONE|NOT_DONE\nREASON: <concise reason>\nDo not use tools or take actions.`;
  const isCurrentGoalEvaluationTurn = (evaluator: GoalEvaluationTurn): boolean => goalEvaluationTurn === evaluator
    && evaluator.runtimeGeneration === generation
    && evaluator.sessionEpoch === epoch
    && evaluator.manifestHash === acceptedHash;
  const clearGoalEvaluationTurn = (evaluator: GoalEvaluationTurn | undefined = goalEvaluationTurn): void => {
    if (evaluator === undefined || goalEvaluationTurn !== evaluator) return;
    // The slot becomes available only after its owner restores the snapshot.
    // This is deliberately single-flight: lifecycle events contain no turn ID.
    goalEvaluationTurn = undefined;
    pi.setActiveTools?.(evaluator.activeTools);
  };
  const cancelGoalEvaluationTurn = (goalRevision: number | undefined): void => {
    const evaluator = goalEvaluationTurn;
    if (evaluator === undefined) return;
    // A repeated policy projection for this exact revision is idempotent.
    if (goalRevision !== undefined && evaluator.goalRevision === goalRevision) return;
    // Do not clear the slot yet. A queued lifecycle event still belongs to this
    // evaluator and must be allowed to reach the path that restores its tools.
    evaluator.phase = "cancelled";
    evaluator.assistantText = "";
  };
  const boundedGoalEvaluation = (text: string): { outcome: "DONE" | "NOT_DONE"; reason: string } => {
    const lines = text.split(/\r?\n/);
    const verdict = lines.length === 2 ? /^(DONE|NOT_DONE)$/.exec(lines[0] ?? "")?.[1] : undefined;
    const reason = lines.length === 2 ? /^REASON: (.*)$/.exec(lines[1] ?? "")?.[1] : undefined;
    if ((verdict !== "DONE" && verdict !== "NOT_DONE") || reason === undefined || reason.length === 0 || reason.trim() !== reason || Buffer.byteLength(reason, "utf8") > 512) return { outcome: "NOT_DONE", reason: "invalid evaluator output" };
    return { outcome: verdict, reason: reason.replace(/\s+/g, " ") };
  };
  let runtimeRegistrationFailed = false;
  const safeRegistrationFailure = (cause: unknown): string => {
    if (cause instanceof Error && /^(Managed tool|Managed command) collision: [a-z_]+$/.test(cause.message)) {
      // Tool and command names are fixed manifest identifiers, not user input.
      return cause.message;
    }
    return "Managed extension registry setup failed.";
  };
  const failClosedRegistration = (cause?: unknown): void => {
    if (runtimeRegistrationFailed) return;
    runtimeRegistrationFailed = true;
    // Pi has no unregister API. Any failed registry write may leave a partial
    // command or tool set model-visible, so this process cannot safely continue.
    // Keep one safe stderr diagnostic for the router's bounded crash projection.
    console.error(safeRegistrationFailure(cause));
    const exit = setTimeout(() => process.exit(1), 0);
    exit.unref();
  };
  // The installed-Pi S4 proof can force one post-registration emission failure.
  // This test-only seam is disabled outside its dedicated native process.
  const forcedRootControlSendFailureAt = process.env.MECHA_RPC_TEST_ROOT_CONTROL_SEND_FAILURE_AT;
  let rootControlEmissionCount = 0;
  const toolRegistry = createToolRegistry(pi, {
    emitControl: async (request) => {
      const requestId = typeof request.request_id === "string" ? request.request_id : undefined;
      const record = requestId === undefined ? undefined : nativeRootControlRecords.get(requestId);
      if (record !== undefined) {
        record.sendStarted = true;
        publishNativeRootControlObservation();
      }
      if (nativeRootControlTestEnabled && forcedRootControlSendFailureAt !== undefined) {
        rootControlEmissionCount++;
        if (String(rootControlEmissionCount) === forcedRootControlSendFailureAt) {
          if (record !== undefined) {
            record.sendRejectedBeforeEmission = true;
            publishNativeRootControlObservation();
          }
          throw new Error("forced S4 root-control send failure");
        }
      }
      await emitControlRequest(pi, request);
      if (record === undefined) return;
      record.requestEmitted = true;
      publishNativeRootControlObservation();
      if (nativeRootControlHoldAvailable) {
        nativeRootControlHoldAvailable = false;
        await new Promise<void>((resolve) => { record.release = resolve; });
      }
      record.sendResolved = true;
      publishNativeRootControlObservation();
    },
    getRuntime: () => ({ runtimeGeneration: generation, manifestSha256: acceptedHash ?? "", sessionEpoch: epoch }),
    getContextRef: (context) => {
      if (!isCommandContext(context)) return undefined;
      if (childBridge.isChildSession(context)) return childBridge.getLiveChildContextRef(context);
      if (!isUnknownRecord(context.sessionManager) || typeof context.sessionManager.getSessionId !== "function") return undefined;
      const sessionId = context.sessionManager.getSessionId();
      return typeof sessionId === "string" && sessionId.length > 0 ? "root" : undefined;
    },
    onFatalRegistrationFailure: (error) => failClosedRegistration(error),
    onRequestQueued: (request) => {
      const requestId = request.request_id;
      const contextRef = request.context_ref;
      if (typeof requestId !== "string" || typeof contextRef !== "string" || contextRef === "root") return;
      if (!childBridge.bindPendingRequestForContext(requestId, contextRef)) throw new TypeError("STALE_CHILD_CONTEXT");
    },
    onRootRequestPrepared: (request) => {
      const requestId = request.request_id;
      if (typeof requestId !== "string" || !UUID.test(requestId)) throw new TypeError("INVALID_ROOT_REQUEST");
      if (!rootPendingRequestIds.has(requestId) && rootPendingRequestIds.size >= 512) throw new TypeError("ROOT_REQUEST_CAPACITY");
      rootPendingRequestIds.add(requestId);
      if (nativeRootControlTestEnabled) {
        nativeRootControlRecords.set(requestId, {
          ordinal: ++nativeRootControlNextOrdinal, requestId, pendingBeforeEmission: rootPendingRequestIds.has(requestId), sendStarted: false,
          requestEmitted: false, sendResolved: false, sendRejectedBeforeEmission: false, pendingAfterSendFailure: undefined,
          pendingBeforeResult: undefined, sendResolvedBeforeResult: undefined, appendOutcome: undefined, pendingAfterAppend: undefined,
          release: undefined, releaseCalled: false, resultContent: undefined, resultDetails: undefined, resultEntryId: undefined,
          duplicateOutcome: undefined, changedAppendAttempted: false, changedAppendRejected: undefined,
        });
        publishNativeRootControlObservation();
      }
    },
    onRootRequestSendFailed: (request) => {
      const requestId = request.request_id;
      if (typeof requestId !== "string") return;
      rootPendingRequestIds.delete(requestId);
      const record = nativeRootControlRecords.get(requestId);
      if (record !== undefined) {
        record.pendingAfterSendFailure = rootPendingRequestIds.has(requestId);
        publishNativeRootControlObservation();
      }
    },
  });
  // Inline factories execute while DefaultResourceLoader.reload() assembles the
  // child extension set. Register the trusted snapshot now so AgentSession sees
  // every managed definition before bindExtensions or a first model turn.
  if (initialAcceptedManifest !== undefined) {
    acceptedHash = hashCanonicalJson(toolRegistry.installAcceptedSnapshot(initialAcceptedManifest));
  }

  const rootControlTestCommand = process.env.MECHA_RPC_TEST_ROOT_CONTROL_PROBE === "1"
    ? "mecha-s4-root-control-probe-v1"
    : undefined;
  const commands = [...HIDDEN_COMMAND_NAMES, ...LEGACY_COMMAND_NAMES, ...(rootControlTestCommand === undefined ? [] : [rootControlTestCommand])];
  // Stock-Pi acceptance tests can deterministically exercise the no-unregister
  // failure path without changing production registration behavior.
  const forcedCommandRegistrationFailureAt = /^\d+$/.test(process.env.MECHA_RPC_TEST_FAIL_COMMAND_REGISTRATION_AT ?? "")
    ? Number(process.env.MECHA_RPC_TEST_FAIL_COMMAND_REGISTRATION_AT)
    : undefined;
  let commandRegistrationAttempts = 0;
  let commandsRegistered = false;
  let commandRegistrationFailed = false;
  let registeredOwnerSessionId: string | undefined;
  let disposeChildFactory: (() => void) | undefined;
  let disposeCompletionHook: (() => void) | undefined;
  // Each entry is consumed by its matching completion hook; shutdown clears the in-flight set.
  const terminalDeltaAcknowledgements = new Map<string, Promise<boolean>>();
  const childDeltaBuffer = new BoundedChildDeltaBuffer(
    async (wire) => sendHiddenCustomMessage(pi, "mecha.child.delta.v1", "Managed child lifecycle updated.", wire, { triggerTurn: false }),
    (ownerSessionId) => registeredOwnerSessionId === ownerSessionId,
  );
  // The source increments lifecycle sequence before its asynchronous custom
  // message append settles. The bounded FIFO preserves that order; overflow or
  // exhausted retry leaves a sequence gap for router-side snapshot recovery.
  const enqueueChildDelta = (wire: JsonRecord): void => {
    const ownerSessionId = wire.owner_session_id;
    if (typeof ownerSessionId !== "string") return;
    const acknowledgement = childDeltaBuffer.enqueue(ownerSessionId, wire);
    const changes = wire.changes;
    const lifecycleState = isRecord(changes) ? changes.lifecycle_state : undefined;
    if (typeof wire.task_id === "string" && (lifecycleState === "completed" || lifecycleState === "error" || lifecycleState === "stopped" || lifecycleState === "aborted" || lifecycleState === "steered")) {
      terminalDeltaAcknowledgements.set(wire.task_id, acknowledgement);
    }
  };
  const registerCompletionHook = (ownerSessionId: string | undefined): void => {
    disposeCompletionHook?.();
    disposeCompletionHook = undefined;
    if (ownerSessionId === undefined) return;
    const service = getSubagentsService(ownerSessionId);
    if (service === undefined || typeof service.registerBeforeCompletionHookV1 !== "function") return;
    disposeCompletionHook = service.registerBeforeCompletionHookV1(async (record) => {
      if (registeredOwnerSessionId !== ownerSessionId) throw new Error("Managed child session closed before terminal delta append.");
      const acknowledgement = terminalDeltaAcknowledgements.get(record.id);
      if (acknowledgement === undefined) return;
      terminalDeltaAcknowledgements.delete(record.id);
      if (!await acknowledgement) throw new Error("Managed terminal child delta was not durably appended.");
    });
  };
  const queuedCommands: Array<{ name: string; command: Command }> = [];
  const queueCommand = (name: string, command: Command): void => {
    queuedCommands.push({ name, command });
  };

  /** Register exactly one fixed child factory after this parent accepts its manifest. */
  const makeAcceptedToolsModelVisible = (desired: DesiredManifest): void => {
    // Keep the three fixed pi-subagents tools available to preserve the
    // existing managed child bridge. Managed launches explicitly exclude
    // ambient Cursor/other-extension tools; stock Pi keeps its prior tools so
    // supplemental collision and child-bridge behavior remains compatible.
    const installed = toolNames(pi.getAllTools?.() ?? []);
    const active = pi.getActiveTools?.() ?? [];
    const desiredNames = new Set(desired.tools.map((tool) => tool.name));
    const ambient = process.env.MECHA_MANAGED_ACTIVE_ONLY === "1"
      ? [...FIXED_MANAGED_CODING_TOOL_NAMES, ...FIXED_SUBAGENTS_TOOL_NAMES].filter((name) => installed.has(name))
      : active.filter((name) => !desiredNames.has(name));
    pi.setActiveTools?.([
      ...ambient,
      ...desiredNames,
    ]);
  };
  const activeToolsForReceipt = (): string[] | undefined => {
    const active = pi.getActiveTools?.();
    if (active === undefined) return undefined;
    if (!Array.isArray(active) || active.some((name) => typeof name !== "string" || name.length === 0)) {
      throw new TypeError("Pi active tool registry is invalid.");
    }
    return [...active];
  };

  const registerAcceptedChildFactory = (context: CommandContext): void => {
    if (initialAcceptedManifest !== undefined) return;
    const ownerSessionId = sessionIdFromContext(context);
    if (ownerSessionId === undefined) return;
    const service = childExtensionFactoryService(ownerSessionId);
    if (service === undefined) return;
    registerCompletionHook(ownerSessionId);
    const acceptedManifest = toolRegistry.acceptedManifest();
    if (acceptedManifest === undefined) throw new TypeError("Managed child factory requires an accepted manifest.");
    // Existing children retain their spawn snapshot. Replacing this registration
    // only gives future children the newly accepted add-only manifest.
    disposeChildFactory?.();
    disposeChildFactory = undefined;
    // This closure intentionally captures only the cloned, parent-validated manifest.
    disposeChildFactory = service.registerChildExtensionV1({
      name: CHILD_FACTORY_NAME,
      factory: (childPi: ExtensionAPI) => {
        if (!isPiApi(childPi)) throw new TypeError("Pi child extension API is invalid.");
        createMechaRpcExtension(childPi, { initialAcceptedManifest: acceptedManifest });
      },
    });
  };

  const replayInstallationReceipt = async (delivery: InstallationDelivery | undefined): Promise<InstallationReceipt | undefined> => {
    if (delivery === undefined) return undefined;
    const receipt = installationReceipts.get(delivery.deliveryId);
    if (receipt === undefined) return undefined;
    if (receipt.fingerprint !== delivery.fingerprint) throw new TypeError("Installation delivery ID was reused with a different envelope.");
    await sendInstallationReceipt(receipt);
    return receipt;
  };

  queueCommand("mecha-bootstrap-v1", {
    description: "Reserved managed bootstrap command.",
    async handler(argumentsText, context) {
      let delivery: InstallationDelivery | undefined;
      let replayed = false;
      let settled = false;
      try {
        const envelope = decodeHiddenEnvelope(argumentsText);
        delivery = installationDelivery(envelope, "mecha.bootstrap/v1");
        const prior = await replayInstallationReceipt(delivery);
        if (prior !== undefined) {
          replayed = true;
          if (!prior.accepted) throw new TypeError(prior.reason ?? "Managed tool manifest rejected.");
          return { accepted: true, replayed: true };
        }
        requireCurrentRuntime(envelope, generation, epoch);
        const desired = toolRegistry.installFull(envelope);
        acceptedHash = hashCanonicalJson(desired);
        makeAcceptedToolsModelVisible(desired);
        registerAcceptedChildFactory(context);
        if (Array.isArray(envelope.restored_request_ids)) {
          const restored = new Set<string>();
          for (const requestId of envelope.restored_request_ids) {
            if (typeof requestId !== "string" || !UUID.test(requestId)) throw new TypeError("INVALID_ROOT_REQUEST");
            restored.add(requestId);
          }
          const newRequestCount = [...restored].filter((requestId) => !rootPendingRequestIds.has(requestId)).length;
          if (rootPendingRequestIds.size + newRequestCount > 512) throw new TypeError("ROOT_REQUEST_CAPACITY");
          for (const requestId of restored) rootPendingRequestIds.add(requestId);
        }
        policyBridge.apply(isRecord(envelope.inline_policy) && isRecord(envelope.recall_projection)
          ? { inline: envelope.inline_policy, recall: { content: envelope.recall_projection.content ?? "" } }
          : {});
        const receipt: InstallationReceipt = {
          fingerprint: delivery?.fingerprint ?? "", accepted: true, manifestSha256: acceptedHash,
          manifestRevision: desired.revision, activeTools: activeToolsForReceipt(),
        };
        retainInstallationReceipt(delivery, receipt);
        settled = true;
        await sendInstallationReceipt(receipt);
        return { accepted: true };
      } catch (error) {
        if (!replayed && !settled) {
          const reason = (error instanceof Error ? error.message : "invalid bootstrap").slice(0, 2_000);
          const receipt: InstallationReceipt = { fingerprint: delivery?.fingerprint ?? "", accepted: false, reason };
          retainInstallationReceipt(delivery, receipt);
          await sendInstallationReceipt(receipt);
        }
        throw error;
      }
    },
  });

  queueCommand("mecha-install-tools-v1", {
    description: "Reserved managed supplemental install command.",
    async handler(argumentsText, context) {
      let delivery: InstallationDelivery | undefined;
      let replayed = false;
      let settled = false;
      try {
        const envelope = decodeHiddenEnvelope(argumentsText);
        delivery = installationDelivery(envelope, "mecha.tools-install/v1");
        const prior = await replayInstallationReceipt(delivery);
        if (prior !== undefined) {
          replayed = true;
          if (!prior.accepted) throw new TypeError(prior.reason ?? "Managed supplemental manifest rejected.");
          return { accepted: true, replayed: true };
        }
        // The supplemental envelope carries the next manifest hash; the registry
        // independently fences its base hash against the current accepted manifest.
        requireCurrentRuntime(envelope, generation, epoch);
        const desired = toolRegistry.installSupplemental(envelope);
        acceptedHash = hashCanonicalJson(desired);
        makeAcceptedToolsModelVisible(desired);
        registerAcceptedChildFactory(context);
        const receipt: InstallationReceipt = {
          fingerprint: delivery?.fingerprint ?? "", accepted: true, manifestSha256: acceptedHash,
          manifestRevision: desired.revision, activeTools: activeToolsForReceipt(),
        };
        retainInstallationReceipt(delivery, receipt);
        settled = true;
        await sendInstallationReceipt(receipt);
        return { accepted: true };
      } catch (error) {
        if (!replayed && !settled) {
          const reason = (error instanceof Error ? error.message : "invalid supplemental manifest").slice(0, 2_000);
          const receipt: InstallationReceipt = { fingerprint: delivery?.fingerprint ?? "", accepted: false, reason };
          retainInstallationReceipt(delivery, receipt);
          await sendInstallationReceipt(receipt);
        }
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
      const nativeRecord = nativeRootControlRecords.get(payload.request_id);
      if (nativeRecord !== undefined) {
        nativeRecord.pendingBeforeResult = rootPendingRequestIds.has(payload.request_id);
        nativeRecord.sendResolvedBeforeResult = nativeRecord.sendResolved;
        publishNativeRootControlObservation();
      }
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
      const entryId = `mcr1_${payload.result_id.replaceAll("-", "")}`;
      // The result is a deferred hidden append while the root turn is active.
      // Pi flushes it after the tool-result continuation, preserving the exact
      // entry ID without starting a second model turn.
      const outcome = await sendHiddenCustomMessageWithEntryId(
        pi,
        "mecha.control.result.v1",
        payload.content,
        details,
        entryId,
      );
      if (outcome.entryId !== entryId) throw new TypeError("Explicit control-result entry ID mismatch.");
      if (nativeRecord !== undefined) {
        nativeRecord.appendOutcome = outcome.kind;
        nativeRecord.resultContent = payload.content;
        nativeRecord.resultDetails = structuredClone(details);
        nativeRecord.resultEntryId = entryId;
      }
      rootPendingRequestIds.delete(payload.request_id);
      if (nativeRecord !== undefined) {
        nativeRecord.pendingAfterAppend = rootPendingRequestIds.has(payload.request_id);
        publishNativeRootControlObservation();
      }
      return { kind: outcome.kind, result_id: payload.result_id };
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
      if (envelope.evaluator !== undefined) {
        const request = envelope.evaluator;
        if (!isRecord(request) || acceptedHash === undefined || envelope.manifest_sha256 !== acceptedHash) throw new TypeError("Managed goal evaluator is unavailable.");
        // A second request is rejected, not overwritten or queued. Pi omits
        // turn identity from lifecycle events, so one live evaluator is the
        // only deterministic ownership model. The router keeps this request
        // recoverable by retrying after the active evaluator settles.
        if (goalEvaluationTurn !== undefined) throw new TypeError("Managed goal evaluator is busy; retry after the active evaluator settles.");
        const evaluator: GoalEvaluationTurn = {
          evaluationId: String(request.evaluation_id), goalRevision: Number(request.goal_revision), settledSequence: Number(request.settled_sequence), manifestHash: acceptedHash, goal: String(request.goal),
          activeTools: pi.getActiveTools?.() ?? [], assistantText: "", phase: "armed", runtimeGeneration: generation, sessionEpoch: epoch,
        };
        // sendMessage({ triggerTurn: true }) starts a turn directly, without
        // before_agent_start. Isolate the evaluator before enqueuing its hidden
        // message so its provider request cannot advertise normal tools.
        pi.setActiveTools?.([]);
        goalEvaluationTurn = evaluator;
        try {
          await sendHiddenCustomMessage(pi, "mecha.policy.applied.v1", evaluatorPrompt(evaluator.goal), {
            protocol: "mecha.policy/v1", manifest_sha256: evaluator.manifestHash, runtime_generation: generation, session_epoch: epoch,
            evaluator: { evaluation_id: evaluator.evaluationId, goal_revision: evaluator.goalRevision, settled_sequence: evaluator.settledSequence, phase: "requested" },
          }, { deliverAs: "followUp", triggerTurn: true });
        } catch (error) {
          clearGoalEvaluationTurn(evaluator);
          throw error;
        }
        return { queued: true };
      }
      const projection = policyBridge.apply(envelope.policy);
      if (projection.goalControl !== undefined) cancelGoalEvaluationTurn(projection.goalControl.action === "set" ? projection.goalControl.revision : undefined);
      await sendHiddenCustomMessage(pi, "mecha.policy.applied.v1", "Managed policy applied.", {
        protocol: "mecha.policy/v1", runtime_generation: generation, session_epoch: epoch,
        applied: {
          inline_enabled: projection.inlineEnabled,
          inline_credits: { ...projection.inlineCredits }, recall_content: projection.recallContent,
          settled_enabled: projection.settledEnabled, goal_lock_enabled: projection.goalLockEnabled, compaction_deferred: projection.compactionDeferred,
          ...(projection.goalControl === undefined ? {} : { goal_revision: projection.goalControl.revision }),
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

  // The router invokes this only after Pi's native compact command has
  // resolved. Keeping the note in Pi makes it a real post-compaction handoff,
  // rather than a router-side imitation that can survive a failed compaction.
  queueCommand("mecha-post-compaction-notification-v1", {
    description: "Reserved managed post-compaction notification command.",
    async handler(argumentsText, context) {
      const envelope = decodeHiddenEnvelope(argumentsText);
      if (!isRecord(envelope) || Object.keys(envelope).length !== 5
        || envelope.protocol !== "mecha.post-compaction/v1"
        || typeof envelope.notification !== "string"
        || Buffer.byteLength(envelope.notification, "utf8") === 0
        || Buffer.byteLength(envelope.notification, "utf8") > 2_000
        || childBridge.isChildSession(context)) {
        throw new TypeError("Managed post-compaction notification is invalid.");
      }
      requireCurrentRuntime(envelope, generation, epoch, acceptedHash);
      await pi.sendMessage({
        customType: "mecha.post_compaction.v1",
        content: envelope.notification,
        display: true,
        details: {
          protocol: envelope.protocol,
          runtime_generation: generation,
          session_epoch: epoch,
          manifest_sha256: acceptedHash ?? "",
        },
      }, { deliverAs: "followUp", triggerTurn: true });
      return { notified: true };
    },
  });

  const registerRootControlTestCommand = (): void => {
    if (rootControlTestCommand === undefined) return;
    queueCommand(rootControlTestCommand, {
      description: "Test-only S4 root control probe.",
      async handler(argumentsText, context) {
        const sessionId = sessionIdFromContext(context);
        if (sessionId === undefined) throw new TypeError("S4 probe session is unavailable.");
        switch (argumentsText.trim()) {
          case "goal-status":
            return { request_id: await emitInstalledRootControlForTest(sessionId) };
          case "release":
            releaseInstalledRootControlForTest(sessionId);
            return { released: true };
          case "observe":
            return inspectInstalledRootControlForTest(sessionId);
          case "duplicate-append": {
            const outcome = await duplicateInstalledRootControlResultForTest(sessionId);
            return { kind: outcome.kind };
          }
          case "changed-append":
            return changedInstalledRootControlResultForTest(sessionId);
          default:
            throw new TypeError("S4 probe arguments are invalid.");
        }
      },
    });
  };
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
    if (commandRegistrationFailed) throw new TypeError("Managed command registration previously failed.");
    try {
      const existingCommands = commandNames(pi.getCommands?.() ?? []);
      const existingTools = toolNames(pi.getAllTools?.() ?? []);
      for (const name of HIDDEN_COMMAND_NAMES) {
        if (existingCommands.has(name) || existingTools.has(name)) throw new TypeError(`Managed command collision: ${name}`);
      }
      // The child factory deliberately installs the documented goal tool before
      // session_start. That one managed child tool remains callable; every other
      // command or tool collision is a hard error rather than a silent omission.
      const legacyCommandsToRegister = new Set<string>();
      for (const name of LEGACY_COMMAND_NAMES) {
        const isDocumentedChildGoalTool = initialAcceptedManifest !== undefined
          && name === "goal"
          && existingTools.has(name)
          && toolRegistry.isManagedToolName(name);
        if (existingCommands.has(name) || (existingTools.has(name) && !isDocumentedChildGoalTool)) {
          throw new TypeError(`Managed command collision: ${name}`);
        }
        if (!isDocumentedChildGoalTool) legacyCommandsToRegister.add(name);
      }
      registerRootControlTestCommand();
      registerLegacyCommandBridge();
      if (queuedCommands.length !== commands.length) throw new TypeError("Managed command registry is incomplete");
      for (const { name, command } of queuedCommands) {
        if (HIDDEN_COMMAND_NAMES.some((hiddenName) => hiddenName === name) || legacyCommandsToRegister.has(name) || name === rootControlTestCommand) {
          commandRegistrationAttempts++;
          if (forcedCommandRegistrationFailureAt === commandRegistrationAttempts) {
            throw new Error(`forced managed command registration failure at ${commandRegistrationAttempts}`);
          }
          pi.registerCommand(name, {
            ...command,
            handler: async (argumentsText, context) => {
              if (commandRegistrationFailed || runtimeRegistrationFailed) throw new TypeError("Managed command registry is unavailable.");
              return command.handler(argumentsText, context);
            },
          });
        }
      }
      // The acceptance matrix includes the commit fence after all 15 command
      // writes. This test-only position proves a runtime cannot escape after a
      // complete-looking but uncommitted registry update.
      if (forcedCommandRegistrationFailureAt === commands.length + 1) {
        throw new Error(`forced managed command registration failure at ${forcedCommandRegistrationFailureAt}`);
      }
      commandsRegistered = true;
    } catch (error) {
      commandRegistrationFailed = true;
      failClosedRegistration(error);
      throw error;
    }
  };

  // Pi only exposes registry inspection after its extension runtime starts. This
  // is still before get_commands or any model request can observe this extension.
  pi.on("session_start", async (_event, context) => {
    const ownerSessionId = sessionIdFromContext(context);
    if (registeredOwnerSessionId !== ownerSessionId) {
      disposeCompletionHook?.();
      disposeCompletionHook = undefined;
      terminalDeltaAcknowledgements.clear();
      childDeltaBuffer.clear();
    }
    registeredOwnerSessionId = ownerSessionId;
    registerCompletionHook(ownerSessionId);
    registerCommands();
    if (nativeRootControlTestEnabled && registeredOwnerSessionId !== undefined && initialAcceptedManifest === undefined) {
      const ownerSessionId = registeredOwnerSessionId;
      nativeRootControlTestHooks.set(ownerSessionId, {
        emit: () => toolRegistry.emitInstalledRootRequestForTest("goal", { action: "status" }, `s4-native-${randomUUID()}`),
        inspect: nativeRootControlSnapshot,
        release: () => {
          const record = [...nativeRootControlRecords.values()].find((candidate) => candidate.release !== undefined && candidate.appendOutcome !== undefined);
          if (record === undefined || record.release === undefined || record.releaseCalled) throw new TypeError("S4 root control send-resolution gate is unavailable.");
          record.releaseCalled = true;
          const release = record.release;
          record.release = undefined;
          publishNativeRootControlObservation();
          release();
        },
        duplicateAppend: async () => {
          const record = [...nativeRootControlRecords.values()].find((candidate) => candidate.appendOutcome === "accepted" && candidate.resultContent !== undefined && candidate.resultDetails !== undefined && candidate.resultEntryId !== undefined);
          if (record === undefined || record.resultContent === undefined || record.resultDetails === undefined || record.resultEntryId === undefined) throw new TypeError("S4 root control result is unavailable.");
          const outcome = await sendHiddenCustomMessageWithEntryId(pi, "mecha.control.result.v1", record.resultContent, structuredClone(record.resultDetails), record.resultEntryId);
          record.duplicateOutcome = outcome.kind;
          publishNativeRootControlObservation();
          return outcome;
        },
        changedAppend: async () => {
          const record = [...nativeRootControlRecords.values()].find((candidate) => candidate.appendOutcome === "accepted" && candidate.resultContent !== undefined && candidate.resultDetails !== undefined && candidate.resultEntryId !== undefined);
          if (record === undefined || record.resultContent === undefined || record.resultDetails === undefined || record.resultEntryId === undefined) throw new TypeError("S4 root control result is unavailable.");
          record.changedAppendAttempted = true;
          publishNativeRootControlObservation();
          try {
            const outcome = await sendHiddenCustomMessageWithEntryId(pi, "mecha.control.result.v1", `${record.resultContent} changed`, structuredClone(record.resultDetails), record.resultEntryId);
            record.changedAppendRejected = false;
            publishNativeRootControlObservation();
            return outcome;
          } catch (error) {
            record.changedAppendRejected = true;
            publishNativeRootControlObservation();
            throw error;
          }
        },
      });
    }
    if (initialAcceptedManifest !== undefined) {
      const desired = toolRegistry.acceptedManifest();
      if (desired === undefined || acceptedHash === undefined) throw new TypeError("Managed child manifest installation was lost before bind.");
      // Child sessions begin with their agent profile's coding-tool allowlist.
      // The factory installed this manifest during loader reload, and this bind
      // callback makes its already-registered names model-visible before turn one.
      makeAcceptedToolsModelVisible(desired);
      const activeTools = activeToolsForReceipt();
      await sendHiddenCustomMessage(pi, "mecha.tools.accepted.v1", "Managed tool manifest accepted.", {
        protocol: "mecha.tools/v1", manifest_sha256: acceptedHash, runtime_generation: generation, session_epoch: epoch,
        manifest_revision: desired.revision, ...(activeTools === undefined ? {} : { active_tools: activeTools }),
      }, { triggerTurn: false });
    }
  });
  pi.on("session_shutdown", () => {
    clearGoalEvaluationTurn();
    disposeChildFactory?.();
    disposeChildFactory = undefined;
    childBridge.clearOwnerSession(registeredOwnerSessionId);
    if (registeredOwnerSessionId !== undefined) nativeRootControlTestHooks.delete(registeredOwnerSessionId);
    rootPendingRequestIds.clear();
    installationReceipts.clear();
    childDeltaBuffer.clear();
    terminalDeltaAcknowledgements.clear();
    registeredOwnerSessionId = undefined;
  });

  pi.on("agent_start", () => {
    const evaluator = goalEvaluationTurn;
    if (evaluator !== undefined && evaluator.phase === "armed" && isCurrentGoalEvaluationTurn(evaluator)) evaluator.phase = "running";
  });
  pi.on("message_end", (event) => {
    const evaluator = goalEvaluationTurn;
    if (evaluator !== undefined && evaluator.phase === "running" && isCurrentGoalEvaluationTurn(evaluator) && isRecord(event.message) && event.message.role === "assistant") {
      evaluator.assistantText = contentBlocks(event).filter((block) => block.type === "text" && typeof block.text === "string").map((block) => String(block.text)).join("").slice(0, 1024);
    }
    for (const block of contentBlocks(event)) {
      if (block.type !== "toolCall" || typeof block.id !== "string" || typeof block.name !== "string") continue;
      toolRegistry.captureRaw(block.id, block.name, block.arguments);
    }
  });
  pi.on("tool_call", (event, context) => {
    const evaluator = goalEvaluationTurn;
    if (evaluator !== undefined && evaluator.phase === "running" && isCurrentGoalEvaluationTurn(evaluator)) {
      return { block: true, reason: "Managed goal evaluation cannot call tools." };
    }
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
  pi.on("agent_settled", async () => {
    const evaluator = goalEvaluationTurn;
    if (evaluator === undefined || !isCurrentGoalEvaluationTurn(evaluator)) return;
    // An armed evaluator has not observed its own start boundary. Do not let a
    // normal or older settlement consume it. A cancelled armed evaluator keeps
    // ownership until that terminal event arrives, then releases its snapshot.
    if (evaluator.phase === "armed") return;
    const completed = evaluator.phase === "running";
    clearGoalEvaluationTurn(evaluator);
    if (!completed) return;
    const verdict = boundedGoalEvaluation(evaluator.assistantText);
    await sendHiddenCustomMessage(pi, "mecha.policy.applied.v1", "Managed goal evaluation completed.", {
      protocol: "mecha.policy/v1", manifest_sha256: evaluator.manifestHash, runtime_generation: evaluator.runtimeGeneration, session_epoch: evaluator.sessionEpoch,
      evaluator: { evaluation_id: evaluator.evaluationId, goal_revision: evaluator.goalRevision, settled_sequence: evaluator.settledSequence, phase: "result", outcome: verdict.outcome, reason: verdict.reason },
    }, { triggerTurn: false });
  });

  pi.events?.on?.("subagents:lifecycle-v2", (event) => {
    try {
      enqueueChildDelta(childBridge.applyLifecycleDelta(event));
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
