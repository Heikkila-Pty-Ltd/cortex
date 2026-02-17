package beads

import (
	"testing"
)

func TestBuildDepGraph(t *testing.T) {
	beads := []Bead{
		{ID: "a", Title: "Task A", Status: "open", DependsOn: nil},
		{ID: "b", Title: "Task B", Status: "open", DependsOn: []string{"a"}},
		{ID: "c", Title: "Task C", Status: "open", DependsOn: []string{"a", "b"}},
	}

	g := BuildDepGraph(beads)

	if len(g.Nodes()) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(g.Nodes()))
	}

	// b depends on a
	deps := g.DependsOnIDs("b")
	if len(deps) != 1 || deps[0] != "a" {
		t.Errorf("b depends on %v, want [a]", deps)
	}

	// a blocks b and c
	blocks := g.BlocksIDs("a")
	if len(blocks) != 2 {
		t.Errorf("a blocks %v, want 2 items", blocks)
	}

	// c depends on a and b
	deps = g.DependsOnIDs("c")
	if len(deps) != 2 {
		t.Errorf("c depends on %v, want 2 items", deps)
	}
}

func TestFilterUnblockedOpen_AllDepsClosed(t *testing.T) {
	beads := []Bead{
		{ID: "a", Title: "Task A", Status: "closed"},
		{ID: "b", Title: "Task B", Status: "open", DependsOn: []string{"a"}, Priority: 1, EstimateMinutes: 30},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	if len(result) != 1 {
		t.Fatalf("expected 1 unblocked bead, got %d", len(result))
	}
	if result[0].ID != "b" {
		t.Errorf("expected bead b, got %s", result[0].ID)
	}
}

func TestFilterUnblockedOpen_SomeDepsOpen(t *testing.T) {
	beads := []Bead{
		{ID: "a", Title: "Task A", Status: "open"},
		{ID: "b", Title: "Task B", Status: "open", DependsOn: []string{"a"}},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	// Only a should be unblocked (no deps); b is blocked by a
	if len(result) != 1 {
		t.Fatalf("expected 1 unblocked bead, got %d", len(result))
	}
	if result[0].ID != "a" {
		t.Errorf("expected bead a, got %s", result[0].ID)
	}
}

func TestFilterUnblockedOpen_ExcludesEpics(t *testing.T) {
	beads := []Bead{
		{ID: "e1", Title: "Epic", Status: "open", Type: "epic"},
		{ID: "t1", Title: "Task", Status: "open", Type: "task"},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	if len(result) != 1 {
		t.Fatalf("expected 1 non-epic bead, got %d", len(result))
	}
	if result[0].ID != "t1" {
		t.Errorf("expected t1, got %s", result[0].ID)
	}
}

func TestFilterUnblockedOpen_PrioritySorting(t *testing.T) {
	beads := []Bead{
		{ID: "low", Title: "Low", Status: "open", Priority: 3, EstimateMinutes: 10},
		{ID: "high", Title: "High", Status: "open", Priority: 0, EstimateMinutes: 60},
		{ID: "med", Title: "Med", Status: "open", Priority: 1, EstimateMinutes: 30},
		{ID: "med2", Title: "Med2", Status: "open", Priority: 1, EstimateMinutes: 15},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	if len(result) != 4 {
		t.Fatalf("expected 4 beads, got %d", len(result))
	}

	expected := []string{"high", "med2", "med", "low"}
	for i, id := range expected {
		if result[i].ID != id {
			t.Errorf("position %d: expected %s, got %s", i, id, result[i].ID)
		}
	}
}

func TestFilterUnblockedOpen_EmptyList(t *testing.T) {
	g := BuildDepGraph(nil)
	result := FilterUnblockedOpen(nil, g)
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d", len(result))
	}
}

func TestFilterUnblockedOpen_NoDeps(t *testing.T) {
	beads := []Bead{
		{ID: "a", Title: "A", Status: "open", Priority: 2},
		{ID: "b", Title: "B", Status: "open", Priority: 1},
	}

	g := BuildDepGraph(beads)
	result := FilterUnblockedOpen(beads, g)

	if len(result) != 2 {
		t.Fatalf("expected 2 beads, got %d", len(result))
	}
	if result[0].ID != "b" {
		t.Errorf("expected b first (priority 1), got %s", result[0].ID)
	}
}
