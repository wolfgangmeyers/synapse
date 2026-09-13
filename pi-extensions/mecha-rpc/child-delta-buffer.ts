import type { JsonRecord } from "./manifest.js";

export const MAX_BUFFERED_CHILD_DELTAS = 64;
const MAX_CHILD_DELTA_DELIVERY_ATTEMPTS = 3;

type BufferedChildDelta = { ownerSessionId: string; wire: JsonRecord };
type SendChildDelta = (wire: JsonRecord) => Promise<void>;

/**
 * Keeps lifecycle delivery ordered without allowing a stalled Pi append to
 * retain an unbounded source history. Replacing the newest queued entry on
 * overflow deliberately leaves a sequence gap, which makes the router obtain
 * an authoritative snapshot instead of trusting a partial delta stream.
 */
export class BoundedChildDeltaBuffer {
  private readonly pending: BufferedChildDelta[] = [];
  private draining = false;
  private generation = 0;

  constructor(
    private readonly send: SendChildDelta,
    private readonly isCurrentOwner: (ownerSessionId: string) => boolean,
  ) {}

  enqueue(ownerSessionId: string, wire: JsonRecord): void {
    const entry = { ownerSessionId, wire };
    if (this.pending.length < MAX_BUFFERED_CHILD_DELTAS) {
      this.pending.push(entry);
    } else {
      // The head may already be in an append attempt. Preserve it and replace
      // only the newest waiting item so delivery remains FIFO up to the gap.
      this.pending[this.pending.length - 1] = entry;
    }
    this.startDraining();
  }

  clear(): void {
    this.generation++;
    this.pending.length = 0;
  }

  private startDraining(): void {
    if (this.draining) return;
    this.draining = true;
    void this.drain();
  }

  private async drain(): Promise<void> {
    const generation = this.generation;
    try {
      while (generation === this.generation && this.pending.length > 0) {
        const entry = this.pending[0];
        if (entry === undefined) return;
        await this.deliver(entry, generation);
        if (generation !== this.generation) return;
        this.pending.shift();
      }
    } finally {
      this.draining = false;
      if (this.pending.length > 0 && generation === this.generation) this.startDraining();
    }
  }

  private async deliver(entry: BufferedChildDelta, generation: number): Promise<void> {
    for (let attempt = 0; attempt < MAX_CHILD_DELTA_DELIVERY_ATTEMPTS; attempt++) {
      if (generation !== this.generation || !this.isCurrentOwner(entry.ownerSessionId)) return;
      try {
        await this.send(entry.wire);
        return;
      } catch {
        // The retry count is intentionally finite. A later retained delta
        // carries a sequence gap and causes router-side snapshot recovery.
      }
    }
  }
}
