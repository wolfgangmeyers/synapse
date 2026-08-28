const REMINDER_INCARNATION = Symbol("reminder-incarnation");

type Reminder = Record<string, unknown> & { [REMINDER_INCARNATION]?: unknown };
type ReminderTarget = { agentId: string; instanceId: string; leaseId: string };
type ReminderControlRequest = {
  reminderId: string;
  action: "set" | "clear";
  targetAgentId: string;
  targetInstanceId: string;
  leaseId: string;
  expiresAt: string;
  isCurrent: () => boolean;
};
type ReminderLock = {
  targetAgentId: string;
  targetInstanceId: string;
  leaseId: string;
  identity: string;
  incarnation?: string;
  reminder: Reminder;
};
type ReminderReader = (reminderId: string) => Promise<Reminder | undefined> | Reminder | undefined;
type ReminderLockOptions = {
  getCurrentReminder?: ReminderReader;
  getCurrentTarget?: () => ReminderTarget | undefined;
  now?: () => number;
};
type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;
type ReminderLockResult = { locked: boolean; errorCode?: string };

const REMINDER_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$/;
const REMINDER_INCARNATION_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

function reminderIncarnation(reminder: Reminder | null | undefined): string | undefined {
  const value = reminder?.[REMINDER_INCARNATION];
  return typeof value === "string" && REMINDER_INCARNATION_PATTERN.test(value) ? value : undefined;
}

export function createRouterReminderReader({
  fetchImpl = globalThis.fetch,
  routerUrl = "http://127.0.0.1:8765",
}: { fetchImpl?: FetchLike; routerUrl?: string } = {}): ReminderReader {
  return async (reminderId: string) => {
    if (typeof fetchImpl !== "function") return undefined;
    const response = await fetchImpl(`${routerUrl.replace(/\/$/, "")}/v1/reminders/${encodeURIComponent(reminderId)}`, { signal: AbortSignal.timeout(5_000) });
    if (!response.ok) return undefined;
    const payload = await response.json();
    const reminder = isRecord(payload) && isRecord(payload.reminder) ? payload.reminder : undefined;
    if (reminder === undefined) return undefined;
    const incarnation = response.headers?.get?.("x-reminder-incarnation");
    if (typeof incarnation === "string" && REMINDER_INCARNATION_PATTERN.test(incarnation)) {
      Object.defineProperty(reminder, REMINDER_INCARNATION, { value: incarnation });
    }
    return reminder?.id === reminderId ? reminder : undefined;
  };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function validString(value: unknown, maximum = 128): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= maximum;
}

function isReminderControlRequest(value: unknown): value is ReminderControlRequest {
  if (!isRecord(value) || !validString(value.reminderId, 99) || !REMINDER_ID.test(value.reminderId)
    || (value.action !== "set" && value.action !== "clear")
    || !validString(value.targetAgentId) || !validString(value.targetInstanceId)
    || !validString(value.leaseId) || !validString(value.expiresAt, 64)
    || typeof value.isCurrent !== "function") return false;
  return true;
}

function reminderIdentity(reminder: Reminder | null | undefined): string | undefined {
  if (reminder === null || typeof reminder !== "object" || Array.isArray(reminder)
    || !validString(reminder.id, 99) || !REMINDER_ID.test(reminder.id)
    || !validString(reminder.creator) || !validString(reminder.created_at, 64)) return undefined;
  return `${reminder.id}\u0000${reminder.creator}\u0000${reminder.created_at}`;
}

