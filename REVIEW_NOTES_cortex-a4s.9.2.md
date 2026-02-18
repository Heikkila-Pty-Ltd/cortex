# Code Review Notes: cortex-a4s.9.2

## Review Date: 2026-02-18

## Issues Requiring Resolution

### 1. **CRITICAL: Incomplete Dependency Graph Implementation**
The `BuildDependencyGraph()` function returns `nil` and has no actual implementation.

**Problem:**
```go
func (s *Store) buildDepGraphFromBeads(beadList []*beads.Bead) (*beads.DepGraph, error) {
    // Since DepGraph fields are not exported, we'll work with what we can access
    // For now, return nil as the dependency analysis can be done with the bead data directly
    return nil, nil
}
```

**Required Fix:** Use the available `beads.BuildDepGraph()` function:
```go
func (s *Store) BuildDependencyGraph(beadList []*beads.Bead) (*beads.DepGraph, error) {
    beadSlice := make([]beads.Bead, len(beadList))
    for i, bead := range beadList {
        beadSlice[i] = *bead
    }
    return beads.BuildDepGraph(beadSlice), nil
}
```

### 2. **CRITICAL: Missing Test Coverage**
No test file exists for the new sprint planning functionality.

**Required:**
- Create `internal/store/sprint_test.go`
- Test all three main functions: GetBacklogBeads(), GetSprintContext(), BuildDependencyGraph()
- Include edge cases (no beads, circular dependencies, etc.)

### 3. **Major: Incorrect Dependency Logic**
`calculateReadinessStats()` assumes any dependency makes a bead blocked, which is wrong.

**Problem:**
```go
if len(bead.DependsOn) == 0 {
    readyCount++
} else {
    // Any dependency makes it blocked - WRONG!
    isReady = false
}
```

**Required Fix:** Check if dependencies are actually resolved/closed:
```go
func (s *Store) calculateReadinessStats(backlogBeads []*BacklogBead, depGraph *beads.DepGraph) (readyCount, blockedCount int) {
    for _, bead := range backlogBeads {
        if s.isBeadBlocked(bead, depGraph) {
            blockedCount++
            bead.IsBlocked = true
            bead.BlockingReasons = s.getBlockingReasons(bead, depGraph)
        } else {
            readyCount++
        }
    }
    return readyCount, blockedCount
}

func (s *Store) isBeadBlocked(bead *BacklogBead, graph *beads.DepGraph) bool {
    for _, depID := range bead.DependsOn {
        if dep, exists := graph.Nodes()[depID]; exists {
            if dep.Status != "closed" {
                return true
            }
        } else {
            return true // dependency doesn't exist
        }
    }
    return false
}
```

## Additional Issues

### 4. **Minor: Type Conversion Issues**
The `BuildDependencyGraph` function expects `[]*beads.Bead` but `beads.BuildDepGraph` expects `[]beads.Bead` (values, not pointers).

### 5. **Performance: Multiple Bead Iterations**  
The code calls `beads.ListBeadsCtx()` three times in separate functions. Consider loading once and filtering multiple times.

### 6. **Missing Error Context**
Some error handling could be more descriptive, especially in `enrichBacklogBead()`.

## Code Quality Notes

**Good:**
- Well-structured types and function signatures
- Proper error handling and context usage  
- Comprehensive metadata collection
- Clear separation of concerns

**Available APIs to Use:**
- `beads.BuildDepGraph([]beads.Bead)` - builds proper dependency graph
- `beads.FilterUnblockedOpen(beads []Bead, graph *DepGraph)` - filters ready beads
- DepGraph methods: `Nodes()`, `DependsOnIDs()`, `BlocksIDs()`

## Acceptance Criteria Status

- ✅ Query finds all beads with no stage label or stage:backlog
- ✅ Context includes current sprint work and recent completions  
- ❌ **Dependency information is accurately captured** (nil graph, incorrect logic)
- ✅ Metadata is complete for scrum master analysis
- ⚠️ Functions are efficient (mostly okay, room for improvement)

**Status: RETURNED TO CODING**

The core structure is solid, but dependency graph functionality must be implemented properly using the available beads package APIs before this can proceed to QA.