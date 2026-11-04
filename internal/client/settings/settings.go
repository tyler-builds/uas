// Package settings loads and saves the user's uas settings.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// FlatpakScope says where Flatpak apps are installed.
type FlatpakScope string

const (
	// ScopeUser installs for the current user only. No password prompt.
	ScopeUser FlatpakScope = "user"
	// ScopeSystem installs for every user. Flatpak asks for a password through polkit.
	ScopeSystem FlatpakScope = "system"
)

// Valid reports whether s is a known scope.
func (s FlatpakScope) Valid() bool { return s == ScopeUser || s == ScopeSystem }

// Settings are the user's preferences. The zero value is not valid; use Default.
type Settings struct {
	FlatpakScope FlatpakScope `json:"flatpak_scope"`
	// Telemetry is off unless the user turns it on. Nothing is collected in this version either way.
	Telemetry bool `json:"telemetry"`
}

// Default returns the settings used before the user changes anything.
func Default() Settings {
	return Settings{FlatpakScope: ScopeUser, Telemetry: false}
}

// Path returns $XDG_CONFIG_HOME/uas/settings.json.
func Path() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "uas", "settings.json"), nil
}

// Load reads settings from path. A missing file gives the defaults; missing fields keep their defaults.
func Load(path string) (Settings, error) {
	s := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Default(), fmt.Errorf("settings %s: %w", path, err)
	}
	if !s.FlatpakScope.Valid() {
		return Default(), fmt.Errorf("settings %s: unknown flatpak_scope %q", path, s.FlatpakScope)
	}
	return s, nil
}

// Save writes settings to path atomically.
func Save(path string, s Settings) error {
	if !s.FlatpakScope.Valid() {
		return fmt.Errorf("unknown flatpak scope %q", s.FlatpakScope)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
