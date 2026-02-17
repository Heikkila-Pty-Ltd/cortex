package health

import (
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/store"
)

func TestClassifyDeadSessionEvent(t *testing.T) {
	sessionName := "ctx-cortex-test-123"

	tests := []struct {
		name          string
		dispatch      *store.Dispatch
		wantEventType string
		wantContains  string
	}{
		{
			name:          "no matching dispatch stays alerting",
			dispatch:      nil,
			wantEventType: "zombie_killed",
			wantContains:  "no matching dispatch",
		},
		{
			name: "running dispatch stays alerting",
			dispatch: &store.Dispatch{
				ID:     99,
				BeadID: "cortex-abc.1",
				Status: "running",
			},
			wantEventType: "zombie_killed",
			wantContains:  "status running",
		},
		{
			name: "completed dispatch becomes cleanup event",
			dispatch: &store.Dispatch{
				ID:     100,
				BeadID: "cortex-abc.2",
				Status: "completed",
			},
			wantEventType: "session_cleaned",
			wantContains:  "status completed",
		},
		{
			name: "failed dispatch becomes cleanup event",
			dispatch: &store.Dispatch{
				ID:     101,
				BeadID: "cortex-abc.3",
				Status: "failed",
			},
			wantEventType: "session_cleaned",
			wantContains:  "status failed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotType, gotDetails := classifyDeadSessionEvent(sessionName, tc.dispatch)
			if gotType != tc.wantEventType {
				t.Fatalf("event type = %q, want %q", gotType, tc.wantEventType)
			}
			if !strings.Contains(gotDetails, tc.wantContains) {
				t.Fatalf("details = %q, expected to contain %q", gotDetails, tc.wantContains)
			}
		})
	}
}

