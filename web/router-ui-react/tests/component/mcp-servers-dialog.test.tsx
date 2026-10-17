import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { afterEach, describe, it, expect, vi, beforeEach } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MCPServersDialog } from "../../src/components/MCPServersDialog";
import type { RouterHttpClient, MCPServer } from "../../src/transport/http-client";

afterEach(cleanup);

describe("MCPServersDialog", () => {
  const mockServers: MCPServer[] = [
    {
      id: "srv-1",
      name: "Podclave MCP",
      url: "https://podclave.com/mcp",
      auth_type: "none",
      status: "connected",
      tools_count: 5,
      last_sync_at: "2024-10-16T12:00:00Z",
    },
    {
      id: "srv-2",
      name: "Linear MCP",
      url: "https://mcp.linear.app/mcp",
      auth_type: "oauth",
      status: "needs_auth",
      tools_count: 0,
    },
  ];

  let mockClient: Partial<RouterHttpClient>;

  beforeEach(() => {
    mockClient = {
      listMCPServers: vi.fn().mockResolvedValue([...mockServers]),
      addMCPServer: vi.fn().mockImplementation(async (payload) => ({
        server: {
          id: "srv-new",
          name: payload.name || payload.url,
          url: payload.url,
          auth_type: payload.auth_type || "none",
          status: payload.auth_type === "oauth" ? "needs_auth" : "connected",
          tools_count: 0,
        },
        auth_url: payload.auth_type === "oauth" ? "https://auth.example.com/oauth" : undefined,
      })),
      deleteMCPServer: vi.fn().mockResolvedValue(undefined),
      syncMCPServer: vi.fn().mockResolvedValue({
        server: { ...mockServers[0], tools_count: 6 },
        tools: [],
      }),
      startMCPOAuth: vi.fn().mockResolvedValue({
        authorization_url: "https://auth.example.com/oauth/authorize?state=123",
      }),
      revokeMCPServer: vi.fn().mockResolvedValue({
        server: { ...mockServers[1], status: "needs_auth" },
      }),
    };
  });

  it("renders configured servers with status badges and tool counts in list mode", async () => {
    render(
      <MCPServersDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Podclave MCP")).toBeInTheDocument();
      expect(screen.getByText("Linear MCP")).toBeInTheDocument();
    });

    expect(screen.getByText("Connected")).toBeInTheDocument();
    expect(screen.getByText("Needs Auth")).toBeInTheDocument();
    expect(screen.getByText("5 tools")).toBeInTheDocument();
    expect(screen.getByText("0 tools")).toBeInTheDocument();

    // Verify header and that form inputs are not visible in list mode
    expect(screen.getByRole("heading", { name: "MCP Servers" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "+ Add Server" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Server URL")).not.toBeInTheDocument();
  });

  it("transitions between list and add mode and adds a new MCP server", async () => {
    const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);

    render(
      <MCPServersDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Podclave MCP")).toBeInTheDocument();
    });

    // Click "+ Add Server" to transition to add mode
    const addServerBtn = screen.getByRole("button", { name: "+ Add Server" });
    fireEvent.click(addServerBtn);

    // Verify add mode header and form inputs are visible
    expect(screen.getByRole("heading", { name: "Add MCP Server" })).toBeInTheDocument();
    const urlInput = screen.getByLabelText("Server URL");
    const nameInput = screen.getByLabelText("Server name");
    expect(urlInput).toBeInTheDocument();

    // Test cancel returns to list mode
    const cancelBtn = screen.getByRole("button", { name: "Cancel" });
    fireEvent.click(cancelBtn);
    expect(screen.getByRole("heading", { name: "MCP Servers" })).toBeInTheDocument();
    expect(screen.queryByLabelText("Server URL")).not.toBeInTheDocument();

    // Go back to add mode and submit
    fireEvent.click(screen.getByRole("button", { name: "+ Add Server" }));
    fireEvent.change(screen.getByLabelText("Server URL"), { target: { value: "https://test.example.com/mcp" } });
    fireEvent.change(screen.getByLabelText("Server name"), { target: { value: "Test Server" } });

    // With auth dropdown removed, button says "Authorize & Add"
    const submitBtn = screen.getByRole("button", { name: /Authorize & Add/i });
    fireEvent.click(submitBtn);

    await waitFor(() => {
      expect(mockClient.addMCPServer).toHaveBeenCalledWith({
        url: "https://test.example.com/mcp",
        name: "Test Server",
        auth_type: "oauth",
      });
    });

    // Should return to list mode
    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "MCP Servers" })).toBeInTheDocument();
    });

    openSpy.mockRestore();
  });

  it("renders empty state without a duplicate add button when no servers exist", async () => {
    mockClient.listMCPServers = vi.fn().mockResolvedValue([]);

    render(
      <MCPServersDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(
        screen.getByText('No MCP servers configured yet. Click "+ Add Server" above to connect one.')
      ).toBeInTheDocument();
    });

    // Exactly one "+ Add Server" button in header/toolbar, none in the empty state
    const addButtons = screen.getAllByRole("button", { name: "+ Add Server" });
    expect(addButtons).toHaveLength(1);
    fireEvent.click(addButtons[0]!);

    expect(screen.getByRole("heading", { name: "Add MCP Server" })).toBeInTheDocument();
    expect(screen.getByLabelText("Server URL")).toBeInTheDocument();
  });

  it("initiates OAuth popup for servers needing auth", async () => {
    const openSpy = vi.spyOn(window, "open").mockImplementation(() => null);

    render(
      <MCPServersDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Linear MCP")).toBeInTheDocument();
    });

    const authBtn = screen.getByRole("button", { name: "Authorize Linear MCP" });
    fireEvent.click(authBtn);

    await waitFor(() => {
      expect(mockClient.startMCPOAuth).toHaveBeenCalledWith("srv-2");
      expect(openSpy).toHaveBeenCalledWith(
        "https://auth.example.com/oauth/authorize?state=123",
        "mcp_oauth",
        expect.any(String)
      );
    });

    openSpy.mockRestore();
  });

  it("syncs tools when sync button is clicked", async () => {
    render(
      <MCPServersDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Podclave MCP")).toBeInTheDocument();
    });

    const syncBtn = screen.getByRole("button", { name: "Sync tools for Podclave MCP" });
    fireEvent.click(syncBtn);

    await waitFor(() => {
      expect(mockClient.syncMCPServer).toHaveBeenCalledWith("srv-1");
    });
  });

  it("deletes a server when delete button is clicked and confirmed", async () => {
    vi.spyOn(window, "confirm").mockReturnValue(true);

    render(
      <MCPServersDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Podclave MCP")).toBeInTheDocument();
    });

    const deleteBtn = screen.getByRole("button", { name: "Delete Podclave MCP" });
    fireEvent.click(deleteBtn);

    await waitFor(() => {
      expect(mockClient.deleteMCPServer).toHaveBeenCalledWith("srv-1");
    });
  });

  it("refreshes state upon receiving window message 'mcp_oauth_complete'", async () => {
    render(
      <MCPServersDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("Linear MCP")).toBeInTheDocument();
    });

    // Fire window postMessage event
    fireEvent(
      window,
      new MessageEvent("message", {
        data: {
          type: "mcp_oauth_complete",
          success: true,
          serverId: "srv-2",
          message: "Authorization successful",
        },
      })
    );

    await waitFor(() => {
      expect(screen.getByText("OAuth authorization completed successfully.")).toBeInTheDocument();
      expect(mockClient.listMCPServers).toHaveBeenCalledTimes(2); // initial + refresh
    });
  });
});
