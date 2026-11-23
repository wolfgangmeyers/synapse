import "@testing-library/jest-dom/vitest";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { Conversation } from "../../src/components/Conversation";
import { ConversationLinks } from "../../src/components/ConversationLinks";
import { resetLinksSession } from "../../src/persistence/links-session";
import type { HistoryMessage } from "../../src/domain/history";

const msg = (id: string, body: string, at: number, direction: "incoming" | "outgoing" = "incoming"): HistoryMessage =>
  ({ id, body, direction, created_at: new Date(at).toISOString() });

const messages = [
  msg("1", "Check [Spec](https://spec.dev/a) please", 1000, "outgoing"),
  msg("2", "and https://plain.io/some/long/path", 2000),
];

// The parent keys the widget by agent so state cannot leak between agents.
const widget = (agentId: string, list: HistoryMessage[] = messages) => <ConversationLinks key={agentId} agentId={agentId} messages={list} />;

function open(agentId = "a1", list: HistoryMessage[] = messages) {
  const view = render(widget(agentId, list));
  fireEvent.click(screen.getByRole("button", { name: /^Links/ }));
  return view;
}

beforeEach(() => resetLinksSession());
afterEach(cleanup);

describe("ConversationLinks", () => {
  it("lists links from both directions, labels first, newest first, and shows the count", () => {
    open();
    expect(screen.getByRole("button", { name: "Links (2)" })).toBeInTheDocument();
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(within(items[0]!).getByRole("link")).toHaveAttribute("href", "https://plain.io/some/long/path");
    expect(within(items[0]!).getByText("plain.io/some/long/path")).toBeInTheDocument();
    expect(within(items[0]!).queryByText("https://plain.io/some/long/path")).toBeNull();
    expect(within(items[1]!).getByText("Spec")).toBeInTheDocument();
    expect(within(items[1]!).getByText("https://spec.dev/a")).toBeInTheDocument();
    expect(within(items[1]!).getByRole("link")).toHaveAttribute("target", "_blank");
  });

  it("shows an empty state", () => {
    open("a1", []);
    expect(screen.getByText("No links yet.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear all" })).toBeNull();
  });

  it("keeps the add form out of the list view and adds a link through a separate form view", () => {
    open();
    expect(screen.queryByLabelText("Link URL")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "+ Add link" }));
    expect(screen.queryByRole("list")).toBeNull();
    fireEvent.change(screen.getByLabelText("Link URL"), { target: { value: "https://mine.io/x" } });
    fireEvent.change(screen.getByLabelText("Link label"), { target: { value: "Mine" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.queryByLabelText("Link URL")).toBeNull();
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(3);
    expect(within(items[0]!).getByText("Mine")).toBeInTheDocument();
  });

  it("rejects an invalid url, stays on the form, and Cancel returns to the list without saving", () => {
    open();
    fireEvent.click(screen.getByRole("button", { name: "+ Add link" }));
    fireEvent.change(screen.getByLabelText("Link URL"), { target: { value: "not a url" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByRole("alert")).toHaveTextContent("valid http(s) URL");
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "+ Add link" }));
    expect(screen.getByLabelText("Link URL")).toHaveValue("");
  });

  it("removes one link and clears all", () => {
    open();
    fireEvent.click(screen.getByRole("button", { name: "Remove link Spec" }));
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Clear all" }));
    expect(screen.queryAllByRole("listitem")).toHaveLength(0);
    expect(screen.getByText("No links yet.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Links" })).toBeInTheDocument();
  });

  it("restores per-agent session state when the agent is revisited, without leaking between agents", () => {
    const view = open("a1");
    fireEvent.click(screen.getByRole("button", { name: "Remove link Spec" }));
    view.rerender(widget("a2"));
    expect(screen.getByRole("button", { name: "Links (2)" })).toBeInTheDocument();
    view.rerender(widget("a1"));
    expect(screen.getByRole("button", { name: "Links (1)" })).toBeInTheDocument();
  });

  it("resurfaces a removed link when a newer message mentions it again", () => {
    const view = open();
    fireEvent.click(screen.getByRole("button", { name: "Remove link Spec" }));
    view.rerender(widget("a1", [...messages, msg("3", "again https://spec.dev/a", 3000)]));
    expect(screen.getByRole("button", { name: "Links (2)" })).toBeInTheDocument();
  });

  it("saves a link with a blank label using its preview and updates the header button", () => {
    open("a1", []);
    expect(screen.getByRole("button", { name: "Links" })).not.toHaveClass("active");
    fireEvent.click(screen.getByRole("button", { name: "+ Add link" }));
    fireEvent.change(screen.getByLabelText("Link URL"), { target: { value: "https://www.example.com/docs" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getByRole("link", { name: "example.com/docs" })).toHaveAttribute("href", "https://www.example.com/docs");
    expect(screen.getByRole("button", { name: "Links (1)" })).toHaveClass("active");
  });

  it("removes a manually added link and decrements the count", () => {
    open("a1", []);
    fireEvent.click(screen.getByRole("button", { name: "+ Add link" }));
    fireEvent.change(screen.getByLabelText("Link URL"), { target: { value: "https://mine.io/x" } });
    fireEvent.change(screen.getByLabelText("Link label"), { target: { value: "Mine" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove link Mine" }));
    expect(screen.getByText("No links yet.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Links" })).toBeInTheDocument();
  });

  it("closing the dialog discards a half-filled form and reopens on the list", () => {
    open();
    fireEvent.click(screen.getByRole("button", { name: "+ Add link" }));
    fireEvent.change(screen.getByLabelText("Link URL"), { target: { value: "bad" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    fireEvent.click(screen.getByRole("button", { name: "Close dialog" }));
    fireEvent.click(screen.getByRole("button", { name: /^Links/ }));
    expect(screen.queryByLabelText("Link URL")).toBeNull();
    expect(screen.queryByRole("alert")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "+ Add link" }));
    expect(screen.getByLabelText("Link URL")).toHaveValue("");
  });

  it("keeps manual links per agent", () => {
    const view = open("a1", []);
    fireEvent.click(screen.getByRole("button", { name: "+ Add link" }));
    fireEvent.change(screen.getByLabelText("Link URL"), { target: { value: "https://one.io" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    view.rerender(widget("a2", []));
    expect(screen.getByRole("button", { name: "Links" })).toBeInTheDocument();
    view.rerender(widget("a1", []));
    expect(screen.getByRole("button", { name: "Links (1)" })).toBeInTheDocument();
  });

  it("a manual link that shadows a message link is removed completely", () => {
    open();
    fireEvent.click(screen.getByRole("button", { name: "+ Add link" }));
    fireEvent.change(screen.getByLabelText("Link URL"), { target: { value: "https://spec.dev/a" } });
    fireEvent.change(screen.getByLabelText("Link label"), { target: { value: "Pinned Spec" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(screen.getAllByRole("listitem")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "Remove link Pinned Spec" }));
    expect(screen.getAllByRole("listitem")).toHaveLength(1);
    expect(screen.queryByText("Spec")).toBeNull();
  });
});

describe("Conversation integration", () => {
  it("shows the links button in the header for the selected agent and keeps state per agent", () => {
    const view = render(<Conversation peer={{ agent_id: "helper" }} messages={messages} />);
    expect(screen.getByRole("button", { name: "Links (2)" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Links (2)" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove link Spec" }));
    expect(screen.getByRole("button", { name: "Links (1)" })).toBeInTheDocument();
    view.rerender(<Conversation peer={{ agent_id: "other" }} messages={messages} />);
    expect(screen.getByRole("button", { name: "Links (2)" })).toBeInTheDocument();
  });

  it("omits the links button when no agent is selected", () => {
    render(<Conversation messages={messages} />);
    expect(screen.queryByRole("button", { name: /^Links/ })).toBeNull();
  });
});
