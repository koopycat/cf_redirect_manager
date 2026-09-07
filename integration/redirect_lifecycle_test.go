package integration_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/koopycat/cf-redirect/internal/app"
	"github.com/koopycat/cf-redirect/internal/auth"
	"github.com/koopycat/cf-redirect/internal/cloudflare"
	"github.com/koopycat/cf-redirect/internal/config"
	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/planner"
)

const liveTestEnv = "CF_REDIRECT_INTEGRATION"

// TestRedirectLifecycleAgainstMockCloudflare exercises the planner, executor,
// asynchronous operation polling, and HTTP client together. The mock is kept
// in-process so this deterministic contract test can run in every go test and
// CI invocation without Docker, Java, credentials, or network access.
func TestRedirectLifecycleAgainstMockCloudflare(t *testing.T) {
	mock := newMockCloudflareAPI(t)
	mock.items = append(mock.items, cloudflare.RedirectItem{
		ID: "existing-item",
		Redirect: cloudflare.RedirectItemOptions{
			SourceURL:  "existing.example.com/",
			TargetURL:  "https://example.com/existing/",
			StatusCode: 302,
		},
		Comment: "must remain untouched",
	})
	server := httptest.NewServer(mock)
	defer server.Close()

	client := &cloudflare.Client{
		HTTPClient: server.Client(),
		BaseURL:    server.URL,
		Token:      mock.token,
	}
	exerciseRedirectLifecycle(t, client, mock.accountID, mock.listID)

	wantMethods := []string{http.MethodPost, http.MethodDelete, http.MethodPost, http.MethodDelete}
	if got := mock.mutations(); !slices.Equal(got, wantMethods) {
		t.Fatalf("mutation methods = %v, want %v", got, wantMethods)
	}
}

// TestRedirectLifecycleAgainstLiveCloudflare is intentionally opt-in because
// it temporarily creates, replaces, and deletes one uniquely named item in the
// list selected by the tool's normal configuration resolution.
func TestRedirectLifecycleAgainstLiveCloudflare(t *testing.T) {
	if os.Getenv(liveTestEnv) != "1" {
		t.Skipf("set %s=1 to run against the currently configured Cloudflare list", liveTestEnv)
	}

	cfg, err := config.Resolve("", "")
	if err != nil {
		t.Fatalf("resolve configured Cloudflare list: %v", err)
	}
	token, err := auth.NewResolver(cfg.AccountID).Token()
	if err != nil {
		t.Fatalf("resolve Cloudflare API token: %v", err)
	}
	client := &cloudflare.Client{Token: token}

	t.Logf("using configured Cloudflare account %s and list %s", cfg.AccountID, cfg.ListID)
	exerciseRedirectLifecycle(t, client, cfg.AccountID, cfg.ListID)
}

