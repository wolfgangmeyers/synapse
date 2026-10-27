import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";

afterEach(() => {
  cleanup();
  localStorage.clear();
});
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
    expect(Array.from(header.children).map((child) => child.getAttribute("data-testid") ?? child.className)).toEqual(["conversation-header-avatar-col", "conversation-header-content"]);
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
    localStorage.setItem("mecha.toolbar.pinnedControls", JSON.stringify(["goal", "reminders", "toolSettings"]));
    const agent: Agent = { agent_id: "fixture-peer", activity_state: "active", subagent_count: 0, subagents: [] };
    render(<ConversationHeader agent={agent} onGoal={() => undefined} onReminders={() => undefined} onInlineToolSettings={() => undefined} />);
    const banner = screen.getAllByRole("banner").at(-1);
    expect(banner).toBeDefined();
    if (!banner) throw new Error("Selected header is missing");
    expect(banner).toHaveClass("conversation-header", "selected");
    const actions = banner.querySelector(".conversation-header-actions");
    expect(actions).not.toBeNull();

    const heading = screen.getAllByRole("heading", { level: 2 }).find((candidate) => candidate.textContent?.includes("Fixture Peer"));
    expect(heading).toBeDefined();
    if (!heading) throw new Error("Selected peer heading is missing");
    expect(heading.querySelector(".peer-name-label")).toHaveTextContent("Fixture Peer");
    expect(actions?.querySelector("button[aria-label='Set goal']")).toBeInTheDocument();

    expect(banner.querySelector("button[aria-label='Add reminder']")).toBeInTheDocument();
    expect(banner.querySelector("button[aria-label='Inline tool settings']")).toBeInTheDocument();
    expect(banner.querySelector(".peer-activity")).toHaveClass("peer-activity");
    expect(banner.querySelector(".peer-activity")).not.toHaveTextContent("fixture-peer");
  });

  it("uses compact semantic icons while retaining action labels", () => {
    localStorage.setItem("mecha.toolbar.pinnedControls", JSON.stringify(["goal", "reminders", "toolSettings"]));
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

  it("keeps the agent-scoped Scratchpad action in the conversation header", () => {
    const onScratchpad = vi.fn();
    const agent: Agent = { agent_id: "rpc-test", activity_state: "idle", subagent_count: 0, subagents: [] };
    render(<ConversationHeader agent={agent} onScratchpad={onScratchpad} />);
    const button = screen.getByRole("button", { name: "Scratchpad" });
    expect(button).toHaveAttribute("title", "View scratchpad for Rpc Test");
    expect(button).toHaveClass("scratchpad-header-button");
    expect(button.querySelector(".fa-note-sticky")).toBeInTheDocument();
    fireEvent.click(button);
    expect(onScratchpad).toHaveBeenCalledOnce();
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
    const detail = screen.getByRole("banner").querySelector(".peer-activity");
    expect(detail).toHaveClass("peer-activity");
    expect(detail).not.toHaveTextContent("typescript-extensions");
    const subagentsCard = screen.getByRole("banner").querySelector(".subagents-card");
    expect(subagentsCard).toHaveTextContent("2");
    expect(screen.getByRole("banner").querySelector(".avatar.working")).toBeInTheDocument();
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
    expect(screen.getByRole("banner").querySelector(".peer-activity")).toHaveTextContent("Compacting");
  });
});
