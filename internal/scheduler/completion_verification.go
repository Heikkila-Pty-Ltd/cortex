package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/git"
	"github.com/antigravity-dev/cortex/internal/store"
)

// CompletionVerificationResult represents the result of completion verification for a project
type CompletionVerificationResult struct {
	Project            string
	CompletedBeads     []CompletedBead    // Beads that should be closed
	OrphanedCommits    []OrphanedCommit   // Commits referencing non-existent beads
	VerificationErrors []VerificationError // Errors during verification
}

// CompletedBead represents a bead that has commits but is still open
type CompletedBead struct {
	BeadID       string
	Status       string
	Title        string
	Type         string
	Commits      []git.Commit
	LastCommitAt time.Time
}

// OrphanedCommit represents a commit referencing a non-existent bead
type OrphanedCommit struct {
	BeadID string
	Commit git.Commit
}

// VerificationError represents an error during the verification process
type VerificationError struct {
	BeadID string
	Error  string
}

// CompletionVerifier handles verification of bead completion based on git commits
type CompletionVerifier struct {
	store    *store.Store
	logger   *slog.Logger
	projects map[string]config.Project
}

// NewCompletionVerifier creates a new completion verifier
func NewCompletionVerifier(store *store.Store, logger *slog.Logger) *CompletionVerifier {
	return &CompletionVerifier{
		store:  store,
		logger: logger,
	}
}

// VerifyCompletion checks for beads that should be closed based on git commit references
func (cv *CompletionVerifier) VerifyCompletion(ctx context.Context, projects map[string]config.Project, lookbackDays int) ([]CompletionVerificationResult, error) {
	var results []CompletionVerificationResult
	
	for projectName, project := range projects {
		if !project.Enabled {
			continue
		}
		
		result := CompletionVerificationResult{
			Project: projectName,
		}
		
		cv.logger.Debug("verifying completion for project", "project", projectName)
		
		// Get recent commits from the project workspace
		workspace := config.ExpandHome(project.Workspace)
		commits, err := git.GetRecentCommits(workspace, lookbackDays)
		if err != nil {
			result.VerificationErrors = append(result.VerificationErrors, VerificationError{
				Error: fmt.Sprintf("failed to get commits: %v", err),
			})
			results = append(results, result)
			continue
		}
		
		// Get all beads for this project
		beadsDir := config.ExpandHome(project.BeadsDir)
		beadList, err := beads.ListBeads(beadsDir)
		if err != nil {
			result.VerificationErrors = append(result.VerificationErrors, VerificationError{
				Error: fmt.Sprintf("failed to list beads: %v", err),
			})
			results = append(results, result)
			continue
		}
		
		// Create a map for quick bead lookup
		beadMap := make(map[string]beads.Bead)
		for _, bead := range beadList {
			beadMap[bead.ID] = bead
		}
		
		// Group commits by bead ID
		commitsByBead := make(map[string][]git.Commit)
		for _, commit := range commits {
			for _, beadID := range commit.BeadIDs {
				commitsByBead[beadID] = append(commitsByBead[beadID], commit)
			}
		}
		
		// Check each bead ID found in commits
		for beadID, beadCommits := range commitsByBead {
			bead, exists := beadMap[beadID]
			if !exists {
				// Commit references non-existent bead
				for _, commit := range beadCommits {
					result.OrphanedCommits = append(result.OrphanedCommits, OrphanedCommit{
						BeadID: beadID,
						Commit: commit,
					})
				}
				continue
			}
			
			// Check if bead should be considered completed
			if cv.shouldBeadBeClosed(bead, beadCommits, projectName) {
				// Find the most recent commit for this bead
				var lastCommitAt time.Time
				for _, commit := range beadCommits {
					if commit.Date.After(lastCommitAt) {
						lastCommitAt = commit.Date
					}
				}
				
				result.CompletedBeads = append(result.CompletedBeads, CompletedBead{
					BeadID:       beadID,
					Status:       bead.Status,
					Title:        bead.Title,
					Type:         bead.Type,
					Commits:      beadCommits,
					LastCommitAt: lastCommitAt,
				})
			}
		}
		
		results = append(results, result)
	}
	
	return results, nil
}

// shouldBeadBeClosed determines if a bead should be closed based on commits and other factors
func (cv *CompletionVerifier) shouldBeadBeClosed(bead beads.Bead, commits []git.Commit, projectName string) bool {
	// Only consider open beads
	if strings.ToLower(strings.TrimSpace(bead.Status)) != "open" {
		return false
	}
	
	// Don't auto-close epics - they require manual review
	if strings.ToLower(bead.Type) == "epic" {
		return false
	}
	
	// Need at least one commit
	if len(commits) == 0 {
		return false
	}
	
	// Check for commit messages that indicate completion
	for _, commit := range commits {
		if cv.commitIndicatesCompletion(commit.Message, bead.ID) {
			return true
		}
	}
	
	// Check if commits are recent and contain implementation keywords
	cutoff := time.Now().AddDate(0, 0, -2) // 2 days ago
	hasRecentImplementation := false
	
	for _, commit := range commits {
		if commit.Date.After(cutoff) && cv.commitIndicatesImplementation(commit.Message) {
			hasRecentImplementation = true
			break
		}
	}
	
	// If we have recent implementation commits, consider checking dispatch success
	if hasRecentImplementation {
		// Check if the latest dispatch for this bead was successful
		if cv.hasSuccessfulRecentDispatch(bead.ID, projectName) {
			return true
		}
	}
	
	return false
}

