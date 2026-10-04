import { afterEach, describe, expect, it, vi } from "vitest";
import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { ActivityDetailDialog } from "../../src/components/ActivityDetailDialog";
import { ConversationHeader } from "../../src/components/ConversationHeader";
import { SubagentBadge } from "../../src/components/SubagentBadge";
import type { Agent } from "../../src/state/agent-projection";
import type { RouterHttpClient } from "../../src/transport/http-client";

afterEach(cleanup);

describe("ActivityDetailDialog parameter formatting", () => {
  it("renders tool parameters as labeled sections with unescaped plain text for strings", () => {
    const entry = {
      id: "activity-1",
      agent_id: "test-agent",
      at: "2026-10-04T12:00:00.000Z",
      presentation: { kind: "tool" as const, tool_name: "custom_tool", outcome: "success" as const },
      tool_input: {
        prompt: "First line\nSecond line\nThird line",
        description: "A multi-line\ndescription with \"quotes\"",
        count: 5,
        nested: { foo: "bar" },
      },
    };

    const { container } = render(<ActivityDetailDialog entry={entry} open onClose={() => {}} />);

    expect(screen.getByRole("heading", { name: "Parameters" })).toBeInTheDocument();
    expect(screen.getByText("prompt")).toBeInTheDocument();
    expect(container.querySelector(".activity-param-value")?.textContent).toBe("First line\nSecond line\nThird line");
    expect(screen.getByText("description")).toBeInTheDocument();
    expect(screen.getByText("count")).toBeInTheDocument();
    expect(screen.getByText("5")).toBeInTheDocument();
  });

  it("parses stringified JSON tool input into labeled sections", () => {
    const entry = {
      id: "activity-2",
      agent_id: "test-agent",
      at: "2026-10-04T12:00:00.000Z",
      presentation: { kind: "tool" as const, tool_name: "custom_tool", outcome: "success" as const },
      tool_input: JSON.stringify({
        action: "deploy",
        notes: "Line 1\nLine 2",
      }),
    };

    const { container } = render(<ActivityDetailDialog entry={entry} open onClose={() => {}} />);

    expect(screen.getByText("action")).toBeInTheDocument();
    expect(screen.getByText("deploy")).toBeInTheDocument();
    expect(screen.getByText("notes")).toBeInTheDocument();
    const values = Array.from(container.querySelectorAll(".activity-param-value")).map((el) => el.textContent);
    expect(values).toContain("Line 1\nLine 2");
  });
});

describe("ConversationHeader interrupt control", () => {
  it("renders Interrupt button when agent activity_state is active and calls interruptManagedAgent", async () => {
    const interruptManagedAgent = vi.fn().mockResolvedValue(undefined);
    const mockClient = { interruptManagedAgent } as unknown as RouterHttpClient;
    const activeAgent: Agent = {
      agent_id: "worker-1",
      source: "router-managed",
      activity_state: "active",
    };

    render(<ConversationHeader agent={activeAgent} client={mockClient} />);

    const interruptBtn = screen.getByRole("button", { name: "Interrupt worker" });
    expect(interruptBtn).toBeInTheDocument();
    expect(interruptBtn.querySelector(".fa-square")).toBeInTheDocument();

    fireEvent.click(interruptBtn);
    expect(interruptManagedAgent).toHaveBeenCalledWith("worker-1");
  });

  it("does not render Interrupt button when agent is idle", () => {
    const idleAgent: Agent = {
      agent_id: "worker-1",
      source: "router-managed",
      activity_state: "idle",
    };

    render(<ConversationHeader agent={idleAgent} />);
    expect(screen.queryByRole("button", { name: "Interrupt worker" })).not.toBeInTheDocument();
  });

  it("lights up reminder button when activeReminderCount > 0 for a managed agent", () => {
    const managedAgent: Agent = {
      agent_id: "worker-1",
      source: "router-managed",
      activity_state: "idle",
    };

    render(<ConversationHeader agent={managedAgent} activeReminderCount={2} />);
    const reminderBtn = screen.getByRole("button", { name: "Add reminder" });
    expect(reminderBtn).not.toHaveClass("empty");
  });
});

describe("SubagentBadge stop control", () => {
  it("renders Stop subagent button for running subagents and calls stopManagedSubagent", () => {
    const stopManagedSubagent = vi.fn().mockResolvedValue(undefined);
    const mockClient = { stopManagedSubagent } as unknown as RouterHttpClient;

    render(
      <SubagentBadge
        agentId="parent-agent"
        client={mockClient}
        expanded={true}
        subagents={[
          { id: "child-1", description: "Running task", status: "running" },
          { id: "child-2", description: "Done task", status: "completed" },
        ]}
      />
    );

    const stopButtons = screen.getAllByRole("button", { name: "Stop subagent" });
    expect(stopButtons).toHaveLength(1);

    fireEvent.click(stopButtons[0]!);
    expect(stopManagedSubagent).toHaveBeenCalledWith("parent-agent", "child-1");
  });
});
