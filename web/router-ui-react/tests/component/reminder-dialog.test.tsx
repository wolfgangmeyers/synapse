import { describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach } from "vitest";
import "@testing-library/jest-dom/vitest";
import userEvent from "@testing-library/user-event";
import { ReminderDialog } from "../../src/components/ReminderDialog";
import type { Reminder } from "../../src/domain/reminders";
import type { PromptTemplate } from "../../src/domain/prompt-templates";

afterEach(cleanup);

const reminder: Reminder = { id: "r1", target: "agent-one", creator: "web", body: "Check this", subject: "Check", state: "scheduled", enabled: true, next_due_at: new Date(Date.now() + 60_000).toISOString(), interval_ms: null };
const base = { open: true, peer: "agent-one", creator: "web", reminders: [reminder], onClose: vi.fn(), onCreate: vi.fn(), onUpdate: vi.fn(), onDelete: vi.fn(), onLock: vi.fn() };

describe("ReminderDialog", () => {
  it("lists countdown, recurrence, and supports lock and delete", async () => { const user = userEvent.setup(); const onLock = vi.fn(); const onDelete = vi.fn(); render(<ReminderDialog {...base} onLock={onLock} onDelete={onDelete} />); expect(screen.getByText("One-shot")).toBeInTheDocument(); await user.click(screen.getByRole("button", { name: "Lock" })); expect(onLock).toHaveBeenCalledWith(reminder, "set"); await user.click(screen.getByRole("button", { name: "Delete" })); expect(onDelete).toHaveBeenCalledWith("r1"); });
  it("preserves edit drafts through an equivalent reminder-list refresh", async () => { const user = userEvent.setup(); const { rerender } = render(<ReminderDialog {...base} />); await user.click(screen.getByRole("button", { name: "Edit" })); const subject = screen.getByRole("textbox", { name: "Subject" }); const body = screen.getByRole("textbox", { name: "Reminder" }); await user.clear(subject); await user.type(subject, "Local subject"); await user.clear(body); await user.type(body, "Local body"); rerender(<ReminderDialog {...base} reminders={[{ ...reminder }]} />); expect(screen.getByRole("textbox", { name: "Subject" })).toHaveValue("Local subject"); expect(screen.getByRole("textbox", { name: "Reminder" })).toHaveValue("Local body"); });
  it("rejects blank body and invalid delay", async () => { const user = userEvent.setup(); const onCreate = vi.fn(); render(<ReminderDialog {...base} reminders={[]} onCreate={onCreate} />); await user.click(screen.getByRole("button", { name: "Add reminder" })); await user.click(screen.getByRole("button", { name: "Create reminder" })); expect(screen.getByRole("alert")).toHaveTextContent("Enter reminder text"); await user.type(screen.getByRole("textbox", { name: "Reminder" }), "Do it"); const delay = screen.getByRole("spinbutton"); await user.clear(delay); await user.type(delay, "10081"); await user.click(screen.getByRole("button", { name: "Create reminder" })); expect(onCreate).not.toHaveBeenCalled(); expect(screen.getByRole("alert")).toHaveTextContent("1 to 10080"); });
  it("contains rejected delete requests and shows the reminder error", async () => { const user = userEvent.setup(); const onDelete = vi.fn().mockRejectedValue(new Error("Delete failed")); render(<ReminderDialog {...base} onDelete={onDelete} />); await user.click(screen.getByRole("button", { name: "Delete" })); await expect(screen.findByRole("alert")).resolves.toHaveTextContent("Delete failed"); expect(screen.getByText("Check this")).toBeInTheDocument(); });
  it("shows refresh errors while keeping the current reminder list visible", () => { render(<ReminderDialog {...base} status="Reminder deleted, but the list could not be refreshed." />); expect(screen.getByRole("status")).toHaveTextContent("list could not be refreshed"); expect(screen.getByText("Check this")).toBeInTheDocument(); });
  it("removes the prompt picker when its native reminder dialog closes", async () => {
    const user = userEvent.setup();
    const template: PromptTemplate = { id: "t1", name: "Review", body: "Review ${file}" };
    const { rerender } = render(<ReminderDialog {...base} reminders={[]} templates={[template]} onUseTemplate={vi.fn()} dialogState={{ mode: "edit", editing: reminder, subject: "Check", body: "Check this", delay: 1, recurring: false, lockAfterCreate: false }} onDialogStateChange={vi.fn()} />);
    await user.click(screen.getByRole("button", { name: "Select prompt" }));
    expect(screen.getByRole("dialog", { name: "Choose prompt template" })).toBeInTheDocument();
    rerender(<ReminderDialog {...base} open={false} reminders={[]} templates={[template]} onUseTemplate={vi.fn()} dialogState={{ mode: "edit", editing: reminder, subject: "Check", body: "Check this", delay: 1, recurring: false, lockAfterCreate: false }} onDialogStateChange={vi.fn()} />);
    expect(screen.queryByRole("dialog", { name: "Choose prompt template" })).not.toBeInTheDocument();
  });
  it("reports the editor selection for template use without submitting", async () => {
    const user = userEvent.setup(); const template: PromptTemplate = { id: "t1", name: "Review", body: "Review ${file}" }; const onUseTemplate = vi.fn(); const onCreate = vi.fn();
    const state = { mode: "create" as const, subject: "", body: "Start", delay: 1, recurring: false, lockAfterCreate: false };
    const { rerender } = render(<ReminderDialog {...base} reminders={[]} onCreate={onCreate} templates={[template]} onUseTemplate={onUseTemplate} dialogState={state} onDialogStateChange={vi.fn()} />);
    const body = screen.getByRole("textbox", { name: "Reminder" });
    expect(body).toBeInstanceOf(HTMLTextAreaElement);
    if (!(body instanceof HTMLTextAreaElement)) throw new Error("Reminder input is not a textarea");
    body.focus(); body.setSelectionRange(5, 5);
    await user.click(screen.getByRole("button", { name: "Select prompt" })); await user.click(screen.getByRole("option", { name: "Use prompt template Review" }));
    expect(onUseTemplate).toHaveBeenCalledWith(template, { start: 5, end: 5 });
    rerender(<ReminderDialog {...base} reminders={[]} onCreate={onCreate} templates={[template]} onUseTemplate={onUseTemplate} dialogState={{ ...state, body: "StartReview app.ts" }} onDialogStateChange={vi.fn()} />);
    expect(screen.getByRole("textbox", { name: "Reminder" })).toHaveValue("StartReview app.ts"); expect(onCreate).not.toHaveBeenCalled();
  });
});
