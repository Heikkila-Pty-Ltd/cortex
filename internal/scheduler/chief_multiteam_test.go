package scheduler

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestRunMultiTeamPlanningInjectsPortfolioContextIntoDispatchPrompt(t *testing.T) {
	tmp := t.TempDir()
	projectA := filepath.Join(tmp, "proj-a")
	projectB := filepath.Join(tmp, "proj-b")
	for _, dir := range []string{
		filepath.Join(projectA, ".beads"),
		filepath.Join(projectB, ".beads"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	fakeBin := t.TempDir()
	bdPath := filepath.Join(fakeBin, "bd")
	script := `#!/bin/sh
case "$1" in
  list)
    case "$PWD" in
      *proj-a)
        cat <<'JSON'
[{"id":"a-1","title":"A endpoint","description":"Provide endpoint for proj-b","status":"open","priority":1,"issue_type":"task","labels":["stage:planning","sprint:selected"],"estimated_minutes":120,"depends_on":[],"acceptance_criteria":"done","design":"spec","created_at":"2026-02-18T00:00:00Z","updated_at":"2026-02-18T01:00:00Z"}]
JSON
        ;;
      *proj-b)
        cat <<'JSON'
[{"id":"b-1","title":"B integration","description":"Depends on A endpoint","status":"open","priority":1,"issue_type":"task","labels":["stage:backlog","sprint:blocked"],"estimated_minutes":90,"depends_on":["proj-a:a-1"],"acceptance_criteria":"done","design":"spec","created_at":"2026-02-18T00:00:00Z","updated_at":"2026-02-18T01:05:00Z"}]
JSON
        ;;
      *)
        echo '[]'
        ;;
    esac
    ;;
  show)
    echo '{}'
    ;;
  *)
    echo '[]'
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", fakeBin+":"+os.Getenv("PATH"))

	cfg := &config.Config{
		Chief: config.Chief{Enabled: true, AgentID: "chief-sm"},
		Tiers: config.Tiers{
			Premium:  []string{"premium-provider"},
			Balanced: []string{"balanced-provider"},
		},
		RateLimits: config.RateLimits{
			Budget: map[string]int{
				"proj-a": 60,
				"proj-b": 40,
			},
		},
		Projects: map[string]config.Project{
			"proj-a": {
				Enabled:   true,
				Priority:  1,
				Workspace: projectA,
				BeadsDir:  filepath.Join(projectA, ".beads"),
			},
			"proj-b": {
				Enabled:   true,
				Priority:  2,
				Workspace: projectB,
				BeadsDir:  filepath.Join(projectB, ".beads"),
			},
		},
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := store.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	dispatcher := NewMockDispatcher()
	chiefSM := NewChiefSM(cfg, logger, st, dispatcher)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := chiefSM.RunMultiTeamPlanning(ctx); err != nil {
		t.Fatalf("RunMultiTeamPlanning failed: %v", err)
	}
	cancel()
	time.Sleep(10 * time.Millisecond)

	dispatches := dispatcher.GetDispatches()
	if len(dispatches) != 1 {
		t.Fatalf("expected 1 dispatch, got %d", len(dispatches))
	}
	prompt := dispatches[0].Prompt

	for _, want := range []string{
		`"portfolio_backlog"`,
		`"cross_project_deps"`,
		`"capacity_budgets"`,
		`"proj-a": 60`,
		`"proj-b": 40`,
		`"project_planning_results"`,
		`"selected_beads": 1`,
		`"blocked_beads": 1`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q", want)
		}
	}
}
