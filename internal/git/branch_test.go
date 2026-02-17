package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// setupTestRepo creates a temporary git repository for testing
func setupTestRepo(t *testing.T) string {
	t.Helper()
	
	tmpDir := t.TempDir()
	
	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}
	
	// Configure git user (required for commits)
	exec.Command("git", "config", "user.name", "Test User").Dir = tmpDir
	exec.Command("git", "config", "user.email", "test@example.com").Dir = tmpDir
	
	// Create an initial commit
	testFile := filepath.Join(tmpDir, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test Repo\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}
	
	cmd = exec.Command("git", "add", "README.md")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	
	cmd = exec.Command("git", "commit", "-m", "Initial commit")
	cmd.Dir = tmpDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}
	
	return tmpDir
}

func TestGetCurrentBranch(t *testing.T) {
	repo := setupTestRepo(t)
	
	branch, err := GetCurrentBranch(repo)
	if err != nil {
		t.Fatalf("GetCurrentBranch failed: %v", err)
	}
	
	// Should be on main or master
	if branch != "main" && branch != "master" {
		t.Errorf("expected branch to be main or master, got %s", branch)
	}
}

func TestCreateFeatureBranch(t *testing.T) {
	repo := setupTestRepo(t)
	
	// Get current branch to use as base
	baseBranch, err := GetCurrentBranch(repo)
	if err != nil {
		t.Fatalf("failed to get current branch: %v", err)
	}
	
	beadID := "test-123"
	if err := CreateFeatureBranch(repo, beadID, baseBranch); err != nil {
		t.Fatalf("CreateFeatureBranch failed: %v", err)
	}
	
	// Verify we're on the new branch
	currentBranch, err := GetCurrentBranch(repo)
	if err != nil {
		t.Fatalf("failed to get current branch after creation: %v", err)
	}
	
	expectedBranch := "feat/test-123"
	if currentBranch != expectedBranch {
		t.Errorf("expected to be on branch %s, got %s", expectedBranch, currentBranch)
	}
}

func TestBranchExists(t *testing.T) {
	repo := setupTestRepo(t)
	
	// Check existing branch
	currentBranch, _ := GetCurrentBranch(repo)
	exists, err := BranchExists(repo, currentBranch)
	if err != nil {
		t.Fatalf("BranchExists failed: %v", err)
	}
	if !exists {
		t.Errorf("expected current branch %s to exist", currentBranch)
	}
	
	// Check non-existing branch
	exists, err = BranchExists(repo, "nonexistent-branch")
	if err != nil {
		t.Fatalf("BranchExists failed for nonexistent branch: %v", err)
	}
	if exists {
		t.Errorf("expected nonexistent-branch to not exist")
	}
}

func TestEnsureFeatureBranchWithBase(t *testing.T) {
	repo := setupTestRepo(t)
	
	baseBranch, _ := GetCurrentBranch(repo)
	beadID := "test-456"
	
	// Test creating new branch
	if err := EnsureFeatureBranchWithBase(repo, beadID, baseBranch, "feat/"); err != nil {
		t.Fatalf("EnsureFeatureBranchWithBase failed: %v", err)
	}
	
	expectedBranch := "feat/test-456"
	currentBranch, _ := GetCurrentBranch(repo)
	if currentBranch != expectedBranch {
		t.Errorf("expected to be on branch %s, got %s", expectedBranch, currentBranch)
	}
	
	// Switch to different branch
	cmd := exec.Command("git", "checkout", baseBranch)
	cmd.Dir = repo
	if err := cmd.Run(); err != nil {
		t.Fatalf("failed to switch back to base branch: %v", err)
	}
	
	// Test switching to existing branch
	if err := EnsureFeatureBranchWithBase(repo, beadID, baseBranch, "feat/"); err != nil {
		t.Fatalf("EnsureFeatureBranchWithBase failed on existing branch: %v", err)
	}
	
	currentBranch, _ = GetCurrentBranch(repo)
	if currentBranch != expectedBranch {
		t.Errorf("expected to be on existing branch %s, got %s", expectedBranch, currentBranch)
	}
}