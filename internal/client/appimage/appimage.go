// Package appimage reads the desktop entry, icon and AppStream metainfo out of an AppImage without running it.
//
// A type 2 AppImage is a small ELF runtime followed by a squashfs image of the app. The image starts where
// the ELF file ends (after its section header table), and holds the app's .desktop file and icon at its root.
package appimage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/CalebQ42/squashfs"
)

// Info is what Inspect found.
type Info struct {
	// Desktop holds the unlocalized keys of the [Desktop Entry] group, such as "Name" and "Exec".
	Desktop map[string]string
	// DesktopName is the desktop entry's file name, such as "org.example.App.desktop".
	DesktopName string
	Icon        []byte
	IconExt     string // ".png" or ".svg"
	// Metainfo is the app's AppStream metainfo file (usr/share/metainfo), if it ships one.
	Metainfo []byte
	// ElectronUpdate holds the keys of resources/app-update.yml, where apps built with electron-builder say
	// where their updates come from.
	ElectronUpdate map[string]string
}

const maxIcon = 4 << 20
const maxDesktop = 256 << 10
const maxMetainfo = 1 << 20

// Inspect reads an AppImage file. It never executes it.
func Inspect(name string) (*Info, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return InspectAt(f)
}

// InspectAt reads an AppImage from r, which need not be a local file: the indexer reads AppImages on GitHub a
// few ranges at a time.
func InspectAt(r io.ReaderAt) (*Info, error) {
	off, err := squashfsOffset(r)
	if err != nil {
		return nil, err
	}
	sq, err := squashfs.NewReaderAtOffset(r, off)
	if err != nil {
		return nil, fmt.Errorf("appimage: %w", err)
	}
	return fromFS(sqfs{sq.FS})
}

// squashfsOffset returns where the squashfs image starts: the end of the ELF section header table.
func squashfsOffset(r io.ReaderAt) (int64, error) {
	var h [64]byte
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return 0, fmt.Errorf("appimage: read ELF header: %w", err)
	}
	if !bytes.Equal(h[:4], []byte("\x7fELF")) {
		return 0, errors.New("appimage: not an ELF file")
	}
	if h[4] != 2 { // ELFCLASS64
		return 0, errors.New("appimage: only 64-bit AppImages are supported")
	}
	var bo binary.ByteOrder = binary.LittleEndian
	if h[5] == 2 {
		bo = binary.BigEndian
	}
	shoff := bo.Uint64(h[0x28:])
	shentsize := uint64(bo.Uint16(h[0x3a:]))
	shnum := uint64(bo.Uint16(h[0x3c:]))
	end := shoff + shentsize*shnum
	if end == 0 || end > 1<<32 {
		return 0, errors.New("appimage: bad ELF section header table")
	}
	var magic [4]byte
	if _, err := r.ReadAt(magic[:], int64(end)); err != nil || string(magic[:]) != "hsqs" {
		return 0, errors.New("appimage: no squashfs image after the ELF runtime (type 1 AppImages are not supported)")
	}
	return int64(end), nil
}

// linkFS is a filesystem that can say where a symlink points.
type linkFS interface {
	fs.FS
	ReadLink(name string) (string, error)
}

var errNotLink = errors.New("not a symlink")

// sqfs adds ReadLink to the squashfs filesystem.
type sqfs struct{ squashfs.FS }

func (s sqfs) ReadLink(name string) (string, error) {
	f, err := s.FS.OpenFile(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if !f.IsSymlink() {
		return "", errNotLink
	}
	return f.SymlinkPath(), nil
}

// fromFS finds the desktop entry and icon at the root of an AppImage's filesystem.
func fromFS(fsys fs.FS) (*Info, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("appimage: %w", err)
	}
	var desktops []map[string]string
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".desktop") {
			b, err := readFile(fsys, e.Name(), maxDesktop)
			if err != nil {
				return nil, fmt.Errorf("appimage: %s: %w", e.Name(), err)
			}
			desktops = append(desktops, parseDesktop(string(b)))
			names = append(names, e.Name())
		}
	}
	if len(desktops) == 0 {
		return nil, errors.New("appimage: no .desktop file at the image root")
	}
	pick := pickDesktop(fsys, names, desktops)
	info := &Info{Desktop: desktops[pick], DesktopName: names[pick]}

	var candidates []string
	if icon := info.Desktop["Icon"]; icon != "" && !strings.ContainsAny(icon, "/\\") {
		candidates = append(candidates, icon+".svg", icon+".png", icon)
	}
	candidates = append(candidates, ".DirIcon")
	for _, c := range candidates {
		b, err := readFile(fsys, c, maxIcon)
		if err != nil {
			continue
		}
		if ext := iconExt(b); ext != "" {
			info.Icon, info.IconExt = b, ext
			break
		}
	}
	info.Metainfo = findMetainfo(fsys, names[pick])
	if b, err := readFile(fsys, "resources/app-update.yml", maxDesktop); err == nil {
		info.ElectronUpdate = parseYAMLKeys(string(b))
	}
	return info, nil
}

