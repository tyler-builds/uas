// Package catalog defines the app catalog that the indexer builds and the client reads.
//
// The catalog is a SQLite database built fresh on every indexer run. It is never migrated in place: a client
// that finds an unknown schema version refuses it and waits for a matching catalog.
package catalog

import (
	"fmt"
	"time"
)

// SchemaVersion is the version of the catalog database layout. Bump it on any incompatible change.
const SchemaVersion = 2

// SourceID names a place apps come from.
type SourceID string

// The sources in the Bazzite slice.
const (
	SourceFlathub  SourceID = "flathub"
	SourceFedora   SourceID = "fedora"
	SourceCOPR     SourceID = "copr"
	SourceHomebrew SourceID = "homebrew"
	SourceGitHub   SourceID = "github"
)

// Name is the source's name for people, such as "Flathub".
func (s SourceID) Name() string {
	switch s {
	case SourceFlathub:
		return "Flathub"
	case SourceFedora:
		return "Fedora"
	case SourceHomebrew:
		return "Homebrew"
	case SourceCOPR:
		return "COPR"
	case SourceGitHub:
		return "GitHub"
	}
	return string(s)
}

// SourcePrecedence orders sources for display: when several listings describe one app, fields such as the
// name and screenshots come from the earliest source in this list that has them.
var SourcePrecedence = []SourceID{SourceFlathub, SourceFedora, SourceHomebrew, SourceCOPR, SourceGitHub}

// Kind is the AppStream component type of an app.
type Kind string

// Kinds kept in the catalog.
const (
	KindDesktopApp Kind = "desktop-application"
	KindConsoleApp Kind = "console-application"
)

// Screenshot is one screenshot of an app.
type Screenshot struct {
	URL      string `json:"url"`
	ThumbURL string `json:"thumb_url,omitempty"`
	Caption  string `json:"caption,omitempty"`
}

// Listing is one app as offered by one source.
type Listing struct {
	Source SourceID `json:"source"`
	// Ref identifies the listing within its source, for example a Flatpak ref such as
	// "app/org.example.App/x86_64/stable".
	Ref         string `json:"ref"`
	AppStreamID string `json:"appstream_id,omitempty"`
	Kind        Kind   `json:"kind"`

	Name        string `json:"name"`
	Summary     string `json:"summary,omitempty"`
	Description string `json:"description,omitempty"` // Markdown
	Developer   string `json:"developer,omitempty"`
	License     string `json:"license,omitempty"`

	Homepage   string `json:"homepage,omitempty"`
	VCSURL     string `json:"vcs_url,omitempty"`
	BugTracker string `json:"bugtracker,omitempty"`

	IconURL     string       `json:"icon_url,omitempty"`
	Screenshots []Screenshot `json:"screenshots,omitempty"`
	Categories  []string     `json:"categories,omitempty"`
	Keywords    []string     `json:"keywords,omitempty"`

	Version  string    `json:"version,omitempty"`
	Released time.Time `json:"released,omitzero"`

	// Verified is true when the source has confirmed the publisher (Flathub's verified badge).
	Verified bool `json:"verified,omitempty"`
	// VerifiedAs says who the publisher was verified as, such as a website domain.
	VerifiedAs string `json:"verified_as,omitempty"`

	// Runtime is the Flatpak runtime the app needs, if any.
	Runtime string `json:"runtime,omitempty"`

	// Installs is a yearly install count, where the source publishes one (Homebrew: installs on request in the
	// past year; Flathub: last month's installs times 12). It ranks search results and browsing.
	Installs int `json:"installs,omitempty"`

	// Package is the name of the package to install, for package-based sources such as Fedora.
	Package string `json:"package,omitempty"`
	// Distro limits the listing to one distribution release, such as "fedora-45". Empty means any.
	Distro string `json:"distro,omitempty"`
	// Project is the COPR project ("owner/project") a COPR listing comes from.
	Project string `json:"project,omitempty"`

	// Assets are downloadable files, for sources that ship release files (GitHub), best first.
	Assets []Asset `json:"assets,omitempty"`
	// Trust explains how far a listing from an unreviewed source (GitHub, COPR) can be trusted.
	Trust *Trust `json:"trust,omitempty"`
}

// Asset kinds.
const (
	AssetAppImage = "appimage"
	AssetFlatpak  = "flatpak"
	AssetRPM      = "rpm"
)

// Asset is one downloadable release file.
type Asset struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256,omitempty"` // hex; empty when the source publishes no checksum
	Downloads int    `json:"downloads,omitempty"`
}

// Asset returns the listing's first asset of the given kind.
func (l *Listing) Asset(kind string) (Asset, bool) {
	for _, a := range l.Assets {
		if a.Kind == kind {
			return a, true
		}
	}
	return Asset{}, false
}

// Trust is a 0-100 score with the signals behind it. Flags are warnings shown whatever the score.
type Trust struct {
	Score   int           `json:"score"`
	Signals []TrustSignal `json:"signals"`
	Flags   []string      `json:"flags,omitempty"`
}

// TrustSignal is one part of a trust score.
type TrustSignal struct {
	Name   string `json:"name"`   // for example "Stars"
	Detail string `json:"detail"` // for example "1,204 stars"
	Points int    `json:"points"`
	Max    int    `json:"max"`
}

// FedoraDistro returns the Distro value for a Fedora release.
func FedoraDistro(release int) string { return fmt.Sprintf("fedora-%d", release) }

// App is one application, combining the listings that describe it across sources.
type App struct {
	ID          string
	Kind        Kind
	Name        string
	Summary     string
	Description string // Markdown
	Developer   string
	License     string
	Homepage    string
	VCSURL      string
	IconURL     string
	Categories  []string
	Keywords    []string
	Screenshots []Screenshot
	// Popularity is the highest Installs of the app's listings.
	Popularity int
	// Listings are ordered by SourcePrecedence.
	Listings []Listing
}

// Verified reports whether any listing of the app has a verified publisher.
func (a *App) Verified() bool {
	for _, l := range a.Listings {
		if l.Verified {
			return true
		}
	}
	return false
}

// SourceStatus records how fetching one source went during a build.
type SourceStatus struct {
	Source    SourceID  `json:"source"`
	Listings  int       `json:"listings"`
	FetchedAt time.Time `json:"fetched_at"`
	Error     string    `json:"error,omitempty"`
}

// BuildInfo describes one catalog build.
type BuildInfo struct {
	SchemaVersion int            `json:"schema_version"`
	BuiltAt       time.Time      `json:"built_at"`
	Apps          int            `json:"apps"`
	Sources       []SourceStatus `json:"sources"`
}

// SearchResult is one row of search output.
type SearchResult struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Summary  string     `json:"summary"`
	Kind     Kind       `json:"kind"`
	IconURL  string     `json:"icon_url,omitempty"`
	Sources  []SourceID `json:"sources"`
	Verified bool       `json:"verified"`
}
