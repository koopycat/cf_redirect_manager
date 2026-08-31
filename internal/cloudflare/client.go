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

	"github.com/koopycat/cf-redirect/internal/domain"
	"github.com/koopycat/cf-redirect/internal/textsafe"
)

const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

const maxResponseBytes = 4 << 20

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
			message := textsafe.StripControls(item.Message)
			if item.Code != 0 {
				message = fmt.Sprintf("%s (code %d)", message, item.Code)
			}
			parts = append(parts, message)
		}
		return fmt.Sprintf("Cloudflare API returned HTTP %d: %s", e.StatusCode, strings.Join(parts, "; "))
	}
	detail := textsafe.StripControls(strings.TrimSpace(e.Body))
	if detail == "" {
		detail = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("Cloudflare API returned HTTP %d: %s", e.StatusCode, detail)
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
	o.ID = textsafe.StripControls(o.ID)
	o.OperationID = textsafe.StripControls(o.OperationID)
	o.Status = textsafe.StripControls(o.Status)
	o.Completed = textsafe.StripControls(o.Completed)
	o.Error = textsafe.StripControls(o.Error)
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
	const perPage = 500 // Cloudflare's documented maximum for list items.
	var all []domain.Redirect
	cursor := ""
	seen := make(map[string]struct{})
	for requestNumber := 1; ; requestNumber++ {
		query := url.Values{"per_page": {strconv.Itoa(perPage)}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		path := c.listPath(accountID, listID) + "?" + query.Encode()
		response, err := doJSON[[]RedirectItem](ctx, c, http.MethodGet, path, nil)
		if err != nil {
			return nil, fmt.Errorf("list redirect items request %d: %w", requestNumber, err)
		}
		for _, item := range response.Result {
			all = append(all, item.domain())
		}
		next := response.ResultInfo.Cursors.After
		if next == "" {
			return all, nil
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
	response, err := doJSON[BulkOperation](ctx, c, http.MethodPost, c.listPath(accountID, listID), items)
	if err != nil {
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
	response, err := doJSON[BulkOperation](ctx, c, http.MethodDelete, c.listPath(accountID, listID), payload)
	if err != nil {
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
	response, err := doJSON[BulkOperation](ctx, c, http.MethodGet, path, nil)
	if err != nil {
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
				detail := operation.Error
				if detail == "" && operation.ErrorCount > 0 {
					detail = fmt.Sprintf("Cloudflare reported %d error(s)", operation.ErrorCount)
				}
				if detail == "" {
					detail = "Cloudflare reported an operation failure"
				}
				return operation, fmt.Errorf("bulk operation %s ended with status %q: %s", operation.ID, operation.Status, textsafe.StripControls(detail))
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

// doJSON performs one typed Cloudflare request. Only read-only GET requests are
// eligible for automatic replay; POST and DELETE are always attempted once.
func doJSON[T any](ctx context.Context, c *Client, method, path string, body any) (envelope[T], error) {
	var zero envelope[T]
	var bodyBytes []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return zero, fmt.Errorf("encode request: %w", err)
		}
		bodyBytes = encoded
	}

	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = defaultClient
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(bodyBytes))
		if err != nil {
			return zero, fmt.Errorf("build %s request: %w", method, err)
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Accept", "application/json")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		response, err := httpClient.Do(req)
		if err != nil {
			return zero, fmt.Errorf("send %s request: %w", method, err)
		}
		payload, err := readResponse(response.Body)
		if err != nil {
			return zero, fmt.Errorf("read %s response: %w", method, err)
		}
		if method == http.MethodGet && retryable(response.StatusCode) && attempt < maxRetries {
			if !sleep(ctx, backoffFor(response.Header, attempt)) {
				return zero, ctx.Err()
			}
			continue
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return zero, apiError(response.StatusCode, payload)
		}

		var result envelope[T]
		if err := json.Unmarshal(payload, &result); err != nil {
			return zero, fmt.Errorf("decode Cloudflare response: %w", err)
		}
		if !result.Success {
			return zero, &APIError{StatusCode: response.StatusCode, Errors: safeMessages(result.Errors), Body: safeBody(payload)}
		}
		return result, nil
	}
}

func readResponse(body io.ReadCloser) ([]byte, error) {
	defer body.Close()
	payload, err := io.ReadAll(io.LimitReader(body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxResponseBytes {
		return nil, fmt.Errorf("Cloudflare response exceeds %d bytes", maxResponseBytes)
	}
	return payload, nil
}

func apiError(statusCode int, payload []byte) *APIError {
	var failed struct {
		Errors []Message `json:"errors"`
	}
	_ = json.Unmarshal(payload, &failed)
	return &APIError{StatusCode: statusCode, Errors: safeMessages(failed.Errors), Body: safeBody(payload)}
}

func safeMessages(messages []Message) []Message {
	for i := range messages {
		messages[i].Message = textsafe.StripControls(messages[i].Message)
	}
	return messages
}

func safeBody(payload []byte) string {
	return textsafe.StripControls(strings.TrimSpace(string(payload)))
}

const (
	// maxRetries bounds automatic retries for transient Cloudflare errors.
	maxRetries = 5
	// baseBackoff is the starting delay; each attempt doubles it.
	baseBackoff = 400 * time.Millisecond
	// maxBackoff caps the per-attempt delay and any Retry-After hint.
	maxBackoff = 8 * time.Second
)

// retryable reports whether a response is transient under Cloudflare's load
// balancing. Callers must separately restrict replay to safe HTTP methods.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || (status >= 500 && status <= 599)
}

// backoffFor returns the wait before the given retry attempt. An explicit
// Retry-After value is honored as-is (subject to the maximum wait); otherwise
// a jittered exponential delay avoids synchronized bursts.
func backoffFor(h http.Header, attempt int) time.Duration {
	if value := h.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			hint := time.Duration(seconds) * time.Second
			if hint > maxBackoff {
				return maxBackoff
			}
			return hint
		}
		if when, err := http.ParseTime(value); err == nil {
			hint := time.Until(when)
			if hint < 0 {
				hint = 0
			}
			if hint > maxBackoff {
				return maxBackoff
			}
			return hint
		}
	}
	delay := baseBackoff << attempt
	if delay > maxBackoff {
		delay = maxBackoff
	}
	return delay + time.Duration(rand.Int64N(int64(delay/4)))
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
	return domain.Redirect{ID: r.ID, Source: textsafe.StripControls(r.Redirect.SourceURL), Target: textsafe.StripControls(r.Redirect.TargetURL),
		StatusCode: r.Redirect.StatusCode, IncludeSubdomains: r.Redirect.IncludeSubdomains,
		SubpathMatching: r.Redirect.SubpathMatching, PreserveQueryString: r.Redirect.PreserveQueryString,
		PreservePathSuffix: r.Redirect.PreservePathSuffix, Comment: textsafe.StripControls(r.Comment)}
}
