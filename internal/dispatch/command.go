package dispatch

import (
	"fmt"
	"strings"
)

// BuildCommand constructs an exec-compatible argv with placeholder substitution
// and validation for provider/model/prompt content.
func BuildCommand(provider, model, prompt string, flags []string) ([]string, error) {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, fmt.Errorf("command builder: provider command is required")
	}

	argv := make([]string, 0, len(flags)+3)
	argv = append(argv, provider)

	modelUsed := false
	for i, raw := range flags {
		if strings.TrimSpace(raw) == "" {
			return nil, fmt.Errorf("command builder: empty flag at index %d", i)
		}

		arg := strings.ReplaceAll(raw, "{prompt}", prompt)
		if strings.Contains(raw, "{model}") {
			if strings.TrimSpace(model) == "" {
				return nil, fmt.Errorf("command builder: model is required by flag %q", raw)
			}
			modelUsed = true
			arg = strings.ReplaceAll(arg, "{model}", model)
		}
		argv = append(argv, arg)
	}

	if strings.TrimSpace(model) != "" && !modelUsed {
		return nil, fmt.Errorf("command builder: model was provided but no model flag placeholder was configured")
	}

	return argv, nil
}
