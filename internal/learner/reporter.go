package learner

import (
	"context"
	"fmt"
	"log/slog"
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
	dispatcher *dispatch.Dispatcher
	logger     *slog.Logger

	mu        sync.Mutex
	alertSent map[string]time.Time // dedup: key -> last sent time
}

// NewReporter creates a new Reporter.
func NewReporter(cfg config.Reporter, s *store.Store, d *dispatch.Dispatcher, logger *slog.Logger) *Reporter {
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
	var b strings.Builder
	fmt.Fprintf(&b, "## Daily Cortex Digest — %s\n\n", time.Now().Format("2006-01-02"))

	for name, proj := range projects {
		if !proj.Enabled {
			continue
		}
		v, err := GetProjectVelocity(r.store, name, 24*time.Hour)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "- **%s:** %d beads completed today\n", name, v.Completed)
	}

	events, err := r.store.GetRecentHealthEvents(24)
	if err == nil {
		fmt.Fprintf(&b, "- **Health:** %d events in last 24h\n", len(events))
	}

	// Include recommendations if enabled
	if includeRecommendations {
		r.appendRecommendations(&b)
	}

	r.dispatchMessage(ctx, b.String())
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
	_, err := r.dispatcher.Dispatch(ctx, r.cfg.AgentID, message, "", "none", "/tmp")
	if err != nil {
		r.logger.Error("failed to dispatch report", "error", err)
	}
}
