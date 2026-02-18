package scheduler

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/antigravity-dev/cortex/internal/config"
)

func TestClassifyPlanningSelection(t *testing.T) {
	tests := []struct {
		name   string
		labels []string
		want   string
	}{
		{name: "selected", labels: []string{"stage:planning", "sprint:selected"}, want: "selected"},
		{name: "deferred", labels: []string{"sprint:deferred"}, want: "deferred"},
		{name: "blocked", labels: []string{"sprint:blocked"}, want: "blocked"},
		{name: "none", labels: []string{"stage:backlog"}, want: ""},
	}

	for _, tc := range tests {
		got := classifyPlanningSelection(tc.labels)
		if got != tc.want {
			t.Fatalf("%s: classifyPlanningSelection() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestRunMultiTeamPlanningDisabled(t *testing.T) {
	cfg := &config.Config{
		Chief: config.Chief{
			Enabled: false,
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	chiefSM := NewChiefSM(cfg, logger, nil, nil)
	err := chiefSM.RunMultiTeamPlanning(context.Background())
	if err == nil {
		t.Fatal("expected error when chief sm is disabled")
	}
}
