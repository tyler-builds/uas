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
| COPR | Write `.repo` file via `pkexec`, then layer (or `dnf install`) | Yes | Layered only |

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
 │  uas gui (Jamb)        uas (CLI)                                                       │
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

- **Codeberg** (`fetch/forge`): the most starred repos, then each one's 8 newest releases, kept like
  GitHub's (same file picking, `trust.GitHub` scoring). Listing refs name the host
  (`codeberg.org/owner/repo`), so they never collide with GitHub's. Codeberg publishes no checksums, so the indexer downloads each listed file once, records its SHA-256 and size in
  the signed catalog, and reads the AppImage for its name, icon and screenshots on the way; both are kept per
  release file (`github/media/forge/`), so only new releases are downloaded. Codeberg allows 2,000 keyless
  requests per 10 minutes (its `RateLimit-Policy` header), so requests are spaced half a second apart and only
  repos with 25+ stars are read (a few hundred requests). Results are cached like GitHub's, and a failed fetch
  (such as a rate limit) reuses results up to a week old. GitLab was indexed for a while and dropped in
  November 2026: of 632 starred projects only one attached a Linux file to its releases, and GitLab.com's
  star-sorted project list often failed with server errors.
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

- **GNOME Shell extensions** (`fetch/gnome`): extensions.gnome.org's v1 API
  (`/api/v1/extensions/?shell_version=N`), 100 a page. The list doesn't say which GNOME releases an extension
  supports, so the fetcher asks once per release, from GNOME 45 (the first whose extensions still load: 45
  changed the extension format) up to the first release with none, and records the releases each extension
  turned up under (`Listing.Shells`). In November 2026 that is GNOME 45 to 51, about 90 pages. The site
  publishes no rate limit and its Django settings set no throttle, so pages are read one a second and the
  result is cached like GitHub's. Every version on the site is reviewed by GNOME's reviewers before it is
  listed, so all are kept. Listings are `Kind` `shell-extension` with the UUID as their ref; icons and the
  one screenshot link to the site.

- **Icon, cursor and GTK themes** (`fetch/pling`): the Pling store family (gnome-look.org, store.kde.org)
  through OpenDesktop's OCS API at `api.opendesktop.org/ocs/v1` (the same paths under gnome-look.org answer
  410 Gone), no key: categories 132 (icon themes), 107 (cursor themes) and 135 ("GTK3/4 Themes"), most
  downloaded first, 100 a page, down to a floor of 10,000 downloads for icon themes, 3,000 for cursor themes
  and 10,000 for GTK themes (about 300, 300 and 270 in November 2026; `-pling-min-downloads`,
  `-pling-min-cursor-downloads`, `-pling-min-gtk-downloads`). GTK 2 themes (136), GNOME Shell themes (134)
  and Cinnamon themes (133) are categories of their own and aren't read; most GTK3/4 items carry their
  `gnome-shell`, `gtk-2.0`, `xfwm4` and `cinnamon` folders anyway, inside each theme's folder. Items Pling's
  moderators exclude (`ghns_excluded`) are left out. Listings are `Kind` `icon-theme`, `cursor-theme` or
  `gtk-theme`, ref the item ID, with
  downloads (`Installs`; Pling's counts are rough, its list and item views disagree), its rating (`Score`,
  where Pling gives an unrated item 50, which uas never shows as a rating), the licence from its tags, its
  preview pictures (linked, like screenshots) and a trust score (`trust.Pling`). Each of an item's files
  (up to 30) is an `AssetTheme`: the indexer downloads it once through the list's own download link,
  checks Pling's MD5, records its SHA-256 and size, and reads it with `internal/appearance` (below), which
  gives the theme folders inside (`Asset.Themes`, the variants the user picks from) and how many files it
  leaves out of it for being runnable (`Asset.LeftOut`), or refuses it. A refused file is left out, and an
  item with none left is too. Each refused file and each item left out is logged (`pling file refused`,
  `pling item left out`, with the item ID, name and reason), so a theme missing from the catalog can be
  traced from the run's log. In a November 2026 sample of the 16 most downloaded GTK items, 14 were listed:
  Ant Themes' files hold two themes of one name each (`Ant-Bloody` and `Ant-Bloody-alt-style/Ant-Bloody`), and
  X-Arc-Collection links to a web page. In the November 2026 sample, a theme nested inside another (Layan) was refused;
  until rules version 3, so were Kora and about 30 other files of the first production run, for a helper
  script such as `create-new-icon-theme.cache.sh`. What was found is kept per item, file
  name and MD5 (`pling/pins` in the indexer cache), so a file is read again only when its author replaces it,
  or when the rules change (`appearance.RulesVersion`). The links themselves expire after two days, redirect
  to storage valid for an hour, and are refused after a few reuses (429 with `retry-after: 900`), so the
  catalog holds none: the client asks for a fresh one at install time. New files are downloaded within a
  per-run budget (`-pling-pin-files`, `-pling-pin-mb`), and an item waits for a later run until all its
  files are read, so the first runs fill the catalog in over a day. Each category first gets an equal share
  of what the budget has left (icon themes a third, cursor themes half of what is left, GTK themes the rest),
  since icon themes come first and are far larger: without shares, the first run spent its 3.3 GB on icon
  themes and listed no cursor theme. GTK themes come last, so icon and cursor themes are read as before; their
  files are small (Orchis's is 220 KB, 30 files of the 16 most downloaded items were 18 MB).
  What the shares leave then goes to the items still waiting, most downloaded first within each category. Pling publishes no rate limit; pages and
  downloads are spaced a second apart, with a generic User-Agent.
  GTK themes needed no schema change: they are apps of kind `gtk-theme`, searched in `appearance_fts`. A uas
  from before them searches apps in `apps_fts` and lists them by category (`AppsOnly`) or by an appearance
  kind it knows, so it never shows one; only `uas info pling:<id>` finds one by its ID, and installing it
  says no source here offers it. A uas with them and a catalog from before them shows "No GTK themes in the
  catalog yet" with Check for a newer catalog. The cached Pling result's key names the GTK floor, so the
  first scheduled run with this indexer reads Pling again rather than reusing a result without GTK themes.
- **Fonts from Google Fonts** (`fetch/googlefonts`): the github.com/google/fonts repo at the commit `main`
  points at, not the Google Fonts Developer API (it needs a key, and its terms forbid embedding one in an
  open-source project). One GitHub API call reads the recursive tree; each family's `METADATA.pb` (a text
  protocol buffer, read by a small parser) and description come from raw.githubusercontent.com, kept by git
  blob hash. Listings are `Kind` `font`, ref the family's folder (`roboto`), with the designer, the licence
  (OFL, Apache, UFL), the category and one `AssetFont` per file, the regular upright one first (previews draw
  it), with links that name the commit so a file never changes under its checksum. Every file is downloaded
  once and checked as a font to record its SHA-256 (kept by blob hash): about 2.5 GB in all, so a run
  downloads at most `-google-fonts-pin-mb` (800 MB) of new files, the most popular families first, and a
  family is listed once all its files are. fonts.google.com/metadata/fonts (no key) gives each family's
  popularity rank (`Listing.Rank`) and last change; it is optional.
