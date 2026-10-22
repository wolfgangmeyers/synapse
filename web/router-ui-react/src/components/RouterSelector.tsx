import type { ReactElement, ReactNode } from "react";
import { connectionLabels, type ConnectionState } from "./ConnectionStatus";

export interface RouterSelectorProps {
  label: string;
  status?: ConnectionState | undefined;
  identity?: string | undefined;
  detail?: string | undefined;
  onClick?: (() => void) | undefined;
  active?: boolean | undefined;
  actions?: ReactNode | undefined;
}

export function RouterSelector({
  label,
  status = "disconnected",
  identity,
  detail,
  onClick,
  active = true,
  actions,
}: RouterSelectorProps): ReactElement {
  const user = identity?.trim() || "wolfgang";
  const subtitle = status === "connected"
    ? `Signed in as ${user}`
    : (connectionLabels[status] ?? "Disconnected");

  return (
    <div
      className={`router-selector${active ? " active" : ""}`}
      onClick={onClick}
      role="button"
      tabIndex={0}
      onKeyDown={(event) => {
        if ((event.key === "Enter" || event.key === " ") && event.target === event.currentTarget) {
          event.preventDefault();
          onClick?.();
        }
      }}
      aria-label={`Router ${label}, ${subtitle}`}
      title={detail ? `Router ${label} (${subtitle}: ${detail})` : `Router ${label} (${subtitle})`}
    >
      <span className="router-selector-icon" aria-hidden="true">
        <i className="fa-solid fa-cube" />
      </span>
      <span className="router-selector-content">
        <strong>{label}</strong>
        <span className="router-selector-subtitle">{subtitle}</span>
      </span>
      {actions ? (
        <div className="router-selector-actions" onClick={(event) => event.stopPropagation()}>
          {actions}
        </div>
      ) : (
        <span className="router-selector-chevron" aria-hidden="true">
          <i className="fa-solid fa-chevron-down" />
        </span>
      )}
    </div>
  );
}
