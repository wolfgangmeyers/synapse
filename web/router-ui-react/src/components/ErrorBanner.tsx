import type { ReactElement } from "react";

export interface ErrorBannerProps {
  message: string;
  code?: string;
  onDismiss: () => void;
  className?: string;
}

export function ErrorBanner({ message, code, onDismiss, className = "" }: ErrorBannerProps): ReactElement {
  return (
    <div className={`error-banner ${className}`.trim()} role="alert">
      <div className="error-banner-content">
        <i className="fa-solid fa-circle-exclamation error-banner-icon" aria-hidden="true" />
        <span className="error-banner-text">{message}</span>
        {code ? <code className="error-banner-code">{code}</code> : null}
      </div>
      <button
        type="button"
        className="error-banner-dismiss"
        onClick={onDismiss}
        aria-label="Dismiss error"
        title="Dismiss error"
      >
        <i className="fa-solid fa-xmark" aria-hidden="true" />
        <span className="error-banner-dismiss-label">Dismiss</span>
      </button>
    </div>
  );
}
