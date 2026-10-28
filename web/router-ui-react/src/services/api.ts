import { activeRouterUrl } from "../persistence/settings-repository";
import { createHttpClient, type ConversationTimelineResponse, type ConversationTimelineEvent } from "../transport/http-client";

export async function fetchConversationTimeline(
  agentId: string,
  limit = 50,
  beforeSeq?: number,
  baseUrl?: string
): Promise<ConversationTimelineResponse> {
  const client = createHttpClient(baseUrl ?? activeRouterUrl());
  return client.fetchConversationTimeline(agentId, limit, beforeSeq);
}

export type { ConversationTimelineResponse, ConversationTimelineEvent };
