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
  (such as `fedora-45`) and a `package` name. Only components whose package is in the repos are kept. Most icons
  ship only as files inside appstream-data (`swcatalog/icons/fedora/128x128/`), so the fetcher extracts the
  largest one each app names and publishes it to R2 with the GitHub icons, named by its SHA-256. Apps that
  name only a theme icon (a "stock" icon) have none.
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
  to four screenshots come from the README: images named like screenshots first, never badges or logos.
  Failing that, they come from picture files in the repo named like screenshots (`screenshots/main.png`,
  `docs/screenshot-dark.png`, a `screens/` folder, `demo.gif`), shallowest first, leaving out vendored code,
  tests and icons. An app whose AppImage has no icon (or that ships no AppImage) gets one from the repo's
  files: the Linux icon theme folders (`hicolor/256x256/apps/`, `hicolor/scalable/apps/`), Tauri's and
  electron-builder's standard places, then `icon.png` or `logo.png` near the top, if roughly square. Every
  screenshot is checked by reading the start of the file: a picture of at least
  480×270, no narrower than 1:3, and not a social card (1280×640 or 1200×630). What was found is kept per release file
  (`github/media` in the indexer cache), so only new versions are read again.

  Icons are scaled to at most 256 px, named by their SHA-256 and uploaded to R2 before the catalog
  (`publish.Icons`, through `icons/<sha256>.png|svg` on the Worker). The Worker serves them as immutable,
  and serves SVGs with a sandboxing Content-Security-Policy, because they come from third parties. A
  GitHub listing never takes an AppStream ID from its metainfo. Otherwise any repo could claim a Flathub
  app's ID and be merged into it as one of its sources.

- **Codeberg and GitLab** (`fetch/forge`): the most starred repos, then each one's 8 newest releases, kept like
  GitHub's (same file picking, `trust.GitHub` scoring). Listing refs name the host
  (`codeberg.org/owner/repo`, `gitlab.com/group/sub/project`), so they never collide with GitHub's. Neither
  forge publishes checksums, so the indexer downloads each listed file once, records its SHA-256 and size in
  the signed catalog, and reads the AppImage for its name, icon and screenshots on the way; both are kept per
  release file (`github/media/forge/`), so only new releases are downloaded. Rate limits shape both:
  Codeberg allows 2,000 keyless requests per 10 minutes (its `RateLimit-Policy` header), so requests are
  spaced half a second apart and only repos with 25+ stars are read (a few hundred requests). GitLab.com
  limits keyless requests to about 60 an hour per IP, too few to index with, so GitLab is indexed only when
  the `GITLAB_TOKEN` secret holds a personal access token (read_api scope; 2,000 requests a minute), and
  left out otherwise. Projects with 50+ stars active in the past year are read, 10 requests a second. Both
  are cached like GitHub's, and a failed fetch (such as a rate limit) reuses results up to a week old.
  In November 2026 Codeberg's 300+ star repos gave Tenacity, Gram and ChiPass.

- **Debian and Ubuntu** (`fetch/debian`, `dep11`): the AppStream data in each release's DEP-11 YAML
  (`dists/<codename>/<component>/dep11/Components-<arch>.yml.gz`) with versions from its Packages indexes,
  release and -updates pockets. Debian's stable release (from `dists/stable/Release`) and Ubuntu's three newest
  supported releases (from `changelogs.ubuntu.com/meta-release`, which also marks old releases under paid
  support as supported) are indexed: Distro `debian-13`, `ubuntu-24.04`. Icon and screenshot paths are relative
  to the file's `MediaBaseUrl`. DEP-11 sometimes lists a translation twice, so translated fields are read from
  the YAML nodes, not Go maps. Files come from `deb.debian.org` and the kernel.org Ubuntu mirror (ports.ubuntu.com
  for aarch64), with ETags. About 1,900 apps a release.
