package coordination

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/learner"
	"github.com/antigravity-dev/cortex/internal/store"
)

// ProjectSummary aggregates cross-project stats for a single project.
type ProjectSummary struct {
	Project string         `json:"project"`
	Config  config.Project `json:"config"`
	Stats   ProjectStats   `json:"stats"`
}

// ProjectStats contains cross-project counters for command/reporting surfaces.
type ProjectStats struct {
	OpenCount            int     `json:"open_count"`
	RunningDispatchCount int     `json:"running_dispatch_count"`
	CompletedCount       int     `json:"completed_count"`
	FailedCount          int     `json:"failed_count"`
	VelocityBeadsPerDay  float64 `json:"velocity_beads_per_day"`
}

// GetProjectSummaries returns enabled-project summaries for a given time window.
func GetProjectSummaries(ctx context.Context, s *store.Store, cfg *config.Config, window time.Duration) ([]ProjectSummary, error) {
	if s == nil {
		return nil, fmt.Errorf("coordinator: nil store")
	}
	if cfg == nil {
		return nil, fmt.Errorf("coordinator: nil config")
	}
	if len(cfg.Projects) == 0 {
		return []ProjectSummary{}, nil
	}

	if window <= 0 {
		window = 24 * time.Hour
	}

	projectNames := make([]string, 0, len(cfg.Projects))
	projectConfigs := make(map[string]config.Project, len(cfg.Projects))
	for project, projectCfg := range cfg.Projects {
		if !projectCfg.Enabled {
			continue
		}
		projectNames = append(projectNames, project)
		projectConfigs[project] = projectCfg
	}
	sort.Strings(projectNames)

	dispatchCutoff := time.Now().Add(-window)
	dispatchStatusCounts, err := s.GetProjectDispatchStatusCounts(dispatchCutoff)
	if err != nil {
		return nil, err
	}

	projectVelocities, err := learner.GetProjectVelocities(s, projectNames, window)
	if err != nil {
		return nil, err
	}

	summaries := make([]ProjectSummary, 0, len(projectNames))
	for _, project := range projectNames {
		projectCfg := projectConfigs[project]

		openCount, err := countOpenBeads(ctx, resolveProjectBeadsDir(projectCfg))
		if err != nil {
			return nil, err
		}

		summary := ProjectSummary{
			Project: project,
			Config:  projectCfg,
			Stats: ProjectStats{
				OpenCount: openCount,
			},
		}

		if counts, ok := dispatchStatusCounts[project]; ok {
			summary.Stats.RunningDispatchCount = counts.Running
			summary.Stats.CompletedCount = counts.Completed
			summary.Stats.FailedCount = counts.Failed
		}

		if velocity, ok := projectVelocities[project]; ok {
			summary.Stats.VelocityBeadsPerDay = velocity.BeadsPerDay
		}

		summaries = append(summaries, summary)
	}

	return summaries, nil
}

func resolveProjectBeadsDir(projectCfg config.Project) string {
	beadsDir := strings.TrimSpace(projectCfg.BeadsDir)
	if beadsDir != "" {
		return config.ExpandHome(beadsDir)
	}

	workspace := strings.TrimSpace(projectCfg.Workspace)
	if workspace == "" {
		return ""
	}

	return filepath.Join(config.ExpandHome(workspace), ".beads")
}

func countOpenBeads(ctx context.Context, beadsDir string) (int, error) {
	beadsDir = strings.TrimSpace(beadsDir)
	if beadsDir == "" {
		return 0, nil
	}

	projectDir := filepath.Dir(beadsDir)
	if projectDir == "." || projectDir == "" {
		return 0, nil
	}
	if _, err := os.Stat(projectDir); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return 0, err
	}

	openCount := 0
	for _, bead := range allBeads {
		if bead.Status == "open" {
			openCount++
		}
	}
	return openCount, nil
}
