import { afterEach, describe, expect, it, vi } from "vitest";
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
});
