// @vitest-environment jsdom
import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useSelectedManagedActivity } from "../../src/hooks/use-selected-managed-activity";
import type { ManagedHistoryEntry, UILiveFrame } from "../../src/protocol/types";
import type { ManagedHistoryResponse, RouterHttpClient } from "../../src/transport/http-client";
import type { UILiveClient } from "../../src/transport/ui-live-client";

function deferred() {
  let resolve!: (value: ManagedHistoryResponse) => void;
  let reject!: (reason: Error) => void;
  const promise = new Promise<ManagedHistoryResponse>((ok, fail) => { resolve = ok; reject = fail; });
  return { promise, resolve, reject };
}
const entry = (agent: string, n: number): ManagedHistoryEntry => ({ agent_id: agent, id: `complete:${n}`, message: `message ${n}` });
const page = (entries: ManagedHistoryEntry[]): ManagedHistoryResponse => ({ view: "complete", entries, next_before: null });
const scope = { routerUrl: "http://localhost:8765", agentId: "user" };
function setup() {
  const fetch = vi.fn<RouterHttpClient["managedHistory"]>();
  let listener: ((frame: UILiveFrame | "connected" | "disconnected") => void) | undefined;
  const live: UILiveClient = { url: "test", projectionVersion: undefined, connect() {}, close() {}, subscribe(fn) { listener = fn; return () => { listener = undefined; }; } };
  return { disconnect() { listener?.("disconnected"); }, reconnect() { listener?.("connected"); }, fetch, client: { managedHistory: fetch }, live, emit(e: ManagedHistoryEntry) { listener?.({ type: "history_entry", protocol: "mecha.web/v1", projection_version: 1, view: "complete", entry: e }); } };
}
describe("selected managed activity", () => {
  it("retries a failed initial page without losing live activity", async () => {
    const h = setup();
    h.fetch.mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(page([entry("A", 1)]));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.error).toBeDefined());
    act(() => h.emit(entry("A", 2)));
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50 });
    expect(result.current.error).toBeUndefined();
    expect(result.current.entries.map(e => e.id)).toEqual(["complete:1", "complete:2"]);
  });
  it("discards by visible identity even on timestamp ties and refetches a zero-contribution source", async () => {
    const h = setup();
    const rows = [1, 2, 3].map((n) => ({ ...entry("A", n), at: "2026-01-01T00:00:00Z" }));
    h.fetch.mockResolvedValueOnce(page(rows)).mockResolvedValueOnce(page(rows));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    act(() => result.current.discardBefore(new Set()));
    expect(result.current.entries).toEqual([]);
    expect(result.current.hasOlder).toBe(true);
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 4 });
    expect(result.current.entries.map((row) => row.id)).toEqual(rows.map((row) => row.id));
  });
  it("merges live with the bounded pending snapshot and filters peers", async () => {
    const h = setup(); const request = deferred(); h.fetch.mockReturnValue(request.promise);
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    expect(h.fetch).toHaveBeenCalledWith("A", "complete", { limit: 50 });
    act(() => { h.emit(entry("B", 99)); h.emit({ ...entry("A", 50), message: "live" }); h.emit(entry("A", 51)); });
    await act(async () => request.resolve(page([...Array.from({ length: 50 }, (_, i) => entry("A", i + 1)), entry("B", 100)])));
    expect(result.current.ready).toBe(true);
    expect(result.current.entries).toHaveLength(51);
    expect(result.current.entries[0]?.id).toBe("complete:1");
    expect(result.current.entries.find((e) => e.id === "complete:50")?.message).toBe("live");
    act(() => h.emit(entry("A", 52)));
    expect(result.current.entries).toHaveLength(52);
    expect(result.current.entries[0]?.id).toBe("complete:1");
  });
  it("paginates independently, preserves live and loaded entries, and retries failures", async () => {
    const h = setup();
    h.fetch.mockResolvedValueOnce({ ...page([entry("A", 3)]), next_before: 3 })
      .mockRejectedValueOnce(new Error("offline"))
      .mockResolvedValueOnce({ ...page([entry("A", 2)]), next_before: 2 })
      .mockResolvedValueOnce(page([entry("A", 1)]));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    await act(async () => result.current.loadOlder());
    expect(result.current.hasOlder).toBe(true);
    act(() => h.emit(entry("A", 4)));
    const anchor = vi.fn();
    await act(async () => result.current.loadOlder(anchor));
    await act(async () => result.current.loadOlder(anchor));
    expect(h.fetch.mock.calls.slice(1).map((call) => call[2]?.before)).toEqual([3, 3, 2]);
    expect(result.current.entries.map((e) => e.id)).toEqual(["complete:1", "complete:2", "complete:3", "complete:4"]);
    expect(anchor).toHaveBeenCalledTimes(2);
    expect(result.current.hasOlder).toBe(false);
  });
  it("discards a page disconnected by a concurrent trim and retries from the retained edge", async () => {
    const h = setup(); const pending = deferred();
    h.fetch.mockResolvedValueOnce({ ...page([entry("A", 10)]), next_before: 10 })
      .mockReturnValueOnce(pending.promise).mockResolvedValueOnce(page([entry("A", 60)]));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    let request!: Promise<void>;
    act(() => { request = result.current.loadOlder(); });
    act(() => { for (let i = 11; i <= 210; i++) h.emit(entry("A", i)); });
    await act(async () => { pending.resolve(page([entry("A", 9)])); await request; });
    expect(result.current.entries.some((e) => e.id === "complete:9")).toBe(false);
    expect(result.current.hasOlder).toBe(true);
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 61 });
  });
  it("exposes a failed page and clears the error on retry", async () => {
    const h = setup(); h.fetch.mockResolvedValueOnce({ ...page([entry("A", 3)]), next_before: 3 })
      .mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce(page([entry("A", 2)]));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    await act(async () => result.current.loadOlder());
    expect(result.current.error?.message).toMatch(/retry/i);
    await act(async () => result.current.loadOlder());
    expect(result.current.error).toBeUndefined();
    expect(h.fetch.mock.calls[2]?.[2]?.before).toBe(3);
  });
  it("bounds sustained live activity and recovers the trimmed gap", async () => {
    const h = setup(); h.fetch.mockResolvedValueOnce(page([])).mockResolvedValueOnce(page([entry("A", 50)]));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    act(() => { for (let i = 1; i <= 200; i++) h.emit(entry("A", i)); });
    expect(result.current.entries).toHaveLength(150);
    expect(result.current.liveIds.size).toBe(150);
    expect(result.current.hasOlder).toBe(true);
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 51 });
    expect(result.current.entries.some((e) => e.id === "complete:50")).toBe(true);
  });
  it("bounds an initial snapshot merged after live trimming and keeps the gap recoverable", async () => {
    const h = setup(); const request = deferred();
    h.fetch.mockReturnValueOnce(request.promise).mockResolvedValueOnce(page([entry("A", 50)]));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    act(() => { for (let i = 51; i <= 251; i++) h.emit(entry("A", i)); });
    await act(async () => request.resolve(page(Array.from({ length: 50 }, (_, i) => entry("A", i + 1)))));
    expect(result.current.entries).toHaveLength(150);
    expect(result.current.entries[0]?.id).toBe("complete:102");
    expect(result.current.liveIds.size).toBe(150);
    expect(result.current.hasOlder).toBe(true);
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 102 });
    expect(result.current.entries).toHaveLength(151);
  });
  it("reconciles reconnect snapshots without losing pagination or concurrent updates", async () => {
    const h = setup(); const reconnect = deferred();
    h.fetch.mockResolvedValueOnce({ ...page([entry("A", 3)]), next_before: 3 })
      .mockResolvedValueOnce({ ...page([entry("A", 2)]), next_before: 2 })
      .mockReturnValueOnce(reconnect.promise);
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    await act(async () => result.current.loadOlder());
    act(() => h.reconnect());
    act(() => h.emit({ ...entry("A", 3), message: "new live" }));
    await act(async () => reconnect.resolve(page([{ ...entry("A", 3), message: "stale" }, entry("A", 4)])));
    expect(result.current.entries.map((e) => e.id)).toEqual(["complete:2", "complete:3", "complete:4"]);
    expect(result.current.entries[1]?.message).toBe("new live");
    expect(result.current.hasOlder).toBe(true);
  });
  it("preserves an explicitly expanded window and cursor across an overlapping reconnect", async () => {
    const h = setup();
    h.fetch.mockResolvedValueOnce({ ...page(Array.from({ length: 50 }, (_, i) => entry("A", i + 151))), next_before: 151 })
      .mockResolvedValueOnce({ ...page(Array.from({ length: 100 }, (_, i) => entry("A", i + 51))), next_before: 51 })
      .mockResolvedValueOnce({ ...page([entry("A", 50)]), next_before: 50 })
      .mockResolvedValueOnce({ ...page(Array.from({ length: 50 }, (_, i) => entry("A", i + 151))), next_before: 151 })
      .mockResolvedValueOnce(page([entry("A", 49)]));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    await act(async () => result.current.loadOlder());
    await act(async () => result.current.loadOlder());
    expect(result.current.entries).toHaveLength(151);
    act(() => { h.disconnect(); h.reconnect(); });
    await waitFor(() => expect(h.fetch).toHaveBeenCalledTimes(4));
    await waitFor(() => expect(result.current.loadingOlder).toBe(false));
    expect(result.current.entries).toHaveLength(151);
    expect(result.current.entries[0]?.id).toBe("complete:50");
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 50 });
    expect(result.current.entries[0]?.id).toBe("complete:49");
  });
  it("bounds new reconnect entries and makes trimmed expanded history recoverable", async () => {
    const h = setup();
    h.fetch.mockResolvedValueOnce({ ...page(Array.from({ length: 150 }, (_, i) => entry("A", i + 51))), next_before: 51 })
      .mockResolvedValueOnce({ ...page([entry("A", 50)]), next_before: 50 })
      .mockResolvedValueOnce({ ...page([entry("A", 200), entry("A", 201)]), next_before: 200 })
      .mockResolvedValueOnce(page([entry("A", 50)]));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    await act(async () => result.current.loadOlder());
    act(() => { h.disconnect(); h.reconnect(); });
    await waitFor(() => expect(result.current.entries.at(-1)?.id).toBe("complete:201"));
    expect(result.current.entries).toHaveLength(151);
    expect(result.current.entries[0]?.id).toBe("complete:51");
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 51 });
  });
  it("resets a disconnected reconnect page and pages into the gap", async () => {
    const h = setup();
    h.fetch.mockResolvedValueOnce({ ...page([entry("A", 1)]), next_before: null })
      .mockResolvedValueOnce({ ...page(Array.from({ length: 50 }, (_, i) => entry("A", i + 102))), next_before: 102 })
      .mockResolvedValueOnce({ ...page([entry("A", 101)]), next_before: 101 });
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    act(() => h.reconnect());
    await waitFor(() => expect(result.current.entries[0]?.id).toBe("complete:102"));
    expect(result.current.hasOlder).toBe(true);
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 102 });
    expect(result.current.entries[0]?.id).toBe("complete:101");
  });
  it("does not trim an expanded window when an existing entry updates", async () => {
    const h = setup();
    h.fetch.mockResolvedValueOnce({ ...page(Array.from({ length: 150 }, (_, i) => entry("A", i + 51))), next_before: 51 })
      .mockResolvedValueOnce(page([entry("A", 50)]));
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    await waitFor(() => expect(result.current.ready).toBe(true));
    await act(async () => result.current.loadOlder());
    act(() => h.emit({ ...entry("A", 100), message: "updated" }));
    expect(result.current.entries).toHaveLength(151);
    expect(result.current.entries[0]?.id).toBe("complete:50");
  });
  it("fences A-B-A requests and clears immediately on deselection", async () => {
    const h = setup(); const first = deferred(); const second = deferred(); const third = deferred();
    h.fetch.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise).mockReturnValueOnce(third.promise);
    const { result, rerender, unmount } = renderHook(({ peer }) => useSelectedManagedActivity(h.client, scope, peer, h.live), { initialProps: { peer: "A" as string | undefined } });
    rerender({ peer: "B" }); rerender({ peer: "A" });
    await act(async () => { first.resolve(page([entry("A", 1)])); second.resolve(page([entry("B", 2)])); });
    expect(result.current.entries).toEqual([]); expect(result.current.ready).toBe(false);
    await act(async () => third.resolve(page([entry("A", 3)])));
    expect(result.current.entries.map((e) => e.id)).toEqual(["complete:3"]);
    rerender({ peer: undefined }); expect(result.current.entries).toEqual([]); expect(result.current.ready).toBe(true);
    expect(h.fetch).toHaveBeenCalledTimes(3); unmount();
  });
  it("settles after failure and retains live entries", async () => {
    const h = setup(); const request = deferred(); h.fetch.mockReturnValue(request.promise);
    const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
    act(() => h.emit(entry("A", 1)));
    await act(async () => request.reject(new Error("offline")));
    await waitFor(() => expect(result.current.ready).toBe(true));
    expect(result.current.entries.map((e) => e.id)).toEqual(["complete:1"]);
  });
});