- **Nerd Fonts** (`fetch/nerdfonts`): the latest release of ryanoasis/nerd-fonts, one listing per `.tar.xz`
  (72 in November 2026), with its download count and the SHA-256 GitHub publishes for the file
  (`AssetFontArchive`), so nothing is downloaded.

  These three are cached for 5 hours (`-appearance-cache-for`), less than the other slow sources, so each
  run reads the next files of the budget.

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

Shell extensions are never merged, into apps or each other: GSConnect the extension shares a name and a repo
with nothing it should become a source of. Each becomes its own app, `gnome-extensions:<uuid>`.

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
| Pling | Lower | Item age, downloads, rating, last change, checksum recorded by uas; no publisher verification |

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
(`indexer/editions.go`): the sources that work everywhere (Flathub, Homebrew, GitHub, Codeberg), plus
`fedora-atomic` (Fedora and COPR, for layering; no snaps, since image-based Fedora has no snapd), `fedora`
(Fedora and COPR for dnf, snaps), one per Debian and Ubuntu release (`debian-13`, `ubuntu-24.04`: that release only,
snaps), `arch` (Arch, snaps) and `other` (snaps). Splitting by source instead would leave clients to merge
apps across sources themselves. The signed manifest names every edition's file (`editions`), and its single
`catalog` is fedora-atomic's, for clients from before editions. A client asks for `platform.Info.Edition()`
and falls back to `other` when there is none for its release (a new Debian, say). The hash of the installed
file is kept beside it (`catalog.db.sha256`), so a client whose system changes edition downloads the new
one. Catalogs of the last few runs are kept for clients mid-download.

**Extensions in the catalog.** Shell extensions are rows of the same `apps` and `listings` tables, in every
edition, but with their own search index (`extensions_fts`), and `Search` and `Browse` return either apps or
extensions, never both. They are also kept out of the build info's `sources` and app count (they have their
own `extensions` entry). Together that hides them from versions of uas from before extensions, which would
otherwise show them in app searches with no way to install them, so adding them needed no schema version
change.

**Fonts and themes in the catalog.** Fonts, icon themes and cursor themes (`Kind` `font`, `icon-theme`,
`cursor-theme`) are in every edition the same way: rows of the `apps` and `listings` tables with their own
search index (`appearance_fts`), searched and listed only when asked for by kind (`SearchOptions.Kind`,
`BrowseQuery.Kind`), never merged with apps or each other (`merge`), and with their fetch status in the build
info's `appearance` list, not `sources`, and out of the app count. Released clients therefore never see
them: their app searches use `apps_fts`, their browsing lists desktop apps or one source of
`SourcePrecedence`, which the new sources aren't in. No schema version change was needed. A Google font's
popularity is its rank, which `Listing.Popularity` turns into a number that sorts with Nerd Fonts' downloads.

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
  is listed with "doesn't update itself". An added AppImage that stays a file install can still be an app the
  catalog has from another source (CurseForge from its site, with CurseForge on Flathub). No listing
  describes that copy, so it is matched to the app by what it calls itself (`backend.InstalledApp.SameApp`):
  its AppStream ID when it ships metainfo, else its launcher's file name against the last part of the app's ID
  or repo (`curseforge` for `com.overwolf.curseforge`, `t3code` for `pingdotgg/t3code`) together with the
  same name. The app's page then manages that copy (Open, Update, Remove, "Your AppImage") instead of
  offering Install, lists mark it installed, the Installed page shows it as the catalog's app, and
  `uas install` says it is there. Launchers another tool (such as Gear Lever) wrote for the added
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
- **codeberg:** the github backend with `Forge` set. Installs go in the same folders and state file
  (recorded with their `source`), and files are checked against the SHA-256 the indexer recorded, since
  Codeberg publishes none. Asset URLs must be under the repo's own release downloads.
