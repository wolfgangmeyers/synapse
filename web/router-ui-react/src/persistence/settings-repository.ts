export const ACTIVE_ROUTER_KEY = "mecha.web.activeRouter";
export const ROUTERS_KEY = "mecha.web.routers";
export const ROUTER_ALIASES_KEY = "mecha.web.routerAliases";
export const AGENT_IDS_KEY = "mecha.web.agentIds";
export const LEGACY_AGENT_ID_KEY = "mecha.web.agentId";
export const SELECTED_PEERS_KEY = "mecha.web.selectedPeers";
export const DEFAULT_ROUTER_URL = "http://127.0.0.1:8765";
export const SCOPE_SEPARATOR = "\u001f";

export interface StorageAdapter {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

export interface SettingsState {
  activeRouter: string;
  routers: string[];
  aliases: Record<string, string>;
  agentIds: Record<string, string>;
  selectedPeers: Record<string, string>;
}

function storageOrUndefined(): StorageAdapter | undefined {
  try {
    return typeof globalThis.localStorage === "undefined" ? undefined : globalThis.localStorage;
  } catch {
    return undefined;
  }
}

export function normalizeRouterUrl(value: string): string {
  const candidate = value.trim();
  if (candidate.length === 0) throw new Error("Router URL is required");
  const withScheme = /^[a-z][a-z\d+.-]*:\/\//i.test(candidate) ? candidate : `http://${candidate}`;
  let url: URL;
  try {
    url = new URL(withScheme);
  } catch {
    throw new Error("Router URL is invalid");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error("Router URL must start with http:// or https://");
  }
  if (url.username !== "" || url.password !== "") {
    throw new Error("Router URL must not include credentials");
  }
  url.pathname = "";
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/$/, "");
}

export const normalizeRouterInput = normalizeRouterUrl;

export function ownerScope(routerUrl: string, agentId: string): string {
  return `${normalizeRouterUrl(routerUrl)}${SCOPE_SEPARATOR}${agentId}`;
}

export function peerScope(routerUrl: string, agentId: string, peer: string): string {
  return `${ownerScope(routerUrl, agentId)}${SCOPE_SEPARATOR}${peer}`;
}

export function draftScope(routerUrl: string, agentId: string, peer: string | undefined): string {
  return peer === undefined || peer === "" ? "" : peerScope(routerUrl, agentId, peer);
}

function parseJson(value: string | null): unknown {
  if (value === null || value === "") return undefined;
  try {
    const parsed: unknown = JSON.parse(value);
    return parsed;
  } catch {
    return undefined;
  }
}

function stringRecord(value: unknown): Record<string, string> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return {};
  const result: Record<string, string> = {};
  for (const [key, candidate] of Object.entries(value)) {
    if (typeof candidate === "string" && candidate.length > 0) result[key] = candidate;
  }
  return result;
}

function canonicalRouters(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const seen = new Set<string>();
  const routers: string[] = [];
  for (const candidate of value) {
    if (typeof candidate !== "string") continue;
    try {
      const router = normalizeRouterUrl(candidate);
      if (!seen.has(router)) {
        seen.add(router);
        routers.push(router);
      }
    } catch {
      // A malformed saved URL cannot be used as a storage key.
    }
  }
  return routers;
}

export function emptySettings(defaultRouter = DEFAULT_ROUTER_URL): SettingsState {
  const router = normalizeRouterUrl(defaultRouter);
  return { activeRouter: router, routers: [router], aliases: {}, agentIds: {}, selectedPeers: {} };
}

export function persistedActiveRouter(storage: StorageAdapter | undefined = storageOrUndefined(), bootstrapRouterUrl?: string): string | undefined {
  const value = storage?.getItem(ACTIVE_ROUTER_KEY);
  if (value === null || value === undefined || value.trim() === "") return undefined;
  try {
    normalizeRouterUrl(value);
    return migrateBootstrapRouter(loadSettings(storage), bootstrapRouterUrl).activeRouter;
  } catch {
    return undefined;
  }
}

