package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStructuredSchedule_Validation(t *testing.T) {
	// 1. Missing days in specific_days
	err := ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "specific_days",
		Days:     []int{},
		TimeMode: "specific_times",
		Times:    []string{"10:00"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "days array must not be empty")

	// 2. Invalid day in days array
	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "specific_days",
		Days:     []int{7},
		TimeMode: "specific_times",
		Times:    []string{"10:00"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid day")

	// 3. Duplicate day in days array
	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "specific_days",
		Days:     []int{1, 1},
		TimeMode: "specific_times",
		Times:    []string{"10:00"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate day")

	// 4. Invalid days_mode
	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "some_days",
		TimeMode: "specific_times",
		Times:    []string{"10:00"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "days_mode must be")

	// 5. Empty times in specific_times
	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "specific_times",
		Times:    []string{},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "times array must not be empty")

	// 6. Malformed time strings
	for _, badTime := range []string{"24:00", "12:60", "9:00", "noon", "12:00:00"} {
		err = ValidateStructuredSchedule(StructuredSchedule{
			DaysMode: "every_day",
			TimeMode: "specific_times",
			Times:    []string{badTime},
		})
		require.Error(t, err, "expected error for time %q", badTime)
	}

	// 7. Duplicate times
	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "specific_times",
		Times:    []string{"09:00", "09:00"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate time")

	// 8. Repeating interval missing interval
	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "repeating_interval",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "interval must not be nil")

	// 9. Repeating interval invalid value or unit
	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "repeating_interval",
		Interval: &IntervalSpec{Value: 0, Unit: "minutes"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "positive integer")

	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "repeating_interval",
		Interval: &IntervalSpec{Value: 5, Unit: "weeks"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "interval unit must be")

	// 10. Invalid timezone
	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "specific_times",
		Times:    []string{"12:00"},
		Timezone: "Mars/Phobos",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid timezone")

	// 11. Valid configurations pass
	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "specific_days",
		Days:     []int{1, 2, 3, 4, 5},
		TimeMode: "specific_times",
		Times:    []string{"09:15", "17:45"},
		Timezone: "America/New_York",
	})
	require.NoError(t, err)

	err = ValidateStructuredSchedule(StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "repeating_interval",
		Interval: &IntervalSpec{Value: 15, Unit: "minutes"},
	})
	require.NoError(t, err)
}

func TestStructuredSchedule_NextRunAt_SpecificTimes(t *testing.T) {
	// Base time: Monday 2026-11-02 08:00:00 UTC
	base := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)

	// SCN-001: Specific times on specific days (Mon-Fri at 09:15 and 17:45)
	spec := StructuredSchedule{
		DaysMode: "specific_days",
		Days:     []int{1, 2, 3, 4, 5},
		TimeMode: "specific_times",
		Times:    []string{"09:15", "17:45"},
		Timezone: "UTC",
	}

	// 1. Next run from 08:00 should be 09:15 today (Monday)
	next, err := NextRunAt(spec, base)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 2, 9, 15, 0, 0, time.UTC), next)

	// 2. Next run from 09:15 should be 17:45 today (Monday)
	next, err = NextRunAt(spec, next)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 2, 17, 45, 0, 0, time.UTC), next)

	// 3. Next run from 17:45 should be Tuesday 09:15
	next, err = NextRunAt(spec, next)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 3, 9, 15, 0, 0, time.UTC), next)

	// 4. Over weekend transition: Friday 2026-11-06 17:45:00 UTC
	fridayEvening := time.Date(2026, 11, 6, 17, 45, 0, 0, time.UTC)
	next, err = NextRunAt(spec, fridayEvening)
	require.NoError(t, err)
	// Must skip Saturday (Nov 7) and Sunday (Nov 8) to Monday Nov 9 09:15
	require.Equal(t, time.Date(2026, 11, 9, 9, 15, 0, 0, time.UTC), next)

	// SCN-002: Specific times every day (daily at 08:30)
	dailySpec := StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "specific_times",
		Times:    []string{"08:30"},
		Timezone: "UTC",
	}
	// From 09:00 today, should advance to tomorrow 08:30
	from0900 := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	nextDaily, err := NextRunAt(dailySpec, from0900)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 3, 8, 30, 0, 0, time.UTC), nextDaily)
}

func TestStructuredSchedule_NextRunAt_RepeatingInterval(t *testing.T) {
	// SCN-004: Repeating interval every day (every 15 minutes)
	intervalSpec := StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "repeating_interval",
		Interval: &IntervalSpec{Value: 15, Unit: "minutes"},
	}
	base := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	next, err := NextRunAt(intervalSpec, base)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 2, 10, 15, 0, 0, time.UTC), next)

	// SCN-003: Repeating interval on specific days (every 2 hours on Sat and Sun)
	weekendSpec := StructuredSchedule{
		DaysMode: "specific_days",
		Days:     []int{0, 6}, // Sunday and Saturday
		TimeMode: "repeating_interval",
		Interval: &IntervalSpec{Value: 2, Unit: "hours"},
		Timezone: "UTC",
	}
	// Friday 2026-11-06 20:00:00 UTC
	friday := time.Date(2026, 11, 6, 20, 0, 0, 0, time.UTC)
	// Jump forward to Saturday midnight
	nextWeekend, err := NextRunAt(weekendSpec, friday)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC), nextWeekend)

	// During Saturday, advancing from 10:00 should be 12:00
	sat10 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	nextWeekend, err = NextRunAt(weekendSpec, sat10)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC), nextWeekend)

	// At end of Sunday (2026-11-08 23:00 UTC), advancing by 2h lands on Monday 01:00 (not eligible) -> jump to next Saturday
	sunLate := time.Date(2026, 11, 8, 23, 0, 0, 0, time.UTC)
	nextWeekend, err = NextRunAt(weekendSpec, sunLate)
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC), nextWeekend)
}

