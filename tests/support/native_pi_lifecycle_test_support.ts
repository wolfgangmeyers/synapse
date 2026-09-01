import { existsSync, readFileSync } from "node:fs";
import { createHook } from "node:async_hooks";

export type JsonRecord = Record<string, unknown>;

type TimerKind = "timeout" | "interval";
type SocketLike = {
  readyState?: number;
  once?: (event: "close", listener: () => void) => unknown;
};
type ChildLike = {
  exitCode: number | null;
  signalCode: NodeJS.Signals | null;
  once?: (event: "exit", listener: () => void) => unknown;
};
type ResourceEntry = { label: string; stack: string };

export function isJsonRecord(value: unknown): value is JsonRecord {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

export function readJsonLines(path: string): JsonRecord[] {
  if (!existsSync(path)) return [];
  const content = readFileSync(path, "utf8").trim();
  if (content.length === 0) return [];
  return content.split("\n").map((line) => JSON.parse(line)).filter(isJsonRecord);
}

export function snapshotEnvironment(): NodeJS.ProcessEnv {
  return { ...process.env };
}

export function restoreEnvironment(environment: NodeJS.ProcessEnv): void {
  for (const key of Object.keys(process.env)) delete process.env[key];
  Object.assign(process.env, environment);
}

/**
 * Tracks resources created by the lifecycle test itself. Timers are registered
 * through the injected lifecycle timer functions, and sockets/processes are
 * registered at their creation sites. The async hook only adds process
 * resources that the installed native package creates internally. This keeps
 * Node's test-runner handles out of the assertion.
 */
export function trackTimeoutResources(): {
  activeCount: () => number;
  activeStacks: () => string[];
  trackTimer: (handle: unknown, kind: TimerKind, label?: string) => void;
  clearTimer: (handle: unknown) => void;
  trackSocket: (socket: SocketLike, label: string) => void;
  trackChild: (child: ChildLike, label: string) => void;
  activeResources: () => {
    timers: string[];
    intervals: string[];
    sockets: string[];
    childProcesses: string[];
  };
  disable: () => void;
} {
  const timers = new Map<unknown, ResourceEntry>();
  const intervals = new Map<unknown, ResourceEntry>();
  const sockets = new Map<SocketLike, ResourceEntry>();
  const children = new Map<ChildLike, ResourceEntry>();
  const asyncProcesses = new Map<number, ResourceEntry>();
  const hook = createHook({
    init(asyncId, type) {
      if (type === "PROCESSWRAP") asyncProcesses.set(asyncId, { label: `process:${asyncId}`, stack: new Error().stack ?? "" });
    },
    destroy(asyncId) { asyncProcesses.delete(asyncId); },
  });
  hook.enable();

  const timerMap = (kind: TimerKind) => kind === "timeout" ? timers : intervals;
  const activeResources = () => ({
    timers: [...timers.values()].map(({ label }) => label),
    intervals: [...intervals.values()].map(({ label }) => label),
    sockets: [...sockets.entries()].filter(([socket]) => socket.readyState !== 3).map(([, { label }]) => label),
    childProcesses: [
      ...[...children.entries()].filter(([child]) => child.exitCode === null && child.signalCode === null).map(([, { label }]) => label),
      ...asyncProcesses.values().map(({ label }) => label),
    ],
  });
  return {
    activeCount: () => timers.size + intervals.size,
    activeStacks: () => [...timers.values(), ...intervals.values()].map(({ stack }) => stack),
    trackTimer(handle: unknown, kind: TimerKind, label = kind) {
      timerMap(kind).set(handle, { label, stack: new Error().stack ?? "" });
    },
    clearTimer(handle: unknown) {
      timers.delete(handle); intervals.delete(handle);
    },
    trackSocket(socket: SocketLike, label: string) {
      if (socket.readyState === 3) return;
      sockets.set(socket, { label, stack: new Error().stack ?? "" });
      socket.once?.("close", () => sockets.delete(socket));
    },
    trackChild(child: ChildLike, label: string) {
      if (child.exitCode !== null || child.signalCode !== null) return;
      children.set(child, { label, stack: new Error().stack ?? "" });
      child.once?.("exit", () => children.delete(child));
    },
    activeResources,
    disable() { hook.disable(); },
  };
}
