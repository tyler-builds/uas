// Package gearlever reads and tidies what Gear Lever, the AppImage manager, keeps about the AppImages it
// integrated, so uas can take them over and leave nothing behind.
//
// Gear Lever moves an AppImage into its folder (~/AppImages unless changed), writes a launcher for it in the
// user's applications folder, saves its icon as <folder>/.icons/<file name without .appimage>, and records it
// in gearlever.conf as a section:
//
//	[app.<md5>]
//	name = Helium
//	file_path = /home/me/AppImages/helium.appimage
//
//	[app.<md5>.update_manager]
//
// When the AppImage goes away without Gear Lever (moved into uas, say), the section and the icon stay.
package gearlever

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Name is the tool's name, for people.
const Name = "Gear Lever"

// ConfPaths are where gearlever.conf can be: in the Flatpak's folder, and in the user's config folder for a
// copy installed another way.
func ConfPaths(home string) []string {
	return []string{
		filepath.Join(home, ".var", "app", "it.mijorus.gearlever", "config", "gearlever.conf"),
		filepath.Join(home, ".config", "gearlever.conf"),
	}
}

// Entry is one AppImage Gear Lever has on record.
type Entry struct {
	Conf    string // the gearlever.conf it is in
	Section string // "app.<md5>"
	Name    string
	Path    string // the AppImage, as an absolute path
}

// Gone reports whether the entry's AppImage is no longer there.
func (e Entry) Gone() bool {
	_, err := os.Stat(e.Path)
	return os.IsNotExist(err)
}

// Icon is where Gear Lever keeps the entry's icon.
func (e Entry) Icon() string { return IconOf(e.Path) }

// IconOf is where Gear Lever keeps the icon of an AppImage in its folder.
func IconOf(appImage string) string {
	base := filepath.Base(appImage)
	if ext := filepath.Ext(base); strings.EqualFold(ext, ".appimage") {
		base = strings.TrimSuffix(base, ext)
	}
	return filepath.Join(filepath.Dir(appImage), ".icons", base)
}

var appSection = regexp.MustCompile(`^\[(app\.[0-9A-Za-z]+)(\.[A-Za-z0-9_]+)?\]\s*$`)

// Entries reads the AppImages on record in the gearlever.conf files under home. A missing file has none.
func Entries(home string) []Entry {
	var out []Entry
	for _, conf := range ConfPaths(home) {
		raw, err := os.ReadFile(conf)
		if err != nil || len(raw) > 4<<20 {
			continue
		}
		var cur *Entry
		flush := func() {
			if cur != nil && filepath.IsAbs(cur.Path) {
				out = append(out, *cur)
			}
			cur = nil
		}
		sc := bufio.NewScanner(bytes.NewReader(raw))
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(strings.TrimSpace(line), "[") {
				flush()
				if m := appSection.FindStringSubmatch(strings.TrimSpace(line)); m != nil && m[2] == "" {
					cur = &Entry{Conf: conf, Section: m[1]}
				}
				continue
			}
			if cur == nil {
				continue
			}
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			switch strings.TrimSpace(k) {
			case "name":
				cur.Name = strings.TrimSpace(v)
			case "file_path":
				cur.Path = expand(strings.TrimSpace(v), home)
			}
		}
		flush()
	}
	return out
}

func expand(p, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	return p
}

// Forget removes entries from their gearlever.conf (each one's section and its sub-sections, such as
// update_manager) and deletes their icons. It leaves everything else in the file as it was. Removing an
// entry whose AppImage is still there would make Gear Lever lose track of it, so callers pass only entries
// whose AppImage has moved or gone.
func Forget(entries ...Entry) error {
	byConf := map[string]map[string]bool{}
	for _, e := range entries {
		if byConf[e.Conf] == nil {
			byConf[e.Conf] = map[string]bool{}
		}
		byConf[e.Conf][e.Section] = true
	}
	for conf, sections := range byConf {
		raw, err := os.ReadFile(conf)
		if err != nil {
			return err
		}
		var out []string
		drop := false
		for _, line := range strings.Split(string(raw), "\n") {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "[") {
				m := appSection.FindStringSubmatch(t)
				drop = m != nil && sections[m[1]]
			}
			if !drop {
				out = append(out, line)
			}
		}
		// Written beside the file and renamed over it, so Gear Lever never reads half of one.
		tmp := conf + ".uas-tmp"
		st, err := os.Stat(conf)
		if err != nil {
			return err
		}
		if err := os.WriteFile(tmp, []byte(strings.Join(out, "\n")), st.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Rename(tmp, conf); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	for _, e := range entries {
		removeIcon(e.Icon())
	}
	return nil
}

// removeIcon deletes a Gear Lever icon: a regular file directly in a folder named .icons.
func removeIcon(path string) {
	if filepath.Base(filepath.Dir(path)) != ".icons" {
		return
	}
	if st, err := os.Lstat(path); err == nil && st.Mode().IsRegular() {
		os.Remove(path)
	}
}
