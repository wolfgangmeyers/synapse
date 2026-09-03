type JsonPrimitive = string | number | boolean | null;
type JsonValue = JsonPrimitive | JsonObject | JsonValue[] | undefined;
type JsonObject = { [key: string]: JsonValue };

export type NativeSubagent = JsonObject & {
  id?: string;
  type?: string;
  description?: string;
  status?: string;
  startedAt?: number | string;
};

export type NativeSubagentService = {
  spawn?: (type: string, prompt: string, options: { description: string; model?: JsonObject; inheritContext: boolean }) => Promise<string> | string;
  getLifecycleSnapshots?: () => NativeSubagent[];
  listAgents?: () => NativeSubagent[];
  getRunning?: () => NativeSubagent[];
  running?: NativeSubagent[];
  runs?: NativeSubagent[];
  subagents?: NativeSubagent[];
};

export type ActivityContext = {
  sessionManager: { getSessionId: () => string };
  ui?: { notify?: (message: string, level?: string) => void };
  isIdle?: () => boolean;
};

export type ActivityGoalProjection = { enabled: boolean; goal: string };
export type ActivityModel = { provider: string; id: string; name?: string; contextWindow?: number; maxTokens?: number };

type ActivityPayload = {
  subagentCount?: number;
  subagents?: unknown;
  goal?: unknown;
  currentModel?: unknown;
};

type ActivityClient = {
  sendActivity?: (state: string, payload?: ActivityPayload) => unknown;
  sendLifecycleSnapshot?: (subagents: unknown) => string | undefined;
};

type Timer = ReturnType<typeof setTimeout>;
export type TimerOptions = { setTimeoutImpl?: typeof setTimeout; clearTimeoutImpl?: typeof clearTimeout };

type ActivityLifecycleTimerOptions = TimerOptions & {
  setIntervalImpl?: typeof setInterval;
  clearIntervalImpl?: typeof clearInterval;
  nowImpl?: () => number;
};

export type SettledNotificationScheduler = {
  onParentActivity: () => void;
  onParentSettled: (send: () => void) => void;
  onChildStarted: (sessionId: string) => void;
  onChildDisposed: (sessionId: string) => void;
  dispose: () => void;
  activeChildCount: () => number;
};

export type ActivityLifecycleOptions = ActivityLifecycleTimerOptions & {
  getClient: () => ActivityClient | undefined;
  getParentContext: () => ActivityContext | undefined;
  isCompacting: () => boolean;
  getGoalProjection: () => ActivityGoalProjection | undefined;
  getCurrentModel: () => ActivityModel | undefined;
  getNativeService: (ownerSessionId: string) => NativeSubagentService | undefined;
  notify: (message: string, level?: string) => void;
  trace: (event: string, fields?: object) => unknown;
  diagnostic?: (event: string, fields?: object) => void;
  createSettledScheduler: (options?: TimerOptions) => SettledNotificationScheduler;
  rememberParentContext?: (context: ActivityContext | undefined) => void;
};

const PI_SUBAGENTS_SERVICE_REGISTRY = Symbol.for("@gotgenes/pi-subagents:service-registry");
const SETTLED_NOTIFICATION_COOLDOWN_MS = 120_000;
// Native lifecycle visibility is intentionally polling-only. This cadence bounds how
// long the web UI can show a stale child without relying on Pi event delivery.
const ACTIVITY_HEARTBEAT_INTERVAL_MS = 2_000;
const ACTIVITY_REFRESH_MIN_MS = 10_000;
const EVENT_TRACKED_MISSING_LIMIT_MS = 60_000;
// Keep activity payloads bounded so a large native service cannot become an
// unbounded cross-extension data path.
const MAX_ACTIVITY_SUBAGENTS = 100;
const MAX_SETTLED_NOTIFICATION_CHILDREN = 2048;
const MAX_TERMINAL_SUBAGENT_TOMBSTONES = 2048;
const LIVE_SUBAGENT_STATUSES = new Set(["queued", "running"]);
const TERMINAL_SUBAGENT_STATUSES = new Set(["completed", "steered", "aborted", "stopped", "error"]);
const LEGACY_TERMINAL_SUBAGENT_STATUSES = [
  "completed", "complete", "done", "finished", "failed", "failure", "error", "errored",
  "canceled", "cancelled", "aborted", "terminated", "stopped", "steered",
];

export function summarizeNativeSubagents(subagents: NativeSubagent[] | undefined): Array<{ id: string; type?: string; status?: string; description_length: number }> {
  if (!Array.isArray(subagents)) return [];
  return subagents.slice(0, 999).flatMap((subagent) => {
    const id = typeof subagent?.id === "string" ? subagent.id.trim().slice(0, 128) : "";
    if (id.length === 0) return [];
    const type = typeof subagent.type === "string" ? subagent.type.trim().slice(0, 64) : "";
    const status = typeof subagent.status === "string" ? subagent.status.trim().slice(0, 64) : "";
    const descriptionLength = typeof subagent.description === "string" ? subagent.description.length : 0;
    return [{ id, ...(type.length > 0 ? { type } : {}), ...(status.length > 0 ? { status } : {}), description_length: descriptionLength }];
  });
}

export function createSettledNotificationScheduler({
  setTimeoutImpl = setTimeout,
  clearTimeoutImpl = clearTimeout,
}: TimerOptions = {}): SettledNotificationScheduler {
  if (typeof setTimeoutImpl !== "function" || typeof clearTimeoutImpl !== "function") {
    throw new TypeError("settled notification timers are required");
  }

  const children = new Set<string>();
  const timers = new Set<Timer>();
  let generation = 0;
  let parentSettled = false;
  // Keep a bounded ID set while retaining suppression when more children arrive
  // than the set can name. Reconciliation or disposal clears the known IDs;
  // the overflow latch is intentionally conservative until scheduler disposal.
  let overflowSuppressed = false;

  function invalidate() {
    generation += 1;
    for (const timer of timers) clearTimeoutImpl(timer);
    timers.clear();
  }

  function schedule(delay: number, expectedGeneration: number, send: () => void): void {
    let timer: Timer;
    timer = setTimeoutImpl(() => {
      timers.delete(timer);
      if (generation !== expectedGeneration || children.size > 0 || overflowSuppressed) return;
      send();
    }, delay);
    timers.add(timer);
    timer?.unref?.();
  }

  return {
    onParentActivity() {
      parentSettled = false;
      invalidate();
    },
    onParentSettled(send: () => void): void {
      if (parentSettled || typeof send !== "function") return;
      parentSettled = true;
      if (children.size > 0) return;

      invalidate();
      const settledGeneration = generation;
      schedule(SETTLED_NOTIFICATION_COOLDOWN_MS, settledGeneration, send);
    },
    onChildStarted(sessionId: string): void {
      if (typeof sessionId !== "string" || sessionId.length === 0 || children.has(sessionId)) return;
      if (children.size >= MAX_SETTLED_NOTIFICATION_CHILDREN) overflowSuppressed = true;
      else children.add(sessionId);
      invalidate();
    },
    onChildDisposed(sessionId: string): void {
      if (typeof sessionId !== "string" || sessionId.length === 0) return;
      children.delete(sessionId);
    },
    dispose() {
      invalidate();
      children.clear();
      overflowSuppressed = false;
      parentSettled = false;
    },
    activeChildCount() {
      return children.size;
    },
  };
}

