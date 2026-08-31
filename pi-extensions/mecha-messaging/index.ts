import { createMailboxClient, createNanoId, assertAgentId, routerHttpUrl, parseReplyArguments } from "./mailbox-client.js";
import {
  GOAL_ACTIVITY_EVENT,
  GOAL_CONTROL_STATE_EVENT,
  GOAL_STATE_EVENT,
  GOAL_STATE_REQUEST_EVENT,
  GOAL_WORKER_ID_EVENT,
} from "../mecha-goal/index.js";
import type { GoalPi } from "../mecha-goal/index.js";
import {
  createGoalControlBridge,
  createModelControlProjection,
  modelControlErrorCode,
} from "./model-control-projection.js";
import type {
  ModelControlProjection,
  ModelInput,
  ModelRegistry,
} from "./model-control-projection.js";
export { createMailboxClient } from "./mailbox-client.js";
export {
  appendMemoryCaptureDiagnostic,
  appendOrphanToolResultDiagnostic,
  createExtensionTrace,
  createNativeSubagentTrace,
  quarantineOrphanToolResults,
} from "./diagnostics-history.js";
import { randomUUID } from "node:crypto";
import { execFile, execFileSync } from "node:child_process";
import { createRecallState } from "./recall-state.js";
import {
  createActivityLifecycle,
  createSettledNotificationScheduler,
  summarizeNativeSubagents,
} from "./activity-lifecycle.js";
import type {
  ActivityContext,
  ActivityLifecycle,
  NativeSubagent,
  NativeSubagentService,
} from "./activity-lifecycle.js";
export { createSettledNotificationScheduler, summarizeNativeSubagents } from "./activity-lifecycle.js";
import {
  captureNativeSentinelSpawner,
  createPiMailboxDeliveryCoordinator,
} from "./delivery-coordinator.js";
import type { DeliveryCoordinator } from "./delivery-coordinator.js";
import { createCompactionOrchestration } from "./compaction-orchestration.js";
import type { CompactionOrchestration } from "./compaction-orchestration.js";
import { createRecallController } from "./recall-controller.js";
import { createRecallPipeline as createPipeline } from "./recall-pipeline.js";
import { runRecallEvaluator as evaluateRecall, validateRecallChoice as validateChoice } from "./recall-evaluator.js";
import { createMemoryCaptureLifecycle } from "./memory-capture.js";
import {
  appendMalformedToolCallDiagnostic,
  appendMemoryCaptureDiagnostic,
  appendOrphanToolResultDiagnostic,
  createNativeSubagentTrace,
  sanitizeContextHistory,
} from "./diagnostics-history.js";
import type { ExtensionTrace, HistoryMessage } from "./diagnostics-history.js";
import { registerOperationalTools } from "./operational-tools.js";
import { createReminderLockState, createRouterReminderReader } from "./reminder-lock.js";
import {
  DEFAULT_COMPACTION_POLICY,
  compactionModelKeys,
  compactionNotificationMessage,
  compactionPolicyFromEnvironment,
  effectiveCompactionPolicy,
  nextCompactionThreshold,
  postCompactionNotificationMessage,
  resolveCompactionPolicy,
  selectCompactionTier,
} from "./compaction-policy.js";
import type { CompactionLevelInput, CompactionPolicy, CompactionPolicyInput } from "./compaction-policy.js";

export {
  DEFAULT_COMPACTION_POLICY,
  compactionModelKeys,
  compactionNotificationMessage,
  compactionPolicyFromEnvironment,
  effectiveCompactionPolicy,
  nextCompactionThreshold,
  postCompactionNotificationMessage,
  resolveCompactionPolicy,
  selectCompactionTier,
};

const DEFAULT_ROUTER_URL = "http://127.0.0.1:8765";
export { createRecallState } from "./recall-state.js";
export { createRecallController } from "./recall-controller.js";
export { createRecallPipeline } from "./recall-pipeline.js";
export { runRecallEvaluator, validateRecallChoice } from "./recall-evaluator.js";

const MAILBOX_SEND_RATE_LIMIT_COUNT = 6;
const MAILBOX_SEND_RATE_LIMIT_WINDOW_MS = 60_000;
const CHILD_SESSION_IDS = Symbol.for("mecha.mailbox-v2.child-session-ids");
const MAX_GOAL_CHARS = 4_000;
const GOAL_OUTCOME_KEYS = ["COMPLETED", "NOT_DONE"];
const MAX_MEANINGFUL_ACTIVITY_IDS = 1_024;
// These tools change Mecha/control-plane state or move messages; their successful completion
// is not proof that the worker advanced its standing goal. Real work tools remain eligible.
const GOAL_ACTIVITY_EXCLUDED_TOOLS = new Set([
  "goal", "goal_snooze", "mecha_parent_message", "mecha_mailbox_send", "mecha_request_inline_tool_allowance",
  "mecha_recall", "mecha_settled_notifications", "mecha_context", "mecha_state", "mecha_priority",
  "mecha_verification_handoff", "mecha_reminder", "mecha_worker", "mecha_pr", "mecha_git", "mecha_router",
]);

const PI_SUBAGENTS_SERVICE_REGISTRY = Symbol.for("@gotgenes/pi-subagents:service-registry");

type JsonPrimitive = string | number | boolean | null;
type JsonValue = JsonPrimitive | JsonObject | JsonValue[] | undefined;
type JsonObject = { [key: string]: JsonValue };
type DynamicRecord = JsonObject;
type Environment = NodeJS.ProcessEnv;
type ErrorLike = Error | { code?: string; message?: string; name?: string; cause?: ErrorLike };
type GoalProjection = { enabled: boolean; goal: string };
type GoalControlInput = DynamicRecord & { enabled: boolean; goal: string; goalLockEnabled: boolean; snoozeEnabled: boolean; snoozeDurationMs: number; snoozeTurnLimit: number | null; snoozeBudgetCapMs: number | null; snoozeRefillYieldMs: number; snoozeRefillElapsedMs: number; outcomeCounts: DynamicRecord };
type GoalControlState = GoalProjection & {
  goalLockEnabled: boolean;
  snoozeEnabled: boolean;
  snoozeDurationMs: number;
  snoozeTurnLimit: number | null;
  snoozeBudgetCapMs: number | null;
  snoozeRefillYieldMs: number;
  snoozeRefillElapsedMs: number;
  outcomeCounts: Record<string, number>;
};
type SessionManager = { getBranch: () => SessionEntry[]; getSessionId: () => string; isPersisted: () => boolean; getSessionFile: () => string | undefined };
type Ui = { setStatus: (key: string, value: string | undefined) => void; notify: (message: string, level?: string) => void };
type PiContext = { sessionManager: SessionManager; model?: ModelInput; modelRegistry?: ModelRegistry; ui: Ui; isIdle?: () => boolean; getContextUsage?: () => ContextUsage | undefined; compact?: (options: CompactOptions) => void; abort?: () => void };
type ServiceRegistry = { get: (sessionId: string) => NativeSubagentService };
type RegistryContainer = { services?: ServiceRegistry };
type SubagentService = NativeSubagentService;
type SessionEntry = JsonObject & { type?: string; customType?: string; details?: JsonObject; message?: JsonObject };
type ContextUsage = { percent: number | null; tokens: number | null; contextWindow: number; model?: ModelInput };
type CompactResult = { tokensBefore?: number; estimatedTokensAfter?: number; firstKeptEntryId?: string; summary?: string };
type CompactOptions = { customInstructions?: string; onComplete?: (result: CompactResult) => void; onError?: (error: ErrorLike) => void };
type ServiceAccessor = (sessionId: string) => SubagentService | undefined;
type InlineToolName = "bash" | "edit" | "write";
type InlineAllowanceOptions = { now?: () => number };
type InlineAllowanceDetails = { toolName: InlineToolName; usageCount: number; grantedCount: number; remaining: number; availableWholeCredits: number; remainingPool: number };
type ConversationBlock = string | { type?: string; text?: string } | null | undefined;
type DeliveryItem = JsonObject & { messageId: string | undefined; messageIncarnationId: string | undefined; attemptNumber: number | undefined; deliveryExpiresAt: string | undefined };
function isMailboxMessage(value: ExternalMailboxMessage): value is MailboxMessage {
  return isRecord(value) && typeof value.id === "string";
}
function normalizeMailboxDelivery(value: ExternalMailboxDelivery): MailboxDelivery | undefined {
  if (!isRecord(value) || typeof value.messageId !== "string" || typeof value.attemptNumber !== "number") return undefined;
  const items = Array.isArray(value.items) ? value.items.filter(isDeliveryItem) : [];
  return { messageId: value.messageId, attemptNumber: value.attemptNumber, items };
}

