# uas

An app store for Bazzite that finds apps across Flathub, Fedora, COPR, Homebrew and GitHub releases.
See [docs/architecture.md](docs/architecture.md).

## Development

Build a catalog from live sources, then search it:

```sh
go run ./cmd/uas-indexer -out ~/.cache/uas/catalog.db
go run ./cmd/uas search firefox
go run ./cmd/uas info org.mozilla.firefox
```

`uas` reads `~/.cache/uas/catalog.db` by default; set `UAS_CATALOG` or pass `-catalog` to use another file.

```sh
go test ./...
```
