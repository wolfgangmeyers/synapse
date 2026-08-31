import type { ReactElement, ReactNode } from "react";

export interface AppShellProps {
  sidebar: ReactNode;
  conversation: ReactNode;
  className?: string;
}

export function AppShell({ sidebar, conversation, className = "" }: AppShellProps): ReactElement {
  return <main data-testid="app-shell" className={`shell ${className}`.trim()}>
    <aside className="sidebar">{sidebar}</aside>
    <section className="conversation" aria-label="Conversation">{conversation}</section>
  </main>;
}
