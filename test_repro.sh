#!/bin/bash

echo "Starting race condition reproduction test..."

# Try running the test suite combination that showed the problem
for i in {1..5}; do
    echo "=== Run $i ==="
    go test ./internal/dispatch ./internal/scheduler -count=1 -run "TestTmuxDispatcher_ExitCodeCapture" -timeout=30s
    if [ $? -ne 0 ]; then
        echo "FAILURE detected on run $i"
        exit 1
    fi
done

echo "All runs passed - attempting stress test"

# Try isolated stress test
for i in {1..10}; do
    echo "=== Isolated Run $i ==="
    go test ./internal/dispatch -run "^TestTmuxDispatcher_ExitCodeCapture$" -count=1 -timeout=15s
    if [ $? -ne 0 ]; then
        echo "ISOLATED FAILURE detected on run $i"
        exit 1
    fi
done

echo "Stress test complete - no failures detected"