type TrackedSubagent = {
  id: string;
  type?: string;
  description?: string;
  status?: string;
  startedAtMs: number;
  lastSeenMs: number;
  eventTracked?: boolean;
  serviceTracked?: boolean;
};
type LifecycleSummary = { id: string; type?: string; description: string; status?: string };
type LifecycleIdentity = { type?: string; description?: string };
type SettledChildState = {
  summary?: LifecycleSummary;
  startFence?: LifecycleIdentity;
  awaitingIdentity?: boolean;
  terminalObserved?: boolean;
  // The native manager creates a new redacted snapshot object from
  // onResumeStarted, even though its ID and identity stay the same. Retain the
  // last bounded source object so a resumed run can be distinguished from the
  // delayed object published before the terminal event.
  lastSnapshot?: NativeSubagent;
};
type TypedLifecycleSnapshot = { summary: LifecycleSummary; source: NativeSubagent };
type LifecycleIdentityFence = {
  identity: LifecycleIdentity;
  mode: "awaiting" | "expected" | "terminal";
};
type ActivitySent = {
  state: string;
  subagentCount: number;
  subagents: Array<{ id: string; type?: string; description: string; status?: string }>;
  goal?: ActivityGoalProjection;
  currentModel?: ActivityModel;
  sentAtMs: number;
};

function errorName<T>(error: T): string {
  return error instanceof Error ? error.name : isRecord(error) && typeof error.name === "string" ? error.name : "Error";
}

