# Trust score

GitHub and Codeberg releases, COPR projects and Pling themes are not reviewed by any distribution, so uas scores each listing from
public signals and shows the breakdown next to the install button, not only the number. Code:
`internal/indexer/trust`.

## GitHub signals (0 to 100)

| Signal | Max | Points |
| --- | --- | --- |
| Project age | 15 | under 3 months 0, under a year 5, 1 to 2 years 10, older 15 |
| Stars | 25 | log scale: 10 stars ≈ 6, 100 ≈ 12, 1,000 ≈ 19, 10,000+ 25 |
| Downloads of the latest release | 10 | log scale: 100,000+ downloads = 10 |
| Release activity | 15 | releases in the past 12 months, prereleases included (nightlies show the project is worked on): 0 → 0, 1 → 6, 2 to 3 → 10, 4+ → 15. Only the 8 newest releases are read, so a repo with 8 in the past year shows "8+" |
| Latest release | 15 | within 6 months 15, a year 10, 18 months 5, older 0 |
| License | 10 | any recognized SPDX license 10 |
| Checksum | 10 | the AppImage has a SHA-256 published by GitHub 10 |

## Codeberg signals

The same as GitHub's, with two differences:

- **Checksum:** Codeberg publishes no checksums, so the indexer downloads each release file once and records
  its SHA-256 in the signed catalog: 5 of 10. The client checks every download against it, so a file swapped
  after indexing is refused, but nothing ties the first download to the developer's own build.
- **License:** Codeberg's API doesn't report a repo's license, and Codeberg hosts only free and open-source
  projects, so Codeberg repos get the 10 points with that as the reason.

## COPR signals (0 to 100)

COPR builds and signs every package, but nobody reviews what goes in, so the COPR score is about upkeep. It
uses the project's last 30 builds.

| Signal | Max | Points |
| --- | --- | --- |
| Latest successful build | 25 | within 3 months 25, 6 months 18, a year 10, older 0 |
| Build health | 20 | share of finished builds that succeeded: 90%+ 20, 75%+ 14, 50%+ 8 |
| Activity | 15 | builds in the past year: 10+ 15, 4+ 10, 1+ 5 |
| Maintainers | 15 | group project or 2+ people submitting builds 15, one person 5 |
| Project details | 10 | homepage or contact listed |
| Description | 10 | at least a sentence |
| Signing | 5 | always: COPR signs with the project's key |

COPR listings below 30 are left out. COPR warnings: "No successful build in the past year", "Most recent
builds failed".

## Pling signals (0 to 100)

Icon and cursor themes from Pling (gnome-look.org) are scored from what Pling publishes about the item.
Pling verifies no publisher and signs nothing, and anyone can upload, so the score is built to stay below
what a well-kept GitHub project gets: 15 points are never given, and the checksum earns half.

| Signal | Max | Points |
| --- | --- | --- |
| Age | 15 | on Pling under 3 months 0, under a year 5, 1 to 2 years 10, longer 15 |
| Downloads | 25 | log scale: 1,000 ≈ 12, 10,000 ≈ 17, 100,000 ≈ 21, 1,000,000+ 25 (Pling's counts are rough) |
| Rating | 20 | 80+ 20, 70+ 14, 60+ 8, lower 0. Pling gives an item nobody rated exactly 50, which counts as not rated: 0 |
| Last change | 15 | within a year 15, 2 years 10, 3 years 5, older 0 |
| Checksum | 10 | always 5: uas records the file's SHA-256 when it builds the catalog (Pling publishes only an MD5) |
| Verified publisher | 15 | always 0: Pling doesn't verify who uploads |

Pling warnings: "No ratings yet", "Not changed in over 3 years: it may not cover newer apps".

What gets into the catalog from Pling: items with at least 10,000 downloads (icon themes) or 3,000 (cursor
themes), not excluded by Pling's moderators, with at least one file uas would install. A file is refused if it
holds a program, script, library, launcher, plugin, QML or JavaScript anywhere, isn't an archive uas opens,
or holds no icon or cursor theme it can tell apart (see docs/architecture.md). There is no minimum score.

Fonts come from Google Fonts' repository, whose families Google's fonts team chooses and checks, and from
the Nerd Fonts project's releases, checked against GitHub's checksum. They have no trust score.

## GitHub warnings

Shown whatever the score:

- New project: created less than 30 days ago.
- No release in over 18 months.
- No published checksum: the download cannot be verified.

## What gets into the catalog

- Archived repos and forks are left out.
- Only the latest stable release counts; pre-releases and drafts are ignored.
- A listing needs at least one Linux file for the architecture (AppImage, Flatpak bundle or RPM) under the
  repo's own `releases/download/` URL. Codeberg listings leave out RPMs, which aren't installable from releases anyway.
- Listings scoring below 35 are left out. In the first full run (November 2026) 2,010 repos were checked,
  1,389 kept and 4 dropped for a low score; the median score was 78.

## Apps installed from a link

An app the user installs from a link to its GitHub repo (Install from URL) skips the rules above: the
client reads the repo and scores it on the spot with the same signals, and keeps archived repos, forks and
scores under 35, adding "Archived" and "A fork" to the warnings. Its page and the install confirmation say
it isn't in the catalog. What the client enforces below applies to it all the same.

## What the client enforces

- uas installs a GitHub AppImage only if it has a published SHA-256, and refuses the file if the download
  doesn't match it. Codeberg files are checked against the SHA-256 the indexer recorded.
- The launcher's `Exec` line is rebuilt to run only the AppImage. Other commands in the app's own desktop
  entry (such as extra actions) are not copied.
- A Pling theme is installed only if its download matches the size and SHA-256 the indexer recorded, from a
  fresh link on Pling's own hosts. Only images and theme files are written: nothing in the archive is run,
  and no link from it is written as a link.
- A Google font file must match its pinned SHA-256 and come from the google/fonts repository, at the commit
  the catalog names; a Nerd Font archive must match GitHub's SHA-256 and may hold only TTF and OTF files.

## Known gaps

- Stars and downloads can be gamed. They are weighted on a log scale and capped so neither dominates.
- "Owner changed recently" (a transferred or sold repo) isn't detected yet; it needs repo event history.
- Contributor count isn't used: GitHub's GraphQL API doesn't expose it cheaply.
