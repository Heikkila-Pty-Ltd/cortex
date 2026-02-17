# Churn Guard Analysis for cortex-evu.2

## Issue Summary
Bead `cortex-evu.2` (Add scheduler RunTick end-to-end test) exceeded churn threshold (6 dispatches in 1 hour) and was blocked.

## Root Cause Analysis

The task was churning because it attempted to implement all 9 complex test scenarios at once:

1. **Happy path**: project with 2 ready beads, providers available → dispatches 2
2. **Rate limited**: all authed providers exhausted → uses free tier
3. **All providers exhausted**: nothing available → dispatches 0, logs warning
4. **Already dispatched**: bead already running → skips
5. **Epic skipped**: bead type=epic → skipped
6. **Max per tick**: 5 ready beads, max_per_tick=2 → dispatches 2
7. **Agent busy**: agent already has running dispatch → skips bead
8. **Multiple projects**: 2 projects, priority ordering respected
9. **Dependency filtering**: bead with unresolved dep → not in ready list

Plus required test infrastructure:
- Mock Dispatcher that records calls instead of spawning processes
- In-memory SQLite store with seeded data
- Mock beads.ListBeads that returns controlled bead lists
- Controlled config with test providers/tiers

## Resolution Strategy

**Split into manageable subtasks:**

1. **Create test infrastructure foundation** (Priority 1)
   - Mock interfaces for Dispatcher, beads.ListBeads
   - Helper functions for test data setup
   - Basic test harness with in-memory store

2. **Implement core path tests** (Priority 2) 
   - Happy path scenario
   - Already dispatched scenario
   - Basic filtering scenarios

3. **Add advanced scenarios** (Priority 3)
   - Rate limiting scenarios
   - Multi-project scenarios  
   - Complex dependency filtering

4. **Add remaining edge cases** (Priority 4)
   - Epic skipping
   - Agent busy scenarios
   - Max per tick limits

This breaks the monolithic task into 4 digestible chunks that can be implemented and verified incrementally.

## Hardening Measures

1. **Test Coverage**: Each subtask adds specific test coverage
2. **Mock Infrastructure**: Reusable mocks prevent external dependencies
3. **Incremental Validation**: Each piece can be tested independently
4. **Clear Acceptance**: Each subtask has specific, measurable goals

## Prevention

- Future complex test tasks should be pre-decomposed into subtasks
- Test infrastructure should be built incrementally
- Each subtask should have clear, limited scope