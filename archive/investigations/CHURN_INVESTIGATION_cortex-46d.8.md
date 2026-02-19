# Churn Guard Investigation: cortex-46d.8

## Issue Summary
Bead `cortex-46d.8` exceeded churn threshold (8 dispatches in 1h0m0s) and was blocked from overnight dispatch.

## Root Cause Analysis
The churning was caused by inadequate error handling in the tmux dispatcher startup sequence:

1. **Missing error checks**: Tmux startup commands (`set`, `set-option`, `send-keys`) were not properly error-checked
2. **Masked failures**: Failed startup attempts would silently fail, causing the dispatch system to retry repeatedly
3. **Fragile cleanup**: Session cleanup relied on string parsing which was brittle for hyphenated agent names

## Resolution Implemented
The issue was resolved in commit `5baf5ee4d59ebe0cd89af8721c5b5016ac9d0aaa` with comprehensive hardening:

### 1. Startup Command Error Checking
- All tmux subcommands now check exit codes and propagate errors
- Failed startup triggers immediate session cleanup via `startupCleanup()`
- Descriptive error messages include captured tmux output

### 2. Explicit Session Metadata Tracking
- Added `metadata` map: `session_name -> agent_id`
- Replaced fragile string parsing with explicit metadata lookups
- Thread-safe access with mutex protection

### 3. Robust Cleanup Mechanisms
- `CleanupSession()` method uses metadata for reliable cleanup
- Fallback parsing with warnings when metadata missing
- Comprehensive resource cleanup for session directories

### 4. Test Coverage
- Unit tests for metadata tracking and hyphenated agent names
- Integration tests for startup failure scenarios
- Edge case handling for missing metadata

## Current Status
✅ **Resolved** - No further action required

The tmux dispatcher now properly handles:
- Command failures during session startup
- Hyphenated agent names (e.g., `cortex-coder`, `hg-website-reviewer`) 
- Session resource cleanup on failure
- Fallback behavior for edge cases

## Files Modified
- `internal/dispatch/tmux.go` - Core hardening implementation
- `internal/dispatch/tmux_test.go` - Comprehensive test coverage

## Prevention
The implemented hardening prevents future churning by:
1. Making startup failures explicit instead of silent
2. Ensuring proper cleanup of failed sessions
3. Using robust metadata instead of fragile string parsing
4. Providing comprehensive error reporting and logging