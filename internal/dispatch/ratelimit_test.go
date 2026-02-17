package dispatch

import (
	"path/filepath"
	"testing"

	"github.com/antigravity-dev/cortex/internal/config"
	"github.com/antigravity-dev/cortex/internal/store"
)

func tempStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open failed: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testProviders() map[string]config.Provider {
	return map[string]config.Provider{
		"cerebras":     {Tier: "fast", Authed: false, Model: "llama"},
		"groq":         {Tier: "fast", Authed: false, Model: "llama"},
		"claude-max20": {Tier: "balanced", Authed: true, Model: "claude"},
		"google-pro":   {Tier: "premium", Authed: true, Model: "gemini"},
	}
}

func testTiers() config.Tiers {
	return config.Tiers{
		Fast:     []string{"cerebras", "groq"},
		Balanced: []string{"claude-max20"},
		Premium:  []string{"google-pro"},
	}
}

func TestCanDispatchAuthed_UnderCap(t *testing.T) {
	s := tempStore(t)
	rl := NewRateLimiter(s, config.RateLimits{Window5hCap: 20, WeeklyCap: 200, WeeklyHeadroomPct: 80})

	ok, reason := rl.CanDispatchAuthed()
	if !ok {
		t.Errorf("should be allowed: %s", reason)
	}
}

func TestCanDispatchAuthed_5hCapReached(t *testing.T) {
	s := tempStore(t)
	rl := NewRateLimiter(s, config.RateLimits{Window5hCap: 3, WeeklyCap: 200, WeeklyHeadroomPct: 80})

	for i := 0; i < 3; i++ {
		s.RecordProviderUsage("claude", "agent", "bead")
	}

	ok, _ := rl.CanDispatchAuthed()
	if ok {
		t.Error("should be blocked by 5h cap")
	}
}

func TestCanDispatchAuthed_WeeklyCapReached(t *testing.T) {
	s := tempStore(t)
	rl := NewRateLimiter(s, config.RateLimits{Window5hCap: 100, WeeklyCap: 5, WeeklyHeadroomPct: 80})

	for i := 0; i < 5; i++ {
		s.RecordProviderUsage("claude", "agent", "bead")
	}

	ok, _ := rl.CanDispatchAuthed()
	if ok {
		t.Error("should be blocked by weekly cap")
	}
}

func TestHeadroomWarning(t *testing.T) {
	s := tempStore(t)
	rl := NewRateLimiter(s, config.RateLimits{Window5hCap: 100, WeeklyCap: 10, WeeklyHeadroomPct: 80})

	// 8 out of 10 = 80% -> should trigger
	for i := 0; i < 8; i++ {
		s.RecordProviderUsage("claude", "agent", "bead")
	}

	if !rl.IsInHeadroomWarning() {
		t.Error("should be in headroom warning at 80%")
	}

	pct := rl.WeeklyUsagePct()
	if pct != 80.0 {
		t.Errorf("WeeklyUsagePct = %f, want 80.0", pct)
	}
}

func TestPickProvider_FastTier(t *testing.T) {
	s := tempStore(t)
	rl := NewRateLimiter(s, config.RateLimits{Window5hCap: 0, WeeklyCap: 0, WeeklyHeadroomPct: 80})

	// Even with zero caps (authed blocked), fast tier should work (free providers)
	p := rl.PickProvider("fast", testProviders(), testTiers())
	if p == nil {
		t.Fatal("should return a free-tier provider")
	}
	if p.Authed {
		t.Error("fast tier should return free provider")
	}
}

func TestPickProvider_AuthedBlocked(t *testing.T) {
	s := tempStore(t)
	// Set caps to 0 to block all authed
	rl := NewRateLimiter(s, config.RateLimits{Window5hCap: 0, WeeklyCap: 0, WeeklyHeadroomPct: 80})

	p := rl.PickProvider("balanced", testProviders(), testTiers())
	if p != nil {
		t.Error("should return nil when authed is blocked")
	}
}

func TestPickProvider_AuthedAllowed(t *testing.T) {
	s := tempStore(t)
	rl := NewRateLimiter(s, config.RateLimits{Window5hCap: 20, WeeklyCap: 200, WeeklyHeadroomPct: 80})

	p := rl.PickProvider("balanced", testProviders(), testTiers())
	if p == nil {
		t.Fatal("should return an authed provider")
	}
	if !p.Authed {
		t.Error("balanced tier should return authed provider")
	}
}

func TestDowngradeTier(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"premium", "balanced"},
		{"balanced", "fast"},
		{"fast", ""},
	}
	for _, tt := range tests {
		got := DowngradeTier(tt.in)
		if got != tt.want {
			t.Errorf("DowngradeTier(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
