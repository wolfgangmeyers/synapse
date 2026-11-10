// Pending writes outlive dialogs and are never evicted. Settled successes are
// removed immediately; at most 100 settled failures per client are retained,
// oldest first, until acknowledged or displaced. Messages are capped at 512.
export const MAX_RETAINED_PERMISSION_FAILURES = 100;
type Entry = { pending: Promise<void>; writes: number; error?: string };
const queues = new WeakMap<object, Map<string, Entry>>();
function entries(client: object): Map<string, Entry> {
  let map = queues.get(client);
  if (!map) { map = new Map(); queues.set(client, map); }
  return map;
}
function prune(map: Map<string, Entry>): void {
  for (const [key, entry] of map) {
    if (entry.writes === 0 && !entry.error) map.delete(key);
  }
  let failures = 0;
  for (const entry of map.values()) if (entry.writes === 0 && entry.error) failures++;
  for (const [key, entry] of map) {
    if (failures <= MAX_RETAINED_PERMISSION_FAILURES) break;
    if (entry.writes === 0 && entry.error) { map.delete(key); failures--; }
  }
}
export function enqueuePermissionWrite(client: object, key: string, write: () => Promise<void>): Promise<void> {
  const map = entries(client);
  let entry = map.get(key);
  if (!entry) { entry = { pending: Promise.resolve(), writes: 0 }; map.set(key, entry); }
  const current = entry;
  current.writes++;
  const result = current.pending.then(write);
  current.pending = result.then(() => undefined, err => {
    current.error = `Some changes may not have been saved: ${err instanceof Error ? err.message : "update failed"}`.slice(0, 512);
    // Refresh failure ordering for the bounded oldest-first retention policy.
    map.delete(key); map.set(key, current);
  }).finally(() => { current.writes--; prune(map); });
  return result;
}
export async function awaitPermissionWrites(client: object, key: string): Promise<string | undefined> {
  const map = queues.get(client);
  if (!map) return undefined;
  // A writer can be queued while an earlier one is settling.
  for (;;) {
    const entry = map.get(key);
    if (!entry) return undefined;
    const pending = entry.pending;
    await pending;
    if (map.get(key) !== entry) continue;
    if (entry.pending !== pending) continue;
    return entry.error;
  }
}
export function acknowledgePermissionError(client: object, key: string): void {
  const map = queues.get(client);
  const entry = map?.get(key);
  if (!map || !entry) return;
  delete entry.error;
  prune(map);
}
