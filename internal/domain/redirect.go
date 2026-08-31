// Package domain contains the redirect model shared by all interfaces.
package domain

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const DefaultStatusCode = 301

// Redirect is one item in a Cloudflare Bulk Redirect List. ID is assigned by
// Cloudflare. Comment and all redirect options are retained during edits.
type Redirect struct {
	ID                  string
	Source              string
	Target              string
	StatusCode          int
	IncludeSubdomains   bool
	SubpathMatching     bool
	PreserveQueryString bool
	PreservePathSuffix  bool
	Comment             string
}

// New creates a redirect with safe defaults.
func New(source, target string) Redirect {
	return Redirect{Source: source, Target: target, StatusCode: DefaultStatusCode}
}

// Validate checks the constraints that can be checked without contacting
// Cloudflare.
func (r Redirect) Validate() error {
	var errs []error
	if err := validateSourceURL(r.Source); err != nil {
		errs = append(errs, err)
	}
	if err := validateTargetURL(r.Target); err != nil {
		errs = append(errs, err)
	}
	if r.StatusCode != 0 && r.StatusCode != 301 && r.StatusCode != 302 && r.StatusCode != 307 && r.StatusCode != 308 {
		errs = append(errs, fmt.Errorf("status code must be 301, 302, 307, or 308 (got %d)", r.StatusCode))
	}
	return errors.Join(errs...)
}

func validateSourceURL(value string) error {
	if err := validateURLText("source", value); err != nil {
		return err
	}
	candidate := value
	if !strings.Contains(value, "://") {
		candidate = "https://" + value
	}
	u, err := url.ParseRequestURI(candidate)
	if err != nil || u.Host == "" {
		return fmt.Errorf("source must be an absolute or schemeless URL with a hostname")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("source URL scheme must be http or https when present")
	}
	if u.User != nil {
		return fmt.Errorf("source URL must not contain user information")
	}
	return nil
}

func validateTargetURL(value string) error {
	if err := validateURLText("target", value); err != nil {
		return err
	}
	u, err := url.ParseRequestURI(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("target must be an absolute URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("target URL scheme must be http or https")
	}
	if u.User != nil {
		return fmt.Errorf("target URL must not contain user information")
	}
	return nil
}

func validateURLText(field, value string) error {
	if strings.TrimSpace(value) != value || value == "" {
		return fmt.Errorf("%s must be a non-empty URL without surrounding whitespace", field)
	}
	if strings.Contains(value, "#") {
		return fmt.Errorf("%s URL must not contain a fragment", field)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s URL must not contain control characters", field)
		}
	}
	return nil
}

// EffectiveStatusCode returns the Cloudflare default for zero-valued models.
func (r Redirect) EffectiveStatusCode() int {
	if r.StatusCode == 0 {
		return DefaultStatusCode
	}
	return r.StatusCode
}

// EqualContent compares all user-controlled fields, excluding Cloudflare's ID.
func (r Redirect) EqualContent(other Redirect) bool {
	return r.Source == other.Source && r.Target == other.Target &&
		r.EffectiveStatusCode() == other.EffectiveStatusCode() &&
		r.IncludeSubdomains == other.IncludeSubdomains &&
		r.SubpathMatching == other.SubpathMatching &&
		r.PreserveQueryString == other.PreserveQueryString &&
		r.PreservePathSuffix == other.PreservePathSuffix &&
		r.Comment == other.Comment
}