function isDeliveryItem(value: JsonValue | undefined): value is DeliveryItem {
  return isRecord(value)
    && (value.messageId === undefined || typeof value.messageId === "string")
    && (value.messageIncarnationId === undefined || typeof value.messageIncarnationId === "string")
    && (value.attemptNumber === undefined || typeof value.attemptNumber === "number")
    && (value.deliveryExpiresAt === undefined || typeof value.deliveryExpiresAt === "string");
}
type MailboxMessage = { id: string; from?: string; to?: string; body?: string; subject?: string; source?: { kind?: string }; sentinel?: { model?: ModelInput } };
type MailboxDelivery = { messageId: string; attemptNumber: number; items: DeliveryItem[] };
type MailboxClient = ReturnType<typeof createMailboxClient>;
type ToolResult = { content: Array<{ type: string; text: string }>; details?: object & { cancelled?: boolean; canceled?: boolean }; isError?: boolean; cancelled?: boolean; canceled?: boolean; aborted?: boolean; error?: JsonValue; toolName?: string; toolCallId?: string };
type ToolCallEvent = JsonObject & { toolName?: string; toolCallId?: string; result?: ToolResult; isError?: boolean; cancelled?: boolean; canceled?: boolean; aborted?: boolean; details?: JsonObject; error?: JsonValue };
type PipelineRecallChoice = Parameters<RecallPipeline["recall"]>[0];
type RecallControllerSelection = Parameters<Parameters<typeof createRecallController>[0]["recall"]>[0];
function isRecallChoice(value: RecallControllerSelection): value is RecallControllerSelection & PipelineRecallChoice {
  if (!isRecord(value) || !Array.isArray(value.ids) || !Number.isSafeInteger(value.depth)) return false;
  return value.ids.every((id) => typeof id === "string");
}
type GoalControlRequest = JsonObject & { controlId?: string; targetInstanceId?: string; correlationId?: string; sourceAgentId?: string; revision?: number; action?: string; goal?: JsonValue; settings?: JsonValue; context?: PiContext };
type GoalControlResult = { accepted: boolean; errorCode?: string; goalState?: GoalControlState };
type ReminderLock = ReturnType<typeof createReminderLockState>;
type SendMessageOptions = { deliverAs?: string; triggerTurn?: boolean };
type MessagingPi = { sendMessage: GoalPi["sendMessage"] };
type PiEvent = JsonObject & { id?: string; status?: string; toolName?: string; toolCallId?: string; result?: ToolResult; isError?: boolean; cancelled?: boolean; canceled?: boolean; aborted?: boolean; error?: JsonValue; details?: JsonObject; message?: JsonObject; messages?: HistoryMessage[]; signal?: AbortSignal; preparation?: { messagesToSummarize?: HistoryMessage[]; turnPrefixMessages?: HistoryMessage[] } };
type GoalEvents = NonNullable<GoalPi["events"]>;
type PiEvents = { on: (name: string, handler: (event: PiEvent, context: PiContext) => unknown) => void; emit?: NonNullable<GoalEvents["emit"]> };
type MailboxClientOptions = NonNullable<Parameters<typeof createMailboxClient>[0]>;
type ExternalMailboxMessage = Parameters<NonNullable<MailboxClientOptions["onEnvelope"]>>[0];
type ExternalMailboxDelivery = Parameters<NonNullable<MailboxClientOptions["onEnvelope"]>>[1];
type ExternalGoalControl = Parameters<NonNullable<MailboxClientOptions["onGoalControl"]>>[0];
type ExternalReminderControl = Parameters<NonNullable<MailboxClientOptions["onReminderLockControl"]>>[0];
type ExternalTraceValue = Parameters<NonNullable<MailboxClientOptions["onLifecycleTrace"]>>[0];
type PiTool = { name: string; label: string; description: string; parameters: object; execute: (...args: never[]) => Promise<object> };
type PiRuntime = MessagingPi & { events: PiEvents; on(name: string, handler: (event: PiEvent, context: PiContext) => unknown): void; on(name: string, handler: GoalPi["on"] extends (event: string, handler: infer Handler) => void ? Handler : never): void; registerTool(tool: object): void; registerCommand(name: string, command: object): void; setModel?: (model: ModelInput) => Promise<boolean> | boolean; exec: (file: string, args: string[], options: { timeout: number }) => Promise<{ code: number; stderr: string }>; getSessionId?: () => string | undefined; appendEntry: GoalPi["appendEntry"] };
type MemoryCapture = ReturnType<typeof createMemoryCaptureLifecycle>;
type RateReservation = { timestamp: number };
type CompactionRequest = { action?: string; policy?: CompactionPolicyInput | readonly CompactionLevelInput[]; customInstructions?: string; postCompactionNotification?: string };
type ParentMessageInput = { message?: string };
type MailboxSendInput = { recipient?: string; body?: string; subject?: string };
type InlineAllowanceInput = { toolName?: string; usageCount?: number };
type RecallToolInput = { action?: string; stores?: string[] };
type SettledToolInput = { action?: string };
type ContextToolInput = CompactionRequest;
type ReminderReservation = { key: string; reservation: RateReservation };
type RecallState = ReturnType<typeof createRecallState>;
type RecallPipeline = ReturnType<typeof createPipeline>;
type CaptureLifecycleOptions = NonNullable<Parameters<typeof createMemoryCaptureLifecycle>[0]>;
type CaptureContextInput = Parameters<NonNullable<CaptureLifecycleOptions["context"]>>[0];
type CaptureApplyInput = Parameters<NonNullable<CaptureLifecycleOptions["apply"]>>[0];
type OperationalReminderLock = NonNullable<NonNullable<Parameters<typeof registerOperationalTools>[1]>["reminderLock"]>;
type OperationalMessagingOptions = Omit<Parameters<typeof registerOperationalTools>[1], "reminderLock"> & { reminderLock?: ReminderLock; getCurrentReminder?: Parameters<typeof createReminderLockState>[0]["getCurrentReminder"]; environment?: Environment; fetchImpl?: typeof fetch };
type ExtensionOptions = { resolveCurrentAgentId?: () => string | undefined; createClient?: typeof createMailboxClient; createCoordinator?: (context: PiContext, mailboxClient: MailboxClient) => DeliveryCoordinator; createSettledScheduler?: () => ReturnType<typeof createSettledNotificationScheduler>; setTimeoutImpl?: typeof setTimeout; clearTimeoutImpl?: typeof clearTimeout; setIntervalImpl?: typeof setInterval; clearIntervalImpl?: typeof clearInterval; nowImpl?: () => number; getSubagentsService?: ServiceAccessor; nativeSubagentTrace?: ExtensionTrace; operationalTools?: OperationalMessagingOptions };

function isRecord<T>(value: T): value is T & DynamicRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function errorMessage<T>(error: T): string {
  return error instanceof Error ? error.message : isRecord(error) && typeof error.message === "string" ? error.message : String(error);
}

function errorName<T>(error: T): string {
  return error instanceof Error ? error.name : isRecord(error) && typeof error.name === "string" ? error.name : "Error";
}

function errorCode<T>(error: T): string | undefined {
  return isRecord(error) && typeof error.code === "string" ? error.code : undefined;
}

function isServiceRegistry(value: object | JsonValue | undefined): value is ServiceRegistry {
  return isRecord(value) && typeof value.get === "function";
}

function normalizeGoalProjection(value: unknown): GoalProjection | undefined {
  if (!isRecord(value) || typeof value.enabled !== "boolean" || typeof value.goal !== "string"
    || value.goal.length > MAX_GOAL_CHARS) return undefined;
  if (!value.enabled && value.goal !== "") return undefined;
  if (value.enabled && value.goal.length === 0) return undefined;
  return { enabled: value.enabled, goal: value.enabled ? value.goal : "" };
}

function defaultGoalControlState(): GoalControlState {
  return {
    enabled: false, goal: "", goalLockEnabled: false, snoozeEnabled: true,
    snoozeDurationMs: 60_000, snoozeTurnLimit: null, snoozeBudgetCapMs: null,
    snoozeRefillYieldMs: 30_000, snoozeRefillElapsedMs: 60_000,
    outcomeCounts: Object.fromEntries(GOAL_OUTCOME_KEYS.map((key) => [key, 0])),
  };
}

function isGoalControlInput(value: unknown): value is GoalControlInput {
  return isRecord(value) && typeof value.enabled === "boolean" && typeof value.goal === "string"
    && typeof value.goalLockEnabled === "boolean" && typeof value.snoozeEnabled === "boolean"
    && typeof value.snoozeDurationMs === "number"
    && (typeof value.snoozeTurnLimit === "number" || value.snoozeTurnLimit === null)
    && (typeof value.snoozeBudgetCapMs === "number" || value.snoozeBudgetCapMs === null)
    && typeof value.snoozeRefillYieldMs === "number" && typeof value.snoozeRefillElapsedMs === "number"
    && isRecord(value.outcomeCounts);
}

function normalizeGoalControlState(value: unknown): GoalControlState | undefined {
  if (!isGoalControlInput(value) || value.goal.length > MAX_GOAL_CHARS || (!value.enabled && value.goal !== "") || (value.enabled && value.goal.length === 0)
    || !Number.isSafeInteger(value.snoozeDurationMs) || value.snoozeDurationMs <= 0
    || (value.snoozeTurnLimit !== null && (!Number.isSafeInteger(value.snoozeTurnLimit) || value.snoozeTurnLimit < 1))
    || (value.snoozeBudgetCapMs !== null && (!Number.isSafeInteger(value.snoozeBudgetCapMs) || value.snoozeBudgetCapMs < 1))
    || !Number.isSafeInteger(value.snoozeRefillYieldMs) || value.snoozeRefillYieldMs <= 0
    || !Number.isSafeInteger(value.snoozeRefillElapsedMs) || value.snoozeRefillElapsedMs <= 0) return undefined;
  const projection = normalizeGoalProjection(value);
  if (projection === undefined) return undefined;
  const outcomeCounts: Record<string, number> = {};
  for (const key of GOAL_OUTCOME_KEYS) {
    const count = value.outcomeCounts[key];
    if (typeof count !== "number" || !Number.isSafeInteger(count) || count < 0) return undefined;
    outcomeCounts[key] = count;
  }
  return { ...projection, goalLockEnabled: value.goalLockEnabled, snoozeEnabled: value.snoozeEnabled,
    snoozeDurationMs: value.snoozeDurationMs, snoozeTurnLimit: value.snoozeTurnLimit,
    snoozeBudgetCapMs: value.snoozeBudgetCapMs, snoozeRefillYieldMs: value.snoozeRefillYieldMs,
    snoozeRefillElapsedMs: value.snoozeRefillElapsedMs, outcomeCounts };
}

