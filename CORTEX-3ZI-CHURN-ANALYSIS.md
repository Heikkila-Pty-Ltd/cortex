# Cortex-3zi Churn Guard Analysis

## Issue Summary
Bead `cortex-46d.7` ("Align runtime behavior with dispatch routing and CLI config") exceeded churn threshold (8 dispatches in 1h0m0s) and was blocked from overnight dispatch.

## Root Cause Analysis

### Primary Issue: Hardcoded Backend Selection
**Location:** `cmd/cortex/main.go:72-79`

The main dispatcher selection logic completely ignores `dispatch.routing` configuration:

```go
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

**Config Being Ignored:**
```toml
[dispatch.routing]
fast_backend = "headless_cli"
balanced_backend = "tmux" 
premium_backend = "tmux"
comms_backend = "headless_cli"
retry_backend = "tmux"
```

This creates **config drift** where control-plane settings have no effect on runtime behavior.

### Secondary Issues: Command Construction Failures

**Observed Error Patterns** (from dispatch IDs 888, 887, 886, 885, 884, 881, 879, 878):
- `"error: unknown option '--model'"` (2 occurrences in 24h)
- `"Syntax error: Unterminated quoted string"`
- `"Syntax error: Bad fd number"`
- `"Syntax error: ( unexpected"`

**Root Cause:** String interpolation-based command construction fails when:
1. User prompts contain unescaped quotes, parentheses, or special chars
2. Model names or CLI flags aren't properly escaped
3. Complex prompt content breaks shell parsing

### Why the Task Was Churning

The original bead `cortex-46d.7` attempted to fix **all these issues simultaneously**:
1. Backend selection hardcoding
2. CLI config validation 
3. Command construction hardening
4. Structured observability
5. Comprehensive test coverage

This created a **monolithic task** that was too complex for reliable single-dispatch completion.

## Resolution Strategy

### Decomposed into 4 Executable Tasks:

1. **cortex-d2b** (P1): Fix hardcoded dispatcher selection in main.go
   - Replace `IsTmuxAvailable()` check with config-driven resolver
   - Implement per-tier backend selection
   - Clean, focused change with clear acceptance criteria

2. **cortex-129** (P1): Add dispatch CLI config validation at startup  
   - Validate provider->CLI bindings exist
   - Check required flags are present
   - Fail fast on misconfiguration

3. **cortex-tbt** (P2): Fix shell command construction to prevent parsing errors
   - Replace string interpolation with argv arrays
   - Use temp files for all user content
   - Cover all observed error patterns with tests

4. **cortex-50w** (P3): Add structured observability for dispatch failures
   - Emit events for config/runtime failures
   - Include debugging context
   - Integrate with health API

### Dependencies
```
cortex-d2b (P1) ← cortex-tbt (P2) ← cortex-50w (P3)
cortex-129 (P1) [parallel to d2b]
```

## Hardening Measures Added

### 1. Task Decomposition
- Each sub-task has **single responsibility**
- Clear acceptance criteria with **measurable outcomes**
- **Incremental delivery** with independent value

### 2. Failure Prevention
- **Config validation** catches mismatches at startup
- **Safe command construction** prevents runtime parsing errors  
- **Structured events** provide debugging visibility

### 3. Test Coverage
- Unit tests for all validation paths
- Integration tests for config-driven selection
- Regression tests covering observed failure patterns

### 4. Backward Compatibility
- Existing valid configs continue to work
- Graceful degradation when backends unavailable
- Clear error messages for operators

## Expected Outcomes

### Immediate (after cortex-d2b + cortex-129)
- Dispatch selection follows `dispatch.routing` config
- Startup fails fast on invalid CLI bindings
- No more silent config drift

### Short-term (after cortex-tbt)  
- Elimination of shell parsing errors
- Complex prompts execute reliably
- No more "unknown option --model" failures

### Long-term (after cortex-50w)
- Full observability of dispatch failures
- Structured debugging data available
- Proactive failure detection

## Prevention Measures

1. **Pre-decomposition**: Future complex infrastructure tasks should be broken down into subtasks during planning
2. **Config validation**: All control-plane settings should be validated at startup
3. **Safe construction**: All user content should be passed via temp files or argv arrays
4. **Incremental testing**: Each component should be testable in isolation

## Technical Debt Eliminated

- Hardcoded tmux availability overrides
- String-based command construction vulnerabilities  
- Silent configuration drift
- Lack of dispatch failure visibility
- Missing startup validation

The churn was caused by attempting to fix all this technical debt in a single task. The decomposed approach addresses each issue systematically while maintaining system stability.