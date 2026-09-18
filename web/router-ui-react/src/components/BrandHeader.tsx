import type { ReactElement } from "react";

export interface BrandHeaderProps {
  title?: string | undefined;
  subtitle?: string | undefined;
  onRefresh?: (() => void) | undefined;
}

export function BrandHeader(_props: BrandHeaderProps): ReactElement {
  return <header className="brand">
    <img className="brand-logo" src="/logo.png" alt="Mecha Developer" />
  </header>;
}
