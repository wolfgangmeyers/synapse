import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState, type ReactElement } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Conversation } from "../../src/components/Conversation";
import type { ConversationView } from "../../src/components/ConversationHeader";

afterEach(cleanup);

function ConversationModes(): ReactElement {
  const [view, setView] = useState<ConversationView>("quiet");
  return <Conversation peer={{ agent_id: "managed-worker", activity_state: "idle" }} conversationView={view} onConversationViewChange={setView} messages={[{ id: "message-1", from: "managed-worker", to: "wolfgang", body: "Conversation message", created_at: "2030-01-01T10:00:00.000Z" }]} activityEntries={[{ id: "activity-1", agent_id: "managed-worker", level: "info", message: "Managed worker restarted", at: "2030-01-01T10:01:00.000Z" }, { id: "activity-untimed", agent_id: "managed-worker", message: "Managed worker activity without a timestamp" }]} />;
}

describe("Selected-agent conversation modes", () => {
  it("keeps Quiet as the conversation and makes Complete include messages plus activity", async () => {
    const user = userEvent.setup();
    render(<ConversationModes />);

    const quiet = screen.getByRole("tab", { name: "Quiet messages" });
    const complete = screen.getByRole("tab", { name: "Complete: messages + activity" });
    expect(quiet).toHaveAttribute("aria-selected", "true");
    expect(quiet).toHaveAttribute("aria-controls", "conversation-view-panel");
    expect(screen.getByText("Conversation message")).toBeInTheDocument();
    expect(screen.queryByText("Managed worker restarted")).not.toBeInTheDocument();

    await user.click(complete);
    expect(complete).toHaveAttribute("aria-selected", "true");
    expect(screen.getByText("Conversation message")).toBeInTheDocument();
    expect(screen.getByText("Managed worker restarted")).toBeInTheDocument();
    expect(screen.getByText("Managed worker activity without a timestamp")).toBeInTheDocument();
    expect(document.querySelector("[data-message-id='message-1']")).toBeInTheDocument();
    expect(document.querySelector("[data-activity-id='activity-1']")).toBeInTheDocument();
    expect([...screen.getByRole("tabpanel").querySelectorAll("[data-message-id], [data-activity-id]")].map((item) => item.getAttribute("data-message-id") ?? item.getAttribute("data-activity-id"))).toEqual(["message-1", "activity-1", "activity-untimed"]);

    await user.click(quiet);
    expect(screen.getByText("Conversation message")).toBeInTheDocument();
    expect(screen.queryByText("Managed worker restarted")).not.toBeInTheDocument();
    expect(screen.queryByText("Managed worker activity without a timestamp")).not.toBeInTheDocument();
  });

  it("reports a failed managed agent in the selected-agent header", () => {
    const managedAgent = { agent_id: "managed-worker", source: "router-managed" as const, cwd: "/tmp/managed-worker", model: { provider: "openai-codex", id: "gpt-5.6-luna" }, desired_state: "running", lifecycle_state: "failed", activity_state: "failed", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: { code: "PROCESS_STOP_FAILED", message: "Managed worker reported an operational error", at: "2030-01-01T00:01:00.000Z" } };
    render(<Conversation peer={{ agent_id: managedAgent.agent_id, source: "router-managed", activity_state: "failed", managed_agent: managedAgent }} />);
    expect(screen.getByRole("status")).toHaveTextContent("Unavailable: Managed worker reported an operational error");
  });

  it("shows managed controls and terminating progress in the selected-agent header", async () => {
    const user = userEvent.setup();
    const onManageManaged = vi.fn();
    const onManagedState = vi.fn();
    const managedAgent = { agent_id: "managed-worker", source: "router-managed" as const, cwd: "/tmp/managed-worker", model: { provider: "openai-codex", id: "gpt-5.6-luna" }, desired_state: "terminated", lifecycle_state: "terminating", activity_state: "active", accepting_prompts: false, manifest: { revision: 1, sha256: "a".repeat(64), installed_tool_count: 1 }, created_at: "2030-01-01T00:00:00.000Z", started_at: "2030-01-01T00:00:00.000Z", updated_at: "2030-01-01T00:00:00.000Z", last_error: null };

    render(<Conversation peer={{ agent_id: managedAgent.agent_id, source: "router-managed", activity_state: "active", managed_agent: managedAgent }} onManageManaged={onManageManaged} onManagedState={onManagedState} />);

    expect(screen.getByRole("status")).toHaveTextContent("Terminating…");
    await user.click(screen.getByRole("button", { name: "Manage" }));
    await user.click(screen.getByRole("button", { name: "State" }));
    expect(onManageManaged).toHaveBeenCalledOnce();
    expect(onManagedState).toHaveBeenCalledOnce();
  });
});
