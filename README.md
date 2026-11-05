<div align="center">

<img src="internal/gui/brand/uas.svg" width="96" height="96" alt="uas logo">

# uas

**One app store for Bazzite.**<br>
Find, install and update apps from Flathub, Fedora, Homebrew, COPR and GitHub, compared and ranked in one place.

![Go](https://img.shields.io/badge/Go-1.27-00ADD8?logo=go&logoColor=white)
![Platform](https://img.shields.io/badge/platform-Bazzite%20%C2%B7%20Fedora%20Atomic-0f766e)
![Display](https://img.shields.io/badge/display-Wayland-555)

<img src="docs/screenshots/home.png" alt="The uas home page: a Spotlight app, popular and new apps, categories and sources" width="900">

</div>

## Why uas

On an image-based system like Bazzite, the same app can come from five places, and each one behaves differently.
A Flatpak is sandboxed and installs right away. A Fedora package is layered into the system image and
needs a restart. A Homebrew formula installs for your user. A GitHub release is whatever its developer
published. uas puts all of them in one catalog and lets you pick with that context in front of you.

- **One search across every source.** About 7,700 apps from Flathub, Fedora, Homebrew, COPR and GitHub
  releases, merged so each app appears once with all the places it's available.
- **A recommended source for every app.** uas picks the best fit for your system (sandboxed and
  restart-free first) and explains why, with the alternatives a click away.
- **Trust scores for unreviewed sources.** GitHub releases and COPR projects get a 0–100 score from public
  signals such as age, activity and release history, with the breakdown shown next to Install. See
  [docs/trust-score.md](docs/trust-score.md).
- **Installs and updates in one place.** One list for everything installed, from every source, with updates
  you can apply one at a time or all at once. Downloaded AppImages can be added and kept up to date too.
- **A signed catalog.** The catalog is rebuilt every 6 hours, and every catalog is signed and verified before
  the client uses it.

## Screenshots

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/app.png" alt="An app page with Install, stats, screenshots and the recommended source"></td>
    <td width="50%"><img src="docs/screenshots/search.png" alt="Search results across sources, with installs a year and release dates"></td>
  </tr>
  <tr>
    <td align="center"><b>App page:</b> the recommended source, install notes and details</td>
    <td align="center"><b>Search:</b> every source in one list, filterable by source</td>
  </tr>
  <tr>
    <td width="50%"><img src="docs/screenshots/installed.png" alt="Installed apps from Flathub and Homebrew, with an available update"></td>
    <td width="50%"><img src="docs/screenshots/home-light.png" alt="The home page in the light theme"></td>
  </tr>
  <tr>
    <td align="center"><b>Installed:</b> everything on the system, with updates</td>
    <td align="center"><b>Light theme:</b> follows the system setting</td>
  </tr>
</table>

## Getting started

uas is written in Go. You need Go 1.27 or newer, and the desktop app needs a Wayland session.

### Desktop app

```sh
go run ./cmd/uas-gui
```

To install it with its icon and launcher entry:

```sh
go install ./cmd/uas-gui        # installs to ~/go/bin; make sure that's on your PATH
cp packaging/linux/io.github.tyler_builds.uas.desktop ~/.local/share/applications/
cp -r packaging/linux/icons ~/.local/share/
```

### Command line

`uas` downloads the published catalog on first use (or with `uas refresh`) into `~/.cache/uas/`.

```sh
go run ./cmd/uas search firefox
go run ./cmd/uas info org.mozilla.firefox
go run ./cmd/uas install org.mozilla.firefox
```

| Command | What it does |
| --- | --- |
| `refresh` | Download the latest catalog |
| `search <words>` | Find apps |
| `info <app-id>` | Show one app and where it's available |
| `install <app-id>` | Install an app from its recommended source |
| `add <file.AppImage>` | Add a downloaded AppImage and keep it updated |
| `remove <app-id>` | Remove an installed app |
| `update <app-id>` / `updates` | Update an app, or list the apps that have updates |
| `list` | List installed apps |
| `system` | Show what uas detected about this system |
| `settings [key value]` | Show or change settings |

Run `uas <command> -h` for a command's flags.

## How it works

```
 Flathub · Fedora · Homebrew · COPR · GitHub
                    │
            uas-indexer (every 6h)        merges, ranks and scores apps,
                    │                     signs the catalog
                    ▼
     Cloudflare Worker + R2 (catalog.db)
                    │
          ┌─────────┴─────────┐
       uas-gui              uas (CLI)      verify the signature, then search
          └─────────┬─────────┘            and install through each source's
                    ▼                      own tool (flatpak, rpm-ostree, brew…)
                your system
```

The full design is in [docs/architecture.md](docs/architecture.md), and the desktop app's design system is in
[docs/design.md](docs/design.md).

## Development

```sh
go test ./...
```

To try the client against a catalog you built yourself:

```sh
go run ./cmd/uas-indexer -out out/catalog.db
UAS_CATALOG=out/catalog.db go run ./cmd/uas search firefox
```

`TestShots` renders every page of the desktop app in both themes to PNGs for design review (the README's
screenshots come from it):

```sh
UAS_SHOTS=/tmp/uas-shots UAS_CATALOG=out/catalog.db go test ./internal/gui -run TestShots
```

### Publishing

The `Index` workflow builds and publishes the catalog every 6 hours, and can be run by hand from the Actions
tab. Publishing needs the `UAS_SIGNING_KEY` and `UAS_UPLOAD_TOKEN` secrets. The catalog Worker lives in
`deploy/cloudflare`; deploy it with `npx wrangler deploy` from that directory.

## Acknowledgements

The UI is built with [jamb](https://github.com/tyler-builds/jamb). The interface font is
[Inter](https://rsms.me/inter/) by Rasmus Andersson (SIL Open Font License). Source logos are from
[Simple Icons](https://simpleicons.org/).
