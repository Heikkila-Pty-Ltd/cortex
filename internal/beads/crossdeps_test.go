package beads

import (
	"testing"
)

func TestParseCrossDep(t *testing.T) {
	tests := []struct {
		input       string
		wantProject string
		wantBead    string
		wantCross   bool
	}{
		{"cortex-abc", "", "cortex-abc", false},
		{"hg-website:cortex-xyz", "hg-website", "cortex-xyz", true},
		{"proj:bead-123", "proj", "bead-123", true},
		{":bead", "", ":bead", false}, // leading colon = not valid cross dep
		{"project:sub:bead", "project", "sub:bead", true}, // only first colon splits
		{"", "", "", false}, // empty string
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			project, beadID, isCross := ParseCrossDep(tt.input)
			if project != tt.wantProject {
				t.Errorf("project: got %q, want %q", project, tt.wantProject)
			}
			if beadID != tt.wantBead {
				t.Errorf("beadID: got %q, want %q", beadID, tt.wantBead)
			}
			if isCross != tt.wantCross {
				t.Errorf("isCross: got %v, want %v", isCross, tt.wantCross)
			}
		})
	}
}

func TestCrossProjectGraph_IsCrossDepResolved(t *testing.T) {
	g := &CrossProjectGraph{
		Projects: map[string]map[string]*Bead{
			"project-a": {
				"bead-1": {ID: "bead-1", Status: "closed"},
				"bead-2": {ID: "bead-2", Status: "open"},
			},
			"project-b": {
				"bead-3": {ID: "bead-3", Status: "closed"},
			},
		},
	}

	tests := []struct {
		name     string
		project  string
		beadID   string
		resolved bool
	}{
		{"closed bead in project-a", "project-a", "bead-1", true},
		{"open bead in project-a", "project-a", "bead-2", false},
		{"closed bead in project-b", "project-b", "bead-3", true},
		{"unknown bead", "project-a", "bead-999", false},
		{"unknown project", "project-z", "bead-1", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := g.IsCrossDepResolved(tt.project, tt.beadID)
			if got != tt.resolved {
				t.Errorf("IsCrossDepResolved(%q, %q) = %v, want %v", tt.project, tt.beadID, got, tt.resolved)
			}
		})
	}
}

func TestCrossProjectGraph_GetCrossProjectBlockers(t *testing.T) {
	g := &CrossProjectGraph{
		Projects: map[string]map[string]*Bead{
			"project-a": {
				"bead-1": {ID: "bead-1", Status: "closed"},
				"bead-2": {ID: "bead-2", Status: "open"},
			},
			"project-b": {
				"bead-3": {ID: "bead-3", Status: "closed"},
			},
		},
	}

	tests := []struct {
		name          string
		bead          Bead
		wantBlockers  int
		wantProjects  []string
		wantBeadIDs   []string
	}{
		{
			name: "no dependencies",
			bead: Bead{ID: "test-1", DependsOn: []string{}},
			wantBlockers: 0,
		},
		{
			name: "only local dependencies",
			bead: Bead{ID: "test-2", DependsOn: []string{"local-1", "local-2"}},
			wantBlockers: 0,
		},
		{
			name: "all cross-project deps resolved",
			bead: Bead{ID: "test-3", DependsOn: []string{"project-a:bead-1", "project-b:bead-3"}},
			wantBlockers: 0,
		},
		{
			name: "one cross-project dep unresolved",
			bead: Bead{ID: "test-4", DependsOn: []string{"project-a:bead-2"}},
			wantBlockers: 1,
			wantProjects: []string{"project-a"},
			wantBeadIDs: []string{"bead-2"},
		},
		{
			name: "mixed local and cross-project deps",
			bead: Bead{ID: "test-5", DependsOn: []string{"local-1", "project-a:bead-2", "project-b:bead-3"}},
			wantBlockers: 1,
			wantProjects: []string{"project-a"},
			wantBeadIDs: []string{"bead-2"},
		},
		{
			name: "multiple unresolved cross-project deps",
			bead: Bead{ID: "test-6", DependsOn: []string{"project-a:bead-2", "project-z:bead-999"}},
			wantBlockers: 2,
			wantProjects: []string{"project-a", "project-z"},
			wantBeadIDs: []string{"bead-2", "bead-999"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blockers := g.GetCrossProjectBlockers(tt.bead)
			if len(blockers) != tt.wantBlockers {
				t.Errorf("got %d blockers, want %d", len(blockers), tt.wantBlockers)
			}
			if tt.wantBlockers > 0 {
				for i, blocker := range blockers {
					if blocker.Project != tt.wantProjects[i] {
						t.Errorf("blocker[%d].Project = %q, want %q", i, blocker.Project, tt.wantProjects[i])
					}
					if blocker.BeadID != tt.wantBeadIDs[i] {
						t.Errorf("blocker[%d].BeadID = %q, want %q", i, blocker.BeadID, tt.wantBeadIDs[i])
					}
				}
			}
		})
	}
}

