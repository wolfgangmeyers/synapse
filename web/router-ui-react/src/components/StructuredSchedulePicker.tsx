import { type ReactElement } from "react";
import type { StructuredSchedule, IntervalSpec } from "../types";

export interface StructuredSchedulePickerProps {
  value: StructuredSchedule;
  onChange: (value: StructuredSchedule) => void;
  disabled?: boolean;
}

const DAY_LABELS = [
  { day: 1, label: "M", full: "Monday" },
  { day: 2, label: "T", full: "Tuesday" },
  { day: 3, label: "W", full: "Wednesday" },
  { day: 4, label: "T", full: "Thursday" },
  { day: 5, label: "F", full: "Friday" },
  { day: 6, label: "S", full: "Saturday" },
  { day: 0, label: "S", full: "Sunday" },
];

const PRESETS = [
  { label: "Weekdays", days: [1, 2, 3, 4, 5] },
  { label: "Weekends", days: [6, 0] },
  { label: "All", days: [1, 2, 3, 4, 5, 6, 0] },
];

export function formatTime12h(timeStr: string): string {
  const [hourStr, minStr] = timeStr.split(":");
  const h = parseInt(hourStr || "0", 10);
  const m = parseInt(minStr || "0", 10);
  const ampm = h >= 12 ? "PM" : "AM";
  const displayH = h % 12 === 0 ? 12 : h % 12;
  const padH = displayH.toString().padStart(2, "0");
  const padM = m.toString().padStart(2, "0");
  return `${padH}:${padM} ${ampm}`;
}

export function describeDays(days: number[]): string {
  if (!days || days.length === 0) return "No days selected";
  const normalized = Array.from(new Set(days)).sort((a, b) => a - b);
  if (normalized.length === 7) return "every day";

  const isWeekdays = normalized.length === 5 && [1, 2, 3, 4, 5].every((d) => normalized.includes(d));
  if (isWeekdays) return "Monday through Friday";

  const isWeekends = normalized.length === 2 && [0, 6].every((d) => normalized.includes(d));
  if (isWeekends) return "on weekends";

  const DAY_NAMES: Record<number, string> = {
    0: "Sunday",
    1: "Monday",
    2: "Tuesday",
    3: "Wednesday",
    4: "Thursday",
    5: "Friday",
    6: "Saturday",
  };

  const names = normalized.map((d) => DAY_NAMES[d] || `Day ${d}`);
  if (names.length === 1) return `every ${names[0]}`;
  if (names.length === 2) return `on ${names[0]} and ${names[1]}`;
  return `on ${names.slice(0, -1).join(", ")}, and ${names[names.length - 1]}`;
}

export function describeStructuredSchedule(schedule?: StructuredSchedule | null): string {
  if (!schedule) return "No schedule configured";

  const isEveryDay = schedule.days_mode === "every_day";
  const daysText = isEveryDay ? "" : describeDays(schedule.days || []);

  if (schedule.time_mode === "repeating_interval") {
    const val = schedule.interval?.value ?? 1;
    let unit = schedule.interval?.unit ?? "hours";
    if (val === 1 && unit.endsWith("s")) {
      unit = unit.slice(0, -1) as "minutes" | "hours" | "days";
    }
    const intervalDesc = `Runs every ${val} ${unit}`;
    if (isEveryDay) {
      return `${intervalDesc} continuously`;
    }
    return `${intervalDesc}, ${daysText}`;
  }

  // specific_times
  const times = schedule.times && schedule.times.length > 0 ? schedule.times : ["09:00"];
  const formattedTimes = times.map(formatTime12h);

  let timesText = "";
  if (formattedTimes.length === 1) {
    timesText = `at ${formattedTimes[0]}`;
  } else if (formattedTimes.length === 2) {
    timesText = `at ${formattedTimes[0]} and ${formattedTimes[1]}`;
  } else {
    timesText = `at ${formattedTimes.slice(0, -1).join(", ")}, and ${formattedTimes[formattedTimes.length - 1]}`;
  }

  if (isEveryDay) {
    return `Runs every day ${timesText}`;
  }

  return `Runs ${daysText} ${timesText}`;
}

