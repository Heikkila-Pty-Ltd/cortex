package scheduler

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/git"
	"github.com/antigravity-dev/cortex/internal/store"
)

type mergeFlowState struct {
	mergeCalls  int
	revertCalls int
	revertSHA   string
}

func writeFakeBDScript(t *testing.T, logPath string) string {
	t.Helper()
	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "bd")
	script := "#!/bin/sh\necho \"$*\" >> " + strconv.Quote(logPath) + "\n"
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd script: %v", err)
	}
	return scriptDir
}

func readLogLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read log file: %v", err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func setupApprovedMergeFixture(t *testing.T, withFailure bool) (*Scheduler, *mergeFlowState, string, string) {
	t.Helper()
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}

	lister := NewMockBeadsLister()
	project := config.Project{
		Enabled:             true,
		Priority:            1,
		Workspace:           workspace,
		BeadsDir:            beadsDir,
		UseBranches:         true,
		MergeMethod:         "",
		PostMergeChecks:     []string{"go test ./..."},
		AutoRevertOnFailure: true,
	}
	cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
	logBuf := &bytes.Buffer{}
	sched, st, _ := newRunTickScenarioScheduler(t, cfg, lister, logBuf)
	sched.cfg.Projects["test-project"] = project

	bdLogPath := filepath.Join(workspace, "bd.log")
	bdDir := writeFakeBDScript(t, bdLogPath)
	t.Setenv("PATH", bdDir+":"+os.Getenv("PATH"))

	state := &mergeFlowState{}
	sched.mergePR = func(string, int, string) error {
		state.mergeCalls++
		return nil
	}
	sched.revertMerge = func(_ string, sha string) error {
		state.revertCalls++
		state.revertSHA = sha
		return nil
	}
	sched.latestCommitSHA = func(string) (string, error) { return "abc123", nil }
	sched.getPRStatus = func(string, string) (*git.PRStatus, error) {
		return &git.PRStatus{State: "open", ReviewDecision: "APPROVED"}, nil
	}
	sched.runPostMergeChecks = func(_ string, _ []string) (*git.DoDResult, error) {
		if withFailure {
			return &git.DoDResult{
				Passed:   false,
				Checks:   []git.CheckResult{{Command: "go test ./...", ExitCode: 1, Passed: false}},
				Failures: []string{"failed"},
			}, nil
		}
		return &git.DoDResult{Passed: true, Checks: []git.CheckResult{{Command: "go test ./...", ExitCode: 0, Passed: true}}}, nil
	}
	sched.listBeads = func(string) ([]beads.Bead, error) {
		bead := createTestBead("abc-1", "Approved PR merge", "task", "open", 1)
		bead.Labels = []string{"stage:review"}
		return []beads.Bead{bead}, nil
	}

	seedDispatchAndStatus(t, st, "abc-1")
	sched.processApprovedPRMerges(context.Background())

	return sched, state, bdLogPath, "abc-1"
}

func seedDispatchAndStatus(t *testing.T, st *store.Store, beadID string) {
	t.Helper()
	dispatchID, err := st.RecordDispatch(beadID, "test-project", "test-project-reviewer", "authed-model", "balanced", 123, "", "prompt", "", "feat/"+beadID, "mock")
	if err != nil {
		t.Fatalf("seed dispatch: %v", err)
	}
	if err := st.UpdateDispatchPR(dispatchID, "https://example.com/p/1", 42); err != nil {
		t.Fatalf("seed dispatch pr: %v", err)
	}
}

