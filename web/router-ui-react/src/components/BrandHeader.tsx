import type { ReactElement } from "react";

export interface BrandHeaderProps {
  title?: string | undefined;
  subtitle?: string | undefined;
  activeTab?: "chat" | "workflows" | undefined;
  onNavigate?: ((tab: "chat" | "workflows") => void) | undefined;
  onRefresh?: (() => void) | undefined;
}

export function BrandHeader({ activeTab = "chat", onNavigate }: BrandHeaderProps): ReactElement {
  const handleNavigate = (tab: "chat" | "workflows") => {
    if (onNavigate) {
      onNavigate(tab);
    } else {
      window.location.hash = tab === "workflows" ? "#/workflows" : "";
    }
  };

  return (
    <header className="brand">
      <div className="brand-title-row">
        <img className="brand-logo" src="/logo.png" alt="Mecha Developer" />
      </div>
      <nav className="brand-nav" aria-label="Main Navigation">
        <button
          type="button"
          className={`brand-nav-tab ${activeTab === "chat" ? "active" : ""}`}
          onClick={() => handleNavigate("chat")}
        >
          <i className="fa-solid fa-comments" aria-hidden="true" /> Chat
        </button>
        <button
          type="button"
          className={`brand-nav-tab ${activeTab === "workflows" ? "active" : ""}`}
          onClick={() => handleNavigate("workflows")}
        >
          <i className="fa-solid fa-diagram-project" aria-hidden="true" /> Workflows
        </button>
      </nav>
    </header>
  );
}
