package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	env := map[string]string{AccountIDEnv: "env-account", ListIDEnv: "env-list"}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }
	got, err := ResolveWithLookup("flag-account", "", lookup)
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != "flag-account" || got.ListID != "env-list" {
		t.Fatalf("unexpected config: %#v", got)
	}
}

func TestResolveValuesPrecedenceFlagsThenEnvThenFile(t *testing.T) {
	env := map[string]string{AccountIDEnv: "env-account"}
	lookup := func(key string) (string, bool) { value, ok := env[key]; return value, ok }
	got, err := ResolveValues("flag-account", "", lookup, Config{AccountID: "file-account", ListID: "file-list"})
	if err != nil {
		t.Fatal(err)
	}
	if got.AccountID != "flag-account" || got.ListID != "file-list" {
		t.Fatalf("unexpected config: %#v", got)
	}
}

func TestSaveIsAtomicAndPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	want := Config{AccountID: "account", ListID: "list"}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got != want {
		t.Fatalf("Load() = %#v, %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files remain: %v, %v", matches, err)
	}
}

func TestResolveRequiresBothIDs(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	if _, err := ResolveWithLookup("", "list", lookup); err == nil {
		t.Fatal("expected missing account error")
	}
	if _, err := ResolveWithLookup("account", "", lookup); err == nil {
		t.Fatal("expected missing list error")
	}
}
