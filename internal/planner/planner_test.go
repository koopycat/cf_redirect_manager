package planner

import (
	"testing"

	"github.com/koopycat/cf-redirect/internal/domain"
)

func existing() domain.Redirect {
	return domain.Redirect{ID: "id-1", Source: "https://old.example/path", Target: "https://target.example/old", StatusCode: 308, IncludeSubdomains: true, SubpathMatching: true, PreserveQueryString: true, PreservePathSuffix: true, Comment: "retain me"}
}

func TestEditPreservesOptionsAndComment(t *testing.T) {
	old := existing()
	plan, err := EditRedirect([]domain.Redirect{old}, old.ID, "https://new.example/path", "https://target.example/new")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || plan.Changes[0].Kind != Update {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	got := plan.Changes[0].After
	if got.StatusCode != old.StatusCode || !got.IncludeSubdomains || !got.SubpathMatching || !got.PreserveQueryString || !got.PreservePathSuffix || got.Comment != old.Comment || got.ID != old.ID {
		t.Fatalf("options were not preserved: %#v", got)
	}
	if plan.Changes[0].Before == &old {
		t.Fatal("plan should own copies")
	}
}

func TestAddAndExplicitDelete(t *testing.T) {
	old := existing()
	if _, err := AddRedirect([]domain.Redirect{old}, domain.New(old.Source, "https://other.example")); err == nil {
		t.Fatal("expected duplicate source error")
	}
	plan, err := DeleteRedirect([]domain.Redirect{old}, old.ID)
	if err != nil || plan.Changes[0].Before.ID != old.ID {
		t.Fatalf("unexpected delete plan: %#v, %v", plan, err)
	}
	if _, err := DeleteRedirect([]domain.Redirect{old}, ""); err == nil {
		t.Fatal("deletion without explicit ID must fail")
	}
}

func TestImportUpsertPreservesExistingAndNeverDeletesOmitted(t *testing.T) {
	old := existing()
	omitted := domain.Redirect{ID: "id-2", Source: "https://omitted.example", Target: "https://keep.example", StatusCode: 302, Comment: "untouched"}
	imported := []domain.Redirect{
		domain.New(old.Source, "https://target.example/changed"),
		domain.New("https://added.example", "https://target.example/added"),
	}
	plan, err := ImportUpsert([]domain.Redirect{old, omitted}, imported)
	if err != nil {
		t.Fatal(err)
	}
	adds, updates, deletes := plan.Counts()
	if adds != 1 || updates != 1 || deletes != 0 {
		t.Fatalf("counts = %d,%d,%d", adds, updates, deletes)
	}
	for _, change := range plan.Changes {
		if change.Kind == Update {
			if change.After.Comment != old.Comment || change.After.StatusCode != old.StatusCode || !change.After.PreservePathSuffix {
				t.Fatalf("upsert lost options: %#v", change.After)
			}
		}
		if change.Before != nil && change.Before.ID == omitted.ID {
			t.Fatal("omitted item was included in plan")
		}
	}
}

func TestImportRejectsDuplicateSource(t *testing.T) {
	item := domain.New("https://same.example", "https://one.example")
	other := domain.New(item.Source, "https://two.example")
	if _, err := ImportUpsert(nil, []domain.Redirect{item, other}); err == nil {
		t.Fatal("expected duplicate import error")
	}
}
