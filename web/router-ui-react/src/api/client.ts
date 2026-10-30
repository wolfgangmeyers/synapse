import { createHttpClient, type RouterHttpClient } from "../transport/http-client";
import { activeRouterUrl } from "../persistence/settings-repository";
import type { ManagedAgentProjection, WorkflowSchedule } from "../types";

export function getApiClient(baseUrl?: string): RouterHttpClient {
  return createHttpClient(baseUrl ?? activeRouterUrl());
}

export async function runWorkflow(
  workflowId: string,
  options?: { variables?: Record<string, string>; cwd?: string; model?: { provider: string; id: string } },
  baseUrl?: string
): Promise<ManagedAgentProjection> {
  const client = getApiClient(baseUrl);
  return client.runWorkflow(workflowId, options);
}

export async function listWorkflowSchedules(workflowId?: string, baseUrl?: string): Promise<WorkflowSchedule[]> {
  const client = getApiClient(baseUrl);
  return client.listWorkflowSchedules(workflowId);
}

export async function createWorkflowSchedule(req: Partial<WorkflowSchedule>, baseUrl?: string): Promise<WorkflowSchedule> {
  const client = getApiClient(baseUrl);
  return client.createWorkflowSchedule(req);
}

export async function updateWorkflowSchedule(id: string, req: Partial<WorkflowSchedule>, baseUrl?: string): Promise<WorkflowSchedule> {
  const client = getApiClient(baseUrl);
  return client.updateWorkflowSchedule(id, req);
}

export async function deleteWorkflowSchedule(id: string, baseUrl?: string): Promise<void> {
  const client = getApiClient(baseUrl);
  return client.deleteWorkflowSchedule(id);
}
