import type { ContextUsage } from "../protocol/types";

export function formatCompactTokens(tokens: number): string {
  if (tokens <= 0) return "0";
  if (tokens >= 1_000_000) {
    const m = tokens / 1_000_000;
    // e.g. 1_048_576 -> 1M, 1_500_000 -> 1.5M, 2_000_000 -> 2M
    return Number.isInteger(m) || tokens === 1_048_576 ? `${Math.round(m)}M` : `${Number(m.toFixed(1))}M`;
  }
  if (tokens >= 1_000) {
    return `${Math.round(tokens / 1_000)}k`;
  }
  return tokens.toString();
}

export function formatContextUsageLabel(usage: ContextUsage): string {
  const compactWindow = formatCompactTokens(usage.context_window);
  if (usage.percent === null || usage.percent === undefined) {
    return `- / ${compactWindow}`;
  }
  return `${Math.round(usage.percent)}% of ${compactWindow}`;
}

export function formatContextUsageTooltip(usage: ContextUsage): string {
  const tokensText = usage.tokens !== null && usage.tokens !== undefined ? usage.tokens.toLocaleString() : "Unknown";
  const windowText = usage.context_window.toLocaleString();
  const pctText = usage.percent !== null && usage.percent !== undefined ? `${usage.percent.toFixed(1)}%` : "N/A";
  return `${tokensText} / ${windowText} tokens (${pctText})`;
}

export function contextUsageSeverityClass(percent: number | null | undefined): "normal" | "warning" | "critical" {
  if (percent === null || percent === undefined || percent < 50) {
    return "normal";
  }
  if (percent < 80) {
    return "warning";
  }
  return "critical";
}
