// Package cloudflare is a focused, typed HTTP client for Bulk Redirect Lists.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/koopycat/cf-redirect/internal/domain"
)

const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// defaultClient guards against hangs when no HTTPClient is configured.
var defaultClient = &http.Client{Timeout: 30 * time.Second}

type Client struct {
	HTTPClient *http.Client
	BaseURL    string
	Token      string
}

type APIError struct {
	StatusCode int
	Errors     []Message
	Body       string
}

func (e *APIError) Error() string {
	if len(e.Errors) > 0 {
		parts := make([]string, 0, len(e.Errors))
		for _, item := range e.Errors {
			parts = append(parts, item.Message)
		}
		return fmt.Sprintf("Cloudflare API returned HTTP %d: %s", e.StatusCode, strings.Join(parts, "; "))
	}
	return fmt.Sprintf("Cloudflare API returned HTTP %d: %s", e.StatusCode, e.Body)
}

type Message struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type envelope[T any] struct {
	Success    bool       `json:"success"`
	Errors     []Message  `json:"errors"`
	Messages   []Message  `json:"messages"`
	Result     T          `json:"result"`
	ResultInfo ResultInfo `json:"result_info"`
}

type ResultInfo struct {
	Cursors Cursors `json:"cursors"`
}

type Cursors struct {
	After  string `json:"after"`
	Before string `json:"before"`
}

type RedirectItem struct {
	ID       string              `json:"id,omitempty"`
	Redirect RedirectItemOptions `json:"redirect"`
	Comment  string              `json:"comment,omitempty"`
	Created  string              `json:"created_on,omitempty"`
	Modified string              `json:"modified_on,omitempty"`
}

type RedirectItemOptions struct {
	SourceURL           string `json:"source_url"`
	TargetURL           string `json:"target_url"`
	StatusCode          int    `json:"status_code"`
	IncludeSubdomains   bool   `json:"include_subdomains"`
	SubpathMatching     bool   `json:"subpath_matching"`
	PreserveQueryString bool   `json:"preserve_query_string"`
	PreservePathSuffix  bool   `json:"preserve_path_suffix"`
}

type DeleteItem struct {
	ID string `json:"id"`
}

type BulkOperation struct {
	ID          string `json:"id"`
	OperationID string `json:"operation_id,omitempty"`
	Status      string `json:"status"`
	Completed   string `json:"completed,omitempty"`
	Percent     int    `json:"percent_complete,omitempty"`
	Error       string `json:"error,omitempty"`
	ErrorCount  int    `json:"error_count,omitempty"`
}

func (o *BulkOperation) normalizeID() {
	if o.ID == "" {
		o.ID = o.OperationID
	}
}

func (o BulkOperation) terminal() (done, success bool) {
	// A non-empty error field is terminal even if the status still says
	// running/pending — Cloudflare reports partial failures this way.
	if o.Error != "" || o.ErrorCount > 0 {
		return true, false
	}
	switch strings.ToLower(o.Status) {
	case "completed", "complete", "success", "succeeded":
		return true, true
	case "failed", "failure", "cancelled", "canceled":
		return true, false
	default:
		return false, false
	}
}

func (c *Client) ListItems(ctx context.Context, accountID, listID string) ([]domain.Redirect, error) {
	return c.listItems(ctx, accountID, listID, "")
}

// SearchItems asks Cloudflare to filter Bulk Redirect items. Callers that also
// search comments should use ListItems and perform that broader filtering
// locally because the API's search semantics are list-type dependent.
func (c *Client) SearchItems(ctx context.Context, accountID, listID, search string) ([]domain.Redirect, error) {
	return c.listItems(ctx, accountID, listID, search)
}

