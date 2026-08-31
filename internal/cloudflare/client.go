// Package cloudflare is a focused, typed HTTP client for Bulk Redirect Lists.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/koopycat/cf-redirect/internal/domain"
)

const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

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
	switch strings.ToLower(o.Status) {
	case "completed", "complete", "success", "succeeded":
		return true, o.Error == "" && o.ErrorCount == 0
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

func (c *Client) WaitBulkOperation(ctx context.Context, accountID, operationID string, interval time.Duration) (BulkOperation, error) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
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
		}
	}
}

func (c *Client) do(ctx context.Context, method, path string, body any, destination any) error {
	baseURL := strings.TrimRight(c.BaseURL, "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failed envelope[json.RawMessage]
		_ = json.Unmarshal(payload, &failed)
		return &APIError{StatusCode: response.StatusCode, Errors: failed.Errors, Body: strings.TrimSpace(string(payload))}
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
	return domain.Redirect{ID: r.ID, Source: r.Redirect.SourceURL, Target: r.Redirect.TargetURL,
		StatusCode: r.Redirect.StatusCode, IncludeSubdomains: r.Redirect.IncludeSubdomains,
		SubpathMatching: r.Redirect.SubpathMatching, PreserveQueryString: r.Redirect.PreserveQueryString,
		PreservePathSuffix: r.Redirect.PreservePathSuffix, Comment: r.Comment}
}
