import "@testing-library/jest-dom/vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { CompleteTimelineEntry } from "../../src/components/CompleteTimelineEntry";
import type { ManagedHistoryEntry, ManagedHistoryPresentation } from "../../src/protocol/types";

const at = "2030-01-01T10:14:00.000Z";

function activityEntry(id: string, presentation: ManagedHistoryPresentation, message = "ignored presentation text"): ManagedHistoryEntry {
  return { id, agent_id: "managed-worker", at, message, presentation };
}

function activityArticle(container: HTMLElement): HTMLElement {
  const article = container.querySelector<HTMLElement>("article");
  if (!article) throw new Error("Activity article was not rendered");
  return article;
}

afterEach(cleanup);

describe("Complete timeline entry", () => {
  it.each([
    [{ kind: "tool" as const, tool_name: "read", outcome: "active" as const }, "Running read", "fa-wrench", "activity-entry--blue", true],
    [{ kind: "tool" as const, tool_name: "bash", outcome: "active" as const }, "Running bash", "fa-terminal", "activity-entry--teal", true],
    [{ kind: "tool" as const, tool_name: "functions.bash", outcome: "active" as const }, "Running bash", "fa-terminal", "activity-entry--teal", true],
    [{ kind: "tool" as const, tool_name: "bash", outcome: "success" as const }, "Ran bash", "fa-terminal", "activity-entry--teal", false],
    [{ kind: "tool" as const, tool_name: "functions.bash", outcome: "success" as const }, "Ran bash", "fa-terminal", "activity-entry--teal", false],
    [{ kind: "tool" as const, tool_name: "goal", outcome: "success" as const }, "Updated goal", "fa-flag", "activity-entry--purple", false],
    [{ kind: "tool" as const, tool_name: "reminder", outcome: "success" as const }, "Scheduled reminder", "fa-clock", "activity-entry--yellow", false],
    [{ kind: "tool" as const, tool_name: "mecha_state", outcome: "success" as const }, "Ran mecha_state", "fa-wrench", "activity-entry--blue", false],
    [{ kind: "tool" as const, outcome: "success" as const }, "Ran tool", "fa-wrench", "activity-entry--blue", false],
    [{ kind: "tool" as const, tool_name: "read", outcome: "failed" as const, error: { code: "FAILED", retryable: false, detail_available: false } }, "Read failed", "fa-wrench", "activity-entry--danger", false],
    [{ kind: "tool" as const, tool_name: "bash", outcome: "interrupted" as const, error: { code: "INTERRUPTED", retryable: false, detail_available: false } }, "Bash interrupted", "fa-terminal", "activity-entry--danger", false],
  ] as const)("renders %s with the canonical visual mapping", (presentation, label, icon, tone, active) => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: `complete:${label}`, agent_id: "managed-worker", at, message: "ignored presentation text", presentation }} />);
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(container.querySelector(`.${icon}`)).toBeInTheDocument();
    expect(container.querySelector(`.${tone}`)).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--active")).toBe(active ? container.querySelector("article") : null);
    if (presentation.outcome === "failed" || presentation.outcome === "interrupted") expect(screen.getByText("No additional safe details available.")).toBeInTheDocument();
  });

  it("renders an old unstructured payload as a named activity, not the generic legacy card", () => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: "complete:legacy", agent_id: "managed-worker", at, message: "Legacy activity" }} />);
    expect(screen.getByText("Legacy activity")).toBeInTheDocument();
    expect(screen.queryByText("Agent activity")).not.toBeInTheDocument();
    expect(container.querySelector("[data-event-kind='message']")).toBeInTheDocument();
  });

  it("keeps error failures recognizable and exposes no unsafe detail affordance", () => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: "complete:failed", agent_id: "managed-worker", at, message: "Bash failed", presentation: { kind: "tool", tool_name: "bash", outcome: "error", error: { code: "PROCESS_EXITED", retryable: true, detail_available: false } } }} />);
    expect(screen.getByText("Bash failed")).toBeInTheDocument();
    expect(screen.getByText("The operation could not be completed.")).toBeInTheDocument();
    expect(screen.getByText("No additional safe details available.")).toBeInTheDocument();
    expect(container.querySelector(".fa-terminal")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--danger")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--active")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /details/i })).not.toBeInTheDocument();
  });

  it.each([
    { outcome: "success" as const, label: "Ran bash", detail: "Command completed", tone: "teal" as const },
    { outcome: "error" as const, label: "Bash failed", detail: "Command failed", tone: "danger" as const, error: { code: "PROCESS_EXITED", retryable: true, detail_available: false } },
    { outcome: "interrupted" as const, label: "Bash interrupted", detail: "Stopped by user", tone: "danger" as const, error: { code: "INTERRUPTED", retryable: false, detail_available: false } },
  ])("updates one builtin bash card in place for $outcome", ({ outcome, label, detail, tone, error }) => {
    const id = "complete:bash";
    const { container, rerender } = render(<CompleteTimelineEntry entry={activityEntry(id, { kind: "tool", tool_name: "bash", outcome: "active" })} />);
    const initialArticle = activityArticle(container);

    expect(container.querySelectorAll("article")).toHaveLength(1);
    expect(initialArticle).toHaveAttribute("data-activity-id", id);
    expect(initialArticle).toHaveClass("activity-entry--teal", "activity-entry--active");

    rerender(<CompleteTimelineEntry entry={activityEntry(id, { kind: "tool", tool_name: "bash", outcome, summary: detail, ...(error === undefined ? {} : { error }) })} />);

    const terminalArticle = activityArticle(container);
    expect(terminalArticle).toBe(initialArticle);
    expect(container.querySelectorAll("article")).toHaveLength(1);
    expect(terminalArticle).toHaveAttribute("data-activity-id", id);
    expect(terminalArticle).toHaveClass(`activity-entry--${tone}`);
    expect(terminalArticle).not.toHaveClass("activity-entry--active");
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(detail)).toBeInTheDocument();
  });

  it.each([
    { initial: "queued" as const, terminal: "completed" as const, outcome: "completed" as const, label: "Subagent finished", tone: "blue" as const },
    { initial: "running" as const, terminal: "completed" as const, outcome: "completed" as const, label: "Subagent finished", tone: "blue" as const },
    { initial: "running" as const, terminal: "aborted" as const, outcome: "aborted" as const, label: "Subagent stopped", tone: "blue" as const },
    { initial: "running" as const, terminal: "error" as const, outcome: "error" as const, label: "Subagent failed", tone: "danger" as const },
    { initial: "running" as const, terminal: "interrupted_unknown" as const, outcome: "unknown" as const, label: "Subagent interrupted — outcome unknown", tone: "amber" as const },
  ])("updates one native subagent card from $initial to $terminal", ({ initial, terminal, outcome, label, tone }) => {
    const id = "complete:subagent";
    const detail = "Native child · Native child finished";
    const { container, rerender } = render(<CompleteTimelineEntry entry={activityEntry(id, { kind: "child", child_state: initial, outcome: "active" })} />);
    const initialArticle = activityArticle(container);

    expect(initialArticle).toHaveAttribute("data-activity-id", id);
    expect(initialArticle).toHaveClass("activity-entry--blue");
    expect(initialArticle).not.toHaveClass("activity-entry--active");

    rerender(<CompleteTimelineEntry entry={activityEntry(id, { kind: "child", child_state: terminal, outcome, child_label: "Native child", summary: "Native child finished" })} />);

    const terminalArticle = activityArticle(container);
    expect(terminalArticle).toBe(initialArticle);
    expect(container.querySelectorAll("article")).toHaveLength(1);
    expect(terminalArticle).toHaveAttribute("data-activity-id", id);
    expect(terminalArticle).toHaveClass(`activity-entry--${tone}`);
    expect(terminalArticle).not.toHaveClass("activity-entry--active");
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(detail)).toBeInTheDocument();
  });

  it.each([
    { outcome: "completed" as const, label: "Context compacted", tone: "purple" as const },
    { outcome: "failed" as const, label: "Context compaction failed", tone: "danger" as const },
    { outcome: "aborted" as const, label: "Context compaction interrupted — outcome unknown", tone: "amber" as const },
    { outcome: "reconciled_unknown" as const, label: "Context compaction interrupted — outcome unknown", tone: "amber" as const },
  ])("updates one parent compaction card to $outcome", ({ outcome, label, tone }) => {
    const id = "complete:compaction";
    const detail = "Compaction finished";
    const { container, rerender } = render(<CompleteTimelineEntry entry={activityEntry(id, { kind: "compaction", outcome: "compacting" })} />);
    const initialArticle = activityArticle(container);

    expect(initialArticle).toHaveAttribute("data-activity-id", id);
    expect(initialArticle).toHaveClass("activity-entry--purple");
    expect(initialArticle).not.toHaveClass("activity-entry--active");

    rerender(<CompleteTimelineEntry entry={activityEntry(id, { kind: "compaction", outcome, summary: detail })} />);

    const terminalArticle = activityArticle(container);
    expect(terminalArticle).toBe(initialArticle);
    expect(container.querySelectorAll("article")).toHaveLength(1);
    expect(terminalArticle).toHaveAttribute("data-activity-id", id);
    expect(terminalArticle).toHaveClass(`activity-entry--${tone}`);
    expect(terminalArticle).not.toHaveClass("activity-entry--active");
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(detail)).toBeInTheDocument();
  });

  it("replaces an active tool card with its interrupted terminal state", () => {
    const entry = { id: "complete:replace", agent_id: "managed-worker", at, message: "ignored presentation text" };
    const { container, rerender } = render(<CompleteTimelineEntry entry={{ ...entry, presentation: { kind: "tool", tool_name: "read", outcome: "active" } }} />);
    rerender(<CompleteTimelineEntry entry={{ ...entry, presentation: { kind: "tool", tool_name: "read", outcome: "interrupted" } }} />);
    expect(container.querySelectorAll("article")).toHaveLength(1);
    expect(container.querySelector("[data-activity-id='complete:replace']")).toBeInTheDocument();
    expect(screen.getByText("Read interrupted")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--danger")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--active")).not.toBeInTheDocument();
  });

  it("renders compaction, recovery, and terminal child outcomes as distinct facts", () => {
    const { container } = render(<>
      <CompleteTimelineEntry entry={{ id: "complete:compaction", agent_id: "managed-worker", at, message: "Context compacted", presentation: { kind: "compaction", outcome: "completed" } }} />
      <CompleteTimelineEntry entry={{ id: "complete:child", agent_id: "managed-worker", at, message: "Child interrupted — outcome unknown", presentation: { kind: "recovery", child_state: "interrupted_unknown", outcome: "unknown" } }} />
      <CompleteTimelineEntry entry={{ id: "complete:steered", agent_id: "managed-worker", at, message: "Child wrapped up", presentation: { kind: "child", child_state: "steered", child_label: "Delay fifteen seconds", summary: "Wrapped up in 15 seconds" } }} />
      <CompleteTimelineEntry entry={{ id: "complete:child-compaction", agent_id: "managed-worker", at, message: "Child compaction failed", presentation: { kind: "compaction", child_label: "Child", outcome: "failed" } }} />
    </>);
    expect(screen.getByText("Context compacted")).toBeInTheDocument();
    expect(screen.getByText("Subagent interrupted — outcome unknown")).toBeInTheDocument();
    expect(screen.getByText("Subagent wrapped up")).toBeInTheDocument();
    expect(screen.getByText("Delay fifteen seconds · Wrapped up in 15 seconds")).toBeInTheDocument();
    expect(screen.getByText("Child compaction failed")).toBeInTheDocument();
    expect(container.querySelector(".fa-arrow-right")).not.toBeInTheDocument();
    expect(container.querySelector(".fa-compress")).toBeInTheDocument();
    expect(container.querySelector(".fa-rotate")).toBeInTheDocument();
  });
});
