// @vitest-environment jsdom

import { describe, expect, it, vi } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useConversation } from "../../src/hooks/use-conversation";
import { CONVERSATION_UNLOAD_THRESHOLD } from "../../src/domain/history";
import type { RouterTransport, TransportEvent } from "../../src/transport/router-transport";
import type { HistoryMessage } from "../../src/domain/history";

describe("useConversation natural unloading", () => {
  function createMockTransport() {
    let subscriber: ((event: TransportEvent) => void) | undefined;
    const transport = {
      http: {
        fetchConversationTimeline: vi.fn().mockResolvedValue({
          agent_id: "agent-1",
          events: [],
          next_before_seq: null,
        }),
      },
      conversation: vi.fn().mockResolvedValue({
        messages: [],
        high_water_sequence: 0,
        next_before: null,
      }),
      subscribe: vi.fn((cb: (event: TransportEvent) => void) => {
        subscriber = cb;
        return () => { subscriber = undefined; };
      }),
      sendMessage: vi.fn().mockResolvedValue({}),
    } as unknown as RouterTransport;

    return {
      transport,
      emit(event: TransportEvent) {
        subscriber?.(event);
      },
    };
  }

  const scope = {
    routerUrl: "http://127.0.0.1:8765",
    agentId: "user",
  };

  it("naturally unloads messages and timelineEvents to CONVERSATION_UNLOAD_THRESHOLD (150) when live count exceeds it", async () => {
    const { transport, emit } = createMockTransport();
    const onReconcile = vi.fn();

    const { result } = renderHook(() =>
      useConversation(transport, scope, "agent-1", onReconcile)
    );

    // Initial state
    expect(result.current.messages).toHaveLength(0);
    expect(result.current.timelineEvents).toHaveLength(0);

    // Wait for the hook's initial refresh() effect to settle
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });

    const baseNow = Date.now();

    // Emit 160 live inbound messages via WebSocket
    act(() => {
      for (let i = 1; i <= 160; i++) {
        const msg: HistoryMessage = {
          id: `msg-${i}`,
          body: `Live message ${i}`,
          direction: "incoming",
          from: "agent-1",
          to: "user",
          sequence: i,
          created_at: new Date(baseNow + i * 1000).toISOString(),
        };
        emit({
          type: "conversation",
          message: msg,
        });
      }
    });

    // Both messages and timelineEvents should be sliced to exactly 150
    expect(result.current.messages).toHaveLength(CONVERSATION_UNLOAD_THRESHOLD);
    expect(result.current.timelineEvents).toHaveLength(CONVERSATION_UNLOAD_THRESHOLD);

    // Oldest items (1..10) are trimmed; in-memory window spans 11..160
    expect(result.current.messages[0]!.id).toBe("msg-11");
    expect(result.current.messages[149]!.id).toBe("msg-160");
    expect(result.current.timelineEvents[0]!.seq).toBe(0);
    expect(result.current.timelineEvents[149]!.seq).toBe(0);

    // hasOlder remains true because oldest item sequence (11) > 1
    expect(result.current.hasOlder).toBe(true);
  });

  it("loadOlder prepends older chunks without trimming from the bottom", async () => {
    let subscriber: ((event: TransportEvent) => void) | undefined;
    const fetchConversationTimelineMock = vi.fn();

    const transport = {
      http: {
        fetchConversationTimeline: fetchConversationTimelineMock,
      },
      conversation: vi.fn(),
      subscribe: vi.fn((cb: (event: TransportEvent) => void) => {
        subscriber = cb;
        return () => { subscriber = undefined; };
      }),
      sendMessage: vi.fn(),
    } as unknown as RouterTransport;

    const baseNow = Date.now();

    // Initial timeline load returns items 51..100
    const initialEvents = Array.from({ length: 50 }, (_, i) => ({
      id: `evt-${i + 51}`,
      agent_id: "agent-1",
      seq: i + 51,
      kind: "message" as const,
      role: "assistant" as const,
      content: `Message ${i + 51}`,
      created_at: baseNow + (i + 51) * 1000,
    }));

    fetchConversationTimelineMock.mockResolvedValueOnce({
      agent_id: "agent-1",
      events: initialEvents,
      next_before_seq: 51,
    });

    const { result } = renderHook(() =>
      useConversation(transport, scope, "agent-1")
    );

    // Initial load happens asynchronously in useEffect -> refresh()
    await act(async () => {
      await new Promise((r) => setTimeout(r, 10));
    });

    expect(result.current.timelineEvents).toHaveLength(50);
    expect(result.current.hasOlder).toBe(true);

    // Next call to loadOlder returns items 1..50
    const olderEvents = Array.from({ length: 50 }, (_, i) => ({
      id: `evt-${i + 1}`,
      agent_id: "agent-1",
      seq: i + 1,
      kind: "message" as const,
      role: "assistant" as const,
      content: `Message ${i + 1}`,
      created_at: baseNow + (i + 1) * 1000,
    }));

    fetchConversationTimelineMock.mockResolvedValueOnce({
      agent_id: "agent-1",
      events: olderEvents,
      next_before_seq: null,
    });

    await act(async () => {
      await result.current.loadOlder();
    });

    // Now all 100 items are present: prepended without trimming bottom
    expect(result.current.timelineEvents).toHaveLength(100);
    expect(result.current.timelineEvents[0]!.seq).toBe(1);
    expect(result.current.timelineEvents[99]!.seq).toBe(100);
  });

  it("updates an existing message in-place without dropping other messages when at threshold", async () => {
    const { transport, emit } = createMockTransport();
    const onReconcile = vi.fn();

    const { result } = renderHook(() =>
      useConversation(transport, scope, "agent-1", onReconcile)
    );

    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });

    const baseNow = Date.now();

    // Fill to exactly CONVERSATION_UNLOAD_THRESHOLD (150 messages)
    act(() => {
      for (let i = 1; i <= CONVERSATION_UNLOAD_THRESHOLD; i++) {
        emit({
          type: "conversation",
          message: {
            id: `msg-${i}`,
            body: `Initial body ${i}`,
            direction: "incoming",
            from: "agent-1",
            to: "user",
            sequence: i,
            created_at: new Date(baseNow + i * 1000).toISOString(),
          },
        });
      }
    });

    expect(result.current.messages).toHaveLength(CONVERSATION_UNLOAD_THRESHOLD);
    expect(result.current.messages[0]!.id).toBe("msg-1");
    expect(result.current.messages[149]!.id).toBe("msg-150");

    // Update an existing message in-place (e.g. msg-50 status or body update)
    act(() => {
      emit({
        type: "conversation",
        message: {
          id: "msg-50",
          body: "Updated body 50",
          direction: "incoming",
          from: "agent-1",
          to: "user",
          sequence: 50,
          created_at: new Date(baseNow + 50 * 1000).toISOString(),
        },
      });
    });

    // Length should remain exactly CONVERSATION_UNLOAD_THRESHOLD and msg-1 should NOT be dropped
    expect(result.current.messages).toHaveLength(CONVERSATION_UNLOAD_THRESHOLD);
    expect(result.current.messages[0]!.id).toBe("msg-1");
    expect(result.current.messages[49]!.id).toBe("msg-50");
    expect(result.current.messages[49]!.body).toBe("Updated body 50");
    expect(result.current.messages[149]!.id).toBe("msg-150");
  });

  it("verifies hasOlder becomes false when oldestEventSeq is 1 and cursor is 0/null", async () => {
    let subscriber: ((event: TransportEvent) => void) | undefined;
    const fetchConversationTimelineMock = vi.fn();

    const transport = {
      http: {
        fetchConversationTimeline: fetchConversationTimelineMock,
      },
      conversation: vi.fn(),
      subscribe: vi.fn((cb: (event: TransportEvent) => void) => {
        subscriber = cb;
        return () => { subscriber = undefined; };
      }),
      sendMessage: vi.fn(),
    } as unknown as RouterTransport;

    const baseNow = Date.now();

    // Timeline returns events starting from seq 1 and next_before_seq is null / 0
    const eventsStartingAtOne = [
      {
        id: "evt-1",
        agent_id: "agent-1",
        seq: 1,
        kind: "message" as const,
        role: "assistant" as const,
        content: "First message ever",
        created_at: baseNow,
      },
      {
        id: "evt-2",
        agent_id: "agent-1",
        seq: 2,
        kind: "message" as const,
        role: "assistant" as const,
        content: "Second message",
        created_at: baseNow + 1000,
      },
    ];

    fetchConversationTimelineMock.mockResolvedValueOnce({
      agent_id: "agent-1",
      events: eventsStartingAtOne,
      next_before_seq: null,
    });

    const { result } = renderHook(() =>
      useConversation(transport, scope, "agent-1")
    );

    await act(async () => {
      await new Promise((r) => setTimeout(r, 10));
    });

    expect(result.current.timelineEvents).toHaveLength(2);
    expect(result.current.timelineEvents[0]!.seq).toBe(1);
    expect(result.current.hasOlder).toBe(false);
  });
});


