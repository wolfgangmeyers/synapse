import type { ReactElement } from "react";
import type { ConnectionStatus as StateConnectionStatus } from "../state/app-state";

export type ConnectionState = StateConnectionStatus | "offline";
export interface ConnectionStatusProps { status: ConnectionState; identity?: string | undefined; detail?: string | undefined }
export const connectionLabels: Record<ConnectionState, string> = { connected: "Connected", connecting: "Connecting", reconnecting: "Reconnecting", disconnected: "Disconnected", error: "Connection error", offline: "Disconnected" };
const labels = connectionLabels;

export function ConnectionStatus({ status, identity, detail }: ConnectionStatusProps): ReactElement {
  const connectedLabel = identity === undefined || identity === "" ? labels.connected : `Connected as ${identity}`;
  return <section className="status-card" aria-live="polite" aria-label="Connection status">
    <span className={`status-dot ${status}`} title={labels[status]} />
    <div><strong>{status === "connected" ? connectedLabel : labels[status]}</strong><p>{status === "connected" ? undefined : (detail ?? "Open routers to connect.")}</p></div>
  </section>;
}
