const REMINDER_ID = /^[A-Za-z0-9][A-Za-z0-9._-]{0,98}$/;
const REMINDER_INCARNATION = Symbol("reminder-incarnation");
const REMINDER_INCARNATION_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$/;

function reminderIncarnation(reminder) {
  const value = reminder?.[REMINDER_INCARNATION];
  return typeof value === "string" && REMINDER_INCARNATION_PATTERN.test(value) ? value : undefined;
}

export function createRouterReminderReader({ fetchImpl = globalThis.fetch, routerUrl = "http://127.0.0.1:8765" } = {}) {
  return async (reminderId) => {
    if (typeof fetchImpl !== "function") return undefined;
    const response = await fetchImpl(`${routerUrl.replace(/\/$/, "")}/v1/reminders/${encodeURIComponent(reminderId)}`, { signal: AbortSignal.timeout(5_000) });
    if (!response.ok) return undefined;
    const payload = await response.json();
    const reminder = payload?.reminder;
    if (reminder === null || typeof reminder !== "object" || Array.isArray(reminder)) return undefined;
    const incarnation = response.headers?.get?.("x-reminder-incarnation");
    if (typeof incarnation === "string" && REMINDER_INCARNATION_PATTERN.test(incarnation)) {
      Object.defineProperty(reminder, REMINDER_INCARNATION, { value: incarnation });
    }
    return reminder?.id === reminderId ? reminder : undefined;
  };
}

function validString(value, maximum = 128) {
  return typeof value === "string" && value.length > 0 && value.length <= maximum;
}

function reminderIdentity(reminder) {
  if (reminder === null || typeof reminder !== "object" || Array.isArray(reminder)
    || !validString(reminder.id, 99) || !REMINDER_ID.test(reminder.id)
    || !validString(reminder.creator) || !validString(reminder.created_at, 64)) return undefined;
  return `${reminder.id}\u0000${reminder.creator}\u0000${reminder.created_at}`;
}

export function createReminderLockState({
  getCurrentReminder,
  getCurrentTarget,
  now = Date.now,
} = {}) {
  if (typeof getCurrentReminder !== "function") throw new TypeError("getCurrentReminder is required");
  if (typeof getCurrentTarget !== "function") throw new TypeError("getCurrentTarget is required");
  if (typeof now !== "function") throw new TypeError("now must be a function");

  // Reminder IDs can be reused after deletion. The identity token uses the ID, creator, and creation timestamp;
  // target is checked separately so target changes invalidate the lock.
  // The router validates role, request bounds, ownership, session/lease, pending correlation, and expiry, then relays controls.
  // This extension enforces the update/delete lock.
  const locks = new Map();

  function clear(reminderId) {
    if (reminderId === undefined) locks.clear();
    else locks.delete(reminderId);
  }

  function currentTarget(targetAgentId, targetInstanceId, leaseId) {
    try {
      const target = getCurrentTarget();
      return target?.agentId === targetAgentId
        && target?.instanceId === targetInstanceId
        && target?.leaseId === leaseId;
    } catch {
      return false;
    }
  }

  async function control(request) {
    if (request === null || typeof request !== "object" || Array.isArray(request)
      || !REMINDER_ID.test(request.reminderId ?? "")
      || !["set", "clear"].includes(request.action)
      || !validString(request.targetAgentId)
      || !validString(request.targetInstanceId)
      || !validString(request.leaseId)
      || !validString(request.expiresAt, 64)
      || typeof request.isCurrent !== "function") {
      return { accepted: false, errorCode: "invalid_control" };
    }
    const expiresAt = Date.parse(request.expiresAt);
    if (!Number.isFinite(expiresAt) || expiresAt <= now()) {
      return { accepted: false, errorCode: "expired" };
    }
    if (!currentTarget(request.targetAgentId, request.targetInstanceId, request.leaseId)
      || !request.isCurrent()) {
      return { accepted: false, errorCode: "stale_target" };
    }

    let reminder;
    try {
      reminder = await getCurrentReminder(request.reminderId);
    } catch {
      return { accepted: false, errorCode: "reminder_unavailable" };
    }
    const identity = reminderIdentity(reminder);
    if (identity === undefined || reminder.target !== request.targetAgentId) {
      locks.delete(request.reminderId);
      return { accepted: false, errorCode: "reminder_not_found" };
    }

    // Lease and expiry are checked again immediately before the map mutation.
    if (Date.parse(request.expiresAt) <= now()
      || !currentTarget(request.targetAgentId, request.targetInstanceId, request.leaseId)
      || !request.isCurrent()) {
      return { accepted: false, errorCode: "stale_target" };
    }
    if (request.action === "set") {
      locks.set(request.reminderId, {
        targetAgentId: request.targetAgentId,
        targetInstanceId: request.targetInstanceId,
        leaseId: request.leaseId,
        identity,
        incarnation: reminderIncarnation(reminder),
        reminder,
      });
    } else {
      locks.delete(request.reminderId);
    }
    return { accepted: true };
  }

  async function guard(reminderId, reminder = undefined) {
    if (!validString(reminderId, 99) || !REMINDER_ID.test(reminderId)) return { locked: false };
    const lock = locks.get(reminderId);
    if (lock === undefined) return { locked: false };
    let current;
    try {
      current = reminder ?? await getCurrentReminder(reminderId);
    } catch {
      locks.delete(reminderId);
      return { locked: false };
    }
    const identity = reminderIdentity(current);
    let target;
    try {
      target = getCurrentTarget();
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
    if (identity === undefined || identity !== lock.identity || !incarnationMatches
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
    has: (reminderId) => locks.has(reminderId),
  };
}

export function lockedReminderResult() {
  return {
    content: [{ type: "text", text: "reminder_locked" }],
    details: { error_code: "reminder_locked" },
    isError: true,
  };
}
