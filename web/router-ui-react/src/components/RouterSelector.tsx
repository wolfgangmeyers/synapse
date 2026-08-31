import type { ReactElement } from "react";

export interface RouterSelectorProps {
  label: string;
  onClick?: (() => void) | undefined;
  active?: boolean | undefined;
}

export function RouterSelector({ label, onClick, active = true }: RouterSelectorProps): ReactElement {
  return <button type="button" className={`router-selector${active ? " active" : ""}`} onClick={onClick} aria-label={`Router ${label}`}>
    <span><small>Router</small><strong>{label}</strong></span><span aria-hidden="true"><i className="fa-solid fa-chevron-down" aria-hidden="true" /></span>
  </button>;
}