export function loadSettings(storage: StorageAdapter | undefined = storageOrUndefined(), defaultRouter = DEFAULT_ROUTER_URL): SettingsState {
  const fallback = emptySettings(defaultRouter);
  if (storage === undefined) return fallback;

  const routers = canonicalRouters(parseJson(storage.getItem(ROUTERS_KEY)));
  const activeValue = storage.getItem(ACTIVE_ROUTER_KEY);
  let activeRouter = activeValue === null ? fallback.activeRouter : activeValue;
  try {
    activeRouter = normalizeRouterUrl(activeRouter);
  } catch {
    activeRouter = fallback.activeRouter;
  }
  if (!routers.includes(activeRouter)) routers.unshift(activeRouter);
  if (routers.length === 0) routers.push(fallback.activeRouter);

  const aliases: Record<string, string> = {};
  for (const [key, alias] of Object.entries(stringRecord(parseJson(storage.getItem(ROUTER_ALIASES_KEY))))) {
    try {
      const router = normalizeRouterUrl(key);
      if (routers.includes(router)) aliases[router] = alias;
    } catch {
      // Ignore aliases that cannot be mapped to a canonical router.
    }
  }
  const agentIds: Record<string, string> = {};
  for (const [key, agentId] of Object.entries(stringRecord(parseJson(storage.getItem(AGENT_IDS_KEY))))) {
    try {
      agentIds[normalizeRouterUrl(key)] = agentId;
    } catch {
      // Ignore IDs whose router key is invalid.
    }
  }
  const selectedPeers: Record<string, string> = {};
  for (const [key, peer] of Object.entries(stringRecord(parseJson(storage.getItem(SELECTED_PEERS_KEY))))) {
    const parts = key.split(SCOPE_SEPARATOR);
    if (parts.length !== 2 || peer.length === 0) continue;
    try {
      selectedPeers[ownerScope(parts[0] ?? "", parts[1] ?? "")] = peer;
    } catch {
      // Ignore malformed scope keys.
    }
  }
  return { activeRouter, routers, aliases, agentIds, selectedPeers };
}

export function saveSettings(settings: SettingsState, storage: StorageAdapter | undefined = storageOrUndefined()): void {
  if (storage === undefined) return;
  const routers = canonicalRouters(settings.routers);
  const activeRouter = normalizeRouterUrl(settings.activeRouter);
  if (!routers.includes(activeRouter)) routers.unshift(activeRouter);
  const aliases: Record<string, string> = {};
  for (const [key, value] of Object.entries(settings.aliases)) {
    try {
      const router = normalizeRouterUrl(key);
      if (routers.includes(router) && value.trim() !== "") aliases[router] = value.trim();
    } catch {
      // Ignore malformed keys when serializing settings.
    }
  }
  const agentIds: Record<string, string> = {};
  for (const [key, value] of Object.entries(settings.agentIds)) {
    try {
      agentIds[normalizeRouterUrl(key)] = value;
    } catch {
      // Ignore malformed keys when serializing settings.
    }
  }
  const selectedPeers: Record<string, string> = {};
  for (const [key, value] of Object.entries(settings.selectedPeers)) {
    const parts = key.split(SCOPE_SEPARATOR);
    if (parts.length !== 2 || value === "") continue;
    try {
      const router = normalizeRouterUrl(parts[0] ?? "");
      if (!routers.includes(router)) continue;
      selectedPeers[ownerScope(router, parts[1] ?? "")] = value;
    } catch {
      // Ignore malformed scope keys when serializing settings.
    }
  }
  storage.setItem(ACTIVE_ROUTER_KEY, activeRouter);
  storage.setItem(ROUTERS_KEY, JSON.stringify(routers));
  storage.setItem(ROUTER_ALIASES_KEY, JSON.stringify(aliases));
  storage.setItem(AGENT_IDS_KEY, JSON.stringify(agentIds));
  storage.setItem(SELECTED_PEERS_KEY, JSON.stringify(selectedPeers));
}

export function removeDeprecatedLocalStorage(storage: StorageAdapter | undefined = storageOrUndefined()): void {
  storage?.removeItem("mecha.web.messages");
  storage?.removeItem("mecha.web.conversations");
}

