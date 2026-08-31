package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/koopycat/cf-redirect/internal/domain"
)

func TestListItemsUsesCursorPaginationAtMaximumPageSize(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("unexpected request: %s, auth %q", r.Method, r.Header.Get("Authorization"))
		}
		if got := r.URL.Query().Get("per_page"); got != "500" {
			t.Errorf("per_page = %q", got)
		}
		if r.URL.Query().Has("page") {
			t.Errorf("page pagination must not be used: %s", r.URL.RawQuery)
		}
		if call == 1 {
			if r.URL.Query().Get("cursor") != "" {
				t.Errorf("first cursor = %q", r.URL.Query().Get("cursor"))
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{map[string]any{"id": "one", "comment": "keep", "redirect": map[string]any{"source_url": "example.com/a", "target_url": "https://b.example", "status_code": 308, "preserve_query_string": true}}}, "result_info": map[string]any{"cursors": map[string]any{"after": "opaque next"}}})
			return
		}
		if r.URL.Query().Get("cursor") != "opaque next" {
			t.Errorf("next cursor = %q", r.URL.Query().Get("cursor"))
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{map[string]any{"id": "two", "redirect": map[string]any{"source_url": "https://c.example", "target_url": "https://d.example", "status_code": 301}}}, "result_info": map[string]any{"cursors": map[string]any{}}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL, Token: "secret"}
	items, err := client.ListItems(context.Background(), "account", "list")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || calls.Load() != 2 || items[0].Comment != "keep" || !items[0].PreserveQueryString {
		t.Fatalf("unexpected items/calls: %#v, %d", items, calls.Load())
	}
}

func TestListItemsRejectsRepeatedCursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}, "result_info": map[string]any{"cursors": map[string]any{"after": "repeat"}}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL}
	if _, err := client.ListItems(context.Background(), "account", "list"); err == nil || !strings.Contains(err.Error(), "repeated pagination cursor") {
		t.Fatalf("expected repeated cursor error, got %v", err)
	}
}

func TestCreateAndDeleteUseOnlyPOSTAndDELETE(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if strings.Contains(r.URL.Path, "bulk_operations") {
			t.Fatal("unexpected poll")
		}
		var raw any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if r.Method == http.MethodDelete {
			body, ok := raw.(map[string]any)
			items, itemsOK := body["items"].([]any)
			if !ok || !itemsOK || len(items) != 1 || items[0].(map[string]any)["id"] != "explicit-id" {
				t.Errorf("delete body = %#v", raw)
			}
		}
		if r.Method == http.MethodPost {
			body, ok := raw.([]any)
			redirect := body[0].(map[string]any)["redirect"].(map[string]any)
			if !ok || len(redirect) != 7 || redirect["include_subdomains"] != false || redirect["subpath_matching"] != false || redirect["preserve_query_string"] != false || redirect["preserve_path_suffix"] != false {
				t.Errorf("create must explicitly preserve booleans: %#v", raw)
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "operation", "status": "pending"}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL, Token: "secret"}
	if _, err := client.CreateItems(context.Background(), "account", "list", []domain.Redirect{domain.New("https://a.example", "https://b.example")}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.DeleteItems(context.Background(), "account", "list", []string{"explicit-id"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(methods, ",") != "POST,DELETE" {
		t.Fatalf("methods = %v", methods)
	}
	if _, err := client.DeleteItems(context.Background(), "account", "list", nil); err == nil {
		t.Fatal("expected explicit ID validation")
	}
}

func TestDoRetriesOnRateLimitAndSucceeds(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 10021, "message": "you have been ratelimited please wait and try again"}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL, Token: "secret"}
	if _, err := doJSON[[]json.RawMessage](context.Background(), &client, http.MethodGet, "/x", nil); err != nil {
		t.Fatalf("GET after rate limit retry: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected one retry, got %d calls", calls.Load())
	}
}

func TestRetryAfterIsHonoredWithoutJitter(t *testing.T) {
	if got := backoffFor(http.Header{"Retry-After": []string{"0"}}, 0); got != 0 {
		t.Fatalf("backoffFor Retry-After 0 = %s", got)
	}
	if got := backoffFor(http.Header{"Retry-After": []string{"9999"}}, 0); got != maxBackoff {
		t.Fatalf("backoffFor capped Retry-After = %s, want %s", got, maxBackoff)
	}
}

func TestDoDoesNotRetryAuthFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 9109, "message": "not authorized"}}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL, Token: "secret"}
	_, err := doJSON[[]json.RawMessage](context.Background(), &client, http.MethodGet, "/x", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if calls.Load() != 1 {
		t.Fatalf("auth failure must not be retried, got %d calls", calls.Load())
	}
}

