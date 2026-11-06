#!/usr/bin/env bash
# Installs uas for this user, from the folder this script is in:
#   ~/.local/bin/uas and uas-gui, the launcher in ~/.local/share/applications, and the icon.
# uas then updates itself (Settings > About, or "uas self-update").
#
#   ./install.sh            install or update
#   ./install.sh --remove   remove uas (apps you installed with it stay)
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
prefix=${PREFIX:-$HOME/.local}
bin="$prefix/bin"
apps="${XDG_DATA_HOME:-$HOME/.local/share}/applications"
icons="${XDG_DATA_HOME:-$HOME/.local/share}/icons/hicolor/scalable/apps"
desktop=io.github.tyler_builds.uas.desktop

if [[ "${1:-}" == "--remove" ]]; then
  rm -f "$bin/uas" "$bin/uas-gui" "$apps/$desktop" "$icons/io.github.tyler_builds.uas.svg"
  systemctl --user disable --now uas-update-check.timer 2>/dev/null || true
  rm -f "${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/uas-update-check."{timer,service}
  echo "uas removed. Its settings and catalog are in ~/.config/uas and ~/.cache/uas."
  exit 0
fi

mkdir -p "$bin" "$apps" "$icons"
install -m 755 "$here/uas" "$bin/uas"
install -m 755 "$here/uas-gui" "$bin/uas-gui"
# The launcher runs uas-gui by its full path, so it works whether or not ~/.local/bin is on the PATH.
sed "s|^Exec=uas-gui|Exec=$bin/uas-gui|" "$here/share/applications/$desktop" > "$apps/$desktop"
install -m 644 "$here/share/icons/hicolor/scalable/apps/io.github.tyler_builds.uas.svg" "$icons/"
update-desktop-database -q "$apps" 2>/dev/null || true
echo "uas $("$bin/uas" version | cut -d' ' -f2) installed in $bin. Open it from your app menu."
case ":$PATH:" in
  *":$bin:"*) ;;
  *) echo "Add $bin to your PATH to use the uas command." ;;
esac