describe("trimmed boundary recovery", () => {
  it("requests the retained event once and preserves the window on failure and retry", async () => {
    let subscriber: ((event: TransportEvent) => void) | undefined;
    const fetch = vi.fn().mockResolvedValueOnce({ events: [], next_before_seq: null });
    const transport = { http: { fetchConversationTimeline: fetch }, conversation: vi.fn(), subscribe: (cb: (event: TransportEvent) => void) => { subscriber = cb; return () => {}; }, sendMessage: vi.fn() } as unknown as RouterTransport;
    const { result } = renderHook(() => useConversation(transport, { routerUrl: "http://localhost", agentId: "user" }, "agent-1"));
    await act(async () => {});
    act(() => { for (let i = 1; i <= 160; i++) subscriber?.({ type: "conversation", message: { id: `m${i}`, body: `m${i}`, direction: "incoming", from: "agent-1", to: "user", sequence: i, created_at: new Date().toISOString() } }); });
    fetch.mockRejectedValueOnce(new Error("not persisted"));
    await act(async () => { await result.current.loadOlder(); });
    expect(result.current.timelineEvents).toHaveLength(150);
    expect(result.current.hasOlder).toBe(true);
    expect(result.current.error?.message).toMatch(/retry/i);
    fetch.mockResolvedValueOnce({ events: [{ id: "older", agent_id: "agent-1", seq: 10, kind: "message", created_at: 1 }], next_before_seq: 10 });
    await act(async () => { await result.current.loadOlder(); });
    expect(fetch).toHaveBeenLastCalledWith("agent-1", 50, undefined, "msg:agent-1:m11");
    expect(result.current.timelineEvents).toHaveLength(151);
    expect(result.current.timelineEvents[0]?.id).toBe("older");
    expect(result.current.messages.some((message) => message.id === "older")).toBe(true);
    expect(result.current.timelineEvents[150]?.id).toBe("msg:agent-1:m160");
  });
});

 it("never sends a timeline cursor to legacy history after a timeline page fails", async () => {
    const fetch = vi.fn().mockResolvedValueOnce({ events: [{ id: "e", agent_id: "agent-1", seq: 100, kind: "message", created_at: 1 }], next_before_seq: 100 }).mockRejectedValueOnce(new Error("offline")).mockResolvedValueOnce({ events: [], next_before_seq: null });
    const conversation = vi.fn();
    const transport = { http: { fetchConversationTimeline: fetch }, conversation, subscribe: () => () => {}, sendMessage: vi.fn() } as unknown as RouterTransport;
    const { result } = renderHook(() => useConversation(transport, { routerUrl: "http://localhost", agentId: "user" }, "agent-1"));
    await act(async () => {});
    await act(async () => result.current.loadOlder());
    expect(conversation).not.toHaveBeenCalled();
    expect(result.current.hasOlder).toBe(true);
    expect(result.current.error?.message).toMatch(/retry/i);
    await act(async () => result.current.loadOlder());
    expect(fetch).toHaveBeenLastCalledWith("agent-1", 50, 100);
 });