- **Arch** (`fetch/arch`): `archlinux-appstream-data`'s version from archlinux.org's package JSON, its
  `Components-x86_64.xml.gz` from sources.archlinux.org, and versions from the core, extra and multilib
  databases on a mirror. Distro `arch`; x86_64 only. Arch's icons come only as JPEG XL files in
  `icons-128x128.tar.gz`; the indexer decodes them with a pure-Go decoder (`kpfaulkner/jxl-go`, used only by the
  indexer), publishes them as PNG with the other icons, and remembers each decode by the file's hash, so a run
  takes 2 seconds once the first has decoded them (15). About 1,240 apps, 1,030 with icons; the rest name no
  128 px icon or use JPEG XL features the decoder lacks.
- **Snap Store** (`fetch/snap`): the v1 search API with `X-Ubuntu-Series: 16`, 100 snaps a page (about 90
  pages). No rate limit is published, so pages are read one a second and the result is cached like GitHub's.
  A snap is kept with a title, summary and icon and either a verified or starred publisher or an AppStream ID
  (its `common_ids`, which merge it with the app's other listings), and at least one app (runtimes, bases
  and themes have none): about 1,060 of 8,800. Keeping snaps with a store section and screenshots too let in
  4,500, most of them unmaintained. An
  unverified snap that shares a reviewed app's name is hidden as an unofficial copy; a verified one joins it.

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

**Editions.** One catalog of every source would be 16 MB compressed, most of it packages for systems the
client isn't. So the indexer fetches once and then merges and writes one catalog per kind of system
(`indexer/editions.go`): the sources that work everywhere (Flathub, Homebrew, GitHub, GitLab, Codeberg), plus
`fedora-atomic` (Fedora and COPR, for layering; no snaps, since image-based Fedora has no snapd), `fedora`
(Fedora for dnf, snaps), one per Debian and Ubuntu release (`debian-13`, `ubuntu-24.04`: that release only,
snaps), `arch` (Arch, snaps) and `other` (snaps). Splitting by source instead would leave clients to merge
apps across sources themselves. The signed manifest names every edition's file (`editions`), and its single
`catalog` is fedora-atomic's, for clients from before editions. A client asks for `platform.Info.Edition()`
and falls back to `other` when there is none for its release (a new Debian, say). The hash of the installed
file is kept beside it (`catalog.db.sha256`), so a client whose system changes edition downloads the new
one. Catalogs of the last few runs are kept for clients mid-download.

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
  that multiplies the bm25 score, so well-known apps rank first. Each app's freedesktop categories are
  indexed as words ("WebBrowser" as "web browser"), and a short synonym list (`internal/catalog/synonyms.go`)
  maps the names of well-known apps to their kind, so "excel" finds spreadsheets. By relevance, an exact
  name comes first, then names starting with the query. A search for a kind of app (several words, or a
  word with synonyms) then puts apps with the words whole in their name, summary or categories, most
  installed first, ahead of apps that only mention them in their description.
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
  **Permissions** (`permissions.go`): an installed app's page lists what it may reach outside its sandbox
  (network, all files, home folder, Downloads, all devices, sound, Bluetooth, X11), read from
  `flatpak info --show-permissions` (the manifest with every override applied), with the ones the user changed
  read from `flatpak override --user --show`. A switch adds a user override (`--share`/`--unshare`,
  `--filesystem`/`--nofilesystem` and so on), which applies in every installation and needs no password; Reset
  runs `flatpak override --user --reset`. These are the files Flatseal edits, so the two agree. Access that
  comes with a broader one (Downloads with the home folder) is shown on but can't be switched off alone.
- **homebrew:** Calls `brew` and reads its JSON output.
- **github:** Downloads the AppImage, checks its size and the SHA-256 GitHub publishes (no checksum, no
  install), and saves it as `~/.local/share/uas/appimages/<owner>__<repo>.AppImage`. The `.desktop` file and
  icon are read from the squashfs image inside it (`internal/client/appimage`), without running it, and a
  launcher is written to `~/.local/share/applications/uas-<owner>__<repo>.desktop` with `Exec` rebuilt to run
  only the AppImage. Installs are recorded in `~/.local/share/uas/state.json` (`internal/client/state`).
  Update compares the installed checksum with the catalog's current release. A release with no AppImage but
  a `.flatpak` bundle (with a published checksum) is installed with `flatpak install --user --bundle`, after
  checking the download and reading the bundle's ref from its header; Flatpak fetches the runtime from
  Flathub. Bundle installs are recorded in the same state file and hidden from the Flatpak backend's lists,
  and an update that changes the bundle's branch removes the old one. `.rpm` files from releases are in the
  catalog but not installable. Releases that ship only `.zip` or `.tar.gz` files are left out: of 482
  popular repos found that way, nearly all were command-line tools and servers, which Homebrew covers.
