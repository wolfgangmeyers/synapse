import type { HistoryMessage } from "../domain/history";

// Count JS string storage conservatively, including the data URL header.
export const OUTBOX_IMAGE_BYTES = 16 * 1024 * 1024;
export function attachmentBytes(message: HistoryMessage): number {
  return (message.attachments ?? []).reduce((total, attachment) => total + (attachment.url?.startsWith("data:") ? attachment.url.length * 2 : 0), 0);
}
export class InFlightImageBudget {
  private readonly reservations = new Map<string, number>();
  reserve(id: string, message: HistoryMessage, retained: Record<string, HistoryMessage[]>): boolean {
    const bytes = attachmentBytes(message);
    let used = [...this.reservations.values()].reduce((a, b) => a + b, 0);
    for (const item of Object.values(retained).flat()) if (!this.reservations.has(item.id)) used += attachmentBytes(item);
    if (used + bytes > OUTBOX_IMAGE_BYTES) return false;
    this.reservations.set(id, bytes);
    return true;
  }
  release(id: string): void { this.reservations.delete(id); }
}

// A settlement can be ignored by the scoped reducer. Release is therefore
// driven by an independent commit, not by changes to the outbox projection.
export class DeferredImageReleases {
  private readonly ids = new Set<string>();
  defer(id: string): void { this.ids.add(id); }
  commit(budget: InFlightImageBudget): void {
    for (const id of this.ids) budget.release(id);
    this.ids.clear();
  }
}
