package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func debugSessionStatus(sessionName string) {
	fmt.Printf("=== Debugging session %s ===\n", sessionName)
	
	// Check if session is alive
	_, err := exec.Command("tmux", "has-session", "-t", sessionName).Output()
	fmt.Printf("Session alive check: %v\n", err == nil)
	
	// Get the raw tmux output
	out, err := exec.Command(
		"tmux", "display-message",
		"-t", sessionName,
		"-p", "#{pane_dead} #{pane_dead_status}",
	).Output()
	
	fmt.Printf("Raw tmux output: %q (err: %v)\n", string(out), err)
	
	if err == nil {
		fields := strings.Fields(strings.TrimSpace(string(out)))
		fmt.Printf("Fields: %v (len=%d)\n", fields, len(fields))
		
		if len(fields) > 0 {
			fmt.Printf("pane_dead: %q\n", fields[0])
		}
		if len(fields) > 1 {
			fmt.Printf("pane_dead_status: %q\n", fields[1])
		}
	}
	
	fmt.Printf("=== End debug ===\n\n")
}

func main() {
	sessionName := "debug-exit-test"
	
	// Create tmux session with command that exits with code 42
	cmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-x", "120", "-y", "30", "sh", "-c", "sleep 0.3; exit 42")
	cmd.Run()
	
	fmt.Printf("Created session %s\n", sessionName)
	
	// Poll session status over time
	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		fmt.Printf("--- Poll %d (t=%dms) ---\n", i+1, (i+1)*100)
		debugSessionStatus(sessionName)
	}
	
	// Clean up
	exec.Command("tmux", "kill-session", "-t", sessionName).Run()
}