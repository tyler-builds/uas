# Trust score

GitHub, GitLab and Codeberg releases and COPR projects are not reviewed by any distribution, so uas scores each listing from
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

## GitLab and Codeberg signals

The same as GitHub's, with two differences:

- **Checksum:** neither forge publishes checksums, so the indexer downloads each release file once and records
  its SHA-256 in the signed catalog: 5 of 10. The client checks every download against it, so a file swapped
  after indexing is refused, but nothing ties the first download to the developer's own build.
- **License:** Codeberg's API doesn't report a repo's license, and Codeberg hosts only free and open-source
  projects, so Codeberg repos get the 10 points with that as the reason. GitLab's license comes from the
  project's own page.
- GitLab has no prerelease flag; tags with alpha, beta, rc, preview, nightly, snapshot or dev count as
  prereleases. GitLab doesn't count downloads, so its listings get 0 for downloads.

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

## GitHub warnings

Shown whatever the score:

- New project: created less than 30 days ago.
- No release in over 18 months.
- No published checksum: the download cannot be verified.

## What gets into the catalog

- Archived repos and forks are left out.
- Only the latest stable release counts; pre-releases and drafts are ignored.
- A listing needs at least one Linux file for the architecture (AppImage, Flatpak bundle or RPM) under the
  repo's own `releases/download/` URL (GitLab: the project's `/-/releases/<tag>/downloads/` link). GitLab and
  Codeberg listings leave out RPMs, which aren't installable from releases anyway.
- Listings scoring below 35 are left out. In the first full run (November 2026) 2,010 repos were checked,
  1,389 kept and 4 dropped for a low score; the median score was 78.

## What the client enforces

- uas installs a GitHub AppImage only if it has a published SHA-256, and refuses the file if the download
  doesn't match it. GitLab and Codeberg files are checked against the SHA-256 the indexer recorded.
- The launcher's `Exec` line is rebuilt to run only the AppImage. Other commands in the app's own desktop
  entry (such as extra actions) are not copied.

## Known gaps

- Stars and downloads can be gamed. They are weighted on a log scale and capped so neither dominates.
- "Owner changed recently" (a transferred or sold repo) isn't detected yet; it needs repo event history.
- Contributor count isn't used: GitHub's GraphQL API doesn't expose it cheaply.
