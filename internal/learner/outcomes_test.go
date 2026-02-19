package learner

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetProviderStatsAggregationCorrectness(t *testing.T) {
	s := tempInMemoryStore(t)
	now := time.Now().Add(-2 * time.Hour)

	seedDispatch(t, s, "provider-1", "project-a", "provider-a", "fast", "completed", 60, now)
	seedDispatch(t, s, "provider-2", "project-a", "provider-a", "fast", "completed", 120, now.Add(time.Minute))
	seedDispatch(t, s, "provider-3", "project-a", "provider-a", "fast", "failed", 0, now.Add(2*time.Minute))

	stats, err := GetProviderStats(s, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProviderStats failed: %v", err)
	}

	ps, ok := stats["provider-a"]
	if !ok {
		t.Fatalf("missing provider-a stats: %v", stats)
	}
	if ps.Total != 3 {
		t.Fatalf("expected total=3, got %d", ps.Total)
	}
	if ps.Completed != 2 {
		t.Fatalf("expected completed=2, got %d", ps.Completed)
	}
	if ps.Failed != 1 {
		t.Fatalf("expected failed=1, got %d", ps.Failed)
	}
	if math.Abs(ps.AvgDuration-90) > 0.0001 {
		t.Fatalf("expected avg duration 90, got %.4f", ps.AvgDuration)
	}
	if math.Abs(ps.SuccessRate-66.6666667) > 0.1 {
		t.Fatalf("expected success rate about 66.67, got %.2f", ps.SuccessRate)
	}
	if math.Abs(ps.FailureRate-33.3333333) > 0.1 {
		t.Fatalf("expected failure rate about 33.33, got %.2f", ps.FailureRate)
	}
}

func TestGetProviderStatsWithMultipleProviders(t *testing.T) {
	s := tempInMemoryStore(t)
	now := time.Now().Add(-time.Hour)

	seedDispatch(t, s, "multi-1", "project-a", "provider-a", "fast", "completed", 100, now)
	seedDispatch(t, s, "multi-2", "project-a", "provider-b", "premium", "failed", 0, now.Add(time.Minute))

	stats, err := GetProviderStats(s, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProviderStats failed: %v", err)
	}

	if len(stats) != 2 {
		t.Fatalf("expected 2 providers, got %d (%v)", len(stats), stats)
	}
	if _, ok := stats["provider-a"]; !ok {
		t.Fatalf("expected provider-a in stats, got %v", stats)
	}
	if _, ok := stats["provider-b"]; !ok {
		t.Fatalf("expected provider-b in stats, got %v", stats)
	}
}

func TestGetTierAccuracyWithUnderestimatedAndOverestimatedCases(t *testing.T) {
	s := tempInMemoryStore(t)
	now := time.Now().Add(-time.Hour)

	seedDispatch(t, s, "tier-1", "project-a", "provider-a", "fast", "completed", 100*60, now)                      // underestimated
	seedDispatch(t, s, "tier-2", "project-a", "provider-a", "fast", "completed", 10*60, now.Add(time.Minute))      // correct
	seedDispatch(t, s, "tier-3", "project-a", "provider-a", "premium", "completed", 20*60, now.Add(2*time.Minute)) // overestimated
	seedDispatch(t, s, "tier-4", "project-a", "provider-a", "premium", "completed", 45*60, now.Add(3*time.Minute)) // correct

	acc, err := GetTierAccuracy(s, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetTierAccuracy failed: %v", err)
	}

	fast := acc["fast"]
	if fast.Total != 2 || fast.Underestimated != 1 || fast.Overestimated != 0 {
		t.Fatalf("unexpected fast tier accuracy: %+v", fast)
	}
	if math.Abs(fast.MisclassificationPct-50) > 0.0001 {
		t.Fatalf("expected fast misclassification 50%%, got %.4f", fast.MisclassificationPct)
	}

	premium := acc["premium"]
	if premium.Total != 2 || premium.Underestimated != 0 || premium.Overestimated != 1 {
		t.Fatalf("unexpected premium tier accuracy: %+v", premium)
	}
	if math.Abs(premium.MisclassificationPct-50) > 0.0001 {
		t.Fatalf("expected premium misclassification 50%%, got %.4f", premium.MisclassificationPct)
	}
}

