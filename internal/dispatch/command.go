package dispatch

import (
	"fmt"
	"regexp"
	"strings"
)

var supportedPlaceholders = map[string]struct{}{
	"{prompt}":      {},
	"{prompt_file}": {},
	"{model}":       {},
}

var placeholderMatcher = regexp.MustCompile(`\{[^}]+\}`)

// BuildCommand constructs an exec-compatible argv with placeholder substitution
// and validation for provider/model/prompt content.
func BuildCommand(provider, model, prompt string, flags []string) ([]string, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, fmt.Errorf("command builder: provider command is required")
	}

	model = strings.TrimSpace(model)
	if len(flags) == 0 {
		return []string{provider}, nil
	}

	argv := make([]string, 0, len(flags)+3)
	argv = append(argv, provider)

	modelUsed := false
	for i, raw := range flags {
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("command builder: empty flag at index %d", i)
		}

		if err := validatePlaceholders(raw); err != nil {
			return nil, fmt.Errorf("command builder: %w", err)
		}

		arg := strings.ReplaceAll(raw, "{prompt}", prompt)
		if strings.Contains(raw, "{model}") {
			if model == "" {
				return nil, fmt.Errorf("command builder: model is required by flag %q", raw)
			}
			modelUsed = true
			arg = strings.ReplaceAll(arg, "{model}", model)
		}
		argv = append(argv, arg)
	}

	if model != "" && !modelUsed {
		return nil, fmt.Errorf("command builder: model was provided but no model flag placeholder was configured")
	}

	return argv, nil
}

func validatePlaceholders(raw string) error {
	matches := placeholderMatcher.FindAllString(raw, -1)
	for _, match := range matches {
		if _, ok := supportedPlaceholders[match]; !ok {
			return fmt.Errorf("unsupported placeholder %q in flag %q", match, raw)
		}
	}
	return nil
}
