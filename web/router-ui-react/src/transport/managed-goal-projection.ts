import type { GoalState } from "../domain/goals";
import type { ManagedAgent } from "../protocol/types";
import type { RouterHttpClient } from "./http-client";

export interface ManagedGoalProjection {
  agentId: string;
  goalState: GoalState;
}

// Goal status lives in each managed Pi runtime rather than the durable worker
// record. Refresh it alongside the durable snapshot so shared agent indicators
// survive a page reload or UI-live reconnect without turning that status into
// a public runtime detail.
export async function loadManagedGoalProjection(client: Pick<RouterHttpClient, "managedGoal">, agents: readonly ManagedAgent[]): Promise<ManagedGoalProjection[]> {
  const results = await Promise.all(agents.filter((agent) => agent.accepting_prompts).map(async (agent) => {
    try {
      const result = await client.managedGoal(agent.agent_id);
      return { agentId: agent.agent_id, goalState: result.goal_state };
    } catch {
      // A stopped or restarting worker cannot answer status controls. Retain
      // the last known value until its next successful managed snapshot read.
      return undefined;
    }
  }));
  return results.flatMap((result) => result === undefined ? [] : [result]);
}
