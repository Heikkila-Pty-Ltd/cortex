package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ExecutionPlanGate represents the currently active approved plan used to gate dispatches.
type ExecutionPlanGate struct {
	PlanID      string
	ApprovedBy  string
	ApprovedAt  time.Time
	ActivatedAt time.Time
	UpdatedAt   time.Time
}

// SetActiveApprovedPlan activates the given approved plan for execution.
func (s *Store) SetActiveApprovedPlan(planID, approvedBy string) error {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return fmt.Errorf("plan id is required")
	}
	approvedBy = strings.TrimSpace(approvedBy)
	if approvedBy == "" {
		approvedBy = "manual"
	}

	_, err := s.db.Exec(`
		INSERT INTO execution_plan_gate (
			id, active_plan_id, approved_by, approved_at, activated_at, updated_at
		) VALUES (
			1, ?, ?, datetime('now'), datetime('now'), datetime('now')
		)
		ON CONFLICT(id) DO UPDATE SET
			active_plan_id = excluded.active_plan_id,
			approved_by = excluded.approved_by,
			approved_at = excluded.approved_at,
			activated_at = excluded.activated_at,
			updated_at = excluded.updated_at
	`, planID, approvedBy)
	if err != nil {
		return fmt.Errorf("store: set active approved plan: %w", err)
	}
	return nil
}

// ClearActiveApprovedPlan clears the currently active plan gate.
func (s *Store) ClearActiveApprovedPlan() error {
	_, err := s.db.Exec(`
		INSERT INTO execution_plan_gate (
			id, active_plan_id, approved_by, approved_at, activated_at, updated_at
		) VALUES (
			1, '', '', NULL, NULL, datetime('now')
		)
		ON CONFLICT(id) DO UPDATE SET
			active_plan_id = excluded.active_plan_id,
			approved_by = excluded.approved_by,
			approved_at = NULL,
			activated_at = NULL,
			updated_at = excluded.updated_at
	`)
	if err != nil {
		return fmt.Errorf("store: clear active approved plan: %w", err)
	}
	return nil
}

// GetActiveApprovedPlan returns the current active approved plan, or nil when no gate is active.
func (s *Store) GetActiveApprovedPlan() (*ExecutionPlanGate, error) {
	var (
		planID      string
		approvedBy  string
		approvedAt  sql.NullTime
		activatedAt sql.NullTime
		updatedAt   time.Time
	)

	err := s.db.QueryRow(`
		SELECT active_plan_id, approved_by, approved_at, activated_at, updated_at
		FROM execution_plan_gate
		WHERE id = 1
	`).Scan(&planID, &approvedBy, &approvedAt, &activatedAt, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get active approved plan: %w", err)
	}

	planID = strings.TrimSpace(planID)
	if planID == "" || !approvedAt.Valid {
		return nil, nil
	}

	activated := approvedAt.Time
	if activatedAt.Valid {
		activated = activatedAt.Time
	}

	return &ExecutionPlanGate{
		PlanID:      planID,
		ApprovedBy:  strings.TrimSpace(approvedBy),
		ApprovedAt:  approvedAt.Time,
		ActivatedAt: activated,
		UpdatedAt:   updatedAt,
	}, nil
}

// HasActiveApprovedPlan returns whether execution currently has an active approved plan.
func (s *Store) HasActiveApprovedPlan() (bool, *ExecutionPlanGate, error) {
	plan, err := s.GetActiveApprovedPlan()
	if err != nil {
		return false, nil, err
	}
	return plan != nil, plan, nil
}
