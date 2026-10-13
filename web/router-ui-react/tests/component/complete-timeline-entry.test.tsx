import "@testing-library/jest-dom/vitest";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it } from "vitest";
import { CompleteTimelineEntry, isMailboxSendToUser } from "../../src/components/CompleteTimelineEntry";
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
    [{ kind: "tool" as const, tool_name: "read", outcome: "active" as const }, "Running read", "fa-file-lines", "activity-entry--blue", true, true],
    [{ kind: "tool" as const, tool_name: "bash", outcome: "active" as const }, "Running bash", "fa-terminal", "activity-entry--teal", true, true],
    [{ kind: "tool" as const, tool_name: "functions.bash", outcome: "active" as const }, "Running bash", "fa-terminal", "activity-entry--teal", true, true],
    [{ kind: "tool" as const, tool_name: "bash", outcome: "success" as const }, "Ran bash", "fa-terminal", "activity-entry--green", false, true],
    [{ kind: "tool" as const, tool_name: "functions.bash", outcome: "success" as const }, "Ran bash", "fa-terminal", "activity-entry--green", false, true],
    [{ kind: "tool" as const, tool_name: "goal", outcome: "success" as const }, "Updated goal", "fa-flag", "activity-entry--green", false, false],
    [{ kind: "tool" as const, tool_name: "reminder", outcome: "success" as const }, "Scheduled reminder", "fa-clock", "activity-entry--green", false, false],
    [{ kind: "tool" as const, tool_name: "mecha_state", outcome: "success" as const }, "Ran mecha_state", "fa-wrench", "activity-entry--green", false, false],
    [{ kind: "tool" as const, outcome: "success" as const }, "Ran tool", "fa-wrench", "activity-entry--green", false, false],
    [{ kind: "tool" as const, tool_name: "read", outcome: "failed" as const, error: { code: "FAILED", retryable: false, detail_available: false } }, "Read failed", "fa-file-lines", "activity-entry--danger", false, true],
    [{ kind: "tool" as const, tool_name: "bash", outcome: "interrupted" as const, error: { code: "INTERRUPTED", retryable: false, detail_available: false } }, "Bash interrupted", "fa-terminal", "activity-entry--danger", false, true],
  ] as const)("renders %s with the canonical visual mapping", (presentation, label, icon, tone, active, isRoutine) => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: `complete:${label}`, agent_id: "managed-worker", at, message: "ignored presentation text", presentation }} />);
    if (isRoutine) {
      expect(screen.queryByText(label, { selector: "strong" })).not.toBeInTheDocument();
      expect(container.querySelector(".activity-entry--compact")).toBeInTheDocument();
    } else {
      expect(screen.getByText(label)).toBeInTheDocument();
    }
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

  it("renders the allowlisted too-small compaction result without a generic redaction notice", () => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: "complete:compaction-skipped", agent_id: "managed-worker", at, message: "Compaction skipped", presentation: { kind: "tool", tool_name: "mecha_context", outcome: "warning", summary: "Compaction skipped: session is too small.", error: { code: "CONTEXT_TOO_SMALL", retryable: false, detail_available: false } } }} />);

    expect(screen.getByText("Compaction skipped", { selector: "strong" })).toBeInTheDocument();
    expect(screen.getByText("Session is too small to compact.")).toBeInTheDocument();
    expect(screen.queryByText("No additional safe details available.")).not.toBeInTheDocument();
    expect(container.querySelector(".fa-compress")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--amber")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--terminal-danger")).not.toBeInTheDocument();
  });

  it("does not apply the safe compaction presentation to another tool", () => {
    render(<CompleteTimelineEntry entry={{ id: "complete:not-compaction", agent_id: "managed-worker", at, message: "Tool failed", presentation: { kind: "tool", tool_name: "bash", outcome: "warning", error: { code: "CONTEXT_TOO_SMALL", retryable: false, detail_available: false } } }} />);

    expect(screen.queryByText("Compaction skipped")).not.toBeInTheDocument();
    expect(screen.getByText("No additional safe details available.")).toBeInTheDocument();
  });

  it("keeps error failures recognizable and exposes no unsafe detail affordance", () => {
    const { container } = render(<CompleteTimelineEntry entry={{ id: "complete:failed", agent_id: "managed-worker", at, message: "Bash failed", presentation: { kind: "tool", tool_name: "bash", outcome: "error", error: { code: "PROCESS_EXITED", retryable: true, detail_available: false } } }} />);
    expect(container.querySelector(".activity-entry--compact")).toBeInTheDocument();
    expect(screen.queryByText("Bash failed", { selector: "strong" })).not.toBeInTheDocument();
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
    expect(card).toHaveTextContent("From ops-sender");
    expect(card).toHaveTextContent("Please inspect **the original report**.");
    expect(screen.queryByText(/acknowledged/i)).not.toBeInTheDocument();
    fireEvent.click(card);

    const dialog = screen.getByRole("dialog", { name: "Message received" });
    expect(within(dialog).getByText("ops-sender")).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Original message" })).toBeInTheDocument();
    expect(within(dialog).getByText("the original report")).toBeInTheDocument();
    expect(within(dialog).queryByText(/acknowledged/i)).not.toBeInTheDocument();
  });

  it("renders outbound reply message entry and opens detail dialog", () => {
    render(<CompleteTimelineEntry entry={{
      id: "complete:reply", agent_id: "managed-worker", at, message: "Reply sent",
      message_content: "Task completed successfully.", sender_identity: "managed-worker",
      presentation: { kind: "message", outcome: "success", from: "managed-worker", to: "pi-go" },
    }} />);

    const card = screen.getByRole("button", { name: /Reply sent.*To pi-go.*View details/i });
    expect(card).toBeInTheDocument();
    fireEvent.click(card);

    const dialog = screen.getByRole("dialog", { name: "Reply sent" });
    expect(within(dialog).getByText("pi-go")).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Message" })).toBeInTheDocument();
    expect(within(dialog).getByText("Task completed successfully.")).toBeInTheDocument();
  });

  it("renders mecha_mailbox_send tool card with message details", () => {
    render(<CompleteTimelineEntry entry={{
      id: "complete:mailbox-send", agent_id: "managed-worker", at, message: "Ran mecha_mailbox_send",
      tool_input: { recipient: "supervisor", body: "Status report: all clear" },
      presentation: { kind: "tool", tool_name: "mecha_mailbox_send", outcome: "success", to: "supervisor" },
    }} />);

    const card = screen.getByRole("button", { name: /Message sent.*To supervisor.*View details/i });
    expect(card).toBeInTheDocument();
    fireEvent.click(card);

    const dialog = screen.getByRole("dialog", { name: "Message sent" });
    expect(within(dialog).getByText("supervisor")).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Message" })).toBeInTheDocument();
    expect(within(dialog).getByText("Status report: all clear")).toBeInTheDocument();
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
    expect(within(dialog).getByRole("heading", { name: "Parameters" })).toBeInTheDocument();
    expect(within(dialog).getByText("path")).toBeInTheDocument();
    expect(within(dialog).getByText("/workspace/report.md")).toBeInTheDocument();
    expect(within(dialog).getByText("offset")).toBeInTheDocument();
    expect(within(dialog).getByText("12")).toBeInTheDocument();
    expect(within(dialog).getByText("options")).toBeInTheDocument();
    expect(within(dialog).getByText(/"hidden": false/)).toBeInTheDocument();
    expect(within(dialog).getByText("secret")).toBeInTheDocument();
    expect(within(dialog).getByText("[REDACTED]")).toBeInTheDocument();
  });

  it("opens tool call detail inspection with pointer button on click", () => {
    render(<CompleteTimelineEntry entry={{
      id: "complete:tool-click", agent_id: "managed-worker", at, message: "Ran bash", tool_input: { command: "ls -la" },
      message_content: "exit=0\nfile1.txt\nfile2.txt",
      presentation: { kind: "tool", tool_name: "bash", outcome: "success", summary: "Completed successfully" },
    }} />);

    const card = screen.getByRole("button", { name: /Ran bash.*View details/i });
    expect(card).toHaveAttribute("tabindex", "0");
    expect(card).toHaveAttribute("aria-haspopup", "dialog");
    fireEvent.click(card);

    const dialog = screen.getByRole("dialog", { name: "Ran bash" });
    expect(within(dialog).getByRole("heading", { name: "Command" })).toBeInTheDocument();
    expect(within(dialog).getByText("ls -la")).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Output" })).toBeInTheDocument();
    const outputPayload = within(dialog).getByRole("heading", { name: "Output" }).closest("section")?.querySelector("pre");
    expect(outputPayload?.textContent).toBe("file1.txt\nfile2.txt");
    expect(within(dialog).queryByText(/exit=0/)).not.toBeInTheDocument();
    expect(within(dialog).queryByText("Completed successfully")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("Event type")).not.toBeInTheDocument();
  });

  it("provides detail inspection for tool entry even without tool_input if detail is available", () => {
    render(<CompleteTimelineEntry entry={{
      id: "complete:tool-no-input", agent_id: "managed-worker", at, message: "Ran mecha_state",
      presentation: { kind: "tool", tool_name: "mecha_state", outcome: "success", summary: "State queried" },
    }} />);

    const card = screen.getByRole("button", { name: /Ran mecha_state.*View details/i });
    expect(card).toHaveAttribute("tabindex", "0");
    fireEvent.click(card);

    const dialog = screen.getByRole("dialog", { name: "Ran mecha_state" });
    expect(within(dialog).getByText("State queried")).toBeInTheDocument();
    expect(within(dialog).getAllByText("Ran mecha_state").length).toBeGreaterThanOrEqual(1);
  });

  it.each([
    { outcome: "success" as const, label: "Ran bash", detail: "Command completed", tone: "green" as const, terminalClass: "activity-entry--terminal-success", icon: "fa-terminal" },
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
    expect(terminalArticle).toHaveClass("activity-entry--compact");
    expect(screen.queryByText(label, { selector: "strong" })).not.toBeInTheDocument();
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
    if (terminalClass === "activity-entry--terminal-success") expect(terminalArticle.querySelector(".fa-diagram-project")).toBeInTheDocument();
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
    if (terminalClass === "activity-entry--terminal-success") expect(terminalArticle.querySelector(".fa-compress")).toBeInTheDocument();
    expect(screen.getByText(label)).toBeInTheDocument();
    expect(screen.getByText(detail)).toBeInTheDocument();
  });

  it("replaces an active tool card with its interrupted terminal state", () => {
    const entry = { id: "complete:replace", agent_id: "managed-worker", at, message: "ignored presentation text" };
    const { container, rerender } = render(<CompleteTimelineEntry entry={{ ...entry, presentation: { kind: "tool", tool_name: "read", outcome: "active" } }} />);
    rerender(<CompleteTimelineEntry entry={{ ...entry, presentation: { kind: "tool", tool_name: "read", outcome: "interrupted" } }} />);
    expect(container.querySelectorAll("article")).toHaveLength(1);
    expect(container.querySelector("[data-activity-id='complete:replace']")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--compact")).toBeInTheDocument();
    expect(screen.queryByText("Read interrupted", { selector: "strong" })).not.toBeInTheDocument();
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

  it("simplifies tool dialog to command and cleaned output without noise", () => {
    render(<CompleteTimelineEntry entry={{
      id: "complete:bash-proof", agent_id: "test", at, message: "Ran bash",
      tool_input: { command: "echo details-proof" },
      message_content: "exit=0\ndetails-proof",
      presentation: { kind: "tool", tool_name: "bash", outcome: "success", summary: "Completed successfully" },
    }} />);

    const card = screen.getByRole("button", { name: /Ran bash.*View details/i });
    fireEvent.click(card);

    const dialog = screen.getByRole("dialog", { name: "Ran bash" });
    expect(within(dialog).getByRole("heading", { name: "Command" })).toBeInTheDocument();
    expect(within(dialog).getByText("echo details-proof")).toBeInTheDocument();
    expect(within(dialog).getByRole("heading", { name: "Output" })).toBeInTheDocument();
    expect(within(dialog).getByText("details-proof")).toBeInTheDocument();
    expect(within(dialog).queryByText(/exit=0/)).not.toBeInTheDocument();
    expect(within(dialog).queryByText("Activity details")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("Event type")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("Outcome")).not.toBeInTheDocument();
    expect(within(dialog).queryByText("Completed successfully")).not.toBeInTheDocument();
  });

  it("renders inbound message with subject on top line and cleaned preview on second line", () => {
    const rawBody = `From: pi-go\nSubject: Fix memory leak\n\nIdentified leak in event listener.\nClosing socket properly resolves it.\n[Reply via mailbox to pi-go.]`;
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:msg-subject", agent_id: "managed-worker", at, message: "Message received",
      message_content: rawBody, sender_identity: "pi-go",
      presentation: { kind: "message", outcome: "success" },
    }} />);

    const label = container.querySelector(".activity-entry-label");
    expect(label?.querySelector("strong")).toHaveTextContent("From pi-go");
    expect(label?.querySelector(".activity-entry-subject")).toHaveTextContent("• Fix memory leak");
    const preview = container.querySelector(".activity-entry-preview");
    expect(preview).toHaveTextContent("Identified leak in event listener. Closing socket properly resolves it.");
  });

  it("renders outbound message with recipient and subject on top line and preview on second line", () => {
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:sent-subject", agent_id: "managed-worker", at, message: "Ran mecha_mailbox_send",
      tool_input: { recipient: "pi-go", subject: "Review diff", body: "Please check line 42 for\nnull pointer possibility." },
      presentation: { kind: "tool", tool_name: "mecha_mailbox_send", outcome: "success", to: "pi-go" },
    }} />);

    const label = container.querySelector(".activity-entry-label");
    expect(label?.querySelector("strong")).toHaveTextContent("To pi-go");
    expect(label?.querySelector(".activity-entry-subject")).toHaveTextContent("• Review diff");
    const preview = container.querySelector(".activity-entry-preview");
    expect(preview).toHaveTextContent("Please check line 42 for null pointer possibility.");
  });

  it("renders reminder card with subject on top line and body on second line", () => {
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:reminder-card", agent_id: "managed-worker", at, message: "Reminder set",
      message_content: "Follow up with user regarding pull request approval.",
      presentation: { kind: "reminder", outcome: "success", from: "cron" } as ManagedHistoryPresentation,
      tool_input: { subject: "PR follow-up" },
    }} />);

    const label = container.querySelector(".activity-entry-label");
    expect(label?.querySelector("strong")).toHaveTextContent("From cron");
    expect(label?.querySelector(".activity-entry-subject")).toHaveTextContent("• PR follow-up");
    const preview = container.querySelector(".activity-entry-preview");
    expect(preview).toHaveTextContent("Follow up with user regarding pull request approval.");
  });

  it("renders state transition as from -> to on second line", () => {
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:state-trans", agent_id: "managed-worker", at, message: "State changed",
      presentation: { kind: "state", from: "idle", to: "active", outcome: "success" },
    }} />);

    const label = container.querySelector(".activity-entry-label strong");
    expect(label).toHaveTextContent("State changed");
    const preview = container.querySelector(".activity-entry-preview");
    expect(preview).toHaveTextContent("idle → active");
  });

  it("renders routine tool card as a compact row with command preview and no header", () => {
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:bash-cmd", agent_id: "managed-worker", at, message: "Ran bash",
      tool_input: { command: "cargo test --all" },
      presentation: { kind: "tool", tool_name: "bash", outcome: "success" },
    }} />);

    expect(container.querySelector(".activity-entry--compact")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry-label strong")).not.toBeInTheDocument();
    const preview = container.querySelector(".activity-entry-preview");
    expect(preview).toHaveTextContent("cargo test --all");
  });

  it("renders thinking trace card with brain icon, purple styling, preview, and opens detail dialog on click", async () => {
    const user = userEvent.setup();
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:thinking-trace", agent_id: "managed-worker", at,
      presentation: { kind: "thinking", outcome: "success", thinking: "Analyzing the AST to find all call expressions." },
      thinking: "Analyzing the AST to find all call expressions.",
    }} />);

    const card = container.querySelector(".thinking-trace-card");
    expect(card).toBeInTheDocument();
    expect(container.querySelector(".fa-brain")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--purple")).toBeInTheDocument();
    expect(screen.getByText("Analyzing the AST to find all call expressions.")).toBeInTheDocument();

    // Click opens the reasoning trace dialog
    await user.click(card!);
    expect(screen.getByRole("heading", { level: 2, name: "Reasoning Trace" })).toBeInTheDocument();
    expect(screen.getAllByText("Analyzing the AST to find all call expressions.")).toHaveLength(2);
  });

  it("renders thinking trace card alongside assistant message when thinking content is present", async () => {
    const user = userEvent.setup();
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:assistant-with-thought", agent_id: "managed-worker", at, message: "Message sent",
      message_content: "Here is the result of the analysis.",
      presentation: { kind: "message", outcome: "success", from: "managed-worker", to: "supervisor" },
      thinking: "Step 1: Check inputs. Step 2: Validate. Step 3: Conclude.",
    }} />);

    // Both the thinking trace card and the message card are rendered
    expect(container.querySelector(".thinking-trace-card")).toBeInTheDocument();
    expect(container.querySelector(".fa-brain")).toBeInTheDocument();
    expect(screen.getByText("Step 1: Check inputs. Step 2: Validate. Step 3: Conclude.")).toBeInTheDocument();
    expect(screen.getByText("To supervisor")).toBeInTheDocument();

    // Clicking the thinking trace card opens the detail dialog
    await user.click(container.querySelector(".thinking-trace-card")!);
    expect(screen.getByRole("heading", { level: 2, name: "Reasoning Trace" })).toBeInTheDocument();
  });

  it("renders goal evaluation card with flag icon for NOT_DONE", async () => {
    const user = userEvent.setup();
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:goal-eval-not-done", agent_id: "managed-worker", at,
      message: "Goal Evaluation • NOT_DONE",
      message_content: "Continue working on the current goal.",
      presentation: { kind: "goal_evaluation", outcome: "not_done", summary: "Continue working on the current goal." },
    }} />);

    expect(container.querySelector(".fa-flag")).toBeInTheDocument();
    expect(screen.getByText("Goal Evaluation • NOT_DONE")).toBeInTheDocument();
    expect(screen.getByText("Continue working on the current goal.")).toBeInTheDocument();

    await user.click(container.querySelector(".activity-entry")!);
    expect(screen.getByRole("heading", { level: 2, name: "Goal Evaluation • NOT_DONE" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 3, name: "Goal Evaluation" })).toBeInTheDocument();
  });

  it("renders goal evaluation card with flag icon for DONE", () => {
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:goal-eval-done", agent_id: "managed-worker", at,
      message: "Goal Evaluation • DONE",
      message_content: "DONE",
      presentation: { kind: "goal_evaluation", outcome: "done", summary: "DONE" },
    }} />);

    expect(container.querySelector(".fa-flag")).toBeInTheDocument();
    expect(screen.getByText("Goal Evaluation • DONE")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--green")).toBeInTheDocument();
  });

  it("renders goal evaluation card with flag icon for COMPLETED and opens detail dialog", async () => {
    const user = userEvent.setup();
    const reason = "Directly looked up five distinct cookie recipes using Google search.";
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:goal-eval-completed", agent_id: "workflows", at,
      message: "Goal Evaluation • COMPLETED",
      message_content: reason,
      presentation: { kind: "goal_evaluation", outcome: "completed", summary: reason },
    }} />);

    expect(container.querySelector(".fa-flag")).toBeInTheDocument();
    expect(screen.getByText("Goal Evaluation • COMPLETED")).toBeInTheDocument();
    expect(screen.getByText(reason)).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--green")).toBeInTheDocument();

    await user.click(container.querySelector(".activity-entry")!);
    expect(screen.getByRole("heading", { level: 2, name: "Goal Evaluation • COMPLETED" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { level: 3, name: "Goal Evaluation" })).toBeInTheDocument();
  });

  it("renders workflow activation prompt with purple tone, bolt icon, and prompt preview", async () => {
    const user = userEvent.setup();
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:wf-activation", agent_id: "managed-worker", at,
      message: "Workflow activated",
      message_content: "[Workflow Activated: test-state]\n\nReport to the user what 3 * 3 is.",
      presentation: { kind: "message", outcome: "success", from: "system", to: "managed-worker", summary: "Workflow Activated: test-state" },
    }} />);

    expect(container.querySelector(".fa-bolt")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--purple")).toBeInTheDocument();
    expect(screen.getByText("Workflow Activated: test-state")).toBeInTheDocument();
    expect(screen.getByText("[Workflow Activated: test-state] Report to the user what 3 * 3 is.")).toBeInTheDocument();

    await user.click(container.querySelector(".activity-entry")!);
    expect(screen.getByRole("heading", { level: 2, name: "Workflow Activated: test-state" })).toBeInTheDocument();
  });

  it("recognizes assistant response addressed to 'user' via isMailboxSendToUser", () => {
    const entry: ManagedHistoryEntry = {
      id: "complete:assistant-reply",
      agent_id: "workflows",
      at,
      message: "Message sent",
      message_content: "3 * 3 is 9.",
      presentation: { kind: "message", outcome: "success", from: "workflows", to: "user" },
    };
    const result = isMailboxSendToUser(entry, "wolfgang");
    expect(result).toEqual({
      recipient: "user",
      body: "3 * 3 is 9.",
      from: "workflows",
    });
  });

  it("renders outbound assistant message addressed to reminders visibly as a compact card with detail dialog", async () => {
    const user = userEvent.setup();
    const millipedeReply = "Got the reminder!\n\nThe animal with the most appendages is the millipede ***Eumillipes persephone***, which has been recorded with up to **1,306 legs**.";
    const entry: ManagedHistoryEntry = {
      id: "complete:millipede-reply",
      agent_id: "workflows",
      at,
      message: "Message sent",
      message_content: millipedeReply,
      presentation: { kind: "message", outcome: "success", from: "workflows", to: "reminders" },
    };

    const { container } = render(<CompleteTimelineEntry entry={entry} />);

    expect(container.querySelector(".fa-paper-plane")).toBeInTheDocument();
    expect(screen.getByText("To reminders")).toBeInTheDocument();
    expect(screen.getByText(/The animal with the most appendages is the millipede/)).toBeInTheDocument();

    await user.click(container.querySelector(".activity-entry")!);
    expect(screen.getByRole("heading", { level: 2, name: "Message sent" })).toBeInTheDocument();
    expect(screen.getByText("reminders")).toBeInTheDocument();
  });

  it("renders subagent completed card with distinct icon, label, description preview, and expandable result dialog", async () => {
    const user = userEvent.setup();
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:subagent-done",
      agent_id: "managed-worker",
      at,
      message: "Subagent completed",
      message_content: "All 42 unit tests passed in 1.2s.",
      tool_input: {
        agent_id: "subagent-worker-123",
        description: "Run unit tests",
        status: "completed",
      },
      presentation: {
        kind: "subagent_complete",
        tool_name: "subagent",
        outcome: "completed",
        summary: "Subagent completed: Run unit tests",
        child_label: "subagent-worker-123",
        occurred_at: at,
      },
    }} />);

    expect(container.querySelector(".fa-circle-check")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--green")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--terminal-success")).toBeInTheDocument();
    expect(screen.getByText("Subagent completed")).toBeInTheDocument();
    expect(screen.getByText("Run unit tests (subagent-worker-123)")).toBeInTheDocument();

    await user.click(container.querySelector(".activity-entry")!);
    expect(screen.getByRole("dialog", { name: /Subagent completed/ })).toBeInTheDocument();
    expect(screen.getByText("All 42 unit tests passed in 1.2s.")).toBeInTheDocument();
  });

  it("renders subagent failed card with distinct icon, label, preview, and error dialog", async () => {
    const user = userEvent.setup();
    const { container } = render(<CompleteTimelineEntry entry={{
      id: "complete:subagent-fail",
      agent_id: "managed-worker",
      at,
      level: "error",
      message: "Subagent failed",
      message_content: "Process exited with code 1: command not found",
      tool_input: {
        agent_id: "subagent-worker-456",
        description: "Build binary",
        status: "error",
      },
      presentation: {
        kind: "subagent_complete",
        tool_name: "subagent",
        outcome: "failed",
        summary: "Subagent failed: Build binary",
        child_label: "subagent-worker-456",
        occurred_at: at,
      },
    }} />);

    expect(container.querySelector(".fa-circle-xmark")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--danger")).toBeInTheDocument();
    expect(container.querySelector(".activity-entry--terminal-danger")).toBeInTheDocument();
    expect(screen.getByText("Subagent failed")).toBeInTheDocument();
    expect(screen.getByText("Build binary (subagent-worker-456)")).toBeInTheDocument();

    await user.click(container.querySelector(".activity-entry")!);
    expect(screen.getByRole("dialog", { name: /Subagent failed/ })).toBeInTheDocument();
    expect(screen.getByText("Process exited with code 1: command not found")).toBeInTheDocument();
  });

  it("renders a clean, prominent visual divider for context reset by category and tool_name", () => {
    const { container: catContainer } = render(
      <CompleteTimelineEntry
        entry={{
          id: "complete:reset-1",
          agent_id: "managed-worker",
          at,
          category: "context_reset",
          message: "Context reset (Epoch 2)",
        }}
      />
    );

    const catDivider = catContainer.querySelector(".timeline-context-reset-divider");
    expect(catDivider).toBeInTheDocument();
    expect(catDivider).toHaveAttribute("role", "separator");
    expect(catDivider).toHaveAttribute("aria-label", "Context Reset (Epoch 2)");
    expect(catContainer.querySelector(".timeline-context-reset-label")?.textContent).toContain("─── Context Reset (Epoch 2) ───");

    const { container: toolContainer } = render(
      <CompleteTimelineEntry
        entry={{
          id: "complete:reset-2",
          agent_id: "managed-worker",
          at,
          presentation: {
            kind: "tool",
            tool_name: "context_reset",
            outcome: "success",
            summary: "Context reset to epoch 3",
          },
        }}
      />
    );

    const toolDivider = toolContainer.querySelector(".timeline-context-reset-divider");
    expect(toolDivider).toBeInTheDocument();
    expect(toolDivider).toHaveAttribute("role", "separator");
    expect(toolDivider).toHaveAttribute("aria-label", "Context Reset (Epoch 3)");
    expect(toolContainer.querySelector(".timeline-context-reset-label")?.textContent).toContain("─── Context Reset (Epoch 3) ───");
  });
});
