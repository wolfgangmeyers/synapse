import type { ReactElement } from "react";
import { AgentAvatar, avatarForAgent } from "./AgentAvatar";
import { humanName } from "../state/selectors";
import type { Agent } from "../state/agent-projection";

export interface AgentWelcomeCardProps {
  agentId: string;
  agent?: Agent | undefined;
}

export function AgentWelcomeCard({ agentId, agent }: AgentWelcomeCardProps): ReactElement {
  const name = humanName(agentId);
  let accentColor = "#38bdf8";
  try {
    const appearance = avatarForAgent(agentId);
    accentColor = appearance.color;
  } catch {
    // fallback
  }

  const activityState = agent?.activity_state ?? "idle";
  const isCompacting = activityState === "compacting";
  const isActive = activityState === "active";
  const isTmux = agent?.source === "tmux";

  return (
    <section
      className="agent-welcome-card"
      aria-label={`Welcome to ${name}`}
      style={{ "--agent-accent": accentColor } as React.CSSProperties}
    >
      <div className="welcome-card-header">
        <div className="welcome-avatar-wrapper">
          <div className="welcome-avatar-glow" aria-hidden="true" />
          <div className="welcome-avatar-frame">
            <AgentAvatar agentId={agentId} />
          </div>
        </div>

        <div className="welcome-agent-identity">
          <div className="welcome-agent-title-row">
            <h2 className="welcome-agent-name">{name}</h2>
            {isTmux ? <span className="welcome-runtime-badge">tmux</span> : null}
          </div>

          <div className="welcome-agent-meta-row">
            <span className="welcome-agent-id">@{agentId}</span>
            <span className="welcome-meta-divider" aria-hidden="true">•</span>
            <span
              className={`welcome-status-pill ${
                isCompacting
                  ? "welcome-status-pill--compacting"
                  : isActive
                  ? "welcome-status-pill--active"
                  : "welcome-status-pill--ready"
              }`}
            >
              <span className="welcome-status-dot" aria-hidden="true" />
              {isCompacting ? "Compacting memory" : isActive ? "Working" : "Ready to assist"}
            </span>
          </div>
        </div>
      </div>

      <p className="welcome-card-description">
        This conversation has no prior messages. Send a message below to start collaborating.
      </p>
    </section>
  );
}
