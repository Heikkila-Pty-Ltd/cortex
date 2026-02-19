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
	"github.com/antigravity-dev/cortex/internal/store"
)

const (
	dailyWindow  = 24 * time.Hour
	weeklyWindow = 7 * 24 * time.Hour
)

// ProjectSummary contains cross-project visibility metrics for a single project.
type ProjectSummary struct {
	Project           string                 `json:"project"`
	Config            config.Project         `json:"config"`
	OpenBeads         OpenBeadSummary        `json:"open_beads"`
	RunningDispatches int                    `json:"running_dispatches"`
	CompletionRates   ProjectCompletionRates `json:"completion_rates"`
	Velocity          ProjectVelocity        `json:"velocity"`
}

// OpenBeadSummary tracks open bead counts by stage and priority.
type OpenBeadSummary struct {
	Total           int                    `json:"total"`
	ByPriority      map[int]int            `json:"by_priority"`
	ByStage         map[string]int         `json:"by_stage"`
	ByStagePriority map[string]map[int]int `json:"by_stage_priority"`
}

// ProjectCompletionRates holds daily and weekly completion/failure ratios.
type ProjectCompletionRates struct {
	Daily  CompletionWindow `json:"daily"`
	Weekly CompletionWindow `json:"weekly"`
}

// CompletionWindow summarizes completed vs failed dispatches for a period.
type CompletionWindow struct {
	Completed int     `json:"completed"`
	Failed    int     `json:"failed"`
	Total     int     `json:"total"`
	Rate      float64 `json:"rate"`
}

// ProjectVelocity tracks simple throughput estimates.
type ProjectVelocity struct {
	CompletedLast24h int     `json:"completed_last_24h"`
	CompletedLast7d  int     `json:"completed_last_7d"`
	BeadsPerDay      float64 `json:"beads_per_day"`
}

type completionWindowAggregation struct {
	completed int
	failed    int
}

// GetCrossProjectStats returns project summaries for all enabled projects using current time.
func GetCrossProjectStats(ctx context.Context, cfg *config.Config, s *store.Store) ([]ProjectSummary, error) {
	return getCrossProjectStats(ctx, cfg, s, time.Now())
}

// GetProjectSummaries is a compatibility wrapper for existing callers using the previous API shape.
func GetProjectSummaries(ctx context.Context, s *store.Store, cfg *config.Config, _ time.Duration) ([]ProjectSummary, error) {
	return GetCrossProjectStats(ctx, cfg, s)
}

func getCrossProjectStats(ctx context.Context, cfg *config.Config, s *store.Store, now time.Time) ([]ProjectSummary, error) {
	if s == nil {
		return nil, fmt.Errorf("coordination: nil store")
	}
	if cfg == nil {
		return nil, fmt.Errorf("coordination: nil config")
	}

	projectNames := make([]string, 0, len(cfg.Projects))
	projectByName := make(map[string]config.Project, len(cfg.Projects))

	for projectName, projectCfg := range cfg.Projects {
		if !projectCfg.Enabled {
			continue
		}
		projectNames = append(projectNames, projectName)
		projectByName[projectName] = projectCfg
	}

	sort.Strings(projectNames)
	if len(projectNames) == 0 {
		return []ProjectSummary{}, nil
	}

	runningDispatches, err := getRunningDispatchesByProject(s)
	if err != nil {
		return nil, err
	}

	dailyByProject, err := getCompletionCountsByProject(s, now.Add(-dailyWindow))
	if err != nil {
		return nil, err
	}
	weeklyByProject, err := getCompletionCountsByProject(s, now.Add(-weeklyWindow))
	if err != nil {
		return nil, err
	}

	summaries := make([]ProjectSummary, 0, len(projectNames))
	for _, project := range projectNames {
		projectCfg := projectByName[project]
		open, err := collectOpenBeadStats(ctx, resolveProjectBeadsDir(projectCfg))
		if err != nil {
			return nil, err
		}

		dailyStats := dailyByProject[project]
		weeklyStats := weeklyByProject[project]

		summary := ProjectSummary{
			Project:           project,
			Config:            projectCfg,
			OpenBeads:         open,
			RunningDispatches: runningDispatches[project],
			CompletionRates: ProjectCompletionRates{
				Daily:  completionWindowFromAgg(dailyStats),
				Weekly: completionWindowFromAgg(weeklyStats),
			},
		}
		summary.Velocity = ProjectVelocity{
			CompletedLast24h: dailyStats.completed,
			CompletedLast7d:  weeklyStats.completed,
		}
		if weeklyWindow > 0 {
			summary.Velocity.BeadsPerDay = float64(weeklyStats.completed) / weeklyWindow.Hours() * 24
		}

		summaries = append(summaries, summary)
	}

	return summaries, nil
}

