package graph

import (
	"sort"
	"strings"
)

// DepGraph is a directed dependency graph built from Task.DependsOn edges.
type DepGraph struct {
	nodes   map[string]*Task
	forward map[string][]string // task -> depends on these
	reverse map[string][]string // task -> blocks these
}

// BuildDepGraph constructs a directed dependency graph from a slice of tasks.
// Tasks are copied into the graph to avoid aliasing the caller's slice.
func BuildDepGraph(tasks []Task) *DepGraph {
	g := &DepGraph{
		nodes:   make(map[string]*Task, len(tasks)),
		forward: make(map[string][]string),
		reverse: make(map[string][]string),
	}

	for i := range tasks {
		task := tasks[i]
		g.nodes[task.ID] = cloneTask(task)
	}

	for i := range tasks {
		id := tasks[i].ID
		deps := tasks[i].DependsOn
		if len(deps) == 0 {
			continue
		}
		g.forward[id] = append(g.forward[id], deps...)
		for _, depID := range deps {
			g.reverse[depID] = append(g.reverse[depID], id)
		}
	}

	return g
}

// Nodes returns the node map.
// Callers must not mutate the returned map or task pointers.
func (g *DepGraph) Nodes() map[string]*Task {
	if g == nil {
		return nil
	}
	return g.nodes
}

// DependsOnIDs returns a copy of the IDs this task depends on.
// Returns nil for unknown task IDs.
func (g *DepGraph) DependsOnIDs(id string) []string {
	if g == nil {
		return nil
	}
	s := g.forward[id]
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// BlocksIDs returns a copy of the task IDs blocked by this task.
// Returns nil for unknown task IDs.
func (g *DepGraph) BlocksIDs(id string) []string {
	if g == nil {
		return nil
	}
	s := g.reverse[id]
	if s == nil {
		return nil
	}
	out := make([]string, len(s))
	copy(out, s)
	return out
}

// FilterUnblockedOpen returns open, non-epic tasks whose dependencies all exist
// and are closed. Results are sorted by Priority ASC, stage-labeled tasks first,
// then EstimateMinutes ASC. Sort is stable.
func FilterUnblockedOpen(tasks []Task, graph *DepGraph) []Task {
	var result []Task

	for _, task := range tasks {
		if task.Status != "open" || task.Type == "epic" {
			continue
		}
		if isBlocked(task, graph) {
			continue
		}
		result = append(result, task)
	}

	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		iStage := hasStageLabel(result[i])
		jStage := hasStageLabel(result[j])
		if iStage != jStage {
			return iStage
		}
		if result[i].EstimateMinutes != result[j].EstimateMinutes {
			return result[i].EstimateMinutes < result[j].EstimateMinutes
		}
		return false
	})

	return result
}

func hasStageLabel(task Task) bool {
	for _, label := range task.Labels {
		if strings.HasPrefix(label, "stage:") {
			return true
		}
	}
	return false
}

func isBlocked(task Task, graph *DepGraph) bool {
	if graph == nil {
		return len(task.DependsOn) > 0
	}
	for _, depID := range task.DependsOn {
		dep, exists := graph.nodes[depID]
		if !exists || dep == nil || dep.Status != "closed" {
			return true
		}
	}
	return false
}

func cloneTask(task Task) *Task {
	cp := task
	if len(task.DependsOn) > 0 {
		cp.DependsOn = append([]string(nil), task.DependsOn...)
	}
	if len(task.Labels) > 0 {
		cp.Labels = append([]string(nil), task.Labels...)
	}
	return &cp
}
