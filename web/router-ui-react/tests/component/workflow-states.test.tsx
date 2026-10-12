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
  it("renders goal badge when state has a goal configured", async () => {
    const statesWithGoal = [
      {
        id: "state-with-goal",
        name: "Goal-Oriented State",
        description: "State with persistent goal",
        entry_text: "Entry instructions",
        goal: "Finish all test coverage",
        created_at: "2025-01-01T00:00:00Z",
        updated_at: "2025-01-01T00:00:00Z",
      },
      {
        id: "state-without-goal",
        name: "No-Goal State",
        description: "State without goal",
        entry_text: "Entry instructions",
        goal: "",
        created_at: "2025-01-01T00:00:00Z",
        updated_at: "2025-01-01T00:00:00Z",
      },
    ];

    const mockClient = {
      workflowStates: vi.fn().mockResolvedValue({ workflow_states: statesWithGoal }),
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowStatesPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByText("Goal-Oriented State")).toBeDefined();
    });

    const goalBadge = screen.getByTestId("state-goal-badge-state-with-goal");
    expect(goalBadge).toBeDefined();
    expect(goalBadge.textContent).toContain("Goal configured");
    expect(goalBadge.getAttribute("title")).toBe("Finish all test coverage");
    expect(screen.queryByTestId("state-goal-badge-state-without-goal")).toBeNull();

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

  it("renders tool restriction badges properly", async () => {
    const statesWithToolConfigs = [
      {
        id: "state-unrestricted",
        name: "Unrestricted State",
        description: "All tools allowed",
        entry_text: "Unrestricted",
        allowed_tools: [],
        created_at: "2025-01-01T00:00:00Z",
        updated_at: "2025-01-01T00:00:00Z",
      },
      {
        id: "state-restricted",
        name: "Restricted State",
        description: "Only read and bash",
        entry_text: "Restricted",
        allowed_tools: ["read", "bash"],
        created_at: "2025-01-01T00:00:00Z",
        updated_at: "2025-01-01T00:00:00Z",
      },
    ];

    const mockClient = {
      workflowStates: vi.fn().mockResolvedValue({ workflow_states: statesWithToolConfigs }),
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowStatesPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByTestId("state-tools-badge-state-unrestricted")).toBeDefined();
      expect(screen.getByTestId("state-tools-badge-state-restricted")).toBeDefined();
    });

    expect(screen.getByTestId("state-tools-badge-state-unrestricted").textContent).toContain("All tools allowed");
    expect(screen.getByTestId("state-tools-badge-state-restricted").textContent).toContain("Tools restricted (2 allowed)");

    unmount();
  });

  it("allows configuring tool restrictions when creating a state", async () => {
    const user = userEvent.setup();
    const createMock = vi.fn().mockResolvedValue({
      workflow_state: {
        id: "new-state-id",
        name: "Restricted Tools State",
        description: "Custom tools",
        entry_text: "Entry",
        allowed_tools: ["read", "write"],
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

    const newStateBtn = screen.getByRole("button", { name: /New State/i });
    await user.click(newStateBtn);

    const nameInput = screen.getByLabelText(/State Name \*/i);
    await user.type(nameInput, "Restricted Tools State");

    // Tool restrictions: toggle off "Allow all tools"
    const allowAllToggle = screen.getByTestId("allow-all-tools-toggle");
    expect(allowAllToggle).toBeDefined();
    expect(allowAllToggle.getAttribute("aria-pressed")).toBe("true");
    await user.click(allowAllToggle);
    expect(allowAllToggle.getAttribute("aria-pressed")).toBe("false");

    // Click Deselect All button
    const deselectBtn = screen.getByRole("button", { name: /Deselect All/i });
    await user.click(deselectBtn);

    // Select read and write tool tiles
    const readTile = screen.getByTestId("tool-toggle-read");
    const writeTile = screen.getByTestId("tool-toggle-write");
    await user.click(readTile);
    await user.click(writeTile);

    // Submit form
    const submitBtn = screen.getByRole("button", { name: /Create State/i });
    await user.click(submitBtn);

    await waitFor(() => {
      expect(createMock).toHaveBeenCalledTimes(1);
    });

    const callArg = createMock.mock.calls[0]?.[0];
    expect(callArg.allowed_tools).toEqual(["read", "write"]);

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

  it("renders transitions and exit badges on state card", async () => {
    const statesWithTransitions = [
      {
        id: "state-plan",
        name: "Planning",
        description: "Decompose requirements",
        entry_text: "Plan carefully",
        allow_exit: false,
        transitions: [
          { target_state_id: "state-impl", description: "When plan validated" },
        ],
        created_at: 1700000000000,
        updated_at: 1700000000000,
      },
      {
        id: "state-impl",
        name: "Implementation",
        description: "Write code",
        entry_text: "Implement features",
        allow_exit: true,
        transitions: [],
        created_at: 1700000000000,
        updated_at: 1700000000000,
      },
    ];

    const mockClient = {
      workflowStates: vi.fn().mockResolvedValue({ workflow_states: statesWithTransitions }),
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowStatesPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByText("Planning")).toBeDefined();
    });

    // Check exit locked on Planning
    const planExitBadge = screen.getByTestId("state-exit-badge-state-plan");
    expect(planExitBadge.textContent).toContain("Exit locked");

    // Check transitions badge on Planning
    const planTransitionsBadge = screen.getByTestId("state-transitions-badge-state-plan");
    expect(planTransitionsBadge.textContent).toContain("1 transition");

    // Check transitions summary list on Planning
    const transitionsSummary = screen.getByTestId("state-transitions-list-state-plan");
    expect(transitionsSummary.textContent).toContain("Implementation");
    expect(transitionsSummary.textContent).toContain("When plan validated");

    // Check exit allowed on Implementation
    const implExitBadge = screen.getByTestId("state-exit-badge-state-impl");
    expect(implExitBadge.textContent).toContain("Exit allowed");

    // Check 0 transitions badge on Implementation
    const implTransitionsBadge = screen.getByTestId("state-transitions-badge-state-impl");
    expect(implTransitionsBadge.textContent).toContain("0 transitions");

    unmount();
  });

  it("can configure exit toggle and transitions when creating a workflow state", async () => {
    const user = userEvent.setup();
    const existingStates = [
      {
        id: "state-impl",
        name: "Implementation",
        description: "Coding state",
        entry_text: "",
        created_at: 1700000000000,
        updated_at: 1700000000000,
      },
    ];

    const createMock = vi.fn().mockResolvedValue({
      workflow_state: {
        id: "state-new",
        name: "Design Phase",
        description: "Design state",
        entry_text: "Design docs",
        allow_exit: false,
        transitions: [{ target_state_id: "state-impl", description: "Design approved" }],
        created_at: 1700000000000,
        updated_at: 1700000000000,
      },
    });

    const mockClient = {
      workflowStates: vi.fn().mockResolvedValue({ workflow_states: existingStates }),
      createWorkflowState: createMock,
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowStatesPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByText("Implementation")).toBeDefined();
    });

    // Open create modal
    const newStateBtn = screen.getByRole("button", { name: /New State/i });
    await user.click(newStateBtn);

    // Fill name
    const nameInput = screen.getByLabelText(/State Name \*/i);
    await user.type(nameInput, "Design Phase");

    // Toggle Allow Exit to OFF
    const exitToggle = screen.getByTestId("allow-exit-toggle");
    expect(exitToggle.getAttribute("aria-pressed")).toBe("true");
    await user.click(exitToggle);
    expect(exitToggle.getAttribute("aria-pressed")).toBe("false");

    // Add a transition
    const addTransitionBtn = screen.getByTestId("add-transition-btn");
    await user.click(addTransitionBtn);

    // Select target state and fill description
    const targetSelect = screen.getByTestId("transition-target-0");
    await user.selectOptions(targetSelect, "state-impl");

    const descInput = screen.getByTestId("transition-desc-0");
    await user.type(descInput, "Design approved");

    // Submit form
    const submitBtn = screen.getByRole("button", { name: /Create State/i });
    await user.click(submitBtn);

    await waitFor(() => {
      expect(createMock).toHaveBeenCalledTimes(1);
    });

    const callArg = createMock.mock.calls[0]?.[0];
    expect(callArg.name).toBe("Design Phase");
    expect(callArg.allow_exit).toBe(false);
    expect(callArg.transitions).toEqual([
      { target_state_id: "state-impl", description: "Design approved" },
    ]);

    unmount();
  });
});
