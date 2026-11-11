// Package state records what uas installed itself, for sources that have no package manager to ask
// (GitHub releases, COPR repos, and AppImages added from files).
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
// Codeberg installs are recorded the same way, with Source set.
type GitHubInstall struct {
	Repo string `json:"repo"` // the listing ref: "owner/repo", or "codeberg.org/owner/repo" and the like
	// Source is the release source, "codeberg"; empty for GitHub.
	Source string `json:"source,omitempty"`
	// Kind is the release file installed: an AppImage ("" or "appimage") or a Flatpak bundle ("flatpak"),
	// which flatpak keeps as FlatpakRef.
	Kind        string `json:"kind,omitempty"`
	FlatpakRef  string `json:"flatpak_ref,omitempty"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Asset       string `json:"asset"`
	SHA256      string `json:"sha256"`
	Path        string `json:"path"`
	DesktopFile string `json:"desktop_file,omitempty"`
	IconFile    string `json:"icon_file,omitempty"`
	// WMClass is the launcher's StartupWMClass when it came from a launcher uas took over, not the AppImage.
	WMClass string `json:"wm_class,omitempty"`
	// ExecArgs are the launcher's launch arguments when they came from a launcher uas took over, because the
	// AppImage's own launcher couldn't be read to take them from.
	ExecArgs string `json:"exec_args,omitempty"`
	// FromURL is true when the user installed the app from a link to its repo and the catalog didn't have it
	// then. It updates from the repo's releases, read from the forge, for as long as the catalog doesn't
	// have it.
	FromURL     bool      `json:"from_url,omitempty"`
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

// FileAppImage is an AppImage the user added from a file, which updates from where the AppImage itself says.
type FileAppImage struct {
	ID      string `json:"id"` // the app's ID, from its metainfo or desktop entry
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// Update is the AppImage's update information, such as "gh-releases-zsync|owner|repo|latest|*.zsync",
	// or "" when it has none and can't update itself.
	Update      string    `json:"update,omitempty"`
	SHA256      string    `json:"sha256"`
	SHA512      string    `json:"sha512,omitempty"` // electron-updater feeds give only SHA-512
	SHA1        string    `json:"sha1"`             // zsync files give only SHA-1
	Path        string    `json:"path"`
	DesktopFile string    `json:"desktop_file,omitempty"`
	IconFile    string    `json:"icon_file,omitempty"`
	WMClass     string    `json:"wm_class,omitempty"`  // as GitHubInstall.WMClass
	ExecArgs    string    `json:"exec_args,omitempty"` // as GitHubInstall.ExecArgs
	InstalledAt time.Time `json:"installed_at"`
}

// State is everything recorded.
type State struct {
	GitHub    []GitHubInstall `json:"github"`
	COPR      []COPRInstall   `json:"copr,omitempty"`
	AppImages []FileAppImage  `json:"appimages,omitempty"`
}

// FindAppImage returns the added AppImage with id.
func (s *State) FindAppImage(id string) (FileAppImage, bool) {
	for _, a := range s.AppImages {
		if a.ID == id {
			return a, true
		}
	}
	return FileAppImage{}, false
}

// PutAppImage adds or replaces the AppImage with a.ID.
func (s *State) PutAppImage(a FileAppImage) {
	for i := range s.AppImages {
		if s.AppImages[i].ID == a.ID {
			s.AppImages[i] = a
			return
		}
	}
	s.AppImages = append(s.AppImages, a)
}

// DeleteAppImage removes the AppImage with id.
func (s *State) DeleteAppImage(id string) {
	out := s.AppImages[:0]
	for _, a := range s.AppImages {
		if a.ID != id {
			out = append(out, a)
		}
	}
	s.AppImages = out
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
