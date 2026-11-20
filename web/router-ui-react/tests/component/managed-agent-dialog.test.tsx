import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { clearWorktreeCheckCache, ManagedAgentDialog } from "../../src/components/ManagedAgentDialog";
import { TerminateConfirmationDialog } from "../../src/components/TerminateConfirmationDialog";
import type { ModelsResponse } from "../../src/transport/http-client";

beforeEach(() => {
  vi.spyOn(HTMLElement.prototype, "getBoundingClientRect").mockReturnValue({ x: 40, y: 40, left: 40, top: 40, right: 840, bottom: 640, width: 800, height: 600, toJSON: () => ({}) });
  vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(800);
  vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(600);
});

describe("ManagedAgentDialog", () => {
  beforeEach(() => {
    clearWorktreeCheckCache();
  });
  afterEach(cleanup);

  it("uses the shared Goal dialog presentation", () => {
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={async () => undefined} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} />);
    expect(screen.getByRole("dialog", { name: "Launch managed agent" })).toHaveClass("message-dialog", "managed-agent-dialog");
  });
  it("uses the folders-only picker and submits a direct launch payload", async () => {
    const user = userEvent.setup();
    const onLaunch = vi.fn(async () => undefined);
    const onListDirectories = vi.fn(async (path: string) => path === "/allowed" ? ({ path, parent: "", directories: [{ name: "project", path: "/allowed/project" }] }) : ({ path, parent: "/allowed", directories: [] }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={onLaunch} onAction={async () => undefined} onListDirectories={onListDirectories} />);
    await user.type(screen.getByLabelText("Name"), "  My Agent  ");
    await user.type(screen.getByLabelText("Working folder"), "/allowed");
    await user.click(screen.getByRole("button", { name: "Browse folders" }));
    const folderBrowser = await screen.findByRole("dialog", { name: "Choose working folder" });
    await user.click(within(folderBrowser).getByRole("button", { name: "project" }));
    await user.click(within(folderBrowser).getByRole("button", { name: "Select folder" }));
    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({ name: "My-Agent", cwd: "/allowed/project", runtime: "embedded" });
  });

  it.each([
    ["MY AGENT", "MY-AGENT"],
    ["my-AGENT", "my-AGENT"],
    ["My\t  Agent", "My-Agent"],
  ])("normalizes %j only when submitting", async (typedName, submittedName) => {
    const user = userEvent.setup();
    const onLaunch = vi.fn(async () => undefined);
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={onLaunch} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} />);
    const nameInput = screen.getByLabelText("Name");
    fireEvent.change(nameInput, { target: { value: typedName } });
    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed" } });
    expect(nameInput).toHaveValue(typedName);
    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({ name: submittedName, cwd: "/allowed", runtime: "embedded" });
  });

  it.each(["My/Agent", "-My Agent", "My Agent-"])("preserves invalid candidate %j for server validation", async (typedName) => {
    const user = userEvent.setup();
    const onLaunch = vi.fn(async () => undefined);
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={onLaunch} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} />);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: typedName } });
    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed" } });
    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({ name: typedName.replaceAll(" ", "-"), cwd: "/allowed", runtime: "embedded" });
  });

  it("uses the searchable model picker and only submits available models", async () => {
    const user = userEvent.setup();
    const onLaunch = vi.fn(async () => undefined);
    const onModels = vi.fn(async (): Promise<ModelsResponse> => ({ auth_revision: 1, default_model: null, models: [
      { provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true },
      { provider: "google", id: "gemini-2.5-pro", name: "Gemini 2.5 Pro", available: false },
    ] }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={onLaunch} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onModels={onModels} />);
    const modelButton = await screen.findByRole("button", { name: "Use router default" });
    await user.click(modelButton);
    await user.type(screen.getByRole("searchbox"), "terra");
    await user.click(screen.getByRole("option", { name: /GPT-5\.6 Terra/ }));
    expect(screen.queryByRole("option", { name: /Gemini/ })).not.toBeInTheDocument();
    await user.type(screen.getByLabelText("Name"), "managed-alpha");
    await user.type(screen.getByLabelText("Working folder"), "/allowed/project");
    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({ name: "managed-alpha", cwd: "/allowed/project", runtime: "embedded", model: { provider: "openai-codex", id: "gpt-5.6-terra" } });
  });

  it("explains when no authenticated provider has model choices", async () => {
    const user = userEvent.setup();
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={async () => undefined} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onModels={async (): Promise<ModelsResponse> => ({ auth_revision: 1, default_model: null, models: [] })} />);
    await user.click(await screen.findByRole("button", { name: "Use router default" }));
    expect(screen.getByText("No connected models. Connect a provider in Provider settings.")).toBeInTheDocument();
  });

  it("explains an active-name collision without clearing the launch form", async () => {
    const user = userEvent.setup();
    render(<ManagedAgentDialog open error="That name is in use by an active or launching agent. Choose another name or wait for it to stop." onClose={() => undefined} onLaunch={async () => undefined} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} />);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "managed-alpha" } });
    expect(screen.getByRole("alert")).toHaveTextContent("That name is in use by an active or launching agent. Choose another name or wait for it to stop.");
    expect(screen.getByLabelText("Name")).toHaveValue("managed-alpha");
  });

  it.each([
    ["google", "gemini-2.5-flash", "Gemini 2.5 Flash"],
    ["google-vertex", "gemini-2.5-flash", "Gemini 2.5 Flash \\(Vertex\\)"],
    ["google-vertex", "gemini-3.8-flash", "Gemini 3.8 Flash \\(Antigravity\\)"],
  ])("launches %s model with rpc runtime instead of embedded", async (provider, id, namePattern) => {
    const user = userEvent.setup();
    const onLaunch = vi.fn(async () => undefined);
    const onModels = vi.fn(async (): Promise<ModelsResponse> => ({
      auth_revision: 1,
      default_model: null,
      models: [
        { provider: "openai-codex", id: "gpt-5.6-terra", name: "GPT-5.6 Terra", available: true },
        { provider: provider as any, id, name: id, available: true },
      ],
    }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={onLaunch} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onModels={onModels} />);
    const modelButton = await screen.findByRole("button", { name: "Use router default" });
    await user.click(modelButton);
    await user.click(screen.getByRole("option", { name: new RegExp(provider) }));
    await user.type(screen.getByLabelText("Name"), "agent-google");
    await user.type(screen.getByLabelText("Working folder"), "/allowed/project");
    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({
      name: "agent-google",
      cwd: "/allowed/project",
      runtime: "embedded",
      model: { provider, id },
    });
  });

  it("launches with rpc runtime when router default model is a Google provider", async () => {
    const user = userEvent.setup();
    const onLaunch = vi.fn(async () => undefined);
    const onModels = vi.fn(async (): Promise<ModelsResponse> => ({
      auth_revision: 1,
      default_model: { provider: "google-vertex", id: "gemini-2.5-flash", name: "Gemini 2.5 Flash (Vertex)" },
      models: [
        { provider: "google-vertex", id: "gemini-2.5-flash", name: "Gemini 2.5 Flash (Vertex)", available: true },
      ],
    }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={onLaunch} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onModels={onModels} />);
    await screen.findByRole("button", { name: "Use router default" });
    await user.type(screen.getByLabelText("Name"), "default-google-agent");
    await user.type(screen.getByLabelText("Working folder"), "/allowed/project");
    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({
      name: "default-google-agent",
      cwd: "/allowed/project",
      runtime: "embedded",
    });
  });

  it("disables submit button and prevents premature launch while catalog is still loading when default model is unresolved", async () => {
    const user = userEvent.setup();
    const onLaunch = vi.fn(async () => undefined);
    let resolveModels: (val: ModelsResponse) => void = () => {};
    const onModels = vi.fn(() => new Promise<ModelsResponse>((resolve) => { resolveModels = resolve; }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={onLaunch} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onModels={onModels} />);
    await user.type(screen.getByLabelText("Name"), "pending-agent");
    await user.type(screen.getByLabelText("Working folder"), "/allowed/project");
    const launchBtn = screen.getByRole("button", { name: "Launch" });
    expect(launchBtn).toBeDisabled();
    await user.click(launchBtn);
    expect(onLaunch).not.toHaveBeenCalled();

    resolveModels({
      auth_revision: 1,
      default_model: { provider: "google-vertex", id: "gemini-3.8-flash" },
      models: [{ provider: "google-vertex", id: "gemini-3.8-flash", name: "Gemini", available: true }],
    });
    await waitFor(() => expect(launchBtn).not.toBeDisabled());
    await user.click(launchBtn);
    expect(onLaunch).toHaveBeenCalledWith({
      name: "pending-agent",
      cwd: "/allowed/project",
      runtime: "embedded",
    });
  });

  it("submits runtime as embedded directly", async () => {
    const user = userEvent.setup();
    const onLaunch = vi.fn(async () => undefined);
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={onLaunch} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} />);
    await user.type(screen.getByLabelText("Name"), "agent-embedded");
    await user.type(screen.getByLabelText("Working folder"), "/allowed");
    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({ name: "agent-embedded", cwd: "/allowed", runtime: "embedded" });
  });

  it("does not show worktree toggle when worktreeCheck returns is_main_worktree: false", async () => {
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: false, has_uncommitted_changes: false }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={async () => undefined} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onWorktreeCheck={onWorktreeCheck} />);
    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed/repo/sub" } });
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo/sub", { includeStatus: false }));
    expect(screen.queryByRole("button", { name: /Create a worktree/i })).not.toBeInTheDocument();
  });

  it("shows worktree toggle when worktreeCheck returns is_main_worktree: true", async () => {
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={async () => undefined} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onWorktreeCheck={onWorktreeCheck} />);
    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed/repo" } });
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo", { includeStatus: false }));
    expect(await screen.findByRole("button", { name: /Create a worktree/i })).toBeInTheDocument();
  });

  it("toggling worktree on reveals branch input defaulted to slugified agent name", async () => {
    const user = userEvent.setup();
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={async () => undefined} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onWorktreeCheck={onWorktreeCheck} />);
    await user.type(screen.getByLabelText("Name"), "Feature Agent Name");
    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed/repo" } });
    const toggle = await screen.findByRole("button", { name: /Create a worktree/i });
    expect(screen.queryByLabelText("Branch name")).not.toBeInTheDocument();

    await user.click(toggle);
    const branchInput = await screen.findByLabelText("Branch name");
    expect(branchInput).toHaveValue("feature-agent-name");
  });

  it("editing branch name and submitting includes worktree: true and worktree_branch in launch payload", async () => {
    const user = userEvent.setup();
    const onLaunch = vi.fn(async () => undefined);
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={onLaunch} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onWorktreeCheck={onWorktreeCheck} />);
    await user.type(screen.getByLabelText("Name"), "my-worker");
    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed/repo" } });
    const toggle = await screen.findByRole("button", { name: /Create a worktree/i });
    await user.click(toggle);

    const branchInput = await screen.findByLabelText("Branch name");
    await user.clear(branchInput);
    await user.type(branchInput, "custom-branch-name");

    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({
      name: "my-worker",
      cwd: "/allowed/repo",
      runtime: "embedded",
      worktree: true,
      worktree_branch: "custom-branch-name",
    });
  });

  it("shows checking git indicator while worktreeCheck is running and restores hint after", async () => {
    let resolveCheck: (val: { is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }) => void = () => {};
    const onWorktreeCheck = vi.fn(() => new Promise<{ is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }>((resolve) => {
      resolveCheck = resolve;
    }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={async () => undefined} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onWorktreeCheck={onWorktreeCheck} />);

    expect(screen.getByText("Drop a folder from Finder.")).toBeInTheDocument();
    expect(screen.queryByText(/Checking git/i)).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed/repo" } });

    // While debounce / check is active, "Checking git…" indicator replaces "Drop a folder from Finder."
    expect(await screen.findByText(/Checking git…/i)).toBeInTheDocument();
    expect(screen.queryByText("Drop a folder from Finder.")).not.toBeInTheDocument();

    // Wait for the debounced call to happen
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo", { includeStatus: false }));

    // Settle the check
    resolveCheck({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false });

    // After completion, indicator disappears, normal hint returns, and worktree toggle is rendered
    expect(await screen.findByText("Drop a folder from Finder.")).toBeInTheDocument();
    expect(screen.queryByText(/Checking git…/i)).not.toBeInTheDocument();
    expect(await screen.findByRole("button", { name: /Create a worktree/i })).toBeInTheDocument();
  });

  it("requires a deliberate termination confirmation", async () => {
    const user = userEvent.setup();
    const onAction = vi.fn(async () => undefined);
    const agent = { agent_id: "managed-alpha", source: "router-managed" as const, cwd: "/allowed/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" }, desired_state: "running", lifecycle_state: "running", activity_state: "idle", accepting_prompts: true, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 }, created_at: "2030-01-01T00:00:00.000Z", started_at: null, updated_at: "2030-01-01T00:00:00.000Z", last_error: null };
    render(<ManagedAgentDialog open agent={agent} onClose={() => undefined} onLaunch={async () => undefined} onAction={onAction} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} />);
    await user.click(screen.getByRole("button", { name: "Terminate" }));
    expect(onAction).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Terminate agent" }));
    expect(onAction).toHaveBeenCalledWith("managed-alpha", "terminate");
  });

  it("passes recent folders through to the working folder picker", async () => {
    const user = userEvent.setup();
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={async () => undefined} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} recentFolders={["/recent/one", "/recent/two"]} />);
    const toggleButton = screen.getByRole("button", { name: "Recent folders" });
    expect(toggleButton).toBeInTheDocument();

    await user.click(toggleButton);
    const dialog = screen.getByRole("dialog", { name: "Choose working folder" });
    expect(dialog).toBeInTheDocument();
    expect(within(dialog).getByRole("option", { name: "Select folder /recent/one" })).toBeInTheDocument();
    expect(within(dialog).getByRole("option", { name: "Select folder /recent/two" })).toBeInTheDocument();
  });

  it("displays create worktree toggle immediately when selecting main worktree from recent folders (WTL-001)", async () => {
    const user = userEvent.setup();
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false }));
    render(
      <ManagedAgentDialog
        open
        onClose={() => undefined}
        onLaunch={async () => undefined}
        onAction={async () => undefined}
        onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })}
        recentFolders={["/allowed/repo"]}
        onWorktreeCheck={onWorktreeCheck}
      />
    );

    const toggleButton = screen.getByRole("button", { name: "Recent folders" });
    await user.click(toggleButton);

    const option = screen.getByRole("option", { name: "Select folder /allowed/repo" });
    await user.click(option);

    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo", { includeStatus: false }));
    expect(await screen.findByRole("button", { name: /Create a worktree/i })).toBeInTheDocument();
  });

  it("displays create worktree toggle immediately when selecting main worktree from folder browser (WTL-001)", async () => {
    const user = userEvent.setup();
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false }));
    const onListDirectories = vi.fn(async (path: string) =>
      path === "/allowed/repo"
        ? { path: "/allowed/repo", parent: "/allowed", directories: [] }
        : { path: "/allowed", parent: "", directories: [{ name: "repo", path: "/allowed/repo" }] },
    );
    render(
      <ManagedAgentDialog
        open
        onClose={() => undefined}
        onLaunch={async () => undefined}
        onAction={async () => undefined}
        onListDirectories={onListDirectories}
        onWorktreeCheck={onWorktreeCheck}
      />
    );

    await user.click(screen.getByRole("button", { name: "Browse folders" }));
    const folderBrowser = await screen.findByRole("dialog", { name: "Choose working folder" });
    const repoBtn = await within(folderBrowser).findByRole("button", { name: "repo" });
    await user.click(repoBtn);
    await user.click(within(folderBrowser).getByRole("button", { name: "Select folder" }));

    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo", { includeStatus: false }));
    expect(await screen.findByRole("button", { name: /Create a worktree/i })).toBeInTheDocument();
  });

  it("preserves in-flight worktree check during parent component re-renders (WTL-002)", async () => {
    let resolveCheck: (val: { is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }) => void = () => {};
    const onWorktreeCheck = vi.fn(() => new Promise<{ is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }>((resolve) => {
      resolveCheck = resolve;
    }));

    const { rerender } = render(
      <ManagedAgentDialog
        open
        pending={false}
        onClose={() => undefined}
        onLaunch={async () => undefined}
        onAction={async () => undefined}
        onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })}
        onWorktreeCheck={onWorktreeCheck}
      />
    );

    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed/repo" } });
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo", { includeStatus: false }));

    const onWorktreeCheckNew = vi.fn();
    rerender(
      <ManagedAgentDialog
        open
        pending={true}
        onClose={() => undefined}
        onLaunch={async () => undefined}
        onAction={async () => undefined}
        onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })}
        onWorktreeCheck={onWorktreeCheckNew}
      />
    );

    resolveCheck({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false });

    expect(await screen.findByRole("button", { name: /Create a worktree/i })).toBeInTheDocument();
  });

  it("debounces manual path input responsively at 150ms (WTL-003)", () => {
    vi.useFakeTimers();
    try {
      const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false }));
      render(
        <ManagedAgentDialog
          open
          onClose={() => undefined}
          onLaunch={async () => undefined}
          onAction={async () => undefined}
          onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })}
          onWorktreeCheck={onWorktreeCheck}
        />
      );

      const input = screen.getByLabelText("Working folder");
      fireEvent.change(input, { target: { value: "/allowed/repo" } });

      vi.advanceTimersByTime(100);
      expect(onWorktreeCheck).not.toHaveBeenCalled();

      vi.advanceTimersByTime(50);
      expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo", { includeStatus: false });
    } finally {
      vi.useRealTimers();
    }
  });

  it("instantly renders worktree toggle for cached paths without network delay (WTL-004)", async () => {
    const user = userEvent.setup();
    const onWorktreeCheck = vi.fn(async (path: string) => {
      if (path === "/allowed/repo-a") {
        return { is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false };
      }
      return { is_git_repo: true, is_main_worktree: false, has_uncommitted_changes: false };
    });

    render(
      <ManagedAgentDialog
        open
        recentFolders={["/allowed/repo-a", "/allowed/repo-b"]}
        onClose={() => undefined}
        onLaunch={async () => undefined}
        onAction={async () => undefined}
        onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })}
        onWorktreeCheck={onWorktreeCheck}
      />
    );

    await user.click(screen.getByRole("button", { name: "Recent folders" }));
    await user.click(screen.getByRole("option", { name: "Select folder /allowed/repo-a" }));
    expect(await screen.findByRole("button", { name: /Create a worktree/i })).toBeInTheDocument();
    expect(onWorktreeCheck).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "Recent folders" }));
    await user.click(screen.getByRole("option", { name: "Select folder /allowed/repo-b" }));
    await waitFor(() => expect(screen.queryByRole("button", { name: /Create a worktree/i })).not.toBeInTheDocument());
    expect(onWorktreeCheck).toHaveBeenCalledTimes(2);

    await user.click(screen.getByRole("button", { name: "Recent folders" }));
    await user.click(screen.getByRole("option", { name: "Select folder /allowed/repo-a" }));

    expect(screen.getByRole("button", { name: /Create a worktree/i })).toBeInTheDocument();
    expect(onWorktreeCheck).toHaveBeenCalledTimes(2);
  });

  it("discards out-of-order check response when directory changes (WTL-005)", async () => {
    let resolveSlowCheck: (val: { is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }) => void = () => {};
    const onWorktreeCheck = vi.fn((path: string) => {
      if (path === "/allowed/slow-repo") {
        return new Promise<{ is_git_repo: boolean; is_main_worktree: boolean; has_uncommitted_changes: boolean }>((resolve) => {
          resolveSlowCheck = resolve;
        });
      }
      return Promise.resolve({ is_git_repo: false, is_main_worktree: false, has_uncommitted_changes: false });
    });

    render(
      <ManagedAgentDialog
        open
        recentFolders={["/allowed/slow-repo", "/allowed/fast-dir"]}
        onClose={() => undefined}
        onLaunch={async () => undefined}
        onAction={async () => undefined}
        onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })}
        onWorktreeCheck={onWorktreeCheck}
      />
    );
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: "Recent folders" }));
    await user.click(screen.getByRole("option", { name: "Select folder /allowed/slow-repo" }));
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/slow-repo", { includeStatus: false }));

    await user.click(screen.getByRole("button", { name: "Recent folders" }));
    await user.click(screen.getByRole("option", { name: "Select folder /allowed/fast-dir" }));
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/fast-dir", { includeStatus: false }));
    await waitFor(() => expect(screen.queryByRole("button", { name: /Create a worktree/i })).not.toBeInTheDocument());

    resolveSlowCheck({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false });
    await new Promise((r) => setTimeout(r, 20));

    expect(screen.queryByRole("button", { name: /Create a worktree/i })).not.toBeInTheDocument();
  });

  it("fast launch check bypasses expensive sequential git status via include_status=false (WTL-006)", async () => {
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false }));
    render(
      <ManagedAgentDialog
        open
        onClose={() => undefined}
        onLaunch={async () => undefined}
        onAction={async () => undefined}
        onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })}
        onWorktreeCheck={onWorktreeCheck}
      />
    );
    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed/huge-repo" } });
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/huge-repo", { includeStatus: false }));
    expect(onWorktreeCheck).toHaveBeenCalledWith(expect.any(String), expect.objectContaining({ includeStatus: false }));
  });

  it("worker termination confirmation retains include_status=true by omitting includeStatus: false (WTL-007)", async () => {
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: false, has_uncommitted_changes: true }));
    const worktreeAgent = {
      agent_id: "managed-alpha",
      source: "router-managed" as const,
      cwd: "/allowed/project",
      worktree: true,
      model: { provider: "openai-codex", id: "gpt-5.6-terra" },
      desired_state: "running",
      lifecycle_state: "running",
      activity_state: "idle",
      accepting_prompts: true,
      manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 16 },
      created_at: "2030-01-01T00:00:00.000Z",
      started_at: null,
      updated_at: "2030-01-01T00:00:00.000Z",
      last_error: null,
    };
    render(
      <TerminateConfirmationDialog
        open
        agent={worktreeAgent}
        onClose={() => undefined}
        onConfirm={async () => undefined}
        onWorktreeCheck={onWorktreeCheck}
      />
    );

    expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/project");
    expect(onWorktreeCheck).not.toHaveBeenCalledWith(expect.any(String), { includeStatus: false });
    expect(await screen.findByText(/Warning: This worktree has uncommitted changes that will be lost\./i)).toBeInTheDocument();
  });
});
