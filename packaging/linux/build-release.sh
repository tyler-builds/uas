#!/usr/bin/env bash
# Builds a uas release archive: uas, uas-gui, the launcher and icon, install.sh, and LICENSE with the
# third-party licenses and notices in licenses/
#
#   packaging/linux/build-release.sh <version> <x86_64|aarch64> <out-dir>
#
# Writes <out-dir>/uas-<version>-linux-<arch>.tar.gz. Run it from the repo root.
set -euo pipefail

version=${1:?version, such as 0.2.0}
arch=${2:?architecture: x86_64 or aarch64}
out=${3:?output directory}
case "$arch" in
  x86_64) goarch=amd64 ;;
  aarch64) goarch=arm64 ;;
  *) echo "unknown architecture $arch" >&2; exit 2 ;;
esac
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  echo "version must be major.minor.patch, not $version" >&2
  exit 2
fi

name="uas-$version-linux-$arch"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
dir="$stage/$name"
mkdir -p "$dir/share/applications"

ldflags="-s -w -X github.com/tyler-builds/uas/internal/version.Version=$version"
for cmd in uas uas-gui; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go build -tags no_gpl -trimpath -ldflags "$ldflags" -o "$dir/$cmd" "./cmd/$cmd"
done
# uas's license, and the licenses and notices of everything the binaries contain: each Go module they link
# (with jamb's NOTICE and the licenses of what it bundles), the Go runtime, the Inter font and the source logos.
cp LICENSE "$dir/"
lic="$dir/licenses"
mkdir -p "$lic/go" "$lic/Inter" "$lic/logos"
goroot=$(go env GOROOT)
# Go's LICENSE is in GOROOT, or beside it in some packages of Go (Homebrew's).
golicense=$goroot/LICENSE
[ -e "$golicense" ] || golicense=$goroot/../LICENSE
cp "$golicense" "$goroot/PATENTS" "$lic/go/"
cp internal/gui/fonts/OFL.txt "$lic/Inter/"
cp internal/gui/logos/README.md "$lic/logos/"
GOOS=linux GOARCH=$goarch go list -deps -tags no_gpl -f '{{with .Module}}{{.Path}} {{.Dir}}{{end}}' ./cmd/uas ./cmd/uas-gui |
  sort -u | while read -r mod moddir; do
    [ "$mod" = github.com/tyler-builds/uas ] && continue
    mkdir -p "$lic/$mod"
    found=
    for f in "$moddir"/*; do
      case "$(basename "$f" | tr '[:upper:]' '[:lower:]')" in
        licen[cs]e* | copying* | notice* | patents* | unlicense*) cp "$f" "$lic/$mod/" && found=1 ;;
      esac
    done
    [ -n "$found" ] || { echo "no license file in $mod ($moddir)" >&2; exit 1; }
    for f in "$moddir"/third_party/*/LICENSE*; do
      [ -e "$f" ] || continue
      mkdir -p "$lic/$mod/$(dirname "${f#"$moddir"/}")"
      cp "$f" "$lic/$mod/${f#"$moddir"/}"
    done
  done
cp packaging/linux/io.github.tyler_builds.uas.desktop "$dir/share/applications/"
cp -r packaging/linux/icons "$dir/share/"
cp packaging/linux/install.sh "$dir/"
chmod 755 "$dir/install.sh"

mkdir -p "$out"
# Reproducible: sorted names, fixed owner and time.
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@${SOURCE_DATE_EPOCH:-0}" \
  -C "$stage" -czf "$out/$name.tar.gz" "$name"
echo "$out/$name.tar.gz"