function cloneGoalControlState(state: GoalControlState): GoalControlState {
  return { ...state, outcomeCounts: { ...state.outcomeCounts } };
}

function getOwnerSubagentsService(ownerSessionId: string | undefined): SubagentService | undefined {
  const registry = Object.getOwnPropertyDescriptor(globalThis, PI_SUBAGENTS_SERVICE_REGISTRY)?.value;
  const services = isRecord(registry) && isServiceRegistry(registry.services) ? registry.services : undefined;
  return ownerSessionId !== undefined && services !== undefined ? services.get(ownerSessionId) : undefined;
}

export const INLINE_TOOL_ALLOWANCE_TOOL_NAME = "mecha_request_inline_tool_allowance";
const MECHA_MESSAGING_CURSOR_TOOL_METADATA = Object.freeze({
  mecha_parent_message: {
    promptSnippet: "Send a message from a native subagent to its parent",
    promptGuidelines: [
      "Use mecha_parent_message only in native subagent sessions to message the parent agent.",
      "Call pi__mecha_parent_message through the Cursor pi bridge.",
    ],
  },
  mecha_mailbox_send: {
    promptSnippet: "Send a durable Mailbox V2 message to another local agent",
    promptGuidelines: [
      "Use mecha_mailbox_send for supervisor, worker, and service replies with recipient, body, and optional subject.",
      "Call pi__mecha_mailbox_send through the Cursor pi bridge.",
    ],
  },
  [INLINE_TOOL_ALLOWANCE_TOOL_NAME]: {
    promptSnippet: "Request a temporary exception to the inline tool guard",
    promptGuidelines: [
      "Use mecha_request_inline_tool_allowance only for genuinely necessary top-level bash/edit/write work.",
      "Call pi__mecha_request_inline_tool_allowance through the Cursor pi bridge.",
    ],
  },
  mecha_recall: {
    promptSnippet: "Enable, disable, or inspect process-local Markdown recall",
    promptGuidelines: [
      "Use mecha_recall with action enable, disable, or status for recall stores.",
      "Call pi__mecha_recall through the Cursor pi bridge.",
    ],
  },
  mecha_settled_notifications: {
    promptSnippet: "Enable, disable, or inspect settled mailbox notifications",
    promptGuidelines: [
      "Use mecha_settled_notifications with action enable, disable, or status for worker settled notifications.",
      "Call pi__mecha_settled_notifications through the Cursor pi bridge.",
    ],
  },
  mecha_context: {
    promptSnippet: "Inspect context-threshold policy or request Pi self-compaction",
    promptGuidelines: [
      "Use mecha_context with action status, set_policy, or compact for context-threshold and compaction control.",
      "Call pi__mecha_context through the Cursor pi bridge.",
    ],
  },
});
const INLINE_ACTION_TOOL_NAMES = new Set(["bash", "edit", "write"]);
const INLINE_TOOL_POOL_CAPACITY = 5;
const INLINE_TOOL_REPLENISHMENT_INTERVAL_MS = 60_000;
const INLINE_TOOL_BLOCK_REASON = "Top-level inline write-ish tool calls are blocked by default. Subagents are mandatory for this work. Only when genuinely necessary, use mecha_request_inline_tool_allowance to request a temporary allowance; that is an exceptional path, not the normal workaround.";
// Keep the catalog bounded while allowing the configured Pi registry to reach
// the UI instead of silently dropping the models after the first page.

function normalizeInlineToolName(toolName: string): InlineToolName {
  if (typeof toolName !== "string" || toolName.trim().length === 0) throw new TypeError("toolName must be a non-empty string");
  const trimmedToolName = toolName.trim();
  const normalizedToolName = trimmedToolName.startsWith("functions.")
    ? trimmedToolName.slice("functions.".length)
    : trimmedToolName;
  if (normalizedToolName === "bash" || normalizedToolName === "edit" || normalizedToolName === "write") return normalizedToolName;
  throw new TypeError(`unsupported inline tool name: ${trimmedToolName}; expected bash, edit, or write`);
}

function validateInlineAllowanceCount(usageCount: number): void {
  if (!Number.isSafeInteger(usageCount) || usageCount < 1 || usageCount > 5) {
    throw new TypeError("usageCount must be an integer from 1 through 5");
  }
}

export function inlineToolBlockReason() {
  return INLINE_TOOL_BLOCK_REASON;
}

export function createInlineToolPolicy({ now = Date.now }: InlineAllowanceOptions = {}) {
  if (typeof now !== "function") throw new TypeError("now must be a function");

  const credits = new Map();
  let poolRemainingMs = INLINE_TOOL_POOL_CAPACITY * INLINE_TOOL_REPLENISHMENT_INTERVAL_MS;
  let poolUpdatedAt = now();

  function replenishPool() {
    const currentTime = now();
    const elapsedMs = Math.max(0, currentTime - poolUpdatedAt);
    poolRemainingMs = Math.min(
      INLINE_TOOL_POOL_CAPACITY * INLINE_TOOL_REPLENISHMENT_INTERVAL_MS,
      poolRemainingMs + elapsedMs,
    );
    poolUpdatedAt = currentTime;
  }

  function allowanceDetails({ toolName, usageCount, grantedCount, remaining, availableWholeCredits, remainingPool }: InlineAllowanceDetails): InlineAllowanceDetails {
    return {
      toolName,
      usageCount,
      grantedCount,
      remaining,
      availableWholeCredits,
      remainingPool,
    };
  }

  return {
    reset() {
      credits.clear();
      poolRemainingMs = INLINE_TOOL_POOL_CAPACITY * INLINE_TOOL_REPLENISHMENT_INTERVAL_MS;
      poolUpdatedAt = now();
    },
    grant(toolName: string, usageCount: number): InlineAllowanceDetails {
      const normalizedToolName = normalizeInlineToolName(toolName);
      validateInlineAllowanceCount(usageCount);
      replenishPool();
      const availableWholeCredits = Math.floor(poolRemainingMs / INLINE_TOOL_REPLENISHMENT_INTERVAL_MS);
      const remainingPool = poolRemainingMs / INLINE_TOOL_REPLENISHMENT_INTERVAL_MS;
      if (availableWholeCredits < usageCount) {
        const exhaustedText = availableWholeCredits === 0 && poolRemainingMs === 0 ? " The pool is exhausted." : "";

        const error = new Error(`Insufficient inline tool credits: requested ${usageCount} credit(s), but only ${availableWholeCredits} whole credit(s) are available (pool currently holds ${remainingPool} credit(s)).${exhaustedText} No credits were granted. Credits replenish at 1 credit per minute up to a maximum of 5 credits.
          ATTENTION: you are probably getting this error because you are ABUSING the inline allowance escape hatch tool. You need to be using subagents instead.

          Read that again: YOU SHOULD BE USING SUBAGENTS INSTEAD. Continue to ignore this mandatory policy and the inline tool allowance will be disabled.`);
        Object.assign(error, { details: allowanceDetails({
          toolName: normalizedToolName,
          usageCount,
          grantedCount: 0,
          remaining: credits.get(normalizedToolName) ?? 0,
          availableWholeCredits,
          remainingPool,
        }) });
        throw error;
      }

      const grantedCount = usageCount;
      poolRemainingMs -= grantedCount * INLINE_TOOL_REPLENISHMENT_INTERVAL_MS;
      const remaining = (credits.get(normalizedToolName) ?? 0) + grantedCount;
      credits.set(normalizedToolName, remaining);
      return allowanceDetails({
        toolName: normalizedToolName,
        usageCount,
        grantedCount,
        remaining,
        availableWholeCredits,
        remainingPool: poolRemainingMs / INLINE_TOOL_REPLENISHMENT_INTERVAL_MS,
      });
    },
    evaluate(toolName: string): { block: true; reason: string } | undefined {
      let normalizedToolName;
      try {
        normalizedToolName = normalizeInlineToolName(toolName);
      } catch {
        // Tools outside the inline write allowance are handled by their own policies.
        return undefined;
      }
      // Credits are per-tool and temporary: matching top-level calls spend them, and exhausted entries are removed. The parent session resets this state at session_start.
      const remaining = credits.get(normalizedToolName) ?? 0;
      if (remaining > 0) {
        if (remaining === 1) credits.delete(normalizedToolName);
        else credits.set(normalizedToolName, remaining - 1);
        return undefined;
      }
      return { block: true, reason: inlineToolBlockReason() };
    },
  };
}

export function boundedConversationText(content: JsonValue, maximum = 4096): string {
  const blocks = Array.isArray(content) ? content : [content];
  const text = blocks.map((block) => typeof block === "string" ? block : isRecord(block) && block.type === "text" && typeof block.text === "string" ? block.text : "").filter(Boolean).join("\n");
  return text.slice(-maximum);
}

function childSessionIds() {
  const existing = Object.getOwnPropertyDescriptor(globalThis, CHILD_SESSION_IDS)?.value;
  if (!(existing instanceof Set)) {
    const children = new Set<string>();
    Object.defineProperty(globalThis, CHILD_SESSION_IDS, { value: children, configurable: true, writable: true });
    return children;
  }
  return existing;
}

function childSessionId(event: JsonObject | undefined): string | undefined {
  return typeof event?.sessionId === "string" && event.sessionId.length > 0
    ? event.sessionId
    : undefined;
}

