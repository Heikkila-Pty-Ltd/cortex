package scheduler

import (
	"github.com/antigravity-dev/cortex/internal/beads"
	"github.com/antigravity-dev/cortex/internal/config"
)

type PromptData struct {
	Bead       beads.Bead
	Project    config.Project
	Role       string
	UseBranches bool
	PRDiff     string
	ShortTitle string
	Files      []string
}