it("preserves a live entry received before a disconnected reconnect page", async () => {
  const h = setup();
  h.fetch.mockResolvedValueOnce(page([entry("A", 1)]))
    .mockResolvedValueOnce({ ...page([entry("A", 100)]), next_before: 100 });
  const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
  await waitFor(() => expect(result.current.ready).toBe(true));
  act(() => h.emit({ ...entry("A", 2), message: "not durable yet" }));
  act(() => h.reconnect());
  await waitFor(() => expect(result.current.entries.some((item) => item.id === "complete:100")).toBe(true));
  expect(result.current.entries.some((item) => item.message === "not durable yet")).toBe(true);
});

it("retains the initial reconnect cursor and ignores the superseded response", async () => {
  const h = setup(); const initial = deferred(); const reconnect = deferred();
  h.fetch.mockReturnValueOnce(initial.promise).mockReturnValueOnce(reconnect.promise)
    .mockResolvedValueOnce(page([entry("A", 9)]));
  const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
  act(() => h.reconnect());
  await act(async () => reconnect.resolve({ ...page([entry("A", 10)]), next_before: 10 }));
  await act(async () => initial.resolve(page([entry("A", 1)])));
  expect(result.current.hasOlder).toBe(true);
  await act(async () => result.current.loadOlder());
  expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 10 });
  expect(result.current.entries.map((e) => e.id)).toEqual(["complete:9", "complete:10"]);
});

