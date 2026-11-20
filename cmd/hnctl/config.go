package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// config is what hnctl remembers between runs: which server, and the login
// token it was given. The token is a secret (anyone who has it can act as the
// user until it expires), so the file is private to its owner.
type config struct {
	Server   string `json:"server"`
	Token    string `json:"token"`
	Username string `json:"username"`
}

// configPath is where the config lives: the HNCTL_CONFIG environment variable if
// it is set (handy for tests and for several logins side by side), otherwise
// hnctl/config.json in the user's config folder.
func configPath() (string, error) {
	if path := os.Getenv("HNCTL_CONFIG"); path != "" {
		return path, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the config folder: %w", err)
	}
	return filepath.Join(dir, "hnctl", "config.json"), nil
}

var errNotLoggedIn = errors.New("not logged in: run `hnctl login -server URL` first")

func loadConfig() (config, error) {
	path, err := configPath()
	if err != nil {
		return config{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return config{}, errNotLoggedIn
	}
	if err != nil {
		return config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var c config
	if err := json.Unmarshal(data, &c); err != nil {
		return config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.Server == "" || c.Token == "" {
		return config{}, errNotLoggedIn
	}
	return c, nil
}

func saveConfig(c config) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create the config folder: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	// 0600: only the owner can read it (it holds the login token). On Windows
	// these permission bits do nothing, and the file stays inside the user's
	// own profile folder, which other users cannot read.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func deleteConfig() error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// normalizeServer checks the server address and removes a trailing slash.
// The scheme must be given: guessing between http and https could send a
// password over plain HTTP by accident.
func normalizeServer(s string) (string, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return "", errors.New("the server address must start with http:// or https://")
	}
	if strings.Contains(s[strings.Index(s, "://")+3:], "/") {
		return "", errors.New("the server address must be just the host and port, like https://headnode.example.com")
	}
	return s, nil
}
