package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/koopycat/cf-redirect/internal/cloudflare"
	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
)

type fakeAPI struct {
	current   []domain.Redirect
	calls     []string
	deleted   []string
	created   []domain.Redirect
	waitError map[string]error
}

func (f *fakeAPI) ListItems(context.Context, string, string) ([]domain.Redirect, error) {
	f.calls = append(f.calls, "list")
	return f.current, nil
}
func (f *fakeAPI) DeleteItems(_ context.Context, _, _ string, ids []string) (cloudflare.BulkOperation, error) {
	f.calls = append(f.calls, "delete")
	f.deleted = append(f.deleted, ids...)
	return cloudflare.BulkOperation{ID: "delete-op", Status: "pending"}, nil
}
func (f *fakeAPI) CreateItems(_ context.Context, _, _ string, items []domain.Redirect) (cloudflare.BulkOperation, error) {
	f.calls = append(f.calls, "create")
	f.created = append(f.created, items...)
	return cloudflare.BulkOperation{ID: "create-op", Status: "pending"}, nil
}
func (f *fakeAPI) WaitBulkOperation(_ context.Context, _, id string, _ time.Duration) (cloudflare.BulkOperation, error) {
	f.calls = append(f.calls, "wait-"+id)
	if err := f.waitError[id]; err != nil {
		return cloudflare.BulkOperation{ID: id, Status: "failed"}, err
	}
	return cloudflare.BulkOperation{ID: id, Status: "completed"}, nil
}

func oldItem() domain.Redirect {
	return domain.Redirect{ID: "old-id", Source: "https://old.example", Target: "https://target.example/old", StatusCode: 308, PreserveQueryString: true, Comment: "keep"}
}

func TestExecutorDeletesUpdatesThenWaitsThenCreates(t *testing.T) {
	old := oldItem()
	after := old
	after.Target = "https://target.example/new"
	add := domain.New("https://add.example", "https://target.example/add")
	plan := planner.Plan{Changes: []planner.Change{
		{Kind: planner.Update, Before: &old, After: &after},
		{Kind: planner.Add, After: &add},
	}}
	api := &fakeAPI{current: []domain.Redirect{old}, waitError: map[string]error{}}
	report, err := (Executor{API: api, AccountID: "account", ListID: "list"}).Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"list", "delete", "wait-delete-op", "create", "wait-create-op"}
	if !reflect.DeepEqual(api.calls, wantCalls) {
		t.Fatalf("calls = %v, want %v", api.calls, wantCalls)
	}
	if !reflect.DeepEqual(api.deleted, []string{"old-id"}) || len(api.created) != 2 || api.created[0].ID != "" || !api.created[0].PreserveQueryString || api.created[0].Comment != "keep" {
		t.Fatalf("bad requests: deleted=%v created=%#v", api.deleted, api.created)
	}
	if len(report.Phases) != 2 || !report.Phases[0].Completed || !report.Phases[1].Completed {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestExecutorReportsPartialFailure(t *testing.T) {
	old := oldItem()
	after := old
	after.Target = "https://target.example/new"
	plan := planner.Plan{Changes: []planner.Change{{Kind: planner.Update, Before: &old, After: &after}}}
	api := &fakeAPI{current: []domain.Redirect{old}, waitError: map[string]error{"create-op": errors.New("create failed")}}
	report, err := (Executor{API: api, AccountID: "account", ListID: "list"}).Apply(context.Background(), plan)
	if err == nil || !report.Partial() || len(report.Phases) != 2 || !report.Phases[0].Completed || report.Phases[1].Err == nil {
		t.Fatalf("expected partial failure, report=%#v err=%v", report, err)
	}
	var executionError *ExecutionError
	if !errors.As(err, &executionError) || !executionError.Report.Partial() {
		t.Fatalf("missing report in error: %v", err)
	}
}

func TestExecutorRejectsStalePlanBeforeMutation(t *testing.T) {
	old := oldItem()
	after := old
	after.Target = "https://target.example/new"
	plan := planner.Plan{Changes: []planner.Change{{Kind: planner.Update, Before: &old, After: &after}}}
	stale := old
	stale.Comment = "someone changed this"
	api := &fakeAPI{current: []domain.Redirect{stale}, waitError: map[string]error{}}
	report, err := (Executor{API: api, AccountID: "account", ListID: "list"}).Apply(context.Background(), plan)
	if err == nil || report.Revalidated || !reflect.DeepEqual(api.calls, []string{"list"}) {
		t.Fatalf("stale apply should not mutate: report=%#v err=%v calls=%v", report, err, api.calls)
	}
}

func TestRevalidateDetectsSourceCollision(t *testing.T) {
	add := domain.New("https://add.example", "https://target.example")
	plan := planner.Plan{Changes: []planner.Change{{Kind: planner.Add, After: &add}}}
	current := []domain.Redirect{{ID: "new-id", Source: add.Source, Target: "https://other.example", StatusCode: 301}}
	if err := Revalidate(plan, current); err == nil {
		t.Fatal("expected collision")
	}
}
