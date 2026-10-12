import "fake-indexeddb/auto";
import { describe, expect, it } from "vitest";
import {
  HISTORY_DB_NAME,
  HISTORY_STORE_NAME,
  IMAGE_STORE_NAME,
  openHistoryDb,
  requestResult,
  transactionDone,
  type IndexedDbFactory,
} from "../../src/persistence/indexed-db";
import { initializePersistence } from "../../src/persistence/history-repository";
import { ACTIVE_ROUTER_KEY, AGENT_IDS_KEY, DEFAULT_ROUTER_URL, LEGACY_AGENT_ID_KEY, ROUTERS_KEY, SELECTED_PEERS_KEY, createSettingsRepository, migrateBootstrapRouter, setActiveRouter } from "../../src/persistence/settings-repository";

class MemoryStorage {
  private readonly values = new Map<string, string>();
  getItem(key: string): string | null { return this.values.get(key) ?? null; }
  setItem(key: string, value: string): void { this.values.set(key, value); }
  removeItem(key: string): void { this.values.delete(key); }
}

function factoryWithOpenCount(): { factory: IndexedDbFactory; opens: () => number } {
  let count = 0;
  return {
    factory: { open: (name, version) => { count += 1; return indexedDB.open(name, version); } },
    opens: () => count,
  };
}

async function deleteDatabase(): Promise<void> {
  await new Promise<void>((resolve, reject) => {
    const request = indexedDB.deleteDatabase(HISTORY_DB_NAME);
    request.onsuccess = () => resolve();
    request.onerror = () => reject(request.error ?? new Error("IndexedDB delete failed"));
    request.onblocked = () => reject(new Error("IndexedDB delete blocked"));
  });
}

describe("settings identity persistence", () => {
  it("removes only an unambiguous legacy bootstrap router", () => {
    const intendedRouter = "http://intended-router.test:43123";
    const bootstrapRouter = "http://web-ui.test:8787";
    const migrated = migrateBootstrapRouter({
      activeRouter: bootstrapRouter,
      routers: [intendedRouter, bootstrapRouter],
      aliases: { [intendedRouter]: "Intended", [bootstrapRouter]: "Old web UI" },
      agentIds: { [intendedRouter]: "web", [bootstrapRouter]: "web" },
      selectedPeers: { [`${intendedRouter}\u001fweb`]: "worker-one", [`${bootstrapRouter}\u001fweb`]: "worker-one" },
    }, bootstrapRouter);

    expect(migrated).toEqual({
      activeRouter: intendedRouter,
      routers: [intendedRouter],
      aliases: { [intendedRouter]: "Intended" },
      agentIds: { [intendedRouter]: "web" },
      selectedPeers: { [`${intendedRouter}\u001fweb`]: "worker-one" },
    });
  });

  it("keeps the same-host Web UI proxy instead of treating it as a legacy bootstrap router", () => {
    const directRouter = "http://web-ui.test:8765";
    const bootstrapRouter = "http://web-ui.test:8787";
    const settings = {
      activeRouter: bootstrapRouter,
      routers: [directRouter, bootstrapRouter],
      aliases: {},
      agentIds: { [bootstrapRouter]: "wolfgang" },
      selectedPeers: { [`${bootstrapRouter}\u001fwolfgang`]: "worker-one" },
    };

    expect(migrateBootstrapRouter(settings, bootstrapRouter)).toEqual(settings);
  });

  it("keeps the first router on the default identity while preserving the second router identity across save and reload", () => {
    const storage = new MemoryStorage();
    const firstRouter = "http://first-router.test:43123";
    const secondRouter = "http://second-router.test:43124";
    storage.setItem(ROUTERS_KEY, JSON.stringify([firstRouter, secondRouter]));
    storage.setItem(AGENT_IDS_KEY, JSON.stringify({ [secondRouter]: "backup-web" }));

    const repository = createSettingsRepository(storage);
    let settings = repository.load();
    expect(repository.agentId(settings, firstRouter, "wolfgang")).toBe("wolfgang");

    settings = setActiveRouter(settings, secondRouter);
    repository.save(settings);
    expect(storage.getItem(LEGACY_AGENT_ID_KEY)).toBeNull();
    expect(repository.agentId(repository.load(), secondRouter, "wolfgang")).toBe("backup-web");

    settings = setActiveRouter(repository.load(), firstRouter);
    expect(repository.agentId(settings, firstRouter, "wolfgang")).toBe("wolfgang");
    expect(JSON.parse(storage.getItem(AGENT_IDS_KEY) ?? "{}")).toEqual({ [secondRouter]: "backup-web" });
  });

  it("blocks port 8787 from known and active routers and resets activeRouter", () => {
    const storage = new MemoryStorage();
    storage.setItem(ROUTERS_KEY, JSON.stringify(["http://127.0.0.1:8787", "http://router.test:8765"]));
    storage.setItem(ACTIVE_ROUTER_KEY, "http://127.0.0.1:8787");

    const repository = createSettingsRepository(storage);
    const settings = repository.load();

    expect(settings.routers).not.toContain("http://127.0.0.1:8787");
    expect(settings.activeRouter).toBe(DEFAULT_ROUTER_URL);
    expect(storage.getItem(ACTIVE_ROUTER_KEY)).toBe(DEFAULT_ROUTER_URL);
  });

  it("migrates port 8787 in agentIds and selectedPeers to DEFAULT_ROUTER_URL so identity and history are preserved", () => {
    const storage = new MemoryStorage();
    storage.setItem(ROUTERS_KEY, JSON.stringify(["http://127.0.0.1:8787"]));
    storage.setItem(ACTIVE_ROUTER_KEY, "http://127.0.0.1:8787");
    storage.setItem(AGENT_IDS_KEY, JSON.stringify({ "http://127.0.0.1:8787": "wolfgang" }));
    storage.setItem(SELECTED_PEERS_KEY, JSON.stringify({ "http://127.0.0.1:8787\u001fwolfgang": "pi-go" }));

    const repository = createSettingsRepository(storage);
    const settings = repository.load();

    expect(settings.activeRouter).toBe(DEFAULT_ROUTER_URL);
    expect(repository.agentId(settings, DEFAULT_ROUTER_URL, "user")).toBe("wolfgang");
    expect(settings.agentIds[DEFAULT_ROUTER_URL]).toBe("wolfgang");
    expect(settings.selectedPeers[`${DEFAULT_ROUTER_URL}\u001fwolfgang`]).toBe("pi-go");
  });
});

