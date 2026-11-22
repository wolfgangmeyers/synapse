import { describe, expect, it, vi, afterEach } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import { BroadcastMessageDialog } from "../../src/components/BroadcastMessageDialog";
import type { Agent } from "../../src/state/agent-projection";

afterEach(cleanup);

const mockAgents: Agent[] = [
  {
    agent_id: "agent-current",
    source: "router-managed" as const,
    activity_state: "active",
    lifecycle_state: "running",
  } as Agent,
  {
    agent_id: "agent-alpha",
    source: "router-managed" as const,
    activity_state: "active",
    lifecycle_state: "running",
  } as Agent,
  {
    agent_id: "agent-beta",
    source: "router-managed" as const,
    activity_state: "idle",
    lifecycle_state: "running",
  } as Agent,
  {
    agent_id: "agent-gamma",
    source: "router-managed" as const,
    activity_state: "stopped",
    lifecycle_state: "terminated",
  } as Agent,
];

describe("BroadcastMessageDialog", () => {
  it("filters out currentAgentId and initializes selection to active agents", () => {
    render(
      <BroadcastMessageDialog
        open={true}
        client={{ sendMessage: vi.fn() }}
        currentAgentId="agent-current"
        agents={mockAgents}
        onClose={vi.fn()}
      />
    );

    expect(screen.queryByText(/agent-current/)).not.toBeInTheDocument();
    expect(screen.getByText("Agent Alpha")).toBeInTheDocument();
    expect(screen.getByText("Agent Beta")).toBeInTheDocument();
    expect(screen.getByText("Agent Gamma")).toBeInTheDocument();

    // Toolbar count: 2 active out of 3 available (alpha and beta are active; gamma is stopped)
    expect(screen.getByText("Selected 2 of 3 agents (2 active)")).toBeInTheDocument();

    // Alpha and Beta checkboxes should be checked; Gamma unchecked
    const checkboxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    expect(checkboxes[0]?.checked).toBe(true);
    expect(checkboxes[1]?.checked).toBe(true);
    expect(checkboxes[2]?.checked).toBe(false);
  });

  it("handles selection controls: Select All, Deselect All, Select Active", () => {
    render(
      <BroadcastMessageDialog
        open={true}
        client={{ sendMessage: vi.fn() }}
        currentAgentId="agent-current"
        agents={mockAgents}
        onClose={vi.fn()}
      />
    );

    // Deselect All
    fireEvent.click(screen.getByRole("button", { name: "Deselect All" }));
    expect(screen.getByText("Selected 0 of 3 agents (2 active)")).toBeInTheDocument();
    const checkboxes = screen.getAllByRole("checkbox") as HTMLInputElement[];
    expect(checkboxes.every((c) => !c.checked)).toBe(true);

    // Select All
    fireEvent.click(screen.getByRole("button", { name: "Select All" }));
    expect(screen.getByText("Selected 3 of 3 agents (2 active)")).toBeInTheDocument();
    expect(checkboxes.every((c) => c.checked)).toBe(true);

    // Select Active
    fireEvent.click(screen.getByRole("button", { name: "Select Active" }));
    expect(screen.getByText("Selected 2 of 3 agents (2 active)")).toBeInTheDocument();
  });

  it("dispatches broadcast sequentially and shows summary", async () => {
    const sendMessage = vi.fn().mockResolvedValue({ ok: true });
    render(
      <BroadcastMessageDialog
        open={true}
        client={{ sendMessage }}
        currentAgentId="agent-current"
        agents={mockAgents}
        onClose={vi.fn()}
      />
    );

    const subjectInput = screen.getByPlaceholderText("Subject (optional)");
    const bodyInput = screen.getByPlaceholderText("Write broadcast message...");
    const sendButton = screen.getByRole("button", { name: "Send Broadcast" });

    // Disabled initially without body
    expect(sendButton).toBeDisabled();

    fireEvent.change(subjectInput, { target: { value: "Test Subject" } });
    fireEvent.change(bodyInput, { target: { value: "Hello everyone" } });

    expect(sendButton).toBeEnabled();

    fireEvent.click(sendButton);

    await waitFor(() => {
      expect(sendMessage).toHaveBeenCalledTimes(2);
    });

    expect(sendMessage).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({
        from: "agent-current",
        to: "agent-alpha",
        subject: "Test Subject",
        body: "Hello everyone",
        source: { kind: "web_ui" },
      })
    );

    expect(sendMessage).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({
        from: "agent-current",
        to: "agent-beta",
        subject: "Test Subject",
        body: "Hello everyone",
        source: { kind: "web_ui" },
      })
    );

    await waitFor(() => {
      expect(screen.getByText("Sent to 2 agents successfully.")).toBeInTheDocument();
    });
  });

  it("shows error summary when a recipient send fails", async () => {
    const sendMessage = vi.fn().mockImplementation((payload: { to: string }) => {
      if (payload.to === "agent-beta") {
        return Promise.reject(new Error("Peer rejected"));
      }
      return Promise.resolve({ ok: true });
    });

    render(
      <BroadcastMessageDialog
        open={true}
        client={{ sendMessage }}
        currentAgentId="agent-current"
        agents={mockAgents}
        onClose={vi.fn()}
      />
    );

    const bodyInput = screen.getByPlaceholderText("Write broadcast message...");
    fireEvent.change(bodyInput, { target: { value: "Alert message" } });

    fireEvent.click(screen.getByRole("button", { name: "Send Broadcast" }));

    await waitFor(() => {
      expect(screen.getByText("Sent to 1 agents; 1 failed.")).toBeInTheDocument();
    });

    expect(screen.getByText("Peer rejected")).toBeInTheDocument();
  });
});