func getRunningDispatchesByProject(s *store.Store) (map[string]int, error) {
	dispatches, err := s.GetRunningDispatches()
	if err != nil {
		return nil, fmt.Errorf("coordination: get running dispatches: %w", err)
	}

	running := make(map[string]int)
	for _, d := range dispatches {
		project := strings.TrimSpace(d.Project)
		if project == "" {
			continue
		}
		running[project]++
	}
	return running, nil
}

func getCompletionCountsByProject(s *store.Store, since time.Time) (map[string]completionWindowAggregation, error) {
	cutoff := since.UTC().Format(time.DateTime)
	rows, err := s.DB().Query(`
		SELECT project, status, COUNT(*)
		FROM dispatches
		WHERE status IN ('completed', 'failed')
		  AND dispatched_at >= ?
		GROUP BY project, status
	`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("coordination: query completion counts: %w", err)
	}
	defer rows.Close()

	aggregates := map[string]completionWindowAggregation{}
	for rows.Next() {
		var project, status string
		var count int
		if err := rows.Scan(&project, &status, &count); err != nil {
			return nil, fmt.Errorf("coordination: scan completion counts: %w", err)
		}
		project = strings.TrimSpace(project)
		if project == "" {
			continue
		}
		entry := aggregates[project]
		switch status {
		case "completed":
			entry.completed += count
		case "failed":
			entry.failed += count
		}
		aggregates[project] = entry
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("coordination: completion rows: %w", err)
	}
	return aggregates, nil
}

func completionWindowFromAgg(agg completionWindowAggregation) CompletionWindow {
	total := agg.completed + agg.failed
	rate := 0.0
	if total > 0 {
		rate = float64(agg.completed) / float64(total) * 100
	}
	return CompletionWindow{
		Completed: agg.completed,
		Failed:    agg.failed,
		Total:     total,
		Rate:      rate,
	}
}

func collectOpenBeadStats(ctx context.Context, beadsDir string) (OpenBeadSummary, error) {
	summary := OpenBeadSummary{
		ByPriority:      make(map[int]int),
		ByStage:         make(map[string]int),
		ByStagePriority: make(map[string]map[int]int),
	}

	beadsDir = strings.TrimSpace(beadsDir)
	if beadsDir == "" {
		return summary, nil
	}

	projectDir := filepath.Dir(beadsDir)
	if projectDir == "." || projectDir == "" {
		return summary, nil
	}
	if _, err := os.Stat(projectDir); err != nil {
		if os.IsNotExist(err) {
			return summary, nil
		}
		return summary, err
	}

	availableBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
	if err != nil {
		return summary, err
	}

	for _, bead := range availableBeads {
		if bead.Status != "open" {
			continue
		}

		summary.Total++
		summary.ByPriority[bead.Priority]++
		stage := beadStageFromLabels(bead.Labels)
		summary.ByStage[stage]++
		if _, ok := summary.ByStagePriority[stage]; !ok {
			summary.ByStagePriority[stage] = map[int]int{}
		}
		summary.ByStagePriority[stage][bead.Priority]++
	}

	return summary, nil
}

func beadStageFromLabels(labels []string) string {
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if len(label) <= len("stage:") || !strings.HasPrefix(strings.ToLower(label), "stage:") {
			continue
		}
		stage := strings.TrimSpace(label[len("stage:"):])
		if stage != "" {
			return stage
		}
	}
	return "unassigned"
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
