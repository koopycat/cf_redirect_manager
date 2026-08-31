// Package config resolves and persists the one account and redirect-list identity.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	AccountIDEnv = "CLOUDFLARE_ACCOUNT_ID"
	ListIDEnv    = "CLOUDFLARE_LIST_ID"
	AppDir       = "cf-redirect"
	FileName     = "config.json"
)

type Config struct {
	AccountID string `json:"account_id"`
	ListID    string `json:"list_id"`
}

// Resolve applies precedence flags > environment > persisted config. The
// persisted file is consulted only when flags and environment do not already
// provide a complete identity, so a corrupt file cannot block higher-precedence
// overrides.
func Resolve(accountID, listID string) (Config, error) {
	cfg := Config{AccountID: strings.TrimSpace(accountID), ListID: strings.TrimSpace(listID)}
	if cfg.AccountID == "" {
		cfg.AccountID, _ = os.LookupEnv(AccountIDEnv)
		cfg.AccountID = strings.TrimSpace(cfg.AccountID)
	}
	if cfg.ListID == "" {
		cfg.ListID, _ = os.LookupEnv(ListIDEnv)
		cfg.ListID = strings.TrimSpace(cfg.ListID)
	}
	if cfg.AccountID == "" || cfg.ListID == "" {
		path, err := Path()
		if err != nil {
			return Config{}, err
		}
		fileConfig, err := Load(path)
		if err != nil {
			return Config{}, err
		}
		if cfg.AccountID == "" {
			cfg.AccountID = strings.TrimSpace(fileConfig.AccountID)
		}
		if cfg.ListID == "" {
			cfg.ListID = strings.TrimSpace(fileConfig.ListID)
		}
	}
	if cfg.AccountID == "" {
		return Config{}, fmt.Errorf("Cloudflare account ID is required (flag, %s, or config file)", AccountIDEnv)
	}
	if cfg.ListID == "" {
		return Config{}, fmt.Errorf("Cloudflare list ID is required (flag, %s, or config file)", ListIDEnv)
	}
	return cfg, nil
}

// ResolveWithLookup is retained for callers that only need flags and env.
func ResolveWithLookup(accountID, listID string, lookup func(string) (string, bool)) (Config, error) {
	return ResolveValues(accountID, listID, lookup, Config{})
}

func ResolveValues(accountID, listID string, lookup func(string) (string, bool), fileConfig Config) (Config, error) {
	if accountID == "" {
		accountID, _ = lookup(AccountIDEnv)
	}
	if accountID == "" {
		accountID = fileConfig.AccountID
	}
	if listID == "" {
		listID, _ = lookup(ListIDEnv)
	}
	if listID == "" {
		listID = fileConfig.ListID
	}
	cfg := Config{AccountID: strings.TrimSpace(accountID), ListID: strings.TrimSpace(listID)}
	if cfg.AccountID == "" {
		return Config{}, fmt.Errorf("Cloudflare account ID is required (flag, %s, or config file)", AccountIDEnv)
	}
	if cfg.ListID == "" {
		return Config{}, fmt.Errorf("Cloudflare list ID is required (flag, %s, or config file)", ListIDEnv)
	}
	return cfg, nil
}

func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(dir, AppDir, FileName), nil
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Config{}, fmt.Errorf("decode config %s: trailing data after JSON object", path)
	}
	cfg.AccountID = strings.TrimSpace(cfg.AccountID)
	cfg.ListID = strings.TrimSpace(cfg.ListID)
	return cfg, nil
}

// Save atomically replaces path with a file readable only by the user.
func Save(path string, cfg Config) error {
	cfg.AccountID = strings.TrimSpace(cfg.AccountID)
	cfg.ListID = strings.TrimSpace(cfg.ListID)
	if cfg.AccountID == "" || cfg.ListID == "" {
		return fmt.Errorf("account ID and list ID are required")
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
