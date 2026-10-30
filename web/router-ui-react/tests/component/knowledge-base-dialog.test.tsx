import "fake-indexeddb/auto";
import "@testing-library/jest-dom/vitest";
import { afterEach, describe, it, expect, vi, beforeEach } from "vitest";
import { cleanup, render, screen, fireEvent, waitFor } from "@testing-library/react";
import { KnowledgeBaseDialog } from "../../src/components/KnowledgeBaseDialog";
import type { RouterHttpClient } from "../../src/transport/http-client";

afterEach(cleanup);

describe("KnowledgeBaseDialog", () => {
  let mockClient: Partial<RouterHttpClient>;

  beforeEach(() => {
    mockClient = {
      getGitSyncTarget: vi.fn().mockImplementation(async (target: string) => {
        if (target === "kb") {
          return { target: "kb", repo_url: "git@github.com:example/kb-docs.git", updated_at: "2025-01-01T00:00:00Z" };
        }
        return null;
      }),
      getKBFile: vi.fn().mockImplementation(async (path: string) => {
        if (path === "INDEX.md") {
          return "# Knowledge Base Index\n\n## Guides\n- [Architecture Guide](architecture-guide.md)\n";
        }
        if (path === "architecture-guide.md") {
          return "# Architecture Guide\n\nThis document describes system architecture.";
        }
        return "";
      }),
      setGitSyncTarget: vi.fn().mockResolvedValue({ target: "kb", repo_url: "git@github.com:example/kb-docs.git" }),
      clearGitSyncTarget: vi.fn().mockResolvedValue(undefined),
      pullGitSync: vi.fn().mockResolvedValue({ success: true }),
      pushGitSync: vi.fn().mockResolvedValue({ success: true }),
    };
  });

  it("renders Knowledge Base dialog shell and title when open=true", async () => {
    render(
      <KnowledgeBaseDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("Knowledge Base")).toBeInTheDocument();
  });

  it("does not render dialog content when open=false", () => {
    render(
      <KnowledgeBaseDialog
        open={false}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("loads configured Git repository URL", async () => {
    render(
      <KnowledgeBaseDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(mockClient.getGitSyncTarget).toHaveBeenCalledWith("kb");
    });

    const input = screen.getByPlaceholderText("GitHub repository URL") as HTMLInputElement;
    await waitFor(() => {
      expect(input.value).toBe("git@github.com:example/kb-docs.git");
    });
  });

  it("fetches and displays markdown content (INDEX.md)", async () => {
    render(
      <KnowledgeBaseDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(mockClient.getKBFile).toHaveBeenCalledWith("INDEX.md");
    });

    await waitFor(() => {
      expect(screen.getByText("Knowledge Base Index")).toBeInTheDocument();
      expect(screen.getByText("Guides")).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Architecture Guide" })).toBeInTheDocument();
    });

    // When on INDEX.md, "Back to Index" should not be visible
    expect(screen.queryByRole("button", { name: "Back to Index" })).not.toBeInTheDocument();
  });

  it("intercepts clicks on relative markdown links and navigates to the topic", async () => {
    render(
      <KnowledgeBaseDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Architecture Guide" })).toBeInTheDocument();
    });

    const link = screen.getByRole("link", { name: "Architecture Guide" });
    fireEvent.click(link);

    await waitFor(() => {
      expect(mockClient.getKBFile).toHaveBeenCalledWith("architecture-guide.md");
    });

    await waitFor(() => {
      expect(screen.getByText("This document describes system architecture.")).toBeInTheDocument();
    });
  });

  it("shows Back to Index when viewing a topic and navigates back", async () => {
    render(
      <KnowledgeBaseDialog
        open={true}
        client={mockClient as RouterHttpClient}
        onClose={vi.fn()}
      />
    );

    await waitFor(() => {
      expect(screen.getByRole("link", { name: "Architecture Guide" })).toBeInTheDocument();
    });

    // Navigate to topic
    fireEvent.click(screen.getByRole("link", { name: "Architecture Guide" }));

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Back to Index" })).toBeInTheDocument();
    });

    // Click "Back to Index"
    fireEvent.click(screen.getByRole("button", { name: "Back to Index" }));

    await waitFor(() => {
      expect(mockClient.getKBFile).toHaveBeenLastCalledWith("INDEX.md");
    });

    await waitFor(() => {
      expect(screen.getByText("Knowledge Base Index")).toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Back to Index" })).not.toBeInTheDocument();
    });
  });

  it("resolves relative paths and ignores anchor fragments", async () => {
    const { resolveRelativePath } = await import("../../src/components/KnowledgeBaseDialog");
    expect(resolveRelativePath("INDEX.md", "#heading")).toBeNull();
    expect(resolveRelativePath("INDEX.md", "operations/worker.md#details")).toBe("operations/worker.md");
    expect(resolveRelativePath("operations/worker.md", "../architecture.md")).toBe("architecture.md");
    expect(resolveRelativePath("operations/sub/worker.md", "./other.md")).toBe("operations/sub/other.md");
  });
});
