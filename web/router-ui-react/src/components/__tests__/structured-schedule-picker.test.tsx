// @vitest-environment jsdom

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import "@testing-library/jest-dom/vitest";
import {
  StructuredSchedulePicker,
  describeStructuredSchedule,
  describeDays,
  formatTime12h,
} from "../StructuredSchedulePicker";
import type { StructuredSchedule } from "../../types";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("StructuredSchedulePicker Component & Helpers", () => {
  it("formats 24-hour clock strings into 12-hour strings with AM/PM", () => {
    expect(formatTime12h("00:00")).toBe("12:00 AM");
    expect(formatTime12h("09:15")).toBe("09:15 AM");
    expect(formatTime12h("12:00")).toBe("12:00 PM");
    expect(formatTime12h("17:45")).toBe("05:45 PM");
    expect(formatTime12h("23:59")).toBe("11:59 PM");
  });

  it("describes day sets accurately", () => {
    expect(describeDays([0, 1, 2, 3, 4, 5, 6])).toBe("every day");
    expect(describeDays([1, 2, 3, 4, 5])).toBe("Monday through Friday");
    expect(describeDays([6, 0])).toBe("on weekends");
    expect(describeDays([1])).toBe("every Monday");
    expect(describeDays([1, 3])).toBe("on Monday and Wednesday");
    expect(describeDays([1, 3, 5])).toBe("on Monday, Wednesday, and Friday");
    expect(describeDays([])).toBe("No days selected");
  });

  it("generates natural English descriptions for all schedule variants", () => {
    const dailySpecific: StructuredSchedule = {
      days_mode: "every_day",
      time_mode: "specific_times",
      times: ["09:00", "17:00"],
    };
    expect(describeStructuredSchedule(dailySpecific)).toBe(
      "Runs every day at 09:00 AM and 05:00 PM"
    );

    const weekdaysInterval: StructuredSchedule = {
      days_mode: "specific_days",
      days: [1, 2, 3, 4, 5],
      time_mode: "repeating_interval",
      interval: { value: 30, unit: "minutes" },
    };
    expect(describeStructuredSchedule(weekdaysInterval)).toBe(
      "Runs every 30 minutes, Monday through Friday"
    );

    const weekendHourInterval: StructuredSchedule = {
      days_mode: "specific_days",
      days: [0, 6],
      time_mode: "repeating_interval",
      interval: { value: 2, unit: "hours" },
    };
    expect(describeStructuredSchedule(weekendHourInterval)).toBe(
      "Runs every 2 hours, on weekends"
    );

    const dailyInterval: StructuredSchedule = {
      days_mode: "every_day",
      time_mode: "repeating_interval",
      interval: { value: 1, unit: "hours" },
    };
    expect(describeStructuredSchedule(dailyInterval)).toBe(
      "Runs every 1 hour continuously"
    );
  });

  it("toggles days mode between Every day and Specific days", () => {
    const value: StructuredSchedule = {
      days_mode: "every_day",
      time_mode: "specific_times",
      times: ["09:00"],
    };
    const onChange = vi.fn();

    render(<StructuredSchedulePicker value={value} onChange={onChange} />);

    const specificDaysBtn = screen.getByText("Specific days");
    fireEvent.click(specificDaysBtn);

    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        days_mode: "specific_days",
      })
    );
  });

  it("selects day pills and quick presets when in specific_days mode", () => {
    const value: StructuredSchedule = {
      days_mode: "specific_days",
      days: [1, 2, 3, 4, 5],
      time_mode: "specific_times",
      times: ["09:00"],
    };
    const onChange = vi.fn();

    render(<StructuredSchedulePicker value={value} onChange={onChange} />);

    // Quick presets are visible
    const weekendsPreset = screen.getByText("Weekends");
    fireEvent.click(weekendsPreset);

    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        days: [6, 0],
      })
    );

    // Toggle day pill
    const monPill = screen.getByRole("button", { name: "Monday" });
    fireEvent.click(monPill);
    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        days: [2, 3, 4, 5],
      })
    );
  });

  it("toggles time mode between Specific times and Repeating interval", () => {
    const value: StructuredSchedule = {
      days_mode: "every_day",
      time_mode: "specific_times",
      times: ["09:00"],
    };
    const onChange = vi.fn();

    render(<StructuredSchedulePicker value={value} onChange={onChange} />);

    const repeatingIntervalBtn = screen.getByText("Repeating interval");
    fireEvent.click(repeatingIntervalBtn);

    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        time_mode: "repeating_interval",
      })
    );
  });

  it("adds and removes clock times when in specific_times mode", () => {
    const value: StructuredSchedule = {
      days_mode: "every_day",
      time_mode: "specific_times",
      times: ["09:00", "12:00"],
    };
    const onChange = vi.fn();

    render(<StructuredSchedulePicker value={value} onChange={onChange} />);

    // Add time button triggers addition of default 12:00
    const addBtn = screen.getByText("+ Add another time");
    fireEvent.click(addBtn);

    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        times: ["09:00", "12:00", "12:00"],
      })
    );

    // Remove existing time
    const removeBtns = screen.getAllByRole("button", { name: "Remove time" });
    const firstRemoveBtn = removeBtns[0];
    expect(firstRemoveBtn).toBeDefined();
    if (firstRemoveBtn) {
      fireEvent.click(firstRemoveBtn);
    }

    expect(onChange).toHaveBeenCalledWith(
      expect.objectContaining({
        times: ["12:00"],
      })
    );
  });

  it("renders the live summary chip", () => {
    const value: StructuredSchedule = {
      days_mode: "every_day",
      time_mode: "specific_times",
      times: ["09:00", "17:00"],
    };
    const onChange = vi.fn();

    render(<StructuredSchedulePicker value={value} onChange={onChange} />);

    expect(
      screen.getByText("Runs every day at 09:00 AM and 05:00 PM")
    ).toBeInTheDocument();
  });
});