func TestStructuredSchedule_NextRunAt_TimezoneHandling(t *testing.T) {
	// Schedule for 09:00 in America/New_York (UTC-5 in standard time)
	nySpec := StructuredSchedule{
		DaysMode: "every_day",
		TimeMode: "specific_times",
		Times:    []string{"09:00"},
		Timezone: "America/New_York",
	}

	// Given 2026-11-02 13:00:00 UTC (which is 08:00:00 EST)
	now := time.Date(2026, 11, 2, 13, 0, 0, 0, time.UTC)
	next, err := NextRunAt(nySpec, now)
	require.NoError(t, err)
	// 09:00 EST = 14:00 UTC
	require.Equal(t, time.Date(2026, 11, 2, 14, 0, 0, 0, time.UTC), next)
}

func TestWorkflowSchedule_StoreCRUDAndAutoNextRun(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "schedules.db")
	s, err := Open(ctx, path, Options{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })

	// Create test workflow
	vars := []WorkflowVariableDefinition{
		{Name: "ENV", Required: true},
	}
	wf, err := s.CreateWorkflow(ctx, WorkflowRequest{
		Name:      "test-wf",
		Variables: &vars,
	})
	require.NoError(t, err)

	// 1. Rejects missing required workflow variables
	_, err = s.CreateWorkflowSchedule(ctx, WorkflowScheduleRequest{
		WorkflowID: wf.ID,
		Name:       "failing-schedule",
		Schedule: StructuredSchedule{
			DaysMode: "every_day",
			TimeMode: "repeating_interval",
			Interval: &IntervalSpec{Value: 10, Unit: "minutes"},
		},
		Variables: map[string]string{}, // missing ENV
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing required workflow parameter(s): ENV")

	// 2. Successful creation with auto-calculated next_run_at
	created, err := s.CreateWorkflowSchedule(ctx, WorkflowScheduleRequest{
		WorkflowID: wf.ID,
		Name:       "good-schedule",
		Schedule: StructuredSchedule{
			DaysMode: "every_day",
			TimeMode: "repeating_interval",
			Interval: &IntervalSpec{Value: 30, Unit: "minutes"},
		},
		Variables: map[string]string{"ENV": "production"},
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	require.NotEmpty(t, created.NextRunAt)
	require.True(t, created.Enabled)
	require.Equal(t, "production", created.Variables["ENV"])

	// 3. GetWorkflowSchedule
	fetched, err := s.GetWorkflowSchedule(ctx, created.ID)
	require.NoError(t, err)
	require.NotNil(t, fetched)
	require.Equal(t, created.ID, fetched.ID)
	require.Equal(t, "every_day", fetched.Schedule.DaysMode)
	require.Equal(t, 30, fetched.Schedule.Interval.Value)

	// 4. ListWorkflowSchedules
	list, err := s.ListWorkflowSchedules(ctx, wf.ID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// 5. UpdateWorkflowSchedule with new schedule and auto-calculated next_run_at
	updated, err := s.UpdateWorkflowSchedule(ctx, created.ID, WorkflowScheduleRequest{
		WorkflowID: wf.ID,
		Name:       "updated-schedule",
		Schedule: StructuredSchedule{
			DaysMode: "specific_days",
			Days:     []int{1, 2, 3},
			TimeMode: "specific_times",
			Times:    []string{"12:00"},
			Timezone: "UTC",
		},
		Variables: map[string]string{"ENV": "staging"},
	})
	require.NoError(t, err)
	require.Equal(t, "updated-schedule", updated.Name)
	require.Equal(t, "specific_days", updated.Schedule.DaysMode)
	require.Equal(t, "staging", updated.Variables["ENV"])

	// 6. AdvanceWorkflowSchedule
	futureTime := "2026-11-03T12:00:00Z"
	err = s.AdvanceWorkflowSchedule(ctx, created.ID, futureTime, "success", "")
	require.NoError(t, err)

	advanced, err := s.GetWorkflowSchedule(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, futureTime, advanced.NextRunAt)
	require.NotNil(t, advanced.LastRunStatus)
	require.Equal(t, "success", *advanced.LastRunStatus)

	// 7. DeleteWorkflowSchedule
	deleted, err := s.DeleteWorkflowSchedule(ctx, created.ID)
	require.NoError(t, err)
	require.True(t, deleted)

	fetchedAfterDelete, err := s.GetWorkflowSchedule(ctx, created.ID)
	require.NoError(t, err)
	require.Nil(t, fetchedAfterDelete)
}