func exerciseRedirectLifecycle(t *testing.T, client *cloudflare.Client, accountID, listID string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	t.Log("reading current redirects and capturing the safety baseline…")
	baseline := listRedirects(t, ctx, client, accountID, listID)
	t.Logf("captured %d pre-existing redirect(s); each will be checked after every mutation", len(baseline))

	runID := randomID(t)
	source := fmt.Sprintf("cf-redirect-%s.example.com/source/", runID)
	original := domain.Redirect{
		Source:              source,
		Target:              fmt.Sprintf("https://example.com/cf-redirect/%s/first/", runID),
		StatusCode:          308,
		IncludeSubdomains:   true,
		SubpathMatching:     true,
		PreserveQueryString: true,
		PreservePathSuffix:  true,
		Comment:             "cf-redirect integration test " + runID,
	}

	t.Logf("temporary test source: %s", source)
	var submittedOperations []string
	defer cleanupRedirect(t, client, accountID, listID, source, &submittedOperations)
	api := &operationTrackingAPI{Client: client, submittedOperations: &submittedOperations}

	t.Log("[1/3] creating the temporary redirect…")
	addPlan, err := planner.AddRedirect(baseline, original)
	if err != nil {
		t.Fatalf("plan add: %v", err)
	}
	addReport := applyPlan(t, ctx, api, accountID, listID, addPlan)
	assertCompletedPhases(t, addReport, app.CreatePhase)

	afterAdd := listRedirects(t, ctx, client, accountID, listID)
	assertBaselineUnchanged(t, afterAdd, baseline)
	created := redirectBySource(t, afterAdd, source)
	assertRedirectContent(t, created, original)
	if created.ID == "" {
		t.Fatal("created redirect has no Cloudflare item ID")
	}
	t.Logf("[1/3] create verified; item ID is %s and pre-existing redirects are unchanged", created.ID)

	t.Log("[2/3] replacing the target while preserving options and the comment…")
	updatedTarget := fmt.Sprintf("https://example.com/cf-redirect/%s/updated/", runID)
	updatePlan, err := planner.EditRedirect([]domain.Redirect{created}, created.ID, created.Source, updatedTarget)
	if err != nil {
		t.Fatalf("plan update: %v", err)
	}
	updateReport := applyPlan(t, ctx, api, accountID, listID, updatePlan)
	assertCompletedPhases(t, updateReport, app.DeletePhase, app.CreatePhase)

	afterUpdate := listRedirects(t, ctx, client, accountID, listID)
	assertBaselineUnchanged(t, afterUpdate, baseline)
	updated := redirectBySource(t, afterUpdate, source)
	wantUpdated := original
	wantUpdated.Target = updatedTarget
	assertRedirectContent(t, updated, wantUpdated)
	if updated.ID == "" {
		t.Fatal("updated redirect has no Cloudflare item ID")
	}
	t.Logf("[2/3] update verified; replacement item ID is %s and all options were preserved", updated.ID)

	t.Log("[3/3] deleting the temporary redirect by explicit item ID…")
	deletePlan, err := planner.DeleteRedirect([]domain.Redirect{updated}, updated.ID)
	if err != nil {
		t.Fatalf("plan delete: %v", err)
	}
	deleteReport := applyPlan(t, ctx, api, accountID, listID, deletePlan)
	assertCompletedPhases(t, deleteReport, app.DeletePhase)

	afterDelete := listRedirects(t, ctx, client, accountID, listID)
	assertBaselineUnchanged(t, afterDelete, baseline)
	assertSourceAbsent(t, afterDelete, source)
	t.Log("[3/3] delete verified; temporary redirect is gone and the baseline is unchanged")
}

func applyPlan(t *testing.T, ctx context.Context, api app.RedirectAPI, accountID, listID string, plan planner.Plan) app.Report {
	t.Helper()

	report, err := (app.Executor{
		API:          api,
		AccountID:    accountID,
		ListID:       listID,
		PollInterval: 100 * time.Millisecond,
		Progress: func(message string) {
			t.Logf("Cloudflare: %s", message)
		},
	}).Apply(ctx, plan)
	if err != nil {
		t.Fatalf("apply plan: %v", err)
	}
	if !report.Revalidated {
		t.Fatal("applied plan was not revalidated")
	}
	return report
}

type operationTrackingAPI struct {
	*cloudflare.Client
	submittedOperations *[]string
}

func (a *operationTrackingAPI) CreateItems(ctx context.Context, accountID, listID string, redirects []domain.Redirect) (cloudflare.BulkOperation, error) {
	operation, err := a.Client.CreateItems(ctx, accountID, listID, redirects)
	a.record(operation)
	return operation, err
}

func (a *operationTrackingAPI) DeleteItems(ctx context.Context, accountID, listID string, ids []string) (cloudflare.BulkOperation, error) {
	operation, err := a.Client.DeleteItems(ctx, accountID, listID, ids)
	a.record(operation)
	return operation, err
}

func (a *operationTrackingAPI) record(operation cloudflare.BulkOperation) {
	if operation.ID != "" {
		*a.submittedOperations = append(*a.submittedOperations, operation.ID)
	}
}

func assertCompletedPhases(t *testing.T, report app.Report, want ...app.Phase) {
	t.Helper()
	if len(report.Phases) != len(want) {
		t.Fatalf("completed phases = %#v, want %v", report.Phases, want)
	}
	for i, phase := range report.Phases {
		if phase.Phase != want[i] || !phase.Completed || phase.Err != nil || phase.Operation.ID == "" {
			t.Fatalf("phase %d = %#v, want completed %s phase with an operation ID", i+1, phase, want[i])
		}
	}
}

