# uas design system

The desktop app's look lives in [`internal/gui/design.go`](../internal/gui/design.go). Pages use its tokens and
components instead of raw sizes and colors.

## Brand

- **Palette:** cool, blue-tinted neutrals with a violet accent (`#7c6cff` dark, `#6650f5` light). The brand
  gradient runs from the accent to cyan and is used sparingly: the logo mark and the tinted hero panels.
- **Themes:** `gui.Themes()` returns a light and a dark theme built over jamb's, so every built-in widget
  (buttons, selects, the sidebar, dialogs, toasts) picks up the palette. The window follows the system's
  light or dark mode.
- **Source colors:** each source has a color, used for its pill everywhere: Flathub blue, Fedora violet,
  Homebrew amber, COPR teal, GitHub gray. Status colors: success green ("Verified", "Ready", good trust),
  warning amber, danger red.

## Tokens

| Token | Values |
| --- | --- |
| Spacing (`space1`..`space8`) | 4, 8, 12, 16, 20, 24, 32 |
| Radius | 8 (buttons, inputs), 12 (rows, icon tiles), 16 (cards), 22 (hero panels) |
| Type | display 30 bold, title 22 semibold, heading 16 semibold, body 14, muted 13, caption 12 |

## Components

- `card`: the standard raised surface (16 radius, hairline border, 20 padding).
- `hero`: a card tinted with the soft brand gradient, for the top of Discover and app pages.
- `sectionHeader`: title, optional caption, controls on the right.
- `pill` / `sourcePill` / `verifiedPill` / `statusPill`: small tinted labels with a dot and a tooltip.
- `statChip`: icon, value and label, such as "2.9M installs a year".
- `iconTile`: frames any app icon on a surface tile so odd shapes line up.
- `meter`: a thin bar, used in the trust breakdown.
- `brandMark`: the logo, the store icon on the brand gradient.

## Reviewing

`TestShots` in `internal/gui/shots_test.go` renders every page in both themes to PNGs:

```sh
UAS_SHOTS=/tmp/uas-shots UAS_CATALOG=out/catalog.db go test ./internal/gui -run TestShots
```

It uses jamb's embedded fonts (the app registers the desktop's fonts at start), so text looks slightly
different from the running app.
