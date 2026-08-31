import "@testing-library/jest-dom/vitest";
import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import { ReminderDialog } from "../../src/components/ReminderDialog";
import { PromptTemplateDialog } from "../../src/components/PromptTemplateDialog";
import { MessageBubble } from "../../src/components/MessageBubble";
import { createInitialState } from "../../src/state/app-state";
import { reducer } from "../../src/state/reducer";
import type { Reminder } from "../../src/domain/reminders";

afterEach(cleanup);

const reminder: Reminder = { id: "r1", target: "agent-one", creator: "web", body: "Check this", subject: "Check", state: "scheduled", enabled: true, next_due_at: "2030-01-01T00:00:05.000Z", interval_ms: null };
const template = { id: "t1", name: "Review", body: "Review ${file}" };
const reminderProps = { open: true, peer: "agent-one", creator: "web", reminders: [reminder], onClose: vi.fn(), onCreate: vi.fn(), onUpdate: vi.fn(), onDelete: vi.fn(), onLock: vi.fn() };
const promptProps = { open: true, templates: [template], onClose: vi.fn(), onSave: vi.fn(), onDelete: vi.fn(), onImport: vi.fn() };

describe("final behavior blockers", () => {
  it("updates reminder countdown while the dialog stays open", () => {
    vi.useFakeTimers({ now: new Date("2030-01-01T00:00:00.000Z") });
    try {
      render(<ReminderDialog {...reminderProps} />);
      expect(screen.getByText("Due in 5s")).toBeInTheDocument();
      act(() => { vi.advanceTimersByTime(1_000); });
      expect(screen.getByText("Due in 4s")).toBeInTheDocument();
    } finally { vi.useRealTimers(); }
  });

  it("shows reminder update and delete failures in the dialog", async () => {
    const user = (await import("@testing-library/user-event")).default.setup();
    const onUpdate = vi.fn(async () => { throw new Error("update failed"); });
    const onDelete = vi.fn(async () => { throw new Error("delete failed"); });
    const { unmount } = render(<ReminderDialog {...reminderProps} onUpdate={onUpdate} />);
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("update failed"));
    unmount();
    render(<ReminderDialog {...reminderProps} onDelete={onDelete} />);
    await user.click(screen.getByRole("button", { name: "Delete" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("delete failed"));
  });

  it("shows prompt CRUD failures without closing the editor", async () => {
    const user = (await import("@testing-library/user-event")).default.setup();
    const onSave = vi.fn().mockRejectedValue(new Error("save failed"));
    render(<PromptTemplateDialog {...promptProps} onSave={onSave} />);
    await user.click(screen.getByRole("button", { name: "Edit" }));
    await user.click(screen.getByRole("button", { name: "Save changes" }));
    await waitFor(() => expect(onSave).toHaveBeenCalledOnce());
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("save failed"));
    expect(screen.getByRole("heading", { name: "Edit prompt template" })).toBeInTheDocument();
  });

  it("renders canonical attachment references found only in the message body", () => {
    render(<MessageBubble routerUrl="http://router.test:4000" ownerId="web" message={{ id: "m1", from: "worker", to: "web", body: "See /v1/attachments/photo.jpg" }} />);
    expect(screen.getByRole("img", { name: "photo.jpg" })).toHaveAttribute("src", "http://router.test:4000/v1/attachments/photo.jpg");
  });

  it("drops agents and messages when the scope changes to a router with the same peer", () => {
    const first = createInitialState({ routerUrl: "http://one.test:4000", agentId: "web" });
    first.selectedPeer = "worker";
    first.agents = [{ agent_id: "worker" }];
    first.messagesByPeer.worker = [{ id: "old", from: "worker", to: "web" }];
    const switched = reducer(first, { type: "scope/reset", scope: { routerUrl: "http://two.test:4000", agentId: "web" } });
    expect(switched.agents).toEqual([]);
    expect(switched.messagesByPeer).toEqual({});
    expect(reducer(switched, { type: "message/append", scope: { routerUrl: "http://one.test:4000", agentId: "web" }, peer: "worker", message: { id: "stale" } })).toBe(switched);
  });
});
