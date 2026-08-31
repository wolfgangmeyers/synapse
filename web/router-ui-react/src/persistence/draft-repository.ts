import {
  DRAFT_LIMIT,
  DRAFT_RETENTION_MS,
  type DraftRecord,
  isRecord,
  pruneDrafts as pruneDraftRecords,
} from "./retention";
import {
  draftScope,
  ownerScope,
  type StorageAdapter,
} from "./settings-repository";

export { DRAFT_LIMIT, DRAFT_RETENTION_MS, draftScope, ownerScope };
export type { DraftRecord } from "./retention";
export type { StorageAdapter } from "./settings-repository";

export const DRAFT_STORE_KEY = "mecha.web.drafts";

export type Drafts = Record<string, DraftRecord>;

function storageOrUndefined(): StorageAdapter | undefined {
  try {
    return typeof globalThis.localStorage === "undefined" ? undefined : globalThis.localStorage;
  } catch {
    return undefined;
  }
}

function parseDrafts(value: string | null): Record<string, unknown> | undefined {
  if (value === null || value === "") return {};
  try {
    const parsed: unknown = JSON.parse(value);
    return isRecord(parsed) ? parsed : undefined;
  } catch {
    return undefined;
  }
}

function draftMap(storage: StorageAdapter | undefined, now: number): Drafts {
  if (storage === undefined) return {};
  const parsed = parseDrafts(storage.getItem(DRAFT_STORE_KEY));
  return parsed === undefined ? {} : pruneDraftRecords(parsed, now);
}

function writeDraftMap(storage: StorageAdapter | undefined, drafts: Drafts): void {
  if (storage === undefined) return;
  storage.setItem(DRAFT_STORE_KEY, JSON.stringify(drafts));
}

export function loadDrafts(storage: StorageAdapter | undefined = storageOrUndefined(), now = Date.now()): Drafts {
  return draftMap(storage, now);
}

export function saveDrafts(
  drafts: Record<string, unknown>,
  storage: StorageAdapter | undefined = storageOrUndefined(),
  now = Date.now(),
): Drafts {
  const retained = pruneDraftRecords(drafts, now);
  writeDraftMap(storage, retained);
  return retained;
}

export function loadDraft(
  routerUrl: string,
  agentId: string,
  peer: string,
  storage: StorageAdapter | undefined = storageOrUndefined(),
  now = Date.now(),
): string {
  const key = draftScope(routerUrl, agentId, peer);
  return key === "" ? "" : draftMap(storage, now)[key]?.body ?? "";
}

export function saveDraft(
  routerUrl: string,
  agentId: string,
  peer: string,
  body: string,
  storage: StorageAdapter | undefined = storageOrUndefined(),
  now = Date.now(),
): void {
  const key = draftScope(routerUrl, agentId, peer);
  if (key === "") return;
  const drafts = draftMap(storage, now);
  if (body === "") delete drafts[key];
  else drafts[key] = { body, updatedAtMs: now };
  saveDrafts(drafts, storage, now);
}

export function clearDraft(
  routerUrl: string,
  agentId: string,
  peer: string,
  storage: StorageAdapter | undefined = storageOrUndefined(),
  now = Date.now(),
): void {
  const key = draftScope(routerUrl, agentId, peer);
  if (key === "") return;
  const drafts = draftMap(storage, now);
  delete drafts[key];
  saveDrafts(drafts, storage, now);
}

export interface DraftRepository {
  loadAll(now?: number): Drafts;
  save(routerUrl: string, agentId: string, peer: string, body: string, now?: number): void;
  load(routerUrl: string, agentId: string, peer: string, now?: number): string;
  clear(routerUrl: string, agentId: string, peer: string, now?: number): void;
  peers(routerUrl: string, agentId: string, now?: number): Set<string>;
}

export function createDraftRepository(
  storage?: StorageAdapter,
): DraftRepository {
  const target = storage ?? storageOrUndefined();
  return {
    loadAll: (now = Date.now()): Drafts => {
      const drafts = draftMap(target, now);
      writeDraftMap(target, drafts);
      return drafts;
    },
    save: (routerUrl, agentId, peer, body, now = Date.now()): void => {
      const key = draftScope(routerUrl, agentId, peer);
      if (key === "") return;
      const drafts = draftMap(target, now);
      if (body === "") delete drafts[key];
      else drafts[key] = { body, updatedAtMs: now };
      saveDrafts(drafts, target, now);
    },
    load: (routerUrl, agentId, peer, now = Date.now()): string => {
      const key = draftScope(routerUrl, agentId, peer);
      return key === "" ? "" : draftMap(target, now)[key]?.body ?? "";
    },
    clear: (routerUrl, agentId, peer, now = Date.now()): void => {
      const key = draftScope(routerUrl, agentId, peer);
      if (key === "") return;
      const drafts = draftMap(target, now);
      delete drafts[key];
      saveDrafts(drafts, target, now);
    },
    peers: (routerUrl, agentId, now = Date.now()): Set<string> => {
      const prefix = `${ownerScope(routerUrl, agentId)}\u001f`;
      const peers = new Set<string>();
      for (const key of Object.keys(draftMap(target, now))) {
        if (key.startsWith(prefix)) peers.add(key.slice(prefix.length));
      }
      return peers;
    },
  };
}