it("keeps a live update received before a later stale snapshot", async () => {
  const h = setup();
  h.fetch.mockResolvedValueOnce({ ...page([entry("A", 10)]), next_before: 10 })
    .mockResolvedValueOnce(page([{ ...entry("A", 10), message: "stale" }, entry("A", 9)]));
  const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
  await waitFor(() => expect(result.current.ready).toBe(true));
  act(() => h.emit({ ...entry("A", 10), message: "live" }));
  await act(async () => result.current.loadOlder());
  expect(result.current.entries.find((e) => e.id === "complete:10")?.message).toBe("live");
});

it("does not mistake post-disconnect activity for reconnect overlap", async () => {
  const h = setup(); const reconnect = deferred();
  h.fetch.mockResolvedValueOnce(page([entry("A", 1)]))
    .mockReturnValueOnce(reconnect.promise)
    .mockResolvedValueOnce(page([entry("A", 99)]));
  const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
  await waitFor(() => expect(result.current.ready).toBe(true));
  act(() => { h.disconnect(); h.emit(entry("A", 100)); h.reconnect(); });
  await act(async () => reconnect.resolve({ ...page([entry("A", 100), entry("A", 101)]), next_before: 100 }));
  expect(result.current.entries.map((e) => e.id)).toEqual(["complete:100", "complete:101"]);
  await act(async () => result.current.loadOlder());
  expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 100 });
});

