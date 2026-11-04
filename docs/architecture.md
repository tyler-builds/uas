# uas architecture (draft): Bazzite vertical slice

Status: draft for review. Name: `uas`, always lowercase, never expanded in the UI. Module path
`github.com/tyler-builds/uas`. Private repo for now.

## Goal

A store for Bazzite that shows each app once, across five sources, recommends the best way to install it on an
immutable system, and explains how far each source can be trusted.

| Source | Install method | Root? | Reboot? |
| --- | --- | --- | --- |
| Flathub | `flatpak`, user scope by default | No (system scope: Flatpak's polkit prompt) | No |
| Homebrew | `brew` | No | No |
| GitHub releases | AppImage to home dir, `.flatpak` bundle, `.rpm` via rpm-ostree | Only for `.rpm` | Only for `.rpm` |
| Fedora repos | `rpm-ostree install` (layering) | rpm-ostree's polkit prompt | Yes |
| COPR | Write `.repo` file via `pkexec`, then layer | Yes | Yes |

Checked on the dev machine: Bazzite 44 (`ID=bazzite`, `ID_LIKE=fedora`), KDE Plasma on Wayland, Flathub as a
system remote, Homebrew at `/home/linuxbrew/.linuxbrew`, libfuse2 present, Go 1.27.3, rpm-ostree layering
works.

## System overview

```
            GitHub Actions (cron, every 6h)                  Cloudflare Worker + R2
 ┌───────────────────────────────────────────────┐        ┌──────────────────────────────┐
 │ uas-indexer                                   │        │ manifest.json + .sig         │
 │  fetch: flathub fedora copr homebrew github   │ ─────► │ catalog-<hash>.db.zst        │
 │  normalize → merge → trust score → build db   │ upload │ icons/                       │
 │  sign manifest (ed25519)                      │        └──────────────┬───────────────┘
 └───────────────────────────────────────────────┘                       │ HTTPS GET
                                                                          ▼
 ┌────────────────────────────── user's Bazzite machine ─────────────────────────────────┐
 │  uas-gui (Jamb)        uas (CLI)                                                       │
 │        └──────┬───────────┘                                                            │
 │          client core: platform · refresh · catalog · recommend · state · jobs       │
 │               │                                                                        │
 │          backends: flatpak · homebrew · github · rpmostree · copr                      │
 │               │            │          │            │            │                      │
 │           flatpak CLI   brew CLI   own installer  rpm-ostree   pkexec + rpm-ostree     │
 └────────────────────────────────────────────────────────────────────────────────────────┘
```

The only server code is a small Worker (`deploy/cloudflare`) in front of R2. It serves the published files and
accepts uploads from the indexer with a bearer token, so CI needs no R2 API keys. The indexer runs in CI, and
every install happens on the client as the user, with polkit prompts only where the system tools already
require them.

## Server side: the indexer

### Fetchers (one package per source)

All endpoints below need confirming during the first spike.

- **Flathub:** AppStream XML from the Flathub repo, plus the Flathub API for verified status and install counts.
- **Fedora:** Fedora ships its AppStream data inside the `appstream-data` RPM, not in repo metadata, so the
  fetcher reads the release and updates repos' `primary.xml` for package versions, then extracts the AppStream
  XML from that RPM (a small built-in RPM/cpio reader). It indexes the two newest releases on the mirror,
  because Bazzite can have the next release staged before it boots it. Each listing carries a `distro`
  (such as `fedora-45`) and a `package` name. Only components whose package is in the repos are kept. Icons
  are only in AppStream's icon tarballs, so Fedora-only apps have no icon until icons are mirrored to R2.
- **COPR:** COPR API v3 lists all ~43,000 projects (43 pages, about 4 minutes). Kept: projects listed on
  COPR's front page, with AppStream metadata turned on, building for one of the two newest Fedora releases
  (~430). Each one's chroot repo is read for AppStream data and package versions, eight projects at a time,
  and the project's last 30 builds give its trust score (see `docs/trust-score.md`). Most COPR repos carry no
  AppStream data even with the option on (108 of 777 repos in November 2026), so COPR adds only about 25 apps,
  among them Bazaar, Joplin, RStudio and gedit. Listings carry the project (`owner/project`). Results are
  cached for 20 hours like GitHub's.
- **Homebrew:** `formulae.brew.sh` JSON API plus install-on-request analytics. Keeps core formulae with a
  Linux bottle and at least 1,000 installs-on-request a year (about 2,500), which drops most libraries;
  keg-only, deprecated and disabled formulae are skipped. The install count is stored on the listing for
  search ranking later.
- **GitHub:** GraphQL searches (repos mentioning AppImage in their README, sliced by stars to stay under the
  1,000-result cap, plus the `appimage` and `linux-app` topics). Each search page also reads the repo's latest
  release with its files and the dates of recent releases. Asking for files of several releases per repo
  exceeds GitHub's per-query limits, and so did a separate batched release lookup, so this single pass is the
  design. A full pass is about 5 minutes (~2,000 repos, ~6 s per page of 50), so results are cached for 20
  hours and only one of the four daily runs searches. The workflow's built-in `GITHUB_TOKEN` is enough
  (about 30 GraphQL points per pass). Pages that GitHub refuses as too costly are retried at half size;
  timeouts and secondary rate limits are retried with backoff.

  Those searches miss apps that never call themselves an AppImage or Linux app (T3 Code ships an x86_64
  AppImage, but its repo has no description, no topics and no mention in its README). So a **sweep** also
  checks every repo with at least 2,000 stars pushed in the past year (about 21,000 repos). It splits star
  ranges until each search fits under the 1,000-result cap, reads only the latest release's file names
  (four searches at a time), and then reads the full details, by node ID, of the repos that have a Linux
  file. Discovery takes about 25 minutes, so its finds are kept for a week (`github/sweep.json` in the
  indexer cache); the found repos are re-read in full on every pass, which takes under a minute, so versions
  stay current and new apps appear within a week. A GitHub app's name comes from its release file when that spells the repo name
  (`T3-Code-0.1.7-x86_64.AppImage` makes `t3code` "T3 Code"), so searching for "t3 code" finds it.

  A repo says little about the app it ships, so the indexer also looks **inside each AppImage**, with HTTP
  range requests (`internal/indexer/fetch/rangeread`, about 400 KB and a second per app, never the whole
  file). It reads the same squashfs as the client (`internal/client/appimage.InspectAt`). The desktop entry
  gives the app's real name, a one-line summary (used when the repo has no description), categories and the
  icon. AppStream metainfo, when the AppImage ships one, gives a description and screenshots. Otherwise up
  to four screenshots come from the README: images named like screenshots first, never badges or logos,
  each checked with a HEAD request to be a picture. What was found is kept per release file
  (`github/media` in the indexer cache), so only new versions are read again.

  Icons are scaled to at most 256 px, named by their SHA-256 and uploaded to R2 before the catalog
  (`publish.Icons`, through `icons/<sha256>.png|svg` on the Worker). The Worker serves them as immutable,
  and serves SVGs with a sandboxing Content-Security-Policy, because they come from third parties. A
  GitHub listing never takes an AppStream ID from its metainfo. Otherwise any repo could claim a Flathub
  app's ID and be merged into it as one of its sources.

Each fetcher returns `[]catalog.Listing` (one app from one source) and stores raw responses as fixtures for tests.

Fetchers run in parallel, so the slow passes (GitHub, COPR) overlap. Slow sources are wrapped in
`fetch.Cached`, which reuses their last result for 20 hours (`-slow-cache-for`), so only one of the four daily
runs pays for them. Fedora and COPR share `fetch/rpmrepo` for repo metadata.

### Merge (deduplication)

Listings are linked into one `App` when, in order of confidence:

1. They share an AppStream ID (Flathub and Fedora).
2. They point at the same code-forge repo (`github.com/owner/repo` and the like), from AppStream `vcs-browser`,
   the GitHub repo URL, or Homebrew's head URL. A repo path is specific enough to link without the name.
3. They have the same name and the same normalized homepage or source-code URL.

Rules 2 and 3 merge only when every group that shares the key comes from a different source, so ambiguous
cases (Fedora's `ardour8` and `ardour9`) stay separate, while Neovim on Flathub, Homebrew and GitHub becomes
one app. Bare forge
hosts such as `github.com` don't count as a homepage. With Flathub and Fedora 44/45, 581 apps merge. Each merge records why it happened,
so wrong merges can be debugged and overridden with a manual overrides file in the repo.

**Unofficial copies** (`merge/copies.go`). GitHub and COPR are where anyone can publish anything, so next to an
app from a reviewed source (Flathub, Fedora, Homebrew) a copy is shown only if it comes from the app's own
developer. The developer is recognized by owning the app's code-forge URLs, by being named in its AppStream ID
(`io.github.<owner>.App`, `org.<owner>.App`), or by the app's homepage being their site. A repo's own homepage
doesn't count, because anyone can point it at the real site.

- A GitHub-only app with a reviewed app's name joins it as another source if the developer published it, and
  is hidden as a duplicate if the app already has their GitHub releases.
- Otherwise it is hidden when it looks like a repackage (`postman-appimage`, "AppImage of podman",
  "unofficial") or is a small repo (under 1,000 stars) using an established app's name (10,000+ installs a
  year).
- Anything else that shares a name is a different app and stays (Scratch the notes app beside MIT Scratch).
- COPR builds of a reviewed app are hidden unless the developer made them. `ublue-os`, Bazzite's vendor, is
  trusted.

This drops some genuine third-party builds, such as a COPR with a newer version; that is the intended trade.
The build log lists each hidden listing and why (at debug level; the count at info). On the November 2026
catalog it hides 40 listings and merges 405 GitHub releases into reviewed apps.

### Trust scoring

Each listing gets a 0 to 100 score, a per-signal breakdown, and hard flags. The UI always shows the breakdown, not
only the number. Details go in `docs/trust-score.md` (to write).

| Source | Base | Signals |
| --- | --- | --- |
| Fedora | High | Built and signed by Fedora |
| Flathub | High | Verified publisher, install count, permission set |
| Homebrew | Medium-high | Reviewed formula, install analytics |
| COPR | Low-medium | Project age, build success rate, last build date, number of maintainers |
| GitHub | Low | Repo age, stars, contributors, release cadence, last commit, license, asset digests, signatures or attestations |

Hard flags (shown as warnings regardless of score): archived repo, repo under 30 days old, no release in 18 months,
no published checksum, owner changed recently.

### Publish

1. Build `catalog.db` (SQLite with FTS5 search), compress with zstd.
2. Write `manifest.json`: schema version, catalog hash and size, build time, per-source fetch status.
3. Sign the manifest with an ed25519 key held as a CI secret. The published file is an envelope: the manifest
   as an opaque payload plus a signature over it. The client ships the public key, so a compromised Worker,
   bucket or upload token cannot serve a fake catalog.
4. Upload the catalog (named `catalog-<sha256>.db.zst`, immutable), then the manifest. Keep the last three
   catalogs so clients mid-download don't break.

The client refuses a manifest older than its local one, so an old signed manifest cannot be replayed to roll
the catalog back. It checks the compressed file's size and hash before decompressing it.

Keys: the private signing key and the upload token live in the repo's Actions secrets (`UAS_SIGNING_KEY`,
`UAS_UPLOAD_TOKEN`), with a backup in `~/.config/uas-indexer/` on the maintainer's machine. To rotate the
signing key, add the new public key to `internal/client/refresh/keys.go`, ship a release, then switch the
secret.

