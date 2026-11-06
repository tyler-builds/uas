<div align="center">

<img src="internal/gui/brand/uas.svg" width="96" height="96" alt="uas logo">

# uas

**One app store for every source.**<br>
Flathub, Homebrew, GitHub, GitLab and Codeberg releases, Fedora and COPR, searched and installed from one place.

![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/platform-Linux-0f766e)
![Display](https://img.shields.io/badge/display-Wayland-555)

<img src="docs/screenshots/home.png" alt="The uas home page: a Spotlight app, popular and new apps, categories and sources" width="900">

</div>

## Features

- **One catalog.** About 7,700 apps, merged so each app appears once with every source it's on.
- **A recommended source.** uas picks the best fit for your system and tells you why, with the other sources a
  click away.
- **Trust scores.** GitHub, GitLab and Codeberg releases and COPR projects are scored 0–100 from public signals, with the breakdown
  shown next to Install. See [docs/trust-score.md](docs/trust-score.md).
- **One list for everything installed,** whatever its source, with updates and Update all.
- **Flatpak permissions.** Turn an app's network, file, device or sound access on or off, as Flatseal does.
- **Your system's own tools.** On Bazzite, Bluefin and Aurora, the ujust commands (drivers, CoolerControl,
  Waydroid, audio tweaks…) are listed by group and run in a terminal window.
- **Update notifications.** Optionally, uas checks once a day and tells you when updates are ready. It never
  installs anything on its own.
- **Back up and restore.** Export the list of installed apps and install them on another computer, from
  whatever source fits it, with your Flatpak permission changes.
- **AppImages too.** Add one you downloaded and uas keeps it updated and in your app menu.
- **A signed catalog,** rebuilt every 6 hours and verified before use.
- **Native tools underneath.** uas installs through flatpak, brew and rpm-ostree, so nothing is hidden from them.

## Screenshots

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/app.png" alt="An app page with Install, stats, screenshots and the recommended source"></td>
    <td width="50%"><img src="docs/screenshots/search.png" alt="Search results across sources, with installs a year and release dates"></td>
  </tr>
  <tr>
    <td align="center">App page</td>
    <td align="center">Search</td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/installed.png" alt="Installed apps from Flathub and Homebrew, with an available update"></td>
    <td width="50%"><img src="docs/screenshots/home-light.png" alt="The home page in the light theme"></td>
  </tr>
  <tr>
    <td align="center">Installed</td>
    <td align="center">Light theme</td>
  </tr>
</table>

## Compatibility

uas runs on any Linux distribution and enables each source it finds tools for.

| Source | Needs | Works on |
| --- | --- | --- |
| Flathub | `flatpak` | Any distro |
| Homebrew | `brew` | Any distro |
| GitHub, GitLab and Codeberg releases, AppImages | — (Flatpak bundles need `flatpak`) | Any distro |
| Fedora packages | `rpm-ostree` | Fedora Atomic (Bazzite, Silverblue, Kinoite, Bluefin, Aurora…) |
| COPR | `rpm-ostree`, `pkexec` | Fedora Atomic |

The desktop app needs a **Wayland** session. The CLI runs anywhere.

**Tested on**

- Bazzite (Fedora 45) with GNOME, Wayland
- Bazzite with KDE Plasma, Wayland

**Notes for other systems**

- On package-based distros (Fedora Workstation, Ubuntu, Arch…), Fedora and COPR listings show as unavailable
  and uas recommends another source. dnf, apt and pacman aren't supported yet.
- On Fedora Atomic, Fedora and COPR packages are layered into the image and take effect after a restart.
- AppImage launchers and dock matching have only been tested on GNOME and KDE; other desktops should work
  but are untested.
- Missing `flatpak` or `brew`? Install it and those sources turn on the next time uas starts.

## Install

You need Go 1.27 or newer. From a clone of this repo:

```sh
go install ./cmd/uas-gui ./cmd/uas      # to ~/go/bin; make sure it's on your PATH

# launcher entry and icon
cp packaging/linux/io.github.tyler_builds.uas.desktop ~/.local/share/applications/
cp -r packaging/linux/icons ~/.local/share/
```

