import { describe, expect, it } from "vitest";
import {
  MANAGED_RECENT_FOLDERS_KEY,
  MANAGED_RECENT_FOLDERS_LIMIT,
  loadManagedRecentFolders,
  recordManagedRecentFolder,
  saveManagedRecentFolders,
} from "../../src/persistence/managed-folder-history";

class MemoryStorage {
  private readonly values = new Map<string, string>();
  getItem(key: string): string | null { return this.values.get(key) ?? null; }
  setItem(key: string, value: string): void { this.values.set(key, value); }
  removeItem(key: string): void { this.values.delete(key); }
}

describe("managed folder history", () => {
  it("keeps a local deduplicated history of fifteen usable folder paths", () => {
    const folders = recordManagedRecentFolder(
      Array.from({ length: MANAGED_RECENT_FOLDERS_LIMIT }, (_, index) => `/projects/${index}`),
      "/projects/new",
    );

    expect(folders).toHaveLength(MANAGED_RECENT_FOLDERS_LIMIT);
    expect(folders[0]).toBe("/projects/new");
    expect(folders).not.toContain(`/projects/${MANAGED_RECENT_FOLDERS_LIMIT - 1}`);
  });

  it("persists only absolute and home-relative folders", () => {
    const storage = new MemoryStorage();
    saveManagedRecentFolders(storage, ["relative/project", "/projects/one", "~/code/two", "/projects/one"]);

    expect(storage.getItem(MANAGED_RECENT_FOLDERS_KEY)).toBe(JSON.stringify(["/projects/one", "~/code/two"]));
    expect(loadManagedRecentFolders(storage)).toEqual(["/projects/one", "~/code/two"]);
  });

  it("treats malformed saved history as empty", () => {
    const storage = new MemoryStorage();
    storage.setItem(MANAGED_RECENT_FOLDERS_KEY, "not json");

    expect(loadManagedRecentFolders(storage)).toEqual([]);
  });
});
