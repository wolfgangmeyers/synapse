import type { ReactElement } from "react";

export interface BrandHeaderProps {
  title?: string | undefined;
  subtitle?: string | undefined;
  onRefresh?: (() => void) | undefined;
}

export function BrandHeader({ title = "Mecha", subtitle = "Your agent network", onRefresh }: BrandHeaderProps): ReactElement {
  return <header className="brand">
    <div><h1>{title}</h1><p>{subtitle}</p></div>
    <button id="refresh" type="button" aria-label="Refresh agents" onClick={onRefresh} disabled={onRefresh === undefined} title="Refresh agents"><i className="fa-solid fa-arrows-rotate" aria-hidden="true" /></button>
  </header>;
}
