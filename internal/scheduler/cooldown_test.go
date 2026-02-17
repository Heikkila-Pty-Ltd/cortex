package scheduler

import (
	"testing"
	"time"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

func TestDispatchCooldown(t *testing.T) {
	// Setup test database
	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := &config.Config{
		General: config.General{
			MaxPerTick:       3,
			DispatchCooldown: config.Duration{Duration: 5 * time.Minute},
		},
	}

	beadID := "test-bead"

	// Test 1: Initially should not be recently dispatched
	recentlyDispatched, err := st.WasBeadDispatchedRecently(beadID, cfg.General.DispatchCooldown.Duration)
	if err != nil {
		t.Fatal(err)
	}
	if recentlyDispatched {
		t.Error("bead should not be recently dispatched initially")
	}

	// Test 2: Record a dispatch to simulate recent activity
	_, err = st.RecordDispatch(beadID, "test-project", "test-agent", "test-provider", "fast", 123, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Test 3: Now should be recently dispatched
	recentlyDispatched, err = st.WasBeadDispatchedRecently(beadID, cfg.General.DispatchCooldown.Duration)
	if err != nil {
		t.Fatal(err)
	}
	if !recentlyDispatched {
		t.Error("bead should be recently dispatched after recording dispatch")
	}

	// Test 4: With zero cooldown, should not be recent
	recentlyDispatched, err = st.WasBeadDispatchedRecently(beadID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if recentlyDispatched {
		t.Error("bead should not be recent with zero cooldown")
	}
}

func TestDispatchCooldownDisabled(t *testing.T) {
	// Setup test database
	tmpDB := t.TempDir() + "/test.db"
	st, err := store.Open(tmpDB)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Config with disabled cooldown
	cfg := &config.Config{
		General: config.General{
			MaxPerTick:       3,
			DispatchCooldown: config.Duration{Duration: 0}, // Disabled
		},
	}

	beadID := "test-bead"

	// Record a dispatch
	_, err = st.RecordDispatch(beadID, "test-project", "test-agent", "test-provider", "fast", 123, "", "test prompt", "", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// With cooldown disabled, should not block re-dispatch (beyond checking recent dispatches)
	// This test mainly ensures the cooldown logic doesn't break when disabled
	recentlyDispatched, err := st.WasBeadDispatchedRecently(beadID, cfg.General.DispatchCooldown.Duration)
	if err != nil {
		t.Fatal(err)
	}
	if recentlyDispatched {
		t.Error("bead should not be recent when cooldown is disabled (duration 0)")
	}
}