function isRecord<T>(value: T): value is T & JsonObject {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function safeDiagnostic(
  diagnostic: ((event: string, fields?: object) => void) | undefined,
  event: string,
  fields?: object,
): void {
  try {
    if (typeof diagnostic === "function") diagnostic(event, fields);
  } catch {
    // Diagnostics are best-effort and must not break lifecycle recovery.
  }
}

function serviceRegistryAvailable(): boolean {
  const registry = Object.getOwnPropertyDescriptor(globalThis, PI_SUBAGENTS_SERVICE_REGISTRY)?.value;
  const services = isRecord(registry) ? registry.services : undefined;
  return isRecord(services) && typeof services.get === "function";
}

export function createActivityLifecycle(options: ActivityLifecycleOptions): ActivityLifecycle {
  const {
    getClient,
    getParentContext,
    isCompacting,
    getGoalProjection,
    getCurrentModel,
    getNativeService: resolveNativeService,
    notify,
    trace,
    diagnostic,
    createSettledScheduler,
    rememberParentContext,
    setIntervalImpl = setInterval,
    clearIntervalImpl = clearInterval,
    nowImpl = Date.now,
  } = options;
  if (typeof getClient !== "function" || typeof getParentContext !== "function"
    || typeof isCompacting !== "function" || typeof getGoalProjection !== "function"
    || typeof getCurrentModel !== "function" || typeof resolveNativeService !== "function"
    || typeof notify !== "function" || typeof trace !== "function"
    || typeof createSettledScheduler !== "function") {
    throw new TypeError("activity lifecycle callbacks are required");
  }
  if (typeof setIntervalImpl !== "function" || typeof clearIntervalImpl !== "function" || typeof nowImpl !== "function") {
    throw new TypeError("activity lifecycle timers are required");
  }

  const settledNotifications = createSettledScheduler(options);
  const runningSubagents = new Map<string, TrackedSubagent>();
  const terminalSubagentTombstones = new Map<string, number>();
  // The settled scheduler has its own child set. Keep the IDs and the latest
  // known run identity here so a late terminal event cannot dispose a reused ID.
  // Native lifecycle payloads have no run/version field. A start fence holds an
  // ID through stale omissions until a snapshot confirms the same safe identity.
  // Keep this map bounded: the native projection itself is capped at 100 IDs.
  const settledNotificationChildStates = new Map<string, SettledChildState>();
  // Keep the identity-only part of a fence when its metadata record is evicted.
  // This is bounded separately from the state map because every state can be
  // fenced during churn, including the state that protects a reused ID.
  const settledNotificationIdentityFences = new Map<string, LifecycleIdentityFence>();
  // Once every bounded fence slot is occupied, quarantine snapshot-only IDs.
  // This keeps a stale row from becoming a child after its identity fence is
  // evicted. Explicit starts can still establish a new fence for that ID.
  let identityFenceOverflowed = false;
  // This set is the live ownership ledger. Metadata is bounded, but a metadata
  // eviction must not release suppression for a child that is still running.
  const settledNotificationChildIds = new Set<string>();
  const MAX_SETTLED_CHILD_STATES = MAX_SETTLED_NOTIFICATION_CHILDREN;
  // Session shutdown is terminal for this lifecycle instance. This fence covers
  // native events plus callbacks already queued by the heartbeat or compaction.
  let nativeEventsBlocked = false;
  // Each heartbeat captures the lifecycle generation in which it was queued.
  // Session boundaries advance it so a callback released by the old session
  // cannot touch the new session's projection or client.
  let lifecycleGeneration = 0;
  let activityHeartbeatTimer: ReturnType<typeof setInterval> | undefined;
  let activityHeartbeatActive = false;
  let lastActivitySent: ActivitySent | undefined;
  let lastNativeSubagentReadDiagnosticMs: number | undefined;

  function remember(context: ActivityContext | undefined): void {
    if (context !== undefined) rememberParentContext?.(context);
  }

  function contextSessionId(context: ActivityContext | undefined): string | undefined {
    return context?.sessionManager?.getSessionId?.();
  }

  function getNativeService(ownerSessionId: string | undefined = contextSessionId(getParentContext())): NativeSubagentService | undefined {
    const available = typeof ownerSessionId === "string";
    let service: NativeSubagentService | undefined;
    try {
      service = available ? resolveNativeService(ownerSessionId) : undefined;
      trace("extension_service_lookup", {
        ...(available ? { parent_session_id: ownerSessionId } : {}),
        available: service !== undefined,
        registry_available: serviceRegistryAvailable(),
        lifecycle_service: typeof service?.getLifecycleSnapshots === "function",
      });
      return service;
    } catch (error) {
      trace("extension_service_lookup_error", {
        // The resolver is called only for a present owner ID, so this spread's
        // alternate branch is unreachable after the guarded lookup.
        /* c8 ignore next */
        ...(available ? { parent_session_id: ownerSessionId } : {}),
        error_name: errorName(error),
      });
      safeDiagnostic(diagnostic, "owner_service_lookup_failed", {
        reason: "service_lookup",
        ...(available ? { session_id: ownerSessionId } : {}),
        error_name: errorName(error),
      });
      return undefined;
    }
  }

  function normalizeSubagentMetadata(event: NativeSubagent, startedAtMs: number = nowImpl()): TrackedSubagent | undefined {
    const id = typeof event?.id === "string" ? event.id.trim().slice(0, 128) : "";
    if (id.length === 0) return undefined;
    const description = typeof event?.description === "string" ? event.description.trim().slice(0, 512) : "";
    const type = typeof event?.type === "string" ? event.type.trim().slice(0, 64) : "";
    const status = typeof event?.status === "string" ? event.status.toLowerCase() : undefined;
    return {
      id,
      ...(type.length > 0 ? { type } : {}),
      ...(description.length > 0 ? { description } : {}),
      ...(status === undefined ? {} : { status }),
      startedAtMs,
      lastSeenMs: startedAtMs,
    };
  }

  function publicSubagentSummaries(): Array<{ id: string; type?: string; description: string; status?: string }> {
    return [...runningSubagents.values()]
      .filter((item): item is TrackedSubagent & { description: string } => typeof item.description === "string" && item.description.length > 0)
      .sort((left, right) => left.startedAtMs - right.startedAtMs || left.id.localeCompare(right.id))
      .slice(0, MAX_ACTIVITY_SUBAGENTS)
      .map(({ id, type, description, status }) => ({ id, ...(type !== undefined ? { type } : {}), description, ...(status !== undefined ? { status } : {}) }));
  }

  function publishLifecycleSnapshot(subagents: NativeSubagent[]): void {
    const summary = summarizeNativeSubagents(subagents);
    try {
      const eventId = getClient()?.sendLifecycleSnapshot?.(subagents);
      trace("extension_snapshot_publish", {
        published: typeof eventId === "string",
        ...(typeof eventId === "string" ? { event_id: eventId } : {}),
        snapshot_count: summary.length,
        subagents: summary,
      });
    } catch (error) {
      safeDiagnostic(diagnostic, "snapshot_publish_failed", {
        reason: "snapshot_publish",
        state: "lifecycle",
        published: false,
        ...(contextSessionId(getParentContext()) === undefined ? {} : { session_id: contextSessionId(getParentContext()) }),
        error_name: errorName(error),
        snapshot_count: summary.length,
      });
      trace("extension_snapshot_publish_error", {
        snapshot_count: summary.length,
        subagents: summary,
        error_name: errorName(error),
      });
      // Lifecycle polling retries on the next tick after a best-effort client failure.
    }
  }

  function sameSubagentMetadata(left: TrackedSubagent | undefined, right: TrackedSubagent | undefined): boolean {
    return left !== undefined && right !== undefined && left.id === right.id && left.type === right.type
      && left.description === right.description && left.status === right.status;
  }

  function sameLifecycleIdentity(left: LifecycleIdentity | undefined, right: LifecycleIdentity | undefined): boolean {
    return left !== undefined && right !== undefined
      && left.type === right.type && left.description === right.description;
  }

  function lifecycleFenceMatches(fence: LifecycleIdentity, record: LifecycleIdentity): boolean {
    return sameLifecycleIdentity(fence, record);
  }

  function hasLifecycleIdentity(identity: LifecycleIdentity): boolean {
    return identity.type !== undefined || identity.description !== undefined;
  }

  function lifecycleIdentity(record: NativeSubagent | LifecycleSummary): LifecycleIdentity {
    const type = typeof record.type === "string" ? record.type.trim().slice(0, 64) : "";
    const description = typeof record.description === "string" ? record.description.trim().slice(0, 512) : "";
    return {
      ...(type.length > 0 ? { type } : {}),
      ...(description.length > 0 ? { description } : {}),
    };
  }

  function rememberIdentityFence(id: string, fence: LifecycleIdentityFence): void {
    if (settledNotificationIdentityFences.has(id)) {
      settledNotificationIdentityFences.delete(id);
      settledNotificationIdentityFences.set(id, fence);
      return;
    }
    if (identityFenceOverflowed) return;
    if (settledNotificationIdentityFences.size >= MAX_SETTLED_CHILD_STATES) {
      // There is no safe fence to evict. Keep the existing bounded fences and
      // quarantine IDs that have no explicit start after this point.
      identityFenceOverflowed = true;
      return;
    }
    settledNotificationIdentityFences.set(id, fence);
  }

  function snapshotIdentityIsBlocked(state: SettledChildState | undefined, fence: LifecycleIdentityFence | undefined): boolean {
    if (state?.awaitingIdentity === true && state.summary === undefined && state.startFence === undefined) return true;
    if (fence?.mode === "awaiting" && !hasLifecycleIdentity(fence.identity)) return true;
    return identityFenceOverflowed && state === undefined && fence === undefined;
  }

  function stateIdentityFence(state: SettledChildState | undefined): LifecycleIdentityFence | undefined {
    if (state?.startFence !== undefined) return { identity: state.startFence, mode: "expected" };
    if (state?.awaitingIdentity === true) {
      // An event without identity still needs a fence. The empty identity means
      // that snapshots remain blocked until a later start supplies metadata.
      return { identity: state.summary === undefined ? {} : lifecycleIdentity(state.summary), mode: "awaiting" };
    }
    if (state?.terminalObserved === true && state.summary !== undefined) {
      return { identity: lifecycleIdentity(state.summary), mode: "terminal" };
    }
    return undefined;
  }

  function identityFenceFor(id: string, state: SettledChildState | undefined = settledNotificationChildStates.get(id)): LifecycleIdentityFence | undefined {
    return stateIdentityFence(state) ?? settledNotificationIdentityFences.get(id);
  }

  function summaryFromIdentity(id: string, identity: LifecycleIdentity): LifecycleSummary {
    // Typed lifecycle summaries always carry both identity fields. These fallbacks
    // only protect an identity reconstructed from a malformed external event.
    /* c8 ignore next 6 */
    return {
      id,
      type: identity.type ?? "general-purpose",
      description: identity.description ?? "Native subagent",
      status: "running",
    };
  }

  function boundedSettledChildState(id: string): SettledChildState {
    const existing = settledNotificationChildStates.get(id);
    if (existing !== undefined) return existing;
    const state: SettledChildState = {};
    settledNotificationChildStates.set(id, state);
    while (settledNotificationChildStates.size > MAX_SETTLED_CHILD_STATES) {
      // Preserve identities that still fence a reused ID. Evict ordinary
      // metadata first; when every entry is fenced, copy its identity into the
      // bounded identity-only fence map before evicting the metadata.
      const evictable = [...settledNotificationChildStates.entries()].find(([candidateId, candidate]) => (
        candidateId !== id
        && candidate.startFence === undefined && candidate.awaitingIdentity !== true && candidate.terminalObserved !== true
      ));
      // Do not evict the state being initialized; its fence is applied below.
      const oldest = evictable?.[0] ?? [...settledNotificationChildStates.keys()].find((candidateId) => candidateId !== id);
      // The map is keyed by strings and the loop runs only while it is nonempty.
      /* c8 ignore next */
      if (typeof oldest !== "string") break;
      const evicted = settledNotificationChildStates.get(oldest);
      const fence = stateIdentityFence(evicted);
      if (fence !== undefined) rememberIdentityFence(oldest, fence);
      settledNotificationChildStates.delete(oldest);
    }
    return state;
  }

  function schedulerChildStarted(id: string | undefined, options: { fence?: LifecycleIdentity; awaitingIdentity?: boolean; force?: boolean } = {}): void {
    if (typeof id !== "string" || id.length === 0) return;
    const state = boundedSettledChildState(id);
    if (options.fence !== undefined) state.startFence = options.fence;
    if (options.awaitingIdentity === true) state.awaitingIdentity = true;
    const newlyOwned = !settledNotificationChildIds.has(id);
    if (newlyOwned && settledNotificationChildIds.size >= MAX_SETTLED_NOTIFICATION_CHILDREN) {
      const oldest = settledNotificationChildIds.keys().next().value;
      if (typeof oldest === "string") settledNotificationChildIds.delete(oldest);
    }
    settledNotificationChildIds.add(id);
    if (newlyOwned || options.force) settledNotifications.onChildStarted(id);
  }

  function schedulerChildDisposed(id: string | undefined): boolean {
    if (typeof id !== "string" || id.length === 0) return false;
    const state = settledNotificationChildStates.get(id);
    // A terminal event carries no run identity. A start fence therefore keeps
    // the ID alive until a later snapshot confirms the new run.
    const fence = identityFenceFor(id, state);
    if (fence?.mode === "expected" || fence?.mode === "awaiting") return false;
    const wasOwned = settledNotificationChildIds.delete(id);
    settledNotificationChildStates.delete(id);
    settledNotificationIdentityFences.delete(id);
    if (wasOwned || state === undefined) settledNotifications.onChildDisposed(id);
    return wasOwned;
  }

  function typedLifecycleSnapshotRecords(source: NativeSubagent[]): TypedLifecycleSnapshot[] {
    return source.flatMap((sourceRecord) => {
      const summary = lifecycleSnapshotSummary(sourceRecord);
      return summary === undefined ? [] : [{ summary, source: sourceRecord }];
    });
  }

  function observeTypedSnapshotRecord(state: SettledChildState | undefined, record: LifecycleSummary, source: NativeSubagent): void {
    if (state === undefined) return;
    // A terminal callback can race a native resume. Pi does not emit
    // subagents:started for that resume, but onResumeStarted replaces the
    // manager's redacted snapshot object. An unchanged source object is the
    // delayed pre-resume snapshot and stays fenced until omission.
    const resumed = state.terminalObserved === true
      && state.lastSnapshot !== undefined
      && state.lastSnapshot !== source
      && state.summary !== undefined
      && sameLifecycleIdentity(lifecycleIdentity(state.summary), lifecycleIdentity(record));
    state.lastSnapshot = source;
    if (resumed) state.terminalObserved = false;
  }

  type ReconciliationCounts = {
    sourceCount: number;
    startedCount: number;
    disposedCount: number;
    trackedCount: number;
  };

  function reconcileSettledNotificationChildren(): ReconciliationCounts {
    const trackedBefore = settledNotificationChildIds.size;
    // This is an internal scheduler repair. Read the typed source directly so
    // resume does not add a diagnostic read or lifecycle publication to the
    // extraction's observable event order.
    const ownerSessionId = contextSessionId(getParentContext());
    if (ownerSessionId === undefined) return { sourceCount: 0, startedCount: 0, disposedCount: 0, trackedCount: trackedBefore };
    let service: NativeSubagentService | undefined;
    try {
      service = resolveNativeService(ownerSessionId);
    } catch (error) {
      safeDiagnostic(diagnostic, "snapshot_service_lookup_failed", {
        reason: "resolver_error",
        state: "snapshot",
        ...(ownerSessionId === undefined ? {} : { session_id: ownerSessionId }),
        error_name: errorName(error),
      });
      return { sourceCount: 0, startedCount: 0, disposedCount: 0, trackedCount: settledNotificationChildIds.size };
    }
    if (typeof service?.getLifecycleSnapshots !== "function") {
      return { sourceCount: 0, startedCount: 0, disposedCount: 0, trackedCount: settledNotificationChildIds.size };
    }
    let source: NativeSubagent[] | undefined;
    try {
      source = service.getLifecycleSnapshots();
    } catch (error) {
      safeDiagnostic(diagnostic, "snapshot_read_failed", {
        reason: "lifecycle_snapshot_error",
        state: "snapshot",
        ...(ownerSessionId === undefined ? {} : { session_id: ownerSessionId }),
        error_name: errorName(error),
      });
      return { sourceCount: 0, startedCount: 0, disposedCount: 0, trackedCount: settledNotificationChildIds.size };
    }
    if (!Array.isArray(source)) return { sourceCount: 0, startedCount: 0, disposedCount: 0, trackedCount: settledNotificationChildIds.size };
    const snapshot = typedLifecycleSnapshotRecords(source);
    let startedCount = 0;
    let disposedCount = 0;
    const authoritative = source.length < MAX_ACTIVITY_SUBAGENTS;
    const activeIds = new Set(snapshot.map(({ summary }) => summary.id));
    if (authoritative) {
      for (const id of settledNotificationChildIds) {
        if (!activeIds.has(id) && identityFenceFor(id) === undefined) {
          if (schedulerChildDisposed(id)) disposedCount += 1;
        }
      }
    }
    for (const { summary: record, source: sourceRecord } of snapshot) {
      const state = settledNotificationChildStates.get(record.id);
      observeTypedSnapshotRecord(state, record, sourceRecord);
      const fence = identityFenceFor(record.id, state);
      if (snapshotIdentityIsBlocked(state, fence)) continue;
      const recordIdentity = lifecycleIdentity(record);
      if ((fence?.mode === "awaiting" || fence?.mode === "terminal")
        && lifecycleFenceMatches(fence.identity, recordIdentity)) continue;
      if (fence?.mode === "expected" && !lifecycleFenceMatches(fence.identity, recordIdentity)) continue;
      if (!settledNotificationChildIds.has(record.id)) startedCount += 1;
      schedulerChildStarted(record.id);
      settledNotificationIdentityFences.delete(record.id);
    }
    return { sourceCount: snapshot.length, startedCount, disposedCount, trackedCount: settledNotificationChildIds.size };
  }

  function lifecycleSnapshotSubagentService(service: NativeSubagentService | undefined = getNativeService()): NativeSubagentService | undefined {
    return typeof service?.getLifecycleSnapshots === "function" ? service : undefined;
  }

  function reportNativeSubagentReadFailure(error: unknown): void {
    const normalizedError = error instanceof Error ? error : new Error(String(error));
    trace("extension_snapshot_read_error", { error_name: errorName(normalizedError) });
    safeDiagnostic(diagnostic, "owner_service_failed", {
      reason: "snapshot_read",
      state: "snapshot",
      ...(contextSessionId(getParentContext()) === undefined ? {} : { session_id: contextSessionId(getParentContext()) }),
      error_name: errorName(normalizedError),
    });
    const nowMs = nowImpl();
    if (lastNativeSubagentReadDiagnosticMs !== undefined && nowMs - lastNativeSubagentReadDiagnosticMs < 60_000) return;
    lastNativeSubagentReadDiagnosticMs = nowMs;
    try {
      notify("Native subagent service read failed; polling will retry.", "error");
    } catch (error) {
      safeDiagnostic(diagnostic, "snapshot_failure_notice_failed", {
        reason: "ui_unavailable",
        state: "snapshot",
        ...(contextSessionId(getParentContext()) === undefined ? {} : { session_id: contextSessionId(getParentContext()) }),
        error_name: errorName(error),
      });
      // An operational notification cannot be allowed to stop later polling retries.
    }
  }

  function nativeSubagentRecords(service: NativeSubagentService | undefined = getNativeService()): NativeSubagent[] | undefined {
    try {
      // The lifecycle projection is the producer's public, redacted source of truth. listAgents()
      // retains terminal result records, so it is only the compatibility fallback for older Pi.
      const source = typeof service?.getLifecycleSnapshots === "function"
        ? service.getLifecycleSnapshots()
        : typeof service?.listAgents === "function"
          ? service.listAgents()
          : typeof service?.getRunning === "function"
            ? service.getRunning()
            : service?.running ?? service?.runs ?? service?.subagents;
      const records = Array.isArray(source) ? source : undefined;
      trace("extension_snapshot_read", {
        source: typeof service?.getLifecycleSnapshots === "function" ? "lifecycle_snapshot" : typeof service?.listAgents === "function" ? "list_agents" : typeof service?.getRunning === "function" ? "get_running" : "property",
        available: records !== undefined,
        snapshot_count: records?.length ?? 0,
        subagents: summarizeNativeSubagents(records),
      });
      return records;
    } catch (error) {
      reportNativeSubagentReadFailure(error);
      return undefined;
    }
  }

  function nativeSubagentStatus(record: NativeSubagent): string | undefined {
    return typeof record?.status === "string" ? record.status.toLowerCase() : record?.status;
  }

  function isActiveNativeSubagentRecord(record: NativeSubagent, service: NativeSubagentService | undefined = getNativeService()): boolean {
    const status = nativeSubagentStatus(record);
    // Typed snapshots are dispatched before this legacy compatibility predicate.
    /* c8 ignore next 3 */
    return lifecycleSnapshotSubagentService(service) !== undefined
      ? status !== undefined && LIVE_SUBAGENT_STATUSES.has(status)
      : [undefined, "running", "queued", "active"].includes(status);
  }

  function isTerminalNativeSubagentRecord(record: NativeSubagent, service: NativeSubagentService | undefined = getNativeService()): boolean {
    const status = nativeSubagentStatus(record);
    // Typed snapshots are dispatched before this legacy compatibility predicate.
    /* c8 ignore next 3 */
    return lifecycleSnapshotSubagentService(service) !== undefined
      ? status !== undefined && TERMINAL_SUBAGENT_STATUSES.has(status)
      : LEGACY_TERMINAL_SUBAGENT_STATUSES.includes(status ?? "");
  }

  function nativeSubagentExplicitStartedAtMs(record: NativeSubagent): number | undefined {
    if (typeof record.startedAt === "number" && Number.isFinite(record.startedAt)) return record.startedAt;
    if (typeof record?.startedAt === "string") {
      const parsed = Date.parse(record.startedAt);
      if (Number.isFinite(parsed)) return parsed;
    }
    return undefined;
  }

  function nativeSubagentStartedAtMs(record: NativeSubagent, fallbackMs: number): number {
    return nativeSubagentExplicitStartedAtMs(record) ?? fallbackMs;
  }

  function rememberTerminalSubagent(id: string | undefined, terminalAtMs: number = nowImpl()): boolean {
    if (typeof id !== "string" || id.length === 0) return false;
    // The bounded tombstone fences stale legacy rows while allowing a newer
    // explicit startedAt value to reuse the same native ID.
    terminalSubagentTombstones.delete(id);
    terminalSubagentTombstones.set(id, terminalAtMs);
    while (terminalSubagentTombstones.size > MAX_TERMINAL_SUBAGENT_TOMBSTONES) {
      const oldest = terminalSubagentTombstones.keys().next().value;
      // The map is keyed by strings and the loop runs only while it is nonempty.
      /* c8 ignore next */
      if (typeof oldest !== "string") break;
      terminalSubagentTombstones.delete(oldest);
    }
    return runningSubagents.delete(id);
  }

  function clearTerminalSubagentTombstone(id: string | undefined): void {
    if (typeof id === "string" && id.length > 0) terminalSubagentTombstones.delete(id);
  }

  function isTerminalTombstonedNativeRecord(id: string, record: NativeSubagent): boolean {
    const terminalAtMs = terminalSubagentTombstones.get(id);
    if (terminalAtMs === undefined) return false;
    const startedAtMs = nativeSubagentExplicitStartedAtMs(record);
    if (startedAtMs !== undefined && startedAtMs > terminalAtMs) {
      terminalSubagentTombstones.delete(id);
      return false;
    }
    return true;
  }

  function mergeNativeSubagentMetadata(existing: TrackedSubagent | undefined, metadata: TrackedSubagent, nowMs: number): TrackedSubagent {
    if (existing?.eventTracked === true) {
      return {
        id: existing.id,
        type: existing.type ?? metadata.type,
        description: existing.description ?? metadata.description,
        status: metadata.status ?? existing.status,
        eventTracked: true,
        serviceTracked: true,
        // Tracked event metadata always receives a numeric start time.
        /* c8 ignore next */
        startedAtMs: existing.startedAtMs ?? metadata.startedAtMs,
        lastSeenMs: nowMs,
      };
    }
    return {
      ...metadata,
      eventTracked: false,
      serviceTracked: true,
      startedAtMs: existing?.startedAtMs ?? metadata.startedAtMs,
      lastSeenMs: nowMs,
    };
  }

  function enforceActivityRecordCap(): boolean {
    let evicted = false;
    while (runningSubagents.size > MAX_ACTIVITY_SUBAGENTS) {
      const oldest = runningSubagents.keys().next().value;
      // The map is keyed by strings and the loop runs only while it is nonempty.
      /* c8 ignore next */
      if (typeof oldest !== "string") break;
      // This only bounds the activity payload. Keep scheduler ownership until
      // a terminal event or an authoritative snapshot proves the child ended.
      runningSubagents.delete(oldest);
      evicted = true;
    }
    return evicted;
  }

  function lifecycleSnapshotSummary(record: NativeSubagent): LifecycleSummary | undefined {
    const id = typeof record?.id === "string" ? record.id.trim().slice(0, 128) : "";
    if (id.length === 0) return undefined;
    const type = typeof record?.type === "string" ? record.type.trim().slice(0, 64) : "";
    const description = typeof record?.description === "string" ? record.description.trim().slice(0, 512) : "";
    const status = nativeSubagentStatus(record);
    if (status === undefined || !LIVE_SUBAGENT_STATUSES.has(status)) return undefined;
    // Copy only observer-safe fields. Prompts, output, results, errors, and timestamps
    // must stay inside the native service and never enter lifecycle frames.
    return {
      id,
      type: type.length > 0 ? type : "general-purpose",
      description: description.length > 0 ? description : "Native subagent",
      status,
    };
  }

  function reconcileTypedLifecycleSnapshot(snapshot: TypedLifecycleSnapshot[], authoritative: boolean, nowMs: number): boolean {
    // The native projection is capped and carries no cursor. A full-looking
    // lower snapshot can therefore be stale or partial. Only a snapshot below
    // the native cap may dispose an omitted ID; start fences protect reuse races.
    const previous = new Map(runningSubagents);
    const next = authoritative ? new Map<string, TrackedSubagent>() : new Map(previous);
    for (const { summary: record, source: sourceRecord } of snapshot) {
      const childState = settledNotificationChildStates.get(record.id);
      observeTypedSnapshotRecord(childState, record, sourceRecord);
      const recordIdentity = lifecycleIdentity(record);
      const fence = identityFenceFor(record.id, childState);
      if (snapshotIdentityIsBlocked(childState, fence)) {
        next.delete(record.id);
        continue;
      }
      if ((fence?.mode === "awaiting" || fence?.mode === "terminal")
        && lifecycleFenceMatches(fence.identity, recordIdentity)) {
        // A metadata-less start or terminal event cannot make this delayed old
        // snapshot a fresh run. Keep the fence for the next heartbeat.
        next.delete(record.id);
        continue;
      }
      if (fence?.mode === "expected" && !lifecycleFenceMatches(fence.identity, recordIdentity)) {
        // This is an older snapshot for a reused ID. Keep the scheduler fence,
        // but remove the old record from the activity projection.
        next.delete(record.id);
        continue;
      }
      const existing = previous.get(record.id);
      next.set(record.id, {
        ...record,
        eventTracked: false,
        serviceTracked: true,
        startedAtMs: existing?.startedAtMs ?? nowMs,
        lastSeenMs: nowMs,
      });
      if (childState !== undefined) {
        if (!previous.has(record.id)) schedulerChildStarted(record.id, { force: true });
        childState.summary = record;
        childState.startFence = undefined;
        childState.awaitingIdentity = false;
        childState.terminalObserved = false;
      } else {
        schedulerChildStarted(record.id);
        const state = settledNotificationChildStates.get(record.id);
        if (state !== undefined) {
          state.summary = record;
          state.lastSnapshot = sourceRecord;
        }
      }
      settledNotificationIdentityFences.delete(record.id);
    }

    for (const id of settledNotificationChildIds) {
      if (next.has(id)) continue;
      const childState = settledNotificationChildStates.get(id);
      const fence = identityFenceFor(id, childState);
      const retainsOwnership = fence?.mode === "expected" || fence?.mode === "awaiting";
      if (!authoritative || retainsOwnership) {
        // A partial read still leaves local activity records usable. A reuse
        // fence keeps scheduler ownership but its old activity record stays out.
        continue;
      }
      schedulerChildDisposed(id);
    }

    // A capped typed snapshot is partial, so its omissions cannot dispose
    // scheduler children. Evict only the oldest local activity records when
    // churn would otherwise grow the projection without changing ownership.
    while (next.size > MAX_ACTIVITY_SUBAGENTS) {
      const oldest = next.keys().next().value;
      // The map is keyed by strings and the loop runs only while it is nonempty.
      /* c8 ignore next */
      if (typeof oldest !== "string") break;
      next.delete(oldest);
    }

    let changed = next.size !== previous.size;
    for (const [id, metadata] of next) {
      if (!sameSubagentMetadata(previous.get(id), metadata)) changed = true;
      schedulerChildStarted(id);
    }
    runningSubagents.clear();
    for (const [id, metadata] of next) runningSubagents.set(id, metadata);
    return changed;
  }

  function lifecyclePublicSnapshot(): NativeSubagent[] {
    return [...runningSubagents.values()].slice(0, MAX_ACTIVITY_SUBAGENTS).map(({ id, type, description, status }) => ({
      id,
      // Typed lifecycle summaries always provide these observer-safe fields.
      /* c8 ignore next */
      ...(type === undefined ? {} : { type }),
      description,
      /* c8 ignore next */
      ...(status === undefined ? {} : { status }),
    }));
  }

  function reconcileNativeSubagentService(): { changed: boolean; snapshot?: NativeSubagent[] } {
    const service = getNativeService();
    const lifecycleSnapshotService = lifecycleSnapshotSubagentService(service);
    const source = nativeSubagentRecords(service);
    if (!Array.isArray(source)) return { changed: false };
    const nowMs = nowImpl();
    if (lifecycleSnapshotService !== undefined) {
      const authoritative = source.length < MAX_ACTIVITY_SUBAGENTS;
      const snapshot = typedLifecycleSnapshotRecords(source).slice(0, MAX_ACTIVITY_SUBAGENTS);
      const changed = reconcileTypedLifecycleSnapshot(snapshot, authoritative, nowMs);
      return { changed, snapshot: authoritative ? lifecyclePublicSnapshot() : undefined };
    }

    const serviceIds = new Set<string>();
    let changed = false;
    for (const record of source) {
      const metadata = normalizeSubagentMetadata(record, nativeSubagentStartedAtMs(record, nowMs));
      if (metadata === undefined) continue;
      // Older service shapes did not expose a typed lifecycle contract.
      if (lifecycleSnapshotService === undefined) delete metadata.status;
      serviceIds.add(metadata.id);
      if (isTerminalNativeSubagentRecord(record, service)) {
        if (rememberTerminalSubagent(metadata.id, nowMs)) changed = true;
        schedulerChildDisposed(metadata.id);
        continue;
      }
      if (!isActiveNativeSubagentRecord(record, service)) continue;
      if (isTerminalTombstonedNativeRecord(metadata.id, record)) {
        // Tombstoned active rows are removed when the tombstone is created.
        /* c8 ignore next */
        if (runningSubagents.delete(metadata.id)) changed = true;
        continue;
      }
      const existing = runningSubagents.get(metadata.id);
      const merged = mergeNativeSubagentMetadata(existing, metadata, nowMs);
      if (sameSubagentMetadata(existing, merged)) {
        // sameSubagentMetadata requires both records, so this guard is a type-safe
        // defensive branch that cannot follow a true comparison.
        /* c8 ignore next */
        if (existing === undefined) continue;
        existing.lastSeenMs = nowMs;
        existing.serviceTracked = true;
        continue;
      }
      runningSubagents.set(metadata.id, merged);
      changed = true;
    }
    if (enforceActivityRecordCap()) changed = true;
    for (const [id, metadata] of runningSubagents.entries()) {
      if (!serviceIds.has(id) && metadata?.serviceTracked === true && metadata?.eventTracked !== true) {
        runningSubagents.delete(id);
        changed = true;
      } else if (!serviceIds.has(id) && metadata?.eventTracked === true
        && nowMs - metadata.lastSeenMs >= EVENT_TRACKED_MISSING_LIMIT_MS) {
        runningSubagents.delete(id);
        schedulerChildDisposed(id);
        changed = true;
      }
    }
    // The first cap enforcement above leaves this map at or below the cap;
    // the second pass cannot evict another record without an intervening add.
    /* c8 ignore next */
    if (enforceActivityRecordCap()) changed = true;
    return { changed };
  }

  function pollNativeLifecycleSnapshot(): boolean {
    if (lifecycleSnapshotSubagentService() === undefined) {
      trace("extension_snapshot_poll_skipped", { reason: "lifecycle_service_unavailable" });
      safeDiagnostic(diagnostic, "activity_gate_rejected", { reason: "lifecycle_service_unavailable", state: "snapshot" });
      return false;
    }
    const { changed, snapshot } = reconcileNativeSubagentService();
    if (snapshot === undefined) {
      trace("extension_snapshot_poll_failed", { reason: "snapshot_unavailable" });
      safeDiagnostic(diagnostic, "activity_gate_rejected", { reason: "snapshot_unavailable", state: "snapshot" });
      return false;
    }
    trace("extension_snapshot_reconciled", {
      state: changed ? "changed" : "unchanged", snapshot_count: snapshot.length, subagents: summarizeNativeSubagents(snapshot),
    });
    // Complete snapshots are the sole typed lifecycle publication path. Native
    // start/terminal events update suppression only; polling owns router state.
    publishLifecycleSnapshot(snapshot);
    return changed;
  }

  function parentActivityStateFromPiContext(): string | undefined {
    // Compaction masks ordinary work, Pi's live idle bit wins when available, and
    // the latch is only the older-context fallback.
    if (isCompacting()) return "compacting";
    const context = getParentContext();
    if (typeof context?.isIdle === "function") return context.isIdle() ? "settled" : "active";
    return undefined;
  }

  function parentActivityStateFromContext(): string {
    return parentActivityStateFromPiContext() ?? (activityHeartbeatActive ? "active" : "settled");
  }

  function effectiveGoalProjection(): ActivityGoalProjection | undefined {
    return getGoalProjection();
  }

  function sendActivityState(state: string, { force = false, reconcile = true }: { force?: boolean; reconcile?: boolean } = {}): boolean {
    // Compaction owns the visible state until its lifecycle resumes; ordinary
    // frames must not clear that pause early.
    // parentActivityStateFromContext() derives "compacting" from this same latch.
    /* c8 ignore next */
    if (isCompacting() && state !== "compacting") {
      safeDiagnostic(diagnostic, "activity_gate_rejected", { reason: "compaction_pending", state: "compacting" });
      return false;
    }
    const nowMs = nowImpl();
    // Native lifecycle state changes only in the timer-driven snapshot poll. Activity
    // events must not produce lifecycle frames or read the typed lifecycle projection.
    const reconciliation = reconcile && lifecycleSnapshotSubagentService() === undefined
      ? reconcileNativeSubagentService()
      : undefined;
    const reconciled = reconciliation?.changed === true;
    const subagents = publicSubagentSummaries();
    const subagentCount = Math.min(runningSubagents.size, MAX_ACTIVITY_SUBAGENTS);
    const currentModel = getCurrentModel();
    const goal = effectiveGoalProjection();
    const unchanged = lastActivitySent !== undefined
      && lastActivitySent.state === state
      && lastActivitySent.subagentCount === subagentCount
      && JSON.stringify(lastActivitySent.subagents) === JSON.stringify(subagents)
      && JSON.stringify(lastActivitySent.goal) === JSON.stringify(goal)
      && JSON.stringify(lastActivitySent.currentModel) === JSON.stringify(currentModel);
    // Settled with no children is terminal until reconciliation changes it; it does
    // not need a periodic refresh frame.
    if (unchanged && state === "settled" && subagentCount === 0 && !force && !reconciled) {
      safeDiagnostic(diagnostic, "activity_gate_rejected", { reason: "unchanged_settled", state });
      return false;
    }
    if (unchanged && lastActivitySent !== undefined && !force && !reconciled && nowMs - lastActivitySent.sentAtMs < ACTIVITY_REFRESH_MIN_MS) {
      safeDiagnostic(diagnostic, "activity_gate_rejected", { reason: "refresh_throttled", state });
      return false;
    }
    try {
      getClient()?.sendActivity?.(state, {
        subagentCount,
        subagents,
        ...(goal === undefined ? {} : { goal }),
        currentModel,
      });
      lastActivitySent = {
        state,
        subagentCount,
        subagents,
        goal,
        currentModel,
        sentAtMs: nowMs,
      };
      return true;
    } catch (error) {
      safeDiagnostic(diagnostic, "activity_publish_failed", {
        reason: "activity_publish",
        state,
        published: false,
        ...(contextSessionId(getParentContext()) === undefined ? {} : { session_id: contextSessionId(getParentContext()) }),
        error_name: errorName(error),
      });
      // Activity is an ephemeral UI hint; mailbox delivery owns durable work.
      return false;
    }
  }

  function ensureHeartbeat(): void {
    if (nativeEventsBlocked || activityHeartbeatTimer !== undefined) return;
    // Keep polling after the parent settles so missed native transitions converge.
    const heartbeatGeneration = lifecycleGeneration;
    activityHeartbeatTimer = setIntervalImpl(() => {
      // A callback may already be queued when session shutdown clears the interval.
      if (nativeEventsBlocked || heartbeatGeneration !== lifecycleGeneration) return;
      // This is the only path that reads and publishes the native lifecycle projection.
      let changed = false;
      let state = "settled";
      let activitySent = false;
      try {
        changed = pollNativeLifecycleSnapshot();
        state = parentActivityStateFromContext();
        activitySent = sendActivityState(state);
      } catch (error) {
        safeDiagnostic(diagnostic, "lifecycle_heartbeat_failed", { reason: "heartbeat_error", state, error_name: errorName(error) });
      }
      trace("extension_lifecycle_heartbeat", {
        state, subagent_count: runningSubagents.size, published: activitySent, available: changed,
        heartbeat_interval_ms: ACTIVITY_HEARTBEAT_INTERVAL_MS,
      });
    }, ACTIVITY_HEARTBEAT_INTERVAL_MS);
    activityHeartbeatTimer?.unref?.();
  }

  function clearHeartbeat(): void {
    if (activityHeartbeatTimer !== undefined) {
      clearIntervalImpl(activityHeartbeatTimer);
      activityHeartbeatTimer = undefined;
    }
  }

  function stopHeartbeat({ settle = false, context }: { settle?: boolean; context?: ActivityContext } = {}): void {
    remember(context);
    if (settle && parentActivityStateFromPiContext() === "active") {
      // A settle event can race a queued continuation. Keep polling when Pi still
      // reports active so a stale settled frame cannot mask work.
      activityHeartbeatActive = true;
      ensureHeartbeat();
      return;
    }
    const wasActive = activityHeartbeatActive;
    activityHeartbeatActive = false;
    if (wasActive && settle && !isCompacting()) sendActivityState(parentActivityStateFromContext(), { force: true });
    // Polling continues while settled or waiting for native child results.
  }

  function clearRunningSubagentsForBoundary(): void {
    runningSubagents.clear();
    activityHeartbeatActive = false;
    if (!isCompacting()) sendActivityState("settled", { force: true, reconcile: false });
    clearHeartbeat();
  }

  function startHeartbeat(context: ActivityContext): void {
    if (nativeEventsBlocked) return;
    remember(context);
    activityHeartbeatActive = true;
    sendActivityState(parentActivityStateFromContext(), { force: true });
    ensureHeartbeat();
  }

  function refresh(context: ActivityContext | undefined = undefined): void {
    if (nativeEventsBlocked) return;
    remember(context);
    sendActivityState(parentActivityStateFromContext(), { force: true });
    if (runningSubagents.size > 0) ensureHeartbeat();
    else if (!activityHeartbeatActive) stopHeartbeat();
  }

  function onNativeStarted(event: NativeSubagent): void {
    if (nativeEventsBlocked) return;
    const service = getNativeService();
    // Snapshot mode only updates settled-notification suppression. The next timer
    // poll remains authoritative for both local projection and lifecycle frames.
    if (lifecycleSnapshotSubagentService(service) !== undefined) {
      trace("extension_native_event_started", { subagent_id: event?.id, source: "event_snapshot_mode" });
      const id = typeof event?.id === "string" ? event.id.trim().slice(0, 128) : undefined;
      if (id !== undefined && id.length > 0) {
        const existing = settledNotificationChildStates.get(id);
        const savedFence = identityFenceFor(id, existing);
        const identity = lifecycleIdentity(event);
        const currentIdentity = existing?.startFence ?? (existing?.summary === undefined ? savedFence?.identity : lifecycleIdentity(existing.summary));
        const hasIdentity = hasLifecycleIdentity(identity);
        const isSameAwaitedIdentity = (savedFence?.mode === "awaiting" || savedFence?.mode === "terminal")
          && sameLifecycleIdentity(savedFence.identity, identity);
        const isNewRun = existing === undefined || existing.terminalObserved === true
          || (existing !== undefined && !hasIdentity)
          || (existing?.awaitingIdentity === true && existing.summary === undefined && hasIdentity)
          || (currentIdentity !== undefined && hasIdentity && !sameLifecycleIdentity(currentIdentity, identity));
        const fenceOptions = isSameAwaitedIdentity
          ? { awaitingIdentity: true }
          : isNewRun
            ? {
              ...(hasIdentity ? { fence: identity } : {}),
              ...(isNewRun && !hasIdentity ? { awaitingIdentity: true } : {}),
            }
            : {};
        schedulerChildStarted(id, fenceOptions);
        const state = settledNotificationChildStates.get(id);
        if (state !== undefined && (savedFence?.mode === "awaiting" || savedFence?.mode === "terminal")
          && state.summary === undefined && (!hasIdentity || isSameAwaitedIdentity)) {
          state.summary = summaryFromIdentity(id, savedFence.identity);
        }
        if (existing !== undefined) {
          existing.terminalObserved = false;
          // Preserve the event callback's existing observable call even though
          // the scheduler itself ignores duplicate child starts.
          settledNotifications.onChildStarted(id);
        }
      }
      return;
    }
    const metadata = normalizeSubagentMetadata(event);
    clearTerminalSubagentTombstone(metadata?.id);
    const existing = metadata === undefined ? undefined : runningSubagents.get(metadata.id);
    const tracked = metadata === undefined ? undefined : {
      ...metadata,
      eventTracked: true,
      serviceTracked: existing?.serviceTracked === true,
      startedAtMs: existing?.startedAtMs ?? metadata.startedAtMs,
    };
    const changed = tracked !== undefined && !sameSubagentMetadata(existing, tracked);
    if (changed && metadata !== undefined) runningSubagents.set(metadata.id, tracked);
    else if (existing !== undefined) existing.eventTracked = true;
    const capped = enforceActivityRecordCap();
    schedulerChildStarted(metadata?.id ?? event.id);
    if (changed || capped) refresh();
  }

  function onNativeTerminal(event: NativeSubagent): void {
    if (nativeEventsBlocked) return;
    const service = getNativeService();
    if (lifecycleSnapshotSubagentService(service) !== undefined) {
      trace("extension_native_event_terminal", { subagent_id: event?.id, status: event?.status, source: "event_snapshot_mode" });
      // Bare terminal events are intentionally ignored here. They cannot prove
      // which run an ID refers to after reuse; snapshot omission handles them.
      const id = typeof event?.id === "string" ? event.id.trim().slice(0, 128) : undefined;
      if (id !== undefined && id.length > 0) {
        const state = settledNotificationChildStates.get(id);
        const fence = identityFenceFor(id, state);
        const expected = state?.startFence
          ?? (state?.summary === undefined ? fence?.identity : lifecycleIdentity(state.summary));
        const observed = lifecycleIdentity(event);
        // A terminal event without identity cannot select a run after ID reuse.
        // Snapshot omission remains the only safe terminal evidence in that case.
        if (hasLifecycleIdentity(observed) && expected !== undefined && lifecycleFenceMatches(expected, observed)) {
          if (state !== undefined) state.terminalObserved = true;
          else rememberIdentityFence(id, { identity: observed, mode: "terminal" });
        }
      }
      return;
    }
    const id = event?.id;
    const changed = rememberTerminalSubagent(id);
    schedulerChildDisposed(id);
    if (changed) refresh();
  }

  let lastCompactionClearedCount = 0;

  function onCompactionStarted(_context: ActivityContext): void {
    lastCompactionClearedCount = runningSubagents.size;
    clearRunningSubagentsForBoundary();
    safeDiagnostic(diagnostic, "compaction_activity_cleared", {
      cleared_count: lastCompactionClearedCount,
      remaining_count: runningSubagents.size,
    });
    sendActivityState("compacting", { force: true, reconcile: false });
  }

  function onCompactionResumed(_context: ActivityContext): void {
    if (nativeEventsBlocked) return;
    // Reconcile scheduler ownership before refreshing activity. The projection
    // was cleared at compaction start, so a typed snapshot omission otherwise
    // leaves settled notifications suppressed until the next timer tick.
    const reconciliation = reconcileSettledNotificationChildren();
    safeDiagnostic(diagnostic, "compaction_activity_reconciled", {
      cleared_count: lastCompactionClearedCount,
      source_count: reconciliation.sourceCount,
      started_count: reconciliation.startedCount,
      disposed_count: reconciliation.disposedCount,
      reconciled_count: reconciliation.trackedCount,
      remaining_count: runningSubagents.size,
    });
    refresh();
    ensureHeartbeat();
  }

  function onSessionBoundary(): void {
    // Advance the generation before clearing the timer. A callback already queued
    // by the old session must be fenced even if the timer clear cannot retract it.
    lifecycleGeneration += 1;
    nativeEventsBlocked = true;
    // Compaction clears transient projections and timers. Tombstones intentionally
    // survive this boundary so stale native rows remain fenced. The root owns
    // scheduler disposal, so clear only this lifecycle's ownership ledger here.
    clearRunningSubagentsForBoundary();
    for (const [id, state] of settledNotificationChildStates) {
      const fence = stateIdentityFence(state);
      if (fence !== undefined) rememberIdentityFence(id, fence);
    }
    settledNotificationChildStates.clear();
    settledNotificationChildIds.clear();
    // Identity-only fences survive the session boundary. A delayed typed
    // snapshot from the old session must not become the next session's child.
  }

  function onSessionStart(): void {
    // The extension instance survives /new and /resume. Reactivate this lifecycle
    // and discard the previous client's last-frame cache before its new heartbeat.
    lifecycleGeneration += 1;
    nativeEventsBlocked = false;
    lastActivitySent = undefined;
  }

  return {
    settledNotifications,
    getNativeService,
    onAgentStart: startHeartbeat,
    onAgentSettled: (context: ActivityContext) => stopHeartbeat({ settle: true, context }),
    onNativeStarted,
    onNativeTerminal,
    onCompactionStarted,
    onCompactionResumed,
    refresh,
    ensureHeartbeat,
    onSessionBoundary,
    onSessionStart,
  };
}

export type ActivityLifecycle = {
  settledNotifications: SettledNotificationScheduler;
  getNativeService: (ownerSessionId?: string) => NativeSubagentService | undefined;
  onAgentStart: (context: ActivityContext) => void;
  onAgentSettled: (context: ActivityContext) => void;
  onNativeStarted: (event: NativeSubagent) => void;
  onNativeTerminal: (event: NativeSubagent) => void;
  onCompactionStarted: (context: ActivityContext) => void;
  onCompactionResumed: (context: ActivityContext) => void;
  refresh: (context?: ActivityContext) => void;
  ensureHeartbeat: () => void;
  onSessionBoundary: () => void;
  onSessionStart: () => void;
};
