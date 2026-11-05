import { useLayoutEffect, useState } from "react";
import { CONVERSATION_UNLOAD_THRESHOLD } from "../domain/history";
import type { ManagedHistoryEntry } from "../protocol/types";
import { compareManagedHistoryEntries } from "../state/reducer";
import type { RouterScope } from "../state/scopes";
import type { RouterHttpClient } from "../transport/http-client";
import type { UILiveClient } from "../transport/ui-live-client";

const LIMIT = 50;
type HistoryClient = Pick<RouterHttpClient, "managedHistory">;
type Activity = { entries: ManagedHistoryEntry[]; liveIds: ReadonlySet<string>; ready: boolean; hasOlder: boolean; loadingOlder: boolean; error?: Error; discardBefore: (ids: ReadonlySet<string>) => void; loadOlder: (beforeCommit?: () => void) => Promise<void> };
type View = Activity & { key: string; client: HistoryClient; live: UILiveClient | undefined };
const noop = async () => {};

function merge(snapshot: ManagedHistoryEntry[], newer: ManagedHistoryEntry[]): ManagedHistoryEntry[] {
  const entries = new Map(snapshot.map((entry) => [entry.id, entry]));
  for (const entry of newer) entries.set(entry.id, entry);
  return [...entries.values()].sort(compareManagedHistoryEntries);
}

