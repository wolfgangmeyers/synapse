import type { ReactElement } from "react";
import "./BrandHeader.css";

export interface BrandHeaderProps {
  title?: string | undefined;
  subtitle?: string | undefined;
  onRefresh?: (() => void) | undefined;
}

export function BrandHeader({}: BrandHeaderProps): ReactElement {
  return (
    <header className="brand">
      <img className="brand-logo" src="/synapse-logo.png" alt="Synapse" width={2172} height={724} />
    </header>
  );
}