- **rpmostree:** Calls the `rpm-ostree` CLI for v1. Its D-Bus API can replace this later for finer progress.
  New layers go onto the newest deployment, which may be a staged update to the next Fedora release, so the
  backend reads that deployment's release (from the kernel version in its image metadata) and only offers
  listings for it. It refuses packages already in the image (`rpm -q`), lists layered packages with a
  "takes effect after restart" flag, and `update` explains that layered packages update with the system.
  Bazzite's polkit rules let `wheel` users layer without a password. Install without restarting: before a
  Fedora install, `CheckLive` runs `rpm-ostree install --dry-run` and offers `--apply-live` when the
  transaction only adds packages, none is a kernel, kmod, driver, firmware, boot or SELinux policy package,
  none replaces a version the running system has (`rpm -q`), and no new image is staged. Anything it can't
  read means a normal layered install. It never passes `--allow-replacement`. After a live apply it adds
  and removes a hidden launcher in `~/.local/share/applications`, since GNOME Shell doesn't notice the new
  launcher in the live-updated `/usr` otherwise. A package applied live isn't flagged "after restart": the
  booted deployment's `live-replaced` commit (or `rpm -q`) shows it's in the running system. The app page
  runs the check in the background for a layered Fedora listing of an app that isn't installed (never for
  lists: each dry run takes seconds, and rpm-ostree runs one transaction at a time) and marks the source
  with a bolt when it passes. A dry run needs the same polkit action as an install
  (`org.projectatomic.rpmostree1.install-uninstall-packages`), so the page runs it only right after
  `pkcheck` (without `--allow-user-interaction`, for uas's own process) exits 0; otherwise (a user not in
  `wheel`, a remote session, no such rule) it skips the check and shows no bolt, rather than ask for a
  password for looking at an app; the install dialog reuses the answer (fresh for 3 minutes, dropped after any
  job). uas's own rpm-ostree transactions take turns: an install or removal cancels a check in flight, and
  retries for up to 2 minutes while rpm-ostree says "Transaction in progress" (a cancelled dry run keeps the
  daemon busy for a few seconds, and so do jobs uas didn't start).
- **copr:** If the project's repo isn't enabled, writes the same `.repo` file `dnf copr enable` would (same
  name, `gpgcheck=1` with the project's key) to a temp file and copies it into `/etc/yum.repos.d` with
  `pkexec install`, then layers the package through the rpmostree backend, or on package-based Fedora
  installs it with `pkexec dnf install -y` (`-y` also accepts the project's signing key the repo file names;
  `gpgcheck=1` refuses packages it didn't sign). With dnf, updates are per package (`dnf upgrade`) and
  `Updates` lists newer builds from `dnf list --upgrades`. A custom polkit policy isn't
  possible because `/usr` is read-only, so the password prompt uses pkexec's generic wording. Installs are
  recorded in the state file; removing the last package uas installed from a project also removes the repo
  file, but only if uas created it. Packages installed from COPR are listed under COPR, not Fedora (with dnf, `pkgmgr.Backend.Exclude`). The dnf path was tested
  as root in a Fedora 45 container (November 2026); layering was not tested live, since enabling a repo needs
  a password.

### Install from URL

`internal/client/fromurl`, `session/fromurl.go`. The catalog leaves out most small GitHub projects (they fall
under the searches' star bars or the trust score's), so a user can paste a link to the app's repo instead:
into the search box, the "Install from a link" dialog (on the empty search page), or `uas install <url>` and
`uas info <url>`. `fromurl.Parse` reads repo pages and any page under them (releases, a release, a release
file's download link, code), `.git` and `git@` addresses, on github.com and codeberg.org; a bare `owner/name`
is not a link, since it could be an app's name. A link to a repo the catalog has (its release listing, or any
listing whose source code link is the repo, as a Flathub app's `vcs-browser`) opens that app's usual page,
without asking GitHub.

Otherwise the client reads the repo itself, the way the indexer does (`github.Fetcher.Live`, beside the
GraphQL fetcher so file picking, `trust.GitHub`, release notes and AppImage reading are the same code):
GitHub's REST API for the repo and its eight newest releases, keyless (60 requests an hour per address), then
range requests into the release's AppImage for the app's name, icon, categories and metainfo, and the README
or the repo's files for screenshots or an icon (at most two more API requests). What it read is kept per
release file under `~/.cache/uas/live`, icons included, and each repo's result for an hour in memory. Unlike
the catalog it keeps archived repos and forks (flagged in the trust score), projects below the score bar, and
releases with nothing installable, so the page can say why: only system packages (midscroll ships an `.rpm`
and a `.deb`; uas installs packages only from repos), no file for the architecture, or no checksum. The
listing has `Live` set and the app the ID a GitHub-only catalog app would have (`github:owner/name`), so the
app page, Install, the confirmation (always a warning), Open and Remove are the catalog's. The page and the
install notes say the app isn't in the catalog and that uas installs only the release file, checked against
GitHub's checksum, never the project's scripts.

The install is recorded in the state file like a catalog one, with `from_url`. The GitHub backend's updates
ask the catalog first, and for such an install, when the catalog doesn't have it, the repo (`Backend.Live`,
without the AppImage read: two requests an hour per app at most). Once the catalog has the app, its listing
takes over and `from_url` is cleared at the next update. Installed lists it with "installed from a link"
and opens its page; exports carry `from_url`, and a restore reads such an app from its repo again (older
versions of uas list it as not in the catalog). Codeberg links open catalog apps only: Codeberg publishes no
checksums, so an app read from it now would have nothing to check the download against.

### Background update checks

`internal/client/updatecheck`, off until the user turns it on (Settings → Updates, or
`uas settings update_checks on`). Turning it on writes a systemd user timer and service
(`~/.config/systemd/user/uas-update-check.{timer,service}`: daily, 15 minutes after login, a random delay of
up to an hour, catching up after the machine was off) and enables it; turning it off disables and deletes
them, so the units are the setting. The service runs the program that turned it on (`uas gui --check-updates`
or `uas check-updates -notify`), which refreshes the catalog, lists updates and, if any are new since the
last notification (remembered with versions in `~/.local/state/uas/notified.json`), shows one notification
through `org.freedesktop.Notifications` on the session bus. It talks D-Bus directly (`internal/client/notify`)
because notify-send is broken on some systems (Bazzite 45's fails with a symbol lookup error). A click on
"Show updates" within an hour opens the app on its Updates page (`uas gui --page updates`), started through
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

### Migrating AppImages from other tools

Settings → Migrate apps to uas, and `uas migrate [-n] [-y] [-all] [-keep-leftovers]`
(`session/migrate.go`). Only AppImages migrate: Flatpaks, Homebrew formulae and packages are already handled
through their own tools whoever installed them, and an app installed by hand has no uninstaller uas could
trust.

A scan (`github.Backend.Strays`) lists the AppImage files uas doesn't manage: the ones launchers of other
tools run, wherever they are (the launchers in the user's applications folder that uas didn't write and whose
`TryExec` or `Exec` is an `.AppImage`), and the files on the top level of `~/AppImages`, `~/Applications`,
Gear Lever's folder, `~/Apps`, `~/.local/bin`, `~/bin`, `~/Desktop` and `~/Downloads`. A file counts only if
it starts like an AppImage (the ELF header with the `AI\x02` magic). Each is read without being run, as Add
AppImage does, to say what migrating it does: whether it becomes the catalog's GitHub listing of the app or
updates from its own update information, or can't update. An AppImage of an app uas already manages is
listed but can't be migrated, since adding it would replace uas's copy, which may be newer. The ones
another tool set up or that sit in an AppImage folder are offered selected; a file that merely sits in
Downloads or on the desktop is listed for the user to pick.

Migrating one is Add AppImage (`Session.AddAppImage`): the file moves into uas's folder and is deleted from
where it was only once uas's copy is in place, uas writes its launcher and removes the other tool's, and
GNOME's dock favorites point at the new one. What the user set up around the app follows it
(`github/carryover.go`, for Add AppImage too): default-app settings in `mimeapps.list` that named the old
launcher name uas's, so a browser stays the default for web links; autostart entries that ran the old path
run the new one; and the AppImage's portable folders (`<file>.home`, `<file>.config`, where an AppImage run
that way keeps its data) move beside the new file. The app's ordinary data is in the home folder's usual
places and is never touched. Launch arguments come from the AppImage itself (below), so ones a user added in the other tool
are not carried over, except for an AppImage uas can't read, whose arguments are taken from the launcher it
replaces. Then the other tool's own notes are removed
(`internal/client/gearlever`). Gear Lever keeps a section per AppImage in `gearlever.conf`
(`[app.<md5>]` with `name` and `file_path`, plus sub-sections such as `update_manager`) and an icon in
`<folder>/.icons/<file name>`; the section and icon of a migrated AppImage are removed, and the rest of the
file is left byte for byte. Sections whose AppImage is already gone ("leftovers", as after AppImages were
moved out of Gear Lever's folder by something else) are listed too and cleared when the user leaves that
box ticked; default-app settings that still name Gear Lever's launcher for an app uas now manages are
pointed at uas's launcher then, and an app uas can't read gets the launch arguments Gear Lever recorded
(`default_exec_arguments`), if uas has none for it.

**Launch arguments.** A launcher uas writes always runs the AppImage, never the command of the AppImage's own
entry, but it keeps that entry's arguments (`appimage.LaunchArgs`): what the app starts itself with, such as
`--no-sandbox` for Electron apps packed without a sandbox helper or `--gtk-single-instance=true`, and one
file or URL placeholder. Arguments are kept only when every one is plain (letters, digits and `_=.,:/+@-`),
so nothing needs quoting and nothing can be more than an argument; otherwise only the placeholder is kept.
Until launcher version 3 uas kept only the placeholder, which started CurseForge without `--no-sandbox`.
For an AppImage uas can't read (DwarFS), the arguments of the launcher it takes over are recorded in the
state file (`exec_args`) and reused on every rewrite. Launchers from older versions are rewritten when uas
starts. Tested for real against Gear Lever 4.6.2 (Flatpak): an AppImage integrated with
`gearlever --integrate`, then migrated. AppImageLauncher's launchers (`appimagekit_*.desktop`) are recognised
and replaced, but that path has not been tested against AppImageLauncher itself, and the icons it caches
are left.

### GNOME Shell extensions

`internal/client/shellext`, in a GNOME session (`XDG_CURRENT_DESKTOP` names GNOME and `org.gnome.Shell` is on
the session bus), talks to the Shell's own D-Bus interface, `org.gnome.Shell.Extensions`, as GNOME's
Extensions app and the browser connector do. It calls the Shell itself (`org.gnome.Shell` at
`/org/gnome/Shell`), not the helper service named like the interface: that is a separate program started on
demand, which exits after two idle seconds and forwards each call with a 25 second limit, less than a user
may take to answer the install dialog. Extensions
aren't apps and have no backend: the Shell lists every extension it knows (`ListExtensions`: the user's under
`~/.local/share/gnome-shell/extensions` and the system's under `/usr`, whoever installed them), installs one
from extensions.gnome.org by UUID after asking the user in its own dialog (`InstallRemoteExtension`, which
answers `successful` or `cancelled`), removes the user's (`UninstallExtension`), switches them
(`EnableExtension`, `DisableExtension`), opens their settings (`OpenExtensionPrefs`) and checks for updates
(`CheckForUpdates`), which it downloads and installs at the next login. uas never unpacks an extension itself,
and nothing but a UUID (letters, digits and `-@._`) reaches the bus. The Shell is written in JavaScript, so
its numbers arrive as doubles.

The desktop app's Extensions page (in the sidebar on GNOME only) has two tabs. Installed lists what the Shell
reports, the user's and then the system's ("Included with Bazzite", which can be switched but not removed),
each with its switch, settings and removal, matched to the catalog by UUID for an icon and a page; it asks the
Shell again every five seconds while it is on screen, so changes made in other apps show up. Browse lists the
catalog's extensions that have a version for the running GNOME release, most downloaded first, and the top
bar searches them. An extension's page shows its downloads, the releases it supports, its screenshot and
description (plain text, as its author wrote it), and Install or its switch. uas doesn't ask before
installing, since the Shell does. Apps and extensions are searched apart, so the app search page says when
extensions match the search too ("9 GNOME extensions match “blur”") and leads to them. CLI: `uas extensions [list | search | install | remove | enable | disable |
settings]`.

**Updates.** The Shell can't swap a running extension's code, so nothing installs an extension update
before the next login, uas included. The Shell looks for updates of the user's extensions once a day (the
system's update with the system), unpacks each one into `~/.local/share/gnome-shell/extension-updates/<uuid>`
and marks the extension `hasUpdate`; at login it moves them into place. uas shows that state: the Updates
page lists those extensions in their own section as "At next login", with the installed version and the
downloaded one (read from the download's `metadata.json`), and Update all and the sidebar's count leave them
out, since there is nothing to press. Opening or refreshing the page asks the Shell to look now
(`CheckForUpdates`), at most once an hour unless Refresh is pressed, since each look asks
extensions.gnome.org. `uas updates` lists them too, and the background check names them in its notification
(it reads what the Shell has found and doesn't ask it to look).

**Backups.** An export lists the extensions worth restoring (`extensions`: UUID, name, on or off): every one
the user added, and the system's that are switched on. Restoring installs the ones that aren't on the system
when extensions.gnome.org has a version for its GNOME release (the Shell asks about each), switches a newly
installed one off again if it was off, and switches on the ones that are there but off. It never switches
anything off, and it says why for the rest ("no version for GNOME 51", "came with Bazzite"). Extensions'
own settings (dconf) aren't exported. Older versions of uas ignore the field, so the export format is still
version 1.

### Fonts, icon themes, cursor themes, GTK themes and GNOME Shell themes

**Reading archives** (`internal/appearance`, shared by the indexer and the client). A theme archive
(`.tar.gz`, `.tar.xz`, `.tar.bz2`, `.tar.zst` or `.zip`; 7-Zip and RAR are refused) is read entry by entry
before anything is written. It is refused for an entry with an absolute path or one that leaves the archive,
a device or pipe, more than 200,000 entries, more than 2 GB unpacked or a file over 64 MB, and for compiled
code anywhere in it: ELF files and files named like shared libraries (`.so`, `.so.1`), which a theme has no
use for. Other files that could be run are skipped, never written, and the rest installs: files starting
with `#!`, and files named like scripts, launchers, plugins, QML, JavaScript, build files (`Makefile`,
`meson.build`) or other archives (`Contents.LeftOut`). Many themes carry a helper script (Kora's
`create-new-icon-theme.cache.sh`), and refusing the theme for it would be no safer, since a skipped file
never reaches the disk. A link that leads to a skipped file is skipped too, and a link to a folder writes
the folder without them. What makes a theme is never skipped: an `index.theme` or a file in a `cursors`
folder that looks runnable refuses the archive, and so does an archive left with no theme. The indexer and
the client run the same checks, so the themes the catalog pins are the ones installed, without the same
files; the client records how many it left out, and an item whose file holds some says so before install.
Themes are found by folder: an `index.theme` whose `[Icon Theme]` section lists icon folders is an icon
theme, a `cursors` folder a cursor theme, and a `gtk-3.0` or `gtk-4.0` folder with a `gtk.css` or
`gtk-dark.css` (a file, or a link to one in the archive) a GTK theme. That is what GTK 3 and 4 load, so a
theme's source code (`src/gtk-3.0/*.scss`) or a GNOME Shell style alone is no theme. What else a GTK theme's
folder holds (`gnome-shell`, `gtk-2.0`, `metacity-1`, `xfwm4`, `cinnamon`, `plank`, `assets`) is written
with it as part of the theme, after the same checks; uas uses the GTK 3 and 4 styles, and the `gnome-shell`
folder as a GNOME Shell theme (below). The archive is
refused when none is found, or they can't be told apart cleanly: a theme with no folder of its own, one
inside another, two of one name, or a name that would take over the desktop's own (`default`, `hicolor`;
for GTK themes `Default` and the ones GTK has built in, `Adwaita`, `Adwaita-dark`, `HighContrast`,
`HighContrastInverse`, `Emacs`). Icon and cursor themes are judged apart from GTK themes, since they install
to other folders: an icon theme inside a GTK theme's folder is no theme inside another, and an archive whose
icon or cursor themes are clean keeps them, as before GTK themes, when its GTK themes aren't (those are left
out of it). So no icon or cursor archive the catalog had changes, and `RulesVersion` stays 3. Themes rely on links (a cursor theme names one cursor
many ways, Papirus-Dark links most of its folders to Papirus's), so links are allowed but never written: a
link to a file or folder inside the archive becomes hard links between files the package wrote (or copies),
and a link that leads anywhere else (out of the archive, to an absolute path, to nothing) is left out, with
nothing written for it. Everything is written through an `os.Root` on a new, empty folder, without execute
permission, and a theme's own `icon-theme.cache` is left out. Papirus, whose variants and `@2x` folders are
links, unpacks to about 890,000 entries this way, in about 25 seconds. Font archives may hold only TTF and
OTF files (and their licence and readme, which are skipped), each checked with a font parser.

**Installing** (`internal/client/appearance`, `session.InstallLook`). Everything is per user, with no root.
A theme's file is chosen by the variant picked; the client asks Pling's API for the item's files
(`/content/data/<id>`), finds the file by name and MD5 (an author can reorder files; a changed MD5 means the
file changed since the catalog was built, and it stops there), takes its fresh link (only on Pling's own
hosts, over HTTPS), downloads it into a new folder under `~/.local/share/uas/staging`, and refuses it unless
its size and SHA-256 are the catalog's. Every theme of the listing's kind in the file is installed, as
`~/.local/share/icons/<name>` for icon and cursor themes (not `~/.icons`, which Flatpak apps can't see) and
`$XDG_DATA_HOME/themes/<name>` (`~/.local/share/themes`, not `~/.themes`) for GTK themes, since variants
inherit from each other. A name that would replace a theme uas didn't install, hide one in `~/.icons` (or
`~/.themes`), or hide one of the system's (`icons`, or `themes`, under each of `$XDG_DATA_DIRS`) is refused
before the download. Icon and cursor themes share their folder's names; GTK themes have their own, so an icon
theme and a GTK theme may both be called Orchis. Google fonts are downloaded file by file from the pinned commit (`raw.githubusercontent.com`
only), checked against their SHA-256 and as fonts; Nerd Fonts' archive from its GitHub release. Fonts go to
`~/.local/share/fonts/uas/<source>-<family>/`, then `fc-cache` runs. uas creates `~/.local/share/fonts` when
it starts, since Flatpak shows apps the user's fonts only if the folder exists when the app starts. Each
install is recorded in the state file (`appearance`: catalog ID, version, file, SHA-256, themes or font
families, paths written), which removal and updates use; removal deletes only those paths, and only under
the three folders uas installs to.

**Using.** Settings have desktop-neutral keys (`appearance.SettingKeys`), and each `Desktop` maps them to its
own and says which it `Supports`. On GNOME, `gsettings` sets `org.gnome.desktop.interface`'s `gtk-theme`,
`icon-theme`, `cursor-theme`, `font-name`, `document-font-name` and `monospace-font-name` (a family alone keeps the
setting's size; a font with a size is set as it is), `color-scheme` (default, light or dark) and
`accent-color`; the last two are offered only when `gsettings list-keys` has them (GNOME 42 and 47). The
wallpaper is three settings, `wallpaper`, `wallpaper-dark` and `wallpaper-fit`: `org.gnome.desktop.background`'s
`picture-uri`, `picture-uri-dark` and `picture-options` (zoom, centered, scaled, stretched, spanned, wallpaper,
none). uas takes and gives paths; the GNOME layer writes them as `file://` URIs with everything outside a URI
path's characters escaped (spaces, `#`, `%`, `?`, non-ASCII bytes) and reads them back, and a value that isn't
a file URI is kept as it is. Setting the wallpaper sets `org.gnome.desktop.screensaver picture-uri` too, as
GNOME's settings do, and one picture with no dark variant is used for both styles. Font
settings are Pango descriptions, read from the end as Pango does (`fontname.go`), so `Fira Code weight=450
10` is the family Fira Code. On KDE Plasma, `plasma-changeicons` and `plasma-apply-cursortheme` change icons
and cursors (fonts and GTK themes are installed only, and the wallpaper isn't set: Plasma sets the GTK theme
through its own `kde-gtk-config` module, which writes GTK's files itself and has no command uas could test).
Other desktops install only. Before the first change of a setting
the state file saves its value (`saved_settings`); Revert puts it back (Revert all puts back every one), a
change back to that value forgets it, and removing a font or theme that is in use reverts first (or resets
the setting when uas didn't change it). The wallpaper's three settings are listed and reverted as one,
"Wallpaper": `revert wallpaper` puts back each of them uas changed (or resets all three when it changed none).

**GNOME Shell themes** (`client/appearance/shell.go`). A Shell theme is a theme folder with
`gnome-shell/gnome-shell.css`, found where GNOME's User Themes extension looks, in its order: `~/.themes/<name>`,
`$XDG_DATA_HOME/themes/<name>`, then each system data folder's `themes/<name>`; a theme found first hides one
of the same name (`ShellThemes`). User Themes also reads `<data dir>/gnome-shell/theme/<name>.css`, which
uas doesn't list. Most GTK themes on Pling carry one, so a GTK theme uas installs is often a Shell theme too,
listed with the GTK theme it came with; uas's GTK theme records are what Remove, `OriginOf` and a preset's
`from` use for it. The setting is `KeyShellTheme`, GNOME's `org.gnome.shell.extensions.user-theme name`
(`""` is the Shell's own look, which `use shell default` sets), and it has Use, Revert, `revert all`,
"Changed by uas" and the revert before a removal like the others; Plasma and other desktops don't support it.
GNOME applies it only while User Themes runs. `UserThemes` asks the Shell (`ListExtensions` and
`UserExtensionsEnabled`, through `shellext`) whether the extension is installed, on, allowed to run (the main
switch) and working. Use refuses a theme that isn't on the system, and while User Themes isn't running it
changes nothing and returns a `NeedsUserThemesError`; the app and the CLI then say why and offer to fix it
(install it from extensions.gnome.org, where the Shell asks in its own dialog; turn on the main switch; turn
the extension on), and `EnableUserThemes` does only that, when the user agrees. Nothing is ever turned off.
When the extension is installed for the user its schema is in its own folder
(`<extensions>/user-theme@…/schemas`), which plain `gsettings` can't see: the GNOME layer then reads and writes
the setting with `gsettings --schemadir <that folder>`. It looks for the folder under
`$XDG_DATA_HOME/gnome-shell/extensions` and each `$XDG_DATA_DIRS`' `gnome-shell/extensions`, only when
`gsettings list-keys` doesn't list the schema system-wide (as on Bazzite, where User Themes comes with the
system and is on). Outside a GNOME session the Shell can't be asked and uas doesn't stop a change.
Shell themes are written for particular GNOME versions; one short line says so where a Shell theme is chosen.
**Where they come from:** only from theme folders already on the system, including GTK themes uas installs.
Pling's "GNOME Shell Themes" category (134) stays out of the catalog: the popular Shell themes come with GTK
themes uas already lists, a Shell-only item is an archive shape the unpacker refuses today (a folder whose
only style is `gnome-shell`), and many are made for one GNOME release; adding it would be a new kind for the
indexer, the catalog and older clients. The catalog doesn't record which GTK themes carry a Shell theme (that
would mean reading every pinned archive again), so a GTK theme's page says so once it is installed.

**GTK 4 files** (`client/appearance/gtk4.go`). GTK 4 apps, libadwaita's above all, don't draw themselves with
the `gtk-theme` setting; they load the user's own style, `$XDG_CONFIG_HOME/gtk-4.0/gtk.css`, when they start.
So using a GTK theme (`Use`, Install and use, a preset's `gtk` part) also copies the files of the theme's
`gtk-4.0` folder, when it has a `gtk.css` there, into `~/.config/gtk-4.0`: `gtk.css`, `gtk-dark.css`, `assets`
and whatever else is in it (Marwaita's `gtk-base.css`, adw-gtk3's `libadwaita.css`). The theme is found as
GTK finds it (`~/.local/share/themes`, `~/.themes`, then the system's), so a theme uas didn't install works the
same. They are copies, not links into the theme as WhiteSur's and Colloid's install scripts make (Colloid
links `gtk.css`, `gtk-dark.css` and `assets`; WhiteSur compiles into the folder and links `gtk.css` to a
light or dark file): Bazzite lets every Flatpak app read `~/.config/gtk-4.0` (a system-wide override,
`xdg-config/gtk-4.0:ro`) but not `~/.local/share/themes`, so a link would lead nowhere in a sandbox, and a
copy stays whole when the theme is updated or removed. Links inside the theme's own folder are followed;
anything leading out of it is left out, and no file is written executable. A theme's dark look is whatever
its own `gtk.css` is: a dark variant that is a folder of its own (Orchis-Dark) is chosen as that theme, and
`gtk-dark.css` comes along as it is (GTK 4 itself loads only `gtk.css` from the folder). Only the names uas
wrote are its own (`gtk4_files` in the state file: the theme, the folder, the names written, and where the
user's are kept). Before writing a name the user has there (a file, a folder or a link), uas moves it to
`$XDG_DATA_HOME/uas/gtk-4.0-yours-<n>/` and records it; nothing else in the folder (`settings.ini`, the
user's other files) is touched. Using another theme takes uas's files away and copies the new theme's (a
user's file whose name the new theme doesn't have comes back); a theme without GTK 4 files, Revert,
`revert all` and removing the theme (in use, or the source of the files even after the setting was changed
elsewhere) take uas's files away and put the user's back as they were, then remove the empty backup folder.
While uas's files are there, `gtk-theme` stays in "Changed by uas", even when the setting already had the
theme. An update of the theme copies its files again. The record matches the disk after every step, even one
that fails part-way, and a name in it that isn't one plain folder entry is ignored. Open apps load the new
files when they start again. Nothing sets `GTK_THEME` or changes libadwaita itself.

**Presets** (`client/appearance/preset.go`, `presetapply.go`, `session/presets.go`, `gui/presets.go`) are looks
saved under a name, one JSON file each in `~/.config/uas/presets/<slug>.json` (`uas_preset` format version,
name, times, and `parts`). A part is optional and is left alone when missing; it holds the setting's `value`
and, for a font or theme uas installed, `from` (its catalog ID and variant), so it can be installed elsewhere.
The parts are `scheme`, `accent`, `gtk`, `shell`, `icons`, `cursor`, `interface`, `document`, `monospace` and
`wallpaper`, and parts and fields this version doesn't know survive a rewrite. The `shell` part is the GNOME
Shell theme by name, as the format reserved it (a part with no value is the Shell's own look), with `from`
naming the GTK theme of the catalog it came with; it never holds the User Themes extension or extension
settings, which presets don't save. Applying one plans it as set, already so, installed with its GTK theme
first, or, while User Themes isn't running, `user-themes`: skipped unless the user agrees in the apply
confirmation (the app's box, the CLI's question; never `-y` alone), when it is turned on first. The Shell's
own look needs no extension. The `gtk` part is the GTK theme by name, as the format reserved it, so files from before it (with no
`gtk` part, or one a hand wrote) read as they are; applying one copies its GTK 4 files as Use does. Its
optional `gtk4: false` leaves them out and puts back what uas moved aside: a Previous look has it when uas
had no GTK 4 files in place before the apply, so Undo restores `~/.config/gtk-4.0` as it was. The
**wallpaper** part has `light`, an optional `dark` (none is the light picture) and `fit` (zoom when missing) in
place of `value`. A picture of the user's (jpg, jpeg, png, webp, jxl or svg) is copied into the wallpaper
store, `$XDG_DATA_HOME/uas/wallpapers/<sha256>.<ext>`, and named `sha256:<hex>`, so moving or deleting the
original doesn't break the preset, and the same picture is stored once; the system's (under a `backgrounds`
folder of `$XDG_DATA_DIRS`, GNOME's `.xml` slideshows too) are named by path and never copied. The system's
wallpapers are listed from `gnome-background-properties/*.xml` in each data folder, which pair a light
`filename` with a `filename-dark` and give a name and `options`; files that are missing or malformed are
skipped. A stored picture is removed once no preset, no Previous look, no value saved for Revert and not the
desktop's current wallpaper uses it (`PruneWallpapers`, run after a preset changes and after the wallpaper is
set or reverted); one added in the last ten minutes is kept, as a preset may be about to use it, and nothing is
removed when any of those can't be read. Names are unique whatever their case; a clash needs Replace
(`-replace`). Capture reads every setting the desktop supports, the wallpaper's picture copied into the store
when it is the user's. **Applying** plans each part (set, already so, install from the catalog first, or
skipped: missing (a wallpaper whose picture can't be found), unsupported here, or a newer uas's), installs what
is missing before anything changes, saves the values it is about to change to
`~/.local/share/uas/previous-look.json` for Undo, then sets the parts in order (style, accent, GTK theme,
Shell theme, icons, cursor, fonts, the wallpaper last) through `Use`, so Revert still goes back to the values from before uas. A part that
fails is reported and the rest are still set. A GTK theme the setting already has is "already so" unless it has
GTK 4 files uas hasn't copied, which applying copies. "In use" is worked out by comparing a preset's parts with the
settings, the wallpaper's pictures and fit too. CLI: `uas appearance preset`, `uas appearance use wallpaper
<file> [-dark <file>] [-fit <mode>]` and `uas appearance use shell <name>|default`.

**The Appearance page** (`gui/appearance.go`) opens on Presets once there are any: cards with a preview in
the preset's style (its icon theme's icons, its accent, in front of its wallpaper when it has one), Apply and
a menu (Change, Rename, Duplicate, Delete), Save current look and New preset; a preset's page
(`gui/presets.go`) changes each part or leaves it as it is. Its Wallpaper row (`gui/wallpapers.go`) has a
light and a dark slot, each with Choose… (a file, through the portal's file chooser filtered to the kinds the
store takes, or the system's wallpapers, a grid of thumbnails with names, where choosing a light picture takes
its dark pair too), "Same for dark", the fit and Leave as is. **Wallpaper thumbnails** are made off the UI
thread and never at full size: the desktop's own thumbnail (`~/.cache/thumbnails/<size>/<md5 of the
URI>.png`, when its `Thumb::MTime` is the picture's), else the file decoded (two at a time) and averaged
down to 520 pixels wide; an SVG is drawn as vectors, a slideshow by its first picture. JPEG, PNG, WebP and
GIF decode with Go's decoders; JPEG XL with the pure-Go decoder already in the build, which can't read many
of GNOME's own (they use features it lacks), so those show a quiet tile with the file's name unless GNOME
has thumbnailed them. Its other tabs are Fonts, Icons, Cursors and GTK themes, each a grid of cards, most
popular first (or recently updated, or by name), searched from the top bar (a catalog from before GTK themes
says so, with Check for a newer catalog), and Installed: what uas installed with what it's in use for, the
settings it changed with Revert (the GTK theme's row names the GTK 4 files uas copied), and every GTK, icon
and cursor theme on the system, the one in use marked and the others with Use. On GNOME a Shell themes tab
lists the Shell themes on the system as rows (Default, GNOME's own look, first), the one in use marked and
the others with Use, under a note when User Themes isn't running with the button that fixes it; Use then asks
first. An installed GTK theme with a Shell theme says so on its page, with Use for the Shell. uas draws no preview of a
GTK theme and its own window keeps its look; one short line on a GTK theme's page and on a preset's GTK
theme row says that GTK 4 apps change only with themes that have GTK 4 files, and open apps after a restart. A theme's page shows its preview pictures
(linked from Pling, as screenshots are), downloads, rating (or "Not rated"), last change, version, trust
score, the variant picker, and Install, Install and use, Use, Remove and Revert. **Font previews**: Jamb
draws text only in fonts registered when the app starts and the desktop's own families, so a font that
isn't installed can't be set on a text. uas draws the sample itself instead (`gui/fontpreview.go`): go-text
(already in the build through Jamb) shapes it, at regular weight for a variable font, and
`golang.org/x/image/vector` fills the outlines into an image. The file is the family's first (regular),
downloaded into `~/.cache/uas/fonts` and checked against the catalog, which the install then reuses. Cards
download it only when it is under 1 MB; Nerd Fonts come as one archive per family, so they are previewed
once installed. A font lacking letters of the sample (a CJK font) says it is for another script. **Updates**
for fonts and themes uas installed are listed in their own section of the Updates page, when the catalog's
file has another SHA-256 than the installed one. CLI: `uas appearance`.

### Back up and restore

`session/export.go`. An export is JSON: each installed app's source, ref, app ID, package and name, the
GNOME Shell extensions (see above), the Flatpak scope setting, and the user's Flatpak overrides (the files under `~/.local/share/flatpak/overrides`),
with no paths or user names. Restoring plans each app: already installed (from any source) is skipped; the
same listing is installed when this system supports it; otherwise the same source's other listing (another
Fedora release), else the source uas recommends ("from Flathub instead of Fedora"). Two exported copies of
one catalog app install once. AppImages added from files can't be fetched again and are listed for the user.
Layered packages go last. Overrides are written only for apps with none here yet. CLI: `uas export`,
`uas restore [-n] [-y]`; desktop: Installed → Back up.

### UI (Jamb)

`uas gui` (`cmd/uas/gui.go`) with `internal/gui`, on top of `internal/client/session`, the engine the CLI uses too (settings,
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

Not a Flatpak: the sandbox would block calling `rpm-ostree`, `brew` and `pkexec`. Not an AppImage either:
that needs FUSE and a runtime, mounts itself on every run (the CLI and the background check run often),
and is the kind of file uas itself manages. A release is one static program per architecture
(`packaging/linux/build-release.sh`; no cgo, Jamb loads Wayland through purego), `uas-<version>-linux-<arch>`,
which is the CLI and the desktop app (`uas gui`, or `uas-gui`, a link to it), carries its launcher and icon
(`packaging/linux`, embedded) and the licenses of everything in it (`uas licenses`, `internal/legal`), and
installs itself (`internal/client/selfinstall`). Run with no arguments from where it was downloaded (not
from the install, and not from a folder on the PATH, where a package would put it), it copies itself to
`~/.local/bin/uas`, links `uas-gui` to it, writes the launcher (with the full path) and the icon, and opens
the app; `uas self-install` does the same from a terminal. Nothing needs root, which suits Fedora Atomic and
its derivatives. Opened from a file manager there is no terminal, so a failure also shows as a
notification. `uas self-uninstall` and Settings → About → Uninstall remove the programs, the launcher, the
icon and the background check's units; apps, settings and the catalog stay.

Releases are GitHub releases. The Release workflow (on a `v*.*.*` tag, or by hand without publishing) builds
both architectures, checks that the x86_64 program installs itself into a clean home
(`packaging/linux/test-install.sh`), and `cmd/uas-release` prepares the release's files: the programs as
`uas-linux-<arch>` (no version in the name, so `releases/latest/download/uas-linux-<arch>` always downloads
the latest, which is the README's install link), `SHA256SUMS` for people, and `uas-release.json`, a signed
description of the release with each program's size and SHA-256 (the catalog's key, under its own signing
context, so a manifest signature can't pass as a release's). `gh release create` publishes them, after
checking that the version is newer than the latest release.

**Self-update** (`internal/client/selfupdate`): `uas self-update`, Settings → About, and the background check
(which lists "uas 0.6.0" with app updates) ask GitHub's API for the latest release
(`api.github.com/repos/tyler-builds/uas/releases/latest`, no account, so 60 requests an hour per address;
Settings checks at most hourly and the background check daily) and compare its tag with the running version.
Only when it is newer does uas download `uas-release.json`, verify it, and check that it is signed as the
tagged version and that the program's asset has the signed size. GitHub is trusted to say a release exists,
not for what it contains: a release changed by anyone without the signing key is refused, and an old signed
description can't pass as a newer release. Installing downloads the program for this architecture, checks
its size and SHA-256 against the signed values, writes it beside the running one, then renames it into
place, only if that folder is the user's. Rate limits (403 or 429 with `X-RateLimit-Remaining: 0` or
`Retry-After`) and network failures become messages that say when to try again. The window still runs the old
program, so in the desktop app the Update button then becomes Restart (disabled while an install or removal
runs, which it would cut off): the app quits and, once it has shut down, the same process becomes the program
now at its path (`exec`), opened on Settings. Builds from source report version `dev` and never update
themselves. A Homebrew tap can come later.

**Older installs.** uas 0.5.0 and older were two programs, `uas` and `uas-gui`, installed by an
`install.sh`, and they look for updates only in the catalog Worker's signed `releases/latest.json`, which
names archives on the Worker. Releases were published there while the repo was private; they no longer are,
so those copies stay on their version and report it as up to date (the Worker still serves the last one,
read-only, so they don't show an error). Installing the new program once by hand replaces such an install:
it makes `uas-gui` a link to `uas` and refreshes the launcher and icon (`Layout.Refresh`, on every start of a
release from its install; it changes nothing when they are up to date, and doesn't bring back a launcher the
user removed).

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
│   ├── uas/                 # the program: CLI (search, info, install, remove, update) and app (uas gui)
│   └── uas-indexer/         # crawler, run in CI
├── internal/
│   ├── catalog/             # shared types, SQLite schema, FTS queries (rebuilt each run, no migrations)
│   ├── manifest/            # signed manifest envelope (shared by indexer and client)
│   ├── appearance/          # safe reading of theme and font archives (shared by indexer and client)
│   ├── indexer/
│   │   ├── appstream/       # AppStream XML parser (Flathub, Fedora)
│   │   ├── fetch/           # Fetcher interface, HTTP cache with ETags
│   │   │   ├── flathub/
│   │   │   ├── fedora/
│   │   │   ├── copr/
│   │   │   ├── homebrew/
│   │   │   ├── github/
│   │   │   ├── gnome/       # extensions.gnome.org
│   │   │   ├── pling/       # icon, cursor and GTK themes from Pling
│   │   │   ├── googlefonts/ # github.com/google/fonts
│   │   │   └── nerdfonts/   # Nerd Fonts releases
│   │   ├── merge/           # dedupe and linking
│   │   ├── trust/           # scoring
│   │   └── publish/         # build db, compress, sign, upload
│   ├── client/
│   │   ├── appimage/        # reads .desktop and icon from an AppImage; writes the launcher
│   │   ├── gearlever/       # Gear Lever's records of its AppImages, for migrating them
│   │   ├── paths/
│   │   ├── platform/
│   │   ├── refresh/         # download, verify and install the catalog
│   │   ├── state/
│   │   ├── recommend/
│   │   ├── run/             # runs flatpak, brew, rpm-ostree, pkexec
│   │   ├── jobs/
│   │   ├── session/         # engine shared by CLI and GUI
│   │   ├── settings/
│   │   ├── shellext/        # GNOME Shell extensions, over the Shell's D-Bus service
│   │   ├── appearance/      # installs, uses and removes fonts and themes for the user
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
