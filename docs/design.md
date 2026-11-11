# uas design system

The desktop app's look lives in [`internal/gui/design.go`](../internal/gui/design.go). Pages use its tokens and
components instead of raw sizes and colors. The feel is that of quiet, dense tools such as Linear and Vercel:
near-black neutral surfaces, hairline borders, a small type scale, and one accent used sparingly.

## Layout

- **Sidebar:** on the darkest surface. The uas mark, then Home, Search and Sources; a "Categories" group
  (Development, Games, Graphics, Internet, Multimedia, Productivity, System, Utilities); a "Library" group with
  Installed and Updates and their counts; the catalog's freshness at the bottom. Items are 30px; the selected
  one has a neutral fill and no bar.
- **Content panel:** the pages sit on an inset panel with a 12px radius and a hairline border, a step lighter
  than the sidebar.
- **Window:** no system title bar (`app.ChromeCustom`) and rounded corners; on GNOME jamb draws them with a
  shadow and an outline, square while maximized or snapped.
- **Title bar:** 48px on the sidebar's surface, above the content panel, as a jamb `ui.TitleBar`. On the left,
  Back (an arrow) when there is somewhere to go back to, and where you are: the page's icon and name, or on
  an app page the page it was opened from and the app (`Home / darktable`). On the right, a compact search
  field (typing opens the search page; 340px, down to 180px in a narrow window), Settings, and the window's
  buttons as the desktop sets them. Its empty parts and the sidebar's header move the window, and a
  double-click maximizes it.
- **Navigation:** a stack of views (`app.go`): opening an app or a list pushes, Back pops, and the sidebar
  starts over.

## Pages

Every page starts with `pageHeader`: a title, a line about the page, and controls on the right.

- **Home:** "Discover"; a Spotlight card featuring one popular app a day, with its first screenshot, Install
  and View details; "Popular Apps" and "New & Updated" rows of compact cards (new means recently released
  among apps with at least 50,000 installs a year); the categories as borderless tiles; and the sources as
  one strip split by hairlines. Sections are 36px apart, a header 12px from its content.
- **Search:** the header, then the source filters (a segmented control) and the sort menu, then the results
  in one bordered list split by hairlines. A row is the icon; the name with its sources, Verified and
  developer as quiet words; the summary; installs a year and the latest release; and an outlined Install icon
  button. The row opens the app's page. Install from a row uses the source the app page would recommend, and
  still asks first for unreviewed and layered sources.
- **Category and source pages:** the header with a sort menu, and the same list.
- **Sources:** the source strip plus how uas treats each source and whether this system can use it.
- **App page:** the icon on a bordered tile, the name, summary and tags; Install (teal) with the source
  picker under it; a stats strip between hairlines (installs a year, latest release, version, and the
  verified publisher, trust score or category); two screenshots side by side; About and the trust breakdown;
  a side panel with Source, License and Developer as label and value rows, then the install notes, "Why
  <source>?" and the Report an issue and Source code links, all in one icon-and-text row style; and "More in
  <category>" cards.
- **Installed and Updates:** the same list for installed apps, with Update where there is one (outlined, with
  a teal icon), Update all, and Remove.

## Brand

- **Logo:** a white isometric package on a teal tile, in [`internal/gui/brand/uas.svg`](../internal/gui/brand/uas.svg).
  The app draws it with `ui.SVG` (`brandMark`), cropped to the tile. The same file, with its margin, is the
  desktop icon in [`packaging/linux`](../packaging/linux), next to the `.desktop` file.
- **Font:** Inter (SIL Open Font License, [`internal/gui/fonts`](../internal/gui/fonts)), embedded and
  registered as the UI font in `cmd/uas/gui.go` (`gui.Fonts()`), so the app looks the same on every desktop.
- **Palette:** neutral near-black surfaces in the dark theme (page `#0e0e10`, sidebar `#08080a`, cards
  `#19191d`, borders `#222227`) and white with light grays in the light theme. Text comes in three tiers:
  primary, muted (summaries) and subtle (metadata). Teal (`#14897e` dark, `#0f766e` light) is kept to primary
  actions, links and focus; the sidebar selection and counts are neutral. `gui.Themes()` builds both themes
  over jamb's, so every widget picks them up, and the window follows the system's light or dark mode.
- **Sources:** each has a color (`sourceTints`) and a mark (`sourceMark` in `internal/gui/logos.go`). A logo
  is never recolored, tinted, cropped or redrawn: a source's mark is its owner's official logo file,
  unchanged and in its own colors (with the owner's dark-background variant in the dark theme), drawn as an
  SVG image that no tint or high-contrast mode changes. That's Debian, GitHub and Codeberg. Where the
  owner's trademark policy doesn't clearly allow its unaltered logo here, or there is no logo, the mark is a
  neutral Lucide icon in the source's color: Flathub a blue store, Fedora a violet package, Homebrew an
  amber terminal, COPR a rose hammer, and so on. `internal/gui/logos/README.md` has the policy behind each.
  Marks show where a source is the subject (the source strip, the source picker, the app page's Source
  row); in lists, sources are quiet words. A Flathub-verified publisher gets a green shield. Hovering any of
  them explains it.
- **Missing icons:** an app without an icon gets a monogram, its initial on a tile tinted by its name.
- **Surfaces:** cards and lists are a fill a step lighter than the page with a hairline border; hovering a
  clickable one lightens it. Cards are jamb `ui.Card`s, styled once in the theme (`Theme.Card`).

## Tokens

| Token | Values |
| --- | --- |
| Spacing (`space1`..`space8`) | 4, 8, 12, 16, 20, 24, 32 |
| Radius | 6 (buttons, inputs, chips), 8 (tiles), 10 (cards and lists), 12 (Spotlight and the content panel) |
| Type | title 24 semibold, heading 16 semibold, subhead 14 medium, body 13, caption 12 |

## Components

- `pageHeader`, `section`, `sectionHeader`, `linkButton`: page and section headers, with a "See all →" style
  link (a ghost button with `IconEnd`).
- `ui.Card()` for surfaces; with `OnClick` the whole card is a button.
- `listBox`, `listRow`: the bordered list and its rows.
- `metaLine`, `sourceWords`: quiet words split by middle dots, such as an app's sources and developer.
- `chip`, `filterChip`, `segmented`: tags on an app page, and the filters.
- `sourcePill` and `statusPill` for Settings and Sources; `infoRow` and `actionRow` for the app page's side
  panel; `monogram`, `brandMark`.
- In `rows.go`: `appCard` (home rows), `resultRow` (lists), `orderPicker`, `pageBody`, `scroll`.

## Reviewing

`TestShots` in `internal/gui/shots_test.go` renders every page in both themes to PNGs:

```sh
UAS_SHOTS=/tmp/uas-shots UAS_CATALOG=out/catalog.db go test ./internal/gui -run TestShots
```

It points fontconfig at the bundled Inter, so the text matches the running app, except that semibold text
renders bold (the test's font lookup has no semibold face).

## Installing the desktop entry

`go build -o /tmp/uas ./cmd/uas && /tmp/uas self-install` installs a build from source as a release
installs itself: `~/.local/bin/uas`, `uas-gui` linked to it, the launcher from `packaging/linux` (with the
full path) and its icon. The app ID, `io.github.tyler_builds.uas`, is the
Wayland app ID, which the desktop matches to the `.desktop` file for the dock's name and icon.