Icons are mirrored to R2. Screenshots link to their original URLs.

**Free-tier fit:** R2's free tier (10 GB, no egress fees) holds this easily: the Flathub-only catalog is
5.4 MB compressed (27 MB uncompressed). The Worker's free tier allows 100,000 requests a day, and each client
refresh is one request when nothing changed, two when it did. A custom domain can come later for edge caching. The repo is private, so Actions minutes count against the free plan's monthly allowance (2,000 minutes on
GitHub Free; confirm for your plan). A 6-hour schedule is about 120 runs a month, so each run must stay under
about 15 minutes. Incremental fetching with ETags keeps it there; drop to every 12 hours if needed.

## Client side

### Core packages

- **platform:** Reads `/etc/os-release` and `rpm-ostree status --json`. Detects distro, release, atomic or not,
  booted image, pending deployment, CPU architecture, libfuse2, presence of `brew` and `flatpak`. Disables sources
  that don't apply.
- **refresh:** Fetches the manifest, checks the signature, downloads the catalog only if its hash changed, and swaps
  it in atomically. Works offline with the last good catalog.
- **catalog:** Shared schema and queries, used by both indexer and client. Schema version 3 stores each app's GitHub `stars` and `updated` (newest release of any listing) for
  sorting and display; version 2 added each app's
  `popularity` (the highest install count of its listings: Homebrew's installs-on-request in the past year,
  Flathub's last-month installs from its `collection/popular` API times 12, matched by the Flatpak ID in the
  listing's ref because an old-style AppStream ID such as `org.godotengine.Godot.desktop` differs from it) and a precomputed search `boost`
  that multiplies the bm25 score, so well-known apps rank first.
- **state:** A local SQLite database of what UAS installed and from where (needed for GitHub apps, which nothing
  else tracks). Reconciled on startup against `flatpak list`, `brew list` and the rpm-ostree deployment, so
  installs made outside UAS still show up.
- **session:** The engine shared by the CLI and the desktop app: loads settings, detects the platform, builds
  the backends, opens (and on first use downloads) the catalog, and runs installs, updates and removals.
- **recommend:** Picks the default source per app and gives the reason. Verified listings first, then
  Flathub, Homebrew, GitHub, Fedora layering, COPR layering (`recommend.InstallOrder`). Trust flags will be
  able to move a source down.
- **jobs:** A queue of install, update and remove jobs with progress events, cancellation and logs. Only one
  rpm-ostree job runs at a time. Tracks the "reboot needed" state.

### Backends

All implement one interface. A backend is only created when platform detection finds its tool. Backends
run tools through a `run.Runner`, so tests use a fake, and they validate every ref before it reaches a command line.

```go
type Backend interface {
    Source() catalog.SourceID
    Installed(ctx context.Context) ([]InstalledApp, error)
    Install(ctx context.Context, l catalog.Listing, progress func(Progress)) error
    Update(ctx context.Context, a InstalledApp, progress func(Progress)) error
    Remove(ctx context.Context, a InstalledApp, progress func(Progress)) error
}
```

- **flatpak:** Calls the `flatpak` CLI with `--noninteractive`. No cgo, matching Jamb's pure-Go approach.
  Installs to user scope by default, so no password prompt. Bazzite ships Flathub only as a system remote, so on
  first use uas adds Flathub as a user remote (`flatpak remote-add --user --if-not-exists`, no root). A setting
  switches the default to system scope. Apps from both scopes appear under Installed and Updates.
- **homebrew:** Calls `brew` and reads its JSON output.
- **github:** Downloads the AppImage, checks its size and the SHA-256 GitHub publishes (no checksum, no
  install), and saves it as `~/.local/share/uas/appimages/<owner>__<repo>.AppImage`. The `.desktop` file and
  icon are read from the squashfs image inside it (`internal/client/appimage`), without running it, and a
  launcher is written to `~/.local/share/applications/uas-<owner>__<repo>.desktop` with `Exec` rebuilt to run
  only the AppImage. Installs are recorded in `~/.local/share/uas/state.json` (`internal/client/state`).
  Update compares the installed checksum with the catalog's current release. `.flatpak` bundles and `.rpm`
  files from releases are in the catalog but not installable yet.
- **rpmostree:** Calls the `rpm-ostree` CLI for v1. Its D-Bus API can replace this later for finer progress.
  New layers go onto the newest deployment, which may be a staged update to the next Fedora release, so the
  backend reads that deployment's release (from the kernel version in its image metadata) and only offers
  listings for it. It refuses packages already in the image (`rpm -q`), lists layered packages with a
  "takes effect after restart" flag, and `update` explains that layered packages update with the system.
  Bazzite's polkit rules let `wheel` users layer without a password.
- **copr:** If the project's repo isn't enabled, writes the same `.repo` file `dnf copr enable` would (same
  name, `gpgcheck=1` with the project's key) to a temp file and copies it into `/etc/yum.repos.d` with
  `pkexec install`, then layers the package through the rpmostree backend. A custom polkit policy isn't
  possible because `/usr` is read-only, so the password prompt uses pkexec's generic wording. Installs are
  recorded in the state file; removing the last package uas installed from a project also removes the repo
  file, but only if uas created it. Packages layered from COPR are listed under COPR, not Fedora. Not tested
  live yet: enabling a repo needs a password.

### UI (Jamb)

`cmd/uas-gui` with `internal/gui`, on top of `internal/client/session`, the engine the CLI uses too (settings,
platform, backends, install and remove, catalog). Pages:

- **Discover:** a search field (searches after a 250 ms pause) and category chips with tooltips. With no
  search it lists the most popular desktop apps, overall or per category (`catalog.DB.Browse`). A sort menu
  offers best match (search only), most popular, recently updated and name (`catalog.Order`). Each row shows
  what the order is based on: installs a year, or GitHub stars when there are no install counts, and the
  latest release date, with a tooltip saying where the numbers come from.
- **App page:** icon, developer, summary; a source picker of every listing this system can install, with the
  recommended one preselected and the reason shown; install notes (scope, password, restart, layering);
  a "Why <source>?" panel listing what that source means (`recommend.Traits`: review, sandbox, restart) and
  why each other listing wasn't picked (`recommend.Alternatives`); Install, or Installed with Update (only
  when an update exists) and Remove; the trust panel for GitHub and COPR listings; screenshots;
  the description as Markdown; license and links.
- **Installed:** everything installed from every source, matched to the catalog for icons and app pages, with
  Update (not for layered packages, which update with the system) and Remove.
- **Settings:** Flatpak scope, the catalog's build and per-source status with a manual refresh, what the
  system supports, and the privacy statement.

GitHub, COPR and Fedora installs ask first in an alert dialog that repeats the notes and the trust panel;
removals always ask. Jobs run in the background with progress in the page and in a toast. Images are cached in
`~/.cache/uas/images`. At start the app checks for a newer catalog in the background and switches to it.

Jamb is pinned to its main branch (`47731b5`): the Linux platform landed after its last tag. Linux support is
experimental (Wayland only, CPU rendering, no AT-SPI or input methods). Jamb picks Linux fonts from Debian paths
only, so `cmd/uas-gui/fonts.go` registers the desktop's fonts through fontconfig. Problems found while building
the UI are written up for the Jamb maintainers outside this repo.

### Packaging the client

Not a Flatpak: the sandbox would block calling `rpm-ostree`, `brew` and `pkexec`. Ship a single static binary.
While the repo is private, build from source; a Homebrew tap and public releases come later.

### Telemetry

None in v1. The code has hooks so it can be added later without touching callers:

- `internal/client/telemetry` defines an `Event` type and a `Sink` interface. v1 ships only a no-op sink.
- refresh, search and jobs emit events (catalog refreshed, install succeeded or failed by source, and so on) through
  the sink. Events never carry search text, file paths, usernames, or machine identifiers.
- A `telemetry` setting exists and defaults to off. A real sink only runs when the user turns it on.

Note that catalog downloads from R2 are plain anonymous GETs. Cloudflare still sees the requesting IP address.

## Repo layout

```
uas/
├── go.mod
├── cmd/
│   ├── uas/                 # CLI: search, info, install, remove, update
│   ├── uas-gui/             # Jamb desktop app (fonts.go: fontconfig font workaround)
│   └── uas-indexer/         # crawler, run in CI
├── internal/
│   ├── catalog/             # shared types, SQLite schema, FTS queries (rebuilt each run, no migrations)
│   ├── manifest/            # signed manifest envelope (shared by indexer and client)
│   ├── indexer/
│   │   ├── appstream/       # AppStream XML parser (Flathub, Fedora)
│   │   ├── fetch/           # Fetcher interface, HTTP cache with ETags
│   │   │   ├── flathub/
│   │   │   ├── fedora/
│   │   │   ├── copr/
│   │   │   ├── homebrew/
│   │   │   └── github/
│   │   ├── merge/           # dedupe and linking
│   │   ├── trust/           # scoring
│   │   └── publish/         # build db, compress, sign, upload
│   ├── client/
│   │   ├── appimage/        # reads .desktop and icon from an AppImage; writes the launcher
│   │   ├── paths/
│   │   ├── platform/
│   │   ├── refresh/         # download, verify and install the catalog
│   │   ├── state/
│   │   ├── recommend/
│   │   ├── run/             # runs flatpak, brew, rpm-ostree, pkexec
│   │   ├── jobs/
│   │   ├── session/         # engine shared by CLI and GUI
│   │   ├── settings/
│   │   ├── telemetry/      # Event, Sink interface, no-op sink
│   │   └── backend/
│   │       ├── backend.go   # interface
│   │       ├── flatpak/
│   │       ├── homebrew/
│   │       ├── github/
│   │       ├── rpmostree/
│   │       └── copr/
│   └── gui/                 # Jamb views: discover, detail, installed, settings, jobs, icons
├── overrides/               # manual merge and trust overrides (YAML)
├── deploy/cloudflare/       # catalog Worker (serves R2, accepts signed-catalog uploads)
├── .github/workflows/
│   ├── ci.yml               # test, lint, build
│   └── index.yml            # cron: crawl, build, sign, publish
└── docs/
    ├── architecture.md
    └── trust-score.md
```

## Milestones

1. **Catalog + Flathub:** schema, Flathub fetcher, local build, `uas search`. Done.
2. **First install:** platform detection, flatpak backend, `uas install`, `remove`, `update`, `list`,
   `system`, `settings`. Done.
3. **Hosting:** signing, Worker + R2, Actions cron, `uas refresh`. Done.
4. **Fedora + rpm-ostree:** layering, reboot-needed state. Done.
5. **Homebrew.** Done. `uas install tree` works by package name: the CLI resolves app IDs, package names,
   AppStream IDs and refs, and lists the choices when a name is ambiguous.
6. **GitHub:** fetcher, trust scoring, AppImage installer, update tracking. Done. Follow-ups: installing
   `.flatpak` bundles and `.rpm` files from releases.
7. **COPR:** fetcher, trust scoring, pkexec repo enabling. Done (live install untested; see above).
8. **Jamb UI:** Discover, app pages, Installed, Settings. Done. Follow-ups: an Updates page (available
   updates per source), icons for apps whose catalog entry has only cached icons (some Fedora and KDE apps),
   synonyms in search ("photo editor" doesn't find GIMP).

## Decisions

- Name `uas`, module `github.com/tyler-builds/uas`.
- Private repo for now.
- Flatpak installs to user scope by default; system scope is a setting.
- No telemetry in v1. Hooks exist, and any future telemetry is an opt-in setting, off by default.
