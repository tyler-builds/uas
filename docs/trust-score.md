# Trust score

GitHub releases are not reviewed by any distribution, so uas scores each GitHub listing from public signals
and shows the breakdown next to the install button, not only the number. Code: `internal/indexer/trust`.

## Signals (0 to 100)

| Signal | Max | Points |
| --- | --- | --- |
| Project age | 15 | under 3 months 0, under a year 5, 1 to 2 years 10, older 15 |
| Stars | 25 | log scale: 10 stars ≈ 6, 100 ≈ 12, 1,000 ≈ 19, 10,000+ 25 |
| Downloads of the latest release | 10 | log scale: 100,000+ downloads = 10 |
| Release activity | 15 | stable releases in the past 12 months: 0 → 0, 1 → 6, 2 to 3 → 10, 4+ → 15 |
| Latest release | 15 | within 6 months 15, a year 10, 18 months 5, older 0 |
| License | 10 | any recognized SPDX license 10 |
| Checksum | 10 | the AppImage has a SHA-256 published by GitHub 10 |

## Warnings

Shown whatever the score:

- New project: created less than 30 days ago.
- No release in over 18 months.
- No published checksum: the download cannot be verified.

## What gets into the catalog

- Archived repos and forks are left out.
- Only the latest stable release counts; pre-releases and drafts are ignored.
- A listing needs at least one Linux file for the architecture (AppImage, Flatpak bundle or RPM) under the
  repo's own `releases/download/` URL.
- Listings scoring below 35 are left out. In the first full run (November 2026) 2,010 repos were checked,
  1,389 kept and 4 dropped for a low score; the median score was 78.

## What the client enforces

- uas installs a GitHub AppImage only if it has a published SHA-256, and refuses the file if the download
  doesn't match it.
- The launcher's `Exec` line is rebuilt to run only the AppImage. Other commands in the app's own desktop
  entry (such as extra actions) are not copied.

## Known gaps

- Stars and downloads can be gamed. They are weighted on a log scale and capped so neither dominates.
- "Owner changed recently" (a transferred or sold repo) isn't detected yet; it needs repo event history.
- Contributor count isn't used: GitHub's GraphQL API doesn't expose it cheaply.
