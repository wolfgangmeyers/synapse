// @vitest-environment jsdom
import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { useConversation } from "../../src/hooks/use-conversation";
import type { RouterTransport } from "../../src/transport/router-transport";
import type { ConversationTimelineResponse } from "../../src/transport/http-client";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

function page(id: string): ConversationTimelineResponse {
  return { agent_id: "worker", next_before_seq: null, events: [{ id, agent_id: "worker", seq: 1, kind: "message", role: "assistant", content: id, created_at: 1700000000000 }] };
}

describe("conversation refresh effect cleanup", () => {
  it("restarts an in-flight refresh when the reconciliation callback changes", async () => {
    const first = deferred<ConversationTimelineResponse>();
    const replacement = deferred<ConversationTimelineResponse>();
    const fetch = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(replacement.promise);
    const transport = { http: { fetchConversationTimeline: fetch }, subscribe: () => () => {} } as unknown as RouterTransport;
    const scope = { routerUrl: "http://localhost:8765", agentId: "user" };
    const oldCallback = vi.fn();
    const newCallback = vi.fn();
    const { result, rerender } = renderHook(({ callback }) => useConversation(transport, scope, "worker", callback), { initialProps: { callback: oldCallback } });

    rerender({ callback: newCallback });
    expect(fetch).toHaveBeenCalledTimes(2);
    await act(async () => { first.resolve(page("stale")); });
    expect(result.current.timelineEvents).toEqual([]);
    expect(result.current.loading).toBe(true);
    await act(async () => { replacement.resolve(page("fresh")); });
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.timelineEvents.map((event) => event.id)).toEqual(["fresh"]);
    expect(oldCallback).not.toHaveBeenCalled();
    expect(newCallback).toHaveBeenCalledTimes(1);
  });
  it("clears loading when a pending peer is deselected", async () => {
    const pending = deferred<ConversationTimelineResponse>();
    const transport = { http: { fetchConversationTimeline: () => pending.promise }, subscribe: () => () => {} } as unknown as RouterTransport;
    const scope = { routerUrl: "http://localhost:8765", agentId: "user" };
    const { result, rerender } = renderHook(({ peer }) => useConversation(transport, scope, peer), { initialProps: { peer: "worker" as string | undefined } });
    expect(result.current.loading).toBe(true);
    rerender({ peer: undefined });
    await waitFor(() => expect(result.current.loading).toBe(false));
  });
  it("retains live messages across a queued refresh whose snapshot omits them", async () => {
    const first = deferred<ConversationTimelineResponse>(); const second = deferred<ConversationTimelineResponse>();
    const fetch = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise);
    let listener: (event: any) => void = () => {};
    const transport = { http: { fetchConversationTimeline: fetch }, subscribe: (fn: typeof listener) => { listener = fn; return () => {}; } } as unknown as RouterTransport;
    const scope = { routerUrl: "http://localhost:8765", agentId: "user" };
    const { result } = renderHook(() => useConversation(transport, scope, "worker"));
    act(() => { listener({ type: "conversation", message: { id: "live", from: "worker", to: "user", body: "hello" } }); void result.current.refresh(); });
    await act(async () => first.resolve(page("first")));
    expect(fetch).toHaveBeenCalledTimes(2);
    await act(async () => second.resolve(page("second")));
    expect(result.current.timelineEvents.map((event) => event.id)).toContain("msg:worker:live");
    expect(result.current.messages.map((message) => message.id)).toContain("live");
  });
  it("retains an expanded window and preexisting live message on refresh", async () => {
    const latest = { ...page("new"), next_before_seq: 5 };
    const fetch = vi.fn().mockResolvedValueOnce(latest).mockResolvedValueOnce(page("old")).mockResolvedValueOnce(latest);
    let listener: (event: any) => void = () => {};
    const transport = { http: { fetchConversationTimeline: fetch }, subscribe: (fn: typeof listener) => { listener = fn; return () => {}; } } as unknown as RouterTransport;
    const scope = { routerUrl: "http://localhost:8765", agentId: "user" };
    const { result } = renderHook(() => useConversation(transport, scope, "worker"));
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () => result.current.loadOlder());
    act(() => listener({ type: "conversation", message: { id: "live", from: "worker", to: "user", body: "hello" } }));
    await act(async () => result.current.refresh());
    expect(result.current.timelineEvents.map((e) => e.id)).toEqual(expect.arrayContaining(["old", "new", "msg:worker:live"]));
    expect(result.current.messages.map((m) => m.id)).toContain("live");
  });

});
