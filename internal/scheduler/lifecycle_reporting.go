package scheduler

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var matrixRetryAfterMSRe = regexp.MustCompile(`retry_after_ms["'=:\s]*([0-9]+)`)

type lifecycleReporter interface {
	SendProjectMessage(ctx context.Context, projectName, message string)
}

type lifecycleMatrixSender interface {
	SendMessage(ctx context.Context, roomID, message string) error
}

type beadLifecycleEvent struct {
	Project       string
	BeadID        string
	DispatchID    int64
	Event         string
	WorkflowStage string
	DispatchStage string
	Status        string
	AgentID       string
	Provider      string
	Tier          string
	ExitCode      int
	DurationS     float64
	Note          string
}

func (s *Scheduler) reportBeadLifecycle(ctx context.Context, evt beadLifecycleEvent) {
	if s == nil || s.dryRun || s.cfg == nil {
		return
	}
	if s.lifecycleMatrixSender == nil && s.lifecycleReporter == nil {
		return
	}

	project := strings.TrimSpace(evt.Project)
	beadID := strings.TrimSpace(evt.BeadID)
	if project == "" || beadID == "" {
		return
	}

	room := strings.TrimSpace(s.cfg.ResolveRoom(project))
	if room == "" {
		return
	}
	now := time.Now()
	if remaining, blocked := s.lifecycleBackoffRemaining(room, now); blocked {
		if s.shouldLogLifecycleRateLimit(room, now) && s.logger != nil {
			s.logger.Warn("matrix lifecycle send suppressed due to active rate-limit backoff",
				"project", project,
				"bead", beadID,
				"room", room,
				"retry_in", remaining.String(),
			)
		}
		return
	}

	evt.Project = project
	evt.BeadID = beadID
	notification := formatLifecycleNotification(evt)

	if s.lifecycleMatrixSender != nil {
		if err := s.lifecycleMatrixSender.SendMessage(ctx, room, notification); err == nil {
			return
		} else if retryAfter, limited := lifecycleRateLimitRetryAfter(err); limited {
			until := now.Add(retryAfter)
			s.setLifecycleBackoff(room, until)
			if s.shouldLogLifecycleRateLimit(room, now) && s.logger != nil {
				s.logger.Warn("matrix lifecycle send rate-limited; applying backoff",
					"project", project,
					"bead", beadID,
					"room", room,
					"retry_after", retryAfter.String(),
				)
			}
			if s.store != nil {
				_ = s.store.RecordHealthEventWithDispatch(
					"matrix_lifecycle_rate_limited",
					fmt.Sprintf("project %s bead %s lifecycle message rate-limited for room %s; backing off for %s", project, beadID, room, retryAfter),
					evt.DispatchID,
					beadID,
				)
			}
			// Avoid fallback dispatch churn while Matrix explicitly asks us to retry later.
			return
		} else if s.logger != nil {
			s.logger.Warn("failed direct matrix lifecycle send; falling back to reporter dispatch",
				"project", project,
				"bead", beadID,
				"room", room,
				"error", err)
		}
	}
	if s.lifecycleReporter != nil {
		s.lifecycleReporter.SendProjectMessage(ctx, project, formatLifecycleMatrixAgentPrompt(room, notification))
	}
}

func (s *Scheduler) lifecycleBackoffRemaining(room string, now time.Time) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	until, ok := s.lifecycleRateLimitUntil[room]
	if !ok {
		return 0, false
	}
	if !until.After(now) {
		delete(s.lifecycleRateLimitUntil, room)
		return 0, false
	}
	return until.Sub(now), true
}

func (s *Scheduler) setLifecycleBackoff(room string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lifecycleRateLimitUntil == nil {
		s.lifecycleRateLimitUntil = make(map[string]time.Time)
	}
	if existing, ok := s.lifecycleRateLimitUntil[room]; ok && existing.After(until) {
		return
	}
	s.lifecycleRateLimitUntil[room] = until
}

func (s *Scheduler) shouldLogLifecycleRateLimit(room string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.lifecycleRateLimitLog == nil {
		s.lifecycleRateLimitLog = make(map[string]time.Time)
	}
	last, ok := s.lifecycleRateLimitLog[room]
	if ok && now.Sub(last) < lifecycleRateLimitLogWindow {
		return false
	}
	s.lifecycleRateLimitLog[room] = now
	return true
}

