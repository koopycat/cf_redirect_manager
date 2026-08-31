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

// keyringStore is the narrow seam used to substitute a credential store in
// tests without exposing keyring configuration in the production API.
type keyringStore interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type osKeyring struct{}

func (osKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (osKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}
func (osKeyring) Delete(service, user string) error { return keyring.Delete(service, user) }

type Resolver struct {
	accountID string
	keyring   keyringStore
	lookupEnv func(string) (string, bool)
}

func NewResolver(accountID string) Resolver {
	return Resolver{
		accountID: strings.TrimSpace(accountID),
		keyring:   osKeyring{},
		lookupEnv: os.LookupEnv,
	}
}

// Token gives the environment token precedence, then consults the OS keychain.
func (r Resolver) Token() (string, error) {
	if r.accountID == "" {
		return "", fmt.Errorf("Cloudflare account ID is required")
	}
	lookup := r.lookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if value, ok := lookup(TokenEnv); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), nil
	}
	store := r.keyring
	if store == nil {
		store = osKeyring{}
	}
	value, err := store.Get(KeyringService, r.keyringUser())
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
	if r.accountID == "" {
		return fmt.Errorf("Cloudflare account ID is required")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("API token must not be empty")
	}
	store := r.keyring
	if store == nil {
		store = osKeyring{}
	}
	if err := store.Set(KeyringService, r.keyringUser(), token); err != nil {
		return fmt.Errorf("store API token in keychain: %w", err)
	}
	return nil
}

func (r Resolver) Delete() error {
	if r.accountID == "" {
		return fmt.Errorf("Cloudflare account ID is required")
	}
	store := r.keyring
	if store == nil {
		store = osKeyring{}
	}
	if err := store.Delete(KeyringService, r.keyringUser()); err != nil && !errors.Is(err, keyring.ErrNotFound) {
		return fmt.Errorf("delete API token from keychain: %w", err)
	}
	return nil
}

func (r Resolver) keyringUser() string {
	return KeyringUser + ":" + r.accountID
}
