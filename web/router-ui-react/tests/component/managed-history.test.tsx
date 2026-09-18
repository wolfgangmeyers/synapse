import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Conversation } from "../../src/components/Conversation";
import { MessageList } from "../../src/components/MessageList";
import type { ConversationView } from "../../src/components/ConversationHeader";

afterEach(cleanup);

function ConversationModes(): ReactElement {
  const [view, setView] = useState<ConversationView>("quiet");
  return <Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} conversationView={view} onConversationViewChange={setView} messages={[{ id: "message-1", from: "managed-worker", to: "wolfgang", body: "Conversation message", created_at: "2030-01-01T10:00:00.000Z" }]} activityEntries={[{ id: "activity-1", agent_id: "managed-worker", level: "info", message: "Managed worker restarted", at: "2030-01-01T10:01:00.000Z" }, { id: "activity-untimed", agent_id: "managed-worker", message: "Managed worker activity without a timestamp" }]} />;
}

describe("Selected-agent conversation modes", () => {
  it("keeps Quiet as the conversation and makes Complete include messages plus activity", async () => {
    const user = userEvent.setup();
    render(<Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} conversationView="complete" messages={[]} activityEntries={[{ id: "ordinary-progress", agent_id: "managed-worker", message: "State changed", presentation: { kind: "state", from: "Starting", to: "Running" } }]} />);
    expect(screen.queryByText("State changed")).not.toBeInTheDocument();
    cleanup();

    render(<ConversationModes />);

    const quiet = screen.getByRole("tab", { name: "Quiet messages" });
    const complete = screen.getByRole("tab", { name: "Complete messages and activity" });
    expect(quiet).toHaveAttribute("aria-selected", "true");
    expect(quiet).toHaveAttribute("aria-controls", "conversation-view-panel");
    expect(screen.getByText("Conversation message")).toBeInTheDocument();
    expect(screen.queryByText("Managed worker restarted")).not.toBeInTheDocument();

    await user.click(complete);
    expect(complete).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Conversation message")).toBeInTheDocument();
    expect(screen.getByText("Managed worker restarted")).toBeInTheDocument();
    expect(screen.getByText("Managed worker activity without a timestamp")).toBeInTheDocument();
    expect(document.querySelector("[data-message-id='message-1']")).toBeInTheDocument();
    expect(document.querySelector("[data-activity-id='activity-1']")).toBeInTheDocument();
    expect([...screen.getByRole("tabpanel").querySelectorAll("[data-message-id], [data-activity-id]")].map((item) => item.getAttribute("data-message-id") ?? item.getAttribute("data-activity-id"))).toEqual(["message-1", "activity-1", "activity-untimed"]);

    await user.click(quiet);
    expect(screen.getByText("Conversation message")).toBeInTheDocument();
    expect(screen.queryByText("Managed worker restarted")).not.toBeInTheDocument();
    expect(screen.queryByText("Managed worker activity without a timestamp")).not.toBeInTheDocument();
  });

  it("marks mail by the selected managed agent's transport direction without creating activity duplicates", () => {
    const { container } = render(<Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} ownerId="supervisor" conversationView="complete" messages={[
      { id: "received", from: "supervisor", to: "managed-worker", body: "Please start", created_at: "2030-01-01T10:00:00.000Z" },
      { id: "sent", from: "managed-worker", to: "supervisor", body: "Started", created_at: "2030-01-01T10:01:00.000Z" },
    ]} activityEntries={[]} />);
    expect(screen.getByText("Received message")).toBeInTheDocument();
    expect(screen.getByText("Sent message")).toBeInTheDocument();
    expect(container.querySelector(".message-transport--received .fa-envelope")).toBeInTheDocument();
    expect(container.querySelector(".message-transport--sent .fa-paper-plane")).toBeInTheDocument();
    expect(container.querySelectorAll("[data-activity-id]")).toHaveLength(0);
  });

  it("does not announce retained Complete history after the selected agent changes", () => {
    const retained = { id: "complete:1", agent_id: "managed-worker", at: "2030-01-01T10:00:00.000Z", message: "Bash failed", presentation: { kind: "tool" as const, tool_name: "bash", outcome: "error" as const } };
    const { container, rerender } = render(<MessageList messages={[]} peerId="managed-worker" activityEntries={[retained]} activityHistoryReady={false} />);
    rerender(<MessageList messages={[]} peerId="managed-worker" activityEntries={[retained]} activityHistoryReady />);
    expect(container.querySelector(".timeline-live-region")).toHaveTextContent("");

    rerender(<MessageList messages={[]} peerId="managed-worker" activityEntries={[retained, { ...retained, id: "complete:2", message: "Child failed", presentation: { kind: "child", child_state: "error" } }]} activityHistoryReady />);
    expect(container.querySelector(".timeline-live-region")).toHaveTextContent("Subagent");
  });

  it("keeps only terminal subagent outcomes in Complete history", () => {
    render(<MessageList messages={[]} peerId="managed-worker" activityEntries={[
      { id: "complete:1", agent_id: "managed-worker", message: "Child queued", presentation: { kind: "child", child_state: "queued" } },
      { id: "complete:2", agent_id: "managed-worker", message: "Child started", presentation: { kind: "child", child_state: "running" } },
      { id: "complete:3", agent_id: "managed-worker", message: "Child wrapped up", presentation: { kind: "child", child_state: "steered", child_label: "Delay fifteen seconds", summary: "Wrapped up in 15 seconds" } },
      { id: "complete:4", agent_id: "managed-worker", message: "Child completed", presentation: { kind: "child", child_state: "completed", child_label: "Wait twenty seconds", summary: "Completed in 20 seconds" } },
    ]} />);

    expect(screen.queryByText("Child queued")).not.toBeInTheDocument();
    expect(screen.getAllByText("Subagent")).toHaveLength(2);
    expect(screen.queryByText("Child started")).not.toBeInTheDocument();
    expect(screen.queryByText("Child wrapped up")).not.toBeInTheDocument();
    expect(screen.queryByText("Child completed")).not.toBeInTheDocument();
    expect(screen.getByText("Completed · Delay fifteen seconds · Wrapped up in 15 seconds")).toBeInTheDocument();
    expect(screen.getByText("Completed · Wait twenty seconds · Completed in 20 seconds")).toBeInTheDocument();
  });

  it("keeps the Run subagent tool invocation in history", () => {
    const { container } = render(<MessageList messages={[]} peerId="managed-worker" activityEntries={[
      { id: "complete:tool", agent_id: "managed-worker", message: "Run subagent", presentation: { kind: "tool", tool_name: "subagent", outcome: "success" } },
      { id: "complete:child", agent_id: "managed-worker", message: "Subagent updated", presentation: { kind: "child", child_state: "completed", child_label: "Review", summary: "Completed successfully" } },
    ]} />);

    expect(container.querySelectorAll("[data-activity-id]")).toHaveLength(2);
    expect(screen.getByText("Ran subagent")).toBeInTheDocument();
    expect(screen.getByText("Subagent")).toBeInTheDocument();
    expect(screen.getByText("Completed · Review · Completed successfully")).toBeInTheDocument();
  });

  it.each([
    { outcome: "success" as const, label: "Ran bash", detail: "Command completed", tone: "teal" as const },
    { outcome: "error" as const, label: "Bash failed", detail: "Command failed", tone: "danger" as const },
    { outcome: "interrupted" as const, label: "Bash interrupted", detail: "Stopped by user", tone: "danger" as const },
  ])("keeps a terminal builtin activity in the same timeline position for $outcome", ({ outcome, label, detail, tone }) => {
    const activity = (id: string, presentation: { kind: "tool"; tool_name: "bash"; outcome: "active" | "success" | "error" | "interrupted" }, message: string) => ({ id, agent_id: "managed-worker", at: "2030-01-01T10:01:00.000Z", message, presentation });
    const before = activity("complete:1", { kind: "tool", tool_name: "bash", outcome: "success" }, "Before");
    const active = activity("complete:2", { kind: "tool", tool_name: "bash", outcome: "active" }, "Running bash");
    const after = activity("complete:3", { kind: "tool", tool_name: "bash", outcome: "success" }, "After");
    const { container, rerender } = render(<MessageList messages={[]} peerId="managed-worker" activityEntries={[before, active, after]} />);
    const initialArticle = container.querySelector<HTMLElement>('[data-activity-id="complete:2"]');
    const initialItem = initialArticle?.closest("li.timeline-item");
    if (!initialArticle || !(initialItem instanceof HTMLElement)) throw new Error("Active activity was not rendered");
    const timelineItems = (): Element[] => Array.from(container.querySelectorAll("li.timeline-item"));

    expect(timelineItems().indexOf(initialItem)).toBe(1);
    expect(container.querySelectorAll("article")).toHaveLength(3);

    rerender(<MessageList messages={[]} peerId="managed-worker" activityEntries={[
      before,
      { ...active, message: label, presentation: { kind: "tool", tool_name: "bash", outcome, summary: detail } },
      after,
    ]} />);

    const terminalArticle = container.querySelector<HTMLElement>('[data-activity-id="complete:2"]');
    const terminalItem = terminalArticle?.closest("li.timeline-item");
    if (!terminalArticle || !terminalItem) throw new Error("Terminal activity was not rendered");
    expect(terminalArticle).toBe(initialArticle);
    expect(terminalItem).toBe(initialItem);
    expect(timelineItems().indexOf(terminalItem)).toBe(1);
    expect(container.querySelectorAll("article")).toHaveLength(3);
    expect([...container.querySelectorAll<HTMLElement>("[data-activity-id]")].map((item) => item.dataset.activityId)).toEqual(["complete:1", "complete:2", "complete:3"]);
    expect(terminalArticle).toHaveClass(`activity-entry--${tone}`);
    expect(terminalArticle).not.toHaveClass("activity-entry--active");
    expect(terminalArticle).toHaveTextContent(label);
    expect(terminalArticle).toHaveTextContent(detail);
  });

  it("keeps queued child lifecycle rows out of Complete until their same-ID terminal outcome arrives", () => {
    const queued = { id: "complete:child", agent_id: "managed-worker", message: "Child queued", presentation: { kind: "child" as const, child_state: "queued" as const, outcome: "active" as const } };
    const { container, rerender } = render(<MessageList messages={[]} peerId="managed-worker" activityEntries={[queued]} />);
    expect(container.querySelectorAll("[data-activity-id]")).toHaveLength(0);

    rerender(<MessageList messages={[]} peerId="managed-worker" activityEntries={[{ ...queued, message: "Child completed", presentation: { kind: "child", child_state: "completed", outcome: "completed", child_label: "Native child", summary: "Completed successfully" } }]} />);

    expect(container.querySelectorAll("[data-activity-id]")).toHaveLength(1);
    expect(container.querySelector('[data-activity-id="complete:child"]')).toBeInTheDocument();
    const terminalArticle = container.querySelector<HTMLElement>('[data-activity-id="complete:child"]');
    if (!terminalArticle) throw new Error("Terminal child activity was not rendered");
    expect(terminalArticle).toHaveTextContent("Subagent");
    expect(terminalArticle).toHaveTextContent("Completed · Native child · Completed successfully");
  });

  it("renders active managed subagents persistently below the conversation instead of in history", () => {
    const active = { id: "complete:child", agent_id: "managed-worker", at: "2030-01-01T10:00:00.000Z", message: "Child started", presentation: { kind: "child" as const, child_state: "running" as const, outcome: "active" as const, child_label: "Review" } };
    const { container } = render(<Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} conversationView="complete" messages={[]} activityEntries={[
      { id: "complete:tool", agent_id: "managed-worker", at: "2030-01-01T10:01:00.000Z", message: "Run subagent", presentation: { kind: "tool", tool_name: "subagent", outcome: "success" } },
      active,
    ]} />);

    const activeArea = screen.getByLabelText("Active subagents");
    expect(within(activeArea).getByText("Subagent")).toBeInTheDocument();
    expect(within(activeArea).getByText("Review")).toBeInTheDocument();
    expect(within(activeArea).getByRole("article")).toHaveClass("activity-entry--active");
    expect(container.querySelector("#conversation-view-panel [data-activity-id='complete:child']")).not.toBeInTheDocument();
    expect(container.querySelector("#conversation-view-panel [data-activity-id='complete:tool']")).toBeInTheDocument();
  });

  it("moves a terminal managed subagent from the active area to the newest history position", () => {
    const active = { id: "complete:child", agent_id: "managed-worker", at: "2030-01-01T10:00:00.000Z", message: "Child started", presentation: { kind: "child" as const, child_state: "running" as const, outcome: "active" as const, child_label: "Review" } };
    const laterHistory = { id: "complete:tool", agent_id: "managed-worker", at: "2030-01-01T10:01:00.000Z", message: "Ran bash", presentation: { kind: "tool" as const, tool_name: "bash", outcome: "success" as const } };
    const { container, rerender } = render(<Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} conversationView="complete" messages={[]} activityEntries={[active, laterHistory]} />);

    expect(screen.getByLabelText("Active subagents")).toContainElement(container.querySelector("[data-activity-id='complete:child']"));
    rerender(<Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} conversationView="complete" messages={[]} activityEntries={[{ ...active, at: "2030-01-01T10:02:00.000Z", message: "Subagent finished", presentation: { kind: "child", child_state: "completed", outcome: "completed", child_label: "Review", summary: "Completed successfully" } }, laterHistory]} />);

    expect(screen.queryByLabelText("Active subagents")).not.toBeInTheDocument();
    const historyIDs = [...container.querySelectorAll<HTMLElement>("#conversation-view-panel [data-activity-id]")].map((element) => element.dataset.activityId);
    expect(historyIDs).toEqual(["complete:tool", "complete:child"]);
    const terminal = container.querySelector<HTMLElement>("#conversation-view-panel [data-activity-id='complete:child']");
    expect(terminal).toHaveClass("activity-entry--terminal-success");
    expect(terminal?.querySelector(".activity-entry-label strong")).toHaveTextContent("Subagent");
    expect(terminal).toHaveTextContent("Completed · Review · Completed successfully");
  });

  it("orders equal-timestamp messages and Complete activities by numeric sequence with a deterministic tie", () => {
    const at = "2030-01-01T10:00:00.000Z";
    render(<MessageList
      messages={[{ id: "message-seq-2", sequence: 2, created_at: at, body: "Message sequence two" }]}
      peerId="managed-worker"
      activityEntries={[
        { id: "complete:10", agent_id: "managed-worker", at, message: "Activity ten", presentation: { kind: "tool", tool_name: "bash", outcome: "success" } },
        { id: "complete:2", agent_id: "managed-worker", at, message: "Activity two", presentation: { kind: "tool", tool_name: "bash", outcome: "success" } },
        { id: "complete:1", agent_id: "managed-worker", at, message: "Activity one", presentation: { kind: "tool", tool_name: "bash", outcome: "success" } },
      ]}
    />);

    const timeline = document.querySelector<HTMLOListElement>("ol.complete-timeline");
    if (!timeline) throw new Error("Complete timeline was not rendered");
    expect([...timeline.querySelectorAll<HTMLElement>("[data-message-id], [data-activity-id]")].map((item) => item.dataset.messageId ?? item.dataset.activityId)).toEqual(["complete:1", "complete:2", "message-seq-2", "complete:10"]);
  });

  it("reports a failed managed agent in the selected-agent header", () => {
    const managedAgent = { agent_id: "managed-worker", source: "router-managed" as const, cwd: "/tmp/managed-worker", model: { provider: "openai-codex", id: "gpt-5.6-luna" }, desired_state: "running", lifecycle_state: "failed", activity_state: "failed", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: { code: "PROCESS_STOP_FAILED", message: "Managed process exited before readiness", at: "2030-01-01T00:01:00.000Z" } };
    render(<Conversation peer={{ agent_id: managedAgent.agent_id, source: "router-managed", activity_state: "failed", managed_agent: managedAgent }} />);
    expect(screen.getByRole("status")).toHaveTextContent("Unavailable: Managed process exited before readiness");
  });

  it("shows managed controls and terminating progress without a raw State popup action", async () => {
    const user = userEvent.setup();
    const onManageManaged = vi.fn();
    const managedAgent = { agent_id: "managed-worker", source: "router-managed" as const, cwd: "/tmp/managed-worker", model: { provider: "openai-codex", id: "gpt-5.6-luna" }, desired_state: "terminated", lifecycle_state: "terminating", activity_state: "active", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: null };

    render(<Conversation peer={{ agent_id: managedAgent.agent_id, source: "router-managed", activity_state: "active", managed_agent: managedAgent }} onManageManaged={onManageManaged} />);

    expect(screen.getByRole("status")).toHaveTextContent("Terminating…");
    await user.click(screen.getByRole("button", { name: "Manage worker" }));
    expect(onManageManaged).toHaveBeenCalledOnce();
    expect(screen.queryByRole("button", { name: "State" })).not.toBeInTheDocument();
  });
});