func TestGetProjectVelocityCalculation(t *testing.T) {
	s := tempInMemoryStore(t)
	now := time.Now().Add(-6 * time.Hour)

	seedDispatch(t, s, "velocity-1", "project-a", "provider-a", "fast", "completed", 120, now)
	seedDispatch(t, s, "velocity-2", "project-a", "provider-a", "fast", "completed", 240, now.Add(time.Minute))
	seedDispatch(t, s, "velocity-3", "project-a", "provider-a", "fast", "failed", 0, now.Add(2*time.Minute))

	v, err := GetProjectVelocity(s, "project-a", 48*time.Hour)
	if err != nil {
		t.Fatalf("GetProjectVelocity failed: %v", err)
	}

	if v.Completed != 2 {
		t.Fatalf("expected completed=2, got %d", v.Completed)
	}
	if math.Abs(v.AvgDurationS-180) > 0.0001 {
		t.Fatalf("expected avg duration 180, got %.4f", v.AvgDurationS)
	}
	if math.Abs(v.BeadsPerDay-1.0) > 0.0001 {
		t.Fatalf("expected beads/day 1.0, got %.4f", v.BeadsPerDay)
	}
}

func TestOutcomesWithZeroDispatches(t *testing.T) {
	s := tempInMemoryStore(t)

	providerStats, err := GetProviderStats(s, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProviderStats failed: %v", err)
	}
	if len(providerStats) != 0 {
		t.Fatalf("expected empty provider stats, got %v", providerStats)
	}

	tierAccuracy, err := GetTierAccuracy(s, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetTierAccuracy failed: %v", err)
	}
	if len(tierAccuracy) != 0 {
		t.Fatalf("expected empty tier accuracy, got %v", tierAccuracy)
	}

	velocity, err := GetProjectVelocity(s, "missing-project", 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProjectVelocity failed: %v", err)
	}
	if velocity.Completed != 0 || velocity.AvgDurationS != 0 || velocity.BeadsPerDay != 0 {
		t.Fatalf("expected zeroed velocity, got %+v", velocity)
	}
}

func TestGetProviderStatsReadsFailureCategoryFromLogPath(t *testing.T) {
	s := tempInMemoryStore(t)
	now := time.Now().Add(-time.Hour)
	seedDispatch(t, s, "logcat-1", "project-a", "provider-a", "fast", "failed", 0, now)

	logPath := filepath.Join(t.TempDir(), "dispatch.log")
	if err := os.WriteFile(logPath, []byte("fatal: Permission denied while writing file"), 0644); err != nil {
		t.Fatalf("write log file: %v", err)
	}

	_, err := s.DB().Exec(`UPDATE dispatches SET log_path = ?, failure_category = '' WHERE bead_id = ?`, logPath, "logcat-1")
	if err != nil {
		t.Fatalf("update dispatch log path failed: %v", err)
	}

	stats, err := GetProviderStats(s, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProviderStats failed: %v", err)
	}

	ps, ok := stats["provider-a"]
	if !ok {
		t.Fatalf("missing provider-a stats: %v", stats)
	}
	if got := ps.FailureCategories["permission_denied"]; got != 1 {
		t.Fatalf("expected permission_denied=1 from log_path parsing, got %d (%v)", got, ps.FailureCategories)
	}
}

func TestGetFastTierCLIComparison(t *testing.T) {
	s := tempInMemoryStore(t)
	now := time.Now().Add(-time.Hour)

	seedDispatch(t, s, "ab-1", "project-a", "kilo-model", "fast", "completed", 60, now)
	seedDispatch(t, s, "ab-2", "project-a", "kilo-model", "fast", "failed", 0, now.Add(time.Minute))
	seedDispatch(t, s, "ab-3", "project-a", "aider-model", "fast", "completed", 60, now.Add(2*time.Minute))
	seedDispatch(t, s, "ab-4", "project-a", "aider-model", "fast", "completed", 60, now.Add(3*time.Minute))

	stats, err := GetFastTierCLIComparison(s, 24*time.Hour, []string{"kilo", "aider"})
	if err != nil {
		t.Fatalf("GetFastTierCLIComparison failed: %v", err)
	}

	byCLI := map[string]FastTierCLIStats{}
	for _, stat := range stats {
		byCLI[stat.CLI] = stat
	}

	if byCLI["kilo"].Total != 2 || math.Abs(byCLI["kilo"].SuccessRate-50) > 0.01 {
		t.Fatalf("unexpected kilo stats: %+v", byCLI["kilo"])
	}
	if byCLI["aider"].Total != 2 || math.Abs(byCLI["aider"].SuccessRate-100) > 0.01 {
		t.Fatalf("unexpected aider stats: %+v", byCLI["aider"])
	}
}

