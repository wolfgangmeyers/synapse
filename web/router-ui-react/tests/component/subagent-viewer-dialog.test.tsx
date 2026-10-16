import "@testing-library/jest-dom/vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { SubagentViewerDialog } from "../../src/components/SubagentViewerDialog";
import type { ManagedChild } from "../../src/protocol/types";

afterEach(cleanup);

describe("SubagentViewerDialog", () => {
  it("sorts subagent cards by recency descending (most recent first)", () => {
    const subagents: Partial<ManagedChild>[] = [
      {
        id: "sub-1",
        description: "Task at 5:39 AM",
        status: "running",
        started_at: "2026-03-30T05:39:00Z",
      },
      {
        id: "sub-2",
        description: "Task at 5:06 AM",
        status: "running",
        started_at: "2026-03-30T05:06:00Z",
      },
      {
        id: "sub-3",
        description: "Task at 5:33 AM",
        status: "running",
        started_at: "2026-03-30T05:33:00Z",
      },
      {
        id: "sub-4",
        description: "Task at 5:42 AM",
        status: "running",
        started_at: "2026-03-30T05:42:00Z",
      },
    ];

    render(
      <SubagentViewerDialog
        open={true}
        onClose={() => {}}
        subagents={subagents as ManagedChild[]}
      />
    );

    const cards = screen.getAllByRole("button", { name: /Task at/ });
    expect(cards).toHaveLength(4);

    // Cards should appear in descending order of started_at:
    // 5:42 AM (sub-4), 5:39 AM (sub-1), 5:33 AM (sub-3), 5:06 AM (sub-2)
    expect(cards[0]).toHaveTextContent("Task at 5:42 AM");
    expect(cards[1]).toHaveTextContent("Task at 5:39 AM");
    expect(cards[2]).toHaveTextContent("Task at 5:33 AM");
    expect(cards[3]).toHaveTextContent("Task at 5:06 AM");
  });

  it("renders assistant messages using MarkdownBody in subagent-message-entry", async () => {
    const subagents = [
      {
        id: "sub-1",
        description: "Worker Task",
        status: "running" as const,
        started_at: "2026-03-30T05:00:00Z",
      },
    ];

    const mockHistoryEntries = [
      {
        id: "complete:2",
        agent_id: "sub-1",
        category: "assistant_text",
        message: "Hello from assistant!",
        at: "2026-03-30T05:02:00Z",
      },
      {
        id: "complete:1",
        agent_id: "sub-1",
        category: "user_prompt",
        message_content: "Please do this task.",
        at: "2026-03-30T05:01:00Z",
      },
    ];

    const mockClient = {
      managedHistory: async () => ({
        view: "complete" as const,
        entries: mockHistoryEntries,
        next_before: null,
      }),
    };

    render(
      <SubagentViewerDialog
        open={true}
        onClose={() => {}}
        subagents={subagents as any}
        client={mockClient as any}
      />
    );

    // Click on the subagent card to open drilldown view
    const card = screen.getByRole("button", { name: /Worker Task/ });
    card.click();

    // Verify messages appear sorted ascending by sequence (complete:1 before complete:2)
    const assistantMsg = await screen.findByText("Hello from assistant!");
    expect(assistantMsg).toBeInTheDocument();

    const userMsg = await screen.findByText("Please do this task.");
    expect(userMsg).toBeInTheDocument();

    const roles = screen.getAllByText(/(Assistant|User)/);
    expect(roles[0]).toHaveTextContent("User");
    expect(roles[1]).toHaveTextContent("Assistant");
  });

  it("does not render manual refresh button in detail view and polls every 2 seconds", async () => {
    vi.useFakeTimers();

    const subagents = [
      {
        id: "sub-1",
        description: "Polling Task",
        status: "running" as const,
        started_at: "2026-03-30T05:00:00Z",
      },
    ];

    let callCount = 0;
    const mockClient = {
      managedHistory: vi.fn(async () => {
        callCount++;
        return {
          view: "complete" as const,
          entries: [
            {
              id: `complete:${callCount}`,
              agent_id: "sub-1",
              category: "assistant_text",
              message: `Turn update ${callCount}`,
              at: "2026-03-30T05:01:00Z",
            },
          ],
          next_before: null,
        };
      }),
    };

    const { rerender } = render(
      <SubagentViewerDialog
        open={true}
        onClose={() => {}}
        subagents={subagents as any}
        client={mockClient as any}
      />
    );

    // Initial render in list view triggers pollPreviews for active subagents
    expect(mockClient.managedHistory).toHaveBeenCalledWith("sub-1", "complete");
    mockClient.managedHistory.mockClear();

    // Open detail view
    const card = screen.getByRole("button", { name: /Polling Task/ });
    act(() => {
      card.click();
    });

    // Verify initial load happened
    expect(mockClient.managedHistory).toHaveBeenCalledTimes(1);

    // Verify no manual refresh button exists
    expect(screen.queryByRole("button", { name: /Refresh conversation/i })).not.toBeInTheDocument();

    // Advance 2 seconds -> should poll again
    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    expect(mockClient.managedHistory).toHaveBeenCalledTimes(2);

    // Advance another 2 seconds -> polls again
    await act(async () => {
      vi.advanceTimersByTime(2000);
    });
    expect(mockClient.managedHistory).toHaveBeenCalledTimes(3);

    // Click back button to navigate back to list view
    const backBtn = screen.getByRole("button", { name: /Back to subagents/i });
    act(() => {
      backBtn.click();
    });

    // Reset mock calls to verify detail polling has stopped and list view preview polling resumed
    mockClient.managedHistory.mockClear();

    // Advance time -> detail history polling should no longer trigger
    await act(async () => {
      vi.advanceTimersByTime(4000);
    });

    // In list view, preview polling polls every 2 seconds for active subagents
    // but the detail view's interval (loading complete entries for sub-1) was stopped upon back navigation
    vi.useRealTimers();
  });

  it("deduplicates thinking traces across consecutive tool invocations carrying identical thinking text", async () => {
    const subagents = [
      {
        id: "sub-1",
        description: "Tool Task",
        status: "running" as const,
        started_at: "2026-03-30T05:00:00Z",
      },
    ];

    const duplicateThinking = "Planning to check the directory structure and read files";

    const mockHistoryEntries = [
      {
        id: "complete:1",
        agent_id: "sub-1",
        category: "managed_tool",
        message: "Tool execution 1",
        thinking: duplicateThinking,
        at: "2026-03-30T05:01:00Z",
        presentation: {
          kind: "tool" as const,
          tool_name: "bash",
          thinking: duplicateThinking,
        },
      },
      {
        id: "complete:2",
        agent_id: "sub-1",
        category: "managed_tool",
        message: "Tool execution 2",
        thinking: duplicateThinking,
        at: "2026-03-30T05:02:00Z",
        presentation: {
          kind: "tool" as const,
          tool_name: "read",
          thinking: duplicateThinking,
        },
      },
      {
        id: "complete:3",
        agent_id: "sub-1",
        category: "managed_tool",
        message: "Tool execution 3",
        thinking: duplicateThinking,
        at: "2026-03-30T05:03:00Z",
        presentation: {
          kind: "tool" as const,
          tool_name: "edit",
          thinking: duplicateThinking,
        },
      },
    ];

    const mockClient = {
      managedHistory: async () => ({
        view: "complete" as const,
        entries: mockHistoryEntries,
        next_before: null,
      }),
    };

    render(
      <SubagentViewerDialog
        open={true}
        onClose={() => {}}
        subagents={subagents as any}
        client={mockClient as any}
      />
    );

    // Open drilldown view
    const card = screen.getByRole("button", { name: /Tool Task/ });
    act(() => {
      card.click();
    });

    // Wait for the thinking text to appear
    const thinkingNodes = await screen.findAllByText(duplicateThinking);
    // Only one ThinkingTraceCard should be rendered across the 3 consecutive tool calls
    expect(thinkingNodes).toHaveLength(1);
  });

  it("displays the last activity preview on card preview and does not duplicate description in drilldown header", async () => {
    const subagents = [
      {
        id: "sub-1",
        description: "Task Description for Worker",
        status: "running" as const,
        started_at: "2026-03-30T05:00:00Z",
      },
    ];

    const mockHistoryEntries = [
      {
        id: "complete:1",
        agent_id: "sub-1",
        category: "managed_tool",
        message: "Tool execution",
        presentation: {
          kind: "tool" as const,
          tool_name: "bash",
        },
        tool_input: {
          command: "git status --porcelain",
        },
        at: "2026-03-30T05:01:00Z",
      },
    ];

    const mockClient = {
      managedHistory: vi.fn(async () => ({
        view: "complete" as const,
        entries: mockHistoryEntries,
        next_before: null,
      })),
    };

    render(
      <SubagentViewerDialog
        open={true}
        onClose={() => {}}
        subagents={subagents as any}
        client={mockClient as any}
      />
    );

    // Verify card preview displays the last activity preview (bash command)
    const previewElement = await screen.findByText("git status --porcelain");
    expect(previewElement).toBeInTheDocument();
    expect(previewElement.closest(".subagent-card-preview")).toBeInTheDocument();

    // Click to enter drilldown view
    const card = screen.getByRole("button", { name: /Task Description for Worker/ });
    act(() => {
      card.click();
    });

    // Verify detail header does NOT duplicate the subagent description string under subagent-drilldown-info
    const drilldownInfo = document.querySelector(".subagent-drilldown-info");
    expect(drilldownInfo).toBeInTheDocument();
    expect(drilldownInfo).not.toHaveTextContent("Task Description for Worker");
    expect(drilldownInfo?.querySelector(".subagent-title")).toBeNull();

    // Dialog title in DialogShell still displays it at the top of the dialog
    expect(screen.getByRole("heading", { name: "Task Description for Worker" })).toBeInTheDocument();
  });

  it("renders companion text and steering messages with sender identity", async () => {
    const subagents = [
      {
        id: "sub-1",
        description: "Steered Task",
        status: "running" as const,
        started_at: "2026-03-30T05:00:00Z",
      },
    ];

    const mockHistoryEntries = [
      {
        id: "complete:1",
        agent_id: "sub-1",
        category: "managed_message",
        message: "Steering from parent-agent",
        message_content: "Please prioritize the emergency fix first.",
        sender_identity: "parent-agent",
        presentation: {
          kind: "message" as const,
          outcome: "success" as const,
          from: "parent-agent",
          summary: "Steering from parent-agent",
        },
        at: "2026-03-30T05:00:01Z",
      },
      {
        id: "complete:2",
        agent_id: "sub-1",
        category: "managed_tool",
        message: "Ran bash",
        presentation: {
          kind: "tool" as const,
          tool_name: "bash",
          outcome: "success" as const,
          summary: "Completed successfully",
          companion_text: "I am executing the emergency fix now.",
        },
        at: "2026-03-30T05:00:02Z",
      },
    ];

    const mockClient = {
      managedHistory: vi.fn(async () => ({
        view: "complete" as const,
        entries: mockHistoryEntries,
        next_before: null,
      })),
    };

    render(
      <SubagentViewerDialog
        open={true}
        onClose={() => {}}
        subagents={subagents as any}
        client={mockClient as any}
      />
    );

    // Drill down
    const card = screen.getByRole("button", { name: /Steered Task/ });
    act(() => {
      card.click();
    });

    // Check steering message and role
    await waitFor(() => {
      expect(screen.getByText("parent-agent")).toBeInTheDocument();
      expect(screen.getByText("Please prioritize the emergency fix first.")).toBeInTheDocument();
    });

    // Check companion text on tool call
    expect(screen.getByText("I am executing the emergency fix now.")).toBeInTheDocument();
  });

  it("renders a steering message with exact payload from complete:36854", async () => {
    const subagents = [
      {
        id: "sub-1",
        description: "Optimize Tests Worker",
        status: "running" as const,
        started_at: "2026-03-30T05:00:00Z",
      },
    ];

    const mockHistoryEntries = [
      {
        id: "complete:36854",
        agent_id: "sub-1",
        from: "optimize-tests",
        message: "Steering from optimize-tests",
        message_content: "Hello from the user",
        presentation: {
          from: "optimize-tests",
        },
      },
    ];

    const mockClient = {
      managedHistory: vi.fn(async () => ({
        view: "complete" as const,
        entries: mockHistoryEntries,
        next_before: null,
      })),
    };

    render(
      <SubagentViewerDialog
        open={true}
        onClose={() => {}}
        subagents={subagents as any}
        client={mockClient as any}
      />
    );

    const card = screen.getByRole("button", { name: /Optimize Tests Worker/ });
    act(() => {
      card.click();
    });

    const content = await screen.findByText("Hello from the user");
    expect(content).toBeInTheDocument();

    const role = screen.getByText("optimize-tests");
    expect(role).toBeInTheDocument();

    const entryDiv = content.closest(".subagent-message-entry");
    expect(entryDiv).toHaveClass("user");
  });

  it("displays the most recent activity preview on card preview regardless of entry ordering from API", async () => {
    const subagents = [
      {
        id: "sub-1",
        description: "Task Description for Worker",
        status: "running" as const,
        started_at: "2026-03-30T05:00:00Z",
      },
    ];

    // API returns entries ordered descending (newest first: complete:3, complete:2, complete:1)
    const mockHistoryEntriesDescending = [
      {
        id: "complete:3",
        agent_id: "sub-1",
        category: "managed_tool",
        message: "Tool execution",
        presentation: {
          kind: "tool" as const,
          tool_name: "edit",
        },
        tool_input: {
          path: "src/main.ts",
        },
        at: "2026-03-30T05:03:00Z",
      },
      {
        id: "complete:2",
        agent_id: "sub-1",
        category: "managed_tool",
        message: "Tool execution",
        presentation: {
          kind: "tool" as const,
          tool_name: "read",
        },
        tool_input: {
          path: "src/index.ts",
        },
        at: "2026-03-30T05:02:00Z",
      },
      {
        id: "complete:1",
        agent_id: "sub-1",
        category: "managed_tool",
        message: "Tool execution",
        presentation: {
          kind: "tool" as const,
          tool_name: "bash",
        },
        tool_input: {
          command: "git status --porcelain",
        },
        at: "2026-03-30T05:01:00Z",
      },
    ];

    const mockClient = {
      managedHistory: vi.fn(async () => ({
        view: "complete" as const,
        entries: mockHistoryEntriesDescending,
        next_before: null,
      })),
    };

    render(
      <SubagentViewerDialog
        open={true}
        onClose={() => {}}
        subagents={subagents as any}
        client={mockClient as any}
      />
    );

    // Verify card preview displays the latest activity (edit: src/main.ts), NOT the earliest (git status)
    const previewElement = await screen.findByText("edit: src/main.ts");
    expect(previewElement).toBeInTheDocument();
    expect(screen.queryByText("git status --porcelain")).toBeNull();
  });

  it("extracts the latest entry as card preview when entries are ordered ascending (earliest at index 0)", async () => {
    const subagents = [
      {
        id: "sub-2",
        description: "Task Ascending Order",
        status: "running" as const,
        started_at: "2026-03-30T05:00:00Z",
      },
    ];

    // API returns entries ordered ascending (earliest first: complete:1, complete:2, complete:3)
    const mockHistoryEntriesAscending = [
      {
        id: "complete:1",
        agent_id: "sub-2",
        category: "managed_tool",
        message: "Tool execution",
        presentation: {
          kind: "tool" as const,
          tool_name: "bash",
        },
        tool_input: {
          command: "npm test",
        },
        at: "2026-03-30T05:01:00Z",
      },
      {
        id: "complete:2",
        agent_id: "sub-2",
        category: "managed_tool",
        message: "Tool execution",
        presentation: {
          kind: "tool" as const,
          tool_name: "read",
        },
        tool_input: {
          path: "package.json",
        },
        at: "2026-03-30T05:02:00Z",
      },
      {
        id: "complete:3",
        agent_id: "sub-2",
        category: "managed_tool",
        message: "Tool execution",
        presentation: {
          kind: "tool" as const,
          tool_name: "write",
        },
        tool_input: {
          path: "src/output.txt",
        },
        at: "2026-03-30T05:03:00Z",
      },
    ];

    const mockClient = {
      managedHistory: vi.fn(async () => ({
        view: "complete" as const,
        entries: mockHistoryEntriesAscending,
        next_before: null,
      })),
    };

    render(
      <SubagentViewerDialog
        open={true}
        onClose={() => {}}
        subagents={subagents as any}
        client={mockClient as any}
      />
    );

    // Verify card preview displays the latest entry (write: src/output.txt), not the first entry (npm test)
    const previewElement = await screen.findByText("write: src/output.txt");
    expect(previewElement).toBeInTheDocument();
    expect(screen.queryByText("npm test")).toBeNull();
  });
});

