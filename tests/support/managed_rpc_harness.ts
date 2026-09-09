import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

export interface ProviderScenario {
  provider: "openai-codex" | "google" | "cursor";
  label: "ChatGPT" | "Google" | "Cursor";
  login_prompt: { kind: "manual_code" | "secret"; message: string };
  model: { id: string; name: string };
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// The release fixture contains only public provider labels and model metadata.
// Keeping its parsing here makes every auth test reject a fixture that grows a
// credential field before it can reach an isolated router process.
export async function loadProviderScenarios(root: string): Promise<readonly ProviderScenario[]> {
  const raw: unknown = JSON.parse(await readFile(resolve(root, "tests/fixtures/mecha-rpc/provider-scenarios.json"), "utf8"));
  if (!isRecord(raw) || raw.schema !== "mecha.rpc.provider-scenarios/v1" || !Array.isArray(raw.providers) || raw.providers.length !== 3) throw new Error("provider fixture is invalid");
  const providers = raw.providers.map((candidate): ProviderScenario => {
    if (!isRecord(candidate) || Object.keys(candidate).some((key) => !["provider", "label", "login_prompt", "model"].includes(key)) || (candidate.provider !== "openai-codex" && candidate.provider !== "google" && candidate.provider !== "cursor") || (candidate.label !== "ChatGPT" && candidate.label !== "Google" && candidate.label !== "Cursor") || !isRecord(candidate.login_prompt) || Object.keys(candidate.login_prompt).some((key) => !["kind", "message"].includes(key)) || (candidate.login_prompt.kind !== "manual_code" && candidate.login_prompt.kind !== "secret") || typeof candidate.login_prompt.message !== "string" || !isRecord(candidate.model) || Object.keys(candidate.model).some((key) => !["id", "name"].includes(key)) || typeof candidate.model.id !== "string" || typeof candidate.model.name !== "string") throw new Error("provider fixture row is invalid");
    return { provider: candidate.provider, label: candidate.label, login_prompt: { kind: candidate.login_prompt.kind, message: candidate.login_prompt.message }, model: { id: candidate.model.id, name: candidate.model.name } };
  });
  if (new Set(providers.map((provider) => provider.provider)).size !== 3) throw new Error("provider fixture is not a bounded non-secret catalog");
  return providers;
}

export function acceptanceRunDirectory(head: string, runID: string): string {
  if (!/^[0-9a-f]{40}$/u.test(head) || !/^[A-Za-z0-9._-]{1,128}$/u.test(runID)) throw new Error("acceptance evidence identity is invalid");
  return `/tmp/mecha-rpc-acceptance/${head}/${runID}`;
}
