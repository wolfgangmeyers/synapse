import { useEffect, useState } from "react";

export interface CountdownOptions { now?: () => number; intervalMs?: number }
export function remainingMilliseconds(nextDueAt: string | number | undefined, now = Date.now()): number {
  const due = typeof nextDueAt === "number" ? nextDueAt : Date.parse(nextDueAt ?? "");
  return Number.isFinite(due) ? Math.max(0, due - now) : 0;
}
export function useCountdown(nextDueAt: string | number | undefined, options: CountdownOptions = {}): number {
  const clock = options.now ?? Date.now; const intervalMs = options.intervalMs ?? 1_000;
  const [remaining, setRemaining] = useState(() => remainingMilliseconds(nextDueAt, clock()));
  useEffect(() => { const update = () => setRemaining(remainingMilliseconds(nextDueAt, clock())); update(); const timer = setInterval(update, intervalMs); return () => clearInterval(timer); }, [nextDueAt, clock, intervalMs]);
  return remaining;
}
