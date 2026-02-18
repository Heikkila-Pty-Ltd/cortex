package git

import (
	"testing"
	"time"
)

func TestCleanupBranchesOlderThanDeletesMatchingPrefix(t *testing.T) {
	repo := setupTestRepo(t)
	baseBranch, _ := GetCurrentBranch(repo)

	runGit(t, repo, "checkout", "-b", "ctx/old-1")
	runGit(t, repo, "checkout", baseBranch)
	runGit(t, repo, "checkout", "-b", "ctx/old-2")
	runGit(t, repo, "checkout", baseBranch)
	runGit(t, repo, "checkout", "-b", "feat/keep")
	runGit(t, repo, "checkout", baseBranch)

	deleted, err := CleanupBranchesOlderThan(repo, "ctx/", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CleanupBranchesOlderThan failed: %v", err)
	}

	if len(deleted) != 2 {
		t.Fatalf("expected 2 deleted branches, got %d (%v)", len(deleted), deleted)
	}

	ctxOneExists, err := BranchExists(repo, "ctx/old-1")
	if err != nil {
		t.Fatalf("BranchExists failed: %v", err)
	}
	if ctxOneExists {
		t.Fatal("expected ctx/old-1 to be deleted")
	}

	ctxTwoExists, err := BranchExists(repo, "ctx/old-2")
	if err != nil {
		t.Fatalf("BranchExists failed: %v", err)
	}
	if ctxTwoExists {
		t.Fatal("expected ctx/old-2 to be deleted")
	}

	keepExists, err := BranchExists(repo, "feat/keep")
	if err != nil {
		t.Fatalf("BranchExists failed: %v", err)
	}
	if !keepExists {
		t.Fatal("expected feat/keep to be retained")
	}
}

func TestCleanupBranchesOlderThanNoPrefixNoop(t *testing.T) {
	repo := setupTestRepo(t)

	deleted, err := CleanupBranchesOlderThan(repo, "", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("CleanupBranchesOlderThan failed: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("expected no deletions, got %v", deleted)
	}
}
