import { describe, expect, it, vi, beforeEach } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { ConversationHeader } from "../../src/components/ConversationHeader";
import type { Agent } from "../../src/state/agent-projection";
import type { Workflow } from "../../src/domain/workflow-states";
import type { RouterHttpClient } from "../../src/transport/http-client";
import {
  formatCompactTokens,
  formatContextUsageLabel,
  formatContextUsageTooltip,
  contextUsageSeverityClass,
} from "../../src/utils/format";

describe("format utilities for context usage", () => {
  it("formats compact tokens correctly", () => {
    expect(formatCompactTokens(1_048_576)).toBe("1M");
    expect(formatCompactTokens(2_000_000)).toBe("2M");
    expect(formatCompactTokens(1_500_000)).toBe("1.5M");
    expect(formatCompactTokens(200_000)).toBe("200k");
    expect(formatCompactTokens(128_000)).toBe("128k");
    expect(formatCompactTokens(32_768)).toBe("33k");
    expect(formatCompactTokens(500)).toBe("500");
    expect(formatCompactTokens(0)).toBe("0");
  });

  it("formats context usage label correctly", () => {
    expect(formatContextUsageLabel({ tokens: 440120, context_window: 1048576, percent: 42.0 })).toBe("42% of 1M");
    expect(formatContextUsageLabel({ tokens: null, context_window: 200000, percent: null })).toBe("- / 200k");
  });

  it("formats context usage tooltip correctly", () => {
    expect(formatContextUsageTooltip({ tokens: 440120, context_window: 1048576, percent: 42.0 })).toBe("440,120 / 1,048,576 tokens (42.0%)");
    expect(formatContextUsageTooltip({ tokens: null, context_window: 100000, percent: null })).toBe("Unknown / 100,000 tokens (N/A)");
  });

  it("calculates severity class correctly", () => {
    expect(contextUsageSeverityClass(null)).toBe("normal");
    expect(contextUsageSeverityClass(undefined)).toBe("normal");
    expect(contextUsageSeverityClass(42)).toBe("normal");
    expect(contextUsageSeverityClass(49.9)).toBe("normal");
    expect(contextUsageSeverityClass(50)).toBe("warning");
    expect(contextUsageSeverityClass(79.9)).toBe("warning");
    expect(contextUsageSeverityClass(80)).toBe("critical");
    expect(contextUsageSeverityClass(95)).toBe("critical");
  });
});

