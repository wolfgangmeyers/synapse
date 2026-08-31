import { useCallback, useEffect, useState } from "react";
import { createDraftRepository, type DraftRepository } from "../persistence/draft-repository";

export interface DraftHook { draft: string; setDraft(value: string): void; clear(): void; save(): void }
export function useDrafts(routerUrl: string, agentId: string, peer: string | undefined, repository?: DraftRepository): DraftHook {
  const [defaultRepository] = useState(() => createDraftRepository());
  const store = repository ?? defaultRepository;
  const [draft, setDraftState] = useState("");
  useEffect(() => { setDraftState(peer === undefined ? "" : store.load(routerUrl, agentId, peer)); }, [routerUrl, agentId, peer, store]);
  const save = useCallback(() => { if (peer !== undefined) store.save(routerUrl, agentId, peer, draft); }, [routerUrl, agentId, peer, draft, store]);
  const setDraft = useCallback((value: string) => { setDraftState(value); if (peer !== undefined) store.save(routerUrl, agentId, peer, value); }, [routerUrl, agentId, peer, store]);
  const clear = useCallback(() => { setDraftState(""); if (peer !== undefined) store.clear(routerUrl, agentId, peer); }, [routerUrl, agentId, peer, store]);
  return { draft, setDraft, clear, save };
}
