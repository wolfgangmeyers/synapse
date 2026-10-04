import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

afterEach(cleanup);
import { AppShell } from "../../src/components/AppShell";
import { avatarForAgent } from "../../src/components/AgentAvatar";
import { ConversationHeader } from "../../src/components/ConversationHeader";
import { ModelPicker } from "../../src/components/ModelPicker";
import type { ModelSummary } from "../../src/domain/models";
import type { Agent } from "../../src/state/agent-projection";

describe("AppShell", () => {
  it("provides the two-column application landmarks", () => {
    render(<AppShell sidebar={<div>Agents</div>} conversation={<div>Chat</div>} />);
    expect(screen.getByRole("complementary").textContent).toContain("Agents");
    expect(screen.getByRole("region", { name: "Conversation" }).textContent).toContain("Chat");
  });

  it("keeps model and prompt controls in the desktop conversation header", () => {
    render(<ConversationHeader modelControls={<div data-testid="model-controls">Model</div>} promptControls={<div data-testid="prompt-controls">Prompt</div>} />);
    const header = screen.getByRole("banner");
    expect(header.querySelector("[data-testid='prompt-controls']")).toBeTruthy();
    expect(header.querySelector("[data-testid='model-controls']")).toBeTruthy();
    expect(Array.from(header.children).map((child) => child.getAttribute("data-testid") ?? child.className)).toEqual(["avatar muted", "peer-heading", "prompt-controls", "model-controls"]);
  });

  it("keeps the selected agent name and model control in the narrow header contract", () => {
    const model: ModelSummary = { provider: "fixture-provider", id: "fixture-model" };
    const agent: Agent = { agent_id: "fixture-peer", activity_state: "idle", subagent_count: 0, subagents: [], current_model: model, available_models: [model] };
    render(<ConversationHeader agent={agent} modelControls={<ModelPicker models={[model]} currentModel={model} onChange={() => undefined} />} />);
    const banner = screen.getAllByRole("banner").at(-1);
    expect(banner).toBeDefined();
    if (!banner) throw new Error("Selected header is missing");
    expect(banner.querySelector(".peer-name-label")).toHaveTextContent("Fixture Peer");
    expect(banner.querySelector(".model-current")).toHaveTextContent("fixture-provider/fixture-model");
  });

  it("uses the selected agent ID to select its bundled avatar", () => {
    const agent: Agent = { agent_id: "fixture peer", activity_state: "idle" };
    const { container } = render(<ConversationHeader agent={agent} />);
    expect(container.querySelector(".avatar img")).toHaveAttribute("src", avatarForAgent(agent.agent_id).src);
  });

  it("keeps selected-peer actions inside the heading after the status", () => {
    const agent: Agent = { agent_id: "fixture-peer", activity_state: "active", subagent_count: 0, subagents: [] };
    render(<ConversationHeader agent={agent} onGoal={() => undefined} onReminders={() => undefined} />);
    const banner = screen.getAllByRole("banner").at(-1);
    expect(banner).toBeDefined();
    if (!banner) throw new Error("Selected header is missing");
    expect(banner).toHaveClass("conversation-header", "selected");
    expect(banner.querySelector(".conversation-header-actions")).toBeNull();

    const heading = screen.getAllByRole("heading", { level: 2 }).find((candidate) => candidate.textContent?.includes("Fixture Peer"));
    expect(heading).toBeDefined();
    if (!heading) throw new Error("Selected peer heading is missing");
    expect(heading.querySelector(".peer-name-label")).toHaveTextContent("Fixture Peer");
    expect(Array.from(heading.children).map((child) => child.className)).toEqual([
      "peer-name-label",
      "working-indicator inline",
      "interrupt-worker-button header-button-interrupt",
      "goal-header-button ",
      "reminder-indicator empty",
      "inline-tool-settings-button",
    ]);
    expect(heading.querySelector("button[aria-label='Set goal']")).toBeInTheDocument();

    expect(banner.querySelector("button[aria-label='Add reminder']")).toBeInTheDocument();
    expect(banner.querySelector("button[aria-label='Inline tool settings']")).toBeInTheDocument();
    expect(banner.querySelector(".peer-heading > p")).toHaveClass("peer-activity");
    expect(banner.querySelector(".peer-heading > p")).not.toHaveTextContent("fixture-peer");
  });

  it("uses compact semantic icons while retaining action labels", () => {
    const agent: Agent = { agent_id: "fixture-peer", activity_state: "idle", subagent_count: 0, subagents: [] };
    render(<ConversationHeader agent={agent} onGoal={() => undefined} onReminders={() => undefined} onInlineToolSettings={() => undefined} />);

    const goal = screen.getByRole("button", { name: "Set goal" });
    const reminder = screen.getByRole("button", { name: "Add reminder" });
    const settings = screen.getByRole("button", { name: "Inline tool settings" });
    expect(goal).toHaveAttribute("title", "Set goal");
    expect(goal.querySelector(".fa-flag")).toBeInTheDocument();
    expect(reminder).toHaveAttribute("title", "Add reminder");
    expect(reminder.querySelector(".fa-clock")).toBeInTheDocument();
    expect(settings).toHaveAttribute("title", "Inline tool settings");
    expect(settings.querySelector(".fa-gear")).toBeInTheDocument();
  });

  it("keeps the agent-scoped Logs action in the conversation header", () => {
    const onLogs = vi.fn();
    const agent: Agent = { agent_id: "rpc-test", activity_state: "idle", subagent_count: 0, subagents: [] };
    render(<ConversationHeader agent={agent} onLogs={onLogs} />);
    const button = screen.getByRole("button", { name: "Logs" });
    expect(button).toHaveAttribute("title", "View logs for Rpc Test");
    expect(button).toHaveClass("logs-header-button");
    expect(button.querySelector(".fa-file-lines")).toBeInTheDocument();
    fireEvent.click(button);
    expect(onLogs).toHaveBeenCalledOnce();
  });

  it("keeps the managed-worker control in the header action row", () => {
    const onManageManaged = vi.fn();
    const agent: Agent = { agent_id: "managed-peer", source: "router-managed", activity_state: "idle", subagent_count: 0, subagents: [] };
    render(<ConversationHeader agent={agent} onManageManaged={onManageManaged} />);
    const button = screen.getByRole("button", { name: "Manage worker" });
    expect(button).toHaveClass("managed-agent-header-button");
    expect(button.querySelector(".fa-gear")).toBeInTheDocument();
    expect(button.closest("h2")).toBeInTheDocument();
    expect(screen.getByRole("banner").querySelector(".managed-agent-controls")).toBeNull();
    fireEvent.click(button);
    expect(onManageManaged).toHaveBeenCalledOnce();
  });

  it("marks the reminder button active when the selected agent has a reminder", () => {
    const agent: Agent = { agent_id: "fixture-peer", activity_state: "idle", subagent_count: 0, subagents: [] };
    render(<ConversationHeader agent={agent} activeReminderCount={1} />);
    expect(screen.getByRole("button", { name: "Add reminder" })).toHaveClass("reminder-indicator");
    expect(screen.getByRole("button", { name: "Add reminder" })).not.toHaveClass("empty");
  });

  it("renders legacy activity and subagent content in the header detail line", () => {
    const agent: Agent = {
      agent_id: "typescript-extensions",
      activity_state: "active",
      subagent_count: 2,
      subagents: [{ id: "one", description: "Run tests" }, { id: "two", description: "Check types" }],
    };
    render(<ConversationHeader agent={agent} expanded onToggleSubagents={() => undefined} />);
    const detail = screen.getByRole("banner").querySelector(".peer-heading > p");
    expect(detail).toHaveClass("peer-activity");
    expect(detail).not.toHaveTextContent("typescript-extensions");
    expect(detail).toHaveTextContent("2 subagents");
    expect(screen.getByRole("heading", { name: /Working/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "2 subagents" })).toHaveAttribute("aria-controls", "conversation-header-subagents-typescript-extensions");
    expect(document.getElementById("conversation-header-subagents-typescript-extensions")).toBeInTheDocument();
  });

  it("does not add managed-only ready or working language", () => {
    const agent: Agent = { agent_id: "managed-peer", source: "router-managed", activity_state: "idle", subagent_count: 0, subagents: [] };
    render(<ConversationHeader agent={agent} />);
    expect(screen.queryByText("Ready")).not.toBeInTheDocument();
    expect(screen.queryByText("Working…")).not.toBeInTheDocument();
  });

  it("uses the shared compacting indicator in the header detail line", () => {
    const agent: Agent = { agent_id: "fixture-peer", activity_state: "compacting", subagent_count: 0, subagents: [] };
    render(<ConversationHeader agent={agent} />);
    expect(screen.getByRole("banner").querySelector(".peer-heading > p")).toHaveTextContent("Compacting");
  });
});
