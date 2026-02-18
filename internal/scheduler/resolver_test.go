package scheduler

import (
	"testing"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/dispatch"
)

func TestDispatcherResolver_CreateDispatcher(t *testing.T) {
	tests := []struct {
		name        string
		routing     config.DispatchRouting
		wantType    string
		wantError   bool
		errorContains string
	}{
		{
			name: "tmux available - should use tmux",
			routing: config.DispatchRouting{
				FastBackend: "tmux",
			},
			wantType: "session",
		},
		{
			name: "headless_cli - should use PID dispatcher",
			routing: config.DispatchRouting{
				FastBackend: "headless_cli",
			},
			wantType: "pid",
		},
		{
			name: "pid backend - should use PID dispatcher",
			routing: config.DispatchRouting{
				FastBackend: "pid",
			},
			wantType: "pid",
		},
		{
			name: "multiple backends - should use first available",
			routing: config.DispatchRouting{
				FastBackend:     "headless_cli",
				BalancedBackend: "tmux",
			},
			wantType: "pid", // should pick headless_cli first
		},
		{
			name: "no backends configured",
			routing: config.DispatchRouting{},
			wantError: true,
			errorContains: "no dispatch backends configured",
		},
		{
			name: "unknown backend type",
			routing: config.DispatchRouting{
				FastBackend: "unknown",
			},
			wantError: true,
			errorContains: "no available dispatch backends",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Dispatch: config.Dispatch{
					Routing: tt.routing,
				},
			}
			
			resolver := NewDispatcherResolver(cfg)
			dispatcher, err := resolver.CreateDispatcher()
			
			if tt.wantError {
				if err == nil {
					t.Errorf("CreateDispatcher() expected error, got nil")
					return
				}
				if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
					t.Errorf("CreateDispatcher() error = %v, want to contain %v", err, tt.errorContains)
				}
				return
			}
			
			if err != nil {
				t.Errorf("CreateDispatcher() unexpected error = %v", err)
				return
			}
			
			if dispatcher == nil {
				t.Errorf("CreateDispatcher() returned nil dispatcher")
				return
			}
			
			if gotType := dispatcher.GetHandleType(); gotType != tt.wantType {
				t.Errorf("CreateDispatcher() handle type = %v, want %v", gotType, tt.wantType)
			}
		})
	}
}

func TestDispatcherResolver_CreateDispatcherForTier(t *testing.T) {
	tests := []struct {
		name        string
		tier        string
		routing     config.DispatchRouting
		wantType    string
		wantError   bool
		errorContains string
	}{
		{
			name: "fast tier with tmux",
			tier: "fast",
			routing: config.DispatchRouting{
				FastBackend: "tmux",
			},
			wantType: "session",
		},
		{
			name: "balanced tier with headless_cli",
			tier: "balanced",
			routing: config.DispatchRouting{
				BalancedBackend: "headless_cli",
			},
			wantType: "pid",
		},
		{
			name: "premium tier with tmux",
			tier: "premium",
			routing: config.DispatchRouting{
				PremiumBackend: "tmux",
			},
			wantType: "session",
		},
		{
			name: "unknown tier",
			tier: "unknown",
			routing: config.DispatchRouting{
				FastBackend: "tmux",
			},
			wantError: true,
			errorContains: "unknown tier: unknown",
		},
		{
			name: "tier with no configured backend",
			tier: "fast",
			routing: config.DispatchRouting{
				BalancedBackend: "headless_cli", // fast not configured; should fallback
			},
			wantType: "pid",
		},
		{
			name: "tier with unknown backend type",
			tier: "fast",
			routing: config.DispatchRouting{
				FastBackend: "unknown",
			},
			wantError: true,
			errorContains: "no available backend for tier fast",
		},
		{
			name: "tier with pid backend",
			tier: "fast",
			routing: config.DispatchRouting{
				FastBackend: "pid",
			},
			wantType: "pid",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Dispatch: config.Dispatch{
					Routing: tt.routing,
				},
			}
			
			resolver := NewDispatcherResolver(cfg)
			dispatcher, err := resolver.CreateDispatcherForTier(tt.tier)
			
			if tt.wantError {
				if err == nil {
					t.Errorf("CreateDispatcherForTier() expected error, got nil")
					return
				}
				if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
					t.Errorf("CreateDispatcherForTier() error = %v, want to contain %v", err, tt.errorContains)
				}
				return
			}
			
			if err != nil {
				t.Errorf("CreateDispatcherForTier() unexpected error = %v", err)
				return
			}
			
			if dispatcher == nil {
				t.Errorf("CreateDispatcherForTier() returned nil dispatcher")
				return
			}
			
			if gotType := dispatcher.GetHandleType(); gotType != tt.wantType {
				t.Errorf("CreateDispatcherForTier() handle type = %v, want %v", gotType, tt.wantType)
			}
		})
	}
}

