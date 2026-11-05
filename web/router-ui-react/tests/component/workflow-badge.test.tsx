import "@testing-library/jest-dom/vitest";
import { describe, expect, it, vi } from "vitest";
import { render, fireEvent, waitFor } from "@testing-library/react";
import type { Agent } from "../../src/state/agent-projection";
import { AgentRow } from "../../src/components/AgentRow";
import { ConversationHeader } from "../../src/components/ConversationHeader";

describe("Workflow Badge E2E Integration (Sidebar AgentRow + ConversationHeader)", () => {
  const initialAgent: Agent = {
    agent_id: "test-builder",
    source: "router-managed",
    activity_state: "idle",
    workflow_state: "End to end testing",
    workflow_name: "Simple Dev",
  };

  it("renders workflow badge with bolt icon in both AgentRow (sidebar) and ConversationHeader", () => {
    const onOpenWorkflow = vi.fn();

    const { container } = render(
      <div>
        <aside className="sidebar">
          <AgentRow agent={initialAgent} selected={true} />
        </aside>
        <main>
          <ConversationHeader agent={initialAgent} onOpenWorkflow={onOpenWorkflow} />
        </main>
      </div>
    );

    // a) Verify ConversationHeader workflow badge
    const headerBadge = container.querySelector(".workflow-state-badge");
    expect(headerBadge).not.toBeNull();
    expect(headerBadge?.textContent).toContain("End to end testing");
    const headerBolt = headerBadge?.querySelector(".fa-bolt");
    expect(headerBolt).not.toBeNull();
    expect(headerBadge?.getAttribute("title")).toBe(
      "Workflow: Simple Dev (End to end testing) - Click to manage"
    );

    // Click badge in ConversationHeader
    fireEvent.click(headerBadge!);
    expect(onOpenWorkflow).toHaveBeenCalledTimes(1);

    // b) Verify AgentRow workflow badge in sidebar
    const rowBadge = container.querySelector(".agent-row-workflow-badge");
    expect(rowBadge).not.toBeNull();
    expect(rowBadge?.textContent).toContain("End to end testing");
    const rowBolt = rowBadge?.querySelector(".fa-bolt");
    expect(rowBolt).not.toBeNull();
    expect(rowBadge?.getAttribute("title")).toBe(
      "Workflow: Simple Dev • State: End to end testing"
    );
  });

  it("dynamically updates badge text and styling when agent workflow_state transitions to Implementation", async () => {
    let currentAgent: Agent = {
      ...initialAgent,
      workflow_state: "End to end testing",
    };

    const { container, rerender } = render(
      <div>
        <aside className="sidebar">
          <AgentRow agent={currentAgent} />
        </aside>
        <main>
          <ConversationHeader agent={currentAgent} />
        </main>
      </div>
    );

    expect(container.querySelector(".workflow-state-badge")?.textContent).toContain("End to end testing");
    expect(container.querySelector(".agent-row-workflow-badge")?.textContent).toContain("End to end testing");

    // Simulate live agent state update transition: End to end testing -> Implementation
    currentAgent = {
      ...currentAgent,
      workflow_state: "Implementation",
      workflow_name: "dev-loop",
    };

    rerender(
      <div>
        <aside className="sidebar">
          <AgentRow agent={currentAgent} />
        </aside>
        <main>
          <ConversationHeader agent={currentAgent} />
        </main>
      </div>
    );

    await waitFor(() => {
      const headerBadge = container.querySelector(".workflow-state-badge");
      expect(headerBadge?.textContent).toContain("Implementation");
      expect(headerBadge?.querySelector(".fa-bolt")).not.toBeNull();
      expect(headerBadge?.getAttribute("title")).toBe("Workflow: dev-loop (Implementation)");

      const rowBadge = container.querySelector(".agent-row-workflow-badge");
      expect(rowBadge?.textContent).toContain("Implementation");
      expect(rowBadge?.querySelector(".fa-bolt")).not.toBeNull();
      expect(rowBadge?.getAttribute("title")).toBe("Workflow: dev-loop • State: Implementation");
    });
  });

  it("removes badges cleanly when workflow_state becomes null or undefined", async () => {
    let currentAgent: Agent = {
      ...initialAgent,
      workflow_state: "Implementation",
    };

    const { container, rerender } = render(
      <div>
        <aside className="sidebar">
          <AgentRow agent={currentAgent} />
        </aside>
        <main>
          <ConversationHeader agent={currentAgent} />
        </main>
      </div>
    );

    expect(container.querySelector(".workflow-state-badge")).not.toBeNull();
    expect(container.querySelector(".agent-row-workflow-badge")).not.toBeNull();

    // Deactivate workflow
    currentAgent = {
      ...currentAgent,
      workflow_state: null,
      workflow_name: null,
    };

    rerender(
      <div>
        <aside className="sidebar">
          <AgentRow agent={currentAgent} />
        </aside>
        <main>
          <ConversationHeader agent={currentAgent} />
        </main>
      </div>
    );

    await waitFor(() => {
      expect(container.querySelector(".workflow-state-badge")).toBeNull();
      expect(container.querySelector(".agent-row-workflow-badge")).toBeNull();
    });
  });
});