it("retries recovery when a second live trim removes the pending boundary", async () => {
  let subscriber: ((event: TransportEvent) => void) | undefined;
  let resolve!: (value: { events: never[]; next_before_seq: null }) => void;
  const pending = new Promise<{ events: never[]; next_before_seq: null }>((done) => { resolve = done; });
  const fetch = vi.fn().mockResolvedValueOnce({ events: [], next_before_seq: null }).mockReturnValueOnce(pending).mockResolvedValueOnce({ events: [], next_before_seq: null });
  const transport = { http: { fetchConversationTimeline: fetch }, conversation: vi.fn(), subscribe: (cb: (event: TransportEvent) => void) => { subscriber = cb; return () => {}; }, sendMessage: vi.fn() } as unknown as RouterTransport;
  const { result } = renderHook(() => useConversation(transport, { routerUrl: "http://localhost", agentId: "user" }, "agent-1"));
  await act(async () => {});
  const emit = (i: number) => subscriber?.({ type: "conversation", message: { id: `m${i}`, body: `m${i}`, direction: "incoming", from: "agent-1", to: "user", sequence: i, created_at: new Date().toISOString() } });
  act(() => { for (let i = 1; i <= 160; i++) emit(i); });
  const anchor = vi.fn();
  let loading!: Promise<void>;
  act(() => { loading = result.current.loadOlder(anchor); });
  act(() => emit(161));
  await act(async () => { resolve({ events: [], next_before_seq: null }); await loading; });
  expect(anchor).not.toHaveBeenCalled();
  expect(result.current.hasOlder).toBe(true);
  await act(async () => result.current.loadOlder(anchor));
  expect(fetch).toHaveBeenLastCalledWith("agent-1", 50, undefined, "msg:agent-1:m12");
  expect(anchor).toHaveBeenCalledTimes(1);
});
