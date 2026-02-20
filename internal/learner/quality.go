package learner

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/antigravity-dev/cortex/internal/beads"
)

var (
	testPassPattern   = regexp.MustCompile(`(?im)^\s*(PASS|ok)\b`)
	testFailPattern   = regexp.MustCompile(`(?im)^\s*FAIL\b`)
	commitPattern     = regexp.MustCompile(`(?im)\b(?:git\s+commit|committed)\b`)
	bdClosePattern    = regexp.MustCompile(`(?im)\bbd\s+close\b`)
	filesChangedRe    = regexp.MustCompile(`(?im)(\d+)\s+files?\s+changed`)
	insertionsRe      = regexp.MustCompile(`(?i)\b(\d+)\s+insertions?\(\+\)`)
	deletionsRe       = regexp.MustCompile(`(?i)\b(\d+)\s+deletions?\(-\)`)
)

// QualityScore captures objective quality metrics for a completed dispatch.
type QualityScore struct {
	DispatchID    int
	Overall       float64  // 0.0-1.0
	TestsPassed   *bool    // did tests pass? nil if no tests detected
	BeadClosed    bool     // did agent close the bead?
	CommitMade    bool     // did agent make a git commit?
	FilesChanged  int      // files touched
	LinesChanged  int      // net lines changed
	Duration      float64  // seconds (shorter = better, within reason)
}

// ScoreDispatch analyzes output and bead state to build an objective quality score.
func ScoreDispatch(output string, workspace string, beadID string) (*QualityScore, error) {
	score := &QualityScore{
		Overall:      0.0,
		FilesChanged: 0,
		LinesChanged: 0,
	}

	normalizedOutput := strings.TrimSpace(output)
	if normalizedOutput == "" {
		score.Overall = 0.5
		return score, nil
	}

	score.TestsPassed = testsFromOutput(normalizedOutput)
	score.CommitMade = commitPattern.MatchString(normalizedOutput)
	score.BeadClosed = beadClosedFromOutputOrBeadState(normalizedOutput, workspace, beadID)
	score.FilesChanged, score.LinesChanged = diffStatsFromOutput(normalizedOutput)
	score.Overall = qualityOverall(score.TestsPassed, score.BeadClosed, score.CommitMade, score.FilesChanged, score.LinesChanged)

	return score, nil
}

func testsFromOutput(output string) *bool {
	hasFail := testFailPattern.MatchString(output)
	hasPass := testPassPattern.MatchString(output)
	if !hasPass && !hasFail {
		return nil
	}
	passed := hasPass && !hasFail
	return &passed
}

func beadClosedFromOutputOrBeadState(output string, workspace string, beadID string) bool {
	if bdClosePattern.MatchString(output) {
		return true
	}

	workspace = strings.TrimSpace(workspace)
	beadID = strings.TrimSpace(beadID)
	if workspace == "" || beadID == "" {
		return false
	}

	detail, err := beads.ShowBead(workspace, beadID)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(detail.Status), "closed")
}

func diffStatsFromOutput(output string) (int, int) {
	var files, insertions, deletions int

	for _, line := range strings.Split(output, "\n") {
		if m := filesChangedRe.FindStringSubmatch(line); len(m) > 1 {
			if v, err := strconv.Atoi(strings.TrimSpace(m[1])); err == nil {
				files += v
			}
		}
		if m := insertionsRe.FindStringSubmatch(line); len(m) > 1 {
			if v, err := strconv.Atoi(strings.TrimSpace(m[1])); err == nil {
				insertions += v
			}
		}
		if m := deletionsRe.FindStringSubmatch(line); len(m) > 1 {
			if v, err := strconv.Atoi(strings.TrimSpace(m[1])); err == nil {
				deletions += v
			}
		}
	}

	return files, insertions - deletions
}

func qualityOverall(testsPassed *bool, beadClosed, commitMade bool, filesChanged int, linesChanged int) float64 {
	var totalScore float64
	var totalWeight float64

	if testsPassed != nil {
		totalScore += boolToScore(*testsPassed) * 0.45
		totalWeight += 0.45
	}

	totalScore += boolToScore(beadClosed) * 0.2
	totalWeight += 0.2

	totalScore += boolToScore(commitMade) * 0.15
	totalWeight += 0.15

	totalScore += normalizeFilesChanged(filesChanged) * 0.1
	totalWeight += 0.1

	totalScore += normalizeLinesChanged(linesChanged) * 0.1
	totalWeight += 0.1

	if totalWeight == 0 {
		return 0.5
	}

	return qualityClamp01(totalScore / totalWeight)
}

func normalizeFilesChanged(filesChanged int) float64 {
	return qualityClamp01(float64(filesChanged) / 10.0)
}

func normalizeLinesChanged(linesChanged int) float64 {
	return qualityClamp01(math.Abs(float64(linesChanged)) / 20.0)
}

func boolToScore(v bool) float64 {
	if v {
		return 1.0
	}
	return 0.0
}

func qualityClamp01(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func (q *QualityScore) MarshalJSON() ([]byte, error) {
	type Alias QualityScore
	return json.Marshal((*Alias)(q))
}