- **appimage (added files):** `uas add <file>` and Installed's Add AppImage button take an AppImage the user
  downloaded, like Gear Lever. uas reads it without running it, including the update information that
  appimagetool embeds in the runtime's `.upd_info` section. If the catalog has the app as a GitHub AppImage,
  found by the repo the update information names, or by the app's ID when it names none, the file becomes that
  GitHub install and updates from the catalog. Otherwise it is kept as
  `~/.local/share/uas/appimages/file__<id>.AppImage` with a launcher, recorded in the state file, and updated
  from where the update information points. uas follows `gh-releases-zsync` and `gh-releases-direct` through
  GitHub's releases API, and `zsync` URLs over https. Apps built with electron-builder leave `.upd_info` empty
  and name their feed in `resources/app-update.yml` instead. uas follows its `generic`, `github` and `s3`
  providers to the `latest-linux.yml` feed and checks the feed's SHA-512. A short list of known feeds
  (`knownFeeds`) covers apps that name none in their files, such as CurseForge. It applies to installs
  added before the feed was known too. A new file is used only if it matches a published
  checksum: GitHub's SHA-256 digest of the release file, the SHA-1 in the `.zsync` header, or the feed's
  SHA-512. The file is
  downloaded whole; zsync's partial downloads aren't used. Release lookups are unauthenticated, which
  GitHub limits to 60 an hour, so results are kept for an hour. The user's file is copied first and deleted
  only once the add succeeds. On btrfs the copy shares the file's data. An AppImage with no update information
  is listed with "doesn't update itself". Launchers another tool (such as Gear Lever) wrote for the added
  file are taken over: their window class and icon fill in what uas can't read, they are removed, and GNOME's
  dock favorites point at uas's launcher instead. Every launcher has a `StartupWMClass` (the app's own, else
  its desktop entry's name), since uas renames launchers and the dock matches windows to them by it.
  Launchers carry `X-UAS-Launcher`, and older ones are rewritten when uas starts.
- **pkgmgr** (apt, dnf, pacman): on package-based systems, `platform.Info.Distro()` names the release
  (`ubuntu-24.04` also for Linux Mint and Pop!_OS, through `UBUNTU_CODENAME`; `arch` for its derivatives;
  `fedora-45` only when not image-based) and the matching backend installs that release's listings. Installs,
  updates and removals run through pkexec (directly as root, as in a container); `--` ends the options before
  every package name. Package managers list libraries too, so Installed keeps only packages the catalog lists
  for the release. Updates come from `apt list --upgradable` and `dnf list --upgrades`; Arch doesn't support
  partial upgrades, so it has no per-app updates. Tested in containers for all four.
- **snap:** `snap install` (with `--classic` for classic snaps), `refresh`, `remove`; snapd asks for the
  password itself. Bases and runtimes are left out of Installed, since the catalog doesn't list them (the
  indexer drops snaps with no apps). Tested in a rootless Ubuntu container running systemd as init, with
  `/dev/fuse` so snapd mounts snaps through squashfuse.
- **gitlab, codeberg:** the github backend with `Forge` set. Installs go in the same folders and state file
  (recorded with their `source`), and files are checked against the SHA-256 the indexer recorded, since
  neither forge publishes one. Asset URLs must be under the repo's own release downloads (Codeberg) or
  project pages (GitLab, whose release links redirect to wherever the file is kept).
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

### Background update checks

