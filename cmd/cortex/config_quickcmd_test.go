package main

import (
	"strings"
	"testing"
)

func TestDisableAnthropicInConfigContentRemovesAnthropicTablesAndTierEntries(t *testing.T) {
	input := `
[providers.claude-sonnet]
tier = "balanced"
model = "claude-sonnet-4-20250514"

[providers.openai]
tier = "balanced"
model = "gpt-5.3-codex"

[providers.partner]
tier = "balanced"
model = "anthropic-special-proxy"

[tiers]
fast = ["codex"]
balanced = ["codex", "claude-sonnet", "anthropic-legacy"]
premium = ["claude-opus", "codex-max"]

[dispatch.cli.claude]
cmd = "claude"

[dispatch.cli.codex]
cmd = "codex"

[chief]
model = "claude-opus-4-6"
`

	got, changed := disableAnthropicInConfigContent(input, "gpt-5.3-codex")
	if !changed {
		t.Fatal("expected config content to change")
	}

	unwanted := []string{
		"[providers.claude-sonnet]",
		`model = "claude-sonnet-4-20250514"`,
		"[providers.partner]",
		"[dispatch.cli.claude]",
		`"claude-sonnet"`,
		`"anthropic-legacy"`,
		`"claude-opus"`,
	}
	for _, value := range unwanted {
		if strings.Contains(got, value) {
			t.Fatalf("unexpected anthrophic value remained: %q\nresult:\n%s", value, got)
		}
	}

	expected := []string{
		"[providers.openai]",
		`balanced = ["codex"]`,
		`premium = ["codex-max"]`,
		`model = "gpt-5.3-codex"`,
		"[dispatch.cli.codex]",
	}
	for _, value := range expected {
		if !strings.Contains(got, value) {
			t.Fatalf("expected value missing: %q\nresult:\n%s", value, got)
		}
	}
}

func TestDisableAnthropicInConfigContentNoOpWhenNothingMatches(t *testing.T) {
	input := `
[providers.codex]
model = "gpt-5.3-codex"

[tiers]
balanced = ["codex"]
`

	got, changed := disableAnthropicInConfigContent(input, "gpt-5.3-codex")
	if changed {
		t.Fatalf("expected unchanged content, got change:\n%s", got)
	}
	if got != input {
		t.Fatalf("expected exact original output when unchanged\nwant:\n%s\ngot:\n%s", input, got)
	}
}

func TestDisableAnthropicInConfigContentDefaultsFallbackChiefModel(t *testing.T) {
	input := `
[chief]
model = "claude-sonnet-4-20250514"
`

	got, changed := disableAnthropicInConfigContent(input, "")
	if !changed {
		t.Fatal("expected chief model replacement")
	}
	if !strings.Contains(got, `model = "gpt-5.3-codex"`) {
		t.Fatalf("expected default fallback chief model, got:\n%s", got)
	}
}
