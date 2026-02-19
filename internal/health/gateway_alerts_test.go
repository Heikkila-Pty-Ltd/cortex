package health

import (
	"testing"
	"time"
)

func TestShouldLogGatewayCritical(t *testing.T) {
	m := &Monitor{}
	base := time.Date(2026, 2, 19, 13, 0, 0, 0, time.UTC)

	if !m.shouldLogGatewayCritical(base, 3) {
		t.Fatal("first critical threshold crossing should log")
	}
	if m.shouldLogGatewayCritical(base.Add(1*time.Minute), 3) {
		t.Fatal("duplicate critical count inside cooldown should not log")
	}
	if !m.shouldLogGatewayCritical(base.Add(2*time.Minute), 4) {
		t.Fatal("higher critical count should log immediately")
	}
	if m.shouldLogGatewayCritical(base.Add(3*time.Minute), 4) {
		t.Fatal("same critical count inside cooldown should not log")
	}
	if !m.shouldLogGatewayCritical(base.Add(gatewayCriticalLogInterval+3*time.Minute), 4) {
		t.Fatal("same critical count after cooldown should log")
	}
	if m.shouldLogGatewayCritical(base.Add(gatewayCriticalLogInterval+4*time.Minute), 2) {
		t.Fatal("below threshold should never log")
	}
	if !m.shouldLogGatewayCritical(base.Add(gatewayCriticalLogInterval+5*time.Minute), 3) {
		t.Fatal("crossing threshold again after reset should log")
	}
}
