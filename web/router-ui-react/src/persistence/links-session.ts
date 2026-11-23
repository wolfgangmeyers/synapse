import { EMPTY_LINKS_STATE, type LinksState } from "../domain/links";

/** Most recently used agents whose links state is kept for this page session. */
export const LINKS_AGENT_LIMIT = 20;

// In-memory only (no storage): links are session state, so nothing outlives the
// page. The Map keeps insertion order, which doubles as recency for eviction.
const sessionStore = new Map<string, LinksState>();

export function getLinksState(agentId: string): LinksState {
  return sessionStore.get(agentId) ?? EMPTY_LINKS_STATE;
}

export function setLinksState(agentId: string, state: LinksState): void {
  sessionStore.delete(agentId);
  sessionStore.set(agentId, state);
  while (sessionStore.size > LINKS_AGENT_LIMIT) {
    const oldest = sessionStore.keys().next().value;
    if (oldest === undefined) break;
    sessionStore.delete(oldest);
  }
}

export function resetLinksSession(): void {
  sessionStore.clear();
}