/** Selected view only. Pass a peer only when it is router-managed. */
export function useSelectedManagedActivity(client: HistoryClient, scope: RouterScope, managedPeer: string | undefined, live?: UILiveClient): Activity {
  const key = JSON.stringify([scope.routerUrl, scope.agentId, managedPeer ?? null]);
  const [view, setView] = useState<View>();
  useLayoutEffect(() => {
    let active = true;
    let entries: ManagedHistoryEntry[] = [];
    let ready = !managedPeer;
    let cursor: number | null = null;
    let initialFailed = false;
    let pending = false;
    let requestVersion = 0;
    let liveVersion = 0;
    const liveVersions = new Map<string, number>();
    let error: Error | undefined;
    let recoveryBoundary: number | null = null;
    let recoverFromLatest = false;
    const liveIds = new Set<string>();
    let preDisconnectIds: Set<string> | undefined;
    const publish = () => { if (active) setView({ key, client, live, entries, liveIds: new Set(liveIds), ready, hasOlder: initialFailed || cursor !== null, loadingOlder: pending && ready, discardBefore, ...(error ? { error } : {}), loadOlder }); };
    const fetchPage = async (initial: boolean, beforeCommit?: () => void, reconnect = false) => {
      if (!active || !managedPeer || pending || (!initial && !initialFailed && cursor === null)) return;
      pending = true;
      const request = ++requestVersion;
      const startVersion = liveVersion;
      const requestIds = new Set(entries.map((entry) => entry.id));
      publish();
      try {
        const before = initial || initialFailed ? undefined : cursor ?? undefined;
        const response = await client.managedHistory(managedPeer, "complete", { limit: LIMIT, ...(before === undefined ? {} : { before }) });
        if (!active || request !== requestVersion) return;
        // A live trim invalidates an older request's boundary. Discard its page;
        // the next request must start at the new retained edge.
        if (!initial && (recoveryBoundary !== null || recoverFromLatest)) {
          if (recoveryBoundary !== null) cursor = recoveryBoundary;
          if (recoverFromLatest) initialFailed = true;
          recoveryBoundary = null;
          recoverFromLatest = false;
          return;
        }
        beforeCommit?.();
        error = undefined;
        const snapshot = response.entries.filter((entry) => {
          if (entry.agent_id !== managedPeer) return false;
          // An in-flight snapshot predating a live trim must not resurrect stale
          // versions of entries that were removed from the retained window.
          if (initial && recoveryBoundary !== null) {
            const match = /^complete:(\d+)(?::.*)?$/.exec(entry.id);
            if (match && Number(match[1]) < recoveryBoundary) return false;
          }
          return true;
        });
        const concurrent = entries.filter((entry) => (liveVersions.get(entry.id) ?? 0) > startVersion || liveIds.has(entry.id));
        // Only the pre-disconnect window can prove continuity across a gap.
        const overlapIds = preDisconnectIds ?? requestIds;
        const disconnected = reconnect && overlapIds.size > 0 && snapshot.length > 0 && response.next_before != null && !snapshot.some((item) => overlapIds.has(item.id));
        // An overlapping reconnect must not collapse history explicitly loaded by
        // the reader. New snapshot entries are still bounded by the larger of
        // the normal live limit and the window already retained at reconciliation.
        const latestLimit = reconnect && ready && !disconnected ? Math.max(CONVERSATION_UNLOAD_THRESHOLD, entries.length) : CONVERSATION_UNLOAD_THRESHOLD;
        entries = merge(merge(disconnected ? [] : entries, snapshot), concurrent);
        if (disconnected) { cursor = response.next_before ?? null; liveIds.clear(); liveVersions.clear(); for (const item of concurrent) { liveIds.add(item.id); liveVersions.set(item.id, liveVersion); } }
        // Bound latest-page merges; explicit older-page loads remain expanded.
        if (initial && entries.length > latestLimit) {
          entries = entries.slice(-latestLimit);
          const match = /^complete:(\d+)(?::.*)?$/.exec(entries[0]?.id ?? "");
          const boundary = match ? Number(match[1]) : NaN;
          if (Number.isSafeInteger(boundary) && boundary > 0) recoveryBoundary = Math.max(recoveryBoundary ?? 0, boundary);
          else recoverFromLatest = true;
          const retainedIds = new Set(entries.map((entry) => entry.id));
          for (const id of liveIds) if (!retainedIds.has(id)) { liveIds.delete(id); liveVersions.delete(id); }
        }
        if (!reconnect || !ready) cursor = response.next_before ?? null;
        if (recoveryBoundary !== null) cursor = Math.max(cursor ?? 0, recoveryBoundary);
        initialFailed = recoverFromLatest;
        if (recoverFromLatest) error = new Error("Unable to recover older managed history. Please retry.");
        if (reconnect) preDisconnectIds = undefined;
        recoveryBoundary = null;
        recoverFromLatest = false;
      } catch {
        if (!active || request !== requestVersion) return;
        if (initial) initialFailed = true;
        error = new Error("Unable to load managed history. Please retry.");
      } finally {
        if (active && request === requestVersion) { pending = false; ready = true; publish(); }
      }
    };
    function discardBefore(visible: ReadonlySet<string>) {
      const removed = entries.filter((entry) => !visible.has(entry.id) && !liveIds.has(entry.id) && /^complete:(\d+)(?::.*)?$/.test(entry.id));
      if (!removed.length) return;
      const boundaries = removed.map((entry) => Number(/^complete:(\d+)/.exec(entry.id)?.[1]));
      if (boundaries.some((value) => !Number.isSafeInteger(value) || value <= 0)) return;
      const ids = new Set(removed.map((entry) => entry.id));
      entries = entries.filter((entry) => !ids.has(entry.id));
      cursor = Math.max(cursor ?? 0, ...boundaries.map((value) => value + 1));
      if (pending) recoveryBoundary = Math.max(recoveryBoundary ?? 0, cursor);
      publish();
    }
    async function loadOlder(beforeCommit?: () => void) { await fetchPage(false, beforeCommit); }
    publish();
    if (!managedPeer) return () => { active = false; };
    const unsubscribe = live?.subscribe?.((frame) => {
      if (!active) return;
      if (frame === "disconnected") { preDisconnectIds ??= new Set(entries.map((entry) => entry.id)); return; }
      if (frame === "connected") { pending = false; void fetchPage(true, undefined, true); return; }
      if (frame.type !== "history_entry" || frame.view !== "complete" || frame.entry.agent_id !== managedPeer) return;
      const existing = entries.some((entry) => entry.id === frame.entry.id);
      liveIds.add(frame.entry.id);
      liveVersions.set(frame.entry.id, ++liveVersion);
      entries = merge(entries, [frame.entry]);
      if (!existing && entries.length > CONVERSATION_UNLOAD_THRESHOLD) {
        const retained = entries.slice(-CONVERSATION_UNLOAD_THRESHOLD);
        const match = /^complete:(\d+)(?::.*)?$/.exec(retained[0]?.id ?? "");
        const boundary = match ? Number(match[1]) : NaN;
        entries = retained;
        if (Number.isSafeInteger(boundary) && boundary > 0) {
          cursor = boundary;
          if (pending) recoveryBoundary = Math.max(recoveryBoundary ?? 0, boundary);
        } else {
          // An unrecognized live ID cannot be used as a durable cursor. Restart
          // from the latest durable page rather than retain an unbounded window.
          initialFailed = true;
          error = new Error("Unable to recover older managed history. Please retry.");
          if (pending) recoverFromLatest = true;
        }
      }
      const retainedIds = new Set(entries.map((entry) => entry.id));
      for (const id of liveIds) if (!retainedIds.has(id)) { liveIds.delete(id); liveVersions.delete(id); }
      publish();
    });
    void fetchPage(true);
    return () => { active = false; unsubscribe?.(); };
  }, [key, client, live, managedPeer]);
  if (!managedPeer) return { entries: [], liveIds: new Set(), ready: true, hasOlder: false, loadingOlder: false, discardBefore: () => {}, loadOlder: noop };
  if (!view || view.key !== key || view.client !== client || view.live !== live) return { entries: [], liveIds: new Set(), ready: false, hasOlder: false, loadingOlder: false, discardBefore: () => {}, loadOlder: noop };
  return view;
}
