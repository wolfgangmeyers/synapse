import { useState, useEffect, type ReactElement } from "react";

export interface ScheduleCronPickerProps {
  value: string;
  onChange: (cronExpression: string) => void;
  disabled?: boolean;
}

const PRESETS: Array<{ label: string; expr: string; desc: string }> = [
  { label: "Every 5 minutes", expr: "*/5 * * * *", desc: "Runs every 5 minutes" },
  { label: "Every 15 minutes", expr: "*/15 * * * *", desc: "Runs every 15 minutes" },
  { label: "Every 30 minutes", expr: "*/30 * * * *", desc: "Runs every 30 minutes" },
  { label: "Hourly", expr: "0 * * * *", desc: "Runs at minute 0 of every hour" },
  { label: "Every 2 hours", expr: "0 */2 * * *", desc: "Runs every 2 hours" },
  { label: "Daily (Midnight)", expr: "0 0 * * *", desc: "Runs every day at 00:00" },
  { label: "Daily (Morning 9 AM)", expr: "0 9 * * *", desc: "Runs every day at 09:00" },
  { label: "Weekdays (9 AM)", expr: "0 9 * * 1-5", desc: "Runs Monday through Friday at 09:00" },
  { label: "Weekly (Monday Midnight)", expr: "0 0 * * 1", desc: "Runs every Monday at 00:00" },
  { label: "Monthly (1st of month)", expr: "0 0 1 * *", desc: "Runs on the 1st of every month at 00:00" },
];

export function describeCron(cron: string | null | undefined): string {
  if (!cron || !cron.trim()) return "No cron expression specified";
  const trimmed = cron.trim();

  // Check direct preset matches
  const preset = PRESETS.find((p) => p.expr === trimmed);
  if (preset) return preset.desc;

  const parts = trimmed.split(/\s+/);
  if (parts.length !== 5) {
    return `Custom expression (${trimmed})`;
  }

  const [min, hour, dom, month, dow] = parts;

  // Pattern: Every X minutes
  if (min?.startsWith("*/") && hour === "*" && dom === "*" && month === "*" && dow === "*") {
    const step = min.slice(2);
    return `Runs every ${step} minutes`;
  }

  // Pattern: Every hour at minute X
  if (min && !min.includes("*") && hour === "*" && dom === "*" && month === "*" && dow === "*") {
    return `Runs at minute ${min} of every hour`;
  }

  // Pattern: Every X hours at minute Y
  if (hour?.startsWith("*/") && dom === "*" && month === "*" && dow === "*") {
    const step = hour.slice(2);
    return `Runs every ${step} hours at minute ${min}`;
  }

  // Pattern: Daily at HH:MM
  if (min && hour && !min.includes("*") && !hour.includes("*") && dom === "*" && month === "*" && dow === "*") {
    const padHour = hour.padStart(2, "0");
    const padMin = min.padStart(2, "0");
    return `Runs every day at ${padHour}:${padMin}`;
  }

  // Pattern: Weekdays at HH:MM
  if (min && hour && !min.includes("*") && !hour.includes("*") && dom === "*" && month === "*" && (dow === "1-5" || dow === "MON-FRI")) {
    const padHour = hour.padStart(2, "0");
    const padMin = min.padStart(2, "0");
    return `Runs Monday through Friday at ${padHour}:${padMin}`;
  }

  // Pattern: Weekly on specific day
  const DAYS: Record<string, string> = {
    "0": "Sunday",
    "7": "Sunday",
    "1": "Monday",
    "2": "Tuesday",
    "3": "Wednesday",
    "4": "Thursday",
    "5": "Friday",
    "6": "Saturday",
    SUN: "Sunday",
    MON: "Monday",
    TUE: "Tuesday",
    WED: "Wednesday",
    THU: "Thursday",
    FRI: "Friday",
    SAT: "Saturday",
  };
  if (min && hour && !min.includes("*") && !hour.includes("*") && dom === "*" && month === "*" && dow && DAYS[dow.toUpperCase()]) {
    const dayName = DAYS[dow.toUpperCase()];
    const padHour = hour.padStart(2, "0");
    const padMin = min.padStart(2, "0");
    return `Runs every ${dayName} at ${padHour}:${padMin}`;
  }

  return `Custom: ${trimmed}`;
}

