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
    const { container } = render(<Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} ownerId="other-user" conversationView="complete" messages={[
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
    expect(terminalArticle).toHaveClass("activity-entry--compact");
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

  it("does not render active managed subagents in a separate widget, preserving inline tool cards", () => {
    const active = { id: "complete:child", agent_id: "managed-worker", at: "2030-01-01T10:00:00.000Z", message: "Child started", presentation: { kind: "child" as const, child_state: "running" as const, outcome: "active" as const, child_label: "Review" } };
    const { container } = render(<Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} conversationView="complete" messages={[]} activityEntries={[
      { id: "complete:tool", agent_id: "managed-worker", at: "2030-01-01T10:01:00.000Z", message: "Run subagent", presentation: { kind: "tool", tool_name: "subagent", outcome: "success" } },
      active,
    ]} />);

    expect(screen.queryByLabelText("Active subagents")).not.toBeInTheDocument();
    expect(container.querySelector(".active-subagents")).not.toBeInTheDocument();
    expect(container.querySelector("#conversation-view-panel [data-activity-id='complete:child']")).not.toBeInTheDocument();
    expect(container.querySelector("#conversation-view-panel [data-activity-id='complete:tool']")).toBeInTheDocument();
  });

  it("renders a terminal managed subagent in the newest history position without an active subagents widget", () => {
    const active = { id: "complete:child", agent_id: "managed-worker", at: "2030-01-01T10:00:00.000Z", message: "Child started", presentation: { kind: "child" as const, child_state: "running" as const, outcome: "active" as const, child_label: "Review" } };
    const laterHistory = { id: "complete:tool", agent_id: "managed-worker", at: "2030-01-01T10:01:00.000Z", message: "Ran bash", presentation: { kind: "tool" as const, tool_name: "bash", outcome: "success" as const } };
    const { container, rerender } = render(<Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} conversationView="complete" messages={[]} activityEntries={[active, laterHistory]} />);

    expect(screen.queryByLabelText("Active subagents")).not.toBeInTheDocument();
    expect(container.querySelector(".active-subagents")).not.toBeInTheDocument();
    rerender(<Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} conversationView="complete" messages={[]} activityEntries={[{ ...active, at: "2030-01-01T10:02:00.000Z", message: "Subagent finished", presentation: { kind: "child", child_state: "completed", outcome: "completed", child_label: "Review", summary: "Completed successfully" } }, laterHistory]} />);

    expect(screen.queryByLabelText("Active subagents")).not.toBeInTheDocument();
    expect(container.querySelector(".active-subagents")).not.toBeInTheDocument();
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

  it("reports a failed managed agent as an explicit accessible header error", () => {
    const managedAgent = { agent_id: "managed-worker", source: "router-managed" as const, cwd: "/tmp/managed-worker", model: { provider: "openai-codex", id: "gpt-5.6-luna" }, desired_state: "running", lifecycle_state: "failed", activity_state: "active", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: { code: "PROCESS_STOP_FAILED", message: "Managed process exited before readiness", at: "2030-01-01T00:01:00.000Z" } };
    const { container } = render(<Conversation peer={{ agent_id: managedAgent.agent_id, source: "router-managed", activity_state: "compacting", managed_agent: managedAgent }} />);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveClass("managed-failure-alert");
    expect(alert).toHaveTextContent("Unavailable: Managed process exited before readiness");
    expect(alert).toHaveTextContent("PROCESS_STOP_FAILED");
    expect(within(alert).getByRole("img", { name: "Warning" })).toBeInTheDocument();
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.queryByText("Compacting")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Working")).not.toBeInTheDocument();
    expect(container.querySelector(".avatar.compacting, .avatar.working")).not.toBeInTheDocument();
  });

  it("uses the safe managed failure fallback when no error code is available", () => {
    const managedAgent = { agent_id: "managed-worker", source: "router-managed" as const, cwd: "/tmp/managed-worker", model: { provider: "openai-codex", id: "gpt-5.6-luna" }, desired_state: "running", lifecycle_state: "running", activity_state: "failed", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: null };
    render(<Conversation peer={{ agent_id: managedAgent.agent_id, source: "router-managed", activity_state: "active", managed_agent: managedAgent }} />);

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Unavailable: managed worker failed.");
    expect(alert.querySelector(".managed-failure-code")).not.toBeInTheDocument();
    expect(screen.queryByLabelText("Working")).not.toBeInTheDocument();
  });

  it("lets managed failure suppress contradictory terminating presentation", () => {
    const managedAgent = { agent_id: "managed-worker", source: "router-managed" as const, cwd: "/tmp/managed-worker", model: { provider: "openai-codex", id: "gpt-5.6-luna" }, desired_state: "terminated", lifecycle_state: "terminating", activity_state: "failed", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: { code: "PROCESS_EXITED", message: "Managed process exited", at: "2030-01-01T00:01:00.000Z" } };
    const { container } = render(<Conversation peer={{ agent_id: managedAgent.agent_id, source: "router-managed", activity_state: "idle", managed_agent: managedAgent }} />);

    expect(screen.getByRole("alert")).toHaveTextContent("Unavailable: Managed process exited");
    expect(screen.queryByText("Terminating…")).not.toBeInTheDocument();
    expect(container.querySelector(".avatar.terminating")).not.toBeInTheDocument();
  });

  it("shows managed controls and terminating progress without a raw State popup action", async () => {
    const user = userEvent.setup();
    const managedAgent = { agent_id: "managed-worker", source: "router-managed" as const, cwd: "/tmp/managed-worker", model: { provider: "openai-codex", id: "gpt-5.6-luna" }, desired_state: "terminated", lifecycle_state: "terminating", activity_state: "active", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: null };

    render(<Conversation peer={{ agent_id: managedAgent.agent_id, source: "router-managed", activity_state: "active", managed_agent: managedAgent }} />);

    expect(screen.getByRole("status")).toHaveTextContent("Terminating…");
    const manageButton = screen.getByRole("button", { name: "More agent controls" });
    await user.click(manageButton);
    expect(screen.queryByRole("button", { name: "State" })).not.toBeInTheDocument();
  });

  it("renders complete view as the only view for router-managed agents without toggle tabs", () => {
    const onConversationViewChange = vi.fn();
    render(<Conversation
      peer={{ agent_id: "managed-worker", source: "router-managed", activity_state: "idle" }}
      conversationView="quiet"
      onConversationViewChange={onConversationViewChange}
      messages={[{ id: "msg-1", from: "managed-worker", to: "wolfgang", body: "Hello from managed worker", created_at: "2030-01-01T10:00:00.000Z" }]}
      activityEntries={[{ id: "act-1", agent_id: "managed-worker", level: "info", message: "Managed worker restarted", at: "2030-01-01T10:01:00.000Z" }]}
    />);

    // Toggle tabs must not be rendered for managed agents
    expect(screen.queryByRole("tab", { name: "Quiet messages" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Complete messages and activity" })).not.toBeInTheDocument();
    expect(document.querySelector(".conversation-view-tabs")).not.toBeInTheDocument();

    // Complete timeline must be rendered with both messages and activity entries
    expect(screen.getByText("Hello from managed worker")).toBeInTheDocument();
    expect(screen.getByText("Managed worker restarted")).toBeInTheDocument();
  });

  it("renders complete view as the only view for embedded runtime agents without toggle tabs", () => {
    const onConversationViewChange = vi.fn();
    render(<Conversation
      peer={{ agent_id: "embedded-worker", activity_state: "idle", managed_agent: { agent_id: "embedded-worker", source: "router-managed", runtime: "embedded", cwd: "/test", model: { provider: "openai-codex", id: "gpt-5.6" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "0".repeat(64), installed_tool_count: 0 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null } }}
      conversationView="quiet"
      onConversationViewChange={onConversationViewChange}
      messages={[{ id: "msg-emb", from: "embedded-worker", to: "wolfgang", body: "Embedded response", created_at: "2030-01-01T10:00:00.000Z" }]}
      activityEntries={[{ id: "act-emb", agent_id: "embedded-worker", level: "info", message: "Embedded tool execution", at: "2030-01-01T10:01:00.000Z" }]}
    />);

    // Toggle tabs must not be rendered for embedded agents
    expect(screen.queryByRole("tab", { name: "Quiet messages" })).not.toBeInTheDocument();
    expect(screen.queryByRole("tab", { name: "Complete messages and activity" })).not.toBeInTheDocument();
    expect(document.querySelector(".conversation-view-tabs")).not.toBeInTheDocument();

    // Complete timeline must be rendered
    expect(screen.getByText("Embedded response")).toBeInTheDocument();
    expect(screen.getByText("Embedded tool execution")).toBeInTheDocument();
  });

  it("leaves tmux agent UI intact with toggle tabs when provided", async () => {
    const user = userEvent.setup();
    function TmuxConversation(): ReactElement {
      const [view, setView] = useState<ConversationView>("quiet");
      return <Conversation
        peer={{ agent_id: "tmux-worker", source: "tmux", activity_state: "idle" }}
        conversationView={view}
        onConversationViewChange={setView}
        messages={[{ id: "msg-tmux", from: "tmux-worker", to: "wolfgang", body: "Tmux message", created_at: "2030-01-01T10:00:00.000Z" }]}
        activityEntries={[{ id: "act-tmux", agent_id: "tmux-worker", level: "info", message: "Tmux activity", at: "2030-01-01T10:01:00.000Z" }]}
      />;
    }

    render(<TmuxConversation />);

    const quietTab = screen.getByRole("tab", { name: "Quiet messages" });
    const completeTab = screen.getByRole("tab", { name: "Complete messages and activity" });
    expect(quietTab).toBeInTheDocument();
    expect(completeTab).toBeInTheDocument();
    expect(quietTab).toHaveAttribute("aria-selected", "true");

    // In quiet view, activity entry is not shown
    expect(screen.getByText("Tmux message")).toBeInTheDocument();
    expect(screen.queryByText("Tmux activity")).not.toBeInTheDocument();

    // Switching to complete view shows activity entry
    await user.click(completeTab);
    expect(completeTab).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Tmux message")).toBeInTheDocument();
    expect(screen.getByText("Tmux activity")).toBeInTheDocument();
  });

  it("renders mailbox messages addressed to currentUser as chat message bubbles in conversation history", () => {
    const { container } = render(
      <Conversation
        peer={{ agent_id: "worker-1", activity_state: "idle" }}
        ownerId="wolfgang"
        conversationView="complete"
        messages={[]}
        activityEntries={[
          {
            id: "act-mail-to-user",
            agent_id: "worker-1",
            at: "2030-01-01T10:02:00.000Z",
            presentation: {
              kind: "tool",
              tool_name: "mecha_mailbox_send",
              outcome: "success",
              to: "wolfgang",
            },
            tool_input: {
              recipient: "wolfgang",
              body: "Hello Wolfgang, I have finished implementing the task.",
            },
          },
        ]}
      />
    );

    // Rendered as a standard message bubble
    const bubble = container.querySelector<HTMLElement>(".bubble-wrap.incoming");
    expect(bubble).toBeInTheDocument();
    expect(within(bubble!).getByText("Hello Wolfgang, I have finished implementing the task.")).toBeInTheDocument();
    // Not rendered as an activity card
    expect(container.querySelector("[data-activity-id='act-mail-to-user']")).not.toBeInTheDocument();
  });

  it("renders mailbox messages addressed to other agents as compact outgoing activity cards", () => {
    const { container } = render(
      <Conversation
        peer={{ agent_id: "worker-1", activity_state: "idle" }}
        ownerId="wolfgang"
        conversationView="complete"
        messages={[]}
        activityEntries={[
          {
            id: "act-mail-to-supervisor",
            agent_id: "worker-1",
            at: "2030-01-01T10:02:00.000Z",
            presentation: {
              kind: "tool",
              tool_name: "mecha_mailbox_send",
              outcome: "success",
              to: "supervisor",
            },
            tool_input: {
              recipient: "supervisor",
              body: "Reporting progress to supervisor.",
            },
          },
        ]}
      />
    );

    // Kept as outgoing activity card "To supervisor"
    expect(screen.getByText("To supervisor")).toBeInTheDocument();
    expect(screen.getByText("Reporting progress to supervisor.")).toBeInTheDocument();
    expect(container.querySelector(".bubble-wrap")).not.toBeInTheDocument();
  });

  it("renders thinking trace card before the assistant message when thinking content is present on mailbox send", () => {
    const { container } = render(
      <Conversation
        peer={{ agent_id: "worker-1", activity_state: "idle" }}
        ownerId="wolfgang"
        conversationView="complete"
        messages={[]}
        activityEntries={[
          {
            id: "act-mail-with-thought",
            agent_id: "worker-1",
            at: "2030-01-01T10:02:00.000Z",
            presentation: {
              kind: "tool",
              tool_name: "mecha_mailbox_send",
              outcome: "success",
              to: "wolfgang",
            },
            tool_input: {
              recipient: "wolfgang",
              body: "Finished the task.",
            },
            thinking: "I should let Wolfgang know that everything passed.",
          },
        ]}
      />
    );

    // Thinking trace card rendered with brain icon and preview
    expect(container.querySelector(".thinking-trace-card")).toBeInTheDocument();
    expect(container.querySelector(".fa-brain")).toBeInTheDocument();
    expect(screen.getByText("I should let Wolfgang know that everything passed.")).toBeInTheDocument();

    // Assistant message bubble also rendered with full body
    const bubble = container.querySelector<HTMLElement>(".bubble-wrap.incoming");
    expect(bubble).toBeInTheDocument();
    expect(within(bubble!).getByText("Finished the task.")).toBeInTheDocument();
  });

  it("omits fallback chat bubble when assistant has Action completed without additional text while tool calls exist", () => {
    const { container } = render(
      <Conversation
        peer={{ agent_id: "worker-1", activity_state: "idle" }}
        ownerId="wolfgang"
        conversationView="complete"
        messages={[]}
        activityEntries={[
          {
            id: "act-bash-call",
            agent_id: "worker-1",
            at: "2030-01-01T10:01:00.000Z",
            presentation: {
              kind: "tool",
              tool_name: "bash",
              outcome: "success",
            },
            tool_input: {
              command: "echo test",
            },
          },
          {
            id: "act-fallback-reply",
            agent_id: "worker-1",
            at: "2030-01-01T10:02:00.000Z",
            message: "Message sent",
            message_content: "(Action completed without additional text)",
            presentation: {
              kind: "message",
              outcome: "success",
              from: "worker-1",
              to: "wolfgang",
            },
          },
        ]}
      />
    );

    // The tool execution card is rendered
    expect(container.querySelector("[data-activity-id='act-bash-call']")).toBeInTheDocument();
    // No chat bubble is rendered for the fallback text
    expect(container.querySelector(".bubble-wrap")).not.toBeInTheDocument();
    expect(screen.queryByText("(Action completed without additional text)")).not.toBeInTheDocument();
  });

  it("omits chat bubble when assistant has empty text content while tool calls exist", () => {
    const { container } = render(
      <Conversation
        peer={{ agent_id: "worker-1", activity_state: "idle" }}
        ownerId="wolfgang"
        conversationView="complete"
        messages={[]}
        activityEntries={[
          {
            id: "act-read-call",
            agent_id: "worker-1",
            at: "2030-01-01T10:01:00.000Z",
            presentation: {
              kind: "tool",
              tool_name: "read",
              outcome: "success",
            },
            tool_input: {
              path: "/tmp/foo",
            },
          },
          {
            id: "act-empty-reply",
            agent_id: "worker-1",
            at: "2030-01-01T10:02:00.000Z",
            message: "Message sent",
            message_content: "",
            presentation: {
              kind: "message",
              outcome: "success",
              from: "worker-1",
              to: "wolfgang",
            },
          },
        ]}
      />
    );

    // The tool execution card is rendered
    expect(container.querySelector("[data-activity-id='act-read-call']")).toBeInTheDocument();
    // No empty chat bubble is rendered
    expect(container.querySelector(".bubble-wrap")).not.toBeInTheDocument();
  });

  it("deduplicates identical thinking trace cards before consecutive tool calls in a multi-step turn", () => {
    const identicalThinking = "Investigating the file structure to find the right configuration.";
    const { container } = render(
      <Conversation
        peer={{ agent_id: "worker-1", activity_state: "idle" }}
        ownerId="wolfgang"
        conversationView="complete"
        messages={[]}
        activityEntries={[
          {
            id: "act-step-1",
            agent_id: "worker-1",
            at: "2030-01-01T10:01:00.000Z",
            presentation: {
              kind: "tool",
              tool_name: "bash",
              outcome: "success",
              thinking: identicalThinking,
            },
            tool_input: { command: "ls -la" },
            thinking: identicalThinking,
          },
          {
            id: "act-step-2",
            agent_id: "worker-1",
            at: "2030-01-01T10:01:05.000Z",
            presentation: {
              kind: "tool",
              tool_name: "read",
              outcome: "success",
              thinking: identicalThinking,
            },
            tool_input: { path: "config.json" },
            thinking: identicalThinking,
          },
          {
            id: "act-step-3",
            agent_id: "worker-1",
            at: "2030-01-01T10:01:10.000Z",
            presentation: {
              kind: "thinking",
              outcome: "success",
              thinking: identicalThinking,
            },
            thinking: identicalThinking,
          },
        ]}
      />
    );

    // Both tool entries should be rendered in the DOM
    expect(container.querySelector("[data-activity-id='act-step-1']")).toBeInTheDocument();
    expect(container.querySelector("[data-activity-id='act-step-2']")).toBeInTheDocument();

    // Exactly one thinking trace card should be rendered across the entire sequence
    const thinkingCards = container.querySelectorAll(".thinking-trace-card");
    expect(thinkingCards).toHaveLength(1);
    expect(screen.getByText(identicalThinking)).toBeInTheDocument();
  });

  it("renders distinct thinking trace cards when thinking changes in a multi-step turn", () => {
    const thinking1 = "First thought: search for files.";
    const thinking2 = "Second thought: apply patch to configuration.";
    const { container } = render(
      <Conversation
        peer={{ agent_id: "worker-1", activity_state: "idle" }}
        ownerId="wolfgang"
        conversationView="complete"
        messages={[]}
        activityEntries={[
          {
            id: "act-step-1",
            agent_id: "worker-1",
            at: "2030-01-01T10:01:00.000Z",
            presentation: {
              kind: "tool",
              tool_name: "bash",
              outcome: "success",
              thinking: thinking1,
            },
            tool_input: { command: "find ." },
            thinking: thinking1,
          },
          {
            id: "act-step-2",
            agent_id: "worker-1",
            at: "2030-01-01T10:01:05.000Z",
            presentation: {
              kind: "tool",
              tool_name: "edit",
              outcome: "success",
              thinking: thinking2,
            },
            tool_input: { path: "config.json" },
            thinking: thinking2,
          },
        ]}
      />
    );

    // Both distinct thinking trace cards should be rendered
    const thinkingCards = container.querySelectorAll(".thinking-trace-card");
    expect(thinkingCards).toHaveLength(2);
    expect(screen.getByText(thinking1)).toBeInTheDocument();
    expect(screen.getByText(thinking2)).toBeInTheDocument();
  });

  it("renders an unbroken chronological timeline across context resets without hiding older history", () => {
    const { container } = render(
      <Conversation
        peer={{ agent_id: "worker-1", activity_state: "idle" }}
        ownerId="wolfgang"
        conversationView="complete"
        messages={[
          {
            id: "msg-1",
            from: "wolfgang",
            to: "worker-1",
            body: "First task in Epoch 1",
            created_at: "2030-01-01T10:00:00.000Z",
          },
        ]}
        activityEntries={[
          {
            id: "complete:1",
            agent_id: "worker-1",
            at: "2030-01-01T10:00:05.000Z",
            presentation: {
              kind: "tool",
              tool_name: "bash",
              outcome: "success",
              summary: "Ran bash in Epoch 1",
            },
          },
          {
            id: "complete:2",
            agent_id: "worker-1",
            at: "2030-01-01T10:00:10.000Z",
            category: "context_reset",
            message: "Context reset (Epoch 2)",
          },
          {
            id: "complete:3",
            agent_id: "worker-1",
            at: "2030-01-01T10:00:15.000Z",
            presentation: {
              kind: "tool",
              tool_name: "write",
              outcome: "success",
              summary: "Ran write in Epoch 2",
            },
          },
        ]}
      />
    );

    // Verify older Epoch 1 message and tool are still visible
    expect(screen.getByText("First task in Epoch 1")).toBeInTheDocument();
    expect(screen.getByText("Ran bash in Epoch 1")).toBeInTheDocument();

    // Verify context reset divider is rendered prominently
    const divider = container.querySelector(".timeline-context-reset-divider");
    expect(divider).toBeInTheDocument();
    expect(divider?.textContent).toContain("─── Context Reset (Epoch 2) ───");

    // Verify newer Epoch 2 activity is visible after the reset
    expect(screen.getByText("Ran write in Epoch 2")).toBeInTheDocument();
  });

  it("deduplicates messages and activity entries for conversation between user and agent", () => {
    const messages = [
      {
        id: "msg-1",
        from: "wolfgang",
        to: "conversation-refactor",
        body: "I've restarted the router. Testing one, two, three.",
        created_at: "2026-10-18T17:51:51.709Z",
      },
      {
        id: "msg-2",
        from: "conversation-refactor",
        to: "wolfgang",
        body: "Loud and clear! I'm here and ready. How are the messages and chat bubbles looking on your end now?",
        created_at: "2026-10-18T17:51:56.031Z",
      },
    ];

    const activityEntries = [
      {
        id: "complete:68986",
        agent_id: "conversation-refactor",
        message: "Message received",
        sender_identity: "wolfgang",
        at: "2026-10-18T17:51:51.940Z",
        presentation: {
          kind: "message" as const,
          outcome: "success" as const,
          from: "wolfgang",
          to: "conversation-refactor",
        },
        message_content: "I've restarted the router. Testing one, two, three.",
      },
      {
        id: "complete:68987",
        agent_id: "conversation-refactor",
        message: "Message sent",
        sender_identity: "conversation-refactor",
        at: "2026-10-18T17:51:56.031Z",
        presentation: {
          kind: "message" as const,
          outcome: "success" as const,
          from: "conversation-refactor",
          to: "wolfgang",
        },
        message_content: "Loud and clear! I'm here and ready. How are the messages and chat bubbles looking on your end now?",
      },
    ];

    const { container } = render(
      <MessageList
        peerId="conversation-refactor"
        ownerId="wolfgang"
        messages={messages}
        activityEntries={activityEntries}
      />
    );

    // Number of .bubble-wrap (or message bubbles) with the assistant's text is EXACTLY 1 (no duplicate assistant bubble).
    const bubbles = Array.from(container.querySelectorAll(".bubble-wrap"));
    const assistantBubbles = bubbles.filter((b) =>
      b.textContent?.includes("Loud and clear! I'm here and ready.")
    );
    expect(assistantBubbles).toHaveLength(1);

    // Number of activity cards with label "From wolfgang" is EXACTLY 0 (no duplicate activity envelope card for the user's message).
    expect(screen.queryByText("From wolfgang")).not.toBeInTheDocument();

    // Number of .bubble-wrap with user's text is EXACTLY 1.
    const userBubbles = bubbles.filter((b) =>
      b.textContent?.includes("I've restarted the router. Testing one, two, three.")
    );
    expect(userBubbles).toHaveLength(1);
  });
});

