package scheduler

import (
	"context"
	"errors"
	"testing"
)

func TestIsStoreUnavailableError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "context canceled", err: context.Canceled, want: true},
		{name: "context deadline", err: context.DeadlineExceeded, want: true},
		{name: "database closed", err: errors.New("store: check bead dispatched: sql: database is closed"), want: true},
		{name: "connection closed", err: errors.New("store: write failed: connection is already closed"), want: true},
		{name: "other", err: errors.New("permission denied"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStoreUnavailableError(tt.err); got != tt.want {
				t.Fatalf("isStoreUnavailableError() = %v, want %v", got, tt.want)
			}
		})
	}
}
