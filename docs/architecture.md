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
            GitHub Actions (cron, every 6h)                  Cloudflare R2 (custom domain, CDN)
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
 │          client core: platform · sync · catalog query · recommend · state · jobs       │
 │               │                                                                        │
 │          backends: flatpak · homebrew · github · rpmostree · copr                      │
 │               │            │          │            │            │                      │
 │           flatpak CLI   brew CLI   own installer  rpm-ostree   pkexec + rpm-ostree     │
 └────────────────────────────────────────────────────────────────────────────────────────┘
```

No server code runs in v1. The indexer runs in CI, R2 serves static files, and every install happens on the
client as the user, with polkit prompts only where the system tools already require them.

## Server side: the indexer

### Fetchers (one package per source)

All endpoints below need confirming during the first spike.

- **Flathub:** AppStream XML from the Flathub repo, plus the Flathub API for verified status and install counts.
- **Fedora:** AppStream data for the current release (`appstream-data`), joined to repo metadata for versions.
  Only components with AppStream entries (desktop apps, add-ons) are kept, not libraries.
- **COPR:** COPR API v3. Only projects with successful builds for `fedora-<N>-x86_64`.
- **Homebrew:** `formulae.brew.sh` JSON API plus install analytics. Formulae that build on Linux only.
- **GitHub:** GraphQL API. A curated seed list plus discovery searches for repos whose latest release has a
  Linux x86_64 `.AppImage`, `.flatpak` or `.rpm` asset. Uses a GitHub App token (higher rate limit than
  `GITHUB_TOKEN`) and ETags for incremental runs. Search caps out at 1,000 results per query, so queries are
  sliced by stars and dates.

Each fetcher returns `[]catalog.Listing` (one app from one source) and stores raw responses as fixtures for tests.

### Merge (deduplication)

Listings are linked into one `App` when, in order of confidence:

1. They share an AppStream ID (Flathub and Fedora).
2. They point at the same upstream repo (AppStream `vcs-browser` or homepage URL, GitHub repo URL, COPR/Homebrew
   source URLs).
3. They share a normalized homepage URL.

Links below a confidence threshold are not merged; they appear as separate apps. Each merge records why it happened,
so wrong merges can be debugged and overridden with a manual overrides file in the repo.

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
3. Sign the manifest with an ed25519 key held as a CI secret. The client ships the public key, so a compromised
   bucket cannot serve a fake catalog.
4. Upload to R2. Keep the last few catalogs so clients mid-download don't break.

Icons are mirrored to R2. Screenshots link to their original URLs.

**Free-tier fit:** R2's free tier (10 GB, no egress fees) holds this easily. Expected catalog size is about
5 to 10 MB compressed. Serve from a custom domain so Cloudflare's CDN caches it; the `r2.dev` URL is rate
limited and only for development. A Worker is not needed in v1. The repo is private, so Actions minutes count against the free plan's monthly allowance (2,000 minutes on
GitHub Free; confirm for your plan). A 6-hour schedule is about 120 runs a month, so each run must stay under
about 15 minutes. Incremental fetching with ETags keeps it there; drop to every 12 hours if needed.

## Client side

### Core packages

- **platform:** Reads `/etc/os-release` and `rpm-ostree status --json`. Detects distro, release, atomic or not,
  booted image, pending deployment, CPU architecture, libfuse2, presence of `brew` and `flatpak`. Disables sources
  that don't apply.
- **sync:** Fetches the manifest, checks the signature, downloads the catalog only if its hash changed, and swaps
  it in atomically. Works offline with the last good catalog.
- **catalog:** Shared schema and queries, used by both indexer and client.
- **state:** A local SQLite database of what UAS installed and from where (needed for GitHub apps, which nothing
  else tracks). Reconciled on startup against `flatpak list`, `brew list` and the rpm-ostree deployment, so
  installs made outside UAS still show up.
- **recommend:** Picks the default source per app and gives the reason. Default order on Bazzite: verified
  Flathub, then Homebrew (for CLI tools), then GitHub `.flatpak`, then AppImage, then Fedora layering, then COPR
  layering. Trust flags can move a source down.
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
- **github:** Downloads the asset, checks it against the GitHub asset digest. AppImages go to
  `~/.local/share/uas/appimages`, and their `.desktop` file and icon are read from the squashfs in Go, without
  running the AppImage. `.flatpak` bundles go to the flatpak backend and `.rpm` files go to rpm-ostree.
- **rpmostree:** Calls the `rpm-ostree` CLI for v1. Its D-Bus API can replace this later for finer progress.
- **copr:** Writes the `.repo` file to a temp file, shows the user what will be added, then runs
  `pkexec install -m 0644 <tmp> /etc/yum.repos.d/...`, then hands off to rpmostree. A custom polkit policy isn't
  possible here because `/usr` is read-only, so the prompt uses pkexec's generic wording.

### UI (Jamb)

Pages: Home (curated and trending), Search, App detail (source picker with recommendation, trust panel,
permissions, reboot warning), Installed, Updates, Settings (sources on or off, refresh, Flatpak scope, telemetry).

Jamb's Linux adapter is experimental. On this machine (KDE Plasma, Wayland) server-side title bars work. Gaps that
matter for this app: no AT-SPI accessibility, no input methods. Those become Jamb work items.

### Packaging the client

Not a Flatpak: the sandbox would block calling `rpm-ostree`, `brew` and `pkexec`. Ship a single static binary.
While the repo is private, build from source; a Homebrew tap and public releases come later.

### Telemetry

None in v1. The code has hooks so it can be added later without touching callers:

- `internal/client/telemetry` defines an `Event` type and a `Sink` interface. v1 ships only a no-op sink.
- sync, search and jobs emit events (catalog synced, install succeeded or failed by source, and so on) through
  the sink. Events never carry search text, file paths, usernames, or machine identifiers.
- A `telemetry` setting exists and defaults to off. A real sink only runs when the user turns it on.

Note that catalog downloads from R2 are plain anonymous GETs. Cloudflare still sees the requesting IP address.

## Repo layout

```
uas/
├── go.mod
├── cmd/
│   ├── uas/                 # CLI: search, info, install, remove, update
│   ├── uas-gui/             # Jamb desktop app
│   └── uas-indexer/         # crawler, run in CI
├── internal/
│   ├── catalog/             # shared types, SQLite schema, FTS queries (rebuilt each run, no migrations)
│   ├── indexer/
│   │   ├── appstream/       # AppStream XML parser (Flathub, Fedora)
│   │   ├── fetch/           # Fetcher interface, HTTP cache with ETags
│   │   │   ├── flathub/
│   │   │   ├── fedora/
│   │   │   ├── copr/
│   │   │   ├── homebrew/
│   │   │   └── github/
│   │   ├── merge/           # dedupe and linking, overrides
│   │   ├── trust/           # scoring
│   │   └── publish/         # build db, compress, sign, upload
│   ├── client/
│   │   ├── paths/
│   │   ├── platform/
│   │   ├── sync/
│   │   ├── state/
│   │   ├── recommend/
│   │   ├── run/             # runs flatpak, brew, rpm-ostree, pkexec
│   │   ├── jobs/
│   │   ├── settings/
│   │   ├── telemetry/      # Event, Sink interface, no-op sink
│   │   └── backend/
│   │       ├── backend.go   # interface
│   │       ├── flatpak/
│   │       ├── homebrew/
│   │       ├── github/
│   │       ├── rpmostree/
│   │       └── copr/
│   └── ui/
│       ├── pages/           # home, search, app, installed, updates, settings
│       └── components/      # trust panel, source picker, job progress
├── overrides/               # manual merge and trust overrides (YAML)
├── deploy/cloudflare/       # R2 setup notes
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
3. **Hosting:** signing, R2 upload, Actions cron, client sync. Proves the free-hosting plan early.
4. **Fedora + rpm-ostree:** layering, reboot-needed state.
5. **Homebrew.**
6. **GitHub:** fetcher, trust scoring, AppImage installer, update tracking.
7. **COPR:** fetcher, trust scoring, pkexec repo enabling.
8. **Jamb UI** on top of the finished core. Can start in parallel after milestone 2.

## Decisions

- Name `uas`, module `github.com/tyler-builds/uas`.
- Private repo for now.
- Flatpak installs to user scope by default; system scope is a setting.
- No telemetry in v1. Hooks exist, and any future telemetry is an opt-in setting, off by default.
