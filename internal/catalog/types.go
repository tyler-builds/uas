// Package catalog defines the app catalog that the indexer builds and the client reads.
//
// The catalog is a SQLite database built fresh on every indexer run. It is never migrated in place: a client
// that finds an unknown schema version refuses it and waits for a matching catalog.
package catalog

import (
	"fmt"
	"strings"
	"time"
)

// SchemaVersion is the version of the catalog database layout. Bump it on any incompatible change.
const SchemaVersion = 3

// SourceID names a place apps come from.
type SourceID string

// The sources in the Bazzite slice.
const (
	SourceFlathub  SourceID = "flathub"
	SourceFedora   SourceID = "fedora"
	SourceCOPR     SourceID = "copr"
	SourceHomebrew SourceID = "homebrew"
	SourceGitHub   SourceID = "github"
	// SourceCodeberg is release files on codeberg.org, like GitHub's. Its listing refs name the host:
	// "codeberg.org/owner/repo".
	SourceCodeberg SourceID = "codeberg"
	// SourceDebian, SourceUbuntu and SourceArch are distribution packages, installed with apt or pacman.
	// Like Fedora's, their listings carry a Distro ("debian-13", "ubuntu-24.04", "arch") and a Package.
	SourceDebian SourceID = "debian"
	SourceUbuntu SourceID = "ubuntu"
	SourceArch   SourceID = "arch"
	// SourceSnap is the Snap Store, installed with snap.
	SourceSnap SourceID = "snap"
	// SourceAppImage is AppImages the user added from files. The catalog has no listings from it.
	SourceAppImage SourceID = "appimage"
	// SourceGNOMEExtensions is extensions.gnome.org. Its listings are GNOME Shell extensions
	// (KindShellExtension), not apps: their ref is the extension's UUID, the Shell installs them itself, and
	// they are listed and searched apart from apps. It is not in SourcePrecedence.
	SourceGNOMEExtensions SourceID = "gnome-extensions"
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
	case SourceCodeberg:
		return "Codeberg"
	case SourceDebian:
		return "Debian"
	case SourceUbuntu:
		return "Ubuntu"
	case SourceArch:
		return "Arch Linux"
	case SourceSnap:
		return "Snap Store"
	case SourceAppImage:
		return "AppImage"
	case SourceGNOMEExtensions:
		return "GNOME Extensions"
	}
	return string(s)
}

// SourcePrecedence orders sources for display: when several listings describe one app, fields such as the
// name and screenshots come from the earliest source in this list that has them.
var SourcePrecedence = []SourceID{SourceFlathub, SourceFedora, SourceDebian, SourceUbuntu, SourceArch,
	SourceHomebrew, SourceSnap, SourceCOPR, SourceGitHub, SourceCodeberg}

// Distribution reports whether the source is a Linux distribution's own packages (Fedora, Debian, Ubuntu,
// Arch), which the distribution builds and signs.
func (s SourceID) Distribution() bool {
	return s == SourceFedora || s == SourceDebian || s == SourceUbuntu || s == SourceArch
}

// Releases reports whether the source is a code forge's release files (GitHub, Codeberg), which uas
// downloads and installs itself.
func (s SourceID) Releases() bool {
	return s == SourceGitHub || s == SourceCodeberg
}

// Host is the web host of a release source, such as "github.com".
func (s SourceID) Host() string {
	switch s {
	case SourceGitHub:
		return "github.com"
	case SourceCodeberg:
		return "codeberg.org"
	}
	return ""
}

// Repo returns a release listing's repo path on its host: "owner/repo" for GitHub, whose refs are that path,
// and the ref without its host for Codeberg.
func (s SourceID) Repo(ref string) string {
	if s == SourceGitHub {
		return ref
	}
	return strings.TrimPrefix(ref, s.Host()+"/")
}

// Kind is the AppStream component type of an app.
type Kind string

