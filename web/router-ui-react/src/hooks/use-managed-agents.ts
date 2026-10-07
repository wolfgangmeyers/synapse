import { useCallback, useEffect, useRef, useState } from "react";
import type { RouterHttpClient, ManagedAgentsResponse } from "../transport/http-client";
import type { ManagedAgent } from "../protocol/types";

export interface ManagedAgentsReconciliation {
  agents: ManagedAgent[];
  ready: boolean;
  loading: boolean;
  error?: Error;
  refresh(): Promise<ManagedAgentsResponse | undefined>;
}

export interface UseManagedAgentsOptions {
  intervalMs?: number;
  enabled?: boolean;
  onReconcile?: (snapshot: ManagedAgentsResponse) => void;
}

export function useManagedAgents(
  client: RouterHttpClient,
  options: UseManagedAgentsOptions = {}
): ManagedAgentsReconciliation {
  const { intervalMs = 3_000, enabled = true, onReconcile } = options;
  const [agents, setAgents] = useState<ManagedAgent[]>([]);
  const [ready, setReady] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<Error | undefined>();
  const requestGeneration = useRef(0);
  const onReconcileRef = useRef(onReconcile);
  onReconcileRef.current = onReconcile;

  const refresh = useCallback(async (): Promise<ManagedAgentsResponse | undefined> => {
    const generation = ++requestGeneration.current;
    setLoading(true);
    try {
      const snapshot = await client.managedAgents();
      if (generation !== requestGeneration.current) return undefined;
      setAgents(snapshot.agents);
      setReady(true);
      setError(undefined);
      onReconcileRef.current?.(snapshot);
      return snapshot;
    } catch (reason) {
      if (generation !== requestGeneration.current) return undefined;
      setError(reason instanceof Error ? reason : new Error("Managed agents refresh failed"));
      return undefined;
    } finally {
      if (generation === requestGeneration.current) setLoading(false);
    }
  }, [client]);

  useEffect(() => {
    if (!enabled) return;
    void refresh();
    const timer = setInterval(() => {
      void refresh();
    }, intervalMs);
    return () => {
      clearInterval(timer);
    };
  }, [client, enabled, intervalMs, refresh]);

  return { agents, ready, loading, ...(error === undefined ? {} : { error }), refresh };
}
