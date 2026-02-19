package learner

import "testing"

func TestCeremonyPurposeMapping(t *testing.T) {
	if got := ceremonyPurpose("review"); got != "review" {
		t.Fatalf("ceremonyPurpose(review)=%q want review", got)
	}
	if got := ceremonyPurpose("retrospective"); got != "reporting" {
		t.Fatalf("ceremonyPurpose(retrospective)=%q want reporting", got)
	}
	if got := ceremonyPurpose("anything-else"); got != "reporting" {
		t.Fatalf("ceremonyPurpose(default)=%q want reporting", got)
	}
}
