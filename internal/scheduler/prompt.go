package scheduler

import (
	"regexp"
	"strings"

	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

var filePathRe = regexp.MustCompile(`(?:^|\s)((?:src|internal|cmd|pkg|lib|app|public|templates|static|test|tests|scripts)/[\w./-]+|[\w-]+\.(?:go|ts|tsx|js|jsx|py|rs|rb|java|vue|svelte|css|scss|html|sql|yaml|yml|toml|json|md|sh))`)

var roleTemplates = map[string]struct{}{
	"generic":        {},
	"scrum":          {},
	"planner":        {},
	"coder":          {},
	"reviewer":       {},
	"ops":            {},
	"sprint_review":  {},
	"sprint_retro":   {},
	"sprint_planning": {},
}

// BuildPrompt constructs the prompt sent to an openclaw agent.
func BuildPrompt(bead beads.Bead, project config.Project) string {
	return BuildPromptWithRole(bead, project, "")
}

// BuildPromptWithRole constructs a role-aware prompt sent to an openclaw agent.
func BuildPromptWithRole(bead beads.Bead, project config.Project, role string) string {
	return BuildPromptWithRoleBranches(bead, project, role, false, "")
}

// BuildPromptWithRoleBranches constructs a role-aware prompt with branch workflow support.
func BuildPromptWithRoleBranches(bead beads.Bead, project config.Project, role string, useBranches bool, prDiff string) string {
	files := extractFilePaths(bead.Description)
	shortTitle := bead.Title
	if len(shortTitle) > 50 {
		shortTitle = shortTitle[:50]
	}

	templateRole := role
	if _, ok := roleTemplates[templateRole]; !ok {
		templateRole = "generic"
	}

	return RenderPrompt(PromptData{
		Bead:        bead,
		Project:     project,
		Role:        templateRole,
		UseBranches: useBranches,
		PRDiff:      prDiff,
		ShortTitle:  shortTitle,
		Files:       files,
	})
}

func extractFilePaths(text string) []string {
	matches := filePathRe.FindAllStringSubmatch(text, -1)
	seen := make(map[string]bool)
	var paths []string
	for _, m := range matches {
		p := strings.TrimSpace(m[1])
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	return paths
}
