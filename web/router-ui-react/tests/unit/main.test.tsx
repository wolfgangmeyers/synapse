import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const render = vi.hoisted(() => vi.fn());
const createRoot = vi.hoisted(() => vi.fn(() => ({ render })));
const browserStorage = vi.hoisted(() => {
  const values = new Map<string, string>();
  return {
    getItem: (key: string): string | null => values.get(key) ?? null,
    setItem: (key: string, value: string): void => { values.set(key, value); },
    removeItem: (key: string): void => { values.delete(key); },
    clear: (): void => { values.clear(); },
  };
});

vi.mock("react-dom/client", () => ({ createRoot }));
vi.mock("../../src/App", () => ({ default: () => null }));

describe("browser entry point", () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    browserStorage.clear();
    vi.stubGlobal("localStorage", browserStorage);
    document.body.innerHTML = '<div id="root"></div>';
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("passes browser storage to the app for managed folder history", async () => {
    browserStorage.setItem("mecha.web.managedRecentFolders", JSON.stringify(["/projects/recent"]));

    await import("../../src/main");

    expect(createRoot).toHaveBeenCalledWith(document.getElementById("root"));
    expect(render).toHaveBeenCalledWith(expect.objectContaining({ props: expect.objectContaining({ storage: browserStorage }) }));
  });
});
