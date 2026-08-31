// Package app coordinates revalidation and ordered execution of pure plans.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/koopycat/cf-redirect/internal/cloudflare"
	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
)

// RedirectAPI is the focused Cloudflare surface needed by the application.
type RedirectAPI interface {
	ListItems(ctx context.Context, accountID, listID string) ([]domain.Redirect, error)
	CreateItems(ctx context.Context, accountID, listID string, redirects []domain.Redirect) (cloudflare.BulkOperation, error)
	DeleteItems(ctx context.Context, accountID, listID string, ids []string) (cloudflare.BulkOperation, error)
	WaitBulkOperation(ctx context.Context, accountID, operationID string, interval time.Duration) (cloudflare.BulkOperation, error)
}

type Executor struct {
	API          RedirectAPI
	AccountID    string
	ListID       string
	PollInterval time.Duration
}

type Phase string

const (
	DeletePhase Phase = "delete"
	CreatePhase Phase = "create"
)

type PhaseResult struct {
	Phase     Phase
	Requested int
	Operation cloudflare.BulkOperation
	Completed bool
	Err       error
}

// Report deliberately retains successful earlier phases when a later phase
// fails, allowing callers to describe partial application accurately.
type Report struct {
	Revalidated bool
	Phases      []PhaseResult
}

func (r Report) Partial() bool {
	succeeded := false
	failed := false
	for _, phase := range r.Phases {
		succeeded = succeeded || phase.Completed
		failed = failed || phase.Err != nil
	}
	return succeeded && failed
}

type ExecutionError struct {
	Report Report
	Err    error
}

func (e *ExecutionError) Error() string {
	if e.Report.Partial() {
		return "plan partially applied: " + e.Err.Error()
	}
	return "apply plan: " + e.Err.Error()
}
func (e *ExecutionError) Unwrap() error { return e.Err }

// Apply fetches fresh list state, proves that the planned assumptions still
// hold, then deletes explicit deletions and old update entries. It waits for
// that operation before creating additions and update replacements.
func (e Executor) Apply(ctx context.Context, plan planner.Plan) (Report, error) {
	report := Report{}
	if e.API == nil {
		return report, fmt.Errorf("redirect API is required")
	}
	if e.AccountID == "" || e.ListID == "" {
		return report, fmt.Errorf("account ID and list ID are required")
	}
	if plan.Empty() {
		report.Revalidated = true
		return report, nil
	}
	current, err := e.API.ListItems(ctx, e.AccountID, e.ListID)
	if err != nil {
		return report, &ExecutionError{Report: report, Err: fmt.Errorf("revalidate list: %w", err)}
	}
	if err := Revalidate(plan, current); err != nil {
		return report, &ExecutionError{Report: report, Err: err}
	}
	report.Revalidated = true

	deleteIDs, creates := split(plan)
	if len(deleteIDs) > 0 {
		result := PhaseResult{Phase: DeletePhase, Requested: len(deleteIDs)}
		operation, err := e.API.DeleteItems(ctx, e.AccountID, e.ListID, deleteIDs)
		result.Operation = operation
		if err == nil {
			operation, err = e.API.WaitBulkOperation(ctx, e.AccountID, operation.ID, e.PollInterval)
			result.Operation = operation
		}
		if err != nil {
			result.Err = err
			report.Phases = append(report.Phases, result)
			return report, &ExecutionError{Report: report, Err: fmt.Errorf("delete phase: %w", err)}
		}
		result.Completed = true
		report.Phases = append(report.Phases, result)
	}

	if len(creates) > 0 {
		result := PhaseResult{Phase: CreatePhase, Requested: len(creates)}
		operation, err := e.API.CreateItems(ctx, e.AccountID, e.ListID, creates)
		result.Operation = operation
		if err == nil {
			operation, err = e.API.WaitBulkOperation(ctx, e.AccountID, operation.ID, e.PollInterval)
			result.Operation = operation
		}
		if err != nil {
			result.Err = err
			report.Phases = append(report.Phases, result)
			return report, &ExecutionError{Report: report, Err: fmt.Errorf("create phase: %w", err)}
		}
		result.Completed = true
		report.Phases = append(report.Phases, result)
	}
	return report, nil
}

