import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
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
    expect(container.querySelector(".timeline-live-region")).toHaveTextContent("Subagent failed");
  });

  it("keeps named terminal subagent outcomes while hiding lifecycle chatter", () => {
    render(<MessageList messages={[]} peerId="managed-worker" activityEntries={[
      { id: "complete:1", agent_id: "managed-worker", message: "Child queued", presentation: { kind: "child", child_state: "queued" } },
      { id: "complete:2", agent_id: "managed-worker", message: "Child started", presentation: { kind: "child", child_state: "running" } },
      { id: "complete:3", agent_id: "managed-worker", message: "Child wrapped up", presentation: { kind: "child", child_state: "steered", child_label: "Delay fifteen seconds", summary: "Wrapped up in 15 seconds" } },
      { id: "complete:4", agent_id: "managed-worker", message: "Child completed", presentation: { kind: "child", child_state: "completed", child_label: "Wait twenty seconds", summary: "Completed in 20 seconds" } },
    ]} />);

    expect(screen.queryByText("Child queued")).not.toBeInTheDocument();
    expect(screen.queryByText("Child started")).not.toBeInTheDocument();
    expect(screen.queryByText("Child wrapped up")).not.toBeInTheDocument();
    expect(screen.queryByText("Child completed")).not.toBeInTheDocument();
    expect(screen.getByText("Subagent wrapped up")).toBeInTheDocument();
    expect(screen.getByText("Delay fifteen seconds · Wrapped up in 15 seconds")).toBeInTheDocument();
    expect(screen.getByText("Subagent finished")).toBeInTheDocument();
    expect(screen.getByText("Wait twenty seconds · Completed in 20 seconds")).toBeInTheDocument();
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
