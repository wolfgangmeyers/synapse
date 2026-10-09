import type { StorageAdapter } from "./settings-repository";

export const MANAGED_RECENT_FOLDERS_KEY = "mecha.web.managedRecentFolders";
export const MANAGED_RECENT_FOLDERS_LIMIT = 15;

function validFolder(value: unknown): value is string {
  return typeof value === "string" && (value.startsWith("/") || value === "~" || value.startsWith("~/"));
}

export function normalizeManagedRecentFolders(values: readonly unknown[]): string[] {
  const folders: string[] = [];
  const seen = new Set<string>();
  for (const value of values) {
    if (!validFolder(value)) continue;
    const folder = value.trim();
    if (folder === "" || seen.has(folder)) continue;
    seen.add(folder);
    folders.push(folder);
    if (folders.length === MANAGED_RECENT_FOLDERS_LIMIT) break;
  }
  return folders;
}

export function loadManagedRecentFolders(storage: StorageAdapter | undefined): string[] {
  if (storage === undefined) return [];
  try {
    const raw = storage.getItem(MANAGED_RECENT_FOLDERS_KEY);
    const parsed: unknown = raw === null ? [] : JSON.parse(raw);
    return Array.isArray(parsed) ? normalizeManagedRecentFolders(parsed) : [];
  } catch {
    return [];
  }
}

export function recordManagedRecentFolder(folders: readonly string[], folder: string): string[] {
  return normalizeManagedRecentFolders([folder, ...folders]);
}

export function saveManagedRecentFolders(storage: StorageAdapter | undefined, folders: readonly string[]): void {
  if (storage === undefined) return;
  storage.setItem(MANAGED_RECENT_FOLDERS_KEY, JSON.stringify(normalizeManagedRecentFolders(folders)));
}
