# Code Review Notes for cortex-a4s.9.2

## Summary
❌ **CHANGES REQUESTED** - Multiple critical issues need to be resolved

## Critical Issues

### 1. Missing Test Coverage
- **Issue**: No `sprint_test.go` file exists for the new functionality
- **Impact**: Data layer functions require comprehensive test coverage for reliability
- **Required**: Create `internal/store/sprint_test.go` with tests for all public functions
- **Test Cases Needed**:
  - `GetBacklogBeads` with various bead configurations
  - `GetSprintContext` with different date ranges
  - Edge cases: no beads, invalid directories, nil responses

### 2. Incorrect Dependency Graph Implementation  
- **Issue**: `buildDepGraphFromBeads()` returns `nil, nil` instead of building the graph
- **Impact**: Breaks dependency analysis completely
- **Fix**: Replace with:
  ```go
  func (s *Store) BuildDependencyGraph(beadList []*beads.Bead) (*beads.DepGraph, error) {
      // Convert []*beads.Bead to []beads.Bead for the API
      beads := make([]beads.Bead, len(beadList))
      for i, b := range beadList {
          beads[i] = *b
      }
      return beads.BuildDepGraph(beads), nil
  }
  ```

### 3. Performance Issues
- **Issue**: `GetSprintContext` calls `beads.ListBeadsCtx()` three times
- **Impact**: Inefficient with large repositories, could cause timeouts
- **Fix**: Load once and filter in memory:
  ```go
  allBeads, err := beads.ListBeadsCtx(ctx, beadsDir)
  // Then filter allBeads for each category
  ```

### 4. Incomplete Dependency Resolution
- **Issue**: `calculateReadinessStats` assumes any dependency makes a bead blocked
- **Impact**: Incorrectly marks beads as blocked even when dependencies are resolved
- **Fix**: Use proper dependency checking:
  ```go
  func (s *Store) isBeadReady(bead *beads.Bead, depGraph *beads.DepGraph) bool {
      for _, depID := range bead.DependsOn {
          dep, exists := depGraph.Nodes()[depID]
          if !exists || dep.Status != "closed" {
              return false
          }
      }
      return true
  }
  ```

## Additional Improvements

### 5. Input Validation
- Add validation for `beadsDir` parameter
- Validate `daysBack > 0` in `GetSprintContext`

### 6. Error Context
- Add more descriptive error messages with context
- Include bead IDs in error messages when enrichment fails

## Files to Update
- `internal/store/sprint.go` - Fix the issues above
- `internal/store/sprint_test.go` - Create comprehensive tests

## Next Steps
1. Fix the dependency graph implementation (critical)
2. Optimize the performance by reducing API calls
3. Add proper dependency resolution logic
4. Create comprehensive test coverage
5. Re-submit for review

## Testing Command
```bash
cd /home/ubuntu/projects/cortex && go test ./internal/store/sprint_test.go -v
```