// Revalidate detects stale plans and malformed externally constructed plans.
func Revalidate(plan planner.Plan, current []domain.Redirect) error {
	byID := make(map[string]domain.Redirect, len(current))
	bySource := make(map[string]domain.Redirect, len(current))
	for _, item := range current {
		if item.ID == "" {
			return fmt.Errorf("revalidation failed: current item %q has no ID", item.Source)
		}
		if _, exists := byID[item.ID]; exists {
			return fmt.Errorf("revalidation failed: duplicate current item ID %q", item.ID)
		}
		if _, exists := bySource[item.Source]; exists {
			return fmt.Errorf("revalidation failed: duplicate current source %q", item.Source)
		}
		byID[item.ID] = item
		bySource[item.Source] = item
	}
	deleteIDs := make(map[string]struct{})
	createSources := make(map[string]struct{})
	var errs []error
	for i, change := range plan.Changes {
		switch change.Kind {
		case planner.Add:
			if change.Before != nil || change.After == nil {
				errs = append(errs, fmt.Errorf("change %d is a malformed add", i+1))
				continue
			}
			if err := change.After.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("change %d: %w", i+1, err))
			}
			if _, exists := bySource[change.After.Source]; exists {
				errs = append(errs, fmt.Errorf("source %q now exists", change.After.Source))
			}
			if _, exists := createSources[change.After.Source]; exists {
				errs = append(errs, fmt.Errorf("plan creates source %q more than once", change.After.Source))
			}
			createSources[change.After.Source] = struct{}{}
		case planner.Update:
			if change.Before == nil || change.After == nil || change.Before.ID == "" {
				errs = append(errs, fmt.Errorf("change %d is a malformed update", i+1))
				continue
			}
			if err := change.After.Validate(); err != nil {
				errs = append(errs, fmt.Errorf("change %d: %w", i+1, err))
			}
			if change.After.ID != "" && change.After.ID != change.Before.ID {
				errs = append(errs, fmt.Errorf("change %d changes item ID from %q to %q", i+1, change.Before.ID, change.After.ID))
			}
			actual, exists := byID[change.Before.ID]
			if !exists || !actual.EqualContent(*change.Before) {
				errs = append(errs, fmt.Errorf("item %q changed or disappeared", change.Before.ID))
			}
			if other, exists := bySource[change.After.Source]; exists && other.ID != change.Before.ID {
				errs = append(errs, fmt.Errorf("replacement source %q now exists", change.After.Source))
			}
			if _, exists := deleteIDs[change.Before.ID]; exists {
				errs = append(errs, fmt.Errorf("plan deletes item %q more than once", change.Before.ID))
			}
			deleteIDs[change.Before.ID] = struct{}{}
			if _, exists := createSources[change.After.Source]; exists {
				errs = append(errs, fmt.Errorf("plan creates source %q more than once", change.After.Source))
			}
			createSources[change.After.Source] = struct{}{}
		case planner.Delete:
			if change.Before == nil || change.After != nil || change.Before.ID == "" {
				errs = append(errs, fmt.Errorf("change %d is a malformed delete", i+1))
				continue
			}
			actual, exists := byID[change.Before.ID]
			if !exists || !actual.EqualContent(*change.Before) {
				errs = append(errs, fmt.Errorf("item %q changed or disappeared", change.Before.ID))
			}
			if _, exists := deleteIDs[change.Before.ID]; exists {
				errs = append(errs, fmt.Errorf("plan deletes item %q more than once", change.Before.ID))
			}
			deleteIDs[change.Before.ID] = struct{}{}
		default:
			errs = append(errs, fmt.Errorf("change %d has unknown kind %q", i+1, change.Kind))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("plan is stale or invalid: %w", err)
	}
	return nil
}

func split(plan planner.Plan) ([]string, []domain.Redirect) {
	var deleteIDs []string
	var creates []domain.Redirect
	for _, change := range plan.Changes {
		switch change.Kind {
		case planner.Add:
			created := *change.After
			created.ID = ""
			creates = append(creates, created)
		case planner.Update:
			deleteIDs = append(deleteIDs, change.Before.ID)
			created := *change.After
			created.ID = ""
			creates = append(creates, created)
		case planner.Delete:
			deleteIDs = append(deleteIDs, change.Before.ID)
		}
	}
	return deleteIDs, creates
}
