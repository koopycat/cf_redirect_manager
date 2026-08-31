// Package auth obtains Cloudflare credentials without exposing them.
package auth

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/zalando/go-keyring"
)

const (
	TokenEnv       = "CLOUDFLARE_API_TOKEN"
	KeyringService = "cf-redirect"
	KeyringUser    = "cloudflare-api-token"
)

var ErrTokenNotFound = errors.New("Cloudflare API token not found")

// Keyring is the narrow credential-store abstraction used by Resolver.
type Keyring interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type OSKeyring struct{}

func (OSKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (OSKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}
func (OSKeyring) Delete(service, user string) error { return keyring.Delete(service, user) }

type Resolver struct {
	LookupEnv func(string) (string, bool)
	Keyring   Keyring
	Service   string
	User      string
	AccountID string
}

func NewResolver(accountID ...string) Resolver {
	r := Resolver{LookupEnv: os.LookupEnv, Keyring: OSKeyring{}, Service: KeyringService, User: KeyringUser}
	if len(accountID) > 0 {
		r.AccountID = strings.TrimSpace(accountID[0])
	}
	return r
}

// Token gives the environment token precedence, then consults the OS keychain.
func (r Resolver) Token() (string, error) {
	lookup := r.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if value, ok := lookup(TokenEnv); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), nil
	}
	if r.Keyring == nil {
		return "", ErrTokenNotFound
	}
	service, user := r.names()
	value, err := r.Keyring.Get(service, user)
	if err != nil {
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrTokenNotFound
		}
		return "", fmt.Errorf("read API token from keychain: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		return "", ErrTokenNotFound
	}
	return strings.TrimSpace(value), nil
}

func (r Resolver) Store(token string) error {
	if strings.TrimSpace(token) == "" {
		return fmt.Errorf("API token must not be empty")
	}
	if r.Keyring == nil {
		return fmt.Errorf("keychain is not configured")
	}
	service, user := r.names()
	if err := r.Keyring.Set(service, user, strings.TrimSpace(token)); err != nil {
		return fmt.Errorf("store API token in keychain: %w", err)
	}
	return nil
}

func (r Resolver) Delete() error {
	if r.Keyring == nil {
		return fmt.Errorf("keychain is not configured")
	}
	service, user := r.names()
	if err := r.Keyring.Delete(service, user); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete API token from keychain: %w", err)
	}
	return nil
}

func (r Resolver) names() (string, string) {
	service, user := r.Service, r.User
	if service == "" {
		service = KeyringService
	}
	if user == "" {
		user = KeyringUser
	}
	if accountID := strings.TrimSpace(r.AccountID); accountID != "" {
		user += ":" + accountID
	}
	return service, user
}
