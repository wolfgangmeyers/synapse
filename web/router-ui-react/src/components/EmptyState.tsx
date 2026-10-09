import type { ReactElement } from "react";
export interface EmptyStateProps { title: string; description?: string; watermark?: boolean }
export function EmptyState({ title, description, watermark }: EmptyStateProps): ReactElement {
  return <div className={`empty-state${watermark ? " with-watermark" : ""}`}>{watermark ? <img src="/mecha-watermark.png" alt="" className="mecha-watermark" aria-hidden="true" /> : null}<h3>{title}</h3>{description ? <p>{description}</p> : null}</div>;
}
