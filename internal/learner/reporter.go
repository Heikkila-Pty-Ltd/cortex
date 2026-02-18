package learner

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
	"github.com/antigravity-dev/cortex/internal/store"
)

// Reporter handles daily digests and event-driven alerts via Matrix.
type Reporter struct {
	cfg        config.Reporter
	store      *store.Store
	dispatcher dispatch.DispatcherInterface
	logger     *slog.Logger

	mu        sync.Mutex
	alertSent map[string]time.Time // dedup: key -> last sent time
}

const defaultReporterAgentID = "main"

// NewReporter creates a new Reporter.
func NewReporter(cfg config.Reporter, s *store.Store, d dispatch.DispatcherInterface, logger *slog.Logger) *Reporter {
	return &Reporter{
		cfg:        cfg,
		store:      s,
		dispatcher: d,
		logger:     logger,
		alertSent:  make(map[string]time.Time),
	}
}

// SendDigest dispatches the daily digest message via openclaw agent.
func (r *Reporter) SendDigest(ctx context.Context, projects map[string]config.Project, includeRecommendations bool) {
	projectNames := make([]string, 0, len(projects))
	for name, project := range projects {
		if project.Enabled {
			projectNames = append(projectNames, name)
		}
	}
	sort.Strings(projectNames)

	for _, projectName := range projectNames {
		r.SendProjectDigest(ctx, projectName, projects[projectName], includeRecommendations)
	}
}

// SendProjectDigest dispatches a project-specific daily digest via the project's scrum agent.
func (r *Reporter) SendProjectDigest(ctx context.Context, projectName string, project config.Project, includeRecommendations bool) {
	if !project.Enabled {
		return
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Daily Cortex Digest — %s (%s)\n\n", projectName, time.Now().Format("2006-01-02"))
	if room := strings.TrimSpace(project.MatrixRoom); room != "" {
		fmt.Fprintf(&b, "- **Matrix Room:** `%s`\n", room)
	}

	v, err := GetProjectVelocity(r.store, projectName, 24*time.Hour)
	if err != nil {
		r.logger.Warn("failed to compute project velocity for digest", "project", projectName, "error", err)
		fmt.Fprintf(&b, "- **%s:** velocity unavailable\n", projectName)
	} else {
		fmt.Fprintf(&b, "- **%s:** %d beads completed today\n", projectName, v.Completed)
	}

	events, err := r.store.GetRecentHealthEvents(24)
	if err == nil {
		fmt.Fprintf(&b, "- **Health:** %d events in last 24h\n", len(events))
	}

	if includeRecommendations {
		r.appendRecommendations(&b)
	}

	r.dispatchProjectMessage(ctx, projectName, b.String())
}

// appendRecommendations adds recent recommendations to the digest.
func (r *Reporter) appendRecommendations(b *strings.Builder) {
	recStore := NewRecommendationStore(r.store)
	recommendations, err := recStore.GetRecentRecommendations(24)
	if err != nil {
		r.logger.Warn("failed to get recent recommendations for digest", "error", err)
		return
	}

	if len(recommendations) == 0 {
		return
	}

	fmt.Fprintf(b, "\n## 🧠 System Recommendations\n\n")

	highConfidenceCount := 0
	for _, rec := range recommendations {
		if rec.Confidence >= 70.0 {
			highConfidenceCount++
			confidence := "Medium"
			if rec.Confidence >= 85.0 {
				confidence = "High"
			}

			fmt.Fprintf(b, "- **%s Confidence**: %s\n",
				confidence, rec.SuggestedAction)
			fmt.Fprintf(b, "  *%s*\n\n", rec.Rationale)
		}
	}

	if highConfidenceCount == 0 {
		fmt.Fprintf(b, "No high-confidence recommendations at this time.\n\n")
	} else {
		fmt.Fprintf(b, "*Based on analysis of recent dispatch outcomes*\n\n")
	}
}

// SendAlert sends an immediate alert, with 1h dedup.
func (r *Reporter) SendAlert(ctx context.Context, alertType string, message string) {
	r.mu.Lock()
	lastSent, exists := r.alertSent[alertType]
	if exists && time.Since(lastSent) < time.Hour {
		r.mu.Unlock()
		return // dedup
	}
	r.alertSent[alertType] = time.Now()
	r.mu.Unlock()

	alert := fmt.Sprintf("**ALERT: %s**\n\n%s", alertType, message)
	r.dispatchMessage(ctx, alert)
}

func (r *Reporter) dispatchMessage(ctx context.Context, message string) {
	agent := strings.TrimSpace(r.cfg.AgentID)
	if agent == "" {
		agent = defaultReporterAgentID
	}
	_, err := r.dispatcher.Dispatch(ctx, agent, message, "", "none", "/tmp")
	if err != nil {
		r.logger.Error("failed to dispatch report", "error", err)
	}
}

func (r *Reporter) dispatchProjectMessage(ctx context.Context, projectName, message string) {
	primary := fmt.Sprintf("%s-scrum", strings.TrimSpace(projectName))
	fallback := strings.TrimSpace(r.cfg.AgentID)
	if fallback == "" {
		fallback = defaultReporterAgentID
	}
	if primary == "-scrum" {
		primary = fallback
	}

	_, err := r.dispatcher.Dispatch(ctx, primary, message, "", "none", "/tmp")
	if err == nil {
		return
	}
	if primary == fallback {
		r.logger.Error("failed to dispatch project digest", "project", projectName, "agent", primary, "error", err)
		return
	}

	r.logger.Warn("project digest dispatch failed, falling back to default agent",
		"project", projectName,
		"agent", primary,
		"fallback", fallback,
		"error", err)
	_, fallbackErr := r.dispatcher.Dispatch(ctx, fallback, message, "", "none", "/tmp")
	if fallbackErr != nil {
		r.logger.Error("failed to dispatch project digest via fallback",
			"project", projectName,
			"fallback", fallback,
			"error", fallbackErr)
	}
}
