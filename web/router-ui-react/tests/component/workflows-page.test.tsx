import { describe, expect, it, vi, afterEach } from "vitest";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";
import { WorkflowsPage } from "../../src/components/WorkflowsPage";
import type { RouterHttpClient } from "../../src/transport/http-client";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("WorkflowsPage GitHub Sync", () => {
  const createMockClient = (overrides: Partial<RouterHttpClient> = {}) => ({
    listWorkflows: vi.fn().mockResolvedValue([
      {
        id: "wf-1",
        name: "Test Workflow",
        description: "Test description",
        entry_state_id: null,
        created_at: 1700000000000,
        updated_at: 1700000000000,
        state_count: 2,
      },
    ]),
    getGitSyncTarget: vi.fn().mockResolvedValue(null),
    setGitSyncTarget: vi.fn(),
    clearGitSyncTarget: vi.fn(),
    pullGitSync: vi.fn(),
    pushGitSync: vi.fn(),
    ...overrides,
  } as unknown as RouterHttpClient);

  it("loads and displays saved repository URL target", async () => {
    const mockClient = createMockClient({
      getGitSyncTarget: vi.fn().mockResolvedValue({ target: "workflows", repo_url: "git@github.com:owner/workflows-repo.git" }),
    });

    render(<WorkflowsPage client={mockClient} />);

    expect(screen.getByRole("region", { name: "GitHub Sync" })).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByDisplayValue("git@github.com:owner/workflows-repo.git")).toBeInTheDocument();
    });
    expect(screen.getByRole("button", { name: /Pull from GitHub/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Push to GitHub/i })).toBeInTheDocument();
  });

  it("saves repository URL on save button click", async () => {
    const user = userEvent.setup();
    const mockClient = createMockClient({
      getGitSyncTarget: vi.fn().mockResolvedValue(null),
      setGitSyncTarget: vi.fn().mockResolvedValue({ target: "workflows", repo_url: "git@github.com:new/repo.git" }),
    });

    render(<WorkflowsPage client={mockClient} />);

    const input = screen.getByPlaceholderText(/git@github.com:owner\/repo.git/);
    await user.type(input, "git@github.com:new/repo.git");

    const saveBtn = screen.getByRole("button", { name: "Save" });
    expect(saveBtn).not.toBeDisabled();
    await user.click(saveBtn);

    expect(mockClient.setGitSyncTarget).toHaveBeenCalledWith("workflows", "git@github.com:new/repo.git");
    expect(await screen.findByText("Repository URL saved.")).toBeInTheDocument();
  });

  it("prompts confirmation when Pull is clicked, and warns when file does not exist", async () => {
    const user = userEvent.setup();
    const mockClient = createMockClient({
      getGitSyncTarget: vi.fn().mockResolvedValue({ target: "workflows", repo_url: "git@github.com:owner/repo.git" }),
      pullGitSync: vi.fn().mockResolvedValue({ exists: false }),
    });

    render(<WorkflowsPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("git@github.com:owner/repo.git")).toBeInTheDocument();
    });

    const pullBtn = screen.getByRole("button", { name: /Pull from GitHub/i });
    await user.click(pullBtn);

    // Destructive confirmation prompt appears
    expect(screen.getByText(/This will replace all local workflows with the version from GitHub. Continue\?/i)).toBeInTheDocument();

    const confirmBtn = screen.getByRole("button", { name: "Confirm Pull" });
    await user.click(confirmBtn);

    expect(mockClient.pullGitSync).toHaveBeenCalledWith("workflows");
    expect(await screen.findByText("No workflows.json found in this repository. Nothing was changed.")).toBeInTheDocument();
  });

  it("reloads workflows and shows count on successful pull", async () => {
    const user = userEvent.setup();
    const mockClient = createMockClient({
      getGitSyncTarget: vi.fn().mockResolvedValue({ target: "workflows", repo_url: "git@github.com:owner/repo.git" }),
      pullGitSync: vi.fn().mockResolvedValue({ exists: true, count: 3 }),
    });

    render(<WorkflowsPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("git@github.com:owner/repo.git")).toBeInTheDocument();
    });

    const pullBtn = screen.getByRole("button", { name: /Pull from GitHub/i });
    await user.click(pullBtn);

    const confirmBtn = screen.getByRole("button", { name: "Confirm Pull" });
    await user.click(confirmBtn);

    expect(mockClient.pullGitSync).toHaveBeenCalledWith("workflows");
    expect(await screen.findByText("Successfully pulled 3 workflows from GitHub.")).toBeInTheDocument();
    expect(mockClient.listWorkflows).toHaveBeenCalledTimes(2); // Initial mount + after pull
  });

  it("pushes to github and shows success message", async () => {
    const user = userEvent.setup();
    const mockClient = createMockClient({
      getGitSyncTarget: vi.fn().mockResolvedValue({ target: "workflows", repo_url: "git@github.com:owner/repo.git" }),
      pushGitSync: vi.fn().mockResolvedValue({ success: true, count: 1 }),
    });

    render(<WorkflowsPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("git@github.com:owner/repo.git")).toBeInTheDocument();
    });

    const pushBtn = screen.getByRole("button", { name: /Push to GitHub/i });
    await user.click(pushBtn);

    expect(mockClient.pushGitSync).toHaveBeenCalledWith("workflows");
    expect(await screen.findByText("Successfully pushed 1 workflow to GitHub.")).toBeInTheDocument();
  });

  it("clears repository URL when clear button is clicked", async () => {
    const user = userEvent.setup();
    const mockClient = createMockClient({
      getGitSyncTarget: vi.fn().mockResolvedValue({ target: "workflows", repo_url: "git@github.com:owner/repo.git" }),
      clearGitSyncTarget: vi.fn().mockResolvedValue(undefined),
    });

    render(<WorkflowsPage client={mockClient} />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("git@github.com:owner/repo.git")).toBeInTheDocument();
    });

    const clearBtn = screen.getByRole("button", { name: "Clear repository URL" });
    expect(clearBtn).toBeInTheDocument();
    await user.click(clearBtn);

    expect(mockClient.clearGitSyncTarget).toHaveBeenCalledWith("workflows");
    expect(await screen.findByText("Repository URL cleared.")).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/git@github.com:owner\/repo.git/)).toHaveValue("");
  });
});
