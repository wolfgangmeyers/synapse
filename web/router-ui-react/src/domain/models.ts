export interface ModelSummary { provider: string; id: string; name?: string }
export interface ModelProjection { currentModel?: ModelSummary; availableModels?: ModelSummary[]; totalCount?: number; truncated?: boolean }
export interface AgentModelFields { current_model?: ModelSummary; available_models?: ModelSummary[]; model_catalog_total_count: number; model_catalog_truncated: boolean }
export interface ModelAgent { agent_id: string; instance_id?: string; current_model?: unknown; available_models?: unknown; model_catalog_total_count?: number; model_catalog_truncated?: boolean; [key: string]: unknown }
const MAX_MODEL_SUMMARIES = 1_000;
const REJECTIONS = new Set(["invalid_model", "model_registry_failed", "model_registry_unavailable", "model_switch_failed", "model_switch_rejected", "model_switch_unavailable", "model_unavailable", "model_control_not_allowed", "unsupported_model_control"]);
function isRecord(value: unknown): value is Record<string, unknown> { return value !== null && typeof value === "object" && !Array.isArray(value); }
function safeInteger(value: unknown): value is number { return typeof value === "number" && Number.isSafeInteger(value); }
function stringValue(value: unknown, max: number): string { if (typeof value !== "string") return ""; const normalized = value.trim(); return normalized && [...normalized].length <= max ? normalized : ""; }
export function normalizeModelSummary(value: unknown): ModelSummary | undefined { if (!isRecord(value)) return undefined; const provider = stringValue(value.provider, 128); const id = stringValue(value.id, 256); if (!provider || !id) return undefined; const name = stringValue(value.name, 256); return name ? { provider, id, name } : { provider, id }; }
export function normalizeModelSummaries(values: unknown): ModelSummary[] { if (!Array.isArray(values)) return []; const seen = new Set<string>(); const result: ModelSummary[] = []; for (const value of values) { const model = normalizeModelSummary(value); if (!model) continue; const key = `${model.provider}\u0000${model.id}`; if (seen.has(key)) continue; seen.add(key); result.push(model); if (result.length === MAX_MODEL_SUMMARIES) break; } return result; }
export function modelKey(model: ModelSummary): string { return `${model.provider}/${model.id}`; }
export function modelOptionLabel(model: ModelSummary): string { return model.name ? `${model.name} (${model.provider})` : modelKey(model); }
export function mergeAgentModelSnapshot(current: ModelAgent | undefined, incoming: ModelAgent, options: { snapshotGeneratedAtMs?: number; modelControlUpdatedAtMs?: number; preserveEmptyCatalog?: boolean } = {}): AgentModelFields {
  const sameInstance = current !== undefined && (incoming.instance_id === undefined || current.instance_id === undefined || incoming.instance_id === current.instance_id);
  const snapshotTime = options.snapshotGeneratedAtMs; const controlTime = options.modelControlUpdatedAtMs;
  const stale = sameInstance && typeof snapshotTime === "number" && Number.isFinite(snapshotTime) && typeof controlTime === "number" && Number.isFinite(controlTime) && snapshotTime < controlTime;
  const currentProvided = Object.hasOwn(incoming, "current_model"); const modelsProvided = Object.hasOwn(incoming, "available_models");
  const currentModel = stale ? normalizeModelSummary(current?.current_model) : currentProvided ? normalizeModelSummary(incoming.current_model) : sameInstance ? normalizeModelSummary(current?.current_model) : undefined;
  const incomingModels = modelsProvided ? normalizeModelSummaries(incoming.available_models) : [];
  const catalogDeferred = options.preserveEmptyCatalog === true && incomingModels.length === 0 && current?.available_models !== undefined;
  const models = stale ? normalizeModelSummaries(current?.available_models) : modelsProvided && !catalogDeferred ? incomingModels : sameInstance ? normalizeModelSummaries(current?.available_models) : [];
  const oldTotal = current?.model_catalog_total_count; const incomingTotal = incoming.model_catalog_total_count;
  const totalCount = stale || catalogDeferred ? Math.max(models.length, safeInteger(oldTotal) ? oldTotal : models.length) : modelsProvided ? Math.max(models.length, safeInteger(incomingTotal) ? incomingTotal : models.length) : sameInstance ? oldTotal ?? 0 : 0;
  const truncated = stale || catalogDeferred ? current?.model_catalog_truncated === true : modelsProvided ? incoming.model_catalog_truncated === true : sameInstance ? current?.model_catalog_truncated === true : false;
  return { ...(currentModel ? { current_model: currentModel } : {}), ...(modelsProvided || stale || current?.available_models !== undefined ? { available_models: models } : {}), model_catalog_total_count: totalCount, model_catalog_truncated: truncated };
}
export function mergeAgentModelProjection(agents: ModelAgent[], peer: string, instanceId: string | undefined, projection: ModelProjection, options: { replaceInstance?: boolean } = {}): { agents: ModelAgent[]; currentModel?: ModelSummary; changed: boolean } {
  if (!peer) return { agents, changed: false }; const current = agents.find((agent) => agent.agent_id === peer) ?? { agent_id: peer }; const sameInstance = instanceId === undefined || current.instance_id === undefined || current.instance_id === instanceId;
  if (!sameInstance && options.replaceInstance === false) { const currentModel = normalizeModelSummary(current.current_model); return { agents, ...(currentModel ? { currentModel } : {}), changed: false }; }
  const base = sameInstance ? current : { agent_id: peer, instance_id: instanceId }; const modelProvided = Object.hasOwn(projection, "currentModel"); const modelsProvided = Object.hasOwn(projection, "availableModels");
  const currentModel = modelProvided ? normalizeModelSummary(projection.currentModel) : sameInstance ? normalizeModelSummary(base.current_model) : undefined; const models = modelsProvided ? normalizeModelSummaries(projection.availableModels) : sameInstance ? normalizeModelSummaries(base.available_models) : [];
  const totalCount = safeInteger(projection.totalCount) ? Math.max(models.length, projection.totalCount) : modelsProvided ? models.length : base.model_catalog_total_count ?? models.length; const truncated = Object.hasOwn(projection, "truncated") ? projection.truncated === true : sameInstance && base.model_catalog_truncated === true;
  const next: ModelAgent = { ...base, ...(instanceId ? { instance_id: instanceId } : {}), ...(currentModel ? { current_model: currentModel } : {}), ...(modelsProvided || base.available_models !== undefined ? { available_models: models } : {}), model_catalog_total_count: totalCount, model_catalog_truncated: truncated };
  const changed = JSON.stringify(next) !== JSON.stringify(current); return { agents: agents.some((agent) => agent.agent_id === peer) ? agents.map((agent) => agent.agent_id === peer ? next : agent) : [...agents, next], ...(currentModel ? { currentModel } : {}), changed };
}
export function modelControlStatusTitle(accepted: unknown, errorCode?: string): string { if (accepted === true) return "Model changed"; return REJECTIONS.has(errorCode ?? "") ? "Model change rejected" : "Model change not confirmed"; }
export function isConfirmedModelRejection(errorCode: unknown): boolean { return typeof errorCode === "string" && REJECTIONS.has(errorCode); }
