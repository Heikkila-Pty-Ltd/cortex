package health

import (
	"path/filepath"
	"testing"
)

func TestAcquireFlock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "cortex.lock")

	f, err := AcquireFlock(lockPath)
	if err != nil {
		t.Fatalf("first lock should succeed: %v", err)
	}
	defer ReleaseFlock(f)

	// Second lock attempt should fail
	_, err = AcquireFlock(lockPath)
	if err == nil {
		t.Fatal("second lock should fail")
	}
}

func TestReleaseFlock(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "cortex.lock")

	f, err := AcquireFlock(lockPath)
	if err != nil {
		t.Fatal(err)
	}

	ReleaseFlock(f)

	// Should be able to lock again after release
	f2, err := AcquireFlock(lockPath)
	if err != nil {
		t.Fatalf("lock after release should succeed: %v", err)
	}
	ReleaseFlock(f2)
}
