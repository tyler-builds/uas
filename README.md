# uas

An app store for Bazzite that finds apps across Flathub, Fedora, COPR, Homebrew and GitHub releases.
See [docs/architecture.md](docs/architecture.md) and [docs/trust-score.md](docs/trust-score.md).

## Desktop app

```sh
go run ./cmd/uas-gui
```

It needs a Wayland session (jamb's Linux support is Wayland only).

## Command line

`uas` downloads the published catalog on first use (or with `uas refresh`) into `~/.cache/uas/`:

```sh
go run ./cmd/uas search firefox
go run ./cmd/uas info org.mozilla.firefox
go run ./cmd/uas install org.mozilla.firefox
```

To use a locally built catalog instead, build one and point `UAS_CATALOG` (or `-catalog`) at it:

```sh
go run ./cmd/uas-indexer -out out/catalog.db
UAS_CATALOG=out/catalog.db go run ./cmd/uas search firefox
```

## Publishing

The `Index` workflow builds and publishes the catalog every 6 hours, and can be run by hand from the Actions
tab. Publishing needs the `UAS_SIGNING_KEY` and `UAS_UPLOAD_TOKEN` secrets. The catalog Worker lives in
`deploy/cloudflare`; deploy it with `npx wrangler deploy` from that directory.

```sh
go test ./...
```