func TestGetProviderStatsIgnoresNonFailedStatusesInFailureCategories(t *testing.T) {
	s := tempInMemoryStore(t)
	now := time.Now().Add(-time.Hour)

	seedDispatch(t, s, "status-filter-1", "project-a", "provider-a", "fast", "failed", 0, now)
	seedDispatch(t, s, "status-filter-2", "project-a", "provider-a", "fast", "pending_retry", 0, now.Add(time.Minute))
	seedDispatch(t, s, "status-filter-3", "project-a", "provider-a", "fast", "running", 0, now.Add(2*time.Minute))

	if _, err := s.DB().Exec(`UPDATE dispatches SET failure_category = 'test_failure' WHERE bead_id = ?`, "status-filter-1"); err != nil {
		t.Fatalf("update failed dispatch category: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE dispatches SET failure_category = 'unknown' WHERE bead_id = ?`, "status-filter-2"); err != nil {
		t.Fatalf("update pending_retry dispatch category: %v", err)
	}
	if _, err := s.DB().Exec(`UPDATE dispatches SET failure_category = 'unknown' WHERE bead_id = ?`, "status-filter-3"); err != nil {
		t.Fatalf("update running dispatch category: %v", err)
	}

	stats, err := GetProviderStats(s, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProviderStats failed: %v", err)
	}

	ps := stats["provider-a"]
	if got := ps.FailureCategories["test_failure"]; got != 1 {
		t.Fatalf("expected test_failure=1, got %d (%v)", got, ps.FailureCategories)
	}
	if got := ps.FailureCategories["unknown"]; got != 0 {
		t.Fatalf("expected unknown failures from non-failed statuses to be ignored, got %d (%v)", got, ps.FailureCategories)
	}
}

func TestGetProjectVelocitiesAcrossProjects(t *testing.T) {
	s := tempInMemoryStore(t)
	now := time.Now().Add(-6 * time.Hour)

	seedDispatch(t, s, "velocity-a-1", "project-a", "provider-a", "fast", "completed", 120, now)
	seedDispatch(t, s, "velocity-a-2", "project-a", "provider-a", "fast", "completed", 180, now.Add(time.Minute))
	seedDispatch(t, s, "velocity-b-1", "project-b", "provider-b", "fast", "completed", 60, now.Add(time.Minute*2))
	seedDispatch(t, s, "velocity-b-fail", "project-b", "provider-b", "fast", "failed", 0, now.Add(time.Minute*3))

	velocities, err := GetProjectVelocities(s, []string{"project-a", "project-b", "project-missing"}, 24*time.Hour)
	if err != nil {
		t.Fatalf("GetProjectVelocities failed: %v", err)
	}

	a, ok := velocities["project-a"]
	if !ok {
		t.Fatalf("missing project-a velocity: %#v", velocities)
	}
	if a.Completed != 2 {
		t.Fatalf("expected project-a completed=2, got %d", a.Completed)
	}
	if a.Project != "project-a" {
		t.Fatalf("expected project-a, got %q", a.Project)
	}
	if a.BeadsPerDay <= 0 {
		t.Fatalf("expected project-a beads/day > 0, got %.4f", a.BeadsPerDay)
	}

	b, ok := velocities["project-b"]
	if !ok {
		t.Fatalf("missing project-b velocity: %#v", velocities)
	}
	if b.Completed != 1 {
		t.Fatalf("expected project-b completed=1, got %d", b.Completed)
	}

	missing, ok := velocities["project-missing"]
	if !ok {
		t.Fatalf("expected missing project key with zero values: %#v", velocities)
	}
	if missing.Completed != 0 || missing.BeadsPerDay != 0 {
		t.Fatalf("expected zero velocity for missing project, got %+v", missing)
	}
}
