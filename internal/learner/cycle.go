package learner

import (
	"context"
	"log/slog"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

// CycleWorker runs periodic analysis to generate recommendations.
type CycleWorker struct {
	cfg    config.Learner
	store  *store.Store
	engine *RecommendationEngine
	recStore *RecommendationStore
	logger *slog.Logger
}

// NewCycleWorker creates a new learner cycle worker.
func NewCycleWorker(cfg config.Learner, s *store.Store, logger *slog.Logger) *CycleWorker {
	return &CycleWorker{
		cfg:      cfg,
		store:    s,
		engine:   NewRecommendationEngine(s),
		recStore: NewRecommendationStore(s),
		logger:   logger,
	}
}

// Start begins the periodic learner cycle.
func (w *CycleWorker) Start(ctx context.Context) {
	if !w.cfg.Enabled {
		w.logger.Info("learner cycle disabled")
		return
	}

	w.logger.Info("starting learner cycle", 
		"interval", w.cfg.CycleInterval.Duration,
		"analysis_window", w.cfg.AnalysisWindow.Duration)

	// Run initial analysis after a brief startup delay
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
			w.runCycle(ctx)
		}
	}()

	// Set up periodic ticker
	ticker := time.NewTicker(w.cfg.CycleInterval.Duration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("learner cycle stopping")
			return
		case <-ticker.C:
			w.runCycle(ctx)
		}
	}
}

// runCycle performs one complete analysis and recommendation cycle.
func (w *CycleWorker) runCycle(ctx context.Context) {
	cycleStart := time.Now()
	w.logger.Info("starting learner cycle", "analysis_window", w.cfg.AnalysisWindow.Duration)

	// Check if we have sufficient data
	if !w.hasSufficientData(ctx) {
		w.logger.Debug("insufficient data for meaningful analysis, skipping cycle")
		return
	}

	// Generate recommendations
	recommendations, err := w.engine.GenerateRecommendations(w.cfg.AnalysisWindow.Duration)
	if err != nil {
		w.logger.Error("failed to generate recommendations", "error", err)
		return
	}

	if len(recommendations) == 0 {
		w.logger.Info("no recommendations generated", "cycle_duration", time.Since(cycleStart))
		return
	}

	// Store recommendations
	stored := 0
	for _, rec := range recommendations {
		if err := w.recStore.StoreRecommendation(rec); err != nil {
			w.logger.Error("failed to store recommendation", "recommendation_id", rec.ID, "error", err)
		} else {
			stored++
		}
	}

	w.logger.Info("learner cycle completed",
		"recommendations_generated", len(recommendations),
		"recommendations_stored", stored,
		"cycle_duration", time.Since(cycleStart))

	// Log high-confidence recommendations for visibility
	for _, rec := range recommendations {
		if rec.Confidence >= 80.0 {
			w.logger.Info("high confidence recommendation",
				"type", rec.Type,
				"confidence", rec.Confidence,
				"action", rec.SuggestedAction)
		}
	}
}

// hasSufficientData checks if there's enough dispatch history to generate meaningful recommendations.
func (w *CycleWorker) hasSufficientData(ctx context.Context) bool {
	cutoff := time.Now().Add(-w.cfg.AnalysisWindow.Duration).UTC().Format(time.DateTime)
	
	var count int
	err := w.store.DB().QueryRowContext(ctx, 
		"SELECT COUNT(*) FROM dispatches WHERE dispatched_at >= ?", 
		cutoff).Scan(&count)
		
	if err != nil {
		w.logger.Warn("failed to check dispatch count", "error", err)
		return false
	}

	// Require at least 10 dispatches for meaningful analysis
	return count >= 10
}

// GetLatestRecommendations returns the most recent recommendations for API/reporting use.
func (w *CycleWorker) GetLatestRecommendations(hours int) ([]Recommendation, error) {
	return w.recStore.GetRecentRecommendations(hours)
}