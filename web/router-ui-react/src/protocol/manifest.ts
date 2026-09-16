export type HttpMethod = "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "OPTIONS";

export interface HttpRoute {
  methods: readonly HttpMethod[];
  path: string;
  actions?: readonly string[];
}

export interface ProtocolManifest {
  version: "v1";
  http: readonly HttpRoute[];
  ui_live: { path: "/v1/ui-live"; protocol: "mecha.web/v1"; frames: readonly string[] };
  websocket: {
    path: "/v1/agents";
    client_frames: readonly string[];
    server_frames: readonly string[];
    schema: "protocol/v1/schema.json";
  };
}

export const clientFrameNames = [
  "register", "lease_renew", "activity", "model_catalog", "model_control", "model_control_result",
  "inline_tool_state", "inline_tool_control", "inline_tool_control_result",
  "goal_control", "goal_control_result", "reminder_lock_control", "reminder_lock_control_result",
  "goal_state", "message_arrival", "injection_requested", "ack", "nack", "ping",
  "subagent_sync_enable", "subagent_lifecycle_snapshot", "subagent_lifecycle_event", "subagent_state_resync",
] as const satisfies readonly string[];

export const serverFrameNames = [
  "registered", "lease_rejected", "error", "pong", "message", "conversation_message", "reminders_changed",
  "model_control", "model_control_result", "inline_tool_state", "inline_tool_control", "inline_tool_control_result",
  "goal_control", "goal_control_result", "reminder_lock_control", "reminder_lock_control_result", "agent_goal_state", "delivery_acknowledged",
  "agent_activity", "agent_model_catalog", "message_arrival", "subagent_state_snapshot",
  "subagent_state_update", "subagent_state_replay", "subagent_lifecycle_acknowledged",
] as const satisfies readonly string[];

export const httpRoutes = [
  { methods: ["OPTIONS"], path: "/*" },
  { methods: ["GET"], path: "/v1/health" },
  { methods: ["GET"], path: "/v1/capabilities" },
  { methods: ["GET"], path: "/v1/router-log" },
  { methods: ["GET"], path: "/v1/managed-agents/{agent_id}/router-log" },
  { methods: ["GET"], path: "/v1/memory/policy" },
  { methods: ["POST"], path: "/v1/memory/rebuild" },
  { methods: ["POST"], path: "/v1/memory/lookup" },
  { methods: ["POST"], path: "/v1/memory/expand" },
  { methods: ["POST"], path: "/v1/memory/apply" },
  { methods: ["POST"], path: "/v1/memory/touch" },
  { methods: ["GET"], path: "/v1/agents" },
  { methods: ["GET"], path: "/v1/conversations" },
  { methods: ["GET"], path: "/v1/attachments/{attachment_id}" },
  { methods: ["POST"], path: "/v1/messages" },
  { methods: ["GET", "POST", "DELETE"], path: "/v1/reminders" },
  { methods: ["GET", "PATCH", "DELETE"], path: "/v1/reminders/{reminder_id}" },
  { methods: ["GET", "POST"], path: "/v1/prompt-templates" },
  { methods: ["GET", "PATCH", "DELETE"], path: "/v1/prompt-templates/{template_id}" },
  { methods: ["GET"], path: "/v1/messages/{message_id}" },
  { methods: ["POST"], path: "/v1/messages/{message_id}/retry" },
  { methods: ["DELETE"], path: "/v1/messages/{message_id}" },
  { methods: ["GET", "POST"], path: "/v1/tmux/windows" },
  { methods: ["DELETE", "PATCH"], path: "/v1/tmux/windows/{window}" },
  { methods: ["POST"], path: "/v1/tmux/windows/{window}/{action}", actions: ["keys", "start", "stop", "restart", "resume", "rename"] },
  { methods: ["GET", "POST"], path: "/v1/managed-agents" },
  { methods: ["GET"], path: "/v1/managed-directories" },
  { methods: ["GET"], path: "/v1/history" },
  { methods: ["POST"], path: "/v1/managed-agents/restart-all" },
  { methods: ["GET", "DELETE"], path: "/v1/managed-agents/{id}" },
  { methods: ["GET"], path: "/v1/managed-agents/{id}/children" },
  { methods: ["POST"], path: "/v1/managed-agents/{agent_id}/restart", actions: ["restart", "change_model"] },
  { methods: ["POST"], path: "/v1/managed-agents/{id}/reset" },
  { methods: ["GET"], path: "/v1/models" },
  { methods: ["PUT"], path: "/v1/settings/default-model" },
  { methods: ["GET"], path: "/v1/provider-auth" },
  { methods: ["POST"], path: "/v1/provider-auth/{provider}/login" },
  { methods: ["POST"], path: "/v1/provider-auth/{provider}/logout" },
  { methods: ["POST"], path: "/v1/provider-auth/operations/{id}/response" },
] as const satisfies readonly HttpRoute[];

export const uiLiveFrameNames = ["managed_agents_snapshot", "managed_agent_delta", "managed_agent_removed", "managed_children_snapshot", "history_entry", "provider_auth_prompt", "provider_auth_state"] as const satisfies readonly string[];

export const manifest = {
  version: "v1",
  http: httpRoutes,
  ui_live: { path: "/v1/ui-live", protocol: "mecha.web/v1", frames: uiLiveFrameNames },
  websocket: {
    path: "/v1/agents",
    client_frames: clientFrameNames,
    server_frames: serverFrameNames,
    schema: "protocol/v1/schema.json",
  },
} satisfies ProtocolManifest;