func (c *Client) listItems(ctx context.Context, accountID, listID, search string) ([]domain.Redirect, error) {
	const perPage = 500 // Cloudflare's documented maximum for list items.
	var all []domain.Redirect
	cursor := ""
	seen := make(map[string]struct{})
	for requestNumber := 1; ; requestNumber++ {
		query := url.Values{"per_page": {fmt.Sprint(perPage)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		if search != "" {
			query.Set("search", search)
		}
		path := c.listPath(accountID, listID) + "?" + query.Encode()
		var response envelope[[]RedirectItem]
		if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
			return nil, fmt.Errorf("list redirect items request %d: %w", requestNumber, err)
		}
		for _, item := range response.Result {
			all = append(all, item.domain())
		}
		next := response.ResultInfo.Cursors.After
		if next == "" {
			break
		}
		if next == cursor {
			return nil, fmt.Errorf("list redirect items: Cloudflare repeated pagination cursor %q", next)
		}
		if _, exists := seen[next]; exists {
			return nil, fmt.Errorf("list redirect items: Cloudflare reused pagination cursor %q", next)
		}
		seen[next] = struct{}{}
		cursor = next
	}
	return all, nil
}

// CreateItems uses POST only; this client intentionally has no replace-all PUT.
func (c *Client) CreateItems(ctx context.Context, accountID, listID string, redirects []domain.Redirect) (BulkOperation, error) {
	items := make([]RedirectItem, len(redirects))
	for i := range redirects {
		if err := redirects[i].Validate(); err != nil {
			return BulkOperation{}, fmt.Errorf("create item %d: %w", i+1, err)
		}
		items[i] = fromDomain(redirects[i])
	}
	var response envelope[BulkOperation]
	if err := c.do(ctx, http.MethodPost, c.listPath(accountID, listID), items, &response); err != nil {
		return BulkOperation{}, fmt.Errorf("create redirect items: %w", err)
	}
	response.Result.normalizeID()
	return response.Result, nil
}

// DeleteItems requires explicit, non-empty item IDs.
func (c *Client) DeleteItems(ctx context.Context, accountID, listID string, ids []string) (BulkOperation, error) {
	if len(ids) == 0 {
		return BulkOperation{}, fmt.Errorf("at least one explicit item ID is required")
	}
	items := make([]DeleteItem, len(ids))
	for i, id := range ids {
		if strings.TrimSpace(id) == "" {
			return BulkOperation{}, fmt.Errorf("delete item %d has an empty ID", i+1)
		}
		items[i].ID = id
	}
	payload := struct {
		Items []DeleteItem `json:"items"`
	}{Items: items}
	var response envelope[BulkOperation]
	if err := c.do(ctx, http.MethodDelete, c.listPath(accountID, listID), payload, &response); err != nil {
		return BulkOperation{}, fmt.Errorf("delete redirect items: %w", err)
	}
	response.Result.normalizeID()
	return response.Result, nil
}

func (c *Client) GetBulkOperation(ctx context.Context, accountID, operationID string) (BulkOperation, error) {
	if operationID == "" {
		return BulkOperation{}, fmt.Errorf("bulk operation ID is required")
	}
	path := "/accounts/" + url.PathEscape(accountID) + "/rules/lists/bulk_operations/" + url.PathEscape(operationID)
	var response envelope[BulkOperation]
	if err := c.do(ctx, http.MethodGet, path, nil, &response); err != nil {
		return BulkOperation{}, fmt.Errorf("get bulk operation: %w", err)
	}
	response.Result.normalizeID()
	return response.Result, nil
}

const (
	// BulkWaitTimeout bounds how long an asynchronous Cloudflare operation may
	// remain without a terminal status before we return a recovery hint.
	BulkWaitTimeout = 90 * time.Second
	// DefaultBulkPollInterval is the first delay after the immediate status
	// check. Subsequent delays double to reduce pressure on the API.
	DefaultBulkPollInterval = time.Second
	// MaxBulkPollInterval keeps the UI reasonably current without polling the
	// shared Cloudflare token aggressively during long-running operations.
	MaxBulkPollInterval = 8 * time.Second
)

func (c *Client) WaitBulkOperation(ctx context.Context, accountID, operationID string, interval time.Duration) (BulkOperation, error) {
	if interval <= 0 {
		interval = DefaultBulkPollInterval
	}
	if interval > MaxBulkPollInterval {
		interval = MaxBulkPollInterval
	}
	deadline := time.NewTimer(BulkWaitTimeout)
	defer deadline.Stop()
	for {
		operation, err := c.GetBulkOperation(ctx, accountID, operationID)
		if err != nil {
			return BulkOperation{}, err
		}
		if done, success := operation.terminal(); done {
			if !success {
				return operation, fmt.Errorf("bulk operation %s ended with status %q: %s", operation.ID, operation.Status, operation.Error)
			}
			return operation, nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return operation, ctx.Err()
		case <-timer.C:
			interval = nextBulkPollInterval(interval)
		case <-deadline.C:
			timer.Stop()
			return operation, fmt.Errorf("bulk operation %s did not complete within %s; check it with: cf-redirect status %s", operation.ID, BulkWaitTimeout, operation.ID)
		}
	}
}

func nextBulkPollInterval(current time.Duration) time.Duration {
	if current >= MaxBulkPollInterval {
		return MaxBulkPollInterval
	}
	next := current * 2
	if next > MaxBulkPollInterval {
		return MaxBulkPollInterval
	}
	return next
}

func (c *Client) do(ctx context.Context, method, path string, body any, destination any) error {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	// Marshal once; a non-nil body is replayed for each retry attempt.
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = defaultClient
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		var reader io.Reader
		if bodyBytes != nil {
			reader = bytes.NewReader(bodyBytes)
		}
		req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/json")
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		payload, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		response.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read response: %w", readErr)
		}
		if retryable(response.StatusCode) && attempt < maxRetries {
			lastErr = &APIError{StatusCode: response.StatusCode, Errors: parseErrors(payload), Body: strings.TrimSpace(string(payload))}
			if !sleep(ctx, backoffFor(response.Header, attempt)) {
				return ctx.Err()
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return &APIError{StatusCode: response.StatusCode, Errors: parseErrors(payload), Body: strings.TrimSpace(string(payload))}
		}
		if err := json.Unmarshal(payload, destination); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		// All used destination types are envelopes. Decode this small projection to
		// enforce API-level success independently of HTTP status.
		var status struct {
			Success bool      `json:"success"`
			Errors  []Message `json:"errors"`
		}
		if err := json.Unmarshal(payload, &status); err == nil && !status.Success {
			return &APIError{StatusCode: response.StatusCode, Errors: status.Errors, Body: strings.TrimSpace(string(payload))}
		}
		return nil
	}
	return lastErr
}

