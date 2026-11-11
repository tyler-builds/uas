#!/usr/bin/env bash
# Builds a uas release archive: uas, uas-gui, the launcher and icon, and install.sh.
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
cp packaging/linux/io.github.tyler_builds.uas.desktop "$dir/share/applications/"
cp -r packaging/linux/icons "$dir/share/"
cp packaging/linux/install.sh "$dir/"
chmod 755 "$dir/install.sh"

mkdir -p "$out"
# Reproducible: sorted names, fixed owner and time.
tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@${SOURCE_DATE_EPOCH:-0}" \
  -C "$stage" -czf "$out/$name.tar.gz" "$name"
echo "$out/$name.tar.gz"
