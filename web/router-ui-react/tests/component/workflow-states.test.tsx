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
  it("renders reminder badges on state card", async () => {
    const statesWithReminders = [
      {
        id: "state-reminders",
        name: "Review Loop",
        description: "Checking review progress",
        entry_text: "In review",
        reminders: [
          {
            id: "rem-1",
            type: "one_shot" as const,
            delay_seconds: 300,
            subject: "Check ping",
            body: "Did reviewer respond?",
          },
          {
            id: "rem-2",
            type: "recurring" as const,
            delay_seconds: 0,
            interval_seconds: 600,
            body: "Periodic ping",
          },
        ],
        created_at: "2025-01-01T00:00:00Z",
        updated_at: "2025-01-01T00:00:00Z",
      },
    ];

    const mockClient = {
      workflowStates: vi.fn().mockResolvedValue({ workflow_states: statesWithReminders }),
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowStatesPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByText("Review Loop")).toBeDefined();
    });

    expect(screen.getByText("One-shot in 5m")).toBeDefined();
    expect(screen.getByText("Recurring every 10m")).toBeDefined();

    unmount();
  });

  it("can add, edit, and configure reminders in the modal dialog", async () => {
    const user = userEvent.setup();
    const createMock = vi.fn().mockResolvedValue({
      workflow_state: {
        id: "new-state-id",
        name: "Testing State",
        description: "Testing reminders",
        entry_text: "Entry message",
        reminders: [],
        created_at: "2025-01-01T00:00:00Z",
        updated_at: "2025-01-01T00:00:00Z",
      },
    });
    const mockClient = {
      workflowStates: vi.fn().mockResolvedValue({ workflow_states: [] }),
      createWorkflowState: createMock,
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowStatesPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByText("No workflow states yet.")).toBeDefined();
    });

    // Click "New State" button in header
    const newStateBtn = screen.getByRole("button", { name: /New State/i });
    await user.click(newStateBtn);

    // Form inputs
    const nameInput = screen.getByLabelText(/State Name \*/i);
    await user.type(nameInput, "Triage State");

    // Add reminder
    const addReminderBtn = screen.getByRole("button", { name: /Add reminder/i });
    await user.click(addReminderBtn);

    // Modal popup should open
    expect(screen.getByText("New Reminder")).toBeDefined();

    // Fill in reminder text inside the popup
    const reminderBody = screen.getByLabelText(/^Reminder \*/i);
    await user.type(reminderBody, "Follow up on ticket");

    // Save reminder in popup
    const saveReminderBtn = screen.getByRole("button", { name: /Save reminder/i });
    await user.click(saveReminderBtn);

    // Reminder card should now be in the workflow reminders list
    const reminderArticle = screen.getByRole("article");
    expect(reminderArticle).toBeDefined();
    expect(reminderArticle.textContent).toContain("Follow up on ticket");

    // Submit workflow state form
    const submitBtn = screen.getByRole("button", { name: /Create State/i });
    await user.click(submitBtn);

    await waitFor(() => {
      expect(createMock).toHaveBeenCalledTimes(1);
    });

    const callArg = createMock.mock.calls[0]?.[0];
    expect(callArg).toBeDefined();
    if (!callArg) throw new Error("Missing callArg");
    expect(callArg.name).toBe("Triage State");
    expect(callArg.reminders).toHaveLength(1);
    expect(callArg.reminders?.[0]?.type).toBe("one_shot");
    expect(callArg.reminders?.[0]?.delay_seconds).toBe(300);
    expect(callArg.reminders?.[0]?.body).toBe("Follow up on ticket");

    unmount();
  });
});
