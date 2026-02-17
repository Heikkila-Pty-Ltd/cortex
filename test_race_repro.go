package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/antigravity-dev/cortex/internal/dispatch"
)

// waitForExitStatus polls for exit status with exponential backoff
func waitForExitStatus(sessionName string, maxWait time.Duration) (status string, exitCode int, err error) {
	start := time.Now()
	backoff := 50 * time.Millisecond
	maxBackoff := 500 * time.Millisecond

	for time.Since(start) < maxWait {
		status, exitCode := dispatch.SessionStatus(sessionName)
		
		switch status {
		case "exited":
			return status, exitCode, nil
		case "gone":
			return status, exitCode, fmt.Errorf("session disappeared unexpectedly")
		case "running":
			// Keep polling
		default:
			return status, exitCode, fmt.Errorf("unknown status: %s", status)
		}

		time.Sleep(backoff)
		if backoff < maxBackoff {
			backoff = backoff * 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}
	}

	// Timeout - return current status
	status, exitCode = dispatch.SessionStatus(sessionName)
	return status, exitCode, fmt.Errorf("timeout waiting for exit after %v, current status: %s", maxWait, status)
}

func main() {
	fmt.Println("Testing intermittent exit code capture race...")

	// Simulate the test suite environment by running multiple tests concurrently
	failures := 0
	runs := 20

	for i := 0; i < runs; i++ {
		fmt.Printf("Run %d: ", i+1)

		d := dispatch.NewTmuxDispatcher()
		name := dispatch.SessionName("test", "exitcode")

		err := d.DispatchToSession(context.Background(), name, `sh -c 'sleep 0.2; exit 42'`, "/tmp", nil)
		if err != nil {
			fmt.Printf("Dispatch failed: %v\n", err)
			failures++
			continue
		}
		defer dispatch.KillSession(name)

		// Use our deterministic polling instead of fixed sleep
		status, exitCode, err := waitForExitStatus(name, 5*time.Second)
		
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
			failures++
		} else if status != "exited" {
			fmt.Printf("FAIL: expected status=exited, got %q\n", status)
			failures++
		} else if exitCode != 42 {
			fmt.Printf("FAIL: expected exit code 42, got %d\n", exitCode)
			failures++
		} else {
			fmt.Printf("PASS\n")
		}

		dispatch.KillSession(name)
	}

	fmt.Printf("\nResults: %d/%d passed, %d failures\n", runs-failures, runs, failures)
	if failures > 0 {
		os.Exit(1)
	}
}