`internal/client/updatecheck`, off until the user turns it on (Settings → Updates, or
`uas settings update_checks on`). Turning it on writes a systemd user timer and service
(`~/.config/systemd/user/uas-update-check.{timer,service}`: daily, 15 minutes after login, a random delay of
up to an hour, catching up after the machine was off) and enables it; turning it off disables and deletes
them, so the units are the setting. The service runs the binary that turned it on (`uas-gui --check-updates`
or `uas check-updates -notify`), which refreshes the catalog, lists updates and, if any are new since the
last notification (remembered with versions in `~/.local/state/uas/notified.json`), shows one notification
through `org.freedesktop.Notifications` on the session bus. It talks D-Bus directly (`internal/client/notify`)
because notify-send is broken on some systems (Bazzite 45's fails with a symbol lookup error). A click on
"Show updates" within an hour opens the app on its Updates page (`uas-gui --page updates`), started through
`systemd-run --user` so it outlives the check. Nothing is installed without the user.

### ujust commands

`internal/client/ujust`. On Universal Blue images (Bazzite, Bluefin, Aurora), `just --justfile
/usr/share/ublue-os/justfile --dump --dump-format json` lists the recipes without running any: name,
description, group and first parameter. Options are read from descriptions that list them
("Options: status | install | uninstall"). The desktop app shows them on a "<system> tools" page, by group,
filterable from the top bar. A command runs only in a terminal window (`xdg-terminal-exec`, else Ptyxis,
Console, Konsole and so on), since many ask questions or for a password, and the window stays open until
Enter so the output can be read. Everything but a `status` option asks first. Recipe names and options are
checked against plain patterns before they reach a command line.

### Back up and restore

`session/export.go`. An export is JSON: each installed app's source, ref, app ID, package and name, the
Flatpak scope setting, and the user's Flatpak overrides (the files under `~/.local/share/flatpak/overrides`),
with no paths or user names. Restoring plans each app: already installed (from any source) is skipped; the
same listing is installed when this system supports it; otherwise the same source's other listing (another
Fedora release), else the source uas recommends ("from Flathub instead of Fedora"). Two exported copies of
one catalog app install once. AppImages added from files can't be fetched again and are listed for the user.
Layered packages go last. Overrides are written only for apps with none here yet. CLI: `uas export`,
`uas restore [-n] [-y]`; desktop: Installed → Back up.

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

uas uses Jamb v0.6.0, its first release with Linux support. That support is experimental (Wayland only, CPU
rendering, no AT-SPI or input methods). Jamb finds the desktop's fonts and emoji through fontconfig. Problems
found while building the UI are written up for the Jamb maintainers outside this repo.

### Packaging the client

Not a Flatpak: the sandbox would block calling `rpm-ostree`, `brew` and `pkexec`. A release is an archive per
architecture (`packaging/linux/build-release.sh`): static `uas` and `uas-gui` (no cgo; Jamb loads Wayland
through purego), the launcher, the icon and `install.sh`, which installs to `~/.local/bin` and writes the
launcher with the full path. The repo is private, so releases aren't served from GitHub: the Release
workflow (on a `v*.*.*` tag, or by hand without publishing) builds both archives and `cmd/uas-release`
uploads them through the catalog Worker, then a signed `releases/latest.json` (the catalog's key, under its
own signing context, so a manifest signature can't pass as a release's). It refuses a version that isn't
newer than the published one, and the Worker refuses to replace a published archive.

**Self-update** (`internal/client/selfupdate`): `uas self-update`, Settings → About, and the background check
(which lists "uas 0.3.0" with app updates) read `latest.json`, verify it, and compare versions. Installing
downloads the archive, checks its size and SHA-256 against the signed values, writes both programs beside
the running one, then renames them into place, only if that folder is the user's. Builds from source report
version `dev` and never update themselves. A Homebrew tap can come later.

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
│   ├── uas-gui/             # Jamb desktop app
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
6. **GitHub:** fetcher, trust scoring, AppImage and Flatpak bundle installer, update tracking. Done.
7. **COPR:** fetcher, trust scoring, pkexec repo enabling. Done (live install untested; see above).
8. **Jamb UI:** Discover, app pages, Installed, Updates, Settings. Done.
9. **Added AppImages:** `uas add` and Add AppImage, updates from the AppImage's own update information. Done.

## Decisions

- Name `uas`, module `github.com/tyler-builds/uas`.
- Private repo for now.
- Flatpak installs to user scope by default; system scope is a setting.
- No telemetry in v1. Hooks exist, and any future telemetry is an opt-in setting, off by default.
