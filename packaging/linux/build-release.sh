#!/usr/bin/env bash
# Builds a uas release for one architecture: the uas program, which is the CLI and the desktop app, carries
# its launcher and icon (packaging/linux) and the licenses and notices of everything in it ("uas licenses"),
# and installs itself when run.
#
#   packaging/linux/build-release.sh <version> <x86_64|aarch64> <out-dir>
#
# Writes, in <out-dir>:
#   uas-<version>-linux-<arch>         the program: what people download, and what uas updates itself to
#   uas-<version>-linux-<arch>.tar.gz  the program as uas and as uas-gui, for uas 0.5.0 and older to update
#                                      from (they read only archives with those two programs)
# Run it from the repo root.
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
# The licenses go in internal/legal/bundle, which a build with the uas_release tag embeds.
lic=internal/legal/bundle
trap 'rm -rf "$stage" "$lic"' EXIT
rm -rf "$lic"

# uas's license, and the licenses and notices of everything the program contains: each Go module it links
# (with jamb's NOTICE and the licenses of what it bundles), the Go runtime, the Inter font and the source logos.
mkdir -p "$lic/go" "$lic/Inter" "$lic/logos"
cp LICENSE "$lic/"
goroot=$(go env GOROOT)
# Go's LICENSE is in GOROOT, or beside it in some packages of Go (Homebrew's).
golicense=$goroot/LICENSE
[ -e "$golicense" ] || golicense=$goroot/../LICENSE
cp "$golicense" "$goroot/PATENTS" "$lic/go/"
cp internal/gui/fonts/OFL.txt "$lic/Inter/"
cp internal/gui/logos/README.md "$lic/logos/"
GOOS=linux GOARCH=$goarch go list -deps -tags no_gpl,uas_release -f '{{with .Module}}{{.Path}} {{.Dir}}{{end}}' ./cmd/uas |
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

mkdir -p "$out"
ldflags="-s -w -X github.com/tyler-builds/uas/internal/version.Version=$version"
CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go build -tags no_gpl,uas_release -trimpath -ldflags "$ldflags" \
  -o "$out/$name" ./cmd/uas
chmod 755 "$out/$name"

# The archive for older copies of uas: the program twice, as regular files, since they replace uas and uas-gui
# each from its own file. Once running, the new program makes uas-gui a link to uas.
dir="$stage/$name"
mkdir -p "$dir"
cp "$out/$name" "$dir/uas"
cp "$out/$name" "$dir/uas-gui"
cp LICENSE "$dir/"
# Reproducible: sorted names, fixed owner and time.
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@${SOURCE_DATE_EPOCH:-0}" \
  -C "$stage" -czf "$out/$name.tar.gz" "$name"
touch -d "@${SOURCE_DATE_EPOCH:-0}" "$out/$name"
echo "$out/$name"
echo "$out/$name.tar.gz"