func TestDispatcherResolver_ValidateConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		routing     config.DispatchRouting
		wantError   bool
		errorContains string
	}{
		{
			name: "valid configuration",
			routing: config.DispatchRouting{
				FastBackend:     "headless_cli",
				BalancedBackend: "headless_cli",
				PremiumBackend:  "headless_cli",
			},
			wantError: false,
		},
		{
			name: "valid mixed configuration",
			routing: config.DispatchRouting{
				FastBackend:     "headless_cli",
				BalancedBackend: "tmux",
			},
			wantError: false,
		},
		{
			name: "no backends configured",
			routing: config.DispatchRouting{},
			wantError: true,
			errorContains: "no dispatch backends configured",
		},
		{
			name: "unknown backend type",
			routing: config.DispatchRouting{
				FastBackend: "unknown",
			},
			wantError: true,
			errorContains: "dispatch backend validation failed",
		},
		{
			name: "valid comms and retry backends",
			routing: config.DispatchRouting{
				FastBackend:  "headless_cli",
				CommsBackend: "tmux",
				RetryBackend: "headless_cli",
			},
			wantError: false,
		},
		{
			name: "valid pid backend",
			routing: config.DispatchRouting{
				FastBackend: "pid",
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				Dispatch: config.Dispatch{
					Routing: tt.routing,
				},
			}
			
			resolver := NewDispatcherResolver(cfg)
			err := resolver.ValidateConfiguration()
			
			if tt.wantError {
				if err == nil {
					t.Errorf("ValidateConfiguration() expected error, got nil")
					return
				}
				if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
					t.Errorf("ValidateConfiguration() error = %v, want to contain %v", err, tt.errorContains)
				}
				return
			}
			
			if err != nil {
				t.Errorf("ValidateConfiguration() unexpected error = %v", err)
			}
		})
	}
}

func TestDispatcherResolver_createDispatcherForBackend(t *testing.T) {
	cfg := &config.Config{}
	resolver := NewDispatcherResolver(cfg)

	tests := []struct {
		name        string
		backend     string
		wantType    string
		wantError   bool
		errorContains string
	}{
		{
			name:     "headless_cli backend",
			backend:  "headless_cli",
			wantType: "pid",
		},
		{
			name:     "pid backend",
			backend:  "pid",
			wantType: "pid",
		},
		{
			name:     "tmux backend",
			backend:  "tmux",
			wantType: "session",
		},
		{
			name:        "unknown backend",
			backend:     "unknown",
			wantError:   true,
			errorContains: "unknown backend type: unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dispatcher, err := resolver.createDispatcherForBackend(tt.backend)
			
			if tt.wantError {
				if err == nil {
					t.Errorf("createDispatcherForBackend() expected error, got nil")
					return
				}
				if tt.errorContains != "" && !containsString(err.Error(), tt.errorContains) {
					t.Errorf("createDispatcherForBackend() error = %v, want to contain %v", err, tt.errorContains)
				}
				return
			}
			
			if err != nil {
				t.Errorf("createDispatcherForBackend() unexpected error = %v", err)
				return
			}
			
			if dispatcher == nil {
				t.Errorf("createDispatcherForBackend() returned nil dispatcher")
				return
			}
			
			if gotType := dispatcher.GetHandleType(); gotType != tt.wantType {
				t.Errorf("createDispatcherForBackend() handle type = %v, want %v", gotType, tt.wantType)
			}
		})
	}
}

func TestDispatcherResolver_Integration(t *testing.T) {
	// Integration test that verifies config-driven selection works end-to-end
	t.Run("config drives dispatcher selection", func(t *testing.T) {
		// Test with headless_cli preference
		cfg1 := &config.Config{
			Dispatch: config.Dispatch{
				Routing: config.DispatchRouting{
					FastBackend: "headless_cli",
				},
			},
		}
		
		resolver1 := NewDispatcherResolver(cfg1)
		dispatcher1, err := resolver1.CreateDispatcher()
		
		if err != nil {
			t.Fatalf("Integration test failed to create headless_cli dispatcher: %v", err)
		}
		
		if dispatcher1.GetHandleType() != "pid" {
			t.Errorf("Integration test: expected PID dispatcher, got %s", dispatcher1.GetHandleType())
		}
		
		// Test with tmux preference (if available)
		if dispatch.IsTmuxAvailable() {
			cfg2 := &config.Config{
				Dispatch: config.Dispatch{
					Routing: config.DispatchRouting{
						FastBackend: "tmux",
					},
				},
			}
			
			resolver2 := NewDispatcherResolver(cfg2)
			dispatcher2, err := resolver2.CreateDispatcher()
			
			if err != nil {
				t.Fatalf("Integration test failed to create tmux dispatcher: %v", err)
			}
			
			if dispatcher2.GetHandleType() != "session" {
				t.Errorf("Integration test: expected session dispatcher, got %s", dispatcher2.GetHandleType())
			}
		}
	})
}

// Helper function to check if a string contains a substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (len(substr) == 0 || s[:len(substr)] == substr || 
		containsString(s[1:], substr))
}
