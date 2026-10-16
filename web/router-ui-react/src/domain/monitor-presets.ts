import type { CreateAgentMonitorPayload } from "../transport/http-client";

export interface MonitorPreset {
  id: string;
  name: string;
  description: string;
  payload: CreateAgentMonitorPayload;
}

export const MONITOR_PRESETS: MonitorPreset[] = [
  {
    id: "loop-stall-detector",
    name: "Loop & Stall Detector",
    description: "Detects if the agent is repeating commands, caught in an unproductive loop, or stalled.",
    payload: {
      name: "Loop & Stall Detector",
      description: "Detects repetitive loops or stall states",
      enabled: true,
      interval_seconds: 30,
      cooldown_seconds: 120,
      max_alerts: 0,
      trailing_messages_count: 6,
      question_type: "noul",
      question_instructions:
        "Is the agent stuck repeating the same commands, tool errors, or unproductive cyclical actions without making forward progress?",
      condition_operator: "gte",
      condition_threshold: "0.8",
      nudge_template:
        "You appear to be repeating actions or stalled in an unproductive loop. Step back, reconsider your plan, and try a different approach.",
      fallback_mode: "skip",
    },
  },
  {
    id: "goal-drift-watchdog",
    name: "Goal Drift Watchdog",
    description: "Alerts when the agent has drifted away from the primary assigned objective or current state goal.",
    payload: {
      name: "Goal Drift Watchdog",
      description: "Alerts when agent drifts from assigned objective",
      enabled: true,
      interval_seconds: 60,
      cooldown_seconds: 300,
      max_alerts: 0,
      trailing_messages_count: 8,
      question_type: "noul",
      question_instructions:
        "Has the agent drifted away from its assigned task or goal, focusing on irrelevant tangents or unauthorized refactoring?",
      condition_operator: "gte",
      condition_threshold: "0.8",
      nudge_template:
        "You are drifting from your core goal. Re-read the primary objective and refocus your next actions strictly on completing it.",
      fallback_mode: "skip",
    },
  },
  {
    id: "tool-error-frequency",
    name: "Tool Error Frequency",
    description: "Detects persistent tool execution failures or command syntax issues.",
    payload: {
      name: "Tool Error Frequency",
      description: "Flags frequent tool execution errors",
      enabled: true,
      interval_seconds: 30,
      cooldown_seconds: 120,
      max_alerts: 0,
      trailing_messages_count: 6,
      question_type: "noul",
      question_instructions:
        "Are the agent's recent tool calls or commands frequently failing, resulting in errors or unexpected output?",
      condition_operator: "gte",
      condition_threshold: "0.7",
      nudge_template:
        "Multiple tool calls have failed recently. Review the error details carefully and verify your command arguments before proceeding.",
      fallback_mode: "skip",
    },
  },
  {
    id: "user-frustration-guard",
    name: "User Frustration Guard",
    description: "Monitors conversation messages for signs of user dissatisfaction or repeated corrections.",
    payload: {
      name: "User Frustration Guard",
      description: "Detects user frustration or repeated corrections",
      enabled: true,
      interval_seconds: 60,
      cooldown_seconds: 300,
      max_alerts: 0,
      trailing_messages_count: 6,
      question_type: "noul",
      question_instructions:
        "Is the user expressing frustration, dissatisfaction, or repeatedly correcting the agent's mistakes in recent messages?",
      condition_operator: "gte",
      condition_threshold: "0.8",
      nudge_template:
        "The user appears frustrated or has corrected you repeatedly. Acknowledge their feedback directly and adjust your approach.",
      fallback_mode: "skip",
    },
  },
];

export function getMonitorPresets(): MonitorPreset[] {
  return MONITOR_PRESETS;
}

export function getMonitorPresetById(id: string): MonitorPreset | undefined {
  return MONITOR_PRESETS.find((p) => p.id === id || p.name === id);
}

export function getMonitorPresetDefaults(idOrName: string): CreateAgentMonitorPayload | undefined {
  const preset = getMonitorPresetById(idOrName);
  return preset ? { ...preset.payload } : undefined;
}
