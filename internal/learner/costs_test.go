package learner

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/store"
)

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestCostAnalytics(t *testing.T) {
	s := tempStore(t)

	// Create test data
	now := time.Now().UTC()
	
	// Dispatch 1: Project A, Bead 1
	id1, _ := s.RecordDispatch("bead-1", "proj-a", "agent-1", "prov-1", "fast", 100, "", "prompt 1", "", "", "")
	s.UpdateDispatchStatus(id1, "completed", 0, 10.0)
	s.RecordDispatchCost(id1, 1000, 500, 0.50)

	// Dispatch 2: Project A, Bead 1 (Retry)
	id2, _ := s.RecordDispatch("bead-1", "proj-a", "agent-1", "prov-1", "fast", 101, "", "prompt 1 retry", "", "", "")
	s.UpdateDispatchStatus(id2, "completed", 0, 15.0)
	s.RecordDispatchCost(id2, 1100, 600, 0.60)

	// Dispatch 3: Project B, Bead 2
	id3, _ := s.RecordDispatch("bead-2", "proj-b", "agent-2", "prov-2", "premium", 102, "", "prompt 2", "", "", "")
	s.UpdateDispatchStatus(id3, "completed", 0, 20.0)
	s.RecordDispatchCost(id3, 2000, 1000, 2.00)

	// Dispatch 4: Old dispatch (outside window)
	id4, _ := s.RecordDispatch("bead-3", "proj-a", "agent-1", "prov-1", "fast", 103, "", "old prompt", "", "", "")
	s.UpdateDispatchStatus(id4, "completed", 0, 5.0)
	s.RecordDispatchCost(id4, 500, 200, 0.20)
	// Manually backdate the dispatch
	oldDate := now.Add(-48 * time.Hour).Format(time.DateTime)
	s.DB().Exec("UPDATE dispatches SET dispatched_at = ? WHERE id = ?", oldDate, id4)

	t.Run("GetProjectCost", func(t *testing.T) {
		// All time
		summary, err := GetProjectCost(s, "proj-a", 0)
		if err != nil {
			t.Fatal(err)
		}
		if summary.TotalCostUSD != 1.30 { // 0.50 + 0.60 + 0.20
			t.Errorf("expected 1.30, got %.2f", summary.TotalCostUSD)
		}
		if summary.DispatchCount != 3 {
			t.Errorf("expected 3 dispatches, got %d", summary.DispatchCount)
		}

		// 24h window
		summary, _ = GetProjectCost(s, "proj-a", 24*time.Hour)
		if summary.TotalCostUSD != 1.10 { // 0.50 + 0.60
			t.Errorf("expected 1.10, got %.2f", summary.TotalCostUSD)
		}
	})

	t.Run("GetBeadCost", func(t *testing.T) {
		summary, err := GetBeadCost(s, "bead-1")
		if err != nil {
			t.Fatal(err)
		}
		if summary.TotalCostUSD != 1.10 {
			t.Errorf("expected 1.10, got %.2f", summary.TotalCostUSD)
		}
		if summary.DispatchCount != 2 {
			t.Errorf("expected 2, got %d", summary.DispatchCount)
		}
	})

	t.Run("GetProviderCost", func(t *testing.T) {
		costs, err := GetProviderCost(s, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(costs) != 2 {
			t.Errorf("expected 2 providers, got %d", len(costs))
		}
		if costs["prov-1"].TotalCostUSD != 1.30 {
			t.Errorf("expected 1.30 for prov-1, got %.2f", costs["prov-1"].TotalCostUSD)
		}
		if costs["prov-2"].TotalCostUSD != 2.00 {
			t.Errorf("expected 2.00 for prov-2, got %.2f", costs["prov-2"].TotalCostUSD)
		}
	})

	t.Run("GetSprintCost", func(t *testing.T) {
		start := now.Add(-12 * time.Hour)
		end := now.Add(12 * time.Hour)
		costs, err := GetSprintCost(s, start, end)
		if err != nil {
			t.Fatal(err)
		}
		if len(costs) != 2 {
			t.Errorf("expected 2 projects, got %d", len(costs))
		}
		if costs["proj-a"].TotalCostUSD != 1.10 {
			t.Errorf("expected 1.10, got %.2f", costs["proj-a"].TotalCostUSD)
		}
	})

	t.Run("GetCostTrend", func(t *testing.T) {
		trend, err := GetCostTrend(s, 7)
		if err != nil {
			t.Fatal(err)
		}
		// Should have at least 2 entries: today and 2 days ago
		if len(trend) < 2 {
			t.Errorf("expected at least 2 days in trend, got %d", len(trend))
		}
		
		today := now.Format("2006-01-02")
		foundToday := false
		for _, dc := range trend {
			if dc.Date == today {
				foundToday = true
				if dc.CostUSD != 3.10 { // 0.5 + 0.6 + 2.0
					t.Errorf("expected 3.10 for today, got %.2f", dc.CostUSD)
				}
			}
		}
		if !foundToday {
			t.Error("today not found in trend")
		}
	})
}
