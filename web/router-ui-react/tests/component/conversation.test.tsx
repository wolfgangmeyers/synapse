import { afterEach, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Conversation } from "../../src/components/Conversation";
import { MessageList } from "../../src/components/MessageList";
import type { ManagedHistoryEntry } from "../../src/protocol/types";

describe("Conversation", () => {
  afterEach(() => cleanup());

  it("keeps populated messages mounted while refreshing or showing an error", () => {
    const message = { id: "1", from: "helper", to: "ui", body: "Still here" };
    const { rerender } = render(<Conversation peer={{ agent_id: "helper" }} messages={[message]} />);
    rerender(<Conversation peer={{ agent_id: "helper" }} messages={[message]} loading error="Refresh failed" />);
    expect(screen.getByText("Still here")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toBe("Refresh failed");
  });

  it("follows a new peer to the bottom even when its message count is unchanged", () => {
    const { container, rerender } = render(<MessageList peerId="one" messages={[{ id: "1", body: "One" }]} />);
    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Message list was not rendered");
    Object.defineProperty(list, "clientHeight", { configurable: true, value: 100 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 200 });
    rerender(<MessageList peerId="two" messages={[{ id: "2", body: "Two" }]} />);
    expect(list.scrollTop).toBe(200);
  });

  it("follows incoming and outgoing messages when the user is scrolled up", () => {
    const { container, rerender } = render(<MessageList peerId="one" messages={[{ id: "1", body: "One" }]} />);
    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Message list was not rendered");
    Object.defineProperty(list, "clientHeight", { configurable: true, value: 100 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 200 });
    list.scrollTop = 20;
    fireEvent.scroll(list);

    rerender(<MessageList peerId="one" messages={[{ id: "1", body: "One" }, { id: "2", direction: "incoming", body: "Two" }]} />);
    expect(list.scrollTop).toBe(200);

    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 300 });
    list.scrollTop = 20;
    fireEvent.scroll(list);
    rerender(<MessageList peerId="one" messages={[{ id: "1", body: "One" }, { id: "2", direction: "incoming", body: "Two" }, { id: "3", direction: "outgoing", body: "Three" }]} />);
    expect(list.scrollTop).toBe(300);
  });

  it("follows a new message when history pruning keeps the message count unchanged", async () => {
    const { container, rerender } = render(<MessageList peerId="one" messages={[{ id: "1", body: "One" }, { id: "2", body: "Two" }]} />);
    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Message list was not rendered");
    Object.defineProperty(list, "clientHeight", { configurable: true, value: 100 });
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 200 });
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    list.scrollTop = 20;

    rerender(<MessageList peerId="one" messages={[{ id: "2", body: "Two" }, { id: "3", direction: "incoming", body: "Three" }]} />);

    expect(list.scrollTop).toBe(200);
  });

  it("deduplicates messages with the same message ID", () => {
    const { container } = render(<MessageList peerId="one" messages={[{ id: "same-id", message_incarnation_id: "first", body: "First incarnation" }, { id: "same-id", message_incarnation_id: "second", body: "Second incarnation" }]} />);
    expect(container.querySelectorAll('[data-message-id="same-id"]')).toHaveLength(1);
    expect(screen.getByText("First incarnation")).toBeTruthy();
    expect(screen.queryByText("Second incarnation")).toBeNull();
  });

  it("keeps a selected message subtree and browser selection stable across routine updates", () => {
    const { container, rerender } = render(<MessageList peerId="one" messages={[{ id: "1", body: "Selected message" }, { id: "2", body: "Other message" }]} />);
    const selectedBubble = container.querySelector<HTMLElement>('[data-message-id="1"]');
    const selectedBody = selectedBubble?.querySelector<HTMLElement>(".markdown-body");
    const selectedText = selectedBody?.querySelector("p")?.firstChild;
    if (!(selectedBubble && selectedBody && selectedText instanceof Text)) throw new Error("Selected message was not rendered");

    const range = document.createRange();
    range.setStart(selectedText, 0);
    range.setEnd(selectedText, 8);
    const selection = window.getSelection();
    if (!selection) throw new Error("Browser selection is unavailable");
    selection.removeAllRanges();
    selection.addRange(range);
    const anchorNode = selection.anchorNode;
    const focusNode = selection.focusNode;

    rerender(<MessageList peerId="one" messages={[{ id: "1", body: "Selected message" }, { id: "2", body: "Updated message" }]} />);

    expect(container.querySelector('[data-message-id="1"]')).toBe(selectedBubble);
    expect(container.querySelector('[data-message-id="1"] .markdown-body')).toBe(selectedBody);
    expect(selection.anchorNode).toBe(anchorNode);
    expect(selection.focusNode).toBe(focusNode);
    expect(selection.anchorOffset).toBe(0);
    expect(selection.focusOffset).toBe(8);
  });

  it("updates a selected message body and removes stale rendered text", () => {
    const { container, rerender } = render(<MessageList peerId="one" messages={[{ id: "1", body: "Old message" }]} />);
    rerender(<MessageList peerId="one" messages={[{ id: "1", body: "New message" }]} />);
    const body = container.querySelector('[data-message-id="1"] .markdown-body');
    expect(body?.textContent).toContain("New message");
    expect(body?.textContent).not.toContain("Old message");
  });

  it("preserves scroll position without writing or churning on a same-length message update", () => {
    const { container, rerender } = render(<MessageList peerId="one" messages={[{ id: "1", body: "One" }]} />);
    const list = container.querySelector<HTMLDivElement>(".messages");
    if (!list) throw new Error("Message list was not rendered");
    Object.defineProperty(list, "scrollHeight", { configurable: true, value: 200 });
    let scrollTop = 30;
    let writes = 0;
    Object.defineProperty(list, "scrollTop", {
      configurable: true,
      get: () => scrollTop,
      set: (value: number) => { writes += 1; scrollTop = value; },
    });

    rerender(<MessageList peerId="one" messages={[{ id: "1", body: "Two" }]} />);

    expect(writes).toBe(0);
    expect(list.scrollTop).toBe(30);
  });

  it("does not focus the disabled textarea when no peer is selected", () => {
    render(<Conversation messages={[]} />);
    const composer = screen.getByRole("textbox");

    expect(composer).toHaveProperty("disabled", true);
    expect(document.activeElement).not.toBe(composer);
  });

  it("focuses the message textarea for an agent and a new peer without stealing focus on same-peer updates", () => {
    const { rerender } = render(<Conversation peer={{ agent_id: "one" }} messages={[]} />);
    const composer = screen.getByRole("textbox");

    expect(document.activeElement).toBe(composer);

    composer.blur();
    expect(document.activeElement).not.toBe(composer);

    rerender(<Conversation peer={{ agent_id: "two" }} messages={[]} />);
    expect(document.activeElement).toBe(composer);

    composer.blur();
    rerender(<Conversation peer={{ agent_id: "two" }} messages={[{ id: "1", from: "two", to: "ui", body: "Updated" }]} />);
    expect(document.activeElement).not.toBe(composer);
  });

  it("does not steal focus after a same-peer controlled value update", () => {
    const { rerender } = render(<Conversation peer={{ agent_id: "one" }} value="Draft" messages={[]} />);
    const composer = screen.getByRole("textbox");

    expect(document.activeElement).toBe(composer);
    composer.blur();
    expect(document.activeElement).not.toBe(composer);

    rerender(<Conversation peer={{ agent_id: "one" }} value="Updated draft" messages={[]} />);
    expect(document.activeElement).not.toBe(composer);
  });

  it("renders safe markdown and sends through the composer", () => {
    const onSend = vi.fn();
    const { container } = render(<Conversation peer={{ agent_id: "helper" }} ownerId="ui" messages={[{ id: "1", from: "helper", to: "ui", body: "**Hello**" }]} onSend={onSend} />);
    expect(screen.getByText("Hello")).toBeTruthy();
    const composer = container.querySelector<HTMLTextAreaElement>("textarea");
    if (!composer) throw new Error("Composer was not rendered");
    fireEvent.change(composer, { target: { value: "Hi" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(onSend).toHaveBeenCalledWith("Hi", []);
  });

  it("does not render an active-subagents aside widget when active subagent entries exist, preserving inline tool cards", () => {
    const activeChild = {
      id: "complete:child",
      agent_id: "managed-worker",
      at: "2030-01-01T10:00:00.000Z",
      message: "Child started",
      presentation: {
        kind: "child" as const,
        child_state: "running" as const,
        outcome: "active" as const,
        child_label: "Worker review",
      },
    };
    const toolCall = {
      id: "complete:tool",
      agent_id: "managed-worker",
      at: "2030-01-01T10:01:00.000Z",
      message: "Spawned subagent",
      presentation: {
        kind: "tool" as const,
        tool_name: "subagent",
        outcome: "success" as const,
      },
    };

    const { container } = render(
      <Conversation
        peer={{ agent_id: "managed-worker", activity_state: "idle" }}
        conversationView="complete"
        messages={[]}
        activityEntries={[activeChild, toolCall]}
      />
    );

    expect(screen.queryByLabelText("Active subagents")).not.toBeInTheDocument();
    expect(container.querySelector(".active-subagents")).not.toBeInTheDocument();
    expect(container.querySelector("#conversation-view-panel [data-activity-id='complete:tool']")).toBeInTheDocument();
  });

  it("renders workflow activation prompt and assistant response bubble in Complete view and chat-only mode", () => {
    const wfActivation = {
      id: "complete:1",
      agent_id: "workflows",
      at: "2030-01-01T10:00:00.000Z",
      message: "Workflow activated",
      message_content: "[Workflow Activated: test-state]\n\nReport to the user what 3 * 3 is.",
      presentation: {
        kind: "message" as const,
        outcome: "success" as const,
        from: "system",
        to: "workflows",
        summary: "Workflow Activated: test-state",
      },
    };
    const assistantReply = {
      id: "complete:2",
      agent_id: "workflows",
      at: "2030-01-01T10:00:05.000Z",
      message: "Message sent",
      message_content: "3 * 3 is 9.",
      presentation: {
        kind: "message" as const,
        outcome: "success" as const,
        from: "workflows",
        to: "user",
      },
    };

    const { container, rerender } = render(
      <Conversation
        peer={{ agent_id: "workflows", source: "router-managed" }}
        conversationView="complete"
        ownerId="wolfgang"
        messages={[]}
        activityEntries={[wfActivation, assistantReply]}
      />
    );

    // Verify workflow activation event card is visible
    expect(container.querySelector("[data-message-id='complete:1'], [data-activity-id='complete:1']")).toBeInTheDocument();
    expect(screen.getByText(/Workflow Activated: test-state/)).toBeInTheDocument();

    // Verify assistant reply is rendered in timeline
    expect(screen.getByText("3 * 3 is 9.")).toBeInTheDocument();
  });

  it("renders empty state 'Pick an agent' when peer is undefined even if messages exist", () => {
    const message = { id: "1", from: "helper", to: "ui", body: "Should not show" };
    render(<Conversation messages={[message]} />);
    expect(screen.getByRole("heading", { level: 3, name: "Pick an agent" })).toBeInTheDocument();
    expect(screen.getByText("Select an agent from the sidebar to view activity and chat.")).toBeInTheDocument();
    expect(screen.queryByText("Should not show")).not.toBeInTheDocument();
  });

  it("renders empty state 'Pick an agent' in MessageList when peerId is undefined even with items", () => {
    render(<MessageList messages={[{ id: "1", body: "Hidden item" }]} />);
    expect(screen.getByText("Pick an agent")).toBeInTheDocument();
    expect(screen.getByText("Select an agent from the sidebar to view activity and chat.")).toBeInTheDocument();
    expect(screen.queryByText("Hidden item")).not.toBeInTheDocument();
  });

  it("renders reminders and assistant replies to reminders visibly in complete mode", () => {
    const reminderReceived: ManagedHistoryEntry = {
      id: "complete:rem-1",
      agent_id: "workflows",
      at: "2026-10-08T19:26:08.783Z",
      level: "info",
      message: "Reminder received",
      message_content: "Testing out workflow 1-shot reminder. Notify the user if you got this and answer this question: What animal has the most appendages?",
      sender_identity: "reminders",
      presentation: {
        kind: "reminder" as const,
        outcome: "success" as const,
      },
    };

    const reminderReply: ManagedHistoryEntry = {
      id: "complete:rem-reply-1",
      agent_id: "workflows",
      at: "2026-10-08T19:26:08.800Z",
      level: "info",
      message: "Message sent",
      message_content: "Got the reminder! The animal with the most appendages is the millipede Eumillipes persephone.",
      sender_identity: "workflows",
      presentation: {
        kind: "message" as const,
        outcome: "success" as const,
        from: "workflows",
        to: "reminders",
      },
    };

    const { container, rerender } = render(
      <Conversation
        peer={{ agent_id: "workflows", source: "router-managed" }}
        conversationView="complete"
        ownerId="wolfgang"
        messages={[]}
        activityEntries={[reminderReceived, reminderReply]}
      />
    );

    // Verify reminder received card
    expect(container.querySelector("[data-message-id='complete:rem-1'], [data-activity-id='complete:rem-1']")).toBeInTheDocument();
    expect(screen.getByText(/Testing out workflow 1-shot reminder/)).toBeInTheDocument();

    // Verify reminder reply card
    expect(container.querySelector("[data-message-id='complete:rem-reply-1'], [data-activity-id='complete:rem-reply-1']")).toBeInTheDocument();
    expect(screen.getByText(/The animal with the most appendages is the millipede/)).toBeInTheDocument();
  });
});
