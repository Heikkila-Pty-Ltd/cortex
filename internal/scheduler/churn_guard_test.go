package scheduler

import (
	"testing"

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
