// Package state records what uas installed itself, for sources that have no package manager to ask
// (GitHub AppImages).
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GitHubInstall is one app installed from a GitHub release.
type GitHubInstall struct {
	Repo string `json:"repo"` // "owner/repo", the listing ref
	// Kind is the release file installed: an AppImage ("" or "appimage") or a Flatpak bundle ("flatpak"),
	// which flatpak keeps as FlatpakRef.
	Kind        string    `json:"kind,omitempty"`
	FlatpakRef  string    `json:"flatpak_ref,omitempty"`
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Asset       string    `json:"asset"`
	SHA256      string    `json:"sha256"`
	Path        string    `json:"path"`
	DesktopFile string    `json:"desktop_file,omitempty"`
	IconFile    string    `json:"icon_file,omitempty"`
	InstalledAt time.Time `json:"installed_at"`
}

// COPRInstall is one package layered from a COPR project.
type COPRInstall struct {
	Project  string `json:"project"` // "owner/project"
	Package  string `json:"package"`
	RepoFile string `json:"repo_file"`
	// AddedRepo is true when uas created RepoFile, so removing the last package may remove the repo too.
	AddedRepo   bool      `json:"added_repo"`
	InstalledAt time.Time `json:"installed_at"`
}

// State is everything recorded.
type State struct {
	GitHub []GitHubInstall `json:"github"`
	COPR   []COPRInstall   `json:"copr,omitempty"`
}

// FindCOPR returns the COPR install of pkg.
func (s *State) FindCOPR(pkg string) (COPRInstall, bool) {
	for _, c := range s.COPR {
		if c.Package == pkg {
			return c, true
		}
	}
	return COPRInstall{}, false
}

// PutCOPR adds or replaces the COPR install of c.Package.
func (s *State) PutCOPR(c COPRInstall) {
	for i := range s.COPR {
		if s.COPR[i].Package == c.Package {
			s.COPR[i] = c
			return
		}
	}
	s.COPR = append(s.COPR, c)
}

// DeleteCOPR removes the COPR install of pkg.
func (s *State) DeleteCOPR(pkg string) {
	out := s.COPR[:0]
	for _, c := range s.COPR {
		if c.Package != pkg {
			out = append(out, c)
		}
	}
	s.COPR = out
}

// Find returns the install of repo.
func (s *State) Find(repo string) (GitHubInstall, bool) {
	for _, g := range s.GitHub {
		if g.Repo == repo {
			return g, true
		}
	}
	return GitHubInstall{}, false
}

// Put adds or replaces the install of g.Repo.
func (s *State) Put(g GitHubInstall) {
	for i := range s.GitHub {
		if s.GitHub[i].Repo == g.Repo {
			s.GitHub[i] = g
			return
		}
	}
	s.GitHub = append(s.GitHub, g)
}

// Delete removes the install of repo.
func (s *State) Delete(repo string) {
	out := s.GitHub[:0]
	for _, g := range s.GitHub {
		if g.Repo != repo {
			out = append(out, g)
		}
	}
	s.GitHub = out
}

// Store is a state file.
type Store struct {
	Path string
}

// DefaultPath returns $XDG_DATA_HOME/uas/state.json (usually ~/.local/share/uas/state.json).
func DefaultPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "uas", "state.json"), nil
}

// DataDir returns $XDG_DATA_HOME, or ~/.local/share.
func DataDir() (string, error) {
	if d := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(d) {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share"), nil
}

// Load reads the state. A missing file is an empty state.
func (s Store) Load() (State, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("state %s: %w", s.Path, err)
	}
	return st, nil
}

// Save writes the state atomically.
func (s Store) Save(st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}