export function StructuredSchedulePicker({
  value,
  onChange,
  disabled = false,
}: StructuredSchedulePickerProps): ReactElement {
  const daysMode = value.days_mode || "every_day";
  const days = value.days || [1, 2, 3, 4, 5];
  const timeMode = value.time_mode || "specific_times";
  const times = value.times && value.times.length > 0 ? value.times : ["09:00"];
  const interval: IntervalSpec = value.interval || { value: 1, unit: "hours" };

  const updateSchedule = (partial: Partial<StructuredSchedule>) => {
    onChange({
      ...value,
      ...partial,
    });
  };

  const handleToggleDay = (day: number) => {
    const exists = days.includes(day);
    let nextDays: number[];
    if (exists) {
      if (days.length <= 1) return; // keep at least 1 day selected
      nextDays = days.filter((d) => d !== day);
    } else {
      nextDays = [...days, day];
    }
    updateSchedule({ days: nextDays });
  };

  const handleApplyPreset = (presetDays: number[]) => {
    updateSchedule({ days: [...presetDays] });
  };

  const handleAddTime = () => {
    updateSchedule({ times: [...times, "12:00"] });
  };

  const handleRemoveTime = (index: number) => {
    if (times.length <= 1) return;
    const nextTimes = times.filter((_, i) => i !== index);
    updateSchedule({ times: nextTimes });
  };

  const handleTimeChange = (index: number, newTime: string) => {
    const nextTimes = [...times];
    nextTimes[index] = newTime;
    updateSchedule({ times: nextTimes });
  };

  const handleIntervalValueChange = (valStr: string) => {
    const parsed = parseInt(valStr, 10);
    const val = Number.isNaN(parsed) || parsed < 1 ? 1 : parsed;
    updateSchedule({ interval: { ...interval, value: val } });
  };

  const handleIntervalUnitChange = (unit: "minutes" | "hours" | "days") => {
    updateSchedule({ interval: { ...interval, unit } });
  };

  return (
    <div className="structured-schedule-picker" style={{ display: "flex", flexDirection: "column", gap: "16px" }}>
      {/* 1. Days Axis */}
      <div className="schedule-section-days" style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
        <span style={{ fontSize: "0.85rem", fontWeight: 600, color: "var(--text-bright, #fff)" }}>
          Days
        </span>
        <div className="segmented-toggle-group" style={{ maxWidth: 320 }}>
          <button
            type="button"
            className={`segmented-toggle-btn ${daysMode === "every_day" ? "active" : ""}`}
            onClick={() => updateSchedule({ days_mode: "every_day" })}
            disabled={disabled}
          >
            Every day
          </button>
          <button
            type="button"
            className={`segmented-toggle-btn ${daysMode === "specific_days" ? "active" : ""}`}
            onClick={() => updateSchedule({ days_mode: "specific_days" })}
            disabled={disabled}
          >
            Specific days
          </button>
        </div>

        {daysMode === "specific_days" && (
          <div
            className="specific-days-controls"
            style={{
              display: "flex",
              flexDirection: "column",
              gap: "8px",
              marginTop: "4px",
              padding: "10px",
              background: "rgba(13, 20, 36, 0.45)",
              borderRadius: "8px",
              border: "1px solid rgba(255, 255, 255, 0.08)",
            }}
          >
            <div style={{ display: "flex", gap: "6px", alignItems: "center" }}>
              {DAY_LABELS.map(({ day, label, full }) => {
                const isSelected = days.includes(day);
                return (
                  <button
                    key={day}
                    type="button"
                    title={full}
                    aria-label={full}
                    onClick={() => handleToggleDay(day)}
                    disabled={disabled}
                    style={{
                      width: "32px",
                      height: "32px",
                      borderRadius: "999px",
                      border: isSelected
                        ? "1.5px solid #38bdf8"
                        : "1px solid rgba(255, 255, 255, 0.15)",
                      background: isSelected
                        ? "rgba(56, 189, 248, 0.25)"
                        : "rgba(15, 23, 42, 0.6)",
                      color: isSelected ? "#38bdf8" : "var(--muted, #94a3b8)",
                      boxShadow: isSelected
                        ? "0 0 10px rgba(56, 189, 248, 0.45)"
                        : "none",
                      fontWeight: 600,
                      cursor: disabled ? "not-allowed" : "pointer",
                      display: "flex",
                      alignItems: "center",
                      justifyContent: "center",
                      fontSize: "0.85rem",
                      transition: "all 0.15s ease",
                    }}
                  >
                    {label}
                  </button>
                );
              })}
            </div>

            <div style={{ display: "flex", gap: "8px", alignItems: "center", fontSize: "0.8rem" }}>
              <span style={{ color: "var(--muted, #94a3b8)" }}>Presets:</span>
              {PRESETS.map((p) => (
                <button
                  key={p.label}
                  type="button"
                  onClick={() => handleApplyPreset(p.days)}
                  disabled={disabled}
                  style={{
                    background: "none",
                    border: "none",
                    padding: 0,
                    color: "#93c5fd",
                    cursor: disabled ? "not-allowed" : "pointer",
                    textDecoration: "underline",
                    fontSize: "0.8rem",
                  }}
                >
                  {p.label}
                </button>
              ))}
            </div>
          </div>
        )}
      </div>

      {/* 2. Time Axis */}
      <div className="schedule-section-time" style={{ display: "flex", flexDirection: "column", gap: "8px" }}>
        <span style={{ fontSize: "0.85rem", fontWeight: 600, color: "var(--text-bright, #fff)" }}>
          Timing
        </span>
        <div className="segmented-toggle-group" style={{ maxWidth: 320 }}>
          <button
            type="button"
            className={`segmented-toggle-btn ${timeMode === "specific_times" ? "active" : ""}`}
            onClick={() => updateSchedule({ time_mode: "specific_times" })}
            disabled={disabled}
          >
            At specific times
          </button>
          <button
            type="button"
            className={`segmented-toggle-btn ${timeMode === "repeating_interval" ? "active" : ""}`}
            onClick={() => updateSchedule({ time_mode: "repeating_interval" })}
            disabled={disabled}
          >
            Repeating interval
          </button>
        </div>

        {timeMode === "specific_times" ? (
          <div
            className="specific-times-container"
            style={{
              display: "flex",
              flexDirection: "column",
              gap: "8px",
              marginTop: "4px",
              padding: "10px",
              background: "rgba(13, 20, 36, 0.45)",
              borderRadius: "8px",
              border: "1px solid rgba(255, 255, 255, 0.08)",
            }}
          >
            {times.map((t, idx) => (
              <div key={idx} style={{ display: "flex", alignItems: "center", gap: "8px" }}>
                <input
                  type="time"
                  className="form-control"
                  style={{ maxWidth: "160px" }}
                  value={t}
                  onChange={(e) => handleTimeChange(idx, e.target.value)}
                  disabled={disabled}
                  required
                />
                {times.length > 1 && (
                  <button
                    type="button"
                    className="icon-btn-round danger"
                    title="Remove time"
                    aria-label="Remove time"
                    onClick={() => handleRemoveTime(idx)}
                    disabled={disabled}
                  >
                    <i className="fa-solid fa-trash" aria-hidden="true" />
                  </button>
                )}
              </div>
            ))}

            <div>
              <button
                type="button"
                className="btn btn-secondary"
                style={{ fontSize: "0.8rem", padding: "4px 10px" }}
                onClick={handleAddTime}
                disabled={disabled}
              >
                + Add another time
              </button>
            </div>
          </div>
        ) : (
          <div
            className="repeating-interval-container"
            style={{
              display: "flex",
              alignItems: "center",
              gap: "8px",
              marginTop: "4px",
              padding: "10px",
              background: "rgba(13, 20, 36, 0.45)",
              borderRadius: "8px",
              border: "1px solid rgba(255, 255, 255, 0.08)",
            }}
          >
            <span style={{ fontSize: "0.85rem", color: "var(--muted, #94a3b8)" }}>Every</span>
            <input
              type="number"
              min="1"
              max="999"
              className="form-control"
              style={{ width: "80px" }}
              value={interval.value}
              onChange={(e) => handleIntervalValueChange(e.target.value)}
              disabled={disabled}
              required
            />
            <select
              className="form-control"
              style={{ width: "120px" }}
              value={interval.unit}
              onChange={(e) => handleIntervalUnitChange(e.target.value as "minutes" | "hours" | "days")}
              disabled={disabled}
            >
              <option value="minutes">Minutes</option>
              <option value="hours">Hours</option>
              <option value="days">Days</option>
            </select>
          </div>
        )}
      </div>

      {/* 3. Live Summary Chip */}
      <div
        className="schedule-summary-chip"
        style={{
          display: "flex",
          alignItems: "center",
          gap: "10px",
          padding: "10px 14px",
          borderRadius: "8px",
          background: "rgba(56, 189, 248, 0.1)",
          border: "1px solid rgba(56, 189, 248, 0.25)",
          color: "#bae6fd",
          fontSize: "0.85rem",
        }}
      >
        <i className="fa-solid fa-clock" aria-hidden="true" style={{ color: "#38bdf8" }} />
        <span>{describeStructuredSchedule(value)}</span>
      </div>
    </div>
  );
}
