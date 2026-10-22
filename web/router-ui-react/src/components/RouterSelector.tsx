import type { ReactElement } from "react";
import { connectionLabels, type ConnectionState } from "./ConnectionStatus";

export interface RouterSelectorProps {
  label: string;
  status?: ConnectionState | undefined;
  identity?: string | undefined;
  detail?: string | undefined;
  onClick?: (() => void) | undefined;
  active?: boolean | undefined;
}

export function RouterSelector({
  label,
  status = "disconnected",
  identity,
  detail,
  onClick,
  active = true,
}: RouterSelectorProps): ReactElement {
  const statusText = status === "connected"
    ? (identity === undefined || identity === "" ? connectionLabels.connected : `Connected as ${identity}`)
    : (connectionLabels[status] ?? "Disconnected");

  return (
    <button
      type="button"
      className={`router-selector${active ? " active" : ""}`}
      onClick={onClick}
      aria-label={`Router ${label}, ${statusText}`}
      title={detail ? `Router ${label} (${statusText}: ${detail})` : `Router ${label} (${statusText})`}
    >
      <span className="router-selector-icon" aria-hidden="true">
        <i className="fa-solid fa-cube" />
      </span>
      <span className="router-selector-content">
        <span className="router-selector-subtitle">Router &amp; sign-in</span>
        <strong>{label}</strong>
      </span>
      <span className="router-selector-chevron" aria-hidden="true">
        <i className="fa-solid fa-chevron-down" />
      </span>
    </button>
  );
}
