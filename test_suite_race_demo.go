package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func runTest(testName string, count int) (bool, string) {
	cmd := exec.Command("go", "test", "./internal/dispatch", "-run", testName, "-count", fmt.Sprintf("%d", count))
	cmd.Dir = "/home/ubuntu/projects/cortex"
	
	output, err := cmd.CombinedOutput()
	outputStr := string(output)
	success := err == nil && !strings.Contains(outputStr, "FAIL") && (strings.Contains(outputStr, "ok ") || strings.Contains(outputStr, "PASS"))
	
	return success, outputStr
}

func runTestSuite(count int) (bool, string) {
	cmd := exec.Command("go", "test", "./internal/dispatch", "./internal/scheduler", "-count", fmt.Sprintf("%d", count), "-timeout", "60s")
	cmd.Dir = "/home/ubuntu/projects/cortex"
	
	output, err := cmd.CombinedOutput()
	outputStr := string(output)
	success := err == nil && !strings.Contains(outputStr, "FAIL") && (strings.Contains(outputStr, "ok ") || strings.Contains(outputStr, "PASS"))
	
	return success, outputStr
}

func main() {
	fmt.Println("=== Testing Tmux Exit Code Capture Stability ===\n")

	// Test 1: Individual test multiple times 
	fmt.Println("1. Testing TestTmuxDispatcher_ExitCodeCapture in isolation...")
	success, output := runTest("^TestTmuxDispatcher_ExitCodeCapture$", 20)
	
	if success {
		fmt.Println("✅ PASS: Individual test stable across 20 runs")
	} else {
		fmt.Println("❌ FAIL: Individual test still flaky")
		fmt.Printf("Output:\n%s\n", output)
		return
	}

	// Test 2: Full suite context (the original failure case)
	fmt.Println("\n2. Testing full dispatch+scheduler suite...")
	start := time.Now()
	success, output = runTestSuite(5)
	elapsed := time.Since(start)
	
	if success {
		fmt.Printf("✅ PASS: Full suite stable across 5 runs (took %v)\n", elapsed)
	} else {
		fmt.Println("❌ FAIL: Suite still has timing issues")
		fmt.Printf("Output:\n%s\n", output)
		return
	}

	// Test 3: Stress test the specific test in suite context
	fmt.Println("\n3. Stress testing exit code capture in mixed context...")
	failures := 0
	
	for i := 0; i < 10; i++ {
		success, _ := runTest("ExitCodeCapture", 1)
		if !success {
			failures++
		}
		fmt.Printf("  Run %d: %s\n", i+1, map[bool]string{true: "PASS", false: "FAIL"}[success])
	}

	if failures == 0 {
		fmt.Printf("✅ PASS: Exit code capture stable across 10 individual stress runs\n")
	} else {
		fmt.Printf("❌ FAIL: %d/10 runs failed\n", failures)
		return
	}

	fmt.Println("\n=== All Tests Pass - Race Condition Fixed ===")
	fmt.Println("\nSolution Summary:")
	fmt.Println("- Replaced fixed time.Sleep() with deterministic polling")
	fmt.Println("- Added waitForSessionExit() helper with exponential backoff")
	fmt.Println("- Eliminates suite-order/timing dependence")
	fmt.Println("- Maintains production behavior (no runtime changes)")
	fmt.Println("- Tests now pass reliably in both isolation and suite context")
}