func TestProcessApprovedPRMerges_ClosesBeadWhenChecksPass(t *testing.T) {
	_, state, bdLogPath, beadID := setupApprovedMergeFixture(t, false)
	if state.mergeCalls != 1 {
		t.Fatalf("merge calls = %d, want 1", state.mergeCalls)
	}
	if state.revertCalls != 0 {
		t.Fatalf("revert calls = %d, want 0", state.revertCalls)
	}
	commands := readLogLines(t, bdLogPath)
	if len(commands) == 0 {
		t.Fatalf("expected bd command logs, got none")
	}
	foundClose := false
	foundUpdate := false
	for _, cmd := range commands {
		if strings.HasPrefix(cmd, "close "+beadID) {
			foundClose = true
		}
		if strings.Contains(cmd, "update "+beadID) && strings.Contains(cmd, "stage:coding") {
			foundUpdate = true
		}
	}
	if !foundClose {
		t.Fatalf("expected close command for bead %s, got %v", beadID, commands)
	}
	if foundUpdate {
		t.Fatalf("did not expect transition-to-coding command after passing checks: %v", commands)
	}
}

func TestProcessApprovedPRMerges_RevertsAndReopensWhenChecksFail(t *testing.T) {
	_, state, bdLogPath, beadID := setupApprovedMergeFixture(t, true)
	if state.mergeCalls != 1 {
		t.Fatalf("merge calls = %d, want 1", state.mergeCalls)
	}
	if state.revertCalls != 1 {
		t.Fatalf("revert calls = %d, want 1", state.revertCalls)
	}
	if state.revertSHA != "abc123" {
		t.Fatalf("revert called with commit %q, want abc123", state.revertSHA)
	}

	commands := readLogLines(t, bdLogPath)
	if len(commands) == 0 {
		t.Fatalf("expected bd command logs, got none")
	}
	foundUpdate := false
	foundClose := false
	for _, cmd := range commands {
		if strings.Contains(cmd, "update "+beadID) && strings.Contains(cmd, "stage:coding") {
			foundUpdate = true
		}
		if strings.HasPrefix(cmd, "close "+beadID) {
			foundClose = true
		}
	}
	if !foundUpdate {
		t.Fatalf("expected stage:coding update for failed checks, got %v", commands)
	}
	if foundClose {
		t.Fatalf("did not expect close command when checks fail, got %v", commands)
	}
}

func TestProcessApprovedPRMerges_NoMergeWithoutApproval(t *testing.T) {
	lister := NewMockBeadsLister()
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o755); err != nil {
		t.Fatalf("create beads dir: %v", err)
	}

	project := config.Project{
		Enabled:     true,
		Priority:    1,
		Workspace:   workspace,
		BeadsDir:    beadsDir,
		UseBranches: true,
	}
	cfg := newRunTickScenarioConfig(5, map[string]config.Project{"test-project": project})
	logBuf := &bytes.Buffer{}
	sched, st, _ := newRunTickScenarioScheduler(t, cfg, lister, logBuf)
	sched.cfg.Projects["test-project"] = project

	bdLogPath := filepath.Join(workspace, "bd.log")
	bdDir := writeFakeBDScript(t, bdLogPath)
	t.Setenv("PATH", bdDir+":"+os.Getenv("PATH"))

	state := &mergeFlowState{}
	sched.mergePR = func(string, int, string) error {
		state.mergeCalls++
		return nil
	}
	sched.getPRStatus = func(string, string) (*git.PRStatus, error) {
		return &git.PRStatus{State: "open", ReviewDecision: "CHANGES_REQUESTED"}, nil
	}
	sched.listBeads = func(string) ([]beads.Bead, error) {
		bead := createTestBead("abc-2", "Not approved", "task", "open", 1)
		bead.Labels = []string{"stage:review"}
		return []beads.Bead{bead}, nil
	}
	seedDispatchAndStatus(t, st, "abc-2")

	sched.processApprovedPRMerges(context.Background())

	if state.mergeCalls != 0 {
		t.Fatalf("expected 0 merge calls, got %d", state.mergeCalls)
	}
	if cmds := readLogLines(t, bdLogPath); len(cmds) > 0 {
		t.Fatalf("did not expect bd commands for unapproved PR, got %v", cmds)
	}
}