/**
 * Converts a legacy interval in milliseconds to a cron expression if possible,
 * or returns a sensible default if not cleanly convertible.
 */
export function intervalToCron(intervalMs: number | null | undefined): string {
  if (!intervalMs || intervalMs <= 0) return "0 * * * *";
  const seconds = Math.floor(intervalMs / 1000);
  const minutes = Math.floor(seconds / 60);
  const hours = Math.floor(minutes / 60);

  if (minutes < 1) return "* * * * *"; // Min cron resolution is 1 minute
  if (minutes < 60) {
    if (minutes === 1) return "* * * * *";
    if (minutes === 5) return "*/5 * * * *";
    if (minutes === 10) return "*/10 * * * *";
    if (minutes === 15) return "*/15 * * * *";
    if (minutes === 30) return "*/30 * * * *";
    return `*/${minutes} * * * *`;
  }
  if (hours < 24) {
    if (hours === 1) return "0 * * * *";
    return `0 */${hours} * * *`;
  }
  const days = Math.floor(hours / 24);
  if (days === 1) return "0 0 * * *";
  return `0 0 */${days} * *`;
}

export function ScheduleCronPicker({
  value,
  onChange,
  disabled = false,
}: ScheduleCronPickerProps): ReactElement {
  const [mode, setMode] = useState<"preset" | "custom">(() => {
    return PRESETS.some((p) => p.expr === value) ? "preset" : "custom";
  });

  // Whenever value changes externally, update mode if it matches a preset
  useEffect(() => {
    if (PRESETS.some((p) => p.expr === value)) {
      setMode("preset");
    }
  }, [value]);

  const selectedPreset = PRESETS.find((p) => p.expr === value)?.expr ?? "";

  const handlePresetSelect = (expr: string) => {
    onChange(expr);
  };

  const handleModeChange = (newMode: "preset" | "custom") => {
    setMode(newMode);
    if (newMode === "preset" && !PRESETS.some((p) => p.expr === value)) {
      onChange(PRESETS[0] ? PRESETS[0].expr : "0 * * * *");
    }
  };

  return (
    <div className="schedule-cron-picker" style={{ display: "flex", flexDirection: "column", gap: "10px" }}>
      <div className="segmented-toggle-group" style={{ maxWidth: 260 }}>
        <button
          type="button"
          className={`segmented-toggle-btn ${mode === "preset" ? "active" : ""}`}
          onClick={() => handleModeChange("preset")}
          disabled={disabled}
        >
          Common Presets
        </button>
        <button
          type="button"
          className={`segmented-toggle-btn ${mode === "custom" ? "active" : ""}`}
          onClick={() => handleModeChange("custom")}
          disabled={disabled}
        >
          Custom Cron
        </button>
      </div>

      {mode === "preset" ? (
        <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
          <select
            className="form-control"
            value={selectedPreset}
            onChange={(e) => handlePresetSelect(e.target.value)}
            disabled={disabled}
          >
            {PRESETS.map((p) => (
              <option key={p.expr} value={p.expr}>
                {p.label} ({p.expr})
              </option>
            ))}
          </select>
        </div>
      ) : (
        <div style={{ display: "flex", flexDirection: "column", gap: "6px" }}>
          <input
            type="text"
            className="form-control"
            placeholder="e.g. 0 0 * * * (min hour day month dow)"
            value={value}
            onChange={(e) => onChange(e.target.value)}
            disabled={disabled}
            required
          />
          <small style={{ color: "var(--muted)", fontSize: "0.75rem" }}>
            Format: <code>minute hour day-of-month month day-of-week</code>
          </small>
        </div>
      )}

      {/* Human-readable description badge / pill */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          gap: "8px",
          padding: "8px 12px",
          borderRadius: "8px",
          background: "rgba(99, 102, 241, 0.12)",
          border: "1px solid rgba(99, 102, 241, 0.25)",
          color: "#c7d2fe",
          fontSize: "0.82rem",
        }}
      >
        <i className="fa-solid fa-clock" aria-hidden="true" style={{ color: "#818cf8" }} />
        <span>{describeCron(value)}</span>
      </div>
    </div>
  );
}
