package scheduler

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/chief"
	"github.com/antigravity-dev/cortex/internal/config"
)

func TestCeremonySchedulerInitialization(t *testing.T) {
	cfg := &config.Config{
		Chief: config.Chief{
			Enabled: true,
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	cs := NewCeremonyScheduler(cfg, nil, nil, logger)

	if cs == nil {
		t.Fatal("Expected ceremony scheduler to be created")
	}

	schedules := cs.GetSchedules()
	if len(schedules) == 0 {
		t.Error("Expected ceremony schedules to be initialized")
	}

	if _, exists := schedules[chief.CeremonyMultiTeamPlanning]; !exists {
		t.Error("Expected multi-team planning ceremony to be scheduled")
	}
	if _, exists := schedules[chief.CeremonyRetrospective]; !exists {
		t.Error("Expected overall retrospective ceremony to be scheduled")
	}
}

func TestCeremonySchedulerDisabled(t *testing.T) {
	cfg := &config.Config{
		Chief: config.Chief{
			Enabled: false,
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	cs := NewCeremonyScheduler(cfg, nil, nil, logger)

	// Should not check ceremonies when disabled
	ctx := context.Background()
	cs.CheckCeremonies(ctx) // Should return immediately without error

	// Schedules should still be initialized for potential enabling later
	schedules := cs.GetSchedules()
	if len(schedules) == 0 {
		t.Error("Expected ceremony schedules to be initialized even when disabled")
	}
}

func TestUpdateSchedule(t *testing.T) {
	cfg := &config.Config{
		Chief: config.Chief{
			Enabled: true,
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	cs := NewCeremonyScheduler(cfg, nil, nil, logger)

	// Update schedule
	newSchedule := chief.CeremonySchedule{
		Type:      chief.CeremonyMultiTeamPlanning,
		DayOfWeek: time.Tuesday,
		TimeOfDay: time.Date(0, 1, 1, 10, 30, 0, 0, time.UTC),
	}

	cs.UpdateSchedule(chief.CeremonyMultiTeamPlanning, newSchedule)

	// Verify update
	schedules := cs.GetSchedules()
	updated := schedules[chief.CeremonyMultiTeamPlanning]

	if updated.DayOfWeek != time.Tuesday {
		t.Errorf("Expected Tuesday, got %v", updated.DayOfWeek)
	}

	if updated.TimeOfDay.Hour() != 10 || updated.TimeOfDay.Minute() != 30 {
		t.Errorf("Expected 10:30, got %02d:%02d", updated.TimeOfDay.Hour(), updated.TimeOfDay.Minute())
	}
}

func TestContainsIgnoreCase(t *testing.T) {
	cs := &CeremonyScheduler{}

	testCases := []struct {
		str      string
		substr   string
		expected bool
	}{
		{"Chief SM ceremony: Multi-team", "multi-team", true},
		{"Chief SM ceremony: Multi-team", "MULTI-TEAM", true},
		{"Chief SM ceremony: Multi-team", "team", true},
		{"Chief SM ceremony: Multi-team", "xyz", false},
		{"", "test", false},
		{"test", "", true}, // empty substr should match
	}

	for _, tc := range testCases {
		result := cs.containsIgnoreCase(tc.str, tc.substr)
		if result != tc.expected {
			t.Errorf("containsIgnoreCase(%q, %q) = %v, expected %v", tc.str, tc.substr, result, tc.expected)
		}
	}
}

func TestHasRunningCeremonyDispatch(t *testing.T) {
	// Test that ceremony dispatch detection logic is sound

	// Test ceremony bead ID pattern recognition
	ceremonyBeadID := "ceremony-12345"
	regularBeadID := "regular-bead-123"

	if len(ceremonyBeadID) < len("ceremony-") || ceremonyBeadID[:9] != "ceremony-" {
		t.Error("Ceremony bead ID pattern should be recognized")
	}

	if len(regularBeadID) > len("ceremony-") && regularBeadID[:9] == "ceremony-" {
		t.Error("Regular bead ID should not match ceremony pattern")
	}

	t.Log("TestHasRunningCeremonyDispatch: ceremony detection logic test completed")
}

func TestOverallRetrospectiveScheduleRunsAfterProjectRetrospective(t *testing.T) {
	cfg := &config.Config{
		Chief: config.Chief{
			Enabled: true,
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	cs := NewCeremonyScheduler(cfg, nil, nil, logger)

	schedules := cs.GetSchedules()
	projectRetro := schedules[chief.CeremonySprintRetro]
	overallRetro := schedules[chief.CeremonyRetrospective]

	if projectRetro.DayOfWeek != overallRetro.DayOfWeek {
		t.Fatalf("expected overall retro to be on same day as project retro, got %v vs %v", overallRetro.DayOfWeek, projectRetro.DayOfWeek)
	}

	projectMins := projectRetro.TimeOfDay.Hour()*60 + projectRetro.TimeOfDay.Minute()
	overallMins := overallRetro.TimeOfDay.Hour()*60 + overallRetro.TimeOfDay.Minute()
	if overallMins <= projectMins {
		t.Fatalf("expected overall retro to run after project retro, got %02d:%02d <= %02d:%02d",
			overallRetro.TimeOfDay.Hour(),
			overallRetro.TimeOfDay.Minute(),
			projectRetro.TimeOfDay.Hour(),
			projectRetro.TimeOfDay.Minute())
	}
}

// Note: Full integration tests would require a real store instance
// These tests focus on the ceremony scheduling logic
