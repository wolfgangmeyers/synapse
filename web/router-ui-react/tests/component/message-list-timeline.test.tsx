// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MessageList } from "../../src/components/MessageList";
import type { HistoryMessage } from "../../src/domain/history";
import type { ManagedHistoryEntry } from "../../src/protocol/types";
import { fetchConversationTimeline, type ConversationTimelineEvent } from "../../src/services/api";

afterEach(cleanup);

describe("MessageList", () => {
  it("offers initial history retry without hiding live content", () => {
    const retry = vi.fn();
    const { rerender } = render(<MessageList peerId="agent-1" timelineEvents={[]} messages={[]} error="History request timed out" onRetryHistory={retry} />);
    fireEvent.click(screen.getByRole("button", { name: "Retry history" }));
    expect(retry).toHaveBeenCalledTimes(1);
    rerender(<MessageList peerId="agent-1" timelineEvents={[]} messages={[{ id: "live", body: "Live content", direction: "incoming" }]} error="History request timed out" onRetryHistory={retry} />);
    expect(screen.getByText("Live content")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Retry history" }));
    expect(retry).toHaveBeenCalledTimes(2);
  });
  it("bounds combined historical items, preserves live and pending, and reveals deferred before fetching", async () => {
    const time = Date.parse("2026-01-01T00:00:00Z");
    const timeline: ConversationTimelineEvent[] = Array.from({ length: 40 }, (_, i) => ({ id: `msg:agent-1:t${i}`, agent_id: "agent-1", kind: "message", role: "assistant", seq: i + 1, created_at: time + i * 2000, content: `timeline-${i}` }));
    const activity: ManagedHistoryEntry[] = Array.from({ length: 40 }, (_, i) => ({ id: `a${i}`, agent_id: "agent-1", at: new Date(time + i * 2000 + 1000).toISOString(), presentation: { kind: "tool", tool_name: "bash", summary: `activity-${i}`, outcome: "success" } }));
    const pending: HistoryMessage = { id: "pending", body: "pending text", direction: "outgoing", pending: true };
    const load = vi.fn(async () => {});
    const props = { peerId: "agent-1", timelineEvents: timeline, activityEntries: activity, messages: [pending], liveTimelineIds: new Set(["msg:agent-1:t0"]), liveActivityIds: new Set(["a0"]), onLoadOlder: load, hasOlder: true };
    const { rerender } = render(<MessageList {...props} historyWindowReady={false} />);
    expect(screen.getByText("timeline-0")).toBeInTheDocument();
    expect(screen.getByText("activity-0")).toBeInTheDocument();
    expect(screen.getByText("pending text")).toBeInTheDocument();
    expect(screen.queryByText("timeline-39")).not.toBeInTheDocument();
    rerender(<MessageList {...props} historyWindowReady />);
    expect(screen.queryByText("timeline-1")).not.toBeInTheDocument();
    expect(screen.getByText("timeline-39")).toBeInTheDocument();
    expect(screen.getByText("activity-39")).toBeInTheDocument();
    expect(document.querySelectorAll("[data-scroll-anchor]")).toHaveLength(53);
    await act(async () => fireEvent.click(screen.getByRole("button", { name: /load older history/i })));
    expect(screen.getByText("timeline-1")).toBeInTheDocument();
    expect(load).not.toHaveBeenCalled();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: /load older history/i })));
    expect(load).toHaveBeenCalledTimes(1);
    rerender(<MessageList {...props} peerId="other" historyWindowReady />);
    rerender(<MessageList {...props} historyWindowReady />);
    expect(screen.queryByText("timeline-1")).not.toBeInTheDocument();
  });
  it("releases hidden source candidates without rendered overflow", () => {
    const discard = vi.fn();
    const hidden: ManagedHistoryEntry = { id: "complete:1", agent_id: "agent-1", message: "started", presentation: { kind: "lifecycle" } };
    render(<MessageList peerId="agent-1" timelineEvents={[]} activityEntries={[hidden]} historyWindowReady onDiscardHistoryBefore={discard} />);
    expect(discard).toHaveBeenCalled();
    expect(discard.mock.lastCall?.[0].has("complete:1")).toBe(false);
  });
  it("does not expand the window when older loading fails before commit", async () => {
    const timeline: ConversationTimelineEvent[] = Array.from({ length: 60 }, (_, i) => ({ id: `item-${i}`, agent_id: "agent-1", kind: "message", role: "assistant", seq: i + 1, created_at: i + 1, content: `item ${i}` }));
    const discard = vi.fn();
    const load = vi.fn(async () => {});
    render(<MessageList peerId="agent-1" timelineEvents={timeline} historyWindowReady onDiscardHistoryBefore={discard} onLoadOlder={load} hasOlder />);
    expect(screen.queryByText("item 0")).not.toBeInTheDocument();
    await act(async () => fireEvent.click(screen.getByRole("button", { name: /load older history/i })));
    expect(load).toHaveBeenCalledTimes(1);
    expect(screen.queryByText("item 0")).not.toBeInTheDocument();
  });
  it("allows paging when all current entries are hidden", () => {
    const load = vi.fn();
    render(<MessageList peerId="agent-1" timelineEvents={[]} activityEntries={[]} hasOlder onLoadOlder={load} />);
    fireEvent.click(screen.getByRole("button", { name: /load older history/i }));
    expect(load).toHaveBeenCalled();
  });
  it("renders a message list with correct roles", () => {
    const messages: HistoryMessage[] = Array.from({ length: 50 }, (_, i) => ({
      id: `msg-${i}`,
      body: `Message body content ${i}`,
      direction: i % 2 === 0 ? "incoming" : "outgoing",
      from: i % 2 === 0 ? "agent-1" : "user",
      to: i % 2 === 0 ? "user" : "agent-1",
    }));

    const { container } = render(
      <MessageList
        peerId="agent-1"
        messages={messages}
      />
    );

    // Should render the timeline
    const timeline = container.querySelector("ol.complete-timeline");
    expect(timeline).toBeInTheDocument();

    // Verify messages are rendered
    expect(screen.getByText("Message body content 49")).toBeInTheDocument();
  });

  it("renders managed-only activity independently of the timeline boundary", () => {
    const timestamp = Date.parse("2026-10-27T10:00:00Z");
    const snapshot: ManagedHistoryEntry = { id: "snapshot", agent_id: "agent-1", at: new Date(timestamp).toISOString(), presentation: { kind: "tool", tool_name: "bash", summary: "old snapshot", outcome: "success" } };
    const live: ManagedHistoryEntry = { id: "live", agent_id: "agent-1", at: new Date(timestamp + 2000).toISOString(), presentation: { kind: "tool", tool_name: "bash", summary: "live activity", outcome: "success" } };
    const ids = new Set(["live"]);
    const { rerender } = render(<MessageList peerId="agent-1" timelineEvents={[]} loading activityEntries={[snapshot, live]} liveActivityIds={ids} />);
    expect(screen.getByText("old snapshot")).toBeInTheDocument();
    expect(screen.getByText("live activity")).toBeInTheDocument();
    const timeline: ConversationTimelineEvent[] = [{ id: "boundary", agent_id: "agent-1", kind: "message", seq: 1, created_at: timestamp + 1000, content: "boundary", metadata: {} }];
    rerender(<MessageList peerId="agent-1" timelineEvents={timeline} loading={false} activityEntries={[snapshot, live]} liveActivityIds={ids} />);
    expect(screen.getByText("old snapshot")).toBeInTheDocument();
    expect(screen.getByText("live activity")).toBeInTheDocument();
  });

  it("keeps a live update even when its original timestamp predates the timeline cutoff", () => {
    const timestamp = Date.parse("2026-10-27T10:00:00Z");
    const live: ManagedHistoryEntry = { id: "active", agent_id: "agent-1", at: new Date(timestamp).toISOString(), presentation: { kind: "tool", tool_name: "bash", summary: "still active", outcome: "active" } };
    const timeline: ConversationTimelineEvent[] = [{ id: "boundary", agent_id: "agent-1", kind: "message", seq: 1, created_at: timestamp + 1000, content: "boundary", metadata: {} }];
    render(<MessageList peerId="agent-1" timelineEvents={timeline} activityEntries={[live]} liveActivityIds={new Set(["active"])} />);
    expect(screen.getByText("still active")).toBeInTheDocument();
  });

  it("replaces a stale timeline activity with its live update without duplication", () => {
    const timestamp = Date.parse("2026-10-27T10:00:00Z");
    const timeline: ConversationTimelineEvent[] = [{ id: "hist:complete:1", agent_id: "agent-1", kind: "activity", seq: 1, created_at: timestamp, content: "stale snapshot", metadata: { presentation: { kind: "tool", tool_name: "bash", summary: "stale snapshot" } } }];
    const live: ManagedHistoryEntry = { id: "1", agent_id: "agent-1", at: new Date(timestamp).toISOString(), presentation: { kind: "tool", tool_name: "bash", summary: "live update", outcome: "success" } };
    render(<MessageList peerId="agent-1" timelineEvents={timeline} activityEntries={[live]} liveActivityIds={new Set(["1"])} />);
    expect(screen.getByText("live update")).toBeInTheDocument();
    expect(screen.queryByText("stale snapshot")).not.toBeInTheDocument();
  });

  it("renders both messages and timeline activity entries in the virtual list", () => {
    const messages: HistoryMessage[] = [
      { id: "msg-1", body: "Hello from user", direction: "outgoing", from: "user", to: "agent-1" },
    ];

    const activityEntries: ManagedHistoryEntry[] = [
      {
        id: "complete:1",
        agent_id: "agent-1",
        at: "2026-10-27T10:00:00.000Z",
        message: "Tool invoked",
        presentation: {
          kind: "tool",
          tool_name: "bash",
          outcome: "success",
          summary: "Ran bash command",
        },
      },
      {
        id: "complete:2",
        agent_id: "agent-1",
        at: "2026-10-27T10:01:00.000Z",
        message: "Context reset",
        presentation: {
          kind: "context_reset",
        },
      },
    ];

    const { container } = render(
      <MessageList
        peerId="agent-1"
        messages={messages}
        activityEntries={activityEntries}
      />
    );

    // MessageBubble rendered
    expect(screen.getByText("Hello from user")).toBeInTheDocument();

    // CompleteTimelineEntry for tool activity rendered
    expect(screen.getByText("Ran bash command")).toBeInTheDocument();

    // Context reset entry rendered
    const contextResetItem = container.querySelector(".timeline-item--context-reset");
    expect(contextResetItem).toBeInTheDocument();
  });

  it("anchors scroll to bottom when near bottom (<= 180px)", () => {
    const messages: HistoryMessage[] = [
      { id: "msg-1", body: "First message" },
      { id: "msg-2", body: "Second message" },
    ];

    const { container, rerender } = render(
      <MessageList
        peerId="agent-1"
        messages={messages}
      />
    );

    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Messages container not found");

    Object.defineProperty(list, "clientHeight", { configurable: true, value: 200 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 500 });
    // User is at bottom (500 - (300 + 200) = 0 <= 180)
    list.scrollTop = 300;

    rerender(
      <MessageList
        peerId="agent-1"
        messages={[...messages, { id: "msg-3", body: "Third message" }]}
      />
    );

    // Moves to new scrollHeight because near bottom
    expect(list.scrollTop).toBe(500);
  });

  it("locks viewport position when user scrolls up (> 180px from bottom)", () => {
    const messages: HistoryMessage[] = [
      { id: "msg-1", body: "First message" },
    ];

    const { container, rerender } = render(
      <MessageList
        peerId="agent-1"
        messages={messages}
      />
    );

    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Messages container not found");

    Object.defineProperty(list, "clientHeight", { configurable: true, value: 200 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1000 });
    // User scrolled up: scrollTop = 50 -> distance to bottom = 1000 - (50 + 200) = 750 > 180
    list.scrollTop = 50;
    fireEvent.scroll(list);

    rerender(
      <MessageList
        peerId="agent-1"
        messages={[...messages, { id: "msg-2", body: "Second message" }]}
      />
    );

    // Reading position remains preserved at 50
    expect(list.scrollTop).toBe(50);
  });

  it("resumes auto-scrolling when user scrolls back down within threshold", () => {
    const messages: HistoryMessage[] = [
      { id: "msg-1", body: "First message" },
    ];

    const { container, rerender } = render(
      <MessageList
        peerId="agent-1"
        messages={messages}
      />
    );

    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Messages container not found");

    Object.defineProperty(list, "clientHeight", { configurable: true, value: 200 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1000 });

    // Scroll up first
    list.scrollTop = 50;
    fireEvent.scroll(list);

    // Scroll back down near bottom: 1000 - (700 + 200) = 100 <= 180
    list.scrollTop = 700;
    fireEvent.scroll(list);

    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1200 });

    rerender(
      <MessageList
        peerId="agent-1"
        messages={[...messages, { id: "msg-2", body: "Second message" }]}
      />
    );

    // Resumes auto-scrolling to bottom
    expect(list.scrollTop).toBe(1200);
  });

  it("resets and forces scroll to bottom on conversation switch", () => {
    const messages: HistoryMessage[] = [
      { id: "msg-1", body: "First message" },
    ];

    const { container, rerender } = render(
      <MessageList
        peerId="agent-1"
        messages={messages}
      />
    );

    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Messages container not found");

    Object.defineProperty(list, "clientHeight", { configurable: true, value: 200 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 1000 });

    // User scrolled up in agent-1
    list.scrollTop = 50;
    fireEvent.scroll(list);

    // Switch to agent-2
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 800 });
    rerender(
      <MessageList
        peerId="agent-2"
        messages={[{ id: "msg-2-1", body: "Hello from agent 2" }]}
      />
    );

    // Forced scroll to bottom on switch
    expect(list.scrollTop).toBe(800);
  });

  it("renders from unified timeline events", () => {
    const events: ConversationTimelineEvent[] = Array.from({ length: 50 }, (_, i) => {
      const seq = i + 1;
      if (i % 3 === 0) {
        return {
          id: `evt-${seq}`,
          agent_id: "agent-1",
          seq,
          kind: "tool",
          content: `Executed tool action ${seq}`,
          metadata: { tool_name: "bash", outcome: "success" },
          created_at: 1700000000000 + i * 1000,
        };
      }
      return {
        id: `evt-${seq}`,
        agent_id: "agent-1",
        seq,
        kind: "message",
        role: i % 2 === 0 ? "assistant" : "user",
        content: `Timeline message body ${seq}`,
        created_at: 1700000000000 + i * 1000,
      };
    });

    const { container } = render(
      <MessageList
        peerId="agent-1"
        events={events}
      />
    );

    const timeline = container.querySelector("ol.complete-timeline");
    expect(timeline).toBeInTheDocument();

    // The latest item should be rendered
    expect(screen.getByText("Timeline message body 50")).toBeInTheDocument();
  });

  it("handles pagination via load older button and scroll up", async () => {
    let finishLoad: () => void = () => { throw new Error("Pagination has not started"); };
    const onLoadOlder = vi.fn(() => new Promise<void>((resolve) => { finishLoad = resolve; }));
    const events: ConversationTimelineEvent[] = Array.from({ length: 50 }, (_, i) => ({
      id: `evt-${i + 51}`,
      agent_id: "agent-1",
      seq: i + 51,
      kind: "message",
      role: "assistant",
      content: `Message ${i + 51}`,
      created_at: 1700000000000 + i * 1000,
    }));

    const { container } = render(
      <MessageList
        peerId="agent-1"
        events={events}
        hasOlder={true}
        onLoadOlder={onLoadOlder}
      />
    );

    // 1. Verify load older button exists and triggers callback when clicked
    const loadOlderBtn = screen.getByRole("button", { name: /Load older history/i });
    expect(loadOlderBtn).toBeInTheDocument();
    fireEvent.click(loadOlderBtn);
    expect(onLoadOlder).toHaveBeenCalledTimes(1);

    // Scrolling upward while the button request is pending must not overlap it.
    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Messages container not found");
    Object.defineProperty(list, "clientHeight", { configurable: true, value: 200 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 500 });
    list.scrollTop = 50;
    fireEvent.scroll(list);
    expect(onLoadOlder).toHaveBeenCalledTimes(1);

    await act(async () => { finishLoad(); });

    // After settlement, another upward movement can request the next page.
    list.scrollTop = 40;
    fireEvent.scroll(list);
    expect(onLoadOlder).toHaveBeenCalledTimes(2);
    await act(async () => { finishLoad(); });
  });

  it("fetchConversationTimeline calls GET /v1/conversation with agent_id, limit, and before_seq", async () => {
    const originalFetch = global.fetch;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.includes("/v1/conversation?agent_id=agent-test&limit=50&before_seq=42")) {
        return new Response(JSON.stringify({
          agent_id: "agent-test",
          events: [
            { id: "evt-1", agent_id: "agent-test", seq: 1, kind: "message", role: "user", content: "old message", created_at: 1700000000000 },
          ],
          next_before_seq: null,
        }), { status: 200, headers: { "content-type": "application/json" } });
      }
      if (url.includes("/v1/conversation?agent_id=agent-test&limit=50")) {
        return new Response(JSON.stringify({
          agent_id: "agent-test",
          events: [
            { id: "evt-42", agent_id: "agent-test", seq: 42, kind: "message", role: "assistant", content: "recent message", created_at: 1700000050000 },
          ],
          next_before_seq: 42,
        }), { status: 200, headers: { "content-type": "application/json" } });
      }
      return new Response(JSON.stringify({ error: "not found" }), { status: 404 });
    });

    global.fetch = fetchMock;
    try {
      // 1. Initial load without before_seq
      const initial = await fetchConversationTimeline("agent-test", 50, undefined, "http://127.0.0.1:8765");
      expect(initial.agent_id).toBe("agent-test");
      expect(initial.events).toHaveLength(1);
      expect(initial.events[0]?.seq).toBe(42);
      expect(initial.next_before_seq).toBe(42);

      // 2. Older load with before_seq from oldest entry
      const oldestSeq = initial.events[0]?.seq;
      expect(oldestSeq).toBeDefined();
      const older = await fetchConversationTimeline("agent-test", 50, oldestSeq, "http://127.0.0.1:8765");
      expect(older.agent_id).toBe("agent-test");
      expect(older.events).toHaveLength(1);
      expect(older.events[0]?.seq).toBe(1);
      expect(older.next_before_seq).toBeNull();
    } finally {
      global.fetch = originalFetch;
    }
  });

  describe("message classification", () => {
    it("renders assistant messages as incoming bubbles", () => {
      const events: ConversationTimelineEvent[] = [
        {
          id: "msg-1",
          agent_id: "agent-1",
          seq: 1,
          kind: "message",
          role: "assistant",
          content: "Hello from assistant!",
          created_at: 1700000000000,
        },
      ];

      const { container } = render(
        <MessageList
          peerId="agent-1"
          events={events}
        />
      );

      const bubbleWrap = container.querySelector(".bubble-wrap");
      expect(bubbleWrap).toBeInTheDocument();
      expect(bubbleWrap).toHaveClass("incoming");
      expect(bubbleWrap).not.toHaveClass("outgoing");
      expect(screen.getByText("Hello from assistant!")).toBeInTheDocument();
    });

    it("renders user messages as outgoing bubbles", () => {
      const events: ConversationTimelineEvent[] = [
        {
          id: "msg-2",
          agent_id: "agent-1",
          seq: 2,
          kind: "message",
          role: "user",
          content: "Hello from user!",
          created_at: 1700000001000,
        },
      ];

      const { container } = render(
        <MessageList
          peerId="agent-1"
          events={events}
        />
      );

      const bubbleWrap = container.querySelector(".bubble-wrap");
      expect(bubbleWrap).toBeInTheDocument();
      expect(bubbleWrap).toHaveClass("outgoing");
      expect(bubbleWrap).not.toHaveClass("incoming");
      expect(screen.getByText("Hello from user!")).toBeInTheDocument();
    });

    it("renders inter-agent messages as mailbox envelopes", () => {
      const events: ConversationTimelineEvent[] = [
        {
          id: "msg-3",
          agent_id: "agent-1",
          seq: 3,
          kind: "message",
          role: "assistant",
          content: "Message to sibling agent",
          metadata: {
            from: "agent-1",
            to: "agent-2",
            subject: "Work update",
          },
          created_at: 1700000002000,
        },
      ];

      const { container } = render(
        <MessageList
          peerId="agent-1"
          ownerId="user"
          events={events}
        />
      );

      // Should NOT render as a message bubble
      expect(container.querySelector(".bubble-wrap")).toBeNull();

      // Should render as an activity entry with envelope icon
      const envelopeIcon = container.querySelector(".activity-entry-icon.fa-envelope");
      expect(envelopeIcon).toBeInTheDocument();
      expect(screen.getByText("To agent-2")).toBeInTheDocument();
      expect(screen.getByText(/Work update/)).toBeInTheDocument();
    });

    it("deduplicates messages between timeline events and live messages without dropping new live messages", () => {
      const events: ConversationTimelineEvent[] = [
        {
          id: "msg:conversation-simplification:embedded-mail-0a8d",
          agent_id: "conversation-simplification",
          kind: "message",
          role: "assistant",
          seq: 1,
          content: "Historical message from timeline",
          created_at: 1700000000000,
        },
      ];

      const messages: HistoryMessage[] = [
        // Duplicate of the event above with raw message id
        {
          id: "embedded-mail-0a8d",
          body: "Historical message from timeline",
          direction: "incoming",
          from: "conversation-simplification",
          to: "user",
          sequence: 1,
        },
        // Fresh live incoming message not yet in timeline events
        {
          id: "embedded-mail-live-1234",
          body: "Brand new live message",
          direction: "incoming",
          from: "conversation-simplification",
          to: "user",
        },
      ];

      render(
        <MessageList
          peerId="conversation-simplification"
          ownerId="user"
          events={events}
          messages={messages}
        />
      );

      // Verify the duplicated message is only rendered once
      const historicalElements = screen.getAllByText("Historical message from timeline");
      expect(historicalElements).toHaveLength(1);

      // Verify the live message is preserved and rendered
      expect(screen.getByText("Brand new live message")).toBeInTheDocument();
    });
  });

  describe("natural unloading and zombie prevention", () => {
    it("zombie prevention: skips stale messages but retains selected managed-only activity", () => {
      // Simulate that events only contains items starting from sequence 51 (items 1..50 were unloaded)
      const events: ConversationTimelineEvent[] = Array.from({ length: 50 }, (_, i) => ({
        id: `evt-${i + 51}`,
        agent_id: "agent-1",
        seq: i + 51,
        kind: "message",
        role: "assistant",
        content: `Active message ${i + 51}`,
        created_at: 1700000000000 + (i + 51) * 1000,
      }));

      // Secondary stores (messages, activityEntries) have zombie items with sequence < 51 and created_at < oldestEvent.created_at
      const staleMessages: HistoryMessage[] = [
        {
          id: "zombie-msg-1",
          body: "Zombie message 1",
          direction: "incoming",
          from: "agent-1",
          to: "user",
          sequence: 1,
          created_at: new Date(1700000000000 + 1 * 1000).toISOString(),
        },
        {
          id: "zombie-msg-25",
          body: "Zombie message 25",
          direction: "incoming",
          from: "agent-1",
          to: "user",
          sequence: 25,
          created_at: new Date(1700000000000 + 25 * 1000).toISOString(),
        },
      ];

      staleMessages.push({ id: "failed-old", body: "Failed outgoing remains", direction: "outgoing", from: "user", to: "agent-1", failed: true, created_at: new Date(1700000000000).toISOString() });

      const staleActivityEntries: ManagedHistoryEntry[] = [
        {
          id: "complete:2",
          agent_id: "agent-1",
          at: new Date(1700000000000 + 2 * 1000).toISOString(),
          message: "Zombie tool execution",
          presentation: {
            kind: "tool",
            tool_name: "bash",
            outcome: "success",
            summary: "Zombie tool summary",
          },
        },
      ];

      render(
        <MessageList
          peerId="agent-1"
          ownerId="user"
          events={events}
          messages={staleMessages}
          activityEntries={staleActivityEntries}
        />
      );

      // Active messages should be present
      expect(screen.getByText("Active message 51")).toBeInTheDocument();
      expect(screen.getByText("Active message 100")).toBeInTheDocument();

      // Zombie items with sequence < oldestEventSeq (51) should NOT be injected
      expect(screen.queryByText("Zombie message 1")).not.toBeInTheDocument();
      expect(screen.queryByText("Zombie message 25")).not.toBeInTheDocument();
      expect(screen.getByText("Failed outgoing remains")).toBeInTheDocument();
      expect(screen.getByText("Zombie tool summary")).toBeInTheDocument();
    });

    it("calling loadOlder prepends older chunks and does NOT drop items from the bottom", () => {
      // 150 items (51..200) currently loaded
      const currentEvents: ConversationTimelineEvent[] = Array.from({ length: 150 }, (_, i) => ({
        id: `evt-${i + 51}`,
        agent_id: "agent-1",
        seq: i + 51,
        kind: "message",
        role: "assistant",
        content: `Message ${i + 51}`,
        created_at: 1700000000000 + (i + 51) * 1000,
      }));

      const onLoadOlder = vi.fn();
      const { rerender } = render(
        <MessageList
          peerId="agent-1"
          events={currentEvents}
          hasOlder={true}
          onLoadOlder={onLoadOlder}
        />
      );

      expect(screen.getByText("Message 51")).toBeInTheDocument();
      expect(screen.getByText("Message 200")).toBeInTheDocument();

      // Prepend older chunk 1..50
      const olderEvents: ConversationTimelineEvent[] = Array.from({ length: 50 }, (_, i) => ({
        id: `evt-${i + 1}`,
        agent_id: "agent-1",
        seq: i + 1,
        kind: "message",
        role: "assistant",
        content: `Message ${i + 1}`,
        created_at: 1700000000000 + (i + 1) * 1000,
      }));

      rerender(
        <MessageList
          peerId="agent-1"
          events={[...olderEvents, ...currentEvents]}
          hasOlder={false}
          onLoadOlder={onLoadOlder}
        />
      );

      // Now all 200 items are present: Message 1 prepended, Message 200 still present at bottom
      expect(screen.getByText("Message 1")).toBeInTheDocument();
      expect(screen.getByText("Message 51")).toBeInTheDocument();
      expect(screen.getByText("Message 200")).toBeInTheDocument();
    });
  });
});