describe("persistence startup migration", () => {
  it("cleans deprecated local storage once while preserving current settings and records", async () => {
    await deleteDatabase();
    const { factory, opens } = factoryWithOpenCount();
    const storage = new MemoryStorage();
    storage.setItem("mecha.web.messages", "deprecated messages");
    storage.setItem("mecha.web.conversations", "deprecated conversations");
    storage.setItem("mecha.web.activeRouter", "http://router.test");
    storage.setItem("mecha.web.drafts", JSON.stringify({ "router\u001fweb\u001fpeer": { body: "keep this", updatedAtMs: 10 } }));
    const db = await openHistoryDb(factory);
    const history = db.transaction(HISTORY_STORE_NAME, "readwrite").objectStore(HISTORY_STORE_NAME);
    history.put({
      key: "history-key",
      ownerScope: "http://router.test\u001fweb",
      peerScope: "http://router.test\u001fweb\u001fpeer",
      routerUrl: "http://router.test",
      agentId: "web",
      peer: "peer",
      message: { id: "message-1", body: "keep this message", created_at: "2030-01-01T00:00:00.000Z" },
      createdAtMs: Date.parse("2030-01-01T00:00:00.000Z"),
      savedAtMs: Date.parse("2030-01-01T00:00:00.000Z"),
    });
    const thumbnail = db.transaction(IMAGE_STORE_NAME, "readwrite").objectStore(IMAGE_STORE_NAME);
    // fake-indexeddb does not preserve Blob identity across a structured clone; this shape exercises the same size/type compatibility path.
    const blob = { size: "keep this thumbnail".length, type: "image/jpeg" };
    thumbnail.put({ key: "thumbnail-key", blob, savedAtMs: Date.parse("2030-01-01T00:00:00.000Z") });
    await Promise.all([
      transactionDone(history.transaction),
      transactionDone(thumbnail.transaction),
    ]);

    const now = Date.parse("2030-01-02T00:00:00.000Z");
    await Promise.all([
      initializePersistence(factory, storage, now),
      initializePersistence(factory, storage, now),
    ]);

    expect(opens()).toBe(2);
    expect(storage.getItem("mecha.web.messages")).toBeNull();
    expect(storage.getItem("mecha.web.conversations")).toBeNull();
    expect(storage.getItem("mecha.web.activeRouter")).toBe("http://router.test");
    expect(storage.getItem("mecha.web.drafts")).toContain("keep this");

    const historyRecords = await requestResult<unknown[]>(db.transaction(HISTORY_STORE_NAME, "readonly").objectStore(HISTORY_STORE_NAME).getAll());
    expect(historyRecords).toEqual([expect.objectContaining({ message: expect.objectContaining({ id: "message-1", body: "keep this message" }) })]);
    const thumbnailRecords = await requestResult<unknown[]>(db.transaction(IMAGE_STORE_NAME, "readonly").objectStore(IMAGE_STORE_NAME).getAll());
    expect(thumbnailRecords).toHaveLength(1);
    expect(thumbnailRecords[0]).toEqual(expect.objectContaining({ key: "thumbnail-key", savedAtMs: Date.parse("2030-01-01T00:00:00.000Z") }));
    const thumbnailValue = thumbnailRecords[0];
    if (typeof thumbnailValue !== "object" || thumbnailValue === null || !("blob" in thumbnailValue) || typeof thumbnailValue.blob !== "object" || thumbnailValue.blob === null || !("size" in thumbnailValue.blob)) throw new Error("Thumbnail blob was lost during startup migration");
    expect(thumbnailValue.blob.size).toBe(blob.size);
  });
});
