package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func sessionStatus(sessionName string) (status string, exitCode int) {
	// Check if session is alive
	_, err := exec.Command("tmux", "has-session", "-t", sessionName).Output()
	if err != nil {
		return "gone", -1
	}

	out, err := exec.Command(
		"tmux", "display-message",
		"-t", sessionName,
		"-p", "#{pane_dead} #{pane_dead_status}",
	).Output()
	if err != nil {
		// Session exists but we cannot query it — treat as running.
		return "running", 0
	}

	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 {
		return "running", 0
	}

	paneDead := fields[0]
	if paneDead == "1" {
		code := -1
		if len(fields) >= 2 {
			code, _ = strconv.Atoi(fields[1])
		}
		return "exited", code
	}
	return "running", 0
}

func runRaceTest(iteration int) bool {
	sessionName := fmt.Sprintf("race-test-%d", iteration)
	
	// Create tmux session with very short sleep
	cmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-x", "120", "-y", "30", "sh", "-c", "sleep 0.1; exit 42")
	err := cmd.Run()
	if err != nil {
		fmt.Printf("Failed to create session %s: %v\n", sessionName, err)
		return false
	}
	
	defer exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	
	// Poll very frequently to catch the race
	start := time.Now()
	for time.Since(start) < 2*time.Second {
		status, exitCode := sessionStatus(sessionName)
		if status == "exited" {
			if exitCode != 42 {
				fmt.Printf("RACE DETECTED in iteration %d: expected exit code 42, got %d\n", iteration, exitCode)
				return false
			}
			return true
		}
		time.Sleep(1 * time.Millisecond) // Very frequent polling
	}
	
	fmt.Printf("Timeout in iteration %d\n", iteration)
	return false
}

func main() {
	failures := 0
	total := 100
	
	fmt.Printf("Running %d race tests...\n", total)
	
	for i := 0; i < total; i++ {
		if !runRaceTest(i) {
			failures++
		}
		if i%10 == 9 {
			fmt.Printf("Completed %d/%d tests, failures: %d\n", i+1, total, failures)
		}
	}
	
	fmt.Printf("Final results: %d failures out of %d tests\n", failures, total)
}