import { afterEach, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { Conversation } from "../../src/components/Conversation";
import { MessageList } from "../../src/components/MessageList";

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

  it("renders distinct message incarnations with the same message ID as separate bubbles", () => {
    const { container } = render(<MessageList messages={[{ id: "same-id", message_incarnation_id: "first", body: "First incarnation" }, { id: "same-id", message_incarnation_id: "second", body: "Second incarnation" }]} />);
    expect(container.querySelectorAll('[data-message-id="same-id"]')).toHaveLength(2);
    expect(screen.getByText("First incarnation")).toBeTruthy();
    expect(screen.getByText("Second incarnation")).toBeTruthy();
  });

  it("keeps a selected message subtree and browser selection stable across routine updates", () => {
    const { container, rerender } = render(<MessageList messages={[{ id: "1", body: "Selected message" }, { id: "2", body: "Other message" }]} />);
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

    rerender(<MessageList messages={[{ id: "1", body: "Selected message" }, { id: "2", body: "Updated message" }]} />);

    expect(container.querySelector('[data-message-id="1"]')).toBe(selectedBubble);
    expect(container.querySelector('[data-message-id="1"] .markdown-body')).toBe(selectedBody);
    expect(selection.anchorNode).toBe(anchorNode);
    expect(selection.focusNode).toBe(focusNode);
    expect(selection.anchorOffset).toBe(0);
    expect(selection.focusOffset).toBe(8);
  });

  it("updates a selected message body and removes stale rendered text", () => {
    const { container, rerender } = render(<MessageList messages={[{ id: "1", body: "Old message" }]} />);
    rerender(<MessageList messages={[{ id: "1", body: "New message" }]} />);
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
    const { container } = render(<Conversation peer={{ agent_id: "helper" }} messages={[{ id: "1", from: "helper", to: "ui", body: "**Hello**" }]} onSend={onSend} />);
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
});
