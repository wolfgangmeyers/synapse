import type { ReactElement } from "react";
import { generateAlienTokens, type AlienToken } from "../utils/alienText";

export interface AlienTextProps {
  /** Input seed (e.g. action id, entry id, or tool name) */
  input: string | number;
  minLength?: number;
  maxLength?: number;
  color?: string;
  className?: string;
}

export function AlienText({
  input,
  minLength = 3,
  maxLength = 7,
  color,
  className = "",
}: AlienTextProps): ReactElement | null {
  const tokens = generateAlienTokens(input, minLength, maxLength);
  if (tokens.length === 0) return null;

  return (
    <span
      className={`alien-text ${className}`.trim()}
      aria-hidden="true"
      style={color ? { color } : undefined}
    >
      {tokens.map((token: AlienToken, idx: number) => {
        if (token.type === "space") {
          return <span key={`space-${idx}`} className="alien-space" />;
        }
        const sym = token.asset;
        return (
          <span
            key={`${sym.id}-${idx}`}
            className="alien-symbol"
            style={{
              maskImage: `url("${sym.src}")`,
              WebkitMaskImage: `url("${sym.src}")`,
              backgroundColor: color ? "currentColor" : undefined,
            }}
          />
        );
      })}
    </span>
  );
}
