/**
 * Deterministic alien text generation system based on FNV-1a hashing.
 */

// Load all alien symbol PNG assets eagerly via Vite glob
const symbolModules = import.meta.glob<string>("../../alien-symbols/*.png", {
  eager: true,
  import: "default",
  query: "?url",
});

export interface AlienSymbolAsset {
  id: string; // e.g. "symbol001"
  src: string;
}

// Sorted list of alien symbols for deterministic indexing
export const alienSymbolAssets: AlienSymbolAsset[] = Object.entries(symbolModules)
  .map(([path, src]) => {
    const filename = path.split("/").pop() ?? "";
    const id = filename.replace(/\.[^/.]+$/, "");
    return { id, src };
  })
  .sort((a, b) => a.id.localeCompare(b.id));

/**
 * 32-bit Mulberry32 PRNG seeded with an integer.
 * Produces high-quality uniform pseudorandom numbers in [0, 1).
 */
export function mulberry32(seed: number): () => number {
  let s = seed >>> 0;
  return function next(): number {
    s = (s + 0x6d2b79f5) >>> 0;
    let t = Math.imul(s ^ (s >>> 15), 1 | s);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

/**
 * 32-bit FNV-1a hash function matching AgentAvatar.tsx.
 */
export function hashAlienInput(input: string | number): number {
  const str = String(input);
  let hash = 0x811c9dc5;
  for (let index = 0; index < str.length; index += 1) {
    hash ^= str.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return hash >>> 0;
}

export interface AlienWordToken {
  type: "symbol";
  asset: AlienSymbolAsset;
}

export interface AlienSpaceToken {
  type: "space";
}

export type AlienToken = AlienWordToken | AlienSpaceToken;

/**
 * Deterministically generates a sequence of alien tokens (symbols and spaces) from an input seed.
 * Total symbol count is chosen deterministically between minLength and maxLength (default: 3-7).
 * Word spaces are deterministically placed between adjacent symbols using a seeded PRNG stream,
 * producing diverse word patterns (single-word, 2-word, or 3-word combinations).
 */
export function generateAlienTokens(
  input: string | number,
  minLength = 3,
  maxLength = 7
): AlienToken[] {
  if (alienSymbolAssets.length === 0) {
    return [];
  }
  const min = Math.max(1, Math.min(minLength, maxLength));
  const max = Math.max(min, maxLength);
  const range = max - min + 1;

  const seed = hashAlienInput(input);
  const prng = mulberry32(seed);

  // Deterministic total symbol count uniformly in [min, max]
  const symbolCount = min + Math.floor(prng() * range);

  // Deterministically select alien symbol assets
  const symbols: AlienSymbolAsset[] = [];
  for (let i = 0; i < symbolCount; i += 1) {
    const symbolIndex = Math.floor(prng() * alienSymbolAssets.length);
    const asset = alienSymbolAssets[symbolIndex];
    if (asset) {
      symbols.push(asset);
    }
  }

  // Roll for spaces between symbols (indices 0 to symbolCount - 2).
  // Non-consecutive spaces prevent empty words.
  // Space probability is ~20% for short strings (count <= 3) and ~30% for count >= 4.
  const spacePositions = new Set<number>();
  let lastSpace = -2;
  const spaceProb = symbolCount <= 3 ? 0.2 : 0.3;

  for (let i = 0; i < symbols.length - 1; i += 1) {
    if (i - lastSpace > 1 && prng() < spaceProb) {
      spacePositions.add(i);
      lastSpace = i;
    }
  }

  const tokens: AlienToken[] = [];
  for (let i = 0; i < symbols.length; i += 1) {
    tokens.push({ type: "symbol", asset: symbols[i]! });
    if (spacePositions.has(i) && i < symbols.length - 1) {
      tokens.push({ type: "space" });
    }
  }

  return tokens;
}

/**
 * Deterministically generates a sequence of alien symbols from an input seed (string or number).
 * Length is chosen deterministically between minLength and maxLength.
 */
export function generateAlienText(
  input: string | number,
  minLength = 3,
  maxLength = 7
): AlienSymbolAsset[] {
  const tokens = generateAlienTokens(input, minLength, maxLength);
  return tokens
    .filter((token): token is AlienWordToken => token.type === "symbol")
    .map((token) => token.asset);
}

/**
 * Pool of at least 32 distinct action card colors matching the saturation and lightness
 * of the brightAvatarColors / action styling in app.css.
 */
export const actionCardColors = [
  "#e56991", // rose
  "#e18156", // coral
  "#e2c35c", // gold
  "#92cc5d", // lime
  "#47c2ac", // mint
  "#53bae1", // sky
  "#8699ea", // periwinkle
  "#ae85ea", // violet
  "#db7ae7", // orchid
  "#e05252", // light red
  "#e78b79", // salmon
  "#e6ab70", // apricot
  "#dfad49", // amber
  "#e7de79", // yellow
  "#c7e04d", // chartreuse
  "#c0e774", // light lime
  "#93e25a", // spring green
  "#91e774", // bright green
  "#56e156", // green
  "#67e487", // emerald
  "#52e099", // jade
  "#70e6be", // seafoam
  "#56e1d6", // turquoise
  "#67e4e4", // cyan
  "#52c9e0", // light ocean
  "#74b7e7", // light azure
  "#5a93e2", // cornflower
  "#6c8ae5", // royal
  "#5656e1", // indigo
  "#8470e6", // purple
  "#a552e0", // deep orchid
  "#c76ce5", // magenta
  "#e156d6", // fuchsia
  "#e774ca", // bubblegum
  "#e25aa9", // hot pink
  "#e87d8f", // blush
] as const;

export interface ActionColor {
  hex: string;
  rgb: string; // "r, g, b" format for CSS variables
}

function hexToRgbString(hex: string): string {
  const r = parseInt(hex.slice(1, 3), 16);
  const g = parseInt(hex.slice(3, 5), 16);
  const b = parseInt(hex.slice(5, 7), 16);
  return `${r}, ${g}, ${b}`;
}

/**
 * Deterministically picks a color from the 36-color pool for a given action type or seed string.
 */
export function colorForAction(actionType: string): ActionColor {
  const hash = hashAlienInput(`${actionType}\0action-color`);
  const hex = actionCardColors[hash % actionCardColors.length] ?? actionCardColors[0];
  return {
    hex,
    rgb: hexToRgbString(hex),
  };
}
