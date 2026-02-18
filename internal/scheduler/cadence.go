package scheduler

import (
	"fmt"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
)

// SprintCadence models shared sprint timing across projects.
type SprintCadence struct {
	Length    time.Duration
	StartDay  time.Weekday
	StartTime string // HH:MM
	Timezone  *time.Location

	startHour   int
	startMinute int
}

// NewSprintCadenceFromConfig builds sprint cadence from config values.
func NewSprintCadenceFromConfig(cfg config.Cadence) (*SprintCadence, error) {
	length, err := cfg.SprintLengthDuration()
	if err != nil {
		return nil, err
	}
	weekday, err := cfg.StartWeekday()
	if err != nil {
		return nil, err
	}
	hour, minute, err := cfg.StartClock()
	if err != nil {
		return nil, err
	}
	loc, err := cfg.LoadLocation()
	if err != nil {
		return nil, err
	}

	return &SprintCadence{
		Length:      length,
		StartDay:    weekday,
		StartTime:   cfg.SprintStartTime,
		Timezone:    loc,
		startHour:   hour,
		startMinute: minute,
	}, nil
}

// CurrentSprint returns sprint number and current sprint boundaries for now.
func (c *SprintCadence) CurrentSprint() (number int, start, end time.Time) {
	return c.CurrentSprintAt(time.Now())
}

// CurrentSprintAt returns sprint number and boundaries for an explicit timestamp.
func (c *SprintCadence) CurrentSprintAt(now time.Time) (number int, start, end time.Time) {
	local := now.In(c.Timezone)
	base := c.baseAnchor()
	elapsed := local.Sub(base)

	idx := elapsed / c.Length
	if elapsed < 0 && elapsed%c.Length != 0 {
		idx--
	}

	start = base.Add(idx * c.Length)
	end = start.Add(c.Length)
	number = int(idx) + 1
	return number, start, end
}

// IsSprintBoundary reports whether t is within windowMinutes of sprint start/end.
func (c *SprintCadence) IsSprintBoundary(t time.Time, windowMinutes int) (isStart, isEnd bool) {
	if windowMinutes < 0 {
		windowMinutes = -windowMinutes
	}
	_, start, end := c.CurrentSprintAt(t)
	window := time.Duration(windowMinutes) * time.Minute
	local := t.In(c.Timezone)

	isStart = absDuration(local.Sub(start)) <= window
	isEnd = absDuration(local.Sub(end)) <= window
	return isStart, isEnd
}

// SprintDay returns the day index in the current sprint (1-indexed).
func (c *SprintCadence) SprintDay(t time.Time) int {
	_, start, _ := c.CurrentSprintAt(t)
	local := t.In(c.Timezone)
	if local.Before(start) {
		return 1
	}

	day := int(local.Sub(start)/(24*time.Hour)) + 1
	if day < 1 {
		day = 1
	}
	maxDays := int(c.Length / (24 * time.Hour))
	if maxDays > 0 && day > maxDays {
		day = maxDays
	}
	return day
}

// NextCeremony returns the next ceremony type and scheduled timestamp from now.
func (c *SprintCadence) NextCeremony() (name string, at time.Time) {
	return c.NextCeremonyAt(time.Now())
}

// NextCeremonyAt returns the next ceremony type and timestamp for a reference time.
func (c *SprintCadence) NextCeremonyAt(now time.Time) (name string, at time.Time) {
	local := now.In(c.Timezone)
	_, _, sprintEnd := c.CurrentSprintAt(local)

	daily := c.dailyAt(local)
	if !daily.After(local) {
		daily = daily.Add(24 * time.Hour)
	}
	review := sprintEnd.Add(-24 * time.Hour)
	retro := review.Add(time.Hour)
	planning := sprintEnd

	candidates := []struct {
		name string
		at   time.Time
		rank int
	}{
		{name: "daily_standup", at: daily, rank: 4},
		{name: "sprint_review", at: review, rank: 2},
		{name: "sprint_retro", at: retro, rank: 3},
		{name: "sprint_planning", at: planning, rank: 1},
	}

	var chosenName string
	var chosenAt time.Time
	chosenRank := 99
	for _, candidate := range candidates {
		if !candidate.at.After(local) {
			continue
		}
		if chosenAt.IsZero() || candidate.at.Before(chosenAt) || (candidate.at.Equal(chosenAt) && candidate.rank < chosenRank) {
			chosenAt = candidate.at
			chosenName = candidate.name
			chosenRank = candidate.rank
		}
	}

	if chosenAt.IsZero() {
		return "sprint_planning", planning.Add(c.Length)
	}
	return chosenName, chosenAt
}

// CeremonyScheduleTimes returns canonical ceremony times for the active sprint.
func (c *SprintCadence) CeremonyScheduleTimes(t time.Time) (planning, daily, review, retro time.Time) {
	local := t.In(c.Timezone)
	_, _, sprintEnd := c.CurrentSprintAt(local)
	planning = sprintEnd
	daily = c.dailyAt(local)
	if !daily.After(local) {
		daily = daily.Add(24 * time.Hour)
	}
	review = sprintEnd.Add(-24 * time.Hour)
	retro = review.Add(time.Hour)
	return planning, daily, review, retro
}

func (c *SprintCadence) baseAnchor() time.Time {
	if c == nil || c.Timezone == nil {
		return time.Time{}
	}
	base := time.Date(1970, 1, 1, c.startHour, c.startMinute, 0, 0, c.Timezone)
	dayDelta := (int(c.StartDay) - int(base.Weekday()) + 7) % 7
	return base.AddDate(0, 0, dayDelta)
}

func (c *SprintCadence) dailyAt(t time.Time) time.Time {
	local := t.In(c.Timezone)
	return time.Date(local.Year(), local.Month(), local.Day(), c.startHour, c.startMinute, 0, 0, c.Timezone)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func mustCadence(cfg config.Cadence) *SprintCadence {
	cadence, err := NewSprintCadenceFromConfig(cfg)
	if err != nil {
		panic(fmt.Sprintf("invalid cadence config: %v", err))
	}
	return cadence
}