export function transientChildAgentId(sessionId: string, generateId: () => string = createNanoId): string {
  if (typeof sessionId !== "string" || sessionId.length === 0 || typeof generateId !== "function") {
    throw new TypeError("a child session ID and ID generator are required");
  }
  return `subagent-${generateId()}`;
}

export function resolveAgentId(
  environment: Environment = process.env,
  { queryWindowName = queryTmuxWindowName }: { queryWindowName?: (environment: Environment) => string } = {},
) {
  if (typeof environment.TMUX !== "string" || environment.TMUX.length === 0
    || typeof environment.TMUX_PANE !== "string" || !/^%\d+$/.test(environment.TMUX_PANE)) {
    return undefined;
  }
  const agentId = queryWindowName(environment);
  return assertAgentId(typeof agentId === "string" ? agentId.trim() : agentId);
}

export function queryTmuxWindowName(
  environment: Environment = process.env,
  { execFileSync: exec = execFileSync }: { execFileSync?: typeof execFileSync } = {},
) {
  const paneId = environment.TMUX_PANE;
  if (typeof paneId !== "string" || !/^%\d+$/.test(paneId)) {
    throw new Error("Mailbox v2 could not identify the current tmux pane");
  }
  try {
    const windowName = exec("tmux", ["display-message", "-p", "-t", paneId, "#{window_name}"], {
      encoding: "utf8",
      stdio: ["ignore", "pipe", "ignore"],
    });
    return windowName.trim();
  } catch {
    throw new Error("Mailbox v2 could not read the current tmux window");
  }
}

export {
  routeMailboxEnvelope,
  mailboxSessionDeliveryState,
} from "./delivery-coordinator.js";

export {
  spawnNativeSentinel,
  captureNativeSentinelSpawner,
  createPiMailboxDeliveryCoordinator,
} from "./delivery-coordinator.js";

