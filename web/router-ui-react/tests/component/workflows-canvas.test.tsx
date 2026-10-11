import { describe, it, expect, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { WorkflowsPage } from "../../src/components/WorkflowsPage";
import { WorkflowCanvas } from "../../src/components/WorkflowCanvas";
import type { RouterHttpClient } from "../../src/transport/http-client";

// Mock React Flow for component testing because ResizeObserver and SVG path measurements require full browser environment
vi.mock("@xyflow/react", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("@xyflow/react");
  return {
    ...actual,
    ReactFlow: ({ nodes, edges, children }: any) => (
      <div data-testid="mock-react-flow">
        <div data-testid="mock-nodes">
          {nodes.map((n: any) => (
            <div key={n.id} data-testid={`mock-node-${n.id}`}>
              {n.data.state.name}
              {n.data.isEntry && <span data-testid={`entry-badge-${n.id}`}>ENTRY</span>}
              <button
                type="button"
                data-testid={`config-btn-${n.id}`}
                onClick={() => n.data.onOpenConfig(n.data.state)}
              >
                Configure
              </button>
              <button
                type="button"
                data-testid={`delete-btn-${n.id}`}
                onClick={() => n.data.onDeleteState(n.data.state)}
              >
                Delete
              </button>
            </div>
          ))}
        </div>
        <div data-testid="mock-edges">
          {edges.map((e: any) => (
            <div key={e.id} data-testid={`mock-edge-${e.id}`}>
              {`${e.source} -> ${e.target} : ${e.label}`}
            </div>
          ))}
        </div>
        {children}
      </div>
    ),
    Controls: () => <div data-testid="mock-controls" />,
    Background: () => <div data-testid="mock-background" />,
  };
});

describe("WorkflowsPage & WorkflowCanvas Integration", () => {
  const mockWorkflows = [
    {
      id: "wf-1",
      name: "Feature Dev",
      description: "Standard dev loop",
      entry_state_id: "st-1",
      state_count: 2,
      created_at: "2026-10-01T00:00:00Z",
      updated_at: "2026-10-01T00:00:00Z",
    },
  ];

  const mockStates = [
    {
      id: "st-1",
      workflow_id: "wf-1",
      name: "Planning",
      description: "Write specification",
      entry_text: "Plan carefully.",
      allow_exit: false,
      transitions: [{ target_state_id: "st-2", description: "Plan approved" }],
      ui_pos_x: 100,
      ui_pos_y: 100,
      created_at: "2026-10-01T00:00:00Z",
      updated_at: "2026-10-01T00:00:00Z",
    },
    {
      id: "st-2",
      workflow_id: "wf-1",
      name: "Implementation",
      description: "Build code",
      entry_text: "Implement feature.",
      allow_exit: true,
      transitions: [],
      ui_pos_x: 400,
      ui_pos_y: 100,
      created_at: "2026-10-01T00:00:00Z",
      updated_at: "2026-10-01T00:00:00Z",
    },
  ];

  it("renders workflow list and navigates into canvas when selected", async () => {
    const user = userEvent.setup();
    const mockClient = {
      listWorkflows: vi.fn().mockResolvedValue(mockWorkflows),
      getWorkflow: vi.fn().mockResolvedValue({
        workflow: mockWorkflows[0],
        states: mockStates,
      }),
      updateWorkflowLayout: vi.fn().mockResolvedValue(undefined),
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowsPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByText("Feature Dev")).toBeDefined();
      expect(screen.getByText("Standard dev loop")).toBeDefined();
    });

    // Click workflow card to open canvas
    await user.click(screen.getByText("Feature Dev"));

    await waitFor(() => {
      expect(screen.getByText("Planning")).toBeDefined();
      expect(screen.getByText("Implementation")).toBeDefined();
      expect(screen.getByTestId("entry-badge-st-1")).toBeDefined();
    });

    // Verify back button navigates back to list
    const backBtn = screen.getByRole("button", { name: /Workflows/i });
    await user.click(backBtn);

    await waitFor(() => {
      expect(screen.getByText("New Workflow")).toBeDefined();
    });
    unmount();
  });

  it("displays health check warnings on canvas when there is a dead-end or no entry", async () => {
    const brokenWorkflow = {
      id: "wf-broken",
      name: "Broken Workflow",
      description: "Has dead end",
      entry_state_id: null,
      created_at: "2026-10-01T00:00:00Z",
      updated_at: "2026-10-01T00:00:00Z",
    };

    const deadEndStates = [
      {
        id: "st-trap",
        workflow_id: "wf-broken",
        name: "Trap State",
        description: "Cannot exit and no transitions",
        entry_text: "You are stuck.",
        allow_exit: false,
        transitions: [],
        ui_pos_x: 100,
        ui_pos_y: 100,
        created_at: "2026-10-01T00:00:00Z",
        updated_at: "2026-10-01T00:00:00Z",
      },
    ];

    const mockClient = {
      getWorkflow: vi.fn().mockResolvedValue({
        workflow: brokenWorkflow,
        states: deadEndStates,
      }),
      updateWorkflowLayout: vi.fn().mockResolvedValue(undefined),
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowCanvas workflowId="wf-broken" client={mockClient} onBack={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText(/Workflow Health Checks/i)).toBeDefined();
      expect(screen.getByText(/No entry state is designated/i)).toBeDefined();
      expect(screen.getByText(/Dead-end state\(s\) without exit permission: Trap State/i)).toBeDefined();
    });
    unmount();
  });

  it("handles Tidy Layout and triggers updateWorkflowLayout", async () => {
    const user = userEvent.setup();
    const updateWorkflowLayoutMock = vi.fn().mockResolvedValue(undefined);
    const mockClient = {
      getWorkflow: vi.fn().mockResolvedValue({
        workflow: mockWorkflows[0],
        states: mockStates,
      }),
      updateWorkflowLayout: updateWorkflowLayoutMock,
    } as unknown as RouterHttpClient;

    const { unmount } = render(<WorkflowCanvas workflowId="wf-1" client={mockClient} onBack={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText("Tidy Layout")).toBeDefined();
    });

    await user.click(screen.getByText("Tidy Layout"));

    await waitFor(() => {
      expect(updateWorkflowLayoutMock).toHaveBeenCalled();
    });
    unmount();
  });
});
