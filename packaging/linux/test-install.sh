#!/usr/bin/env bash
# Checks that a release program (from build-release.sh) installs itself into a clean home, that the launcher
# and the CLI work afterwards, that an install in the old layout (by install.sh) becomes the new one, and
# that self-uninstall removes it all.
#
#   packaging/linux/test-install.sh dist/uas-<version>-linux-x86_64
#
# It never touches your own home: HOME is a temporary folder.
set -euo pipefail

prog=$(realpath "${1:?the release program to test}")
version=$(basename "$prog" | sed -E 's/^uas-([0-9]+\.[0-9]+\.[0-9]+)-linux-.*/\1/')
export HOME=$(mktemp -d)
trap 'rm -rf "$HOME"' EXIT
unset XDG_DATA_HOME XDG_CONFIG_HOME
bin=$HOME/.local/bin
desktop=$HOME/.local/share/applications/io.github.tyler_builds.uas.desktop
icon=$HOME/.local/share/icons/hicolor/scalable/apps/io.github.tyler_builds.uas.svg
fail() { echo "FAIL: $*" >&2; exit 1; }

check_installed() {
  [ "$("$bin/uas" version)" = "uas $version" ] || fail "uas version is $("$bin/uas" version)"
  [ "$(readlink "$bin/uas-gui")" = uas ] || fail "uas-gui is not a link to uas"
  grep -qx "Exec=$bin/uas-gui" "$desktop" || fail "the launcher doesn't run $bin/uas-gui"
  grep -qx "Icon=io.github.tyler_builds.uas" "$desktop" || fail "the launcher has no icon"
  [ -s "$icon" ] || fail "no icon"
}

echo "== a fresh install, from a download"
dl=$HOME/Downloads/$(basename "$prog")
mkdir -p "$(dirname "$dl")"
cp "$prog" "$dl"
chmod 755 "$dl"
"$dl" self-install
check_installed
"$bin/uas" licenses | grep -c "Apache License" >/dev/null || fail "no licenses"
"$bin/uas" help | grep -c self-uninstall >/dev/null || fail "no help"

echo "== installing again keeps one install"
"$dl" self-install >/dev/null
check_installed

echo "== an install by install.sh (uas 0.5.0 and older), with uas and uas-gui as two copies of this program"
"$bin/uas" self-uninstall >/dev/null
mkdir -p "$bin" "$(dirname "$desktop")"
cp "$prog" "$bin/uas"
cp "$prog" "$bin/uas-gui"
printf '[Desktop Entry]\nType=Application\nName=uas\nExec=%s\nIcon=io.github.tyler_builds.uas\n' "$bin/uas-gui" >"$desktop"
[ -L "$bin/uas-gui" ] && fail "the old layout should have uas-gui as a file"
"$bin/uas" version >/dev/null # the next run of the new program refreshes the install
check_installed
refreshed=$(cat "$desktop")
"$bin/uas" self-install >/dev/null
[ "$refreshed" = "$(cat "$desktop")" ] || fail "the launcher wasn't refreshed to the new one"

echo "== self-uninstall"
"$bin/uas" self-uninstall
for f in "$bin/uas" "$bin/uas-gui" "$desktop" "$icon"; do
  [ -e "$f" ] || [ -L "$f" ] && fail "$f is still there"
done
echo "ok: uas $version installs, refreshes an old install, and uninstalls"
