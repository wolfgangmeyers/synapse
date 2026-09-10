import { secureUuid } from "../platform/secure-uuid";

export interface SavedRouter { url: string; alias?: string; agentId: string }
export function normalizeRouterUrl(value: string): string {
  const candidate = value.trim(); const withScheme = /^[a-z]+:\/\//i.test(candidate) ? candidate : `http://${candidate}`; const url = new URL(withScheme);
  if (url.protocol !== "http:" && url.protocol !== "https:") throw new Error("Router URL must start with http:// or https://");
  if (url.username || url.password) throw new Error("Router URL must not include credentials");
  url.pathname = ""; url.search = ""; url.hash = ""; return url.toString().replace(/\/$/, "");
}
export function routerDisplayName(url: string, alias?: string): string { if (alias?.trim()) return alias.trim(); return new URL(normalizeRouterUrl(url)).host; }
export const normalizeRouterInput = normalizeRouterUrl;
function isRouterMap(value: readonly SavedRouter[] | Record<string, string>): value is Record<string, string> { return !Array.isArray(value); }
export function agentIdForRouter(routers: readonly SavedRouter[] | Record<string, string>, url: string, defaultAgentId = "wolfgang"): string { const canonical = normalizeRouterUrl(url); if (!isRouterMap(routers)) return routers.find((router) => normalizeRouterUrl(router.url) === canonical)?.agentId || defaultAgentId; const value = routers[canonical]; return value || defaultAgentId; }
export function createConnectionIdentity(agentId: string): { agentId: string; instanceId: string } { return { agentId, instanceId: secureUuid() }; }
export function normalizeSavedRouter(router: SavedRouter): SavedRouter { const url = normalizeRouterUrl(router.url); return { url, ...(router.alias?.trim() ? { alias: router.alias.trim() } : {}), agentId: router.agentId.trim() }; }
export function ensureRouter(routers: SavedRouter[], router: SavedRouter): SavedRouter[] { const normalized = normalizeSavedRouter(router); return routers.some((candidate) => normalizeRouterUrl(candidate.url) === normalized.url) ? routers.map((candidate) => normalizeRouterUrl(candidate.url) === normalized.url ? normalized : candidate) : [...routers, normalized]; }
export function removeRouter(routers: SavedRouter[], url: string): SavedRouter[] { if (routers.length <= 1) return routers; const canonical = normalizeRouterUrl(url); return routers.filter((router) => normalizeRouterUrl(router.url) !== canonical); }
export function switchRouterState(current: { routerUrl: string; agentId: string; selectedPeer?: string }, next: SavedRouter): { routerUrl: string; agentId: string; selectedPeer: string; reset: true } { const router = normalizeSavedRouter(next); return { routerUrl: router.url, agentId: router.agentId, selectedPeer: "", reset: true }; }
