import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen, within } from "@testing-library/react";
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
    await user.type(screen.getByLabelText("Name"), "managed-alpha");
    await user.type(screen.getByLabelText("Working folder"), "/allowed");
    await user.click(screen.getByRole("button", { name: "Browse folders" }));
    const folderBrowser = await screen.findByRole("dialog", { name: "Choose working folder" });
    await user.click(within(folderBrowser).getByRole("button", { name: "project" }));
    await user.click(within(folderBrowser).getByRole("button", { name: "Select folder" }));
    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({ name: "managed-alpha", cwd: "/allowed/project" });
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
    expect(onLaunch).toHaveBeenCalledWith({ name: "managed-alpha", cwd: "/allowed/project", model: { provider: "openai-codex", id: "gpt-5.6-terra" } });
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
    await new Promise((resolve) => setTimeout(resolve, 0));
    await user.type(screen.getByLabelText("Name"), "managed-alpha");
    expect(screen.getByRole("alert")).toHaveTextContent("That name is in use by an active or launching agent. Choose another name or wait for it to stop.");
    expect(screen.getByLabelText("Name")).toHaveValue("managed-alpha");
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
});