func lifecycleRateLimitRetryAfter(err error) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	isRateLimited := strings.Contains(lower, "m_limit_exceeded") ||
		strings.Contains(lower, "too many requests") ||
		strings.Contains(lower, "status 429")
	if !isRateLimited {
		return 0, false
	}

	retry := 5 * time.Second
	if m := matrixRetryAfterMSRe.FindStringSubmatch(msg); len(m) == 2 {
		if ms, convErr := strconv.Atoi(m[1]); convErr == nil && ms > 0 {
			retry = time.Duration(ms) * time.Millisecond
		}
	}
	if retry < lifecycleRateLimitMinBackoff {
		retry = lifecycleRateLimitMinBackoff
	}
	if retry > lifecycleRateLimitMaxBackoff {
		retry = lifecycleRateLimitMaxBackoff
	}
	return retry, true
}

func lifecycleEventForDispatchStatus(status string) string {
	switch strings.TrimSpace(status) {
	case "completed":
		return "dispatch_completed"
	case "failed":
		return "dispatch_failed"
	case "pending_retry":
		return "dispatch_retry_queued"
	case "retried":
		return "dispatch_retried"
	case "cancelled":
		return "dispatch_cancelled"
	case "interrupted":
		return "dispatch_interrupted"
	default:
		return "dispatch_status_changed"
	}
}

func workflowStageFromLabelsCSV(labelsCSV string) string {
	if strings.TrimSpace(labelsCSV) == "" {
		return ""
	}
	for _, raw := range strings.Split(labelsCSV, ",") {
		label := strings.TrimSpace(raw)
		if strings.HasPrefix(strings.ToLower(label), "stage:") {
			return label
		}
	}
	return ""
}

func formatLifecycleNotification(evt beadLifecycleEvent) string {
	event := strings.TrimSpace(evt.Event)
	if event == "" {
		event = "bead_updated"
	}

	var b strings.Builder
	b.WriteString("## Bead Lifecycle Update\n\n")
	fmt.Fprintf(&b, "- **Project:** `%s`\n", evt.Project)
	fmt.Fprintf(&b, "- **Bead:** `%s`\n", evt.BeadID)
	fmt.Fprintf(&b, "- **Event:** `%s`\n", event)
	if stage := strings.TrimSpace(evt.WorkflowStage); stage != "" {
		fmt.Fprintf(&b, "- **Workflow Stage:** `%s`\n", stage)
	}
	if stage := strings.TrimSpace(evt.DispatchStage); stage != "" {
		fmt.Fprintf(&b, "- **Dispatch Stage:** `%s`\n", stage)
	}
	if status := strings.TrimSpace(evt.Status); status != "" {
		fmt.Fprintf(&b, "- **Status:** `%s`\n", status)
	}
	if evt.DispatchID > 0 {
		fmt.Fprintf(&b, "- **Dispatch ID:** `%d`\n", evt.DispatchID)
	}
	if agent := strings.TrimSpace(evt.AgentID); agent != "" {
		fmt.Fprintf(&b, "- **Agent:** `%s`\n", agent)
	}
	if provider := strings.TrimSpace(evt.Provider); provider != "" {
		fmt.Fprintf(&b, "- **Provider:** `%s`\n", provider)
	}
	if tier := strings.TrimSpace(evt.Tier); tier != "" {
		fmt.Fprintf(&b, "- **Tier:** `%s`\n", tier)
	}
	if evt.DurationS > 0 {
		fmt.Fprintf(&b, "- **Duration:** `%.1fs`\n", evt.DurationS)
	}
	if evt.ExitCode != 0 {
		fmt.Fprintf(&b, "- **Exit Code:** `%d`\n", evt.ExitCode)
	}
	fmt.Fprintf(&b, "- **Time (UTC):** `%s`\n", time.Now().UTC().Format(time.RFC3339))
	if note := strings.TrimSpace(evt.Note); note != "" {
		fmt.Fprintf(&b, "\nNote: %s\n", note)
	}

	return b.String()
}

func formatLifecycleMatrixAgentPrompt(room string, notification string) string {
	var b strings.Builder
	b.WriteString("# Matrix Bead Lifecycle Update\n\n")
	fmt.Fprintf(&b, "Send the following update to Matrix room `%s`:\n\n", strings.TrimSpace(room))
	b.WriteString(strings.TrimSpace(notification))
	b.WriteString("\n")
	return b.String()
}
