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

func testResolver(accountID string, store keyringStore, lookup func(string) (string, bool)) Resolver {
	resolver := NewResolver(accountID)
	resolver.keyring = store
	resolver.lookupEnv = lookup
	return resolver
}

func TestTokenEnvironmentPrecedesKeyring(t *testing.T) {
	store := &fakeKeyring{value: "keyring-token", err: errors.New("must not be called")}
	resolver := testResolver("account", store, func(string) (string, bool) { return " env-token ", true })
	got, err := resolver.Token()
	if err != nil || got != "env-token" {
		t.Fatalf("Token() = %q, %v", got, err)
	}
}

func TestTokenFallsBackToAccountScopedKeyring(t *testing.T) {
	store := &fakeKeyring{value: "keyring-token"}
	resolver := testResolver(" account-a ", store, func(string) (string, bool) { return "", false })
	got, err := resolver.Token()
	if err != nil || got != "keyring-token" {
		t.Fatalf("Token() = %q, %v", got, err)
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
	resolver := testResolver("account", &fakeKeyring{err: keyring.ErrNotFound}, func(string) (string, bool) { return "", false })
	if _, err := resolver.Token(); !errors.Is(err, ErrTokenNotFound) {
		t.Fatalf("expected ErrTokenNotFound, got %v", err)
	}
	if err := resolver.Store(" "); err == nil {
		t.Fatal("expected empty token error")
	}
}

func TestResolverRequiresAccountID(t *testing.T) {
	resolver := testResolver(" ", &fakeKeyring{}, func(string) (string, bool) { return "token", true })
	if _, err := resolver.Token(); err == nil {
		t.Fatal("Token must reject an empty account ID")
	}
	if err := resolver.Store("token"); err == nil {
		t.Fatal("Store must reject an empty account ID")
	}
	if err := resolver.Delete(); err == nil {
		t.Fatal("Delete must reject an empty account ID")
	}
}
