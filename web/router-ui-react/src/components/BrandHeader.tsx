import type { ReactElement } from "react";

export interface BrandHeaderProps {
  title?: string | undefined;
  subtitle?: string | undefined;
  onRefresh?: (() => void) | undefined;
}

export function BrandHeader({}: BrandHeaderProps): ReactElement {
  return (
    <header className="brand">
      <div className="brand-title-row">
        <div className="brand-sigil" aria-hidden="true">
          <svg viewBox="0 0 40 40" fill="none" xmlns="http://www.w3.org/2000/svg">
            <path d="M20 2L24 10H16L20 2Z" fill="#38bdf8" />
            <path d="M20 38L16 30H24L20 38Z" fill="#38bdf8" />
            <path d="M2 20L10 16V24L2 20Z" fill="#38bdf8" />
            <path d="M38 20L30 24V16L38 20Z" fill="#38bdf8" />
            <path d="M20 10L30 20L20 30L10 20L20 10Z" stroke="#818cf8" strokeWidth="2.5" fill="rgba(56, 189, 248, 0.15)" />
            <circle cx="20" cy="20" r="4.5" fill="#38bdf8" />
          </svg>
        </div>
        <div className="brand-text-col">
          <h1 className="brand-main-title">Mecha-Developer</h1>
          <span className="brand-sub-title">AI AGENTS FOR REAL BUILDERS</span>
        </div>
      </div>
    </header>
  );
}
