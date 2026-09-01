import {
  compactionNotificationMessage,
  effectiveCompactionPolicy,
  hardCompactionThreshold,
  nextCompactionThreshold,
  normalizePostCompactionNotification,
  postCompactionNotificationMessage,
  resolveCompactionPolicy,
  selectCompactionTier,
} from "./compaction-policy.js";
import type {
  CompactionLevelInput,
  CompactionPolicy,
  CompactionPolicyInput,
  CompactionTier,
} from "./compaction-policy.js";

export type CompactionErrorLike = Error | {
  code?: string;
  message?: string;
  name?: string;
  cause?: CompactionErrorLike;
};

export type CompactionUsage = {
  percent: number | null;
  tokens: number | null;
  contextWindow: number;
  model?: { provider?: string; id?: string; name?: string; contextWindow?: number; maxTokens?: number };
};

export type CompactionResult = {
  tokensBefore?: number;
  estimatedTokensAfter?: number;
  firstKeptEntryId?: string;
  summary?: string;
};

export type CompactionContext = {
  sessionManager: { getSessionId: () => string };
  model?: { provider?: string; id?: string; name?: string; contextWindow?: number; maxTokens?: number };
  ui?: {
    notify?: (message: string, level?: string) => void;
    setStatus?: (key: string, value: string | undefined) => void;
  };
  isIdle?: () => boolean;
  getContextUsage?: () => CompactionUsage | undefined;
  compact?: (options: {
    customInstructions?: string;
    onComplete?: (result: CompactionResult) => void;
    onError?: (error: CompactionErrorLike) => void;
  }) => void;
  abort?: () => void;
};

export type CompactionEvent = {
  signal?: AbortSignal;
  preparation?: {
    messagesToSummarize?: readonly unknown[];
    turnPrefixMessages?: readonly unknown[];
  };
};

export type CaptureLifecycle = {
  compact: (messages: unknown, options?: { signal?: AbortSignal }) => Promise<unknown>;
  shutdown: (messages: unknown, options?: { deadlineAt?: number; signal?: AbortSignal }) => Promise<unknown>;
};

export type PendingSelfCompaction = {
  customInstructions?: string;
  postCompactionNotification?: string;
};

type PendingHardCompaction = { thresholdPercent: number };

type SelectedCompactionTier = CompactionTier & {
  matchedModelKey?: string;
  modelKeys: string[];
};

export type CompactionStatus = {
  policy: CompactionPolicy;
  effectivePolicy: CompactionPolicy;
  modelKeys: string[];
  matchedModelKey?: string;
  selectedTier?: SelectedCompactionTier;
  usage?: CompactionUsage;
  notifiedThroughPercent: number;
  configurationError?: string;
};

export type CompactionOrchestration = {
  isMailboxPaused: () => boolean;
  hasPendingSelfCompaction: () => boolean;
  hasPendingHardCompaction: () => boolean;
  status: (context: CompactionContext) => CompactionStatus;
  setPolicy: (
    input: CompactionPolicyInput | readonly CompactionLevelInput[] | undefined,
    context: CompactionContext,
  ) => CompactionStatus;
  requestSelfCompaction: (
    input: { customInstructions?: string; postCompactionNotification?: string },
    context: CompactionContext,
  ) => PendingSelfCompaction;
  runPendingSelfCompaction: (context: CompactionContext) => void;
  enforceHardCompaction: (context: CompactionContext) => boolean;
  monitorContextThreshold: (context: CompactionContext) => void;
  onSessionBeforeCompact: (event: CompactionEvent, context: CompactionContext) => Promise<void>;
  onSessionCompact: (context: CompactionContext) => void;
  onSessionCompactFailed: (context: CompactionContext) => void;
  onSessionShutdown: () => void;
};

export type CompactionOrchestrationOptions = {
  getClient: () => { sendMessage?: (message: object, options?: { deliverAs?: string; triggerTurn?: boolean }) => unknown } | undefined;
  isChildSession: (sessionId: string) => boolean;
  rememberParentContext: (context: CompactionContext) => void;
  activity: {
    onCompactionStarted: (context: CompactionContext) => void;
    onCompactionResumed: (context: CompactionContext) => void;
  };
  setMailboxStatus: (context: CompactionContext, value: "compacting" | "connected") => void;
  sendMessage: (message: object, options?: { deliverAs?: string; triggerTurn?: boolean }) => unknown;
  ensureCapture: (signal: AbortSignal | undefined) => Promise<CaptureLifecycle>;
  getEnvironment: () => NodeJS.ProcessEnv;
  initialPolicy: CompactionPolicy;
  initialPolicyError?: CompactionErrorLike;
  errorMessage: (error: unknown) => string;
  setTimeoutImpl: typeof setTimeout;
};

