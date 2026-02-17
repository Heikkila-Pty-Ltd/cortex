# Churn Analysis: cortex-46d.7 "Align runtime behavior with dispatch routing and CLI config"

## Root Cause

The bead `cortex-46d.7` has been churning (8 dispatches in 1 hour) because it attempts to solve multiple complex, interdependent problems in a single task:

### 1. **Overly Broad Scope**

The task tries to fix 4 major areas simultaneously:
- Backend selection logic (currently hardcoded to tmux availability) 
- CLI configuration validation at startup
- Command construction hardening (preventing shell parse failures)
- Structured error event emission

### 2. **High Complexity & Risk**

The changes require touching critical code paths:
- `cmd/cortex/main.go` - Application startup and dispatcher selection
- `internal/config/config.go` - Configuration loading and validation  
- `internal/scheduler/scheduler.go` - Core dispatch routing logic
- `internal/dispatch/*` - Command building and execution

Any bugs in these changes could break the entire dispatch system.

### 3. **Interdependent Changes**

Each area depends on others:
- Backend selection needs config validation
- Config validation needs error handling  
- Command hardening affects both dispatchers
- All changes need coordinated testing

## Evidence from Code

### Current Hardcoded Dispatcher Selection
```go
// cmd/cortex/main.go lines 81-89
// Choose dispatcher based on tmux availability
var d dispatch.DispatcherInterface
if dispatch.IsTmuxAvailable() {
    logger.Info("tmux available, using TmuxDispatcher")
    d = dispatch.NewTmuxDispatcher()
} else {
    logger.Info("tmux not available, using PID-based Dispatcher")  
    d = dispatch.NewDispatcher()
}
```

The configured `dispatch.routing.*_backend` settings are completely ignored.

### Observed Failures
From the bead notes:
- `error: unknown option '--model'` (CLI flag issues)
- Shell parse failures: `"Syntax error: Unterminated quoted string"`
- Failed dispatch IDs: 888, 887, 886, 885, 884, 881, 879, 878

These indicate command construction problems in the dispatch system.

## Solution: Task Decomposition

Break cortex-46d.7 into 4 independent, focused tasks:

### Task 1: Config Validation at Startup  
**Goal**: Validate CLI bindings exist and are properly configured
- Add validation in `config.Load()` for provider->CLI mappings
- Ensure required CLI tools are available
- Fast startup failure for misconfigurations
- **Low risk**: Only affects startup, easy to test

### Task 2: Implement Config-Driven Backend Selection
**Goal**: Make `dispatch.routing.*_backend` actually control dispatcher choice
- Replace hardcoded tmux check with config lookup
- Add validation for backend names  
- Preserve fallback behavior for compatibility
- **Medium risk**: Changes startup flow but clear behavior

### Task 3: Harden Command Construction
**Goal**: Fix shell parsing failures in dispatch commands
- Audit command building in both dispatchers
- Use proper shell escaping for user content
- Add integration tests for complex prompts
- **High value**: Directly fixes observed failures

### Task 4: Structured Error Events  
**Goal**: Emit diagnostic events for dispatch failures
- Add event emission for config/backend/command failures
- Include structured data (bead, provider, backend, reason)
- Integrate with existing health event system  
- **Low risk**: Pure addition, doesn't change core behavior

## Benefits of Decomposition

1. **Reduced Risk**: Each task can be implemented and tested independently
2. **Faster Progress**: Smaller tasks are less likely to fail/churn  
3. **Better Testing**: Focused scope enables thorough test coverage
4. **Incremental Value**: Each completed task provides immediate value
5. **Easier Review**: Smaller changes are easier to understand and approve

## Recommendation

1. **Close** cortex-46d.7 as "split into smaller tasks"
2. **Create** 4 new focused beads as outlined above  
3. **Prioritize** Task 3 (command hardening) as it directly addresses observed failures
4. **Sequence** implementation: Config validation → Backend selection → Command hardening → Error events