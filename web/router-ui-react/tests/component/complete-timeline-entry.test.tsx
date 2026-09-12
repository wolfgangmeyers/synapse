import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { CompleteTimelineEntry } from "../../src/components/CompleteTimelineEntry";

const at = "2030-01-01T10:14:00.000Z";

afterEach(cleanup);

describe("Complete timeline entry", () => {
  it.each([
    [{ kind: "tool" as const, tool_name: "bash", outcome: "success" as const }, "Ran bash", "fa-terminal", "activity-entry--teal"],
    [{ kind: "tool" as const, tool_name: "functions.bash", outcome: "success" as const }, "Ran bash", "fa-terminal", "activity-entry--teal"],
    [{ kind: "tool" as const, tool_name: "goal", outcome: "success" as const }, "Updated goal", "fa-flag", "activity-entry--purple"],
    [{ kind: "tool" as const, tool_name: "reminder", outcome: "success" as const }, "Scheduled reminder", "fa-clock", "activity-entry--yellow"],
    [{ kind: "tool" as const, tool_name: "mecha_state", outcome: "success" as const }, "Ran mecha_state", "fa-wrench", "activity-entry--blue"],
    [{ kind: "tool" as const, outcome: "success" as const }, "Ran tool", "fa-wrench", "activity-entry--blue"],
  ] as const)("renders %s with the canonical visual mapping", (presentation, label, icon, tone) => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: `complete:${label}`, agent_id: "managed-worker", at, message: "ignored presentation text", presentation }} />);
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(container.querySelector(`.${icon}`)).toBeInTheDocument();
    expect(container.querySelector(`.${tone}`)).toBeInTheDocument();
  });

  it("renders an old unstructured payload as a named activity, not the generic legacy card", () => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: "complete:legacy", agent_id: "managed-worker", at, message: "Legacy activity" }} />);
    expect(screen.getByText("Legacy activity")).toBeInTheDocument();
    expect(screen.queryByText("Agent activity")).not.toBeInTheDocument();
    expect(container.querySelector("[data-event-kind='message']")).toBeInTheDocument();
  });

  it("keeps a failed tool recognizable and exposes no unsafe detail affordance", () => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: "complete:failed", agent_id: "managed-worker", at, message: "Bash failed", presentation: { kind: "tool", tool_name: "bash", outcome: "error", error: { code: "PROCESS_EXITED", retryable: true, detail_available: false } } }} />);
    expect(screen.getByText("Bash failed")).toBeInTheDocument();
    expect(screen.getByText("The operation could not be completed.")).toBeInTheDocument();
    expect(screen.getByText("No additional safe details available.")).toBeInTheDocument();
    expect(container.querySelector(".fa-terminal")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--danger")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /details/i })).not.toBeInTheDocument();
  });

  it("renders compaction, recovery, and child unknown outcomes as distinct facts", () => {
    const { container } = render(<>
      <CompleteTimelineEntry entry={{ id: "complete:compaction", agent_id: "managed-worker", at, message: "Context compacted", presentation: { kind: "compaction", outcome: "completed" } }} />
      <CompleteTimelineEntry entry={{ id: "complete:child", agent_id: "managed-worker", at, message: "Child interrupted — outcome unknown", presentation: { kind: "recovery", child_state: "interrupted_unknown", outcome: "unknown" } }} />
      <CompleteTimelineEntry entry={{ id: "complete:child-compaction", agent_id: "managed-worker", at, message: "Child compaction failed", presentation: { kind: "compaction", child_label: "Child", outcome: "failed" } }} />
    </>);
    expect(screen.getByText("Context compacted")).toBeInTheDocument();
    expect(screen.getByText("Subagent interrupted — outcome unknown")).toBeInTheDocument();
    expect(screen.getByText("Child compaction failed")).toBeInTheDocument();
    expect(container.querySelector(".fa-arrow-right")).not.toBeInTheDocument();
    expect(container.querySelector(".fa-compress")).toBeInTheDocument();
    expect(container.querySelector(".fa-rotate")).toBeInTheDocument();
  });
});
