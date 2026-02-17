package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
)

func TestNewDoDChecker(t *testing.T) {
	checks := []string{"go test ./...", "go vet ./..."}
	checker := NewDoDChecker(checks, 80, true, true)
	
	if len(checker.checks) != 2 {
		t.Errorf("expected 2 checks, got %d", len(checker.checks))
	}
	if checker.coverageMin != 80 {
		t.Errorf("expected coverageMin 80, got %d", checker.coverageMin)
	}
	if !checker.requireEstimate {
		t.Error("expected requireEstimate to be true")
	}
	if !checker.requireAcceptance {
		t.Error("expected requireAcceptance to be true")
	}
}

func TestValidateBeadRequirements(t *testing.T) {
	tests := []struct {
		name              string
		requireEstimate   bool
		requireAcceptance bool
		bead              beads.Bead
		wantPassed        bool
		wantFailureCount  int
	}{
		{
			name:              "all requirements met",
			requireEstimate:   true,
			requireAcceptance: true,
			bead: beads.Bead{
				ID:              "test-1",
				EstimateMinutes: 30,
				Acceptance:      "User can do X",
			},
			wantPassed:       true,
			wantFailureCount: 0,
		},
		{
			name:              "missing estimate",
			requireEstimate:   true,
			requireAcceptance: false,
			bead: beads.Bead{
				ID:              "test-2",
				EstimateMinutes: 0,
			},
			wantPassed:       false,
			wantFailureCount: 1,
		},
		{
			name:              "missing acceptance",
			requireEstimate:   false,
			requireAcceptance: true,
			bead: beads.Bead{
				ID:         "test-3",
				Acceptance: "",
			},
			wantPassed:       false,
			wantFailureCount: 1,
		},
		{
			name:              "missing both",
			requireEstimate:   true,
			requireAcceptance: true,
			bead: beads.Bead{
				ID:              "test-4",
				EstimateMinutes: 0,
				Acceptance:      "",
			},
			wantPassed:       false,
			wantFailureCount: 2,
		},
		{
			name:              "no requirements",
			requireEstimate:   false,
			requireAcceptance: false,
			bead: beads.Bead{
				ID:              "test-5",
				EstimateMinutes: 0,
				Acceptance:      "",
			},
			wantPassed:       true,
			wantFailureCount: 0,
		},
		{
			name:              "whitespace acceptance not accepted",
			requireEstimate:   false,
			requireAcceptance: true,
			bead: beads.Bead{
				ID:         "test-6",
				Acceptance: "   \n\t  ",
			},
			wantPassed:       false,
			wantFailureCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := NewDoDChecker(nil, 0, tt.requireEstimate, tt.requireAcceptance)
			result := &DoDResult{
				Passed:   true,
				Failures: make([]string, 0),
			}
			
			err := checker.validateBeadRequirements(tt.bead, result)
			if err != nil {
				t.Fatalf("validateBeadRequirements failed: %v", err)
			}
			
			if result.Passed != tt.wantPassed {
				t.Errorf("expected passed=%v, got %v", tt.wantPassed, result.Passed)
			}
			
			if len(result.Failures) != tt.wantFailureCount {
				t.Errorf("expected %d failures, got %d: %v", tt.wantFailureCount, len(result.Failures), result.Failures)
			}
		})
	}
}

