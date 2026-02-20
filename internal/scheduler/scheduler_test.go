package scheduler

import (
	"strings"
	"testing"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

func TestBuildPrompt(t *testing.T) {
	b := beads.Bead{
		Title:       "Fix the widget",
		Description: "The widget is broken in production.",
		Acceptance:  "Widget renders correctly on all browsers.",
		Design:      "Patch the CSS media query.",
	}

	got := buildPrompt(b)

	if got == "" {
		t.Fatal("buildPrompt returned empty string")
	}
	for _, want := range []string{
		"Fix the widget",
		"The widget is broken in production.",
		"ACCEPTANCE CRITERIA:",
		"Widget renders correctly on all browsers.",
		"DESIGN:",
		"Patch the CSS media query.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildPrompt missing %q in output:\n%s", want, got)
		}
	}
}

func TestBuildPromptMinimal(t *testing.T) {
	b := beads.Bead{Title: "Simple task"}
	got := buildPrompt(b)
	if got != "Simple task" {
		t.Errorf("expected 'Simple task', got %q", got)
	}
}

func TestResolveProvider(t *testing.T) {
	tests := []struct {
		name string
		fast []string
		want string
	}{
		{"fast tier", []string{"codex-spark", "claude"}, "codex-spark"},
		{"empty tiers", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Tiers.Fast = tt.fast
			got := resolveProvider(cfg)
			if got != tt.want {
				t.Errorf("resolveProvider() = %q, want %q", got, tt.want)
			}
		})
	}
}