// Kinds kept in the catalog.
const (
	KindDesktopApp Kind = "desktop-application"
	KindConsoleApp Kind = "console-application"
	// KindShellExtension is a GNOME Shell extension. Extensions are in the catalog's app tables but have their
	// own search index, and Search and Browse return them only when asked (see SearchOptions.Extensions).
	KindShellExtension Kind = "shell-extension"
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

	Name    string `json:"name"`
	Summary string `json:"summary,omitempty"`
	// Description is Markdown, except for a shell extension's, which is the plain text its author wrote.
	Description string `json:"description,omitempty"`
	Developer   string `json:"developer,omitempty"`
	License     string `json:"license,omitempty"`

	Homepage   string `json:"homepage,omitempty"`
	VCSURL     string `json:"vcs_url,omitempty"`
	BugTracker string `json:"bugtracker,omitempty"`
	// StoreURL is the listing's own page on its source's site, for sources uas links to
	// (extensions.gnome.org).
	StoreURL string `json:"store_url,omitempty"`

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
	// past year; Flathub: last month's installs times 12). It ranks search results and browsing. For a shell
	// extension it is the downloads extensions.gnome.org counts, over all time.
	Installs int `json:"installs,omitempty"`

	// Stars is the GitHub repository's star count, for GitHub listings.
	Stars int `json:"stars,omitempty"`

	// Package is the name of the package to install, for package-based sources such as Fedora.
	Package string `json:"package,omitempty"`
	// Distro limits the listing to one distribution release, such as "fedora-45". Empty means any.
	Distro string `json:"distro,omitempty"`
	// Project is the COPR project ("owner/project") a COPR listing comes from.
	Project string `json:"project,omitempty"`
	// Classic is true for a snap with classic confinement, which runs without the snap sandbox and installs
	// with "snap install --classic".
	Classic bool `json:"classic,omitempty"`
	// Shells are the GNOME Shell releases a shell extension has a version for, by major version ("48", "51"),
	// oldest first.
	Shells []string `json:"shells,omitempty"`

	// Changelog is what changed in the newest releases, newest first, where the source publishes release notes:
	// AppStream release descriptions, or a code forge's release notes.
	Changelog []Change `json:"changelog,omitempty"`

	// Assets are downloadable files, for sources that ship release files (GitHub), best first.
	Assets []Asset `json:"assets,omitempty"`
	// Trust explains how far a listing from an unreviewed source (GitHub, COPR) can be trusted.
	Trust *Trust `json:"trust,omitempty"`
}

// Change is one release's notes.
type Change struct {
	Version string    `json:"version"`
	Date    time.Time `json:"date,omitzero"`
	Notes   string    `json:"notes"` // Markdown, shortened to a few paragraphs
	// URL is where the full notes are, when the source has a page for them.
	URL string `json:"url,omitempty"`
	// Cut is true when Notes was shortened.
	Cut bool `json:"cut,omitempty"`
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

// ExtensionAppID returns the catalog ID of the shell extension with a UUID.
func ExtensionAppID(uuid string) string { return string(SourceGNOMEExtensions) + ":" + uuid }

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
	// Stars is the highest Stars of the app's listings (GitHub).
	Stars int
	// Updated is the newest Released of the app's listings.
	Updated time.Time
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
	SchemaVersion int       `json:"schema_version"`
	BuiltAt       time.Time `json:"built_at"`
	// Apps counts the apps, without shell extensions.
	Apps    int            `json:"apps"`
	Sources []SourceStatus `json:"sources"`
	// Extensions is how fetching shell extensions went, with their count. It is kept out of Sources because
	// versions of uas from before extensions list every source there and know nothing about this one.
	Extensions *SourceStatus `json:"extensions,omitempty"`
}

// SearchResult is one row of search output.
type SearchResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
	// Developer is who makes the app, as its sources name them ("The Godot Engine Community"), or the
	// GitHub account for a GitHub-only app.
	Developer string     `json:"developer,omitempty"`
	Kind      Kind       `json:"kind"`
	IconURL   string     `json:"icon_url,omitempty"`
	Sources   []SourceID `json:"sources"`
	Verified  bool       `json:"verified"`
	// Popularity is the app's yearly installs (see App.Popularity), Stars its GitHub stars, and Updated the
	// date of the newest release of any of its listings.
	Popularity int       `json:"popularity,omitempty"`
	Stars      int       `json:"stars,omitempty"`
	Updated    time.Time `json:"updated,omitzero"`
	// Refs identify the app's listings, so a list can tell which apps are installed.
	Refs []ListingRef `json:"refs,omitempty"`
}

// ListingRef identifies a listing without its details.
type ListingRef struct {
	Source  SourceID `json:"source"`
	Ref     string   `json:"ref"`
	Package string   `json:"package,omitempty"`
}
