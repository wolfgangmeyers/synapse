import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedAgentDialog } from "../../src/components/ManagedAgentDialog";
import type { ModelsResponse } from "../../src/transport/http-client";

describe("ManagedAgentDialog", () => {
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
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo/sub"));
    expect(screen.queryByRole("button", { name: /Create a worktree/i })).not.toBeInTheDocument();
  });

  it("shows worktree toggle when worktreeCheck returns is_main_worktree: true", async () => {
    const onWorktreeCheck = vi.fn(async () => ({ is_git_repo: true, is_main_worktree: true, has_uncommitted_changes: false }));
    render(<ManagedAgentDialog open onClose={() => undefined} onLaunch={async () => undefined} onAction={async () => undefined} onListDirectories={async () => ({ path: "/allowed", parent: "", directories: [] })} onWorktreeCheck={onWorktreeCheck} />);
    fireEvent.change(screen.getByLabelText("Working folder"), { target: { value: "/allowed/repo" } });
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo"));
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
    await waitFor(() => expect(onWorktreeCheck).toHaveBeenCalledWith("/allowed/repo"));

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
});