// pickDesktop chooses the app's own desktop entry when the image root has several (some AppImages also carry
// those of libraries they bundle): the one whose icon is the image's .DirIcon, else the first that isn't a
// library's (lib*.desktop), else the first.
func pickDesktop(fsys fs.FS, names []string, desktops []map[string]string) int {
	if len(desktops) == 1 {
		return 0
	}
	if lfs, ok := fsys.(linkFS); ok {
		if target, err := lfs.ReadLink(".DirIcon"); err == nil {
			icon := strings.TrimSuffix(path.Base(target), path.Ext(target))
			for i, d := range desktops {
				if d["Icon"] == icon {
					return i
				}
			}
		}
	}
	for i, n := range names {
		if !strings.HasPrefix(strings.ToLower(n), "lib") {
			return i
		}
	}
	return 0
}

// findMetainfo returns the AppStream metainfo of the app whose desktop entry is desktop, or nil. AppImages
// often bundle the metainfo of libraries they carry, so a file counts only if it is named after the desktop
// entry or names it as its launchable.
func findMetainfo(fsys fs.FS, desktop string) []byte {
	base := strings.ToLower(strings.TrimSuffix(desktop, ".desktop"))
	for _, dir := range []string{"usr/share/metainfo", "usr/share/appdata"} {
		entries, err := fs.ReadDir(fsys, dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			id, ok := strings.CutSuffix(n, ".metainfo.xml")
			if !ok {
				id, ok = strings.CutSuffix(n, ".appdata.xml")
			}
			if !ok {
				continue
			}
			b, err := readFile(fsys, dir+"/"+n, maxMetainfo)
			if err != nil {
				continue
			}
			if strings.EqualFold(id, base) || bytes.Contains(b, []byte(">"+desktop+"<")) {
				return b
			}
		}
	}
	return nil
}

// readFile reads a file, following up to five relative symlinks that stay inside the image.
func readFile(fsys fs.FS, name string, limit int64) ([]byte, error) {
	for hop := 0; hop < 5; hop++ {
		if lfs, ok := fsys.(linkFS); ok {
			target, err := lfs.ReadLink(name)
			if err == nil {
				if path.IsAbs(target) {
					return nil, fmt.Errorf("symlink %s points outside the image", name)
				}
				name = path.Clean(path.Join(path.Dir(name), target))
				if !fs.ValidPath(name) {
					return nil, fmt.Errorf("symlink %s points outside the image", name)
				}
				continue
			}
			// Any error means "not a symlink" here; opening the file reports real problems.
		}
		f, err := fsys.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(b)) > limit {
			return nil, fmt.Errorf("%s is too large", name)
		}
		return b, nil
	}
	return nil, fmt.Errorf("too many symlinks at %s", name)
}

func iconExt(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.Contains(b[:min(len(b), 512)], []byte("<svg")):
		return ".svg"
	}
	return ""
}

// parseDesktop reads the unlocalized keys of the [Desktop Entry] group.
func parseDesktop(s string) map[string]string {
	vals := map[string]string{}
	inEntry := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inEntry = line == "[Desktop Entry]"
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		k = strings.TrimSpace(k)
		if !inEntry || !ok || strings.Contains(k, "[") {
			continue
		}
		if _, dup := vals[k]; !dup {
			vals[k] = strings.TrimSpace(v)
		}
	}
	return vals
}
