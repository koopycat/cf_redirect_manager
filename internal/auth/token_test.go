package auth

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

type fakeKeyring struct {
	value      string
	err        error
	sets       []string
	lastUser   string
	deleteUser string
}

func (f *fakeKeyring) Get(_ string, user string) (string, error) {
	f.lastUser = user
	return f.value, f.err
}
func (f *fakeKeyring) Set(_, user, value string) error {
	f.lastUser = user
	f.sets = append(f.sets, value)
	return f.err
}
func (f *fakeKeyring) Delete(_ string, user string) error {
	f.deleteUser = user
	return f.err
}

func TestTokenEnvironmentPrecedesKeyring(t *testing.T) {
	store := &fakeKeyring{value: "keyring-token", err: errors.New("must not be called")}
	resolver := Resolver{LookupEnv: func(string) (string, bool) { return " env-token ", true }, Keyring: store}
	got, err := resolver.Token()
	if err != nil || got != "env-token" {
		t.Fatalf("Token() = %q, %v", got, err)
	}
}

func TestTokenFallsBackToKeyring(t *testing.T) {
	store := &fakeKeyring{value: "keyring-token"}
	resolver := Resolver{LookupEnv: func(string) (string, bool) { return "", false }, Keyring: store}
	got, err := resolver.Token()
	if err != nil || got != "keyring-token" {
		t.Fatalf("Token() = %q, %v", got, err)
	}
}

func TestKeyringCredentialIsAccountSpecific(t *testing.T) {
	store := &fakeKeyring{value: "token"}
	resolver := Resolver{LookupEnv: func(string) (string, bool) { return "", false }, Keyring: store, AccountID: "account-a"}
	if _, err := resolver.Token(); err != nil {
		t.Fatal(err)
	}
	if store.lastUser != KeyringUser+":account-a" {
		t.Fatalf("keyring user = %q", store.lastUser)
	}
	if err := resolver.Delete(); err != nil {
		t.Fatal(err)
	}
	if store.deleteUser != KeyringUser+":account-a" {
		t.Fatalf("delete keyring user = %q", store.deleteUser)
	}
}

func TestTokenNotFoundAndStoreValidation(t *testing.T) {
	resolver := Resolver{LookupEnv: func(string) (string, bool) { return "", false }, Keyring: &fakeKeyring{err: keyring.ErrNotFound}}
	if _, err := resolver.Token(); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("expected ErrTokenNotFound, got %v", err)
	}
	if err := resolver.Store(" "); err == nil {
		t.Fatal("expected empty token error")
	}
}
