package learner

import "testing"

func TestScoreDispatchDetectsTestsPass(t *testing.T) {
	output := "ok\tgithub.com/example\t0.123s\n"
	score, err := ScoreDispatch(output, "", "")
	if err != nil {
		t.Fatalf("ScoreDispatch failed: %v", err)
	}
	if score.TestsPassed == nil || !*score.TestsPassed {
		t.Fatalf("expected tests to pass, got %v", score.TestsPassed)
	}
	if score.Overall <= 0 {
		t.Fatalf("expected positive overall score, got %f", score.Overall)
	}
}

func TestScoreDispatchDetectsTestsFail(t *testing.T) {
	output := "FAIL\texample\t0.123s\n"
	score, err := ScoreDispatch(output, "", "")
	if err != nil {
		t.Fatalf("ScoreDispatch failed: %v", err)
	}
	if score.TestsPassed == nil || *score.TestsPassed {
		t.Fatalf("expected tests to fail, got %v", score.TestsPassed)
	}
}

func TestScoreDispatchParsesDiffStatsAndCommit(t *testing.T) {
	output := "2 files changed, 10 insertions(+), 5 deletions(-)\nCommitted successfully\n"
	score, err := ScoreDispatch(output, "", "")
	if err != nil {
		t.Fatalf("ScoreDispatch failed: %v", err)
	}
	if score.FilesChanged != 2 {
		t.Fatalf("expected 2 files changed, got %d", score.FilesChanged)
	}
	if score.LinesChanged != 5 {
		t.Fatalf("expected 5 net lines changed, got %d", score.LinesChanged)
	}
	if !score.CommitMade {
		t.Fatalf("expected CommitMade=true")
	}
	if score.Overall <= 0 {
		t.Fatalf("expected positive overall score, got %f", score.Overall)
	}
}

func TestScoreDispatchDetectsBeadClose(t *testing.T) {
	output := "agent complete\nbd close ISSUE-123\n"
	score, err := ScoreDispatch(output, "", "")
	if err != nil {
		t.Fatalf("ScoreDispatch failed: %v", err)
	}
	if !score.BeadClosed {
		t.Fatalf("expected bead close detection from output")
	}
}

func TestScoreDispatchNoOutputDefaultsToNeutral(t *testing.T) {
	score, err := ScoreDispatch("   \n", "", "")
	if err != nil {
		t.Fatalf("ScoreDispatch failed: %v", err)
	}
	if score.Overall != 0.5 {
		t.Fatalf("expected neutral score 0.5, got %f", score.Overall)
	}
	if score.TestsPassed != nil {
		t.Fatalf("expected nil tests result for empty output")
	}
}
