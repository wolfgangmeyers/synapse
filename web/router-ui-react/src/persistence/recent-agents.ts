import { SCOPE_SEPARATOR, type StorageAdapter } from "./settings-repository";

export const RECENT_AGENTS_KEY = "mecha.web.recentAgents";
export const RECENT_AGENTS_LIMIT = 10;
export const RECENT_AGENT_SCOPES_LIMIT = 20;

// Keep disconnected picks until bounded eviction so reconnecting agents retain their order.
export function normalizeRecentAgents(values: readonly unknown[]): string[] {
  const recent: string[] = [];
  for (const value of values) {
    if (typeof value !== "string" || !/^(tmux|router-managed):\S.*$/.test(value) || recent.includes(value)) continue;
    recent.push(value);
    if (recent.length === RECENT_AGENTS_LIMIT) break;
  }
  return recent;
}

function loadScopes(storage: StorageAdapter | undefined): Record<string, string[]> {
  try {
    const parsed: unknown = JSON.parse(storage?.getItem(RECENT_AGENTS_KEY) ?? "{}");
    // The old unscoped list has no trustworthy owner, so it cannot be migrated.
    if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) return {};
    const entries: [string, unknown][] = Object.entries(parsed);
    return Object.fromEntries(entries.flatMap<[string, string[]]>(([scope, values]) => {
      const parts = scope.split(SCOPE_SEPARATOR);
      return parts.length === 2 && parts.every(Boolean) && Array.isArray(values)
        ? [[scope, normalizeRecentAgents(values)]] : [];
    }).slice(-RECENT_AGENT_SCOPES_LIMIT));
  } catch {
    return {};
  }
}

export function loadRecentAgents(storage: StorageAdapter | undefined, scope: string): string[] {
  return loadScopes(storage)[scope] ?? [];
}

export function saveRecentAgents(storage: StorageAdapter | undefined, scope: string, recent: readonly string[]): void {
  try {
    const scopes = loadScopes(storage);
    // Insertion order tracks the last selection, bounding both scopes and picks.
    delete scopes[scope];
    scopes[scope] = normalizeRecentAgents(recent);
    storage?.setItem(RECENT_AGENTS_KEY, JSON.stringify(Object.fromEntries(Object.entries(scopes).slice(-RECENT_AGENT_SCOPES_LIMIT))));
  } catch {
    // Storage can be unavailable or full; selection and in-memory recency still work.
  }
}
