import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { afterEach, describe, it, expect, vi, beforeEach } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { WorkerToolsDialog } from "../../src/components/WorkerToolsDialog";
import type { RouterHttpClient, AgentToolsResponse } from "../../src/transport/http-client";

afterEach(cleanup);

describe("WorkerToolsDialog", () => {
  const mockAgentTools: AgentToolsResponse = {
    agent_id: "agent-123",
    workflow_override: false,
    active_workflow_state: "",
    tools: [
      { tool_name: "read", category: "File Operations", enabled: true, effective_enabled: true },
      { tool_name: "write", category: "File Operations", enabled: false, effective_enabled: false },
      { tool_name: "bash", category: "System & Shell", enabled: true, effective_enabled: true },
    ],
  };

  let mockClient: Partial<RouterHttpClient>;

  beforeEach(() => {
    mockClient = {
      getAgentTools: vi.fn().mockResolvedValue({ ...mockAgentTools }),
      updateAgentTools: vi.fn().mockResolvedValue({ ok: true }),
    };
  });

  it("renders worker tools grouped by category with grant counts", async () => {
    render(
      <WorkerToolsDialog
        open={true}
        agentId="agent-123"
        agentName="Agent 123"
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("File Operations")).toBeInTheDocument();
      expect(screen.getByText("System & Shell")).toBeInTheDocument();
      expect(screen.getByText("1 / 2 granted")).toBeInTheDocument();
    });

    expect(screen.getByText("read")).toBeInTheDocument();
    expect(screen.getByText("write")).toBeInTheDocument();
    expect(screen.getByText("bash")).toBeInTheDocument();
  });

  it("shows workflow override banner when workflow_override is true", async () => {
    mockClient.getAgentTools = vi.fn().mockResolvedValue({
      agent_id: "agent-123",
      workflow_override: true,
      active_workflow_state: "PlanningPhase",
      tools: [
        { tool_name: "read", category: "File Operations", enabled: true, effective_enabled: true },
        { tool_name: "bash", category: "System & Shell", enabled: true, effective_enabled: false },
      ],
    });

    render(
      <WorkerToolsDialog
        open={true}
        agentId="agent-123"
        agentName="Agent 123"
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByRole("alert")).toBeInTheDocument();
      expect(screen.getByText(/PlanningPhase/)).toBeInTheDocument();
      expect(screen.getByText("(effective: allowed)")).toBeInTheDocument();
      expect(screen.getByText("(effective: blocked)")).toBeInTheDocument();
    });
  });

  it("handles toggling an individual tool", async () => {
    render(
      <WorkerToolsDialog
        open={true}
        agentId="agent-123"
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("read")).toBeInTheDocument();
    });

    const readCheckbox = screen.getByLabelText("read") as HTMLInputElement;
    expect(readCheckbox.checked).toBe(true);

    fireEvent.click(readCheckbox);

    await waitFor(() => {
      expect(mockClient.updateAgentTools).toHaveBeenCalledWith("agent-123", {
        tool_name: "read",
        enabled: false,
      });
    });
  });

  it("handles Grant All and Revoke All for a category", async () => {
    render(
      <WorkerToolsDialog
        open={true}
        agentId="agent-123"
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("File Operations")).toBeInTheDocument();
    });

    const grantAllButtons = screen.getAllByRole("button", { name: "Grant All" });
    expect(grantAllButtons[0]).toBeDefined();
    fireEvent.click(grantAllButtons[0]!);

    await waitFor(() => {
      expect(mockClient.updateAgentTools).toHaveBeenCalledWith("agent-123", {
        category: "File Operations",
        enabled: true,
      });
    });

    const revokeAllButtons = screen.getAllByRole("button", { name: "Revoke All" });
    expect(revokeAllButtons[0]).toBeDefined();
    fireEvent.click(revokeAllButtons[0]!);

    await waitFor(() => {
      expect(mockClient.updateAgentTools).toHaveBeenCalledWith("agent-123", {
        category: "File Operations",
        enabled: false,
      });
    });
  });
});
