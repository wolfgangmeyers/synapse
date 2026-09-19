import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
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
    [{ kind: "tool" as const, tool_name: "bash", outcome: "success" as const }, "Ran bash", "fa-check", "activity-entry--green", false],
    [{ kind: "tool" as const, tool_name: "functions.bash", outcome: "success" as const }, "Ran bash", "fa-check", "activity-entry--green", false],
    [{ kind: "tool" as const, tool_name: "goal", outcome: "success" as const }, "Updated goal", "fa-check", "activity-entry--green", false],
    [{ kind: "tool" as const, tool_name: "reminder", outcome: "success" as const }, "Scheduled reminder", "fa-check", "activity-entry--green", false],
    [{ kind: "tool" as const, tool_name: "mecha_state", outcome: "success" as const }, "Ran mecha_state", "fa-check", "activity-entry--green", false],
    [{ kind: "tool" as const, outcome: "success" as const }, "Ran tool", "fa-check", "activity-entry--green", false],
    [{ kind: "tool" as const, tool_name: "read", outcome: "failed" as const, error: { code: "FAILED", retryable: false, detail_available: false } }, "Read failed", "fa-wrench", "activity-entry--danger", false],
    [{ kind: "tool" as const, tool_name: "bash", outcome: "interrupted" as const, error: { code: "INTERRUPTED", retryable: false, detail_available: false } }, "Bash interrupted", "fa-terminal", "activity-entry--danger", false],
  ] as const)("renders %s with the canonical visual mapping", (presentation, label, icon, tone, active) => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: `complete:${label}`, agent_id: "managed-worker", at, message: "ignored presentation text", presentation }} />);
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(container.querySelector(`.${icon}`)).toBeInTheDocument();
    expect(container.querySelector(`.${tone}`)).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--active")).toBe(active ? container.querySelector("article") : null);
    if (presentation.outcome === "success") expect(container.querySelector(".activity-entry--terminal-success")).toBeInTheDocument();
    if (presentation.outcome === "failed" || presentation.outcome === "interrupted") {
      expect(container.querySelector(".activity-entry--terminal-danger")).toBeInTheDocument();
      expect(screen.getByText("No additional safe details available.")).toBeInTheDocument();
    }
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
    expect(container.querySelector(".activity-entry--terminal-danger")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--active")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /details/i })).not.toBeInTheDocument();
  });

  it("opens retained inbound message details by click without acknowledgment copy", () => {
    render(<CompleteTimelineEntry entry={{
      id: "complete:message", agent_id: "managed-worker", at, message: "Message acknowledged",
      message_content: "Please inspect **the original report**.", sender_identity: "ops-sender",
      presentation: { kind: "message", outcome: "success", summary: "Inbound message acknowledged" },
    }} />);

    const card = screen.getByRole("button", { name: /Message received.*From ops-sender.*View details/i });
    expect(card).toHaveAttribute("aria-haspopup", "dialog");
    expect(card).toHaveTextContent("Message received");
    expect(screen.queryByText(/acknowledged/i)).not.toBeInTheDocument();
    fireEvent.click(card);

    const dialog = screen.getByRole("dialog", { name: "Message received" });
    expect(within(dialog).getByText("ops-sender")).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Original message" })).toBeInTheDocument();
    expect(within(dialog).getByText("the original report")).toBeInTheDocument();
    expect(within(dialog).queryByText(/acknowledged/i)).not.toBeInTheDocument();
  });

  it("opens readable retained tool parameters from the keyboard", () => {
    render(<CompleteTimelineEntry entry={{
      id: "complete:tool-input", agent_id: "managed-worker", at, message: "Ran read", tool_input: { path: "/workspace/report.md", offset: 12, options: { hidden: false }, secret: "[REDACTED]" },
      presentation: { kind: "tool", tool_name: "read", outcome: "success" },
    }} />);

    const card = screen.getByRole("button", { name: /Ran read.*View details/i });
    card.focus();
    fireEvent.keyDown(card, { key: "Enter" });

    const dialog = screen.getByRole("dialog", { name: "Ran read" });
    const parameters = within(dialog).getByText(/"path": "\/workspace\/report\.md"/);
    expect(within(dialog).getByRole("heading", { name: "Tool parameters" })).toBeInTheDocument();
    expect(parameters).toHaveTextContent('"offset": 12');
    expect(parameters).toHaveTextContent('"hidden": false');
    expect(parameters).toHaveTextContent('"secret": "[REDACTED]"');
  });

  it.each([
    { outcome: "success" as const, label: "Ran bash", detail: "Command completed", tone: "green" as const, terminalClass: "activity-entry--terminal-success", icon: "fa-check" },
    { outcome: "error" as const, label: "Bash failed", detail: "Command failed", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger", icon: "fa-terminal", error: { code: "PROCESS_EXITED", retryable: true, detail_available: false } },
    { outcome: "interrupted" as const, label: "Bash interrupted", detail: "Stopped by user", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger", icon: "fa-terminal", error: { code: "INTERRUPTED", retryable: false, detail_available: false } },
  ])("updates one builtin bash card in place for $outcome", ({ outcome, label, detail, tone, terminalClass, icon, error }) => {
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
    expect(terminalArticle).toHaveClass(terminalClass);
    expect(terminalArticle).not.toHaveClass("activity-entry--active");
    expect(terminalArticle.querySelector(`.${icon}`)).toBeInTheDocument();
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(detail)).toBeInTheDocument();
  });

  it.each([
    { initial: "queued" as const, terminal: "completed" as const, outcome: "completed" as const, state: "Completed", tone: "green" as const, terminalClass: "activity-entry--terminal-success" },
    { initial: "running" as const, terminal: "completed" as const, outcome: "completed" as const, state: "Completed", tone: "green" as const, terminalClass: "activity-entry--terminal-success" },
    { initial: "running" as const, terminal: "aborted" as const, outcome: "aborted" as const, state: "Stopped", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger" },
    { initial: "running" as const, terminal: "stopped" as const, outcome: "unknown" as const, state: "Stopped", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger" },
    { initial: "running" as const, terminal: "error" as const, outcome: "error" as const, state: "Failed", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger" },
    { initial: "running" as const, terminal: "interrupted_unknown" as const, outcome: "unknown" as const, state: "Interrupted — outcome unknown", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger" },
  ])("updates one native subagent card from $initial to $terminal", ({ initial, terminal, outcome, state, tone, terminalClass }) => {
    const id = "complete:subagent";
    const detail = "Native child · Native child finished";
    const { container, rerender } = render(<CompleteTimelineEntry entry={activityEntry(id, { kind: "child", child_state: initial, outcome: "active" })} />);
    const initialArticle = activityArticle(container);

    expect(initialArticle).toHaveAttribute("data-activity-id", id);
    expect(initialArticle).toHaveClass("activity-entry--blue", "activity-entry--active");

    rerender(<CompleteTimelineEntry entry={activityEntry(id, { kind: "child", child_state: terminal, outcome, child_label: "Native child", summary: "Native child finished" })} />);

    const terminalArticle = activityArticle(container);
    expect(terminalArticle).toBe(initialArticle);
    expect(container.querySelectorAll("article")).toHaveLength(1);
    expect(terminalArticle).toHaveAttribute("data-activity-id", id);
    expect(terminalArticle).toHaveClass(`activity-entry--${tone}`, terminalClass);
    expect(terminalArticle).not.toHaveClass("activity-entry--active");
    if (terminalClass === "activity-entry--terminal-success") expect(terminalArticle.querySelector(".fa-check")).toBeInTheDocument();
    expect(terminalArticle.querySelector(".activity-entry-label strong")).toHaveTextContent("Subagent");
    expect(terminalArticle).toHaveTextContent(state);
    expect(screen.getByText(`${state} · ${detail}`)).toBeInTheDocument();
  });

  it("settles a completed subagent when a delayed lifecycle state still says running", () => {
    const { container } = render(<CompleteTimelineEntry entry={activityEntry("complete:subagent-outcome", { kind: "child", child_state: "running", outcome: "completed", child_label: "Worker", summary: "Returned successfully" })} />);
    const article = activityArticle(container);

    expect(article.querySelector(".activity-entry-label strong")).toHaveTextContent("Subagent");
    expect(article).toHaveClass("activity-entry--terminal-success");
    expect(article).not.toHaveClass("activity-entry--active");
    expect(article).toHaveTextContent("Completed · Worker · Returned successfully");
  });

  it.each([
    { outcome: "completed" as const, label: "Context compacted", tone: "green" as const, terminalClass: "activity-entry--terminal-success" },
    { outcome: "failed" as const, label: "Context compaction failed", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger" },
    { outcome: "interrupted" as const, label: "Context compaction interrupted — outcome unknown", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger" },
    { outcome: "aborted" as const, label: "Context compaction interrupted — outcome unknown", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger" },
    { outcome: "reconciled_unknown" as const, label: "Context compaction interrupted — outcome unknown", tone: "danger" as const, terminalClass: "activity-entry--terminal-danger" },
  ])("updates one parent compaction card to $outcome", ({ outcome, label, tone, terminalClass }) => {
    const id = "complete:compaction";
    const detail = "Compaction finished";
    const { container, rerender } = render(<CompleteTimelineEntry entry={activityEntry(id, { kind: "compaction", outcome: "compacting" })} />);
    const initialArticle = activityArticle(container);

    expect(initialArticle).toHaveAttribute("data-activity-id", id);
    expect(initialArticle).toHaveClass("activity-entry--purple", "activity-entry--active");

    rerender(<CompleteTimelineEntry entry={activityEntry(id, { kind: "compaction", outcome, summary: detail })} />);

    const terminalArticle = activityArticle(container);
    expect(terminalArticle).toBe(initialArticle);
    expect(container.querySelectorAll("article")).toHaveLength(1);
    expect(terminalArticle).toHaveAttribute("data-activity-id", id);
    expect(terminalArticle).toHaveClass(`activity-entry--${tone}`, terminalClass);
    expect(terminalArticle).not.toHaveClass("activity-entry--active");
    if (terminalClass === "activity-entry--terminal-success") expect(terminalArticle.querySelector(".fa-check")).toBeInTheDocument();
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
    expect(screen.getAllByText("Subagent")).toHaveLength(2);
    expect(screen.getByText("Interrupted — outcome unknown")).toBeInTheDocument();
    expect(screen.getByText("Completed · Delay fifteen seconds · Wrapped up in 15 seconds")).toBeInTheDocument();
    expect(screen.getByText("Child compaction failed")).toBeInTheDocument();
    expect(container.querySelector(".fa-arrow-right")).not.toBeInTheDocument();
    expect(container.querySelector(".fa-compress")).toBeInTheDocument();
    expect(container.querySelector(".fa-rotate")).toBeInTheDocument();
  });
});
