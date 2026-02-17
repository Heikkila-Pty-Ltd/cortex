package learner

import (
	"strings"
	"testing"
	"time"
)

func TestGenerateWeeklyRetroWithSampleDispatchData(t *testing.T) {
	s := tempInMemoryStore(t)
	seedDispatch(t, s, "retro-1", "project-a", "provider-a", "fast", "completed", 120, time.Now().Add(-6*24*time.Hour))
	seedDispatch(t, s, "retro-2", "project-a", "provider-a", "fast", "failed", 0, time.Now().Add(-5*24*time.Hour))
	seedDispatch(t, s, "retro-3", "project-a", "provider-a", "premium", "completed", 300, time.Now().Add(-4*24*time.Hour))

	report, err := GenerateWeeklyRetro(s)
	if err != nil {
		t.Fatalf("GenerateWeeklyRetro failed: %v", err)
	}

	if report.TotalDispatches != 3 {
		t.Fatalf("expected 3 total dispatches, got %d", report.TotalDispatches)
	}
	if report.Completed != 2 {
		t.Fatalf("expected 2 completed dispatches, got %d", report.Completed)
	}
	if report.Failed != 1 {
		t.Fatalf("expected 1 failed dispatch, got %d", report.Failed)
	}
	if report.AvgDuration != 210 {
		t.Fatalf("expected avg duration 210s, got %.1f", report.AvgDuration)
	}
}

func TestGenerateRecommendationsWithHighFailureRateProvider(t *testing.T) {
	report := &RetroReport{
		TotalDispatches: 6,
		ProviderStats: map[string]ProviderStats{
			"provider-bad": {
				Provider:    "provider-bad",
				Total:       6,
				FailureRate: 50,
			},
		},
		TierAccuracy: map[string]TierAccuracy{},
	}

	recs := generateRecommendations(report)
	found := false
	for _, rec := range recs {
		if strings.Contains(rec, "Provider provider-bad had 50% failure rate") {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("expected provider failure recommendation, got %v", recs)
	}
}

func TestGenerateRecommendationsWithHighMisclassificationTier(t *testing.T) {
	report := &RetroReport{
		TotalDispatches: 6,
		ProviderStats:   map[string]ProviderStats{},
		TierAccuracy: map[string]TierAccuracy{
			"fast": {
				Tier:                 "fast",
				Total:                6,
				MisclassificationPct: 33,
			},
		},
	}

	recs := generateRecommendations(report)
	found := false
	for _, rec := range recs {
		if strings.Contains(rec, "Tier fast has 33% misclassification rate") {
			found = true
			break
		}
	}

	if !found {
		t.Fatalf("expected tier misclassification recommendation, got %v", recs)
	}
}

func TestFormatRetroMarkdownProducesValidMarkdownTable(t *testing.T) {
	report := &RetroReport{
		Period:          "2026-02-01 to 2026-02-08",
		TotalDispatches: 10,
		Completed:       7,
		Failed:          3,
		AvgDuration:     42.0,
		ProviderStats: map[string]ProviderStats{
			"alpha": {
				Provider:    "alpha",
				Total:       10,
				SuccessRate: 70,
				FailureRate: 30,
				AvgDuration: 42,
			},
		},
		TierAccuracy: map[string]TierAccuracy{
			"fast": {
				Tier:                 "fast",
				Total:                6,
				MisclassificationPct: 33,
			},
		},
		Recommendations: []string{"Review provider alpha"},
	}

	md := FormatRetroMarkdown(report)
	if !strings.Contains(md, "# Weekly Cortex Retrospective") {
		t.Fatalf("missing title: %q", md)
	}
	if !strings.Contains(md, "| Provider | Total | Success | Failure | Avg Duration |") {
		t.Fatalf("missing provider table header: %q", md)
	}
	if !strings.Contains(md, "| alpha | 10 | 70% | 30% | 42.0s |") {
		t.Fatalf("missing provider row: %q", md)
	}
	if !strings.Contains(md, "## Recommendations") {
		t.Fatalf("missing recommendations section: %q", md)
	}
}

func TestGenerateWeeklyRetroWithEmptyData(t *testing.T) {
	s := tempInMemoryStore(t)

	report, err := GenerateWeeklyRetro(s)
	if err != nil {
		t.Fatalf("GenerateWeeklyRetro failed: %v", err)
	}

	if report.TotalDispatches != 0 {
		t.Fatalf("expected zero dispatches, got %d", report.TotalDispatches)
	}

	found := false
	for _, rec := range report.Recommendations {
		if strings.Contains(rec, "No dispatches in the past week") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected no-dispatch recommendation, got %v", report.Recommendations)
	}
}