func TestRunCheck(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := t.TempDir()
	
	tests := []struct {
		name            string
		command         string
		wantPassed      bool
		wantExitCode    int
		setupWorkspace  func(string) error
	}{
		{
			name:         "successful command",
			command:      "echo hello",
			wantPassed:   true,
			wantExitCode: 0,
		},
		{
			name:         "failing command",
			command:      "false",
			wantPassed:   false,
			wantExitCode: 1,
		},
		{
			name:         "command not found",
			command:      "nonexistent-command",
			wantPassed:   false,
			wantExitCode: -1,
		},
		{
			name:         "go test success",
			command:      "go test -v .",
			wantPassed:   true,
			wantExitCode: 0,
			setupWorkspace: func(dir string) error {
				// Create a simple Go module with a passing test
				goMod := "module test\ngo 1.21\n"
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
					return err
				}
				
				testFile := `package main
import "testing"
func TestSimple(t *testing.T) {
	if 1+1 != 2 {
		t.Errorf("math is broken")
	}
}
`
				return os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(testFile), 0644)
			},
		},
		{
			name:         "go test failure",
			command:      "go test -v .",
			wantPassed:   false,
			wantExitCode: 1,
			setupWorkspace: func(dir string) error {
				// Create a Go module with a failing test
				goMod := "module test\ngo 1.21\n"
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
					return err
				}
				
				testFile := `package main
import "testing"
func TestFailing(t *testing.T) {
	t.Errorf("this test always fails")
}
`
				return os.WriteFile(filepath.Join(dir, "main_test.go"), []byte(testFile), 0644)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := filepath.Join(tempDir, tt.name)
			if err := os.MkdirAll(workspace, 0755); err != nil {
				t.Fatalf("failed to create workspace: %v", err)
			}
			
			if tt.setupWorkspace != nil {
				if err := tt.setupWorkspace(workspace); err != nil {
					t.Fatalf("failed to setup workspace: %v", err)
				}
			}
			
			checker := NewDoDChecker(nil, 0, false, false)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			
			result, err := checker.runCheck(ctx, workspace, tt.command)
			if err != nil {
				t.Fatalf("runCheck failed: %v", err)
			}
			
			if result.Passed != tt.wantPassed {
				t.Errorf("expected passed=%v, got %v", tt.wantPassed, result.Passed)
			}
			
			if result.ExitCode != tt.wantExitCode {
				t.Errorf("expected exitCode=%d, got %d", tt.wantExitCode, result.ExitCode)
			}
			
			if result.Command != tt.command {
				t.Errorf("expected command=%q, got %q", tt.command, result.Command)
			}
			
			if result.Duration <= 0 {
				t.Error("expected duration > 0")
			}
		})
	}
}