export function createMechaMessagingExtension(pi: PiRuntime, {
  resolveCurrentAgentId = resolveAgentId,
  createClient = createMailboxClient,
  createCoordinator,
  createSettledScheduler = createSettledNotificationScheduler,
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
  setIntervalImpl = setInterval,
  clearIntervalImpl = clearInterval,
  nowImpl = Date.now,
  getSubagentsService = getOwnerSubagentsService,
  nativeSubagentTrace = createNativeSubagentTrace(),
  operationalTools = {},
}: ExtensionOptions = {}) {
  let client: MailboxClient | undefined;
  const configuredReminderLock = operationalTools.reminderLock;
  const reminderLock = configuredReminderLock ?? createReminderLockState({
    getCurrentReminder: operationalTools.getCurrentReminder ?? createRouterReminderReader({
      fetchImpl: operationalTools.fetchImpl ?? globalThis.fetch,
      routerUrl: operationalTools.environment?.MECHA_ROUTER_URL ?? process.env.MECHA_ROUTER_URL ?? "http://127.0.0.1:8765",
    }),
    getCurrentTarget: () => {
      let agentId;
      try { agentId = resolveCurrentAgentId(); } catch { agentId = undefined; }
      const instanceId = client?.instanceId;
      const leaseId = client?.currentLeaseId?.();
      return typeof agentId === "string" && typeof instanceId === "string" && typeof leaseId === "string"
        ? { agentId, instanceId, leaseId }
        : undefined;
    },
    now: nowImpl,
  });
  const operationalReminderLock: OperationalReminderLock = { guard: async (reminderId: string, reminder: Parameters<NonNullable<OperationalReminderLock["guard"]>>[1]) => reminderLock.guard(reminderId, isRecord(reminder) ? reminder : undefined), clear: (reminderId: string) => reminderLock.clear(reminderId) };
  registerOperationalTools(pi, { ...operationalTools, reminderLock: operationalReminderLock });
  if (nativeSubagentTrace === undefined || typeof nativeSubagentTrace.record !== "function") {
    throw new TypeError("nativeSubagentTrace must provide record");
  }
  const traceNativeSubagent = (event: string, fields: object = {}) => nativeSubagentTrace.record(event, fields);
  const appendDiagnostic = (event: string, payload: object) => traceNativeSubagent(event, payload);
  traceNativeSubagent("extension_initialized", {
    registry_available: (() => {
      const registry = Object.getOwnPropertyDescriptor(globalThis, PI_SUBAGENTS_SERVICE_REGISTRY)?.value;
      const services = isRecord(registry) ? registry.services : undefined;
      return isRecord(services) && typeof services.get === "function";
    })(),
  });
  const currentSubagentsServiceAccessor = getSubagentsService;
  let coordinator: DeliveryCoordinator | undefined;
  let parentAgentId: string | undefined;
  let parentActivityContext: PiContext | undefined;
  let modelControlProjection: ModelControlProjection | undefined;
  let goalProjection: GoalProjection | undefined;
  let goalProjectionReceived = false;
  let meaningfulActivityRevision = 0;
  const activityProducerEpoch = randomUUID();
  const meaningfulActivityIds = new Set();
  let goalControlState = defaultGoalControlState();
  const receiveGoalProjection = (value: JsonValue) => {
    const normalized = normalizeGoalProjection(value);
    if (normalized === undefined) return;
    goalProjection = normalized;
    goalProjectionReceived = true;
    if (parentAgentId !== undefined) emitWorkerIdentity(parentActivityContext, parentAgentId);
  };
  const receiveGoalControlState = (value: JsonValue) => {
    const normalized = normalizeGoalControlState(value);
    if (normalized === undefined) return;
    goalControlState = normalized;
    publishGoalState();
  };
  const emitWorkerIdentity = (context: PiContext | undefined, workerId: string) => {
    const sessionId = contextSessionId(context);
    if (typeof workerId !== "string" || workerId.length === 0 || typeof sessionId !== "string" || sessionId.length === 0) return;
    try {
      pi.events?.emit?.(GOAL_WORKER_ID_EVENT, { workerId, sessionId, producerEpoch: activityProducerEpoch });
    } catch {
      // The goal extension is optional; an unavailable local bridge must not block startup.
    }
  };
  const emitMeaningfulActivity = (kind: string, rawActivityId: string | undefined, context: PiContext | undefined) => {
    if (!goalProjectionReceived || goalProjection?.enabled !== true) return;
    const activityId = typeof rawActivityId === "string" ? rawActivityId.trim() : "";
    const sessionId = contextSessionId(context) ?? contextSessionId(parentActivityContext);
    if (activityId.length === 0 || typeof sessionId !== "string" || sessionId.length === 0
      || typeof parentAgentId !== "string" || parentAgentId.length === 0
      || meaningfulActivityIds.has(`${kind}:${activityId}`)) return;
    const key = `${kind}:${activityId}`;
    meaningfulActivityIds.add(key);
    while (meaningfulActivityIds.size > MAX_MEANINGFUL_ACTIVITY_IDS) {
      meaningfulActivityIds.delete(meaningfulActivityIds.keys().next().value);
    }
    const revision = ++meaningfulActivityRevision;
    try {
      pi.events?.emit?.(GOAL_ACTIVITY_EVENT, {
        activityId: key,
        kind,
        revision,
        sessionId,
        workerId: parentAgentId,
        producerEpoch: activityProducerEpoch,
        goal: { ...goalProjection },
      });
    } catch {
      // Progress observation is best-effort and cannot change the worker action outcome.
    }
  };
  const isConcreteGoalTool = (toolName: string | undefined) => {
    return typeof toolName === "string" && toolName.length > 0 && !GOAL_ACTIVITY_EXCLUDED_TOOLS.has(toolName);
  };
  const publishGoalState = (mailboxClient: MailboxClient | undefined = client) => {
    if (mailboxClient === undefined || typeof mailboxClient.sendGoalState !== "function") return false;
    try {
      mailboxClient.sendGoalState(cloneGoalControlState(goalControlState));
      return true;
    } catch {
      return false;
    }
  };
  const handleReminderLockControl = async (control: ExternalReminderControl) => reminderLock.control(control);
  const handleGoalControl = createGoalControlBridge<GoalControlState>({
    emit: (event, value) => { pi.events?.emit?.(event, value); },
    getContext: () => parentActivityContext,
    getState: () => goalControlState,
    normalizeState: (value) => normalizeGoalControlState(value),
    setState: (state) => { goalControlState = state; },
    publish: () => { publishGoalState(); },
    cloneState: cloneGoalControlState,
    setTimeoutImpl,
    clearTimeoutImpl,
  });
  pi.events?.on?.(GOAL_STATE_EVENT, receiveGoalProjection);
  pi.events?.on?.(GOAL_CONTROL_STATE_EVENT, receiveGoalControlState);
  try { pi.events?.emit?.(GOAL_STATE_REQUEST_EVENT); } catch { /* optional goal bridge is best-effort */ }
  let startupError: ErrorLike | undefined;
  let activeWork = ""; let recallAbort: AbortController | undefined; let recallProcess: ReturnType<typeof execFile> | undefined; let policyAbort: AbortController | undefined; let policyReady: Promise<JsonObject> | undefined; let capture: MemoryCapture | undefined;
  const ensureCapture = async (signal: AbortSignal | undefined): Promise<MemoryCapture> => { if (capture !== undefined) return capture; const response = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/policy`, { signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(5_000)]) : AbortSignal.timeout(5_000) }); if (!response.ok) throw new Error("capture policy unavailable"); const { recall: policy } = await response.json(); capture = createMemoryCaptureLifecycle({ policy, context: async ({ signal }: CaptureContextInput) => { const lookup = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/lookup`, { method: "POST", headers: { "content-type": "application/json" }, signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(policy.capture.router_timeout_ms)]) : AbortSignal.timeout(policy.capture.router_timeout_ms), body: JSON.stringify({ stores: ["default"] }) }); if (!lookup.ok) throw new Error("capture destination unavailable"); const { records } = await lookup.json(); return records; }, apply: async (body: CaptureApplyInput, signal: AbortSignal) => { const applied = await fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/apply`, { method: "POST", headers: { "content-type": "application/json" }, signal: signal ? AbortSignal.any([signal, AbortSignal.timeout(policy.capture.router_timeout_ms)]) : AbortSignal.timeout(policy.capture.router_timeout_ms), body: JSON.stringify(body) }); if (!applied.ok) throw new Error("capture apply failed"); return applied.json(); }, diagnostics: appendMemoryCaptureDiagnostic }); return capture; };
  const ensureRecallPolicy = async () => { if (policyReady !== undefined) return policyReady; policyAbort = new AbortController(); const timeout = setTimeout(() => policyAbort?.abort(), 5_000); policyReady = fetch(`${routerHttpUrl(process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL)}/v1/memory/policy`, { signal: policyAbort.signal }).then((response) => { if (!response.ok) throw new Error("recall policy unavailable"); return response.json(); }).then((payload) => { const policy = isRecord(payload.recall) ? payload.recall : {}; recallController.configure(policy); return payload; }).finally(() => clearTimeout(timeout)).catch((error) => { policyReady = undefined; throw error; }); return policyReady; };
  let recall: ReturnType<typeof createRecallState>; let recallConfigurationError: ErrorLike | undefined;
  try { recall = createRecallState(); } catch (error) { recallConfigurationError = error instanceof Error ? error : new Error(String(error)); recall = createRecallState({}); }
  let recallPipeline: ReturnType<typeof createPipeline> | undefined;
  const reportedOrphanToolResults = new Set();
  const reportedMalformedToolCalls = new Set();

  const recallController = createRecallController({
    cancel: () => { policyAbort?.abort(); recallAbort?.abort(); recallProcess?.kill(); },
    evaluate: async (active: () => boolean) => {
      const status = recall.status();
      if (!status.enabled || !active()) return { ids: [] };
      const pipeline = createPipeline({ routerUrl: process.env.MECHA_ROUTER_URL ?? DEFAULT_ROUTER_URL, stores: status.stores, workSnapshot: activeWork, evaluate: evaluateRecall, validateChoice, onPolicy: (policy) => recallController.configure({ inactivity_seconds: typeof policy.inactivity_seconds === "number" ? policy.inactivity_seconds : undefined, maximum_window_seconds: typeof policy.maximum_window_seconds === "number" ? policy.maximum_window_seconds : undefined }), registerAbort: (controller) => { recallAbort = controller; }, registerProcess: (child) => { recallProcess = child; }, send: pi.sendMessage.bind(pi) });
      recallPipeline = pipeline;
      return pipeline.evaluate(active);
    },
    recall: (choice, active) => {
      if (!isRecallChoice(choice) || recallPipeline === undefined) return;
      return recallPipeline.recall(choice, active);
    },
  });
  let settledNotificationAgentId: string | undefined;
  let settledNotificationPending = false;
  let settledNotificationRecipient: string | undefined;
  let settledNotificationEnabled = false;
  let initialCompactionPolicy: CompactionPolicy;
  let initialCompactionPolicyError: ErrorLike | undefined;
  try { initialCompactionPolicy = compactionPolicyFromEnvironment(); } catch (error) { initialCompactionPolicyError = error instanceof Error ? error : new Error(String(error)); initialCompactionPolicy = resolveCompactionPolicy(DEFAULT_COMPACTION_POLICY); }
  let compaction: CompactionOrchestration | undefined;

  function rememberParentActivityContext(context: PiContext | undefined) {
    if (context !== undefined) {
      parentActivityContext = context;
      modelControlProjection?.onContext(context);
    }
  }

  function isPiContext(context: ActivityContext | undefined): context is PiContext {
    const sessionManager = context?.sessionManager;
    const ui = context?.ui;
    return context !== undefined
      && isRecord(sessionManager)
      && typeof sessionManager.getBranch === "function"
      && typeof sessionManager.isPersisted === "function"
      && typeof sessionManager.getSessionFile === "function"
      && isRecord(ui)
      && typeof ui.setStatus === "function";
  }

  function contextSessionId(context: PiContext | undefined): string | undefined {
    return context?.sessionManager?.getSessionId?.();
  }

  function createMailboxCoordinator(context: PiContext, mailboxClient: MailboxClient) {
    if (createCoordinator !== undefined) return createCoordinator(context, mailboxClient);
    return createPiMailboxDeliveryCoordinator({
      pi,
      sessionContext: context,
      client: mailboxClient,
      spawnSubagent: captureNativeSentinelSpawner(async () => activityLifecycle.getNativeService(context.sessionManager.getSessionId())),
    });
  }

  try {
    settledNotificationRecipient = assertAgentId(process.env.MECHA_PI_NOTIFY_ON_SETTLED);
  } catch {
    // The launcher rejects invalid values; a manually configured Pi stays disabled.
  }
  const children = childSessionIds();
  const compactionState = (): CompactionOrchestration => {
    if (compaction === undefined) throw new Error("compaction orchestration is not initialized");
    return compaction;
  };
  const activityLifecycle: ActivityLifecycle = createActivityLifecycle({
    getClient: () => client,
    getParentContext: () => parentActivityContext,
    isCompacting: () => compaction?.isMailboxPaused() ?? false,
    getGoalProjection: () => goalProjectionReceived ? goalProjection : undefined,
    getCurrentModel: () => modelControlProjection?.current() ?? undefined,
    getNativeService: (ownerSessionId) => currentSubagentsServiceAccessor?.(ownerSessionId),
    notify: (message, level) => { parentActivityContext?.ui?.notify?.(message, level); },
    trace: traceNativeSubagent,
    createSettledScheduler,
    rememberParentContext: (context) => {
      if (context !== undefined && isPiContext(context)) rememberParentActivityContext(context);
    },
    setTimeoutImpl,
    clearTimeoutImpl,
    setIntervalImpl,
    clearIntervalImpl,
    nowImpl,
  });
  modelControlProjection = createModelControlProjection({
    getParentContext: () => parentActivityContext,
    setModel: (model) => pi.setModel?.call(pi, model) ?? false,
    isModelSwitchAvailable: () => typeof pi.setModel === "function",
    refresh: (context) => activityLifecycle.refresh(context),
    trace: traceNativeSubagent,
    getCurrentModel: (context) => context?.model,
  });
  compaction = createCompactionOrchestration({
    getClient: () => client === undefined ? undefined : {},
    isChildSession: (sessionId) => children.has(sessionId),
    rememberParentContext: (context) => { if (isPiContext(context)) rememberParentActivityContext(context); },
    activity: {
      onCompactionStarted: (context) => activityLifecycle.onCompactionStarted(context),
      onCompactionResumed: (context) => activityLifecycle.onCompactionResumed(context),
    },
    setMailboxStatus: (context, value) => context.ui?.setStatus?.("mecha-mailbox", `mailbox: ${value}`),
    sendMessage: (message, messageOptions) => pi.sendMessage(message, messageOptions),
    ensureCapture,
    getEnvironment: () => process.env,
    initialPolicy: initialCompactionPolicy,
    initialPolicyError: initialCompactionPolicyError,
    errorMessage,
    setTimeoutImpl,
  });
  const settledNotifications = activityLifecycle.settledNotifications;
  const childReplyClients = new Map<string, MailboxClient | undefined>();
  const childReplyClientAgentIds = new Map<string, string>();
  const inlineToolPolicy = createInlineToolPolicy({ now: nowImpl });
  const mailboxSendRateLimits = new Map<string, RateReservation[]>();

  function reserveMailboxSend(sender: string | undefined, recipient: string): ReminderReservation {
    const now = nowImpl();
    const key = `${sender ?? "any"}\u0000${recipient}`;
    const recent = (mailboxSendRateLimits.get(key) ?? []).filter(
      (reservation) => now - reservation.timestamp < MAILBOX_SEND_RATE_LIMIT_WINDOW_MS,
    );
    if (recent.length === 0) mailboxSendRateLimits.delete(key);
    else mailboxSendRateLimits.set(key, recent);
    if (recent.length >= MAILBOX_SEND_RATE_LIMIT_COUNT) {
      throw new Error("Temporary rate limit reached for this recipient. Stop sending and retry after 60 seconds, after the reset.");
    }
    const reservation = { timestamp: now };
    recent.push(reservation);
    mailboxSendRateLimits.set(key, recent);
    return { key, reservation };
  }

  function rollbackMailboxSend({ key, reservation }: ReminderReservation): void {
    const reservations = mailboxSendRateLimits.get(key);
    if (reservations === undefined) return;
    const index = reservations.indexOf(reservation);
    if (index >= 0) reservations.splice(index, 1);
    if (reservations.length === 0) mailboxSendRateLimits.delete(key);
  }

  function mailboxSenderForSession(context: PiContext) {
    const sessionId = context?.sessionManager?.getSessionId?.();
    return childReplyClientAgentIds.get(sessionId) ?? parentAgentId;
  }

  function mailboxClientForSession(context: PiContext): MailboxClient | undefined {
    const sessionId = context?.sessionManager?.getSessionId?.();
    // Keep a disposed child identifiable until its shutdown cleanup; otherwise a late child send would use the parent client.
    return children.has(sessionId) || childReplyClients.has(sessionId)
      ? childReplyClients.get(sessionId)
      : client;
  }

  const handleModelControl = (control: { controlId: string; sourceAgentId: string; action: string; model: ModelInput | undefined }) => {
    if (modelControlProjection === undefined) throw new Error("model control is not initialized");
    return modelControlProjection.handleControl(control);
  };

  pi.registerTool({
    name: "mecha_parent_message",
    label: "Message parent",
    description: "Child-only: send an ordinary Mailbox message to this native subagent's target parent.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_parent_message,
    parameters: {
      type: "object",
      additionalProperties: false,
      required: ["message"],
      properties: { message: { type: "string", minLength: 1 } },
    },
    async execute(_toolCallId: string, { message }: ParentMessageInput, _signal: AbortSignal | undefined, _onUpdate: (() => void) | undefined, toolContext: PiContext) {
      const childClient = childReplyClients.get(toolContext.sessionManager.getSessionId());
      const parentAgentId = resolveCurrentAgentId();
      if (childClient === undefined || parentAgentId === undefined || typeof message !== "string" || message.length === 0) {
        return { content: [{ type: "text", text: "Parent message is unavailable in this session." }], isError: true };
      }
      try {
        const sent = await childClient.sendReply({ to: parentAgentId, body: message, subject: "Pi subagent message" });
        return { content: [{ type: "text", text: "Parent message accepted." }], details: { message_id: sent.id } };
      } catch {
        return { content: [{ type: "text", text: "Parent message was not accepted." }], isError: true };
      }
    },
  });

  pi.registerTool({
    name: "mecha_mailbox_send",
    label: "Send Mailbox message",
    description: "Send an ordinary Mailbox V2 message to another local agent.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_mailbox_send,
    parameters: {
      type: "object",
      additionalProperties: false,
      required: ["recipient", "body"],
      properties: {
        recipient: { type: "string", minLength: 1 },
        body: { type: "string", minLength: 1 },
        subject: { type: "string" },
      },
    },
    async execute(_toolCallId: string, input: MailboxSendInput, _signal: AbortSignal | undefined, _onUpdate: (() => void) | undefined, toolContext: PiContext) {
      try {
        if (input !== null && typeof input === "object" && "from" in input) {
          throw new Error("the mailbox sender is derived from the current tmux window");
        }
        const recipient = input?.recipient;
        const body = input?.body;
        const subject = input?.subject;
        if (typeof recipient !== "string" || recipient.length === 0) throw new Error("a mailbox recipient is required");
        assertAgentId(recipient);
        if (typeof body !== "string" || body.length === 0) throw new Error("a message body is required");
        if (subject !== undefined && typeof subject !== "string") throw new Error("a mailbox subject must be a string");

        const sessionId = toolContext?.sessionManager?.getSessionId?.();
        const mailboxClient = mailboxClientForSession(toolContext);
        // Native children must use only their initialized client; falling back to the parent would send under the wrong identity.
        if (mailboxClient === undefined) {
          if (children.has(sessionId) || childReplyClients.has(sessionId)) throw new Error("Mailbox v2 is not initialized for this native child session");
          throw startupError ?? new Error("Mailbox v2 is not initialized");
        }
        const reservation = reserveMailboxSend(mailboxSenderForSession(toolContext), recipient);
        let sent;
        try {
          sent = await mailboxClient.sendReply({ to: recipient, body, ...(subject === undefined ? {} : { subject }) });
        } catch (error) {
          rollbackMailboxSend(reservation);
          throw error;
        }
        return {
          content: [{ type: "text", text: `Mailbox message accepted (${sent.id}, ${sent.state}).` }],
          details: { message_id: sent.id, state: sent.state },
        };
      } catch (error) {
        return { content: [{ type: "text", text: errorMessage(error) }], isError: true };
      }
    },
  });

  pi.registerTool({
    name: INLINE_TOOL_ALLOWANCE_TOOL_NAME,
    label: "Request inline tool allowance",
    description: "Request a temporary exception for genuinely necessary inline use of a named tool. This is an exception to the rule; subagents remain mandatory by default.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA[INLINE_TOOL_ALLOWANCE_TOOL_NAME],
    parameters: {
      type: "object",
      additionalProperties: false,
      required: ["toolName", "usageCount"],
      properties: {
        toolName: { type: "string", minLength: 1, maxLength: 64, description: "Canonical tool name (bash, edit, or write), or its functions.* alias." },
        usageCount: { type: "integer", minimum: 1, maximum: 5, description: "Number of temporary uses to grant, from 1 through 5." },
      },
    },
    async execute(_toolCallId: string, input: InlineAllowanceInput, _signal: AbortSignal | undefined, _onUpdate: (() => void) | undefined, toolContext: PiContext) {
      try {
        // Children already bypass the guard; keeping their requests out of this state prevents a child from granting parent-session credits.
        if (children.has(toolContext?.sessionManager?.getSessionId?.())) {
          return { content: [{ type: "text", text: "Native subagents are exempt from the inline tool guard; no parent allowance was changed." }] };
        }
        if (typeof input.toolName !== "string" || typeof input.usageCount !== "number") throw new Error("toolName and usageCount are required");
        const grant = inlineToolPolicy.grant(input.toolName, input.usageCount);
        const text = `Granted ${grant.grantedCount} temporary inline use(s) for ${grant.toolName}. This allowance is an exception to the rule; subagents remain mandatory by default.`;
        return { content: [{ type: "text", text }], details: grant };
      } catch (error) {
        return { content: [{ type: "text", text: errorMessage(error) }], details: isRecord(error) ? error.details ?? {} : {}, isError: true };
      }
    },
  });

  if (typeof pi.registerTool === "function") pi.registerTool({
    name: "mecha_recall",
    label: "Mecha Recall",
    description: "Enable, disable, or inspect process-local Markdown recall.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_recall,
    parameters: { type: "object", additionalProperties: false, required: ["action"], properties: {
      action: { type: "string", enum: ["enable", "disable", "status"] },
      stores: { type: "array", items: { type: "string" } },
    } },
    async execute(_toolCallId: string, input: RecallToolInput) {
      try {
        if (recallConfigurationError !== undefined) throw recallConfigurationError;
        if (input.action === "enable") { recallController.stop(); recall.enable(input.stores); }
        else if (input.action === "disable") { recall.disable(); recallController.stop(); }
        else if (input.action !== "status") throw new Error("unsupported mecha_recall action");
        const status = recall.status();
        return { content: [{ type: "text", text: JSON.stringify(status) }], details: status };
      } catch (error) {
        return { content: [{ type: "text", text: errorMessage(error) }], details: {}, isError: true };
      }
    },
  });

  pi.events.on("subagents:started", (event: NativeSubagent) => {
    // A launch is a concrete worker action. This signal is intentionally emitted from the
    // launch event itself, never inferred from lifecycle polling or liveness refreshes.
    emitMeaningfulActivity("native-subagent-launch", event?.id, parentActivityContext);
    activityLifecycle.onNativeStarted(event);
  });
  // Pi versions expose different terminal event names. Delegate every alias to the
  // lifecycle owner so legacy removal and typed polling retention stay consistent.
  for (const eventName of [
    "subagents:completed", "subagents:failed", "subagents:canceled", "subagents:cancelled",
    "subagents:aborted", "subagents:terminated", "subagents:stopped", "subagents:steered",
  ]) {
    pi.events.on(eventName, (event: NativeSubagent) => activityLifecycle.onNativeTerminal(event));
  }

  function settledNotificationStatus() {
    return {
      configured: settledNotificationRecipient !== undefined,
      enabled: settledNotificationEnabled,
      recipient: settledNotificationRecipient,
      pending: settledNotificationPending,
      cooldownMs: 120_000,
    };
  }

  if (typeof pi.registerTool === "function") pi.registerTool({
    name: "mecha_settled_notifications",
    label: "Mecha Settled Notifications",
    description: "Enable, disable, or inspect this Pi process's managed-worker settled mailbox notifications.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_settled_notifications,
    parameters: { type: "object", additionalProperties: false, required: ["action"], properties: {
      action: { type: "string", enum: ["enable", "disable", "status"] },
    } },
    async execute(_toolCallId: string, input: SettledToolInput) {
      try {
        if (input.action === "disable") {
          settledNotificationEnabled = false;
          settledNotificationPending = false;
          settledNotifications.dispose();
        } else if (input.action === "enable") {
          if (settledNotificationRecipient === undefined) throw new Error("Settled notifications are not configured for this process.");
          settledNotificationEnabled = true;
        } else if (input.action !== "status") {
          throw new Error("unsupported mecha_settled_notifications action");
        }
        const status = settledNotificationStatus();
        return { content: [{ type: "text", text: JSON.stringify(status) }], details: status };
      } catch (error) {
        return { content: [{ type: "text", text: errorMessage(error) }], details: settledNotificationStatus(), isError: true };
      }
    },
  });

  if (typeof pi.registerTool === "function") pi.registerTool({
    name: "mecha_context",
    label: "Mecha Context",
    description: "Inspect or configure Pi context-threshold notifications, or trigger self-compaction. Use action=compact only after a threshold notification or direct user instruction. postCompactionNotification is optional: include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed. A default handoff note can be configured in the compaction policy, and a per-compact call can override it.",
    ...MECHA_MESSAGING_CURSOR_TOOL_METADATA.mecha_context,
    parameters: { type: "object", additionalProperties: false, required: ["action"], properties: {
      action: { type: "string", enum: ["status", "set_policy", "compact"], description: "status inspects usage/policy, set_policy changes thresholds/default handoff note, compact requests self-compaction." },
      policy: { oneOf: [
        { type: "object", additionalProperties: false, required: ["levels"], properties: {
          postCompactionNotification: { type: "string", description: "Optional default handoff note injected after successful compaction. Include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed." },
          models: { type: "object" },
          levels: { type: "array", minItems: 1, items: { oneOf: [
            { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 },
            { type: "object", additionalProperties: false, required: ["percent"], properties: {
              percent: { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 },
              guidance: { type: "string", minLength: 1 },
              mandatory: { type: "boolean" },
            } },
          ] } },
        } },
        { type: "object", additionalProperties: false, required: ["tiers"], properties: {
          postCompactionNotification: { type: "string", description: "Optional default handoff note injected after successful compaction. Include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed." },
          models: { type: "object" },
          tiers: { type: "array", minItems: 1, items: {
            type: "object", additionalProperties: false, required: ["levels"], properties: {
              maxContextTokens: { oneOf: [{ type: "integer", minimum: 1 }, { type: "null" }] },
              levels: { type: "array", minItems: 1, items: { oneOf: [
                { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 },
                { type: "object", additionalProperties: false, required: ["percent"], properties: {
                  percent: { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 },
                  guidance: { type: "string", minLength: 1 },
                  mandatory: { type: "boolean" },
                } },
              ] } },
            },
          } },
        } },
        { type: "array", minItems: 1, items: { type: "number", exclusiveMinimum: 0, exclusiveMaximum: 100 } },
      ] },
      customInstructions: { type: "string", description: "Optional instructions for Pi's compaction summary." },
      postCompactionNotification: { type: "string", description: "Optional per-compact handoff note. Include it while you are in the middle of an active task, review, or decision loop; omit it only when clearly idle/done and no details are needed. Overrides any default handoff note configured in the compaction policy for this compact call." },
    } },
    async execute(_toolCallId: string, input: ContextToolInput, _signal: AbortSignal | undefined, _onUpdate: (() => void) | undefined, toolContext: PiContext) {
      try {
        if (input.action === "set_policy") {
          const status = compactionState().setPolicy(input.policy, toolContext);
          return { content: [{ type: "text", text: JSON.stringify(status) }], details: status };
        }
        if (input.action === "compact") {
          const request = compactionState().requestSelfCompaction(input, toolContext);
          return { content: [{ type: "text", text: "Self-compaction deferred until Pi reaches the idle settled boundary." }], details: { ...compactionState().status(toolContext), compactState: "deferred_until_agent_settled", postCompactionNotification: request.postCompactionNotification } };
        }
        if (input.action !== "status") throw new Error("unsupported mecha_context action");
        const status = compactionState().status(toolContext);
        return { content: [{ type: "text", text: JSON.stringify(status) }], details: status };
      } catch (error) {
        return { content: [{ type: "text", text: errorMessage(error) }], details: {}, isError: true };
      }
    },
  });

  pi.events.on("subagents:child:session-created", (event: NativeSubagent) => {
    const sessionId = childSessionId(event);
    if (sessionId !== undefined) {
      children.add(sessionId);
      traceNativeSubagent("extension_child_session_created", { session_id: sessionId });
    }
  });
  pi.events.on("subagents:child:disposed", (event: NativeSubagent) => {
    const sessionId = childSessionId(event);
    if (sessionId !== undefined) {
      children.delete(sessionId);
      traceNativeSubagent("extension_child_session_disposed", { session_id: sessionId });
      childReplyClients.delete(sessionId);
      childReplyClientAgentIds.delete(sessionId);
      // The tombstone keeps late child tool calls from falling back to the parent client.
      childReplyClients.set(sessionId, undefined);
    }
  });

  pi.on("tool_call", (_event: PiEvent, context: PiContext) => {
    // Suppress only native subagent children recorded by lifecycle events. Do not use the generic
    // parentSession header here: ordinary /fork and /clone sessions carry it but are still top-level agents.
    if (children.has(context?.sessionManager?.getSessionId?.())) return undefined;
    if (compactionState().hasPendingHardCompaction()) {
      return {
        block: true,
        reason: "Pi context compaction is pending; tool work resumes after it settles.",
        terminate: true,
      };
    }
    return typeof _event.toolName === "string" ? inlineToolPolicy.evaluate(_event.toolName) : undefined;
  });
  // These completion hooks are the local action boundary. Count only successful, uniquely
  // identified real tools; intent, output text, failures, and control-plane completions are not progress.
  const recordToolCompletion = (_event: PiEvent, context: PiContext) => {
    if (children.has(context?.sessionManager?.getSessionId?.())) return;
    const result = _event?.result ?? _event;
    const explicitlySuccessful = _event?.isError === false || result?.isError === false;
    if (!explicitlySuccessful
      || _event?.cancelled === true || _event?.canceled === true || _event?.aborted === true
      || result?.cancelled === true || result?.canceled === true || result?.aborted === true
      || _event?.details?.cancelled === true || _event?.details?.canceled === true
      || result?.details?.cancelled === true || result?.details?.canceled === true
      || _event?.error !== undefined || result?.error !== undefined) return;
    const toolName = _event?.toolName ?? result?.toolName;
    if (!isConcreteGoalTool(toolName)) return;
    const toolCallId = _event?.toolCallId ?? _event?.tool_call_id ?? _event?.id ?? result?.toolCallId;
    if (typeof toolCallId === "string") emitMeaningfulActivity("tool-completed", toolCallId, context);
  };
  pi.on("tool_result", recordToolCompletion);
  pi.on("tool_execution_end", recordToolCompletion);

  pi.on("session_before_compact", async (event: PiEvent, context: PiContext) => {
    if (children.has(context.sessionManager.getSessionId())) return undefined;
    await compactionState().onSessionBeforeCompact(event, context);
    return undefined;
  });
  pi.on("context", (event: PiEvent, context: PiContext) => {
    if (!children.has(context.sessionManager.getSessionId())) compactionState().enforceHardCompaction(context);

    // The adapter keeps Pi's malformed-filtered event list separate from the provider list.
    const { messages, messagesBeforeOrphanQuarantine, malformed, orphaned } = sanitizeContextHistory(event);
    if (malformed.length > 0) {
      const sessionId = context.sessionManager.getSessionId();
      const fresh = malformed.filter(({ toolCallId }) => {
        const key = `${sessionId}:${toolCallId}`;
        if (reportedMalformedToolCalls.has(key)) return false;
        reportedMalformedToolCalls.add(key);
        return true;
      });
      if (fresh.length > 0) {
        let agentId;
        try { agentId = resolveCurrentAgentId(); } catch {}
        appendMalformedToolCallDiagnostic({
          sessionId,
          sessionFile: typeof context.sessionManager.getSessionFile === "function" ? context.sessionManager.getSessionFile() : undefined,
          model: context.model,
          agentId,
          totalMessages: Array.isArray(event.messages) ? event.messages.length : undefined,
          malformedCount: fresh.length,
          malformed: fresh,
        }, appendDiagnostic);
        const names = fresh.map(({ toolName }) => typeof toolName === "string" ? toolName.slice(0, 50) : String(toolName)).join(", ");
        context.ui?.notify(`Quarantined malformed tool call(s): ${names}`, "error");
      }
    }
    event.messages = messagesBeforeOrphanQuarantine;
    // Preserve the baseline's post-assignment event read while the adapter owns both sanitation passes.
    void (event.messages ?? []);

    // Orphan diagnostics follow the event assignment, while the returned provider list is final.
    if (orphaned.length === 0) return undefined;
    const sessionId = context.sessionManager.getSessionId();
    const fresh = orphaned.filter(({ toolCallId }) => {
      const key = `${sessionId}:${toolCallId}`;
      if (reportedOrphanToolResults.has(key)) return false;
      reportedOrphanToolResults.add(key);
      return true;
    });
    if (fresh.length > 0) {
      let agentId;
      try { agentId = resolveCurrentAgentId(); } catch {}
      appendOrphanToolResultDiagnostic({
        sessionId,
        sessionFile: typeof context.sessionManager.getSessionFile === "function" ? context.sessionManager.getSessionFile() : undefined,
        model: context.model,
        agentId,
        totalMessages: Array.isArray(event.messages) ? event.messages.length : undefined,
        quarantinedCount: fresh.length,
        orphans: fresh,
      });
      const callIds = fresh.map(({ toolCallId }) => toolCallId).join(", ");
      context.ui?.notify(`Quarantined orphan tool result(s) before provider request: ${callIds}`, "error");
    }
    return { messages };
  });
  pi.on("message_end", (event: PiEvent, context: PiContext) => { if (!children.has(context.sessionManager.getSessionId())) activeWork = boundedConversationText(event.message?.content, 4096); coordinator?.onMessageEnd(event, context); });
  pi.on("message_start", async (_event: PiEvent, context: PiContext) => { if (children.has(context.sessionManager.getSessionId()) || !recall.status().enabled) return; recallController.activity(); try { await ensureRecallPolicy(); } catch { /* Fail closed: the scheduled evaluation will not receive policy. */ } });
  pi.on("agent_start", (_event: PiEvent, context: PiContext) => {
    if (children.has(context.sessionManager.getSessionId())) return;
    activityLifecycle.onAgentStart(context);
    if (settledNotificationEnabled && settledNotificationRecipient !== undefined) {
      settledNotificationPending = true;
      settledNotifications.onParentActivity();
    }
  });
  pi.on("agent_settled", async (_event: PiEvent, context: PiContext) => { if (children.has(context.sessionManager.getSessionId()) || !recall.status().enabled) return; recallController.activity(); try { await ensureRecallPolicy(); } catch { /* Fail closed: the scheduled evaluation will not receive policy. */ } });
  pi.on("agent_settled", (_event: PiEvent, context: PiContext) => { compactionState().monitorContextThreshold(context); });
  pi.on("session_compact", (_event: PiEvent, context: PiContext) => { if (!children.has(context.sessionManager.getSessionId())) compactionState().onSessionCompact(context); });
  pi.on("agent_settled", (_event: PiEvent, context: PiContext) => {
    if (!children.has(context.sessionManager.getSessionId())) activityLifecycle.onAgentSettled(context);
    if (compactionState().hasPendingSelfCompaction()) settledNotificationPending = false;
    compactionState().runPendingSelfCompaction(context);
    if (children.has(context.sessionManager.getSessionId()) || !settledNotificationEnabled || !settledNotificationPending) return;

    // Consume before sendReply starts so a later run can arm independently.
    settledNotificationPending = false;
    settledNotifications.onParentSettled(() => {
      const notificationClient = client;
      const worker = settledNotificationAgentId;
      if (notificationClient === undefined || worker === undefined || settledNotificationRecipient === undefined) return;
      try {
        Promise.resolve(notificationClient.sendReply({
          to: settledNotificationRecipient,
          subject: "Pi worker settled",
          body: `Worker ${worker} has settled and is idle.`,
        })).catch((error: ErrorLike) => {
          context.ui.notify(`Pi settled notification failed: ${errorMessage(error)}`, "error");
        });
      } catch (error) {
        context.ui.notify(`Pi settled notification failed: ${errorMessage(error)}`, "error");
      }
    });
  });
  pi.on("session_shutdown", async (_event: PiEvent, context: PiContext) => {
    // A disposed child stays in childReplyClients as a tombstone until this cleanup; do not run parent capture cleanup with its context.
    if (children.has(context.sessionManager.getSessionId()) || childReplyClients.has(context.sessionManager.getSessionId())) return;
    mailboxSendRateLimits.clear();
    recallController.stop(); const controller = new AbortController(); const deadlineAt = Date.now() + 10_000; const timer = setTimeout(() => controller.abort(), 10_000); try { const branch = context.sessionManager.getBranch(); const boundary = branch.map(({ type }) => type).lastIndexOf("compaction"); await (await ensureCapture(controller.signal)).shutdown(branch.slice(boundary + 1), { deadlineAt, signal: controller.signal }); } catch {} finally { controller.abort(); clearTimeout(timer); }
  });

  pi.on("session_start", (_event: PiEvent, context: PiContext) => {
    const sessionId = context.sessionManager.getSessionId();
    if (children.has(sessionId)) {
      try {
        const parentAgentId = resolveCurrentAgentId();
        if (parentAgentId === undefined) return;
        const childAgentId = transientChildAgentId(sessionId);
        childReplyClients.set(sessionId, createClient({
          agentId: childAgentId,
          onEnvelope: () => {},
        }));
        childReplyClientAgentIds.set(sessionId, childAgentId);
      } catch {
        // A child without ordinary Mailbox send capability can still finish natively.
      }
      return;
    }
    mailboxSendRateLimits.clear();
    reminderLock.clear();
    modelControlProjection?.reset();
    // Keep allowance state across agent runs; a parent session owns one pool for its lifetime.
    inlineToolPolicy.reset();
    mailboxSendRateLimits.clear();
    try {
      const agentId = resolveCurrentAgentId();
      if (agentId === undefined) return;
      parentAgentId = agentId;
      emitWorkerIdentity(context, agentId);
      pi.sendMessage({
        customType: "mecha-worker-identity",
        content: `Your managed worker identity is ${agentId}.`,
        display: false,
        details: { agent_id: agentId },
      }, { deliverAs: "followUp", triggerTurn: false });
      rememberParentActivityContext(context);
      settledNotificationPending = false;
      settledNotificationAgentId = agentId;
      pi.registerCommand("mecha-parent-message", {
        description: "Child-only ordinary Mailbox message to its target parent: /mecha-parent-message <message>",
        handler: async (argumentsText: string, commandContext: PiContext) => {
          try {
            const childClient = childReplyClients.get(commandContext.sessionManager.getSessionId());
            const parentAgentId = resolveCurrentAgentId();
            const body = argumentsText.trim();
            if (childClient === undefined || parentAgentId === undefined || body.length === 0) {
              throw new Error("This command is available only to a child with a parent message");
            }
            const message = await childClient.sendReply({ to: parentAgentId, body, subject: "Pi subagent message" });
            commandContext.ui.notify(`Mailbox v2 child message accepted (${message.id})`, "info");
          } catch (error) {
            commandContext.ui.notify(errorMessage(error), "error");
          }
        },
      });
      pi.registerCommand("mecha-reply", {
        description: "Send a Mailbox v2 reply: /mecha-reply <recipient> <message>",
        handler: async (argumentsText: string, commandContext: PiContext) => {
          try {
            if (startupError !== undefined) throw startupError;
            if (client === undefined) throw new Error("Mailbox v2 is not initialized");
            const { recipient, body } = parseReplyArguments(argumentsText);
            const message = await client.sendReply({ to: recipient, body });
            commandContext.ui.notify(`Mailbox v2 reply accepted (${message.id})`, "info");
          } catch (error) {
            commandContext.ui.notify(errorMessage(error), "error");
          }
        },
      });
      let mailboxClient: MailboxClient;
      mailboxClient = createClient({
        agentId,
        parentSessionId: sessionId,
        onEnvelope: (message, delivery) => { const normalizedDelivery = normalizeMailboxDelivery(delivery); if (!compactionState().isMailboxPaused() && isMailboxMessage(message) && normalizedDelivery !== undefined) coordinator?.onEnvelope(message, normalizedDelivery); },
        onModelControl: handleModelControl,
        onModelControlTrace: (value) => { if (isRecord(value) && typeof value.event === "string") traceNativeSubagent(value.event, value); },
        onGoalControl: handleGoalControl,
        onGoalControlTrace: (value) => { if (isRecord(value) && typeof value.event === "string") traceNativeSubagent(value.event, value); },
        onReminderLockControl: handleReminderLockControl,
        onReminderLockReset: () => reminderLock.clear(),
        getGoalState: () => cloneGoalControlState(goalControlState),
        getModelCatalog: () => {
          try {
            if (modelControlProjection === undefined) throw new Error("model control is not initialized");
            return modelControlProjection.catalog();
          } catch (error) {
            traceNativeSubagent("extension_model_catalog_read_failed", {
              error_code: modelControlErrorCode(error),
              error_name: errorName(error),
            });
            throw error;
          }
        },
        getCurrentModel: () => modelControlProjection?.current() ?? null,
        onLifecycleReady: (value) => { if (isRecord(value)) traceNativeSubagent("extension_client_lifecycle_ready", { router_version: value.routerVersion, reconnect: value.reconnect }); },
        onLifecycleTrace: (value) => { if (isRecord(value) && typeof value.event === "string") traceNativeSubagent(value.event, value); },
        kickstart: async () => {
          if (typeof process.getuid !== "function") return;
          const result = await pi.exec(
            "launchctl",
            ["kickstart", `gui/${process.getuid()}/${process.env.MECHA_ROUTER_LAUNCHD_LABEL ?? "life.meyers.mecha-router"}`],
            { timeout: 5_000 },
          );
          if (result.code !== 0) {
            throw new Error(result.stderr.trim() || `launchctl exited ${result.code}`);
          }
        },
        onKickstartFailure: (error) => {
          context.ui.setStatus("mecha-mailbox", "mailbox: router start failed; retrying");
          context.ui.notify(`Mailbox v2 router kickstart failed: ${errorMessage(error)}`, "error");
        },
        onConnectionState: (value) => { if (!isRecord(value)) return; const state = value.state; const error = value.error;
          if (client !== mailboxClient) return;
          if (state === "connected") {
            publishGoalState(mailboxClient);
            context.ui.setStatus("mecha-mailbox", "mailbox: connected");
          } else if (state === "connecting" || state === "reconnecting") {
            context.ui.setStatus("mecha-mailbox", "mailbox: reconnecting");
          } else if (state === "disconnected") {
            reminderLock.clear();
            context.ui.setStatus("mecha-mailbox", "mailbox: disconnected");
            if (error !== undefined) context.ui.notify(`Mailbox v2 reconnecting: ${errorMessage(error)}`, "error");
          }
        },
        log: () => {},
      });
      client = mailboxClient;
      publishGoalState(mailboxClient);
      coordinator = createMailboxCoordinator(context, mailboxClient);
      activityLifecycle.ensureHeartbeat();
      void mailboxClient.connect().catch((error: ErrorLike) => {
        if (client === mailboxClient) context.ui.notify(`Mailbox v2 unavailable: ${errorMessage(error)}`, "error");
      });
    } catch (error) {
      startupError = error instanceof Error ? error : new Error(String(error));
      context.ui.notify(`Mailbox v2 unavailable: ${errorMessage(error)}`, "error");
    }
  });

  pi.on("session_shutdown", (_event: PiEvent, context: PiContext) => {
    const sessionId = context.sessionManager.getSessionId();
    if (children.has(sessionId) || childReplyClients.has(sessionId)) {
      childReplyClients.delete(sessionId);
      childReplyClientAgentIds.delete(sessionId);
      return;
    }
    mailboxSendRateLimits.clear();
    rememberParentActivityContext(context);
    activityLifecycle.onSessionBoundary();
    parentActivityContext = undefined;
    settledNotificationPending = false;
    settledNotificationAgentId = undefined;
    compactionState().onSessionShutdown();
    settledNotifications.dispose();
    coordinator?.close();
    coordinator = undefined;
    client?.close();
    client = undefined;
    context.ui.setStatus("mecha-mailbox", undefined);
  });
}

export default function mechaMessagingExtension(pi: PiRuntime) {
  return createMechaMessagingExtension(pi);
}
