import { afterEach, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { MessageList } from "../../src/components/MessageList";
import type { HistoryMessage } from "../../src/domain/history";
import type { ManagedHistoryEntry } from "../../src/protocol/types";
import { fetchConversationTimeline, type ConversationTimelineEvent } from "../../src/services/api";

afterEach(cleanup);

describe("MessageList", () => {
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

  it("anchors scroll to bottom when not locked", () => {
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

    rerender(
      <MessageList
        peerId="agent-1"
        messages={[...messages, { id: "msg-3", body: "Third message" }]}
      />
    );

    // When not locked, scroll moves to scrollHeight
    expect(list.scrollTop).toBe(500);
  });

  it("does not force scroll to bottom when scrollLocked is true", () => {
    const messages: HistoryMessage[] = [
      { id: "msg-1", body: "First message" },
    ];

    const { container, rerender } = render(
      <MessageList
        peerId="agent-1"
        messages={messages}
        scrollLocked={true}
      />
    );

    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Messages container not found");

    Object.defineProperty(list, "clientHeight", { configurable: true, value: 200 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 500 });
    list.scrollTop = 50;

    rerender(
      <MessageList
        peerId="agent-1"
        messages={[...messages, { id: "msg-2", body: "Second message" }]}
        scrollLocked={true}
      />
    );

    // Position remains untouched because scrollLocked is true
    expect(list.scrollTop).toBe(50);
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

  it("handles pagination via load older button and scroll up", () => {
    const onLoadOlder = vi.fn();
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

    // 2. Verify scrolling up (scrollTop < 100 on scrollable container) triggers onLoadOlder
    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Messages container not found");
    Object.defineProperty(list, "clientHeight", { configurable: true, value: 200 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 500 });
    list.scrollTop = 50;
    fireEvent.scroll(list);
    expect(onLoadOlder).toHaveBeenCalledTimes(2);
  });

  it("renders prepended older events when pagination loads more and maintains scroll position", () => {
    const initialEvents: ConversationTimelineEvent[] = Array.from({ length: 50 }, (_, i) => ({
      id: `evt-${i + 51}`,
      agent_id: "agent-1",
      seq: i + 51,
      kind: "message",
      role: "assistant",
      content: `Message ${i + 51}`,
      created_at: 1700000000000 + (i + 51) * 1000,
    }));

    const onLoadOlder = vi.fn();
    const { container, rerender } = render(
      <MessageList
        peerId="agent-1"
        events={initialEvents}
        hasOlder={true}
        onLoadOlder={onLoadOlder}
      />
    );

    expect(screen.getByText("Message 100")).toBeInTheDocument();
    expect(screen.queryByText("Message 1")).not.toBeInTheDocument();

    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Messages container not found");

    // Simulate initial scroll state before pagination
    Object.defineProperty(list, "clientHeight", { configurable: true, value: 500 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, writable: true, value: 2000 });
    list.scrollTop = 50;

    // Trigger load older (which records prevScrollHeight = 2000 and prevScrollTop = 50)
    fireEvent.scroll(list);
    expect(onLoadOlder).toHaveBeenCalled();

    const olderEvents: ConversationTimelineEvent[] = Array.from({ length: 50 }, (_, i) => ({
      id: `evt-${i + 1}`,
      agent_id: "agent-1",
      seq: i + 1,
      kind: "message",
      role: "assistant",
      content: `Message ${i + 1}`,
      created_at: 1700000000000 + (i + 1) * 1000,
    }));

    // ScrollHeight increases after prepending items to 4000 (delta = +2000)
    Object.defineProperty(list, "scrollHeight", { configurable: true, writable: true, value: 4000 });

    // Prepend older events
    rerender(
      <MessageList
        peerId="agent-1"
        events={[...olderEvents, ...initialEvents]}
        hasOlder={false}
        onLoadOlder={onLoadOlder}
      />
    );

    // Verify scroll position was compensated: prevScrollTop (50) + delta (2000) = 2050
    expect(list.scrollTop).toBe(2050);

    // After loading older, 100 items exist, and load older button disappears
    expect(screen.queryByRole("button", { name: /Load older history/i })).not.toBeInTheDocument();
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
  });
});
