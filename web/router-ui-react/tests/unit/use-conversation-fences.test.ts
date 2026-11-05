// @vitest-environment jsdom
import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useConversation } from "../../src/hooks/use-conversation";
import type { RouterTransport, TransportEvent } from "../../src/transport/router-transport";
import type { ConversationTimelineResponse } from "../../src/transport/http-client";
import type { UILiveClient } from "../../src/transport/ui-live-client";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
const scope = { routerUrl: "http://localhost:8765", agentId: "user" };
function page(agent: string, ids: string[], cursor: number | null = null): ConversationTimelineResponse {
  return { agent_id: agent, next_before_seq: cursor, events: ids.map((id, index) => ({ id, agent_id: agent, seq: index + 1, kind: "message", role: "assistant", content: id, created_at: 1700000000000 + index })) };
}
function setup() {
  let eventListener: ((event: TransportEvent) => void) | undefined;
  let liveListener: ((frame: "connected") => void) | undefined;
  const fetch = vi.fn<RouterTransport["http"]["fetchConversationTimeline"]>();
  const transport = { http: { fetchConversationTimeline: fetch }, conversation: vi.fn(), sendMessage: vi.fn(), subscribe: (listener: (event: TransportEvent) => void) => { eventListener = listener; return () => { eventListener = undefined; }; } } as unknown as RouterTransport;
  const live = { subscribe: (listener: (frame: "connected") => void) => { liveListener = listener; return () => { liveListener = undefined; }; } } as unknown as UILiveClient;
  return { transport, live, fetch, emit: (event: TransportEvent) => eventListener?.(event), reconnect: () => liveListener?.("connected") };
}