it("recovers when snapshot plus live entries overflow without a live trim", async () => {
  const h = setup(); const request = deferred();
  h.fetch.mockReturnValueOnce(request.promise).mockResolvedValueOnce(page([entry("A", 50)]));
  const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
  act(() => { for (let i = 51; i <= 200; i++) h.emit(entry("A", i)); });
  await act(async () => request.resolve(page(Array.from({ length: 50 }, (_, i) => entry("A", i + 1)))));
  expect(result.current.entries).toHaveLength(150);
  expect(result.current.hasOlder).toBe(true);
  await act(async () => result.current.loadOlder());
  expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 51 });
});

it("applies repeated unversioned updates for an existing ID", async () => {
  const h = setup(); h.fetch.mockResolvedValueOnce(page([entry("A", 1)]));
  const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
  await waitFor(() => expect(result.current.ready).toBe(true));
  act(() => { h.emit({ ...entry("A", 1), message: "first" }); h.emit({ ...entry("A", 1), message: "second" }); });
  expect(result.current.entries[0]?.message).toBe("second");
});

it("reports an explicit retry when an overflowing live edge has no durable cursor", async () => {
  const h = setup(); h.fetch.mockResolvedValueOnce(page([]));
  const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
  await waitFor(() => expect(result.current.ready).toBe(true));
  act(() => { for (let i = 0; i < 151; i++) h.emit({ ...entry("A", i), id: `unversioned-${i}` }); });
  expect(result.current.entries).toHaveLength(150);
  expect(result.current.hasOlder).toBe(true);
  expect(result.current.error?.message).toMatch(/retry/i);
});