const CONTEXT_THRESHOLD_MESSAGE_TYPE = "mecha-context-threshold";
const POST_COMPACTION_MESSAGE_TYPE = "mecha-post-compaction-handoff";

export function createCompactionOrchestration(
  options: CompactionOrchestrationOptions,
): CompactionOrchestration {
  let mailboxPausedForCompaction = false;
  let compactionPolicy = options.initialPolicy;
  let compactionPolicyError = options.initialPolicyError;
  let notifiedCompactionPercent = 0;
  let pendingSelfCompaction: PendingSelfCompaction | undefined;
  let pendingHardCompaction: PendingHardCompaction | undefined;

  const isChild = (context: CompactionContext): boolean => options.isChildSession(context.sessionManager.getSessionId());

  const pauseMailboxForCompaction = (context: CompactionContext): void => {
    if (isChild(context) || options.getClient() === undefined || mailboxPausedForCompaction) return;
    options.rememberParentContext(context);
    mailboxPausedForCompaction = true;
    options.activity.onCompactionStarted(context);
    options.setMailboxStatus(context, "compacting");
  };

  const resumeMailboxAfterCompaction = (context: CompactionContext): void => {
    if (isChild(context) || options.getClient() === undefined || !mailboxPausedForCompaction) return;
    options.rememberParentContext(context);
    mailboxPausedForCompaction = false;
    options.activity.onCompactionResumed(context);
    options.setMailboxStatus(context, "connected");
  };

  const status = (context: CompactionContext): CompactionStatus => {
    const usage = context.getContextUsage?.();
    const environment = options.getEnvironment();
    const effective = effectiveCompactionPolicy(compactionPolicy, context.model, environment);
    return {
      policy: compactionPolicy,
      effectivePolicy: effective.policy,
      modelKeys: effective.modelKeys,
      matchedModelKey: effective.matchedModelKey,
      selectedTier: usage === undefined
        ? undefined
        : selectCompactionTier(compactionPolicy, usage.contextWindow, context.model, environment),
      usage,
      notifiedThroughPercent: notifiedCompactionPercent,
      configurationError: compactionPolicyError === undefined ? undefined : options.errorMessage(compactionPolicyError),
    };
  };

  const sendPostCompactionNotification = (note: string | undefined, result: CompactionResult): void => {
    const normalizedNote = normalizePostCompactionNotification(note);
    if (normalizedNote === undefined) return;
    const message = {
      customType: POST_COMPACTION_MESSAGE_TYPE,
      content: postCompactionNotificationMessage(normalizedNote, result),
      display: true,
      details: {
        tokens_before: typeof result?.tokensBefore === "number" && Number.isFinite(result.tokensBefore) ? result.tokensBefore : undefined,
        estimated_tokens_after: typeof result?.estimatedTokensAfter === "number" && Number.isFinite(result.estimatedTokensAfter) ? result.estimatedTokensAfter : undefined,
        first_kept_entry_id: typeof result?.firstKeptEntryId === "string" ? result.firstKeptEntryId : undefined,
        summary_chars: typeof result?.summary === "string" ? result.summary.length : undefined,
      },
    };
    options.setTimeoutImpl(() => {
      try {
        options.sendMessage(message, { deliverAs: "followUp", triggerTurn: true });
      } catch {
        // A post-compaction handoff must not turn a successful compaction into a failed compact call.
      }
    }, 0);
  };

  const requestSelfCompaction = (
    input: { customInstructions?: string; postCompactionNotification?: string },
    context: CompactionContext,
  ): PendingSelfCompaction => {
    if (typeof context.compact !== "function") throw new Error("Pi compaction is unavailable in this context");
    const handoffNote = input.postCompactionNotification
      ?? effectiveCompactionPolicy(compactionPolicy, context.model, options.getEnvironment()).policy.postCompactionNotification;
    pendingSelfCompaction = {
      customInstructions: input.customInstructions,
      postCompactionNotification: normalizePostCompactionNotification(handoffNote),
    };
    return pendingSelfCompaction;
  };

  const runPendingSelfCompaction = (context: CompactionContext): void => {
    if (pendingSelfCompaction === undefined || isChild(context)) return;
    if (typeof context.compact !== "function") {
      pendingSelfCompaction = undefined;
      context.ui?.notify?.("Self-compaction is unavailable at the idle boundary; use /compact manually.", "error");
      return;
    }
    if (typeof context.isIdle === "function" && !context.isIdle()) return;
    const request = pendingSelfCompaction;
    pendingSelfCompaction = undefined;
    context.compact({
      customInstructions: request.customInstructions,
      onComplete: (result: CompactionResult) => {
        resumeMailboxAfterCompaction(context);
        notifiedCompactionPercent = 0;
        sendPostCompactionNotification(request.postCompactionNotification, result);
      },
      onError: (error: CompactionErrorLike) => {
        resumeMailboxAfterCompaction(context);
        context.ui?.notify?.(`Self-compaction failed: ${options.errorMessage(error)}`, "error");
      },
    });
  };

  const enforceHardCompaction = (context: CompactionContext): boolean => {
    if (pendingHardCompaction !== undefined) {
      context.abort?.();
      return true;
    }
    const usage = context.getContextUsage?.();
    if (usage === undefined || usage.percent === null) return false;
    const threshold = hardCompactionThreshold(compactionPolicy, usage, undefined, options.getEnvironment());
    if (threshold === undefined) return false;
    if (typeof context.compact !== "function") {
      context.abort?.();
      context.ui?.notify?.("Pi context limit reached, but compaction is unavailable.", "error");
      return true;
    }

    const request: PendingHardCompaction = { thresholdPercent: threshold.percent };
    pendingHardCompaction = request;
    // Pi sends this abort signal to the provider and running tools, but custom
    // implementations can ignore it and delay the idle boundary for compaction.
    context.abort?.();
    try {
      context.compact({
        onComplete: () => {
          if (pendingHardCompaction !== request) return;
          pendingHardCompaction = undefined;
          resumeMailboxAfterCompaction(context);
        },
        onError: (error: CompactionErrorLike) => {
          if (pendingHardCompaction !== request) return;
          pendingHardCompaction = undefined;
          resumeMailboxAfterCompaction(context);
          context.ui?.notify?.(`Hard context compaction failed: ${options.errorMessage(error)}`, "error");
        },
      });
    } catch (error) {
      if (pendingHardCompaction === request) pendingHardCompaction = undefined;
      const message = options.errorMessage(error);
      context.ui?.notify?.(`Hard context compaction failed: ${message}`, "error");
    }
    return true;
  };

  const monitorContextThreshold = (context: CompactionContext): void => {
    if (isChild(context)) return;
    const usage = context.getContextUsage?.();
    if (usage?.percent === null || usage?.percent === undefined) return;
    const level = nextCompactionThreshold(
      compactionPolicy,
      { ...usage, model: context.model },
      notifiedCompactionPercent,
      undefined,
      undefined,
      options.getEnvironment(),
    );
    if (level === undefined) return;
    notifiedCompactionPercent = level.percent;
    options.sendMessage({
      customType: CONTEXT_THRESHOLD_MESSAGE_TYPE,
      content: compactionNotificationMessage({ level, usage }),
      display: true,
      details: {
        percent: usage.percent,
        tokens: usage.tokens,
        context_window: usage.contextWindow,
        threshold_percent: level.percent,
        tier_max_context_tokens: level.tier.maxContextTokens,
        mandatory: level.mandatory === true,
      },
    }, level.mandatory === true
      ? { triggerTurn: false }
      : { deliverAs: "followUp", triggerTurn: true });
  };

  const onSessionBeforeCompact = async (event: CompactionEvent, context: CompactionContext): Promise<void> => {
    pauseMailboxForCompaction(context);
    const resumeIfCanceled = () => resumeMailboxAfterCompaction(context);
    event.signal?.addEventListener?.("abort", resumeIfCanceled, { once: true });
    try {
      const capture = await options.ensureCapture(event.signal);
      await capture.compact([
        ...(event.preparation?.messagesToSummarize ?? []),
        ...(event.preparation?.turnPrefixMessages ?? []),
      ], { signal: event.signal });
    } catch {
      // Capture is best effort and must not change Pi's compaction outcome.
    }
    if (event.signal?.aborted) resumeIfCanceled();
  };

  const onSessionCompact = (context: CompactionContext): void => {
    if (isChild(context)) return;
    notifiedCompactionPercent = 0;
    resumeMailboxAfterCompaction(context);
  };

  const onSessionCompactFailed = (context: CompactionContext): void => {
    if (isChild(context)) return;
    // Failure ends the pause, but keeps watermark and pending requests intact.
    resumeMailboxAfterCompaction(context);
  };

  const onSessionShutdown = (): void => {
    pendingSelfCompaction = undefined;
    mailboxPausedForCompaction = false;
  };

  return {
    isMailboxPaused: () => mailboxPausedForCompaction,
    hasPendingSelfCompaction: () => pendingSelfCompaction !== undefined,
    hasPendingHardCompaction: () => pendingHardCompaction !== undefined,
    status,
    setPolicy: (input, context) => {
      compactionPolicy = resolveCompactionPolicy(input);
      compactionPolicyError = undefined;
      notifiedCompactionPercent = 0;
      return status(context);
    },
    requestSelfCompaction,
    runPendingSelfCompaction,
    enforceHardCompaction,
    monitorContextThreshold,
    onSessionBeforeCompact,
    onSessionCompact,
    onSessionCompactFailed,
    onSessionShutdown,
  };
}