describe("conversation selection fences", () => {
  it("refetches and fences same-peer source switches", async () => {
    const h = setup(); const stale = deferred<ConversationTimelineResponse>(); const fresh = deferred<ConversationTimelineResponse>();
    h.fetch.mockResolvedValueOnce(page("A", ["old"], 7)).mockReturnValueOnce(stale.promise).mockReturnValueOnce(fresh.promise);
    const { result, rerender } = renderHook(({ selection }) => useConversation(h.transport, scope, "A", undefined, undefined, selection), { initialProps: { selection: "tmux:A" } });
    await waitFor(() => expect(result.current.timelineEvents[0]?.id).toBe("old"));
    act(() => { void result.current.loadOlder(); });
    rerender({ selection: "router-managed:A" });
    expect(result.current.timelineEvents).toEqual([]); expect(result.current.hasOlder).toBe(false); expect(result.current.loading).toBe(true);
    expect(h.fetch).toHaveBeenLastCalledWith("A", 50);
    await act(async () => stale.resolve(page("A", ["stale"], 3)));
    expect(result.current.timelineEvents).toEqual([]); expect(result.current.loading).toBe(true);
    await act(async () => fresh.resolve(page("A", ["fresh"])));
    expect(result.current.timelineEvents.map((event) => event.id)).toEqual(["fresh"]);
  });
  it("ignores stale A/B responses after returning to A", async () => {
    const h = setup(); const a1 = deferred<ConversationTimelineResponse>(); const b = deferred<ConversationTimelineResponse>(); const a2 = deferred<ConversationTimelineResponse>();
    h.fetch.mockReturnValueOnce(a1.promise).mockReturnValueOnce(b.promise).mockReturnValueOnce(a2.promise);
    const { result, rerender } = renderHook(({ peer }) => useConversation(h.transport, scope, peer), { initialProps: { peer: "A" } });
    rerender({ peer: "B" }); rerender({ peer: "A" });
    await act(async () => { a1.resolve(page("A", ["old"])); b.resolve(page("B", ["other"])); });
    expect(result.current.timelineEvents).toEqual([]); expect(result.current.loading).toBe(true);
    await act(async () => { a2.resolve(page("A", ["fresh"])); });
    expect(result.current.timelineEvents.map((e) => e.id)).toEqual(["fresh"]);
  });
  it("keeps live messages while reconnect immediately supersedes stalled snapshot", async () => {
    const h = setup(); const first = deferred<ConversationTimelineResponse>(); const repair = deferred<ConversationTimelineResponse>();
    h.fetch.mockReturnValueOnce(first.promise).mockReturnValueOnce(repair.promise);
    const callback = vi.fn();
    const { result } = renderHook(() => useConversation(h.transport, scope, "A", callback, h.live));
    act(() => { h.emit({ type: "conversation", message: { id: "live", from: "A", to: "user", body: "live", direction: "incoming" } }); h.reconnect(); });
    expect(h.fetch).toHaveBeenCalledTimes(2);
    await act(async () => { first.resolve(page("A", ["stale"])); });
    expect(result.current.timelineEvents.map((e) => e.id)).toEqual(["msg:A:live"]);
    expect(callback).not.toHaveBeenCalled();
    await act(async () => { repair.resolve(page("A", ["snapshot", "msg:A:live"])); });
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.liveTimelineIds.has("msg:A:live")).toBe(true);
  });
  it("restarts paging from a disconnected latest timeline page", async () => {
    const h = setup();
    h.fetch.mockResolvedValueOnce(page("A", ["old"]))
      .mockResolvedValueOnce(page("A", Array.from({ length: 50 }, (_, i) => `new${i}`), 102))
      .mockResolvedValueOnce(page("A", ["gap"], 101));
    const { result } = renderHook(() => useConversation(h.transport, scope, "A", undefined, h.live));
    await waitFor(() => expect(result.current.timelineEvents[0]?.id).toBe("old"));
    act(() => h.reconnect());
    await waitFor(() => expect(result.current.timelineEvents[0]?.id).toBe("new0"));
    expect(result.current.hasOlder).toBe(true);
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", 50, 102);
    expect(result.current.timelineEvents[0]?.id).toBe("gap");
  });
  it("does not use a post-disconnect live event as retained overlap", async () => {
    const h = setup(); const repair = deferred<ConversationTimelineResponse>();
    h.fetch.mockResolvedValueOnce(page("A", ["old"])).mockReturnValueOnce(repair.promise);
    const { result } = renderHook(() => useConversation(h.transport, scope, "A", undefined, h.live));
    await waitFor(() => expect(result.current.timelineEvents[0]?.id).toBe("old"));
    act(() => h.reconnect());
    act(() => h.emit({ type: "conversation", message: { id: "fresh", from: "A", to: "user", body: "fresh" } }));
    await act(async () => repair.resolve(page("A", ["new", "msg:A:fresh"], 42)));
    expect(result.current.timelineEvents.map((event) => event.id)).toEqual(["new", "msg:A:fresh"]);
    expect(result.current.hasOlder).toBe(true);
  });
  it("restarts legacy pagination when the latest page has a gap", async () => {
    const h = setup();
    h.fetch.mockRejectedValue(new Error("unsupported"));
    const conversation = vi.mocked(h.transport.conversation);
    conversation.mockResolvedValueOnce({ owner: "user", peer: "A", messages: [{ id: "old" }], high_water_sequence: 1, next_before: 5 });
    conversation.mockResolvedValueOnce({ owner: "user", peer: "A", messages: [{ id: "new" }], high_water_sequence: 20, next_before: 15 });
    conversation.mockResolvedValueOnce({ owner: "user", peer: "A", messages: [{ id: "gap" }], high_water_sequence: 20, next_before: null });
    const { result } = renderHook(() => useConversation(h.transport, scope, "A", undefined, h.live));
    await waitFor(() => expect(result.current.messages[0]?.id).toBe("old"));
    act(() => h.reconnect());
    await waitFor(() => expect(result.current.messages[0]?.id).toBe("new"));
    await act(async () => result.current.loadOlder());
    expect(conversation).toHaveBeenLastCalledWith("user", "A", { before: 15, limit: 50 });
    expect(result.current.messages.map((message) => message.id)).toContain("gap");
  });
  it("does not claim an unversioned delivery can be distinguished from a new update", async () => {
    const h = setup(); const held = deferred<ConversationTimelineResponse>();
    h.fetch.mockResolvedValueOnce(page("A", ["msg:A:old"])).mockResolvedValueOnce(page("B", [])).mockReturnValueOnce(held.promise);
    const { result, rerender } = renderHook(({ peer }) => useConversation(h.transport, scope, peer), { initialProps: { peer: "A" } });
    await waitFor(() => expect(result.current.timelineEvents).toHaveLength(1));
    rerender({ peer: "B" }); await waitFor(() => expect(result.current.loading).toBe(false));
    rerender({ peer: "A" });
    const old = { id: "old", from: "A", to: "user", body: "old", direction: "incoming" as const };
    const live = { id: "new", from: "A", to: "user", body: "new", direction: "incoming" as const };
    const receipt = { type: "ack" as const, message_id: "old", attempt_number: 1 };
    act(() => { h.emit({ type: "delivery", message: old, messages: [old], receipt }); h.emit({ type: "conversation", message: live }); });
    await act(async () => { held.resolve(page("A", [])); });
    act(() => h.emit({ type: "delivery", message: old, messages: [old], receipt }));
    expect(result.current.timelineEvents.map((event) => event.id)).toEqual(["msg:A:old", "msg:A:new"]);
  });
  it("keeps changed same-ID deliveries and normalizes sender and recipient", async () => {
    const h = setup(); h.fetch.mockResolvedValue(page("A", []));
    const { result } = renderHook(() => useConversation(h.transport, scope, "A"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    const receipt = { type: "ack" as const, message_id: "id", attempt_number: 1 };
    const message = { id: "id", sender: "A", recipient: "user", body: "first" };
    act(() => { h.emit({ type: "delivery", message, messages: [message], receipt }); h.emit({ type: "delivery", message: { ...message, body: "updated" }, messages: [], receipt }); });
    expect(result.current.timelineEvents).toHaveLength(1);
    expect(result.current.timelineEvents[0]?.content).toBe("updated");
  });
  it("prefers live content to an overlapping stale snapshot", async () => {
    const h = setup(); const pending = deferred<ConversationTimelineResponse>(); h.fetch.mockReturnValue(pending.promise);
    const { result } = renderHook(() => useConversation(h.transport, scope, "A"));
    act(() => h.emit({ type: "conversation", message: { id: "live0", from: "A", to: "user", body: "new0" } }));
    await act(async () => { pending.resolve(page("A", ["msg:A:live0", "snapshot"])); });
    expect(result.current.timelineEvents).toHaveLength(2);
    expect(result.current.timelineEvents.find((event) => event.id === "msg:A:live0")?.content).toBe("new0");
  });
  it("bounds an overlapping snapshot and live merge by trimming the oldest events", async () => {
    const h = setup(); const pending = deferred<ConversationTimelineResponse>(); h.fetch.mockReturnValue(pending.promise);
    const { result } = renderHook(() => useConversation(h.transport, scope, "A"));
    act(() => { for (let i = 0; i < 150; i++) h.emit({ type: "conversation", message: { id: `live${i}`, from: "A", to: "user", body: `new${i}` } }); });
    const snapshot = page("A", ["msg:A:live0", ...Array.from({ length: 49 }, (_, i) => `snapshot${i}`)]);
    await act(async () => { pending.resolve(snapshot); });
    expect(result.current.timelineEvents).toHaveLength(150);
    expect(result.current.timelineEvents.some((event) => event.id === "msg:A:live0")).toBe(false);
    expect(result.current.timelineEvents.find((event) => event.id === "msg:A:live149")?.content).toBe("new149");
    expect(result.current.messages.length).toBeLessThanOrEqual(150);
    expect(result.current.hasOlder).toBe(true);
  });
  it("does not restore an old page or let its late pagination change a returned selection", async () => {
    const h = setup(); const older = deferred<ConversationTimelineResponse>(); const returned = deferred<ConversationTimelineResponse>();
    h.fetch.mockResolvedValueOnce(page("A", ["previous"], 10)).mockReturnValueOnce(older.promise)
      .mockResolvedValueOnce(page("B", ["other"])).mockReturnValueOnce(returned.promise);
    const { result, rerender } = renderHook(({ peer }) => useConversation(h.transport, scope, peer), { initialProps: { peer: "A" } });
    await waitFor(() => expect(result.current.hasOlder).toBe(true));
    let pending!: Promise<void>; act(() => { pending = result.current.loadOlder(); });
    rerender({ peer: "B" });
    await waitFor(() => expect(result.current.timelineEvents.map((event) => event.id)).toEqual(["other"]));
    rerender({ peer: "A" });
    expect(result.current.timelineEvents).toEqual([]);
    expect(result.current.loading).toBe(true);
    await act(async () => { older.resolve(page("A", ["old-page"], 5)); await pending; });
    expect(result.current.timelineEvents).toEqual([]);
    expect(result.current.loading).toBe(true);
    expect(result.current.loadingOlder).toBe(false);
    await act(async () => returned.resolve(page("A", ["fresh"], 8)));
    expect(result.current.timelineEvents.map((event) => event.id)).toEqual(["fresh"]);
    expect(result.current.hasOlder).toBe(true);
    expect(h.fetch).toHaveBeenCalledTimes(4);
  });
  it("invalidates a pending page when a non-oldest event is discarded", async () => {
    const h = setup(); const older = deferred<ConversationTimelineResponse>(); const commit = vi.fn();
    h.fetch.mockResolvedValueOnce(page("A", ["oldest", "hidden", "visible"], 1))
      .mockReturnValueOnce(older.promise).mockResolvedValueOnce(page("A", ["recovered"], null));
    const { result } = renderHook(() => useConversation(h.transport, scope, "A"));
    await waitFor(() => expect(result.current.timelineEvents).toHaveLength(3));
    let pending!: Promise<void>; act(() => { pending = result.current.loadOlder(commit); });
    act(() => result.current.discardBefore(new Set(["oldest", "visible"])));
    expect(result.current.loadingOlder).toBe(false);
    await act(async () => { older.resolve(page("A", ["stale"], null)); await pending; });
    expect(commit).not.toHaveBeenCalled();
    expect(result.current.timelineEvents.map((event) => event.id)).toEqual(["oldest", "visible"]);
    expect(result.current.hasOlder).toBe(true);
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", 50, 3);
    expect(result.current.timelineEvents.map((event) => event.id)).toContain("recovered");
  });
  it("retains the discard cursor when refresh returns an empty latest page", async () => {
    const h = setup();
    h.fetch.mockResolvedValueOnce(page("A", ["hidden"]))
      .mockResolvedValueOnce(page("A", []))
      .mockResolvedValueOnce(page("A", ["recovered"]));
    const { result } = renderHook(() => useConversation(h.transport, scope, "A"));
    await waitFor(() => expect(result.current.timelineEvents).toHaveLength(1));
    act(() => result.current.discardBefore(new Set()));
    await act(async () => result.current.refresh());
    expect(result.current.hasOlder).toBe(true);
    await act(async () => result.current.loadOlder());
    expect(h.fetch).toHaveBeenLastCalledWith("A", 50, 2);
    expect(result.current.timelineEvents.map((event) => event.id)).toEqual(["recovered"]);
  });
  it("ignores pagination after selection changes", async () => {
    const h = setup(); const older = deferred<ConversationTimelineResponse>();
    h.fetch.mockResolvedValueOnce(page("A", ["recent"], 10)).mockReturnValueOnce(older.promise).mockResolvedValueOnce(page("B", ["B"]));
    const { result, rerender } = renderHook(({ peer }) => useConversation(h.transport, scope, peer), { initialProps: { peer: "A" } });
    await waitFor(() => expect(result.current.hasOlder).toBe(true));
    let pending!: Promise<void>; act(() => { pending = result.current.loadOlder(); });
    rerender({ peer: "B" });
    await act(async () => { older.resolve(page("A", ["old"])); await pending; });
    await waitFor(() => expect(result.current.timelineEvents.map((e) => e.id)).toEqual(["B"]));
    expect(result.current.loadingOlder).toBe(false);
  });
});