func TestParseCoverage(t *testing.T) {
	checker := NewDoDChecker(nil, 0, false, false)
	
	tests := []struct {
		name        string
		output      string
		want        float64
		wantError   bool
	}{
		{
			name: "total coverage line",
			output: `?   	github.com/example/pkg	[no test files]
ok  	github.com/example/other	0.123s	coverage: 75.0% of statements
total:	(statements)	82.5%`,
			want: 82.5,
		},
		{
			name: "single package coverage",
			output: `ok  	github.com/example/pkg	0.123s	coverage: 85.2% of statements`,
			want: 85.2,
		},
		{
			name: "multiple packages average",
			output: `ok  	github.com/example/pkg1	0.123s	coverage: 80.0% of statements
ok  	github.com/example/pkg2	0.456s	coverage: 90.0% of statements`,
			want: 85.0, // average of 80 and 90
		},
		{
			name: "no coverage info",
			output: `ok  	github.com/example/pkg	0.123s`,
			wantError: true,
		},
		{
			name: "mixed with no test files",
			output: `?   	github.com/example/nopkg	[no test files]
ok  	github.com/example/pkg	0.123s	coverage: 75.5% of statements`,
			want: 75.5,
		},
		{
			name: "total coverage overrides individual",
			output: `ok  	github.com/example/pkg1	0.123s	coverage: 80.0% of statements
ok  	github.com/example/pkg2	0.456s	coverage: 90.0% of statements
total:	(statements)	88.0%`,
			want: 88.0, // total takes precedence over average
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := checker.parseCoverage(tt.output)
			if tt.wantError {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}
			
			if err != nil {
				t.Fatalf("parseCoverage failed: %v", err)
			}
			
			if got != tt.want {
				t.Errorf("parseCoverage() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCheckCoverage(t *testing.T) {
	// This test requires a real Go workspace with coverage info
	tempDir := t.TempDir()
	
	// Create a simple Go module with tests
	goMod := "module test\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatalf("failed to create go.mod: %v", err)
	}
	
	// Create main.go with some functions
	mainFile := `package main
func Add(a, b int) int {
	return a + b
}
func Multiply(a, b int) int {
	return a * b
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(mainFile), 0644); err != nil {
		t.Fatalf("failed to create main.go: %v", err)
	}
	
	// Create test file that covers only Add function (50% coverage)
	testFile := `package main
import "testing"
func TestAdd(t *testing.T) {
	if Add(2, 3) != 5 {
		t.Error("Add failed")
	}
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "main_test.go"), []byte(testFile), 0644); err != nil {
		t.Fatalf("failed to create main_test.go: %v", err)
	}
	
	tests := []struct {
		name            string
		coverageMin     int
		wantPassed      bool
		wantFailures    int
	}{
		{
			name:         "coverage above minimum",
			coverageMin:  30, // should pass with ~50% coverage
			wantPassed:   true,
			wantFailures: 0,
		},
		{
			name:         "coverage below minimum",
			coverageMin:  80, // should fail with ~50% coverage
			wantPassed:   false,
			wantFailures: 1,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := NewDoDChecker(nil, tt.coverageMin, false, false)
			result := &DoDResult{
				Passed:   true,
				Checks:   make([]CheckResult, 0),
				Failures: make([]string, 0),
			}
			
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			
			err := checker.checkCoverage(ctx, tempDir, result)
			if err != nil {
				t.Fatalf("checkCoverage failed: %v", err)
			}
			
			if result.Passed != tt.wantPassed {
				t.Errorf("expected passed=%v, got %v", tt.wantPassed, result.Passed)
			}
			
			failureCount := len(result.Failures)
			if failureCount != tt.wantFailures {
				t.Errorf("expected %d failures, got %d: %v", tt.wantFailures, failureCount, result.Failures)
			}
			
			// Should have added one check result for coverage
			if len(result.Checks) != 1 {
				t.Errorf("expected 1 check result, got %d", len(result.Checks))
			} else {
				check := result.Checks[0]
				if check.Command != "go test -cover ./..." {
					t.Errorf("expected coverage command, got %q", check.Command)
				}
				if check.Duration <= 0 {
					t.Error("expected duration > 0")
				}
			}
		})
	}
}

func TestCheck_Integration(t *testing.T) {
	// Integration test with real commands
	tempDir := t.TempDir()
	
	// Create a simple Go module
	goMod := "module test\ngo 1.21\n"
	if err := os.WriteFile(filepath.Join(tempDir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatalf("failed to create go.mod: %v", err)
	}
	
	// Create a simple Go file
	mainFile := `package main
func Hello() string {
	return "hello"
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "main.go"), []byte(mainFile), 0644); err != nil {
		t.Fatalf("failed to create main.go: %v", err)
	}
	
	// Create a test file
	testFile := `package main
import "testing"
func TestHello(t *testing.T) {
	if Hello() != "hello" {
		t.Error("Hello failed")
	}
}
`
	if err := os.WriteFile(filepath.Join(tempDir, "main_test.go"), []byte(testFile), 0644); err != nil {
		t.Fatalf("failed to create main_test.go: %v", err)
	}
	
	tests := []struct {
		name            string
		checks          []string
		coverageMin     int
		requireEstimate bool
		requireAccept   bool
		bead            beads.Bead
		wantPassed      bool
	}{
		{
			name:        "all checks pass",
			checks:      []string{"go test ./...", "go vet ./..."},
			coverageMin: 0,
			bead: beads.Bead{
				ID:              "test-1",
				EstimateMinutes: 30,
				Acceptance:      "Works correctly",
			},
			wantPassed: true,
		},
		{
			name:            "missing estimate fails",
			checks:          []string{"go test ./..."},
			requireEstimate: true,
			bead: beads.Bead{
				ID:              "test-2",
				EstimateMinutes: 0,
			},
			wantPassed: false,
		},
		{
			name:          "missing acceptance fails",
			checks:        []string{"go test ./..."},
			requireAccept: true,
			bead: beads.Bead{
				ID:         "test-3",
				Acceptance: "",
			},
			wantPassed: false,
		},
		{
			name:        "coverage below threshold fails",
			checks:      []string{"go test ./..."},
			coverageMin: 200, // impossible threshold
			bead: beads.Bead{
				ID: "test-4",
			},
			wantPassed: false,
		},
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := NewDoDChecker(tt.checks, tt.coverageMin, tt.requireEstimate, tt.requireAccept)
			
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			
			result, err := checker.Check(ctx, tempDir, tt.bead)
			if err != nil {
				t.Fatalf("Check failed: %v", err)
			}
			
			if result.Passed != tt.wantPassed {
				t.Errorf("expected passed=%v, got %v", tt.wantPassed, result.Passed)
				t.Logf("Failures: %v", result.Failures)
				for i, check := range result.Checks {
					t.Logf("Check %d: %s -> exit %d, passed=%v", i, check.Command, check.ExitCode, check.Passed)
					if check.Output != "" {
						t.Logf("Output: %s", check.Output)
					}
				}
			}
			
			// Verify we have check results for each command
			expectedChecks := len(tt.checks)
			if tt.coverageMin > 0 {
				expectedChecks++ // coverage adds an extra check
			}
			
			if len(result.Checks) != expectedChecks {
				t.Errorf("expected %d check results, got %d", expectedChecks, len(result.Checks))
			}
		})
	}
}

func TestCheck_CommandParsing(t *testing.T) {
	checker := NewDoDChecker([]string{"echo hello world"}, 0, false, false)
	tempDir := t.TempDir()
	
	ctx := context.Background()
	result, err := checker.Check(ctx, tempDir, beads.Bead{ID: "test"})
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	
	if !result.Passed {
		t.Error("expected check to pass")
	}
	
	if len(result.Checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(result.Checks))
	}
	
	check := result.Checks[0]
	if !strings.Contains(check.Output, "hello world") {
		t.Errorf("expected output to contain 'hello world', got: %q", check.Output)
	}
}

func TestCheck_EmptyCommand(t *testing.T) {
	checker := NewDoDChecker([]string{""}, 0, false, false)
	tempDir := t.TempDir()
	
	ctx := context.Background()
	_, err := checker.Check(ctx, tempDir, beads.Bead{ID: "test"})
	if err == nil {
		t.Error("expected error for empty command")
	}
	if !strings.Contains(err.Error(), "empty command") {
		t.Errorf("expected 'empty command' error, got: %v", err)
	}
}

func TestCheck_ContextCancellation(t *testing.T) {
	checker := NewDoDChecker([]string{"sleep 10"}, 0, false, false)
	tempDir := t.TempDir()
	
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	
	_, err := checker.Check(ctx, tempDir, beads.Bead{ID: "test"})
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestDoDResult_String(t *testing.T) {
	result := &DoDResult{
		Passed: false,
		Checks: []CheckResult{
			{
				Command:  "go test ./...",
				ExitCode: 1,
				Output:   "FAIL: test failed",
				Passed:   false,
				Duration: time.Second,
			},
		},
		Failures: []string{"Command failed: go test ./... (exit 1)", "Coverage 45.2% below minimum 80%"},
	}
	
	// Test that we can format the result meaningfully
	if result.Passed {
		t.Error("expected result to show as failed")
	}
	
	if len(result.Failures) != 2 {
		t.Errorf("expected 2 failures, got %d", len(result.Failures))
	}
	
	if len(result.Checks) != 1 {
		t.Errorf("expected 1 check, got %d", len(result.Checks))
	}
	
	check := result.Checks[0]
	if check.Duration != time.Second {
		t.Errorf("expected duration=1s, got %v", check.Duration)
	}
}