func assertRedirectContent(t *testing.T, got, want domain.Redirect) {
	t.Helper()
	if !got.EqualContent(want) {
		t.Fatalf("redirect content mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func redirectBySource(t *testing.T, items []domain.Redirect, source string) domain.Redirect {
	t.Helper()
	var matches []domain.Redirect
	for _, item := range items {
		if item.Source == source {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("redirect count for source %q = %d, want 1", source, len(matches))
	}
	return matches[0]
}

func assertSourceAbsent(t *testing.T, items []domain.Redirect, source string) {
	t.Helper()
	for _, item := range items {
		if item.Source == source {
			t.Fatalf("integration redirect %q remains after delete", source)
		}
	}
}

func assertBaselineUnchanged(t *testing.T, current, baseline []domain.Redirect) {
	t.Helper()
	byID := make(map[string]domain.Redirect, len(current))
	for _, item := range current {
		byID[item.ID] = item
	}
	for _, want := range baseline {
		got, ok := byID[want.ID]
		if !ok {
			t.Fatalf("pre-existing redirect %q (%s) disappeared", want.Source, want.ID)
		}
		if !got.EqualContent(want) {
			t.Fatalf("pre-existing redirect %q (%s) changed:\n got: %#v\nwant: %#v", want.Source, want.ID, got, want)
		}
	}
}

func listRedirects(t *testing.T, ctx context.Context, client *cloudflare.Client, accountID, listID string) []domain.Redirect {
	t.Helper()
	items, err := client.ListItems(ctx, accountID, listID)
	if err != nil {
		t.Fatalf("list redirects: %v", err)
	}
	return items
}

// cleanupRedirect only removes the unique source generated by this test. It
// waits for known operations first so a timed-out create cannot appear after
// cleanup has already checked the list.
func cleanupRedirect(t *testing.T, client *cloudflare.Client, accountID, listID, source string, submittedOperations *[]string) {
	t.Helper()

	t.Log("safety cleanup: checking that no temporary redirect remains…")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for _, operationID := range *submittedOperations {
		if _, err := client.WaitBulkOperation(ctx, accountID, operationID, 100*time.Millisecond); err != nil {
			t.Logf("cleanup: operation %s did not complete successfully: %v", operationID, err)
		}
	}

	items, err := client.ListItems(ctx, accountID, listID)
	if err != nil {
		t.Errorf("cleanup: list redirects: %v", err)
		return
	}
	var ids []string
	for _, item := range items {
		if item.Source == source {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		t.Log("safety cleanup: nothing left to remove")
		return
	}

	t.Logf("safety cleanup: removing %d remaining temporary item(s)…", len(ids))
	operation, err := client.DeleteItems(ctx, accountID, listID, ids)
	if err != nil {
		t.Errorf("cleanup: delete integration redirect: %v", err)
		return
	}
	if _, err := client.WaitBulkOperation(ctx, accountID, operation.ID, 100*time.Millisecond); err != nil {
		t.Errorf("cleanup: wait for delete operation: %v", err)
		return
	}
	remaining, err := client.ListItems(ctx, accountID, listID)
	if err != nil {
		t.Errorf("cleanup: verify redirect removal: %v", err)
		return
	}
	for _, item := range remaining {
		if item.Source == source {
			t.Errorf("cleanup: redirect %q remains in the configured list; remove it before retrying", source)
			return
		}
	}
	t.Log("safety cleanup: temporary redirect removed")
}

func randomID(t *testing.T) string {
	t.Helper()
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		t.Fatalf("generate integration-test ID: %v", err)
	}
	return hex.EncodeToString(value[:])
}

type mockCloudflareAPI struct {
	t                    *testing.T
	mu                   sync.Mutex
	accountID            string
	listID               string
	token                string
	items                []cloudflare.RedirectItem
	operations           map[string]cloudflare.BulkOperation
	operationPolls       map[string]int
	outstandingOperation string
	nextID               int
	methods              []string
}

func newMockCloudflareAPI(t *testing.T) *mockCloudflareAPI {
	return &mockCloudflareAPI{
		t:              t,
		accountID:      "test-account",
		listID:         "test-list",
		token:          "test-token",
		operations:     make(map[string]cloudflare.BulkOperation),
		operationPolls: make(map[string]int),
	}
}

func (m *mockCloudflareAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if got := r.Header.Get("Authorization"); got != "Bearer "+m.token {
		m.writeError(w, http.StatusUnauthorized, "unexpected authorization header")
		return
	}

	itemsPath := "/accounts/" + m.accountID + "/rules/lists/" + m.listID + "/items"
	operationsPath := "/accounts/" + m.accountID + "/rules/lists/bulk_operations/"
	switch {
	case r.URL.Path == itemsPath:
		m.handleItems(w, r)
	case strings.HasPrefix(r.URL.Path, operationsPath):
		m.handleOperation(w, r, strings.TrimPrefix(r.URL.Path, operationsPath))
	default:
		m.writeError(w, http.StatusNotFound, "unexpected API path")
	}
}

func (m *mockCloudflareAPI) handleItems(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if got := r.URL.Query().Get("per_page"); got != "500" {
			m.t.Errorf("mock list per_page = %q, want 500", got)
		}
		m.writeSuccess(w, m.items, map[string]any{"cursors": map[string]string{}})
	case http.MethodPost:
		if !m.mutationAllowed(w) {
			return
		}
		m.methods = append(m.methods, r.Method)
		var items []cloudflare.RedirectItem
		if err := json.NewDecoder(r.Body).Decode(&items); err != nil {
			m.writeError(w, http.StatusBadRequest, "invalid create body")
			return
		}
		if len(items) == 0 {
			m.writeError(w, http.StatusBadRequest, "empty create body")
			return
		}
		for i := range items {
			m.nextID++
			items[i].ID = fmt.Sprintf("item-%d", m.nextID)
			m.items = append(m.items, items[i])
		}
		m.writeMutation(w)
	case http.MethodDelete:
		if !m.mutationAllowed(w) {
			return
		}
		m.methods = append(m.methods, r.Method)
		var payload struct {
			Items []cloudflare.DeleteItem `json:"items"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			m.writeError(w, http.StatusBadRequest, "invalid delete body")
			return
		}
		if len(payload.Items) == 0 {
			m.writeError(w, http.StatusBadRequest, "delete requires explicit IDs")
			return
		}
		requested := make(map[string]struct{}, len(payload.Items))
		for _, item := range payload.Items {
			if item.ID == "" {
				m.writeError(w, http.StatusBadRequest, "delete contains an empty ID")
				return
			}
			requested[item.ID] = struct{}{}
		}
		for id := range requested {
			if !m.hasItem(id) {
				m.writeError(w, http.StatusNotFound, "delete contains an unknown ID")
				return
			}
		}
		remaining := m.items[:0]
		for _, item := range m.items {
			if _, remove := requested[item.ID]; !remove {
				remaining = append(remaining, item)
			}
		}
		m.items = remaining
		m.writeMutation(w)
	default:
		m.writeError(w, http.StatusMethodNotAllowed, "unexpected items method")
	}
}

func (m *mockCloudflareAPI) handleOperation(w http.ResponseWriter, r *http.Request, operationID string) {
	if r.Method != http.MethodGet {
		m.writeError(w, http.StatusMethodNotAllowed, "bulk operation is read-only")
		return
	}
	operation, ok := m.operations[operationID]
	if !ok {
		m.writeError(w, http.StatusNotFound, "unknown bulk operation")
		return
	}
	if operation.Status != "completed" && m.operationPolls[operationID] == 0 {
		m.operationPolls[operationID]++
		m.writeSuccess(w, operation, nil)
		return
	}
	operation.Status = "completed"
	operation.Percent = 100
	m.operations[operationID] = operation
	if m.outstandingOperation == operationID {
		m.outstandingOperation = ""
	}
	m.writeSuccess(w, operation, nil)
}

func (m *mockCloudflareAPI) mutationAllowed(w http.ResponseWriter) bool {
	if m.outstandingOperation == "" {
		return true
	}
	m.writeError(w, http.StatusConflict, "previous bulk operation was not polled to completion")
	return false
}

func (m *mockCloudflareAPI) writeMutation(w http.ResponseWriter) {
	m.nextID++
	operationID := fmt.Sprintf("operation-%d", m.nextID)
	operation := cloudflare.BulkOperation{ID: operationID, Status: "pending"}
	m.operations[operationID] = operation
	m.outstandingOperation = operationID
	m.writeSuccess(w, operation, nil)
}

func (m *mockCloudflareAPI) hasItem(id string) bool {
	for _, item := range m.items {
		if item.ID == id {
			return true
		}
	}
	return false
}

func (m *mockCloudflareAPI) mutations() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.methods)
}

func (m *mockCloudflareAPI) writeSuccess(w http.ResponseWriter, result any, resultInfo any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success":     true,
		"errors":      []any{},
		"messages":    []any{},
		"result":      result,
		"result_info": resultInfo,
	}); err != nil {
		m.t.Errorf("encode mock success response: %v", err)
	}
}

func (m *mockCloudflareAPI) writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"errors":  []map[string]any{{"code": status, "message": message}},
	}); err != nil {
		m.t.Errorf("encode mock error response: %v", err)
	}
}
