package scheduler

import (
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/beads"
)

func TestHasActiveChurnEscalation(t *testing.T) {
	beadID := "cortex-c4j.3"
	title := "Auto: churn guard blocked bead cortex-c4j.3 (6 dispatches/1h0m0s)"

	tests := []struct {
		name   string
		issues []beads.Bead
		want   bool
	}{
		{
			name: "open bug with discovered-from dependency is active",
			issues: []beads.Bead{
				{
					ID:     "cortex-abc",
					Type:   "bug",
					Status: "open",
					Title:  title,
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-abc", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: true,
		},
		{
			name: "in-progress bug with discovered-from dependency is active",
			issues: []beads.Bead{
				{
					ID:     "cortex-def",
					Type:   "bug",
					Status: "in_progress",
					Title:  title,
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-def", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: true,
		},
		{
			name: "closed bug is ignored",
			issues: []beads.Bead{
				{
					ID:     "cortex-ghi",
					Type:   "bug",
					Status: "closed",
					Title:  title,
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-ghi", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: false,
		},
		{
			name: "non-bug issue is ignored",
			issues: []beads.Bead{
				{
					ID:     "cortex-jkl",
					Type:   "task",
					Status: "open",
					Title:  title,
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-jkl", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: false,
		},
		{
			name: "bug with title mismatch is ignored",
			issues: []beads.Bead{
				{
					ID:     "cortex-mno",
					Type:   "bug",
					Status: "open",
					Title:  "Auto: unrelated incident",
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-mno", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: false,
		},
		{
			name: "bug with only depends_on fallback still matches",
			issues: []beads.Bead{
				{
					ID:        "cortex-pqr",
					Type:      "bug",
					Status:    "open",
					Title:     title,
					DependsOn: []string{beadID},
				},
			},
			want: true,
		},
		{
			name: "multiple issues returns true when any active match exists",
			issues: []beads.Bead{
				{
					ID:     "cortex-stu",
					Type:   "bug",
					Status: "closed",
					Title:  title,
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-stu", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
				{
					ID:     "cortex-vwx",
					Type:   "bug",
					Status: "open",
					Title:  title,
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-vwx", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasActiveChurnEscalation(tt.issues, beadID)
			if got != tt.want {
				t.Fatalf("hasActiveChurnEscalation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHasRecentChurnEscalation(t *testing.T) {
	now := time.Now()
	cutoff := now.Add(-churnWindow)
	beadID := "cortex-c4j.3"
	title := "Auto: churn guard blocked bead cortex-c4j.3 (6 dispatches/1h0m0s)"

	tests := []struct {
		name   string
		issues []beads.Bead
		want   bool
	}{
		{
			name: "open matching bug is treated as recent escalation",
			issues: []beads.Bead{
				{
					ID:     "cortex-open",
					Type:   "bug",
					Status: "open",
					Title:  title,
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-open", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: true,
		},
		{
			name: "recently closed matching bug suppresses duplicate escalation",
			issues: []beads.Bead{
				{
					ID:        "cortex-closed-recent",
					Type:      "bug",
					Status:    "closed",
					Title:     title,
					UpdatedAt: now.Add(-10 * time.Minute),
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-closed-recent", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: true,
		},
		{
			name: "stale closed bug does not suppress new escalation",
			issues: []beads.Bead{
				{
					ID:        "cortex-closed-stale",
					Type:      "bug",
					Status:    "closed",
					Title:     title,
					UpdatedAt: now.Add(-2 * time.Hour),
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-closed-stale", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: false,
		},
		{
			name: "closed bug without updated_at falls back to created_at",
			issues: []beads.Bead{
				{
					ID:        "cortex-created-fallback",
					Type:      "bug",
					Status:    "closed",
					Title:     title,
					CreatedAt: now.Add(-20 * time.Minute),
					Dependencies: []beads.BeadDependency{
						{IssueID: "cortex-created-fallback", DependsOnID: beadID, Type: "discovered-from"},
					},
				},
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasRecentChurnEscalation(tt.issues, beadID, cutoff)
			if got != tt.want {
				t.Fatalf("hasRecentChurnEscalation() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldAutoCloseEpicBreakdownTask(t *testing.T) {
	tests := []struct {
		name   string
		issue  beads.Bead
		byID   map[string]beads.Bead
		wantID string
		want   bool
	}{
		{
			name: "open auto-breakdown task with closed discovered epic is auto-closed",
			issue: beads.Bead{
				ID:     "cortex-34e",
				Type:   "task",
				Status: "open",
				Title:  "Auto: break down epic cortex-a6p into executable bug/task beads",
				Dependencies: []beads.BeadDependency{
					{IssueID: "cortex-34e", DependsOnID: "cortex-a6p", Type: "discovered-from"},
				},
			},
			byID: map[string]beads.Bead{
				"cortex-a6p": {ID: "cortex-a6p", Type: "epic", Status: "closed"},
			},
			wantID: "cortex-a6p",
			want:   true,
		},
		{
			name: "open task is not auto-closed when discovered epic is still open",
			issue: beads.Bead{
				ID:     "cortex-34e",
				Type:   "task",
				Status: "open",
				Title:  "Auto: break down epic cortex-a6p into executable bug/task beads",
				Dependencies: []beads.BeadDependency{
					{IssueID: "cortex-34e", DependsOnID: "cortex-a6p", Type: "discovered-from"},
				},
			},
			byID: map[string]beads.Bead{
				"cortex-a6p": {ID: "cortex-a6p", Type: "epic", Status: "open"},
			},
			want: false,
		},
		{
			name: "stage-qa auto-breakdown task is auto-closed when epic already has executable children",
			issue: beads.Bead{
				ID:     "cortex-34e",
				Type:   "task",
				Status: "open",
				Title:  "Auto: break down epic cortex-a6p into executable bug/task beads",
				Labels: []string{"stage:qa"},
				Dependencies: []beads.BeadDependency{
					{IssueID: "cortex-34e", DependsOnID: "cortex-a6p", Type: "discovered-from"},
				},
			},
			byID: map[string]beads.Bead{
				"cortex-a6p":   {ID: "cortex-a6p", Type: "epic", Status: "open"},
				"cortex-a6p.1": {ID: "cortex-a6p.1", Type: "task", Status: "open", ParentID: "cortex-a6p"},
			},
			wantID: "cortex-a6p",
			want:   true,
		},
		{
			name: "non-qa auto-breakdown task is not auto-closed when epic remains open",
			issue: beads.Bead{
				ID:     "cortex-34e",
				Type:   "task",
				Status: "open",
				Title:  "Auto: break down epic cortex-a6p into executable bug/task beads",
				Labels: []string{"stage:review"},
				Dependencies: []beads.BeadDependency{
					{IssueID: "cortex-34e", DependsOnID: "cortex-a6p", Type: "discovered-from"},
				},
			},
			byID: map[string]beads.Bead{
				"cortex-a6p":   {ID: "cortex-a6p", Type: "epic", Status: "open"},
				"cortex-a6p.1": {ID: "cortex-a6p.1", Type: "task", Status: "open", ParentID: "cortex-a6p"},
			},
			want: false,
		},
		{
			name: "stage-qa auto-breakdown task is not auto-closed without executable children while epic open",
			issue: beads.Bead{
				ID:     "cortex-34e",
				Type:   "task",
				Status: "open",
				Title:  "Auto: break down epic cortex-a6p into executable bug/task beads",
				Labels: []string{"stage:qa"},
				Dependencies: []beads.BeadDependency{
					{IssueID: "cortex-34e", DependsOnID: "cortex-a6p", Type: "discovered-from"},
				},
			},
			byID: map[string]beads.Bead{
				"cortex-a6p": {ID: "cortex-a6p", Type: "epic", Status: "open"},
			},
			want: false,
		},
		{
			name: "task is not auto-closed without discovered-from dependency",
			issue: beads.Bead{
				ID:     "cortex-34e",
				Type:   "task",
				Status: "open",
				Title:  "Auto: break down epic cortex-a6p into executable bug/task beads",
			},
			byID: map[string]beads.Bead{
				"cortex-a6p": {ID: "cortex-a6p", Type: "epic", Status: "closed"},
			},
			want: false,
		},
		{
			name: "task is not auto-closed when discovered-from id mismatches title epic id",
			issue: beads.Bead{
				ID:     "cortex-34e",
				Type:   "task",
				Status: "open",
				Title:  "Auto: break down epic cortex-a6p into executable bug/task beads",
				Dependencies: []beads.BeadDependency{
					{IssueID: "cortex-34e", DependsOnID: "cortex-other", Type: "discovered-from"},
				},
			},
			byID: map[string]beads.Bead{
				"cortex-a6p":   {ID: "cortex-a6p", Type: "epic", Status: "closed"},
				"cortex-other": {ID: "cortex-other", Type: "epic", Status: "closed"},
			},
			want: false,
		},
		{
			name: "non-matching title is not auto-closed",
			issue: beads.Bead{
				ID:     "cortex-34e",
				Type:   "task",
				Status: "open",
				Title:  "Auto: churn guard blocked bead cortex-34e (6 dispatches/1h0m0s)",
				Dependencies: []beads.BeadDependency{
					{IssueID: "cortex-34e", DependsOnID: "cortex-a6p", Type: "discovered-from"},
				},
			},
			byID: map[string]beads.Bead{
				"cortex-a6p": {ID: "cortex-a6p", Type: "epic", Status: "closed"},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotID, got := shouldAutoCloseEpicBreakdownTask(tt.issue, tt.byID)
			if got != tt.want {
				t.Fatalf("shouldAutoCloseEpicBreakdownTask() = %v, want %v", got, tt.want)
			}
			if gotID != tt.wantID {
				t.Fatalf("shouldAutoCloseEpicBreakdownTask() id = %q, want %q", gotID, tt.wantID)
			}
		})
	}
}
