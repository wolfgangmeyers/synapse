import "@testing-library/jest-dom/vitest";
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { ManagedChildList } from "../../src/components/ManagedChildList";

const children = [{ id: "child-1", parent_entry_id: "entry-1", task_id: "task-1", run_id: "run-1", sequence: 1, recovery_state: "current" as const, description: "Prepare a long but bounded report", model: { provider: "openai-codex", id: "gpt-5.6-luna", name: "GPT-5.6 Luna" }, status: "running" as const, compaction_state: "compacting" as const, compaction: { state: "compacting" as const, count: 1 }, duration_ms: 3000 }, { id: "child-2", parent_entry_id: "entry-2", task_id: "task-2", run_id: "run-2", sequence: 2, recovery_state: "current" as const, status: "unknown" as const, compaction_state: "unknown" as const, compaction: { state: "idle" as const, count: 0 } }];

describe("ManagedChildList", () => {
  it("renders structured child status, model, and runtime-provided timing", () => {
    render(<ManagedChildList agentId="managed-alpha" children={children} />);
    expect(screen.getByRole("list", { name: "Children for managed-alpha" })).toBeInTheDocument();
    expect(screen.getByText("Prepare a long but bounded report")).toBeInTheDocument();
    expect(screen.getByText("GPT-5.6 Luna")).toBeInTheDocument();
    expect(screen.getByText("compacting")).toBeInTheDocument();
    expect(screen.getByText("3s")).toBeInTheDocument();
    expect(screen.getByText("unknown")).toBeInTheDocument();
  });
});