export function createReminderLockState({
  getCurrentReminder,
  getCurrentTarget,
  now = Date.now,
}: ReminderLockOptions): {
  clear: (reminderId?: string) => void;
  control: (request: unknown) => Promise<{ accepted: boolean; errorCode?: string }>;
  guard: (reminderId: unknown, reminder?: Reminder) => Promise<ReminderLockResult>;
  size: () => number;
  has: (reminderId: string) => boolean;
} {
  if (typeof getCurrentReminder !== "function") throw new TypeError("getCurrentReminder is required");
  if (typeof getCurrentTarget !== "function") throw new TypeError("getCurrentTarget is required");
  if (typeof now !== "function") throw new TypeError("now must be a function");
  const readReminder = getCurrentReminder;
  const readTarget = getCurrentTarget;

  // Reminder IDs can be reused after deletion. The identity token uses the ID, creator, and creation timestamp;
  // target is checked separately so target changes invalidate the lock.
  // The router validates role, request bounds, ownership, session/lease, pending correlation, and expiry, then relays controls.
  // This extension enforces the update/delete lock.
  const locks = new Map<string, ReminderLock>();

  function clear(reminderId?: string): void {
    if (reminderId === undefined) locks.clear();
    else locks.delete(reminderId);
  }

  function currentTarget(targetAgentId: string, targetInstanceId: string, leaseId: string): boolean {
    try {
      const target = readTarget();
      return target?.agentId === targetAgentId
        && target?.instanceId === targetInstanceId
        && target?.leaseId === leaseId;
    } catch {
      return false;
    }
  }

  async function control(request: unknown) {
    if (request === null || typeof request !== "object" || Array.isArray(request)) {
      return { accepted: false, errorCode: "invalid_control" };
    }
    if (!isReminderControlRequest(request)) return { accepted: false, errorCode: "invalid_control" };
    const { reminderId, action, targetAgentId, targetInstanceId, leaseId, expiresAt: expiresAtText, isCurrent } = request;
    const expiresAt = Date.parse(expiresAtText);
    if (!Number.isFinite(expiresAt) || expiresAt <= now()) {
      return { accepted: false, errorCode: "expired" };
    }
    if (!currentTarget(targetAgentId, targetInstanceId, leaseId)
      || !isCurrent()) {
      return { accepted: false, errorCode: "stale_target" };
    }

    let reminder: Reminder | undefined;
    try {
      reminder = await readReminder(reminderId);
    } catch {
      return { accepted: false, errorCode: "reminder_unavailable" };
    }
    const identity = reminderIdentity(reminder);
    if (identity === undefined || reminder === undefined || reminder.target !== targetAgentId) {
      locks.delete(reminderId);
      return { accepted: false, errorCode: "reminder_not_found" };
    }

    // Lease and expiry are checked again immediately before the map mutation.
    if (Date.parse(expiresAtText) <= now()
      || !currentTarget(targetAgentId, targetInstanceId, leaseId)
      || !isCurrent()) {
      return { accepted: false, errorCode: "stale_target" };
    }
    if (action === "set") {
      locks.set(reminderId, {
        targetAgentId,
        targetInstanceId,
        leaseId,
        identity,
        incarnation: reminderIncarnation(reminder),
        reminder,
      });
    } else {
      locks.delete(reminderId);
    }
    return { accepted: true };
  }

  async function guard(reminderId: unknown, reminder: Reminder | undefined = undefined): Promise<ReminderLockResult> {
    if (!validString(reminderId, 99) || !REMINDER_ID.test(reminderId)) return { locked: false };
    const lock = locks.get(reminderId);
    if (lock === undefined) return { locked: false };
    let current: Reminder | undefined;
    try {
      current = reminder ?? await readReminder(reminderId);
    } catch {
      locks.delete(reminderId);
      return { locked: false };
    }
    const identity = reminderIdentity(current);
    let target;
    try {
      target = readTarget();
    } catch {
      locks.delete(reminderId);
      return { locked: false };
    }
    // Discard stale identity/target/lease/incarnation entries lazily on the next native guard;
    // fail open to the existing mutation when continuity is uncertain. The object reference is
    // the direct in-process fallback when the router metadata header is unavailable in a fixture.
    const incarnationMatches = lock.incarnation === undefined
      ? current === lock.reminder
      : reminderIncarnation(current) === lock.incarnation;
    if (identity === undefined || current === undefined || identity !== lock.identity || !incarnationMatches
      || current.target !== lock.targetAgentId || target?.agentId !== lock.targetAgentId
      || target?.instanceId !== lock.targetInstanceId || target?.leaseId !== lock.leaseId) {
      locks.delete(reminderId);
      return { locked: false };
    }
    return { locked: true, errorCode: "reminder_locked" };
  }

  return {
    clear,
    control,
    guard,
    size: () => locks.size,
    has: (reminderId: string) => locks.has(reminderId),
  };
}

export function lockedReminderResult(): {
  content: [{ type: "text"; text: string }];
  details: { error_code: string };
  isError: boolean;
} {
  return {
    content: [{ type: "text", text: "reminder_locked" }],
    details: { error_code: "reminder_locked" },
    isError: true,
  };
}
