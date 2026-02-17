package main

import (
	"fmt"
	"time"
)

// This simulates the improved test logic
func testTmuxDispatcher_ExitCodeCaptureImproved() {
	fmt.Println("Starting improved test simulation...")

	// Simulate dispatching
	sessionName := "test-session"
	fmt.Printf("Dispatched command to session: %s\n", sessionName)

	// Instead of fixed sleep, poll with timeout
	deadline := time.Now().Add(5 * time.Second)
	var status string
	var exitCode int

	for time.Now().Before(deadline) {
		// Simulate SessionStatus call
		status, exitCode = simulateSessionStatus()
		
		fmt.Printf("Poll: status=%s, exitCode=%d\n", status, exitCode)
		
		if status == "exited" {
			break
		}
		if status == "gone" {
			fmt.Println("ERROR: Session disappeared")
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	if status != "exited" {
		fmt.Printf("ERROR: Timeout waiting for exit, final status: %s\n", status)
		return
	}
	if exitCode != 42 {
		fmt.Printf("ERROR: Expected exit code 42, got %d\n", exitCode)
		return
	}

	fmt.Println("SUCCESS: Test passed with polling approach")
}

// Simulate the timing behavior of SessionStatus
var pollCount = 0
func simulateSessionStatus() (string, int) {
	pollCount++
	
	// Simulate the race condition - first few polls might return "running"
	// even though the command has finished
	if pollCount < 3 {
		return "running", 0
	}
	
	// Eventually return the correct status
	return "exited", 42
}

func main() {
	testTmuxDispatcher_ExitCodeCaptureImproved()
}