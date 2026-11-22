import type { ReactElement } from "react";

const avatarModules = import.meta.glob<string>("../../avatars/*.png", {
  eager: true,
  import: "default",
  query: "?url",
});

const avatarAssets = Object.entries(avatarModules).map(([key, src]) => ({ key, src }));

const brightAvatarColors = [
  "#e56991",
  "#e18156",
  "#e2c35c",
  "#92cc5d",
  "#47c2ac",
  "#53bae1",
  "#8699ea",
  "#ae85ea",
  "#db7ae7",
  "#e05252",
  "#e78b79",
  "#e6ab70",
  "#dfad49",
  "#e7de79",
  "#c7e04d",
  "#c0e774",
  "#93e25a",
  "#91e774",
  "#56e156",
  "#67e487",
  "#52e099",
  "#70e6be",
  "#56e1d6",
  "#67e4e4",
  "#52c9e0",
  "#74b7e7",
  "#5a93e2",
  "#6c8ae5",
  "#5656e1",
  "#8470e6",
  "#a552e0",
  "#c76ce5",
  "#e156d6",
  "#e774ca",
  "#e25aa9",
  "#e87d8f",
] as const;

function hashAgentName(agentName: string): number {
  let hash = 0x811c9dc5;
  for (let index = 0; index < agentName.length; index += 1) {
    hash ^= agentName.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

function avatarCandidateScore(agentName: string, assetKey: string): number {
  return hashAgentName(`agent-avatar/image/v1\0${JSON.stringify([agentName, assetKey])}`);
}

export function selectRendezvousAvatarKey(agentName: string, assetKeys: readonly string[]): string {
  const firstKey = assetKeys[0];
  if (firstKey === undefined) throw new Error("No bundled agent avatars were found");
  let winner = firstKey;
  let winningScore = avatarCandidateScore(agentName, firstKey);
  for (const assetKey of assetKeys.slice(1)) {
    const score = avatarCandidateScore(agentName, assetKey);
    if (score > winningScore || (score === winningScore && assetKey < winner)) {
      winner = assetKey;
      winningScore = score;
    }
  }
  return winner;
}

export interface AgentAvatarAppearance {
  src: string;
  color: string;
}

export function avatarForAgent(agentName: string): AgentAvatarAppearance {
  const assetKey = selectRendezvousAvatarKey(agentName, avatarAssets.map(({ key }) => key));
  const asset = avatarAssets.find(({ key }) => key === assetKey);
  if (asset === undefined) throw new Error("Selected agent avatar was not found");
  const colorHash = hashAgentName(`${agentName}\0color`);
  return {
    src: asset.src,
    color: brightAvatarColors[colorHash % brightAvatarColors.length] ?? brightAvatarColors[0],
  };
}

export function AgentAvatar({ agentId, agent }: { agentId?: string; agent?: { agent_id: string } }): ReactElement {
  const id = agentId ?? agent?.agent_id ?? "agent";
  const appearance = avatarForAgent(id);
  return (
    <span className="agent-avatar-image">
      <img src={appearance.src} alt="" />
      <span
        className="agent-avatar-recolor"
        style={{
          backgroundColor: appearance.color,
          maskImage: `url("${appearance.src}")`,
          WebkitMaskImage: `url("${appearance.src}")`,
        }}
      />
    </span>
  );
}
