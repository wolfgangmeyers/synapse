import { describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach } from "vitest";
import "@testing-library/jest-dom/vitest";
import { ArrivalDialog } from "../../src/components/ArrivalDialog";
import type { Arrival } from "../../src/domain/arrivals";

afterEach(cleanup);

const arrival: Arrival = { agent: "agent-one", from: "worker_bot", subject: "Update", sourceKind: "agent", preview: "done", body: "**Done**\n<script>alert(1)</script>", createdAt: "2025-01-01T00:00:00.000Z", expiresAtMs: Date.now() + 60_000 };

describe("ArrivalDialog", () => {
  it("renders safe metadata and Markdown body, then dismisses", () => { const onClose = vi.fn(); const onDismiss = vi.fn(); render(<ArrivalDialog open arrival={arrival} uiAgentId="web" onClose={onClose} onDismiss={onDismiss} />); expect(screen.getByText("Worker Bot")).toBeInTheDocument(); expect(screen.getByText("Done")).toBeInTheDocument(); expect(screen.getByText("<script>alert(1)</script>")).toBeInTheDocument(); screen.getByRole("button", { name: "Close message" }).click(); expect(onDismiss).toHaveBeenCalledWith("agent-one"); expect(onClose).toHaveBeenCalled(); });
  it("does not render the UI agent arrival", () => { render(<ArrivalDialog open arrival={arrival} uiAgentId="agent-one" onClose={vi.fn()} />); expect(screen.queryByRole("dialog")).not.toBeInTheDocument(); });
});
