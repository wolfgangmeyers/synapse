import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { afterEach, describe, it, expect, vi, beforeEach } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ToolDefaultsDialog } from "../../src/components/ToolDefaultsDialog";
import type { RouterHttpClient, ToolDefinitionItem } from "../../src/transport/http-client";

afterEach(cleanup);

describe("ToolDefaultsDialog", () => {
  const mockTools: ToolDefinitionItem[] = [
    { tool_name: "read", category: "File Operations", enabled: true },
    { tool_name: "write", category: "File Operations", enabled: false },
    { tool_name: "bash", category: "System & Shell", enabled: true },
  ];

  let mockClient: Partial<RouterHttpClient>;

  beforeEach(() => {
    mockClient = {
      getToolDefaults: vi.fn().mockResolvedValue([...mockTools]),
      updateToolDefaults: vi.fn().mockResolvedValue({ ok: true }),
    };
  });

  it("renders tools grouped by category with enable counts", async () => {
    render(
      <ToolDefaultsDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("File Operations")).toBeInTheDocument();
      expect(screen.getByText("System & Shell")).toBeInTheDocument();
      expect(screen.getByText("1 / 2 enabled")).toBeInTheDocument();
      expect(screen.getByText("1 / 1 enabled")).toBeInTheDocument();
    });

    expect(screen.getByText("read")).toBeInTheDocument();
    expect(screen.getByText("write")).toBeInTheDocument();
    expect(screen.getByText("bash")).toBeInTheDocument();
  });

  it("handles toggling an individual tool", async () => {
    render(
      <ToolDefaultsDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("read")).toBeInTheDocument();
    });

    const readCheckbox = screen.getByLabelText("read") as HTMLInputElement;
    expect(readCheckbox.checked).toBe(true);

    fireEvent.click(readCheckbox);

    await waitFor(() => {
      expect(mockClient.updateToolDefaults).toHaveBeenCalledWith({
        tool_name: "read",
        enabled: false,
      });
    });
  });

  it("handles Enable All and Disable All for a category", async () => {
    render(
      <ToolDefaultsDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByText("File Operations")).toBeInTheDocument();
    });

    const enableAllButtons = screen.getAllByRole("button", { name: "Enable All" });
    expect(enableAllButtons[0]).toBeDefined();
    fireEvent.click(enableAllButtons[0]!);

    await waitFor(() => {
      expect(mockClient.updateToolDefaults).toHaveBeenCalledWith({
        category: "File Operations",
        enabled: true,
      });
    });

    const disableAllButtons = screen.getAllByRole("button", { name: "Disable All" });
    expect(disableAllButtons[0]).toBeDefined();
    fireEvent.click(disableAllButtons[0]!);

    await waitFor(() => {
      expect(mockClient.updateToolDefaults).toHaveBeenCalledWith({
        category: "File Operations",
        enabled: false,
      });
    });
  });
});
