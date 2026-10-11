import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { ScratchpadViewer } from "../../src/components/ScratchpadViewer";
import type { RouterHttpClient, ScratchpadResponse } from "../../src/transport/http-client";

afterEach(cleanup);

function mockClient(response: ScratchpadResponse | Promise<ScratchpadResponse>): RouterHttpClient {
  return {
    routerUrl: "http://127.0.0.1:8765",
    scratchpadForAgent: vi.fn().mockImplementation(() => Promise.resolve(response)),
  } as unknown as RouterHttpClient;
}

describe("ScratchpadViewer", () => {
  it("renders scratchpad entries with pinned entries highlighted at top", async () => {
    const entries = [
      {
        id: "note-1",
        agent_id: "test-agent",
        content: "First unpinned note",
        pinned: false,
        created_at: "2026-06-01T12:00:00Z",
        updated_at: "2026-06-01T12:00:00Z",
      },
      {
        id: "note-2",
        agent_id: "test-agent",
        content: "Important pinned note",
        pinned: true,
        created_at: "2026-06-01T12:05:00Z",
        updated_at: "2026-06-01T12:05:00Z",
      },
    ];

    const client = mockClient({ entries, total_count: 2 });
    render(<ScratchpadViewer client={client} agentId="test-agent" open={true} />);

    expect(screen.getByRole("dialog", { name: "Scratchpad for Test Agent" })).toBeInTheDocument();

    await waitFor(() => {
      expect(screen.getByText("Important pinned note")).toBeInTheDocument();
      expect(screen.getByText("First unpinned note")).toBeInTheDocument();
    });

    expect(screen.getByText("📌 Pinned")).toBeInTheDocument();
    expect(screen.getByText("2 entries")).toBeInTheDocument();

    // Verify pinned note is displayed before unpinned note in the DOM
    const articles = screen.getAllByRole("article");
    expect(articles).toHaveLength(2);
    expect(articles[0]).toHaveClass("pinned");
    expect(articles[0]).toHaveTextContent("Important pinned note");
    expect(articles[1]).not.toHaveClass("pinned");
    expect(articles[1]).toHaveTextContent("First unpinned note");
  });

  it("renders empty state when agent has no scratchpad entries", async () => {
    const client = mockClient({ entries: [], total_count: 0 });
    render(<ScratchpadViewer client={client} agentId="test-agent" open={true} />);

    await waitFor(() => {
      expect(
        screen.getByText("No scratchpad entries recorded for this agent.")
      ).toBeInTheDocument();
    });
  });

  it("refreshes entries when clicking the refresh button", async () => {
    const client = mockClient({ entries: [], total_count: 0 });
    render(<ScratchpadViewer client={client} agentId="test-agent" open={true} />);

    await waitFor(() => {
      expect(screen.getByText("No scratchpad entries recorded for this agent.")).toBeInTheDocument();
    });

    expect(client.scratchpadForAgent).toHaveBeenCalledTimes(1);

    const refreshBtn = screen.getByRole("button", { name: "Refresh scratchpad" });
    fireEvent.click(refreshBtn);

    await waitFor(() => {
      expect(client.scratchpadForAgent).toHaveBeenCalledTimes(2);
    });
  });

  it("calls onClose when close button is clicked", async () => {
    const onClose = vi.fn();
    const client = mockClient({ entries: [], total_count: 0 });
    render(<ScratchpadViewer client={client} agentId="test-agent" open={true} onClose={onClose} />);

    const closeBtn = screen.getByRole("button", { name: "Close scratchpad" });
    fireEvent.click(closeBtn);

    expect(onClose).toHaveBeenCalledOnce();
  });
});