func TestFilterUnblockedCrossProject(t *testing.T) {
	// Create local beads
	beads := []Bead{
		{ID: "bead-1", Status: "open", Type: "task", DependsOn: []string{}},
		{ID: "bead-2", Status: "open", Type: "task", DependsOn: []string{"bead-1"}},
		{ID: "bead-3", Status: "open", Type: "task", DependsOn: []string{"project-a:dep-1"}},
		{ID: "bead-4", Status: "open", Type: "task", DependsOn: []string{"project-a:dep-2"}},
		{ID: "bead-5", Status: "open", Type: "task", DependsOn: []string{"bead-1", "project-a:dep-1"}},
		{ID: "bead-6", Status: "closed", Type: "task", DependsOn: []string{}},
		{ID: "bead-7", Status: "open", Type: "epic", DependsOn: []string{}},
	}

	localGraph := BuildDepGraph(beads)

	crossGraph := &CrossProjectGraph{
		Projects: map[string]map[string]*Bead{
			"project-a": {
				"dep-1": {ID: "dep-1", Status: "closed"},
				"dep-2": {ID: "dep-2", Status: "open"},
			},
		},
	}

	t.Run("with cross-project graph", func(t *testing.T) {
		unblocked := FilterUnblockedCrossProject(beads, localGraph, crossGraph)

		// Expected: bead-1 (no deps), bead-3 (cross-dep resolved)
		// Not expected: bead-2 (local dep unresolved), bead-4 (cross-dep unresolved),
		//               bead-5 (local dep unresolved), bead-6 (closed), bead-7 (epic)
		expectedIDs := map[string]bool{
			"bead-1": true,
			"bead-3": true,
		}

		if len(unblocked) != len(expectedIDs) {
			t.Errorf("got %d unblocked beads, want %d", len(unblocked), len(expectedIDs))
		}

		for _, b := range unblocked {
			if !expectedIDs[b.ID] {
				t.Errorf("unexpected bead %q in unblocked list", b.ID)
			}
		}
	})

	t.Run("without cross-project graph", func(t *testing.T) {
		unblocked := FilterUnblockedCrossProject(beads, localGraph, nil)

		// Without cross-graph, cross-project deps are ignored
		// Expected: bead-1, bead-3, bead-4 (all have no local blockers)
		// bead-5 is blocked by local dep bead-1 which is open
		expectedIDs := map[string]bool{
			"bead-1": true,
			"bead-3": true,
			"bead-4": true,
		}

		if len(unblocked) != len(expectedIDs) {
			t.Errorf("got %d unblocked beads, want %d", len(unblocked), len(expectedIDs))
		}

		for _, b := range unblocked {
			if !expectedIDs[b.ID] {
				t.Errorf("unexpected bead %q in unblocked list", b.ID)
			}
		}
	})
}

func TestFilterUnblockedCrossProject_ComplexScenario(t *testing.T) {
	// Complex scenario with mixed local and cross-project dependencies
	beads := []Bead{
		{ID: "local-1", Status: "closed", Type: "task"},
		{ID: "local-2", Status: "open", Type: "task", DependsOn: []string{"local-1"}},
		{ID: "local-3", Status: "open", Type: "task", DependsOn: []string{"local-1", "proj-x:remote-1"}},
		{ID: "local-4", Status: "open", Type: "task", DependsOn: []string{"proj-x:remote-2", "proj-y:remote-3"}},
		{ID: "local-5", Status: "open", Type: "task", DependsOn: []string{"local-2", "proj-x:remote-1"}},
	}

	localGraph := BuildDepGraph(beads)

	crossGraph := &CrossProjectGraph{
		Projects: map[string]map[string]*Bead{
			"proj-x": {
				"remote-1": {ID: "remote-1", Status: "closed"},
				"remote-2": {ID: "remote-2", Status: "open"},
			},
			"proj-y": {
				"remote-3": {ID: "remote-3", Status: "closed"},
			},
		},
	}

	unblocked := FilterUnblockedCrossProject(beads, localGraph, crossGraph)

	// Expected unblocked:
	// - local-2: local dep closed
	// - local-3: local dep closed, cross dep closed
	// Not unblocked:
	// - local-1: closed status
	// - local-4: cross dep proj-x:remote-2 is open
	// - local-5: local dep local-2 is open
	expectedIDs := map[string]bool{
		"local-2": true,
		"local-3": true,
	}

	if len(unblocked) != len(expectedIDs) {
		t.Errorf("got %d unblocked beads, want %d", len(unblocked), len(expectedIDs))
		for _, b := range unblocked {
			t.Logf("  unblocked: %s", b.ID)
		}
	}

	for _, b := range unblocked {
		if !expectedIDs[b.ID] {
			t.Errorf("unexpected bead %q in unblocked list", b.ID)
		}
	}
}
