type TimerHandle = ReturnType<typeof setTimeout>;
type RecallSelection = { ids?: unknown[] };
type RecallControllerOptions = {
  evaluate: (active: () => boolean) => RecallSelection | Promise<RecallSelection>;
  recall: (selection: RecallSelection, active: () => boolean) => void | Promise<void>;
  cancel?: () => void;
  inactivityMs?: number;
  maximumWindowMs?: number;
  setTimer?: (callback: () => void, delay: number) => TimerHandle;
  clearTimer?: (timer: TimerHandle) => void;
  now?: () => number;
};

export function createRecallController({
  evaluate,
  recall,
  cancel = () => {},
  inactivityMs = 30_000,
  maximumWindowMs = 120_000,
  setTimer = setTimeout,
  clearTimer = clearTimeout,
  now = () => Date.now(),
}: RecallControllerOptions) {
  let timer: TimerHandle | undefined;
  let deadlineTimer: TimerHandle | undefined;
  let deadline: number | undefined;
  let cooling = false;
  let followUp = false;
  let followUpDeadline: number | undefined;
  let stopped = false;
  let canceled = false;
  let generation = 0;
  const clearDeadline = () => {
    if (deadlineTimer !== undefined) clearTimer(deadlineTimer);
    deadlineTimer = undefined;
    deadline = undefined;
  };
  const abort = () => {
    if (!canceled) {
      canceled = true;
      cancel();
    }
  };
  const active = (currentGeneration: number) =>
    !stopped && generation === currentGeneration && deadline !== undefined && now() < deadline;
  const finish = () => {
    if (timer !== undefined) clearTimer(timer);
    timer = undefined;
    clearDeadline();
  };
  const schedule = () => {
    if (cooling) {
      if (!followUp) {
        followUp = true;
        followUpDeadline = now() + maximumWindowMs;
      }
      return;
    }
    stopped = false;
    canceled = false;
    const currentGeneration = generation;
    if (timer !== undefined) clearTimer(timer);
    const started = now();
    deadline ??= started + maximumWindowMs;
    if (deadlineTimer !== undefined) clearTimer(deadlineTimer);
    deadlineTimer = setTimer(() => {
      if (generation !== currentGeneration || deadline === undefined || now() < deadline) return;
      if (cooling) {
        generation += 1;
        abort();
      }
      finish();
    }, Math.max(0, deadline - started));
    const delay = Math.max(0, Math.min(inactivityMs, deadline - started));
    timer = setTimer(async () => {
      timer = undefined;
      if (!active(currentGeneration) || cooling) {
        if (!cooling) finish();
        return;
      }
      cooling = true;
      try {
        const selection = await evaluate(() => active(currentGeneration));
        if (active(currentGeneration) && selection?.ids?.length) {
          await recall(selection, () => active(currentGeneration));
        }
      } catch {
        // Recall is best effort and must not interrupt the parent turn.
      } finally {
        cooling = false;
        finish();
        if (followUp && !stopped) {
          followUp = false;
          deadline = followUpDeadline;
          followUpDeadline = undefined;
          schedule();
        }
      }
    }, delay);
  };
  return {
    configure({ inactivity_seconds, maximum_window_seconds }: {
      inactivity_seconds?: number;
      maximum_window_seconds?: number;
    }) {
      if (typeof inactivity_seconds === "number" && Number.isSafeInteger(inactivity_seconds) && inactivity_seconds > 0) inactivityMs = inactivity_seconds * 1000;
      if (typeof maximum_window_seconds === "number" && Number.isSafeInteger(maximum_window_seconds) && maximum_window_seconds > 0) maximumWindowMs = maximum_window_seconds * 1000;
    },
    activity: schedule,
    stop() {
      if (stopped) return;
      stopped = true;
      generation += 1;
      abort();
      if (timer !== undefined) clearTimer(timer);
      timer = undefined;
      clearDeadline();
      followUp = false;
      followUpDeadline = undefined;
    },
    status() { return { pending: timer !== undefined, cooling }; },
  };
}
