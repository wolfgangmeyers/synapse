export const ARRIVAL_VISIBLE_MS = 60_000;
export const ARRIVAL_SOURCE_KINDS = new Set(["agent", "discord", "reminder"]);
export type ArrivalSourceKind = "agent" | "discord" | "reminder";
export interface Arrival { agent: string; from: string; subject?: string; sourceKind: ArrivalSourceKind; preview: string; body: string; createdAt?: string; messageId?: string; expiresAtMs: number }
export interface ArrivalEvent { agent?: unknown; from?: unknown; subject?: unknown; sourceKind?: unknown; preview?: unknown; body?: unknown; createdAt?: unknown; messageId?: unknown }
function sourceKind(value: unknown): value is ArrivalSourceKind { return value === "agent" || value === "discord" || value === "reminder"; }
export function createArrival(event: ArrivalEvent, uiAgentId: string, now = Date.now()): Arrival | undefined { if (typeof event.agent !== "string" || !event.agent || event.agent === uiAgentId || !sourceKind(event.sourceKind)) return undefined; return { agent: event.agent, from: typeof event.from === "string" && event.from ? event.from : "Unknown sender", ...(typeof event.subject === "string" ? { subject: event.subject } : {}), sourceKind: event.sourceKind, preview: typeof event.preview === "string" ? event.preview : "", body: typeof event.body === "string" ? event.body : "", ...(typeof event.createdAt === "string" ? { createdAt: event.createdAt } : {}), ...(typeof event.messageId === "string" ? { messageId: event.messageId } : {}), expiresAtMs: now + ARRIVAL_VISIBLE_MS }; }
export function replaceArrival(arrivals: Map<string, Arrival>, arrival: Arrival): Map<string, Arrival> { arrivals.set(arrival.agent, arrival); return arrivals; }
export function expireArrivals(arrivals: Map<string, Arrival>, now = Date.now()): Map<string, Arrival> { for (const [agent, arrival] of arrivals) if (arrival.expiresAtMs <= now) arrivals.delete(agent); return arrivals; }
export function dismissArrival(arrivals: Map<string, Arrival>, agent: string): Map<string, Arrival> { arrivals.delete(agent); return arrivals; }
export const replaceAgentArrival = replaceArrival;
