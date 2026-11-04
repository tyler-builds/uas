# uas design system

The desktop app's look lives in [`internal/gui/design.go`](../internal/gui/design.go). Pages use its tokens and
components instead of raw sizes and colors.

## Layout

- **Sidebar:** 40px items; the selected one is marked by its fill alone, with no bar. The uas mark, then Home, Search and Sources; a "Categories" group (Development, Games,
  Graphics, Internet, Multimedia, Productivity, System, Utilities); a "Library" group with Installed and
  Updates and their counts; the catalog's freshness at the bottom.
- **Top bar:** a back button when there is somewhere to go back to, a pill-shaped search field (typing
  opens the search page), and a settings button.
- **Navigation:** a stack of views (`app.go`): opening an app or a list pushes, Back pops, and the sidebar
  starts over.

## Pages

- **Home:** a 220px featured banner with geometric art drawn on a canvas, "Popular Apps" and "New & Updated" rows
  of cards (new means recently released among apps with at least 50,000 installs a year), and "Browse by
  Source" tiles with app counts.
- **Search:** source filter chips (All, Flathub, Fedora, Homebrew, COPR, GitHub), a sort menu, and result
  rows: icon, name, summary and sources; installs a year and the latest release; Install (or Installed) and
  Details. Install from a row uses the source the app page would recommend, and still asks first for
  unreviewed and layered sources.
- **Category and source pages:** a header with an icon tile, a line about the category or source, a sort menu,
  and the same rows.
- **Sources:** the tiles plus how uas treats each source and whether this system can use it.
- **App page:** the icon, name, summary and tags; a large Install button with the source picker under it; a
  stats strip (installs a year, latest release, version, verified publisher or trust score); screenshots,
  About and the trust breakdown; a side panel with Source, Category, License and Developer, the install
  notes and "Why <source>?", and Report an Issue and Source Code links.
- **Installed and Updates:** the same rows for installed apps, with Update where there is one, Update all,
  and Remove.

## Brand

- **Palette:** charcoal surfaces with a teal accent (`#2dd4bf` dark, `#0d9488` light). Primary buttons are
  teal with dark text in the dark theme. `gui.Themes()` builds both themes over jamb's, so every widget picks
  them up, and the window follows the system's light or dark mode.
- **Sources:** each has a color and its own logo (`sourceIcons`), shown with its name wherever an app's
  sources are listed: Flathub blue, Fedora violet, Homebrew amber, GitHub gray. The logos are Simple Icons
  SVGs made into ordinary jamb icons with `icons.MustNew` (`internal/gui/logos.go`), so they tint and size
  like any icon. COPR has no logo and shows a rose hammer. A Flathub-verified publisher gets a green shield. Hovering any of them explains it.

- **Surfaces:** cards, rows and tiles are one flat color with no border, lighter than the page (the light
  theme's page is a soft gray so white cards stand out). Hovering lightens a clickable one. They are jamb
  `ui.Card`s, styled once in the theme (`Theme.Card`).

## Tokens

| Token | Values |
| --- | --- |
| Spacing (`space1`..`space8`) | 4, 8, 12, 16, 20, 24, 32 |
| Radius | 8 (buttons, inputs, chips), 12 (tiles), 14 (cards and rows), 20 (featured banner) |
| Type | display 38 bold, title 26 bold, heading 20 semibold, subhead 16 semibold, muted 14, caption 12 |

## Components

- `ui.Card()` for surfaces; with `OnClick` the whole card is a button.
- `sectionHeader`, `linkButton`: section titles with a "See all →" style link (a ghost button with
  `IconEnd`).
- `chip`, `filterChip`: tags on an app page, and the search filters.
- `sourceTag`, `sourceTags`, `verifiedTag`: a source's icon and name; `sourcePill` and `statusPill` for
  Settings and Sources.
- `statLine`, `meter`, `brandMark`, `iconTile`.
- In `rows.go`: `appCard` (home rows), `resultRow` (lists), `orderPicker`, `pageBody`, `scroll`.

## Reviewing

`TestShots` in `internal/gui/shots_test.go` renders every page in both themes to PNGs:

```sh
UAS_SHOTS=/tmp/uas-shots UAS_CATALOG=out/catalog.db go test ./internal/gui -run TestShots
```

It uses jamb's embedded fonts (the running app gets the desktop's fonts from fontconfig), so text looks
slightly different from the running app.
