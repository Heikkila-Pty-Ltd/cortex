package store

import "testing"

func TestWorkflowTrackingLifecycle(t *testing.T) {
	s := tempStore(t)

	stages := []string{"coding", "review", "qa"}
	if err := s.InitBeadWorkflow("workflow-proj", "workflow-bead", "dev", stages); err != nil {
		t.Fatalf("InitBeadWorkflow failed: %v", err)
	}

	stage, err := s.GetBeadStage("workflow-proj", "workflow-bead")
	if err != nil {
		t.Fatalf("GetBeadStage failed: %v", err)
	}
	if stage.CurrentStage != "coding" {
		t.Fatalf("expected current stage coding, got %q", stage.CurrentStage)
	}
	if stage.TotalStages != 3 {
		t.Fatalf("expected total stages 3, got %d", stage.TotalStages)
	}
	if len(stage.StageHistory) != 3 {
		t.Fatalf("expected 3 history entries, got %d", len(stage.StageHistory))
	}

	if err := s.RecordStageCompletion("workflow-proj", "workflow-bead", "coding", 101); err != nil {
		t.Fatalf("RecordStageCompletion coding failed: %v", err)
	}
	if err := s.AdvanceStage("workflow-proj", "workflow-bead"); err != nil {
		t.Fatalf("AdvanceStage to review failed: %v", err)
	}

	stage, err = s.GetBeadStage("workflow-proj", "workflow-bead")
	if err != nil {
		t.Fatalf("GetBeadStage after advance failed: %v", err)
	}
	if stage.CurrentStage != "review" {
		t.Fatalf("expected current stage review, got %q", stage.CurrentStage)
	}

	atReview, err := s.GetBeadsAtStage("workflow-proj", "review")
	if err != nil {
		t.Fatalf("GetBeadsAtStage failed: %v", err)
	}
	if len(atReview) != 1 {
		t.Fatalf("expected 1 bead at review, got %d", len(atReview))
	}

	if err := s.RecordStageCompletion("workflow-proj", "workflow-bead", "review", 102); err != nil {
		t.Fatalf("RecordStageCompletion review failed: %v", err)
	}
	if err := s.AdvanceStage("workflow-proj", "workflow-bead"); err != nil {
		t.Fatalf("AdvanceStage to qa failed: %v", err)
	}
	if err := s.RecordStageCompletion("workflow-proj", "workflow-bead", "qa", 103); err != nil {
		t.Fatalf("RecordStageCompletion qa failed: %v", err)
	}

	complete, err := s.IsWorkflowComplete("workflow-proj", "workflow-bead")
	if err != nil {
		t.Fatalf("IsWorkflowComplete failed: %v", err)
	}
	if !complete {
		t.Fatal("expected workflow to be complete")
	}
}

func TestDispatchWorkflowColumnExists(t *testing.T) {
	s := tempStore(t)

	var count int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM pragma_table_info('dispatches') WHERE name = 'workflow'`).Scan(&count); err != nil {
		t.Fatalf("failed to query dispatches schema: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected workflow column in dispatches table, got count=%d", count)
	}
}
