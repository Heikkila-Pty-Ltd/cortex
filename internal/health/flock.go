package health

import (
	"fmt"
	"os"
	"syscall"
)

// AcquireFlock attempts to acquire an exclusive file lock.
// Returns the lock file handle (keep open for process lifetime) or an error.
func AcquireFlock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("flock: open %s: %w", path, err)
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("another Cortex instance is running (lock: %s)", path)
	}

	// Write our PID for debugging
	f.Truncate(0)
	f.Seek(0, 0)
	fmt.Fprintf(f, "%d\n", os.Getpid())

	return f, nil
}

// ReleaseFlock releases the lock and removes the lock file.
func ReleaseFlock(f *os.File) {
	if f == nil {
		return
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	name := f.Name()
	f.Close()
	os.Remove(name)
}
