import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { WorkflowVariablesModal } from "../../src/components/WorkflowVariablesModal";
import type { Workflow } from "../../src/domain/workflow-states";

describe("WorkflowVariablesModal", () => {
  beforeEach(() => {
    cleanup();
  });

  const baseWorkflow: Workflow = {
    id: "wf-1",
    name: "Deploy Pipeline",
    description: "Automated deployment workflow",
    entry_state_id: "state-1",
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    variables: [
      {
        name: "repo_url",
        label: "Repository URL",
        description: "Git repo url to clone",
        type: "text",
        required: true,
        default_value: "https://github.com/example/repo",
      },
    ],
  };

  it("does not render when isOpen is false", () => {
    const { container } = render(
      <WorkflowVariablesModal
        isOpen={false}
        onClose={vi.fn()}
        workflow={baseWorkflow}
        onSave={vi.fn()}
      />
    );
    expect(container.firstChild).toBeNull();
  });

  it("renders existing variables with badges and values", () => {
    render(
      <WorkflowVariablesModal
        isOpen={true}
        onClose={vi.fn()}
        workflow={baseWorkflow}
        onSave={vi.fn()}
      />
    );

    expect(screen.getByText("Workflow Variables: Deploy Pipeline")).toBeDefined();
    expect(screen.getByText("${repo_url}")).toBeDefined();
    expect(screen.getByText("Repository URL")).toBeDefined();
    expect(screen.getByText("Git repo url to clone")).toBeDefined();
    expect(screen.getByText("text")).toBeDefined();
    expect(screen.getAllByText("Required").length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText("https://github.com/example/repo")).toBeDefined();
  });

  it("validates variable name syntax", async () => {
    render(
      <WorkflowVariablesModal
        isOpen={true}
        onClose={vi.fn()}
        workflow={{ ...baseWorkflow, variables: [] }}
        onSave={vi.fn()}
      />
    );

    const nameInput = screen.getByLabelText("Variable Name *");
    const labelInput = screen.getByLabelText("Display Label *");
    const addBtn = screen.getByRole("button", { name: "Add Variable" });

    // Invalid character
    await userEvent.type(nameInput, "bad name with spaces!");
    await userEvent.type(labelInput, "Bad Label");
    fireEvent.click(addBtn);

    expect(screen.getByText("Variable name may only contain letters, numbers, underscores, and hyphens.")).toBeDefined();
  });

  it("prevents duplicate variable names", async () => {
    render(
      <WorkflowVariablesModal
        isOpen={true}
        onClose={vi.fn()}
        workflow={baseWorkflow}
        onSave={vi.fn()}
      />
    );

    const nameInput = screen.getByLabelText("Variable Name *");
    const labelInput = screen.getByLabelText("Display Label *");
    const addBtn = screen.getByRole("button", { name: "Add Variable" });

    await userEvent.type(nameInput, "repo_url");
    await userEvent.type(labelInput, "Duplicate");
    fireEvent.click(addBtn);

    expect(screen.getByText('A variable named "repo_url" already exists.')).toBeDefined();
  });

  it("adds a new variable and calls onSave on save", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    const onClose = vi.fn();

    render(
      <WorkflowVariablesModal
        isOpen={true}
        onClose={onClose}
        workflow={baseWorkflow}
        onSave={onSave}
      />
    );

    const nameInput = screen.getByLabelText("Variable Name *");
    const labelInput = screen.getByLabelText("Display Label *");
    const typeSelect = screen.getByLabelText("Type *");
    const requiredCheckbox = screen.getByLabelText("Required");
    const defaultInput = screen.getByLabelText("Default Value (optional)");
    const addBtn = screen.getByRole("button", { name: "Add Variable" });

    await userEvent.type(nameInput, "commit_msg");
    await userEvent.type(labelInput, "Commit Message");
    await userEvent.selectOptions(typeSelect, "textarea");
    fireEvent.click(requiredCheckbox);
    const textareaDefaultInput = screen.getByLabelText("Default Value (optional)");
    await userEvent.type(textareaDefaultInput, "Initial commit");
    fireEvent.click(addBtn);

    expect(screen.getByText("${commit_msg}")).toBeDefined();
    expect(screen.getByText("Commit Message")).toBeDefined();

    const saveBtn = screen.getByRole("button", { name: "Save Variables" });
    fireEvent.click(saveBtn);

    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith([
        baseWorkflow.variables![0],
        {
          name: "commit_msg",
          label: "Commit Message",
          type: "textarea",
          required: true,
          default_value: "Initial commit",
        },
      ]);
      expect(onClose).toHaveBeenCalled();
    });
  });

  it("can delete an existing variable", async () => {
    const onSave = vi.fn().mockResolvedValue(undefined);
    render(
      <WorkflowVariablesModal
        isOpen={true}
        onClose={vi.fn()}
        workflow={baseWorkflow}
        onSave={onSave}
      />
    );

    const deleteBtn = screen.getByTitle("Delete variable");
    fireEvent.click(deleteBtn);

    expect(screen.queryByText("${repo_url}")).toBeNull();

    const saveBtn = screen.getByRole("button", { name: "Save Variables" });
    fireEvent.click(saveBtn);

    await waitFor(() => {
      expect(onSave).toHaveBeenCalledWith([]);
    });
  });
});
