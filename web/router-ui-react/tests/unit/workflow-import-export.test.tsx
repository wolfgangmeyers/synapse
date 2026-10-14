import "@testing-library/jest-dom/vitest";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, cleanup, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { WorkflowsPage } from "../../src/components/WorkflowsPage";
import { ImportWorkflowModal } from "../../src/components/ImportWorkflowModal";
import type { RouterHttpClient } from "../../src/transport/http-client";
import type { WorkflowExportBundle } from "../../src/domain/workflow-states";

// Mock React Flow for component testing because ResizeObserver and SVG path measurements require full browser environment
vi.mock("@xyflow/react", async () => {
  const actual = await vi.importActual<Record<string, unknown>>("@xyflow/react");
  return {
    ...actual,
    ReactFlow: ({ children }: any) => <div data-testid="mock-react-flow">{children}</div>,
    Controls: () => <div data-testid="mock-controls" />,
    Background: () => <div data-testid="mock-background" />,
  };
});

describe("Workflow Import & Export UI", () => {
  const mockWorkflows = [
    {
      id: "wf-1",
      name: "Feature Pipeline",
      description: "Automated delivery",
      entry_state_id: "st-1",
      state_count: 2,
      created_at: "2026-10-01T00:00:00Z",
      updated_at: "2026-10-01T00:00:00Z",
    },
  ];

  const mockBundle: WorkflowExportBundle = {
    schema_version: 1,
    exported_at: "2026-10-01T00:00:00Z",
    workflow: {
      name: "Imported Flow",
      description: "Imported from file",
      variables: [
        {
          name: "BRANCH",
          label: "Git Branch",
          type: "text",
          required: true,
          default_value: "main",
        },
      ],
    },
    entry_state_key: "k-1",
    states: [
      {
        key: "k-1",
        name: "Init",
        description: "Initialize workspace",
        entry_text: "Checking out ${BRANCH}",
        goal: "Prepare workspace",
        is_entry_state: true,
        allow_exit: true,
        allowed_tools: ["bash"],
        reminders: [],
        ui_pos_x: 100,
        ui_pos_y: 100,
        transitions: [],
      },
    ],
  };

  let mockClient: Partial<RouterHttpClient>;

  afterEach(cleanup);

  beforeEach(() => {
    mockClient = {
      listWorkflows: vi.fn().mockResolvedValue(mockWorkflows),
      getWorkflow: vi.fn().mockResolvedValue({
        workflow: mockWorkflows[0],
        states: [],
      }),
      exportWorkflow: vi.fn().mockResolvedValue(mockBundle),
      importWorkflow: vi.fn().mockResolvedValue({
        id: "wf-new",
        name: "Imported Flow",
        description: "Imported from file",
        entry_state_id: "st-new",
        state_count: 1,
        created_at: "2026-10-01T00:00:00Z",
        updated_at: "2026-10-01T00:00:00Z",
      }),
    };
  });

  it("exports workflow when clicking Export button on card", async () => {
    const user = userEvent.setup();
    // Mock document.createElement and click
    const clickSpy = vi.fn();
    const originalCreateElement = document.createElement.bind(document);
    vi.spyOn(document, "createElement").mockImplementation((tagName: string) => {
      const el = originalCreateElement(tagName);
      if (tagName === "a") {
        el.click = clickSpy;
      }
      return el;
    });

    render(<WorkflowsPage client={mockClient as RouterHttpClient} />);
    await waitFor(() => {
      expect(screen.getByText("Feature Pipeline")).toBeInTheDocument();
    });

    const exportBtn = screen.getByTitle("Export workflow");
    await user.click(exportBtn);

    expect(mockClient.exportWorkflow).toHaveBeenCalledWith("wf-1");
    expect(clickSpy).toHaveBeenCalled();
  });

  it("opens import modal from header and imports valid bundle", async () => {
    const user = userEvent.setup();
    render(<WorkflowsPage client={mockClient as RouterHttpClient} />);
    await waitFor(() => {
      expect(screen.getByText("Import Workflow")).toBeInTheDocument();
    });

    const importHeaderBtn = screen.getByRole("button", { name: /import workflow/i });
    await user.click(importHeaderBtn);

    expect(screen.getByText("Upload a workflow JSON file to import it into your workspace.")).toBeInTheDocument();

    // Trigger file selection in ImportWorkflowModal
    const file = new File([JSON.stringify(mockBundle)], "test-workflow.json", {
      type: "application/json",
    });

    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    expect(fileInput).not.toBeNull();
    await user.upload(fileInput, file);

    await waitFor(() => {
      expect(screen.getByText("Imported Flow")).toBeInTheDocument();
      expect(screen.getByText(/1 state/i)).toBeInTheDocument();
      expect(screen.getByText(/1 variable/i)).toBeInTheDocument();
    });

    const submitBtn = within(screen.getByRole("dialog")).getByRole("button", { name: "Import Workflow" });
    await user.click(submitBtn);

    await waitFor(() => {
      expect(mockClient.importWorkflow).toHaveBeenCalledWith(mockBundle);
    });

    // Verify dialog was closed
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    });
  });

  it("validates invalid bundle in ImportWorkflowModal", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const onImport = vi.fn();

    render(<ImportWorkflowModal open={true} onClose={onClose} onImport={onImport} />);

    const invalidJson = JSON.stringify({ invalid: true });
    const file = new File([invalidJson], "invalid.json", { type: "application/json" });

    const fileInput = document.querySelector('input[type="file"]') as HTMLInputElement;
    await user.upload(fileInput, file);

    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent("Missing schema_version");
    });

    expect(onImport).not.toHaveBeenCalled();
  });
});
