package team

import (
	"testing"
)

func TestRoleDescriptionsComplete(t *testing.T) {
	expectedRoles := []string{"scrum", "planner", "coder", "reviewer", "ops"}
	for _, role := range expectedRoles {
		if _, ok := roleDescriptions[role]; !ok {
			t.Errorf("missing role description for %q", role)
		}
	}
}

func TestListTeamNoAgents(t *testing.T) {
	// Using a project name that won't have agents created
	agents, err := ListTeam("nonexistent-test-project-xyz", []string{"scrum", "planner", "coder", "reviewer", "ops"})
	if err != nil {
		t.Fatal(err)
	}

	if len(agents) != 5 {
		t.Fatalf("expected 5 agents, got %d", len(agents))
	}

	for _, a := range agents {
		if a.Exists {
			t.Errorf("agent %q should not exist", a.Name)
		}
	}

	// Verify role assignment
	expectedRoles := map[string]string{
		"nonexistent-test-project-xyz-scrum":    "scrum",
		"nonexistent-test-project-xyz-planner":  "planner",
		"nonexistent-test-project-xyz-coder":    "coder",
		"nonexistent-test-project-xyz-reviewer": "reviewer",
		"nonexistent-test-project-xyz-ops":      "ops",
	}

	for _, a := range agents {
		expected, ok := expectedRoles[a.Name]
		if !ok {
			t.Errorf("unexpected agent name %q", a.Name)
			continue
		}
		if a.Role != expected {
			t.Errorf("agent %q has role %q, want %q", a.Name, a.Role, expected)
		}
	}
}
