package graph

import "testing"

func TestBuildDepGraph(t *testing.T) {
	tasks := []Task{
		{ID: "a", Status: "open", Labels: []string{"stage:init"}},
		{ID: "b", Status: "open", DependsOn: []string{"a", "missing"}},
		{ID: "c", Status: "open", DependsOn: []string{"a"}},
	}

	g := BuildDepGraph(tasks)
	if len(g.Nodes()) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(g.Nodes()))
	}

	deps := g.DependsOnIDs("b")
	if len(deps) != 2 || deps[0] != "a" || deps[1] != "missing" {
		t.Fatalf("unexpected dependencies for b: %v", deps)
	}

	blocks := g.BlocksIDs("a")
	if len(blocks) != 2 || blocks[0] != "b" || blocks[1] != "c" {
		t.Fatalf("unexpected blockers for a: %v", blocks)
	}

	reverseForMissing := g.BlocksIDs("missing")
	if len(reverseForMissing) != 1 || reverseForMissing[0] != "b" {
		t.Fatalf("expected missing dependency to be tracked as blocker for b, got %v", reverseForMissing)
	}

	if g.DependsOnIDs("does-not-exist") != nil {
		t.Fatalf("expected nil depends-on list for unknown task")
	}

	tasks[0].ID = "mutated-a"
	tasks[1].DependsOn[0] = "ghost"
	tasks[1].Labels = []string{"stage:changed"}

	if node := g.Nodes()["a"]; node == nil || node.ID != "a" {
		t.Fatalf("expected node 'a' to remain stable after mutating input slice")
	}
	if depends := g.DependsOnIDs("b"); depends[1] != "missing" {
		t.Fatalf("expected copied dependency slice for b, got %v", depends)
	}
	if node := g.Nodes()["b"]; hasStageLabel(*node) {
		t.Fatalf("expected graph task copy to keep labels from original input")
	}
}

func TestFilterUnblockedOpen_DependencyResolution(t *testing.T) {
	tasks := []Task{
		{ID: "closed", Status: "closed"},
		{ID: "open", Status: "open"},
		{ID: "ok", Status: "open", DependsOn: []string{"closed"}},
		{ID: "blocked-by-open", Status: "open", DependsOn: []string{"open"}},
		{ID: "blocked-by-missing", Status: "open", DependsOn: []string{"ghost"}},
	}

	g := BuildDepGraph(tasks)
	result := FilterUnblockedOpen(tasks, g)

	// "open" (no deps, status=open) and "ok" (dep on closed task) are both unblocked.
	expected := []string{"open", "ok"}
	if len(result) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, ids(result))
	}
	for i, id := range expected {
		if result[i].ID != id {
			t.Fatalf("result[%d] = %s, want %s (full: %v)", i, result[i].ID, id, ids(result))
		}
	}
}

func TestFilterUnblockedOpen_ExcludesClosedAndEpic(t *testing.T) {
	tasks := []Task{
		{ID: "closed", Status: "closed", DependsOn: []string{}},
		{ID: "epic", Status: "open", Type: "epic", DependsOn: []string{}},
		{ID: "task", Status: "open", Type: "task", DependsOn: []string{}},
	}

	g := BuildDepGraph(tasks)
	result := FilterUnblockedOpen(tasks, g)

	if len(result) != 1 || result[0].ID != "task" {
		t.Fatalf("expected only non-epic open task, got %v", ids(result))
	}
}

func TestFilterUnblockedOpen_SortingWithStageLabelsAndStableTies(t *testing.T) {
	tasks := []Task{
		{ID: "low-stage-late", Status: "open", Priority: 0, EstimateMinutes: 90, Labels: []string{"stage:release"}},
		{ID: "low-nonstage", Status: "open", Priority: 0, EstimateMinutes: 10, Labels: []string{"bug"}},
		{ID: "low-stage-early", Status: "open", Priority: 0, EstimateMinutes: 30, Labels: []string{"stage:bootstrap"}},
		{ID: "high-stage", Status: "open", Priority: 1, EstimateMinutes: 20, Labels: []string{"stage:plan"}},
		{ID: "high-nonstage", Status: "open", Priority: 1, EstimateMinutes: 5, Labels: []string{"chore"}},
		{ID: "stable-a", Status: "open", Priority: 2, EstimateMinutes: 12},
		{ID: "stable-b", Status: "open", Priority: 2, EstimateMinutes: 12},
	}

	g := BuildDepGraph(tasks)
	result := FilterUnblockedOpen(tasks, g)

	expected := []string{
		"low-stage-early",
		"low-stage-late",
		"low-nonstage",
		"high-stage",
		"high-nonstage",
		"stable-a",
		"stable-b",
	}

	if len(result) != len(expected) {
		t.Fatalf("expected %d tasks, got %d", len(expected), len(result))
	}
	for i, id := range expected {
		if result[i].ID != id {
			t.Fatalf("unexpected order at index %d: got %s, want %s", i, result[i].ID, id)
		}
	}
}

func TestFilterUnblockedOpen_NilGraph(t *testing.T) {
	tasks := []Task{
		{ID: "open", Status: "open"},
		{ID: "blocked", Status: "open", DependsOn: []string{"ghost"}},
	}

	result := FilterUnblockedOpen(tasks, nil)
	if len(result) != 1 || result[0].ID != "open" {
		t.Fatalf("expected only open task with no deps, got %v", ids(result))
	}
}

func TestAccessorsReturnCopiesForDependencies(t *testing.T) {
	tasks := []Task{
		{ID: "a", Status: "open", DependsOn: []string{"b"}},
		{ID: "b", Status: "closed"},
	}
	g := BuildDepGraph(tasks)

	// Mutating returned DependsOnIDs should not affect internal state.
	deps := g.DependsOnIDs("a")
	deps[0] = "corrupted"
	if g.DependsOnIDs("a")[0] != "b" {
		t.Fatal("DependsOnIDs returned an alias to internal slice")
	}

	// Mutating returned BlocksIDs should not affect internal state.
	blocks := g.BlocksIDs("b")
	blocks[0] = "corrupted"
	if g.BlocksIDs("b")[0] != "a" {
		t.Fatal("BlocksIDs returned an alias to internal slice")
	}

	if g.Nodes() == nil {
		t.Fatal("Nodes on a valid graph should not be nil")
	}

	var nilGraph *DepGraph
	if nilGraph.Nodes() != nil {
		t.Fatal("Nodes on nil graph should return nil")
	}
	if nilGraph.DependsOnIDs("x") != nil {
		t.Fatal("DependsOnIDs on nil graph should return nil")
	}
	if nilGraph.BlocksIDs("x") != nil {
		t.Fatal("BlocksIDs on nil graph should return nil")
	}
}

func ids(tasks []Task) []string {
	out := make([]string, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, task.ID)
	}
	return out
}
