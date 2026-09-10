import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ManagedAgentDialog } from "../../src/components/ManagedAgentDialog";

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
    await user.click(await screen.findByRole("button", { name: "project" }));
    await user.click(screen.getByRole("button", { name: "Launch" }));
    expect(onLaunch).toHaveBeenCalledWith({ name: "managed-alpha", cwd: "/allowed/project" });
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
