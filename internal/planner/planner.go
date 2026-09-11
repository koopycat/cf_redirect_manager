// Package planner creates deterministic, side-effect-free redirect changes.
package planner

import (
	"errors"
	"fmt"
	"sort"

	"github.com/koopycat/cf-redirect/internal/domain"
)

type ActionKind string

const (
	Add    ActionKind = "add"
	Update ActionKind = "update"
	Delete ActionKind = "delete"
)

// Change captures both sides of an intended mutation. Before is populated for
// updates and deletes; After is populated for adds and updates.
type Change struct {
	Kind   ActionKind
	Before *domain.Redirect
	After  *domain.Redirect
}

type Plan struct {
	Changes         []Change
	SkippedExisting int
}

// Empty reports whether the plan has no mutations to apply. Reporting-only
// metadata such as SkippedExisting does not make a plan actionable.
func (p Plan) Empty() bool { return len(p.Changes) == 0 }

func (p Plan) Counts() (adds, updates, deletes int) {
	for _, change := range p.Changes {
		switch change.Kind {
		case Add:
			adds++
		case Update:
			updates++
		case Delete:
			deletes++
		}
	}
	return
}

// AddRedirect plans an addition, rejecting an already configured source.
func AddRedirect(current []domain.Redirect, redirect domain.Redirect) (Plan, error) {
	if err := validateCurrent(current); err != nil {
		return Plan{}, err
	}
	if err := redirect.Validate(); err != nil {
		return Plan{}, err
	}
	if findBySource(current, redirect.Source) != nil {
		return Plan{}, fmt.Errorf("redirect source %q already exists", redirect.Source)
	}
	return Plan{Changes: []Change{{Kind: Add, After: pointer(redirect)}}}, nil
}

// EditRedirect plans replacement of an explicitly identified existing entry.
// Every option and comment is preserved; only source and target are changed.
func EditRedirect(current []domain.Redirect, id, source, target string) (Plan, error) {
	if err := validateCurrent(current); err != nil {
		return Plan{}, err
	}
	old := findByID(current, id)
	if old == nil {
		return Plan{}, fmt.Errorf("redirect item %q not found", id)
	}
	replacement := *old
	replacement.Source = source
	replacement.Target = target
	if err := replacement.Validate(); err != nil {
		return Plan{}, err
	}
	if duplicate := findBySource(current, source); duplicate != nil && duplicate.ID != id {
		return Plan{}, fmt.Errorf("redirect source %q already exists", source)
	}
	if old.EqualContent(replacement) {
		return Plan{}, nil
	}
	return Plan{Changes: []Change{{Kind: Update, Before: pointer(*old), After: pointer(replacement)}}}, nil
}

// DeleteAll plans deletion of every currently listed item. Empty lists produce
// an empty plan, and each deletion retains its explicit Cloudflare item ID.
func DeleteAll(current []domain.Redirect) (Plan, error) {
	if err := validateCurrent(current); err != nil {
		return Plan{}, err
	}
	changes := make([]Change, len(current))
	for i := range current {
		changes[i] = Change{Kind: Delete, Before: pointer(current[i])}
	}
	return Plan{Changes: changes}, nil
}

// DeleteRedirect plans deletion by explicit Cloudflare item ID.
func DeleteRedirect(current []domain.Redirect, id string) (Plan, error) {
	if err := validateCurrent(current); err != nil {
		return Plan{}, err
	}
	old := findByID(current, id)
	if old == nil {
		return Plan{}, fmt.Errorf("redirect item %q not found", id)
	}
	return Plan{Changes: []Change{{Kind: Delete, Before: pointer(*old)}}}, nil
}

// ImportUpsert plans CSV-style source/target upserts. Existing redirect
// options, comments and IDs are retained. Current entries omitted from import
// are never deleted.
func ImportUpsert(current []domain.Redirect, imported []domain.Redirect) (Plan, error) {
	if err := validateCurrent(current); err != nil {
		return Plan{}, err
	}
	seen := make(map[string]struct{}, len(imported))
	plan := Plan{Changes: make([]Change, 0, len(imported))}
	for _, candidate := range imported {
		if err := candidate.Validate(); err != nil {
			return Plan{}, fmt.Errorf("import source %q: %w", candidate.Source, err)
		}
		if _, exists := seen[candidate.Source]; exists {
			return Plan{}, fmt.Errorf("duplicate imported source %q", candidate.Source)
		}
		seen[candidate.Source] = struct{}{}
		old := findBySource(current, candidate.Source)
		if old == nil {
			candidate.ID = ""
			plan.Changes = append(plan.Changes, Change{Kind: Add, After: pointer(candidate)})
			continue
		}
		replacement := *old
		replacement.Target = candidate.Target
		if old.EqualContent(replacement) {
			plan.SkippedExisting++
			continue
		}
		plan.Changes = append(plan.Changes, Change{Kind: Update, Before: pointer(*old), After: pointer(replacement)})
	}
	sort.SliceStable(plan.Changes, func(i, j int) bool {
		return plan.Changes[i].After.Source < plan.Changes[j].After.Source
	})
	return plan, nil
}

func validateCurrent(current []domain.Redirect) error {
	ids := make(map[string]struct{}, len(current))
	sources := make(map[string]struct{}, len(current))
	var errs []error
	for i := range current {
		r := current[i]
		if r.ID == "" {
			errs = append(errs, fmt.Errorf("current redirect %q has no item ID", r.Source))
		}
		if _, exists := ids[r.ID]; exists {
			errs = append(errs, fmt.Errorf("duplicate current item ID %q", r.ID))
		}
		ids[r.ID] = struct{}{}
		if _, exists := sources[r.Source]; exists {
			errs = append(errs, fmt.Errorf("duplicate current source %q", r.Source))
		}
		sources[r.Source] = struct{}{}
		if err := r.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("current redirect %q: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

func findByID(items []domain.Redirect, id string) *domain.Redirect {
	for i := range items {
		if items[i].ID == id {
			copy := items[i]
			return &copy
		}
	}
	return nil
}

func findBySource(items []domain.Redirect, source string) *domain.Redirect {
	for i := range items {
		if items[i].Source == source {
			copy := items[i]
			return &copy
		}
	}
	return nil
}

func pointer[T any](v T) *T { return &v }