const (
	// maxRetries bounds automatic retries for transient Cloudflare errors.
	maxRetries = 5
	// baseBackoff is the starting delay; each attempt doubles it.
	baseBackoff = 400 * time.Millisecond
	// maxBackoff caps the per-attempt delay and any Retry-After hint.
	maxBackoff = 8 * time.Second
)

// retryable reports whether a response should be retried: rate limits and
// server-side failures are transient under Cloudflare's load balancing.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// retryAfter extracts Cloudflare's Retry-After hint when present.
func retryAfter(h http.Header) time.Duration {
	value := h.Get("Retry-After")
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if d := time.Until(when); d > 0 {
			return d
		}
	}
	return 0
}

// backoffFor returns the wait before the given retry attempt, preferring
// Cloudflare's hint and adding jitter to avoid synchronized bursts.
func backoffFor(h http.Header, attempt int) time.Duration {
	delay := baseBackoff << attempt
	if delay > maxBackoff {
		delay = maxBackoff
	}
	if hint := retryAfter(h); hint > 0 {
		if hint > maxBackoff {
			hint = maxBackoff
		}
		delay = hint
	}
	jitter := time.Duration(rand.IntN(int(delay / 4)))
	return delay + jitter
}

// sleep waits for d, aborting early when the context is cancelled.
func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// parseErrors decodes the error array from a Cloudflare error envelope.
func parseErrors(payload []byte) []Message {
	var failed envelope[json.RawMessage]
	if err := json.Unmarshal(payload, &failed); err == nil {
		return failed.Errors
	}
	return nil
}

func (c *Client) listPath(accountID, listID string) string {
	return "/accounts/" + url.PathEscape(accountID) + "/rules/lists/" + url.PathEscape(listID) + "/items"
}

func fromDomain(r domain.Redirect) RedirectItem {
	return RedirectItem{Comment: r.Comment, Redirect: RedirectItemOptions{
		SourceURL: r.Source, TargetURL: r.Target, StatusCode: r.EffectiveStatusCode(),
		IncludeSubdomains: r.IncludeSubdomains, SubpathMatching: r.SubpathMatching,
		PreserveQueryString: r.PreserveQueryString, PreservePathSuffix: r.PreservePathSuffix,
	}}
}

func (r RedirectItem) domain() domain.Redirect {
	// Remote data must never carry terminal control sequences into any output.
	return domain.Redirect{ID: r.ID, Source: sanitize(r.Redirect.SourceURL), Target: sanitize(r.Redirect.TargetURL),
		StatusCode: r.Redirect.StatusCode, IncludeSubdomains: r.Redirect.IncludeSubdomains,
		SubpathMatching: r.Redirect.SubpathMatching, PreserveQueryString: r.Redirect.PreserveQueryString,
		PreservePathSuffix: r.Redirect.PreservePathSuffix, Comment: sanitize(r.Comment)}
}

// sanitize removes control characters so remote text cannot inject terminal
// escape sequences or corrupt structured output.
func sanitize(value string) string {
	if !strings.ContainsFunc(value, func(r rune) bool { return unicode.IsControl(r) }) {
		return value
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, value)
}
