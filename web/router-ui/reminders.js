const SECOND_MS = 1000;
const MINUTE_SECONDS = 60;
const HOUR_SECONDS = 60 * MINUTE_SECONDS;
const DAY_SECONDS = 24 * HOUR_SECONDS;

export function formatReminderCountdown(nextDueAt, now = Date.now()) {
  const dueAtMs = typeof nextDueAt === "number" ? nextDueAt : Date.parse(nextDueAt || "");
  if (!Number.isFinite(dueAtMs)) return "Due time unavailable";
  const remainingSeconds = Math.max(0, Math.ceil((dueAtMs - now) / SECOND_MS));
  if (remainingSeconds === 0) return "Due now";

  let seconds = remainingSeconds;
  const days = Math.floor(seconds / DAY_SECONDS);
  seconds -= days * DAY_SECONDS;
  const hours = Math.floor(seconds / HOUR_SECONDS);
  seconds -= hours * HOUR_SECONDS;
  const minutes = Math.floor(seconds / MINUTE_SECONDS);
  seconds -= minutes * MINUTE_SECONDS;
  const parts = [];
  if (days > 0) parts.push(`${days}d`);
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0) parts.push(`${minutes}m`);
  if (seconds > 0) parts.push(`${seconds}s`);
  return `Due in ${parts.join(" ")}`;
}

export function reminderRecurrenceLabel(intervalMs) {
  const interval = Number(intervalMs);
  return Number.isFinite(interval) && interval > 0 ? "Recurring" : "One-shot";
}
