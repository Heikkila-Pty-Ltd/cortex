package scheduler

import (
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
)

func newTestCadence(t *testing.T) *SprintCadence {
	t.Helper()
	cadence, err := NewSprintCadenceFromConfig(config.Cadence{
		SprintLength:    "1w",
		SprintStartDay:  "Monday",
		SprintStartTime: "09:00",
		Timezone:        "UTC",
	})
	if err != nil {
		t.Fatalf("NewSprintCadenceFromConfig failed: %v", err)
	}
	return cadence
}

func TestCurrentSprintAt(t *testing.T) {
	cadence := newTestCadence(t)
	now := time.Date(2026, 2, 18, 10, 0, 0, 0, time.UTC) // Wednesday

	number, start, end := cadence.CurrentSprintAt(now)
	if number <= 0 {
		t.Fatalf("expected positive sprint number, got %d", number)
	}
	if got, want := start, time.Date(2026, 2, 16, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("sprint start = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
	if got, want := end, time.Date(2026, 2, 23, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("sprint end = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

func TestIsSprintBoundary(t *testing.T) {
	cadence := newTestCadence(t)
	start := time.Date(2026, 2, 16, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 23, 9, 0, 0, 0, time.UTC)

	isStart, isEnd := cadence.IsSprintBoundary(start.Add(2*time.Minute), 5)
	if !isStart || isEnd {
		t.Fatalf("expected start boundary true/false, got %v/%v", isStart, isEnd)
	}

	isStart, isEnd = cadence.IsSprintBoundary(end.Add(-2*time.Minute), 5)
	if isStart || !isEnd {
		t.Fatalf("expected end boundary false/true, got %v/%v", isStart, isEnd)
	}
}

func TestSprintDay(t *testing.T) {
	cadence := newTestCadence(t)

	day1 := cadence.SprintDay(time.Date(2026, 2, 16, 10, 0, 0, 0, time.UTC))
	if day1 != 1 {
		t.Fatalf("day1 = %d, want 1", day1)
	}

	day3 := cadence.SprintDay(time.Date(2026, 2, 18, 10, 0, 0, 0, time.UTC))
	if day3 != 3 {
		t.Fatalf("day3 = %d, want 3", day3)
	}
}

func TestNextCeremonyAt(t *testing.T) {
	cadence := newTestCadence(t)

	name, at := cadence.NextCeremonyAt(time.Date(2026, 2, 17, 8, 0, 0, 0, time.UTC))
	if name != "daily_standup" {
		t.Fatalf("next ceremony name = %q, want daily_standup", name)
	}
	if got, want := at, time.Date(2026, 2, 17, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("daily standup time = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	name, at = cadence.NextCeremonyAt(time.Date(2026, 2, 22, 9, 30, 0, 0, time.UTC))
	if name != "sprint_retro" {
		t.Fatalf("next ceremony name = %q, want sprint_retro", name)
	}
	if got, want := at, time.Date(2026, 2, 22, 10, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("retro time = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	name, at = cadence.NextCeremonyAt(time.Date(2026, 2, 22, 10, 30, 0, 0, time.UTC))
	if name != "sprint_planning" {
		t.Fatalf("next ceremony name = %q, want sprint_planning", name)
	}
	if got, want := at, time.Date(2026, 2, 23, 9, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("planning time = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}