it("rejects an older page whose boundary was discarded while pending", async () => {
  const h = setup(); const pending = deferred();
  h.fetch.mockResolvedValueOnce({ ...page([entry("A", 10), entry("A", 11)]), next_before: 10 })
    .mockReturnValueOnce(pending.promise).mockResolvedValueOnce(page([entry("A", 10)]));
  const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
  await waitFor(() => expect(result.current.ready).toBe(true));
  let request!: Promise<void>;
  act(() => { request = result.current.loadOlder(); });
  act(() => result.current.discardBefore(new Set(["complete:11"])));
  await act(async () => { pending.resolve(page([entry("A", 9)])); await request; });
  expect(result.current.entries.map(e => e.id)).toEqual(["complete:11"]);
  await act(async () => result.current.loadOlder());
  expect(h.fetch).toHaveBeenLastCalledWith("A", "complete", { limit: 50, before: 11 });
  expect(result.current.entries.map(e => e.id)).toEqual(["complete:10", "complete:11"]);
});

it("does not resurrect trimmed live entries from an older initial snapshot", async () => {
  const h = setup(); const request = deferred(); h.fetch.mockReturnValueOnce(request.promise);
  const { result } = renderHook(() => useSelectedManagedActivity(h.client, scope, "A", h.live));
  act(() => { for (let i = 1; i <= 151; i++) h.emit({ ...entry("A", i), message: `live ${i}` }); });
  await act(async () => request.resolve(page([{ ...entry("A", 1), message: "stale" }])));
  expect(result.current.entries.some(e => e.id === "complete:1")).toBe(false);
  expect(result.current.hasOlder).toBe(true);
});