// commitIndicatesCompletion checks if a commit message indicates the work is complete
func (cv *CompletionVerifier) commitIndicatesCompletion(message, beadID string) bool {
	message = strings.ToLower(message)
	
	// Strong completion indicators
	completionIndicators := []string{
		"fix(" + strings.ToLower(beadID) + ")",
		"feat(" + strings.ToLower(beadID) + ")",
		"closes " + strings.ToLower(beadID),
		"close " + strings.ToLower(beadID),
		"fixes " + strings.ToLower(beadID),
		"fix " + strings.ToLower(beadID),
		"completes " + strings.ToLower(beadID),
		"complete " + strings.ToLower(beadID),
		"finishes " + strings.ToLower(beadID),
		"finish " + strings.ToLower(beadID),
		"implements " + strings.ToLower(beadID),
		"implement " + strings.ToLower(beadID),
	}
	
	for _, indicator := range completionIndicators {
		if strings.Contains(message, indicator) {
			return true
		}
	}
	
	return false
}

// commitIndicatesImplementation checks if a commit message indicates actual implementation work
func (cv *CompletionVerifier) commitIndicatesImplementation(message string) bool {
	message = strings.ToLower(message)
	
	implementationKeywords := []string{
		"implement", "add", "create", "fix", "update", "improve",
		"enhance", "modify", "refactor", "optimize", "build",
		"develop", "code", "write", "test", "tests",
	}
	
	for _, keyword := range implementationKeywords {
		if strings.Contains(message, keyword) {
			return true
		}
	}
	
	return false
}

// hasSuccessfulRecentDispatch checks if the bead had a recent successful dispatch
func (cv *CompletionVerifier) hasSuccessfulRecentDispatch(beadID, projectName string) bool {
	// Skip dispatch check if store is not available (e.g., in tests)
	if cv.store == nil {
		return false
	}
	
	// Get recent dispatches for this bead
	dispatches, err := cv.store.GetDispatchesByBead(beadID)
	if err != nil {
		cv.logger.Warn("failed to get dispatches for bead", "bead", beadID, "error", err)
		return false
	}
	
	if len(dispatches) == 0 {
		return false
	}
	
	// Check the most recent dispatch
	mostRecent := dispatches[0]
	for _, d := range dispatches {
		if d.DispatchedAt.After(mostRecent.DispatchedAt) {
			mostRecent = d
		}
	}
	
	// Consider successful if completed within the last 24 hours
	cutoff := time.Now().AddDate(0, 0, -1)
	return mostRecent.Status == "completed" && mostRecent.CompletedAt.Valid && mostRecent.CompletedAt.Time.After(cutoff)
}

// AutoCloseCompletedBeads automatically closes beads that have been verified as completed
func (cv *CompletionVerifier) AutoCloseCompletedBeads(ctx context.Context, results []CompletionVerificationResult, dryRun bool) error {
	for _, result := range results {
		if len(result.CompletedBeads) == 0 {
			continue
		}
		
		cv.logger.Info("found beads that should be auto-closed",
			"project", result.Project,
			"count", len(result.CompletedBeads),
			"dry_run", dryRun)
		
		for _, completedBead := range result.CompletedBeads {
			if dryRun {
				cv.logger.Info("would auto-close completed bead",
					"project", result.Project,
					"bead", completedBead.BeadID,
					"title", completedBead.Title,
					"commits", len(completedBead.Commits),
					"last_commit", completedBead.LastCommitAt.Format("2006-01-02 15:04:05"),
					"dry_run", true)
			} else {
				// Find project config to get beads directory
				projectConfig, exists := cv.findProjectConfig(result.Project)
				if !exists {
					cv.logger.Error("project config not found for auto-close", "project", result.Project)
					continue
				}
				
				beadsDir := config.ExpandHome(projectConfig.BeadsDir)
				reason := fmt.Sprintf("Auto-closed: found %d commits indicating completion, last commit %s",
					len(completedBead.Commits), completedBead.LastCommitAt.Format("2006-01-02 15:04:05"))
				
				if err := beads.CloseBeadWithReasonCtx(ctx, beadsDir, completedBead.BeadID, reason); err != nil {
					cv.logger.Error("failed to auto-close completed bead",
						"project", result.Project,
						"bead", completedBead.BeadID,
						"error", err)
					continue
				}
				
				cv.logger.Info("auto-closed completed bead",
					"project", result.Project,
					"bead", completedBead.BeadID,
					"title", completedBead.Title,
					"commits", len(completedBead.Commits))
				
				// Record health event
				if cv.store != nil {
					_ = cv.store.RecordHealthEventWithDispatch("bead_auto_closed",
						fmt.Sprintf("project %s bead %s auto-closed after detecting completion in %d commits",
							result.Project, completedBead.BeadID, len(completedBead.Commits)),
						0, completedBead.BeadID)
				}
			}
		}
	}
	
	return nil
}

// SetProjects sets the project configurations for the verifier
func (cv *CompletionVerifier) SetProjects(projects map[string]config.Project) {
	cv.projects = projects
}

// Helper to find project config
func (cv *CompletionVerifier) findProjectConfig(projectName string) (config.Project, bool) {
	if cv.projects == nil {
		return config.Project{}, false
	}
	
	project, exists := cv.projects[projectName]
	return project, exists
}