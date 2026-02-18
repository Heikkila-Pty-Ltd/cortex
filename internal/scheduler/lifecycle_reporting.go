package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type lifecycleReporter interface {
	SendProjectMessage(ctx context.Context, projectName, message string)
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
	if s == nil || s.dryRun || s.lifecycleReporter == nil || s.cfg == nil {
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

	evt.Project = project
	evt.BeadID = beadID
	s.lifecycleReporter.SendProjectMessage(ctx, project, formatLifecycleMatrixMessage(room, evt))
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

func formatLifecycleMatrixMessage(room string, evt beadLifecycleEvent) string {
	event := strings.TrimSpace(evt.Event)
	if event == "" {
		event = "bead_updated"
	}

	var b strings.Builder
	b.WriteString("# Matrix Bead Lifecycle Update\n\n")
	fmt.Fprintf(&b, "Send the following update to Matrix room `%s`:\n\n", room)
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
