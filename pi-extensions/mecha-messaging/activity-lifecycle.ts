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

  function invalidate() {
    generation += 1;
    for (const timer of timers) clearTimeoutImpl(timer);
    timers.clear();
  }

  function schedule(delay: number, expectedGeneration: number, send: () => void): void {
    let timer: Timer;
    timer = setTimeoutImpl(() => {
      timers.delete(timer);
      if (generation !== expectedGeneration || children.size > 0) return;
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
      children.add(sessionId);
      invalidate();
    },
    onChildDisposed(sessionId: string): void {
      if (typeof sessionId !== "string" || sessionId.length === 0) return;
      children.delete(sessionId);
    },
    dispose() {
      invalidate();
      children.clear();
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
        ...(available ? { parent_session_id: ownerSessionId } : {}),
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

  function lifecycleSubagentSummary(metadata: TrackedSubagent): LifecycleSummary | undefined {
    const id = typeof metadata?.id === "string" ? metadata.id.trim().slice(0, 128) : "";
    if (id.length === 0) return undefined;
    const type = typeof metadata?.type === "string" ? metadata.type.trim().slice(0, 64) : "";
    const description = typeof metadata?.description === "string" ? metadata.description.trim().slice(0, 512) : "";
    const status = typeof metadata.status === "string" && ["queued", "running", "completed", "steered", "aborted", "stopped", "error"].includes(metadata.status)
      ? metadata.status
      : undefined;
    return {
      id,
      ...(type.length > 0 ? { type } : {}),
      // Native callers supply this short label specifically for observers. Keep a
      // stable fallback only for older event shapes that did not carry one.
      description: description.length > 0 ? description : "Native subagent",
      ...(status === undefined ? {} : { status }),
    };
  }

  function lifecycleSubagentSummaries(): LifecycleSummary[] {
    return [...runningSubagents.values()]
      .sort((left, right) => left.startedAtMs - right.startedAtMs || left.id.localeCompare(right.id))
      .slice(0, MAX_ACTIVITY_SUBAGENTS)
      .map(lifecycleSubagentSummary)
      .filter((item): item is LifecycleSummary => item !== undefined);
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
      trace("extension_snapshot_publish_error", {
        snapshot_count: summary.length,
        subagents: summary,
        error_name: errorName(error),
      });
      // Lifecycle polling retries on the next tick after a best-effort client failure.
    }
  }

  function sameSubagentMetadata(left: TrackedSubagent | undefined, right: TrackedSubagent | undefined): boolean {
    return left !== undefined && right !== undefined && left.id === right.id && left.type === right.type && left.description === right.description && left.status === right.status;
  }

  function lifecycleSnapshotSubagentService(service: NativeSubagentService | undefined = getNativeService()): NativeSubagentService | undefined {
    return typeof service?.getLifecycleSnapshots === "function" ? service : undefined;
  }

  function reportNativeSubagentReadFailure(error: unknown): void {
    const normalizedError = error instanceof Error ? error : new Error(String(error));
    trace("extension_snapshot_read_error", { error_name: errorName(normalizedError) });
    const nowMs = nowImpl();
    if (lastNativeSubagentReadDiagnosticMs !== undefined && nowMs - lastNativeSubagentReadDiagnosticMs < 60_000) return;
    lastNativeSubagentReadDiagnosticMs = nowMs;
    try {
      notify("Native subagent service read failed; polling will retry.", "error");
    } catch {
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
    return lifecycleSnapshotSubagentService(service) !== undefined
      ? status !== undefined && LIVE_SUBAGENT_STATUSES.has(status)
      : [undefined, "running", "queued", "active"].includes(status);
  }

  function isTerminalNativeSubagentRecord(record: NativeSubagent, service: NativeSubagentService | undefined = getNativeService()): boolean {
    const status = nativeSubagentStatus(record);
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

  function replaceNativeLifecycleSnapshot(snapshot: LifecycleSummary[], nowMs: number): boolean {
    // Typed snapshots replace the local projection authoritatively; omission is
    // the only safe evidence that a typed child left the active set.
    const next = new Map<string, TrackedSubagent>();
    for (const record of snapshot) {
      const existing = runningSubagents.get(record.id);
      next.set(record.id, {
        ...record,
        eventTracked: false,
        serviceTracked: true,
        startedAtMs: existing?.startedAtMs ?? nowMs,
        lastSeenMs: nowMs,
      });
    }
    let changed = next.size !== runningSubagents.size;
    for (const [id, metadata] of next) {
      if (!sameSubagentMetadata(runningSubagents.get(id), metadata)) changed = true;
    }
    for (const id of runningSubagents.keys()) {
      if (!next.has(id)) settledNotifications.onChildDisposed(id);
    }
    for (const id of next.keys()) {
      if (!runningSubagents.has(id)) settledNotifications.onChildStarted(id);
    }
    runningSubagents.clear();
    for (const [id, metadata] of next) runningSubagents.set(id, metadata);
    return changed;
  }

  function reconcileNativeSubagentService(): { changed: boolean; snapshot?: NativeSubagent[] } {
    const service = getNativeService();
    const lifecycleSnapshotService = lifecycleSnapshotSubagentService(service);
    const source = nativeSubagentRecords(service);
    if (!Array.isArray(source)) return { changed: false };
    const nowMs = nowImpl();
    if (lifecycleSnapshotService !== undefined) {
      const snapshot = source
        .map(lifecycleSnapshotSummary)
        .filter((item): item is LifecycleSummary => item !== undefined)
        .slice(0, MAX_ACTIVITY_SUBAGENTS);
      return { changed: replaceNativeLifecycleSnapshot(snapshot, nowMs), snapshot };
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
        settledNotifications.onChildDisposed(metadata.id);
        continue;
      }
      if (!isActiveNativeSubagentRecord(record, service)) continue;
      if (isTerminalTombstonedNativeRecord(metadata.id, record)) {
        if (runningSubagents.delete(metadata.id)) changed = true;
        continue;
      }
      const existing = runningSubagents.get(metadata.id);
      const merged = mergeNativeSubagentMetadata(existing, metadata, nowMs);
      if (sameSubagentMetadata(existing, merged)) {
        if (existing === undefined) continue;
        existing.lastSeenMs = nowMs;
        existing.serviceTracked = true;
        continue;
      }
      runningSubagents.set(metadata.id, merged);
      changed = true;
    }
    for (const [id, metadata] of runningSubagents.entries()) {
      if (!serviceIds.has(id) && metadata?.serviceTracked === true && metadata?.eventTracked !== true) {
        runningSubagents.delete(id);
        changed = true;
      } else if (!serviceIds.has(id) && metadata?.eventTracked === true
        && nowMs - metadata.lastSeenMs >= EVENT_TRACKED_MISSING_LIMIT_MS) {
        runningSubagents.delete(id);
        settledNotifications.onChildDisposed(id);
        changed = true;
      }
    }
    return { changed };
  }

  function pollNativeLifecycleSnapshot(): boolean {
    if (lifecycleSnapshotSubagentService() === undefined) {
      trace("extension_snapshot_poll_skipped", { reason: "lifecycle_service_unavailable" });
      return false;
    }
    const { changed, snapshot } = reconcileNativeSubagentService();
    if (snapshot === undefined) {
      trace("extension_snapshot_poll_failed", { reason: "snapshot_unavailable" });
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
    if (isCompacting() && state !== "compacting") return false;
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
    if (unchanged && state === "settled" && subagentCount === 0 && !force && !reconciled) return false;
    if (unchanged && lastActivitySent !== undefined && !force && !reconciled && nowMs - lastActivitySent.sentAtMs < ACTIVITY_REFRESH_MIN_MS) return false;
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
    } catch {
      // Activity is an ephemeral UI hint; mailbox delivery owns durable work.
      return false;
    }
  }

  function ensureHeartbeat(): void {
    if (activityHeartbeatTimer !== undefined) return;
    // Keep polling after the parent settles so missed native transitions converge.
    activityHeartbeatTimer = setIntervalImpl(() => {
      // This is the only path that reads and publishes the native lifecycle projection.
      const changed = pollNativeLifecycleSnapshot();
      const state = parentActivityStateFromContext();
      const activitySent = sendActivityState(state);
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
    remember(context);
    activityHeartbeatActive = true;
    sendActivityState(parentActivityStateFromContext(), { force: true });
    ensureHeartbeat();
  }

  function refresh(context: ActivityContext | undefined = undefined): void {
    remember(context);
    sendActivityState(parentActivityStateFromContext(), { force: true });
    if (runningSubagents.size > 0) ensureHeartbeat();
    else if (!activityHeartbeatActive) stopHeartbeat();
  }

  function onNativeStarted(event: NativeSubagent): void {
    const service = getNativeService();
    // Snapshot mode only updates settled-notification suppression. The next timer
    // poll remains authoritative for both local projection and lifecycle frames.
    if (lifecycleSnapshotSubagentService(service) !== undefined) {
      trace("extension_native_event_started", { subagent_id: event?.id, source: "event_snapshot_mode" });
      if (event.id !== undefined) settledNotifications.onChildStarted(event.id);
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
    settledNotifications.onChildStarted(metadata?.id ?? event.id ?? "");
    if (changed) refresh();
  }

  function onNativeTerminal(event: NativeSubagent): void {
    const service = getNativeService();
    if (lifecycleSnapshotSubagentService(service) !== undefined) {
      trace("extension_native_event_terminal", { subagent_id: event?.id, status: event?.status, source: "event_snapshot_mode" });
      if (event.id !== undefined) settledNotifications.onChildDisposed(event.id);
      return;
    }
    const id = event?.id;
    const changed = rememberTerminalSubagent(id);
    if (id !== undefined) settledNotifications.onChildDisposed(id);
    if (changed) refresh();
  }

  function onCompactionStarted(_context: ActivityContext): void {
    clearRunningSubagentsForBoundary();
    sendActivityState("compacting", { force: true, reconcile: false });
  }

  function onCompactionResumed(_context: ActivityContext): void {
    refresh();
    ensureHeartbeat();
  }

  function onSessionBoundary(): void {
    // Compaction clears transient projections and timers. Tombstones intentionally
    // survive this boundary so stale native rows remain fenced.
    clearRunningSubagentsForBoundary();
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
};