describe("ConversationHeader context usage bar", () => {
  const baseAgent: Agent = {
    agent_id: "test-agent",
    source: "router-managed",
    activity_state: "idle",
  };

  it("renders context usage bar with normal severity (<50%)", () => {
    const agent: Agent = {
      ...baseAgent,
      context_usage: {
        tokens: 440120,
        context_window: 1048576,
        percent: 42.0,
      },
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-metric-card.context-card");
    expect(bar).not.toBeNull();
    expect(bar?.getAttribute("title")).toBe("440,120 / 1,048,576 tokens (42.0%)");
    expect(bar?.getAttribute("aria-label")).toBe("Context usage: 42% of 1M");

    const fill = container.querySelector(".header-metric-fill.context-fill");
    expect(fill).not.toBeNull();
    expect(fill?.classList.contains("normal")).toBe(true);
    expect(fill?.getAttribute("style")).toContain("width: 42%");

    const value = bar?.querySelector(".header-metric-value");
    expect(value?.textContent).toBe("42%");
  });

  it("renders context usage bar with warning severity (50-79%)", () => {
    const agent: Agent = {
      ...baseAgent,
      context_usage: {
        tokens: 130000,
        context_window: 200000,
        percent: 65.0,
      },
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-metric-card.context-card");
    expect(bar).not.toBeNull();
    expect(bar?.getAttribute("title")).toBe("130,000 / 200,000 tokens (65.0%)");
    expect(bar?.getAttribute("aria-label")).toBe("Context usage: 65% of 200k");

    const fill = container.querySelector(".header-metric-fill.context-fill");
    expect(fill).not.toBeNull();
    expect(fill?.classList.contains("warning")).toBe(true);
    expect(fill?.getAttribute("style")).toContain("width: 65%");

    const value = bar?.querySelector(".header-metric-value");
    expect(value?.textContent).toBe("65%");
  });

  it("renders context usage bar with critical severity (>=80%)", () => {
    const agent: Agent = {
      ...baseAgent,
      context_usage: {
        tokens: 108800,
        context_window: 128000,
        percent: 85.0,
      },
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-metric-card.context-card");
    expect(bar).not.toBeNull();
    expect(bar?.getAttribute("title")).toBe("108,800 / 128,000 tokens (85.0%)");

    const fill = container.querySelector(".header-metric-fill.context-fill");
    expect(fill).not.toBeNull();
    expect(fill?.classList.contains("critical")).toBe(true);
    expect(fill?.getAttribute("style")).toContain("width: 85%");

    const value = bar?.querySelector(".header-metric-value");
    expect(value?.textContent).toBe("85%");
  });

  it("omits context usage bar when agent has no context_usage (e.g. tmux worker)", () => {
    const agent: Agent = {
      agent_id: "tmux-worker",
      source: "tmux",
      activity_state: "idle",
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-metric-card.context-card");
    expect(bar).toBeNull();
  });

  it("omits context usage bar when context_window <= 0", () => {
    const agent: Agent = {
      ...baseAgent,
      context_usage: {
        tokens: 0,
        context_window: 0,
        percent: null,
      },
    };

    const { container } = render(<ConversationHeader agent={agent} />);
    const bar = container.querySelector(".header-metric-card.context-card");
    expect(bar).toBeNull();
  });
});

describe("ConversationHeader workflow controls", () => {
  beforeEach(() => {
    cleanup();
  });
  const routerManagedAgent: Agent = {
    agent_id: "agent-1",
    source: "router-managed",
    activity_state: "idle",
  };

  const sampleWorkflows: Workflow[] = [
    {
      id: "wf-1",
      name: "Simple Workflow",
      description: "A workflow without variables",
      entry_state_id: "state-1",
      state_count: 3,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
    {
      id: "wf-2",
      name: "Complex Workflow",
      description: "A workflow with variables",
      entry_state_id: "state-2",
      state_count: 5,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      variables: [
        {
          name: "target_env",
          label: "Target Environment",
          type: "text",
          required: true,
          default_value: "production",
        },
      ],
    },
    {
      id: "wf-3",
      name: "No Entry Workflow",
      description: "A workflow without entry state",
      entry_state_id: null,
      state_count: 0,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
    },
  ];

  function createMockClient(overrides: Partial<RouterHttpClient> = {}): RouterHttpClient {
    return {
      listWorkflows: vi.fn().mockResolvedValue(sampleWorkflows),
      setManagedAgentWorkflow: vi.fn().mockResolvedValue({}),
      setManagedAgentWorkflowState: vi.fn().mockResolvedValue({}),
      getWorkflowState: vi.fn().mockResolvedValue({
        workflow_state: {
          id: "state-2",
          name: "Initial State",
          entry_text: "Deploying to ${target_env}",
          goal: "Ensure ${target_env} is healthy",
        },
      }),
      ...overrides,
    } as unknown as RouterHttpClient;
  }

  it("renders lightning bolt icon instead of diagram-project icon", async () => {
    const mockClient = createMockClient();
    const { container } = render(
      <ConversationHeader agent={routerManagedAgent} client={mockClient} />
    );

    const btn = container.querySelector(".workflow-header-button");
    expect(btn).not.toBeNull();
    const boltIcon = btn?.querySelector(".fa-bolt");
    expect(boltIcon).not.toBeNull();
    const diagramIcon = btn?.querySelector(".fa-diagram-project");
    expect(diagramIcon).toBeNull();
  });

  it("adds 'has-workflow active' classes when agent has active workflow or active workflow state", () => {
    const mockClient = createMockClient();
    const agentWithWf: Agent = {
      ...routerManagedAgent,
      active_workflow_id: "wf-1",
    };
    const { container: c1 } = render(
      <ConversationHeader agent={agentWithWf} client={mockClient} />
    );
    expect(c1.querySelector(".workflow-header-button.has-workflow.active")).not.toBeNull();

    const agentWithState: Agent = {
      ...routerManagedAgent,
      active_workflow_state_id: "state-1",
    };
    const { container: c2 } = render(
      <ConversationHeader agent={agentWithState} client={mockClient} />
    );
    expect(c2.querySelector(".workflow-header-button.has-workflow.active")).not.toBeNull();
  });

  it("lists workflows with name, description, and state count badge when dropdown is opened", async () => {
    const mockClient = createMockClient();
    const { container } = render(
      <ConversationHeader agent={routerManagedAgent} client={mockClient} />
    );

    await waitFor(() => {
      expect(mockClient.listWorkflows).toHaveBeenCalled();
    });

    const btn = container.querySelector(".workflow-header-button")!;
    fireEvent.click(btn);

    expect(screen.getByText("Simple Workflow")).toBeDefined();
    expect(screen.getByText("A workflow without variables")).toBeDefined();
    expect(screen.getByText("3 states")).toBeDefined();
    expect(screen.getByText("Complex Workflow")).toBeDefined();
    expect(screen.getByText("5 states")).toBeDefined();
  });

  it("shows Deactivate Workflow action when agent has an active workflow", async () => {
    const mockClient = createMockClient();
    const agentWithWf: Agent = {
      ...routerManagedAgent,
      active_workflow_id: "wf-1",
    };
    const { container } = render(
      <ConversationHeader agent={agentWithWf} client={mockClient} />
    );

    await waitFor(() => {
      expect(mockClient.listWorkflows).toHaveBeenCalled();
    });

    const btn = container.querySelector(".workflow-header-button")!;
    fireEvent.click(btn);

    const deactivateBtn = container.querySelector(".workflow-dropdown-item.deactivate");
    expect(deactivateBtn).not.toBeNull();
    expect(deactivateBtn?.textContent).toContain("Deactivate Workflow");
    expect(deactivateBtn?.querySelector(".fa-xmark")).not.toBeNull();

    fireEvent.click(deactivateBtn!);
    await waitFor(() => {
      expect(mockClient.setManagedAgentWorkflowState).toHaveBeenCalledWith("agent-1", null);
    });
  });

  it("calls setManagedAgentWorkflow directly when clicking workflow without variables", async () => {
    const mockClient = createMockClient();
    const { container } = render(
      <ConversationHeader agent={routerManagedAgent} client={mockClient} />
    );

    await waitFor(() => {
      expect(mockClient.listWorkflows).toHaveBeenCalled();
    });

    const btn = container.querySelector(".workflow-header-button")!;
    fireEvent.click(btn);

    const simpleWfBtn = screen.getByText("Simple Workflow");
    fireEvent.click(simpleWfBtn);

    await waitFor(() => {
      expect(mockClient.setManagedAgentWorkflow).toHaveBeenCalledWith("agent-1", "wf-1");
    });
  });

  it("opens WorkflowLaunchModal when clicking workflow with variables", async () => {
    const mockClient = createMockClient();
    const { container } = render(
      <ConversationHeader agent={routerManagedAgent} client={mockClient} />
    );

    await waitFor(() => {
      expect(mockClient.listWorkflows).toHaveBeenCalled();
    });

    const btn = container.querySelector(".workflow-header-button")!;
    fireEvent.click(btn);

    const complexWfBtn = screen.getByText("Complex Workflow");
    fireEvent.click(complexWfBtn);

    await waitFor(() => {
      expect(screen.getByText("Launch Workflow: Complex Workflow")).toBeDefined();
      expect(screen.getByText("Target Environment")).toBeDefined();
    });
  });

  it("shows notification when workflow has no entry state", async () => {
    const mockClient = createMockClient();
    const { container } = render(
      <ConversationHeader agent={routerManagedAgent} client={mockClient} />
    );

    await waitFor(() => {
      expect(mockClient.listWorkflows).toHaveBeenCalled();
    });

    const btn = container.querySelector(".workflow-header-button")!;
    fireEvent.click(btn);

    const noEntryWfBtn = screen.getByText("No Entry Workflow");
    fireEvent.click(noEntryWfBtn);

    await waitFor(() => {
      expect(screen.getByText('Workflow "No Entry Workflow" has no entry state.')).toBeDefined();
    });
    expect(mockClient.setManagedAgentWorkflow).not.toHaveBeenCalled();
  });

  describe("Agent monitors / watchers button active styling", () => {
    it("renders monitors button without active class and default title when no active monitors exist", async () => {
      const mockClient = createMockClient({
        listAgentMonitors: vi.fn().mockResolvedValue([
          { id: "mon-1", name: "Monitor 1", enabled: false },
        ]),
      });

      const { container } = render(
        <ConversationHeader agent={routerManagedAgent} client={mockClient} />
      );

      await waitFor(() => {
        expect(mockClient.listAgentMonitors).toHaveBeenCalledWith("agent-1");
      });

      const monitorBtn = container.querySelector(".agent-monitors-header-button");
      expect(monitorBtn).not.toBeNull();
      expect(monitorBtn?.classList.contains("active")).toBe(false);
      expect(monitorBtn?.getAttribute("title")).toBe("System 1 Monitors");
      expect(monitorBtn?.getAttribute("aria-label")).toBe("System 1 Monitors");
    });

    it("renders monitors button with active class and count in tooltip when enabled monitors exist", async () => {
      const mockClient = createMockClient({
        listAgentMonitors: vi.fn().mockResolvedValue([
          { id: "mon-1", name: "Monitor 1", enabled: true },
          { id: "mon-2", name: "Monitor 2", enabled: false },
          { id: "mon-3", name: "Monitor 3", enabled: true },
        ]),
      });

      const { container } = render(
        <ConversationHeader agent={routerManagedAgent} client={mockClient} />
      );

      await waitFor(() => {
        const monitorBtn = container.querySelector(".agent-monitors-header-button");
        expect(monitorBtn?.classList.contains("active")).toBe(true);
      });

      const monitorBtn = container.querySelector(".agent-monitors-header-button");
      expect(monitorBtn?.getAttribute("title")).toBe("System 1 Monitors (2 active)");
      expect(monitorBtn?.getAttribute("aria-label")).toBe("System 1 Monitors (2 active)");
    });
  });

  describe("workflow state badge", () => {
    it("renders workflow state badge when agent.workflow_state is provided", () => {
      const agentWithState: Agent = {
        ...routerManagedAgent,
        workflow_state: "Implementation",
        workflow_name: "Feature Loop",
      };
      const { container } = render(<ConversationHeader agent={agentWithState} />);
      const badge = container.querySelector(".workflow-state-badge");
      expect(badge).not.toBeNull();
      expect(badge?.textContent).toContain("Implementation");
      const icon = badge?.querySelector(".fa-bolt");
      expect(icon).not.toBeNull();
      expect(badge?.getAttribute("title")).toBe("Workflow: Feature Loop (Implementation)");
    });

    it("clicking the badge invokes onOpenWorkflow when provided", () => {
      const onOpenWorkflow = vi.fn();
      const agentWithState: Agent = {
        ...routerManagedAgent,
        workflow_state: "Plan review",
      };
      const { container } = render(
        <ConversationHeader agent={agentWithState} onOpenWorkflow={onOpenWorkflow} />
      );
      const badge = container.querySelector(".workflow-state-badge");
      expect(badge).not.toBeNull();
      expect(badge?.tagName.toLowerCase()).toBe("button");
      expect(badge?.getAttribute("title")).toBe("Workflow: Workflow (Plan review) - Click to manage");

      fireEvent.click(badge!);
      expect(onOpenWorkflow).toHaveBeenCalledTimes(1);
    });

    it("omits workflow badge when agent.workflow_state is null, undefined, or empty", () => {
      const { container: c1 } = render(<ConversationHeader agent={routerManagedAgent} />);
      expect(c1.querySelector(".workflow-state-badge")).toBeNull();

      const { container: c2 } = render(
        <ConversationHeader agent={{ ...routerManagedAgent, workflow_state: "" }} />
      );
      expect(c2.querySelector(".workflow-state-badge")).toBeNull();

      const { container: c3 } = render(
        <ConversationHeader agent={{ ...routerManagedAgent, workflow_state: null }} />
      );
      expect(c3.querySelector(".workflow-state-badge")).toBeNull();
    });
  });

  describe("blocked state badge and popover", () => {
    it("renders blocked state badge when agent.blocked is true", () => {
      const blockedAgent: Agent = {
        ...routerManagedAgent,
        blocked: true,
        blocked_reason: "Missing production Stripe test API key",
        blocked_at: "2026-10-27T16:00:00.000Z",
      };
      const { container } = render(<ConversationHeader agent={blockedAgent} />);
      const badge = container.querySelector(".blocked-state-badge");
      expect(badge).not.toBeNull();
      expect(badge?.textContent).toContain("Blocked");
      expect(badge?.querySelector(".fa-hand")).not.toBeNull();
      expect(badge?.getAttribute("title")).toBe("Blocked: Missing production Stripe test API key");
    });

    it("omits blocked state badge when agent.blocked is false or undefined", () => {
      const { container: c1 } = render(<ConversationHeader agent={routerManagedAgent} />);
      expect(c1.querySelector(".blocked-state-badge")).toBeNull();

      const { container: c2 } = render(
        <ConversationHeader agent={{ ...routerManagedAgent, blocked: false }} />
      );
      expect(c2.querySelector(".blocked-state-badge")).toBeNull();
    });

    it("clicking the blocked badge toggles popover showing header, timestamp, and full reason", () => {
      const blockedAgent: Agent = {
        ...routerManagedAgent,
        blocked: true,
        blocked_reason: "Database connection failed due to missing credentials in env",
        blocked_at: "2026-10-27T16:00:00.000Z",
      };
      const { container } = render(<ConversationHeader agent={blockedAgent} />);
      const badge = container.querySelector(".blocked-state-badge")!;
      expect(badge).not.toBeNull();

      // Popover should initially be closed
      expect(container.querySelector(".blocked-reason-popover")).toBeNull();

      // Click badge to open
      fireEvent.click(badge);
      const popover = container.querySelector(".blocked-reason-popover");
      expect(popover).not.toBeNull();
      expect(popover?.querySelector(".blocked-popover-title-row")?.textContent).toContain("Agent Blocked");
      expect(popover?.querySelector(".blocked-popover-timestamp")?.textContent).toContain("Blocked:");
      expect(popover?.querySelector(".blocked-popover-content")?.textContent).toBe("Database connection failed due to missing credentials in env");

      // Click close button inside popover
      const closeBtn = popover?.querySelector(".blocked-popover-close-btn")!;
      expect(closeBtn).not.toBeNull();
      fireEvent.click(closeBtn);
      expect(container.querySelector(".blocked-reason-popover")).toBeNull();
    });

    it("dismisses popover on Escape key and click outside", () => {
      const blockedAgent: Agent = {
        ...routerManagedAgent,
        blocked: true,
        blocked_reason: "API quota exceeded",
      };
      const { container } = render(
        <div>
          <div data-testid="outside-element">Outside</div>
          <ConversationHeader agent={blockedAgent} />
        </div>
      );
      const badge = container.querySelector(".blocked-state-badge")!;

      // Open popover
      fireEvent.click(badge);
      expect(container.querySelector(".blocked-reason-popover")).not.toBeNull();

      // Press Escape
      fireEvent.keyDown(document, { key: "Escape" });
      expect(container.querySelector(".blocked-reason-popover")).toBeNull();

      // Open again and click outside
      fireEvent.click(badge);
      expect(container.querySelector(".blocked-reason-popover")).not.toBeNull();
      fireEvent.mouseDown(screen.getByTestId("outside-element"));
      expect(container.querySelector(".blocked-reason-popover")).toBeNull();
    });
  });
});
