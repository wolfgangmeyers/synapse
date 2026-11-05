import type { Agent } from "../state/agent-projection";

/** Tool-driven metadata: only committed projections can display the marker. */
export function AgentUrgencyMarker({ agent }: { agent: Agent }) {
  return agent.urgent === true
    ? (
      <i className="fa-solid fa-triangle-exclamation agent-urgency-marker" role="img" aria-label="Urgent" title="Urgent" />
    )
    : null;
}
