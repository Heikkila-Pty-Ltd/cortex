package store

import "testing"

func TestExecutionPlanGateLifecycle(t *testing.T) {
	s := tempStore(t)

	active, plan, err := s.HasActiveApprovedPlan()
	if err != nil {
		t.Fatalf("HasActiveApprovedPlan failed: %v", err)
	}
	if active {
		t.Fatalf("expected inactive gate initially")
	}
	if plan != nil {
		t.Fatalf("expected nil plan initially")
	}

	if err := s.SetActiveApprovedPlan("plan-2026-02-18-a", "simon"); err != nil {
		t.Fatalf("SetActiveApprovedPlan failed: %v", err)
	}

	active, plan, err = s.HasActiveApprovedPlan()
	if err != nil {
		t.Fatalf("HasActiveApprovedPlan after set failed: %v", err)
	}
	if !active {
		t.Fatalf("expected active gate after set")
	}
	if plan == nil {
		t.Fatalf("expected plan metadata after set")
	}
	if plan.PlanID != "plan-2026-02-18-a" {
		t.Fatalf("plan id = %q, want %q", plan.PlanID, "plan-2026-02-18-a")
	}
	if plan.ApprovedBy != "simon" {
		t.Fatalf("approved by = %q, want %q", plan.ApprovedBy, "simon")
	}
	if plan.ApprovedAt.IsZero() {
		t.Fatalf("expected approved_at to be populated")
	}
	if plan.ActivatedAt.IsZero() {
		t.Fatalf("expected activated_at to be populated")
	}

	if err := s.ClearActiveApprovedPlan(); err != nil {
		t.Fatalf("ClearActiveApprovedPlan failed: %v", err)
	}

	active, plan, err = s.HasActiveApprovedPlan()
	if err != nil {
		t.Fatalf("HasActiveApprovedPlan after clear failed: %v", err)
	}
	if active {
		t.Fatalf("expected inactive gate after clear")
	}
	if plan != nil {
		t.Fatalf("expected nil plan after clear")
	}
}

func TestExecutionPlanGateRejectsEmptyPlanID(t *testing.T) {
	s := tempStore(t)

	if err := s.SetActiveApprovedPlan("   ", "simon"); err == nil {
		t.Fatalf("expected error for empty plan id")
	}
}
