import type { ReactElement } from "react";

export function roboHashAvatarUrl(agentId: string): string {
  return `https://robohash.org/${encodeURIComponent(agentId)}?set=set3`;
}

export function AgentAvatar({ agentId }: { agentId: string }): ReactElement {
  return <img src={roboHashAvatarUrl(agentId)} alt="" />;
}
