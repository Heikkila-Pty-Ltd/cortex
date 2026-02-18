package scheduler

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
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

func TestRunMultiTeamPlanningMissingDependencies(t *testing.T) {
	cfg := &config.Config{
		Chief: config.Chief{
			Enabled: true,
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))

	t.Run("missing store", func(t *testing.T) {
		chiefSM := NewChiefSM(cfg, logger, nil, NewMockDispatcher())
		err := chiefSM.RunMultiTeamPlanning(context.Background())
		if err == nil {
			t.Fatal("expected error when store is missing")
		}
	})

	t.Run("missing dispatcher", func(t *testing.T) {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		defer st.Close()

		chiefSM := NewChiefSM(cfg, logger, st, nil)
		err = chiefSM.RunMultiTeamPlanning(context.Background())
		if err == nil {
			t.Fatal("expected error when dispatcher is missing")
		}
	})
}
