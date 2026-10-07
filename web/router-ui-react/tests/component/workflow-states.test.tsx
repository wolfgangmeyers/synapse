import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { WorkflowStatesPage } from "../../src/components/WorkflowStatesPage";
import type { RouterHttpClient } from "../../src/transport/http-client";

describe("WorkflowStatesPage", () => {
  const mockStates = [
    {
      id: "state-1",
      name: "Planning",
      description: "Decompose requirements into steps",
      entry_text: "You are in the planning stage. Do not write code yet.",
      created_at: 1700000000000,
      updated_at: 1700000000000,
    },
    {
      id: "state-2",
      name: "Implementation",
      description: "Write and test features",
      entry_text: "Execute the plan step by step.",
      created_at: 1700000000000,
      updated_at: 1700000000000,
    },
  ];

  it("renders workflow states list and header properly", async () => {
    const mockClient = {
      workflowStates: vi.fn().mockResolvedValue({ workflow_states: mockStates }),
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowStatesPage client={mockClient} />);

    expect(screen.getByText("Loading workflow states...")).toBeDefined();

    await waitFor(() => {
      expect(screen.getByText("Planning")).toBeDefined();
      expect(screen.getByText("Implementation")).toBeDefined();
    });

    expect(screen.getByText("Decompose requirements into steps")).toBeDefined();
    expect(screen.getByText("You are in the planning stage. Do not write code yet.")).toBeDefined();
    unmount();
  });

  it("handles back navigation button click", async () => {
    const user = userEvent.setup();
    const mockClient = {
      workflowStates: vi.fn().mockResolvedValue({ workflow_states: [] }),
    } as unknown as RouterHttpClient;
    const onBack = vi.fn();

    const { unmount } = render(<WorkflowStatesPage client={mockClient} onBack={onBack} />);

    await waitFor(() => {
      expect(screen.getByText("No workflow states yet.")).toBeDefined();
    });

    const backBtn = screen.getByRole("button", { name: /Dashboard/i });
    await user.click(backBtn);
    expect(onBack).toHaveBeenCalledTimes(1);
    unmount();
  });
});