## CLI

```sh
uas search firefox
uas info org.mozilla.firefox
uas install org.mozilla.firefox
```

| Command | |
| --- | --- |
| `search <words>` | Find apps |
| `info <app-id>` | Show an app and its sources |
| `install <app-id>` | Install from the recommended source |
| `add <file.AppImage>` | Add an AppImage and keep it updated |
| `remove <app-id>` | Remove an app |
| `update <app-id>` | Update an app |
| `updates` | List available updates |
| `list` | List installed apps |
| `refresh` | Download the latest catalog |
| `system` | Show what uas detected |
| `settings [key value]` | Show or change settings (`update_checks on` turns on daily update notifications) |
| `check-updates [-notify]` | Refresh the catalog and list updates |
| `export [-o file]` | Save the list of installed apps |
| `restore <file>` | Install the apps of an export that aren't installed yet |
| `permissions <app-id> [<key> on\|off \| reset]` | Show or change what a Flatpak app may reach |

## FAQ

**Why not just use GNOME Software or KDE Discover?**<br>
They cover Flatpak and your distro's packages. uas also covers Homebrew, GitHub releases, COPR and AppImages,
compares the sources an app is on, and scores the ones no distribution reviews.

**Why not just use Flathub?**<br>
Use Flathub when it has the app; uas recommends it first for that reason. But plenty of tools aren't there: CLI
tools live in Homebrew, and many apps ship only as GitHub releases.

**Why not install GitHub releases by hand, or with Gear Lever?**<br>
You can. uas finds the release, checks its checksum, shows a trust score before you install, adds it to your
app menu and keeps it updated alongside everything else.

**Why not Distrobox?**<br>
Distrobox is great for a full distro's packages in a container. uas is for installing apps on the host from the
sources built for it. They work fine together.

**Does uas replace flatpak, brew or rpm-ostree?**<br>
No. It runs them for you. Anything uas installs shows up in those tools, and anything they install shows up
in uas.

**Is installing from GitHub, GitLab, Codeberg or COPR safe?**<br>
No distribution reviews those sources, so uas asks before installing, shows a trust score with its breakdown,
and verifies every download against a checksum: the one GitHub publishes, or for GitLab and Codeberg, which
publish none, the one uas recorded when it built the catalog. The score measures how well kept a project is, not
whether its code is safe.

**Does uas collect data?**<br>
No. It downloads the catalog and app icons, and talks to the sources you install from.

## How it works

```
 Flathub · Fedora · Homebrew · COPR · GitHub
                    │
            uas-indexer (every 6h)        merge, rank, score, sign
                    │
                    ▼
     Cloudflare Worker + R2 (catalog.db)
                    │
          ┌─────────┴─────────┐
       uas-gui              uas (CLI)      verify, search, install
          └─────────┬─────────┘            through the native tools
                    ▼
                your system
```

More in [docs/architecture.md](docs/architecture.md) and [docs/design.md](docs/design.md).

## Development

```sh
go test ./...
go run ./cmd/uas-gui
```

Build and use a local catalog:

```sh
go run ./cmd/uas-indexer -out out/catalog.db
UAS_CATALOG=out/catalog.db go run ./cmd/uas search firefox
```

Render every page of the desktop app in both themes (the README's screenshots come from this):

```sh
UAS_SHOTS=/tmp/uas-shots UAS_CATALOG=out/catalog.db go test ./internal/gui -run TestShots
```

**Publishing.** The `Index` workflow builds and publishes the catalog every 6 hours, or by hand from the
Actions tab. It needs the `UAS_SIGNING_KEY` and `UAS_UPLOAD_TOKEN` secrets. GitLab is indexed only if the
optional `GITLAB_TOKEN` secret holds a GitLab.com personal access token with the `read_api` scope, because
GitLab allows too few keyless requests. The catalog Worker is in
`deploy/cloudflare`; deploy it with `npx wrangler deploy`.

## Acknowledgements

Built with [jamb](https://github.com/tyler-builds/jamb). Interface font: [Inter](https://rsms.me/inter/)
(SIL Open Font License). Source logos: [Simple Icons](https://simpleicons.org/).