func TestDoRetriesOtherTransientStatuses(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL, Token: "secret"}
	if _, err := doJSON[[]json.RawMessage](context.Background(), &client, http.MethodGet, "/x", nil); err != nil {
		t.Fatalf("GET after 500 retries: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected two retries, got %d calls", calls.Load())
	}
}

func TestMutationsNeverRetryTransientResponses(t *testing.T) {
	var postCalls, deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var calls *atomic.Int32
		switch r.Method {
		case http.MethodPost:
			calls = &postCalls
		case http.MethodDelete:
			calls = &deleteCalls
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"message": "try later"}}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL}
	redirect := domain.New("https://a.example", "https://b.example")
	if _, err := client.CreateItems(context.Background(), "account", "list", []domain.Redirect{redirect}); err == nil {
		t.Fatal("CreateItems must return the transient error")
	}
	if _, err := client.DeleteItems(context.Background(), "account", "list", []string{"item"}); err == nil {
		t.Fatal("DeleteItems must return the transient error")
	}
	if postCalls.Load() != 1 || deleteCalls.Load() != 1 {
		t.Fatalf("mutations were replayed: POST %d, DELETE %d", postCalls.Load(), deleteCalls.Load())
	}
}

func TestListItemsSanitizesRemoteFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": []any{
			map[string]any{"id": "one", "comment": "comment\x1b[2J\ntext", "redirect": map[string]any{
				"source_url": "source\x1b[31m", "target_url": "target\x07", "status_code": 301,
			}},
		}, "result_info": map[string]any{"cursors": map[string]any{}}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL}
	items, err := client.ListItems(context.Background(), "account", "list")
	if err != nil {
		t.Fatal(err)
	}
	if got := items[0]; got.Source != "source[31m" || got.Target != "target" || got.Comment != "comment[2Jtext" {
		t.Fatalf("remote fields were not sanitized: %#v", got)
	}
}

func TestWaitBulkOperation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := "pending"
		if calls.Add(1) == 2 {
			status = "completed"
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "op", "status": status}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL}
	op, err := client.WaitBulkOperation(context.Background(), "account", "op", time.Millisecond)
	if err != nil || op.Status != "completed" || calls.Load() != 2 {
		t.Fatalf("Wait = %#v, %v, calls %d", op, err, calls.Load())
	}
}

func TestBulkPollIntervalBacksOffExponentiallyAndCaps(t *testing.T) {
	interval := time.Second
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	for i, expected := range want {
		interval = nextBulkPollInterval(interval)
		if interval != expected {
			t.Fatalf("step %d: interval = %s, want %s", i+1, interval, expected)
		}
	}
}

func TestWaitBulkOperationFailureAndAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "bulk_operations") {
			json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"id": "op", "status": "failed", "error": "bad row", "error_count": 1}})
			return
		}
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 1000, "message": "forbidden"}}})
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL}
	if _, err := client.WaitBulkOperation(context.Background(), "account", "op", time.Millisecond); err == nil {
		t.Fatal("expected operation error")
	}
	_, err := client.ListItems(context.Background(), "account", "list")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 403 || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("unexpected API error: %v", err)
	}
}
