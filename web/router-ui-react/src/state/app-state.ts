import type { Agent } from "./agent-projection";
import type { HistoryMessage } from "../domain/history";
import type { Reminder } from "../domain/reminders";
import type { PromptTemplate } from "../domain/prompt-templates";
import type { Arrival } from "../domain/arrivals";
import type { RouterScope } from "./scopes";

export type ConnectionStatus = "disconnected" | "connecting" | "connected" | "reconnecting" | "error";
export interface PendingControl { peer: string; instanceId?: string; leaseId?: string; connectionGeneration?: number; reminderId?: string; revision?: number; action?: string; afterCreate?: boolean }
export interface AppState extends RouterScope {
  connection: ConnectionStatus;
  connectionGeneration: number;
  instanceId?: string;
  leaseId?: string;
  agents: Agent[];
  reminders: Reminder[];
  templates: PromptTemplate[];
  selectedPeer?: string;
  filter: string;
  messagesByPeer: Record<string, HistoryMessage[]>;
  pendingSends: Record<string, string>;
  pendingControls: Map<string, PendingControl>;
  modelProjections: Map<string, Agent>;
  lifecycleVersion: number;
  arrivals: Map<string, Arrival>;
  expandedSubagents: Set<string>;
  openDialog?: string;
  drafts: Record<string, string>;
  reminderLocks: Map<string, boolean>;
}
export function createInitialState(scope: RouterScope, connection: ConnectionStatus = "disconnected"): AppState { return { ...scope, connection, connectionGeneration: 0, agents: [], reminders: [], templates: [], filter: "", messagesByPeer: {}, pendingSends: {}, pendingControls: new Map(), modelProjections: new Map(), lifecycleVersion: 0, arrivals: new Map(), expandedSubagents: new Set(), drafts: {}, reminderLocks: new Map() }; }
