package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

// BeadDependency represents a dependency relationship from bd list --json.
type BeadDependency struct {
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
}

// Bead represents a single work item tracked by the bd CLI.
type Bead struct {
	ID              string           `json:"id"`
	Title           string           `json:"title"`
	Description     string           `json:"description"`
	Status          string           `json:"status"`
	Priority        int              `json:"priority"`
	Type            string           `json:"issue_type"`
	Labels          []string         `json:"labels"`
	EstimateMinutes int              `json:"estimate_minutes"`
	ParentID        string           `json:"parent_id"`
	DependsOn       []string         `json:"depends_on"`
	Dependencies    []BeadDependency `json:"dependencies"`
	Acceptance      string           `json:"acceptance"`
	Design          string           `json:"design"`
	CreatedAt       time.Time        `json:"created_at"`
}

// BeadDetail holds the full output of bd show --json.
type BeadDetail struct {
	Bead
}

// DepGraph is a directed dependency graph built from Bead.DependsOn edges.
type DepGraph struct {
	nodes   map[string]*Bead
	edges   map[string][]string // bead -> depends on these
	reverse map[string][]string // bead -> blocks these
}

// Nodes returns the nodes map.
func (g *DepGraph) Nodes() map[string]*Bead { return g.nodes }

// DependsOnIDs returns the IDs that the given bead depends on.
func (g *DepGraph) DependsOnIDs(beadID string) []string { return g.edges[beadID] }

// BlocksIDs returns the IDs that the given bead blocks.
func (g *DepGraph) BlocksIDs(beadID string) []string { return g.reverse[beadID] }

func projectRoot(beadsDir string) string {
	return filepath.Dir(beadsDir)
}

func runBD(ctx context.Context, projectDir string, args ...string) ([]byte, error) {
	path, err := exec.LookPath("bd")
	if err != nil {
		return nil, fmt.Errorf("bd CLI not found in PATH: %w", err)
	}

	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = projectDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("bd %v failed: %w\nstderr: %s", args, err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// ListBeads runs bd list --json --quiet in the project root and returns parsed beads.
func ListBeads(beadsDir string) ([]Bead, error) {
	return ListBeadsCtx(context.Background(), beadsDir)
}

// ListBeadsCtx is the context-aware version of ListBeads.
func ListBeadsCtx(ctx context.Context, beadsDir string) ([]Bead, error) {
	root := projectRoot(beadsDir)

	out, err := runBD(ctx, root, "list", "--json", "--quiet")
	if err != nil {
		out, err = runBD(ctx, root, "list", "--format=json")
		if err != nil {
			return nil, fmt.Errorf("listing beads: %w", err)
		}
	}

	var beads []Bead
	if err := json.Unmarshal(out, &beads); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}
	resolveDependencies(beads)
	return beads, nil
}

// ShowBead runs bd show --json {beadID} and returns the detail.
func ShowBead(beadsDir, beadID string) (*BeadDetail, error) {
	return ShowBeadCtx(context.Background(), beadsDir, beadID)
}

// ShowBeadCtx is the context-aware version of ShowBead.
func ShowBeadCtx(ctx context.Context, beadsDir, beadID string) (*BeadDetail, error) {
	root := projectRoot(beadsDir)
	out, err := runBD(ctx, root, "show", "--json", beadID)
	if err != nil {
		return nil, fmt.Errorf("showing bead %s: %w", beadID, err)
	}

	var detail BeadDetail
	if err := json.Unmarshal(out, &detail); err != nil {
		return nil, fmt.Errorf("parsing bd show output for %s: %w", beadID, err)
	}
	return &detail, nil
}

// CloseBead runs bd close {beadID} in the project root.
func CloseBead(beadsDir, beadID string) error {
	return CloseBeadCtx(context.Background(), beadsDir, beadID)
}

// CloseBeadCtx is the context-aware version of CloseBead.
func CloseBeadCtx(ctx context.Context, beadsDir, beadID string) error {
	root := projectRoot(beadsDir)
	_, err := runBD(ctx, root, "close", beadID)
	if err != nil {
		return fmt.Errorf("closing bead %s: %w", beadID, err)
	}
	return nil
}

// BuildDepGraph constructs a directed dependency graph from a slice of beads.
func BuildDepGraph(beads []Bead) *DepGraph {
	g := &DepGraph{
		nodes:   make(map[string]*Bead, len(beads)),
		edges:   make(map[string][]string),
		reverse: make(map[string][]string),
	}

	for i := range beads {
		g.nodes[beads[i].ID] = &beads[i]
	}

	for i := range beads {
		b := &beads[i]
		if len(b.DependsOn) == 0 {
			continue
		}
		g.edges[b.ID] = append(g.edges[b.ID], b.DependsOn...)
		for _, depID := range b.DependsOn {
			g.reverse[depID] = append(g.reverse[depID], b.ID)
		}
	}

	return g
}

// FilterUnblockedOpen returns open, non-epic beads whose dependencies are all closed.
// Sorted by Priority ASC then EstimateMinutes ASC.
func FilterUnblockedOpen(beads []Bead, graph *DepGraph) []Bead {
	var result []Bead

	for _, b := range beads {
		if b.Status != "open" {
			continue
		}
		if b.Type == "epic" {
			continue
		}
		if isBlocked(b, graph) {
			continue
		}
		result = append(result, b)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		// Stage-labeled beads get dispatched before non-stage beads
		iStage := hasStageLabel(result[i])
		jStage := hasStageLabel(result[j])
		if iStage != jStage {
			return iStage
		}
		return result[i].EstimateMinutes < result[j].EstimateMinutes
	})

	return result
}

// resolveDependencies populates DependsOn from the Dependencies array
// returned by bd list --json. Only "blocks" type dependencies are treated
// as blocking; "parent-child" is informational.
func resolveDependencies(beads []Bead) {
	for i := range beads {
		if len(beads[i].DependsOn) > 0 {
			continue // already populated (e.g. from a flat depends_on field)
		}
		for _, dep := range beads[i].Dependencies {
			if dep.Type == "blocks" {
				beads[i].DependsOn = append(beads[i].DependsOn, dep.DependsOnID)
			}
		}
	}
}

func hasStageLabel(b Bead) bool {
	for _, label := range b.Labels {
		if len(label) > 6 && label[:6] == "stage:" {
			return true
		}
	}
	return false
}

func isBlocked(b Bead, graph *DepGraph) bool {
	for _, depID := range b.DependsOn {
		dep, exists := graph.nodes[depID]
		if !exists {
			return true
		}
		if dep.Status != "closed" {
			return true
		}
	}
	return false
}
