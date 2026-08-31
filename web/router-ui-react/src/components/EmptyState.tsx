import type { ReactElement } from "react";
export interface EmptyStateProps { title: string; description?: string }
export function EmptyState({ title, description }: EmptyStateProps): ReactElement { return <div className="empty-state"><h3>{title}</h3>{description ? <p>{description}</p> : null}</div>; }