// Older builds treated VITE_ROUTER_URL as a router selection on every load. The
// appended final entry is unambiguous only when there was one existing router;
// leave multi-router profiles alone because their previous selection is unknown.
export function migrateBootstrapRouter(settings: SettingsState, bootstrapRouterUrl?: string): SettingsState {
  if (bootstrapRouterUrl === undefined) return settings;
  let bootstrapRouter: string;
  try {
    bootstrapRouter = normalizeRouterUrl(bootstrapRouterUrl);
  } catch {
    return settings;
  }
  if (settings.activeRouter !== bootstrapRouter || settings.routers.length !== 2 || settings.routers[1] !== bootstrapRouter) return settings;

  const intendedRouter = settings.routers[0];
  if (intendedRouter === undefined) return settings;
  try {
    const intended = new URL(intendedRouter);
    const bootstrap = new URL(bootstrapRouter);
    // A same-host, different-origin bootstrap is the Web UI proxy replacing a
    // direct router connection. It is not the obsolete appended selection.
    if (intended.hostname === bootstrap.hostname && intended.origin !== bootstrap.origin) return settings;
  } catch {
    return settings;
  }
  const aliases = { ...settings.aliases };
  delete aliases[bootstrapRouter];
  const agentIds = { ...settings.agentIds };
  delete agentIds[bootstrapRouter];
  const selectedPeers = Object.fromEntries(Object.entries(settings.selectedPeers).filter(([scope]) => !scope.startsWith(`${bootstrapRouter}${SCOPE_SEPARATOR}`)));
  return { activeRouter: intendedRouter, routers: [intendedRouter], aliases, agentIds, selectedPeers };
}

export function setActiveRouter(settings: SettingsState, routerUrl: string): SettingsState {
  const router = normalizeRouterUrl(routerUrl);
  const routers = settings.routers.includes(router) ? settings.routers : [...settings.routers, router];
  return { ...settings, activeRouter: router, routers };
}

export function setRouterAlias(settings: SettingsState, routerUrl: string, alias: string): SettingsState {
  const router = normalizeRouterUrl(routerUrl);
  const aliases = { ...settings.aliases };
  const value = alias.trim();
  if (value === "") delete aliases[router];
  else aliases[router] = value;
  return { ...settings, aliases };
}

export function setAgentId(settings: SettingsState, routerUrl: string, agentId: string): SettingsState {
  const router = normalizeRouterUrl(routerUrl);
  return { ...settings, agentIds: { ...settings.agentIds, [router]: agentId.trim() } };
}

export function selectedPeerFor(settings: SettingsState, routerUrl: string, agentId: string): string {
  return settings.selectedPeers[ownerScope(routerUrl, agentId)] ?? "";
}

export function setSelectedPeer(settings: SettingsState, routerUrl: string, agentId: string, peer: string): SettingsState {
  return { ...settings, selectedPeers: { ...settings.selectedPeers, [ownerScope(routerUrl, agentId)]: peer } };
}

export function agentIdForRouter(
  settings: SettingsState,
  routerUrl: string,
  defaultAgentId: string,
  storage?: StorageAdapter,
): string {
  const router = normalizeRouterUrl(routerUrl);
  const configured = settings.agentIds[router];
  if (configured !== undefined && configured !== "") return configured;
  // The legacy key represents the first configured router only. It must not
  // select the identity for a different saved router and hide that router's history.
  const legacy = router === settings.routers[0] ? storage?.getItem(LEGACY_AGENT_ID_KEY) : null;
  return legacy === undefined || legacy === null || legacy.trim() === "" ? defaultAgentId : legacy;
}

export function removeRouter(settings: SettingsState, routerUrl: string): SettingsState {
  const router = normalizeRouterUrl(routerUrl);
  if (settings.routers.length <= 1 || !settings.routers.includes(router)) return settings;
  const routers = settings.routers.filter((candidate) => candidate !== router);
  const activeRouter = settings.activeRouter === router ? (routers[0] ?? settings.activeRouter) : settings.activeRouter;
  const aliases = { ...settings.aliases };
  delete aliases[router];
  const agentIds = { ...settings.agentIds };
  delete agentIds[router];
  return { ...settings, activeRouter, routers, aliases, agentIds };
}

export function createSettingsRepository(
  storage?: StorageAdapter,
  defaultRouter = DEFAULT_ROUTER_URL,
) {
  const target = storage ?? storageOrUndefined();
  return {
    load: (): SettingsState => loadSettings(target, defaultRouter),
    save: (settings: SettingsState): void => saveSettings(settings, target),
    remove: (settings: SettingsState, routerUrl: string): SettingsState => {
      const next = removeRouter(settings, routerUrl);
      saveSettings(next, target);
      return next;
    },
    agentId: (settings: SettingsState, routerUrl: string, fallback: string): string => agentIdForRouter(settings, routerUrl, fallback, target),
  };
}
