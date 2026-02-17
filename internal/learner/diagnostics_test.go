package learner

import (
	"testing"
)

func TestDiagnoseFailure_TestFailure(t *testing.T) {
	output := `Running tests...
=== RUN   TestExample
--- FAIL: TestExample (0.00s)
    example_test.go:10: expected 42, got 0
FAIL
exit status 1
FAIL	github.com/example/pkg	0.001s`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}
	if diag.Category != "test_failure" {
		t.Errorf("expected category test_failure, got %s", diag.Category)
	}
	if diag.Summary == "" {
		t.Error("expected non-empty summary")
	}
	if diag.Details == "" {
		t.Error("expected non-empty details")
	}
}

func TestDiagnoseFailure_CompileError(t *testing.T) {
	output := `Building...
main.go:15:2: cannot find package "github.com/missing/pkg" in any of:
	/usr/local/go/src/github.com/missing/pkg (from $GOROOT)
	/home/user/go/src/github.com/missing/pkg (from $GOPATH)
error: compilation failed`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}
	if diag.Category != "compile_error" {
		t.Errorf("expected category compile_error, got %s", diag.Category)
	}
	if diag.Summary == "" {
		t.Error("expected non-empty summary")
	}
}

func TestDiagnoseFailure_UndefinedError(t *testing.T) {
	output := `Compiling...
./main.go:42:10: undefined: MissingFunction
error: build failed`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}
	if diag.Category != "compile_error" {
		t.Errorf("expected category compile_error, got %s", diag.Category)
	}
}

func TestDiagnoseFailure_PermissionDenied(t *testing.T) {
	output := `Starting service...
Error: permission denied while accessing /var/run/docker.sock
Failed to start container`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}
	if diag.Category != "permission_denied" {
		t.Errorf("expected category permission_denied, got %s", diag.Category)
	}
}

func TestDiagnoseFailure_RateLimited(t *testing.T) {
	output := `Making API request...
HTTP 429: Too Many Requests
Rate limit exceeded. Retry after 60 seconds.`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}
	if diag.Category != "rate_limited" {
		t.Errorf("expected category rate_limited, got %s", diag.Category)
	}
}

func TestDiagnoseFailure_RateLimitedByKeyword(t *testing.T) {
	output := `API call failed
Error: rate limit exceeded for this endpoint
Please wait before retrying`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}
	if diag.Category != "rate_limited" {
		t.Errorf("expected category rate_limited, got %s", diag.Category)
	}
}

func TestDiagnoseFailure_Timeout(t *testing.T) {
	output := `Executing long-running task...
Error: context deadline exceeded
Task failed to complete in time`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}
	if diag.Category != "timeout" {
		t.Errorf("expected category timeout, got %s", diag.Category)
	}
}

func TestDiagnoseFailure_ContextCanceled(t *testing.T) {
	output := `Processing request...
context canceled
Operation aborted`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}
	if diag.Category != "timeout" {
		t.Errorf("expected category timeout, got %s", diag.Category)
	}
}

func TestDiagnoseFailure_UnknownError(t *testing.T) {
	output := `Running command...
error: something went wrong
Command exited with code 1`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}
	if diag.Category != "unknown" {
		t.Errorf("expected category unknown, got %s", diag.Category)
	}
}

func TestDiagnoseFailure_NoFailure(t *testing.T) {
	output := `Starting build...
Build completed successfully
All tests passed
Done!`

	diag := DiagnoseFailure(output)
	if diag != nil {
		t.Errorf("expected nil diagnosis for success output, got %+v", diag)
	}
}

func TestDiagnoseFailure_EmptyOutput(t *testing.T) {
	diag := DiagnoseFailure("")
	if diag != nil {
		t.Errorf("expected nil diagnosis for empty output, got %+v", diag)
	}
}

func TestDiagnoseFailure_ContextExtraction(t *testing.T) {
	output := `line 1
line 2
line 3 with error: something failed
line 4
line 5
line 6`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}

	// Summary should be the error line
	if !contains(diag.Summary, "error:") {
		t.Errorf("summary should contain error line, got: %s", diag.Summary)
	}

	// Details should contain surrounding lines (up to 5 lines total)
	if !contains(diag.Details, "line 1") || !contains(diag.Details, "line 5") {
		t.Errorf("details should contain context, got: %s", diag.Details)
	}
}

func TestDiagnoseFailure_PriorityOrder(t *testing.T) {
	// Test that more specific patterns are matched before generic ones
	output := `Test suite starting...
--- FAIL: TestSomething (0.00s)
error: test failed`

	diag := DiagnoseFailure(output)
	if diag == nil {
		t.Fatal("expected diagnosis, got nil")
	}

	// Should match test_failure (higher priority) not unknown (lower priority)
	if diag.Category != "test_failure" {
		t.Errorf("expected test_failure to take priority over generic error, got %s", diag.Category)
	}
}

func TestDiagnoseFailure_CaseVariations(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		category string
	}{
		{
			name:     "uppercase Error",
			output:   "Error: failed to connect",
			category: "unknown",
		},
		{
			name:     "lowercase error",
			output:   "error: failed to connect",
			category: "unknown",
		},
		{
			name:     "uppercase Permission",
			output:   "Permission denied",
			category: "permission_denied",
		},
		{
			name:     "lowercase permission",
			output:   "permission denied",
			category: "permission_denied",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diag := DiagnoseFailure(tt.output)
			if diag == nil {
				t.Fatal("expected diagnosis, got nil")
			}
			if diag.Category != tt.category {
				t.Errorf("expected category %s, got %s", tt.category, diag.Category)
			}
		})
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) > len(substr) && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
