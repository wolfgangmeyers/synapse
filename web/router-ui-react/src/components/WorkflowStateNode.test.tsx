import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { WorkflowStateNode, type WorkflowStateNodeData } from "./WorkflowStateNode";
import type { WorkflowState, Workflow } from "../domain/workflow-states";

// Mock React Flow handles because they require React Flow provider
vi.mock("@xyflow/react", () => ({
  Handle: () => <div data-testid="handle" />,
  Position: {
    Left: "left",
    Right: "right",
    Top: "top",
    Bottom: "bottom",
  },
}));

describe("WorkflowStateNode", () => {
  const baseWorkflow: Workflow = {
    id: "wf-1",
    name: "Dev Loop",
    description: "Standard workflow",
    entry_state_id: "state-1",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };

  const baseState: WorkflowState = {
    id: "state-1",
    workflow_id: "wf-1",
    name: "Specification",
    description: "Draft spec and plan",
    entry_text: "You are drafting specs.",
    created_at: "2026-10-01T00:00:00Z",
    updated_at: "2026-10-01T00:00:00Z",
  };

  it("renders entry badge when isEntry is true", () => {
    const nodeData: WorkflowStateNodeData = {
      state: baseState,
      workflow: baseWorkflow,
      isEntry: true,
      onOpenConfig: vi.fn(),
      onDeleteState: vi.fn(),
    };

    const { container } = render(
      <WorkflowStateNode
        id="node-1"
        data={nodeData as unknown as Record<string, unknown>}
        selected={false}
        type="workflowState"
        zIndex={0}
        isConnectable={true}
        positionAbsoluteX={0}
        positionAbsoluteY={0}
        dragging={false}
        draggable={false}
        selectable={true}
        deletable={true}
      />
    );

    const entryBadge = container.querySelector(".badge-entry");
    expect(entryBadge).not.toBeNull();
    expect(entryBadge?.textContent).toContain("ENTRY");
  });

  it("does not render goal badge as per-state goals are removed", () => {
    const nodeData: WorkflowStateNodeData = {
      state: baseState,
      workflow: baseWorkflow,
      isEntry: false,
      onOpenConfig: vi.fn(),
      onDeleteState: vi.fn(),
    };

    const { container } = render(
      <WorkflowStateNode
        id="node-1"
        data={nodeData as unknown as Record<string, unknown>}
        selected={false}
        type="workflowState"
        zIndex={0}
        isConnectable={true}
        positionAbsoluteX={0}
        positionAbsoluteY={0}
        dragging={false}
        draggable={false}
        selectable={true}
        deletable={true}
      />
    );

    expect(container.querySelector(".badge-goal")).toBeNull();
  });
});
